package lock

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestDisjointArtifactsProceedInParallel proves Disjoint Session Parallelism:
// 10 holders on disjoint targets acquire concurrently with zero contention.
func TestDisjointArtifactsProceedInParallel(t *testing.T) {
	dir := t.TempDir()
	const n = 10
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			target := fmt.Sprintf("disjoint/file-%d.txt", i)
			unlock, err := TryAcquireArtifactLock(dir, target)
			if err != nil {
				errCh <- fmt.Errorf("disjoint target %q must not contend: %w", target, err)
				return
			}
			defer unlock()
			// Simulate a metadata write under the artifact lock.
			if err := WriteMetadataAtomic(dir, fmt.Sprintf("meta-%d.json", i), []byte(`{"i":1}`), 0o644); err != nil {
				if errors.Is(err, ErrConcurrentModification) {
					errCh <- fmt.Errorf("disjoint metadata %d spuriously conflicted: %w", i, err)
					return
				}
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

// TestSameArtifactSecondHolderFailsClosed proves Explicit Artifact Conflict
// Handling at the lock layer: the second contender fails with
// ErrConcurrentModification, never blocks, never overwrites.
func TestSameArtifactSecondHolderFailsClosed(t *testing.T) {
	dir := t.TempDir()
	unlock, err := TryAcquireArtifactLock(dir, "shared/artifact.txt")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer unlock()

	_, err = TryAcquireArtifactLock(dir, "shared/artifact.txt")
	if err == nil {
		t.Fatal("second acquire on same artifact must fail")
	}
	if !errors.Is(err, ErrConcurrentModification) {
		t.Fatalf("second acquire = %v, want ErrConcurrentModification", err)
	}

	// Multi-target acquisition overlapping on one file must also fail closed
	// and release everything it already held.
	_, err = TryAcquireArtifactLocks(dir, []string{"other/a.txt", "shared/artifact.txt"})
	if err == nil {
		t.Fatal("overlapping multi-acquire must fail")
	}
	if !errors.Is(err, ErrConcurrentModification) {
		t.Fatalf("overlapping acquire = %v, want ErrConcurrentModification", err)
	}

	// After release the artifact is acquirable again (no leaked FD).
	unlock()
	unlock2, err := TryAcquireArtifactLock(dir, "shared/artifact.txt")
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	unlock2()
}

// TestMetadataLockStress hammers .izen/metadata with high-frequency
// concurrent atomic writes across 10 goroutines and asserts zero JSON
// truncation or state corruption: every document must decode cleanly.
func TestMetadataLockStress(t *testing.T) {
	dir := t.TempDir()
	const writers = 10
	const writesPerWriter = 25
	var wg sync.WaitGroup
	errCh := make(chan error, writers*writesPerWriter)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < writesPerWriter; i++ {
				doc, _ := json.Marshal(map[string]any{
					"writer": w, "seq": i,
					"payload": string(make([]byte, 512)),
				})
				name := fmt.Sprintf("stress-%d.json", w)
				if err := WriteMetadataAtomic(dir, name, doc, 0o644); err != nil {
					// Under contention the metadata flock may report a
					// conflict; retry once after a backoff — the invariant
					// is zero CORRUPTION, not zero contention.
					if errors.Is(err, ErrConcurrentModification) {
						if rerr := WriteMetadataAtomic(dir, name, doc, 0o644); rerr != nil && !errors.Is(rerr, ErrConcurrentModification) {
							errCh <- rerr
						}
						continue
					}
					errCh <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	// Every surviving document must be valid JSON (zero truncation).
	for w := 0; w < writers; w++ {
		name := fmt.Sprintf("stress-%d.json", w)
		data, err := ReadMetadata(dir, name)
		if err != nil {
			t.Errorf("read %s: %v", name, err)
			continue
		}
		if len(data) == 0 {
			t.Errorf("%s is empty", name)
			continue
		}
		var v map[string]any
		if err := json.Unmarshal(data, &v); err != nil {
			t.Errorf("%s is truncated/corrupt: %v", name, err)
		}
	}
}

// TestOCCBaselineDivergenceFailsClosed proves the OCC layer: a file changed
// after the baseline was captured aborts with ErrConcurrentModification.
func TestOCCBaselineDivergenceFailsClosed(t *testing.T) {
	dir := t.TempDir()
	target := "occ/file.txt"
	full := filepath.Join(dir, filepath.FromSlash(target))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	baseline := SnapshotBaseline(dir, []string{target})
	// Out-of-band writer strikes after the baseline.
	if err := os.WriteFile(full, []byte("v2-external"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBaselineUnderLock(dir, baseline); !errors.Is(err, ErrConcurrentModification) {
		t.Fatalf("diverged baseline verify = %v, want ErrConcurrentModification", err)
	}
	// Clean baseline verifies trivially.
	baseline2 := SnapshotBaseline(dir, []string{target})
	if err := VerifyBaselineUnderLock(dir, baseline2); err != nil {
		t.Fatalf("clean baseline must verify: %v", err)
	}
	// AdvanceOCC bumps the durable clock and records the hash.
	if err := AdvanceOCC(dir, target, liveTargetHash(dir, target)); err != nil {
		t.Fatalf("AdvanceOCC: %v", err)
	}
	if got := OCCVersion(dir, target); got != 1 {
		t.Fatalf("OCCVersion = %d, want 1", got)
	}
	if err := AdvanceOCC(dir, target, liveTargetHash(dir, target)); err != nil {
		t.Fatalf("AdvanceOCC #2: %v", err)
	}
	if got := OCCVersion(dir, target); got != 2 {
		t.Fatalf("OCCVersion = %d, want 2", got)
	}
}

// TestCheckpointLockSerializesCrossProcess proves the checkpoint metadata
// flock: a held checkpoint lock makes a second WithCheckpointLock fail with
// ErrConcurrentModification instead of interleaving manifests.
func TestCheckpointLockSerializesCrossProcess(t *testing.T) {
	dir := t.TempDir()
	// Hold the checkpoint lock file directly (simulating a peer process).
	lockPath := filepath.Join(dir, ".izen", "locks", "checkpoint-store.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	holder, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close() }()
	if err := flockExclusive(holder); err != nil {
		t.Skipf("platform without flock: %v", err)
	}
	defer func() { _ = flockRelease(holder) }()

	err = WithCheckpointLock(dir, func() error { return nil })
	if !errors.Is(err, ErrConcurrentModification) {
		t.Fatalf("contended checkpoint lock = %v, want ErrConcurrentModification", err)
	}
}
