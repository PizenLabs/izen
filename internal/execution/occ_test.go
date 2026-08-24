package execution

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// occWriteTarget writes a workspace-relative file inside root (test helper).
// It creates parent directories, unlike the flat executor_test helper.
func occWriteTarget(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSnapshotBaselineIsTargetScoped proves baseline capture is bounded to the
// declared target geometry: files outside the target set never enter the
// snapshot, no matter how many exist in the workspace.
func TestSnapshotBaselineIsTargetScoped(t *testing.T) {
	root := t.TempDir()
	occWriteTarget(t, root, "src/a.go", "package a\n")
	occWriteTarget(t, root, "src/b.go", "package b\n")
	for i := 0; i < 50; i++ {
		occWriteTarget(t, root, "noise/file"+string(rune('a'+i%26))+time.Now().Format("150405")+"-"+time.Now().Format(".000")+".txt", "noise")
	}
	occWriteTarget(t, root, "other/unrelated.md", "# unrelated\n")

	v := NewOCCVerifier(root)
	b := v.SnapshotBaseline([]string{"src/a.go", "src/b.go"})
	if b == nil {
		t.Fatal("nil baseline")
	}
	got := b.Targets()
	if len(got) != 2 || got[0] != "src/a.go" || got[1] != "src/b.go" {
		t.Fatalf("baseline captured %v, want exactly the declared targets", got)
	}
	if b.Digest() == "" || b.CreatedAt().IsZero() {
		t.Fatal("baseline must carry a digest and a creation timestamp")
	}
}

// TestSnapshotBaselineDeduplicatesAndCleansTargets pins the normalization rule:
// duplicates collapse and paths are slash-cleaned deterministically.
func TestSnapshotBaselineDeduplicatesAndCleansTargets(t *testing.T) {
	root := t.TempDir()
	occWriteTarget(t, root, "a.txt", "x\n")
	v := NewOCCVerifier(root)
	b := v.SnapshotBaseline([]string{"a.txt", "./a.txt", "a.txt", "", "."})
	if got := b.Targets(); len(got) != 1 || got[0] != "a.txt" {
		t.Fatalf("targets = %v, want [a.txt]", got)
	}
}

// TestVerifyAgainstCleanWorkspacePasses is the positive control: an untouched
// workspace verifies clean.
func TestVerifyAgainstCleanWorkspacePasses(t *testing.T) {
	root := t.TempDir()
	occWriteTarget(t, root, "a.txt", "alpha\n")
	occWriteTarget(t, root, "sub/b.txt", "beta\n")
	v := NewOCCVerifier(root)
	b := v.SnapshotBaseline([]string{"a.txt", "sub/b.txt"})
	if err := v.VerifyAgainst(b); err != nil {
		t.Fatalf("clean workspace reported conflict: %v", err)
	}
}

// TestVerifyAgainstCreationIntentStaysClean proves an absent-at-baseline
// target that REMAINS absent verifies cleanly (creation intents are protected,
// not rejected).
func TestVerifyAgainstCreationIntentStaysClean(t *testing.T) {
	root := t.TempDir()
	v := NewOCCVerifier(root)
	b := v.SnapshotBaseline([]string{"new/file.txt"})
	if err := v.VerifyAgainst(b); err != nil {
		t.Fatalf("creation intent flagged as conflict: %v", err)
	}
}

// TestVerifyAgainstDetectsModifiedTarget drives the core OCC guarantee: any
// out-of-band content change between baseline and verify is a conflict that
// wraps ErrWorkspaceStateConflict and names the diverged path.
func TestVerifyAgainstDetectsModifiedTarget(t *testing.T) {
	root := t.TempDir()
	occWriteTarget(t, root, "a.txt", "before\n")
	v := NewOCCVerifier(root)
	b := v.SnapshotBaseline([]string{"a.txt"})

	occWriteTarget(t, root, "a.txt", "out-of-band edit\n")
	err := v.VerifyAgainst(b)
	if err == nil {
		t.Fatal("modified target verified clean — OCC gate broken")
	}
	if !errors.Is(err, ErrWorkspaceStateConflict) {
		t.Fatalf("conflict does not wrap the sentinel: %v", err)
	}
	var wsErr *WorkspaceStateConflict
	if !errors.As(err, &wsErr) || len(wsErr.Conflicts) != 1 {
		t.Fatalf("expected exactly one conflict, got %v", err)
	}
	if c := wsErr.Conflicts[0]; c.Path != "a.txt" || c.Kind != OCCModified {
		t.Fatalf("conflict = %+v, want a.txt/modified", c)
	}
}

// TestVerifyAgainstDetectsDeletedAndCreatedTargets covers the absence flips in
// both directions: a baselined file deleted out-of-band, and a creation target
// that appeared before commit.
func TestVerifyAgainstDetectsDeletedAndCreatedTargets(t *testing.T) {
	t.Run("deleted", func(t *testing.T) {
		root := t.TempDir()
		occWriteTarget(t, root, "gone.txt", "data\n")
		v := NewOCCVerifier(root)
		b := v.SnapshotBaseline([]string{"gone.txt"})
		if err := os.Remove(filepath.Join(root, "gone.txt")); err != nil {
			t.Fatal(err)
		}
		err := v.VerifyAgainst(b)
		if !errors.Is(err, ErrWorkspaceStateConflict) {
			t.Fatalf("deletion not detected: %v", err)
		}
		var wsErr *WorkspaceStateConflict
		_ = errors.As(err, &wsErr)
		if len(wsErr.Conflicts) != 1 || wsErr.Conflicts[0].Kind != OCCDeleted {
			t.Fatalf("conflict = %+v, want deleted", wsErr.Conflicts)
		}
	})
	t.Run("created", func(t *testing.T) {
		root := t.TempDir()
		v := NewOCCVerifier(root)
		b := v.SnapshotBaseline([]string{"fresh.txt"})
		occWriteTarget(t, root, "fresh.txt", "appeared\n")
		err := v.VerifyAgainst(b)
		if !errors.Is(err, ErrWorkspaceStateConflict) {
			t.Fatalf("appearance not detected: %v", err)
		}
		var wsErr *WorkspaceStateConflict
		_ = errors.As(err, &wsErr)
		if len(wsErr.Conflicts) != 1 || wsErr.Conflicts[0].Kind != OCCCreated {
			t.Fatalf("conflict = %+v, want created", wsErr.Conflicts)
		}
	})
}

// TestVerifyAgainstAggregatesEveryConflict proves a multi-file verification
// reports its COMPLETE conflict surface, never just the first divergence.
func TestVerifyAgainstAggregatesEveryConflict(t *testing.T) {
	root := t.TempDir()
	occWriteTarget(t, root, "a.txt", "a\n")
	occWriteTarget(t, root, "b.txt", "b\n")
	occWriteTarget(t, root, "c.txt", "c\n")
	v := NewOCCVerifier(root)
	b := v.SnapshotBaseline([]string{"a.txt", "b.txt", "c.txt"})

	occWriteTarget(t, root, "a.txt", "a changed\n")
	if err := os.Remove(filepath.Join(root, "b.txt")); err != nil {
		t.Fatal(err)
	}

	err := v.VerifyAgainst(b)
	var wsErr *WorkspaceStateConflict
	if !errors.As(err, &wsErr) || len(wsErr.Conflicts) != 2 {
		t.Fatalf("want 2 aggregated conflicts, got %v", err)
	}
	kinds := map[string]OCCConflictKind{}
	for _, c := range wsErr.Conflicts {
		kinds[c.Path] = c.Kind
	}
	if kinds["a.txt"] != OCCModified || kinds["b.txt"] != OCCDeleted {
		t.Fatalf("conflict taxonomy drifted: %+v", wsErr.Conflicts)
	}
	if !strings.Contains(err.Error(), "a.txt") || !strings.Contains(err.Error(), "b.txt") {
		t.Fatalf("error message lost conflict surface: %v", err)
	}
}

// TestVerifyAgainstTouchedSameContentIsClean proves the mtime fast path cannot
// produce false positives: a rewritten file with identical bytes verifies
// clean (the hash comparison settles it).
func TestVerifyAgainstTouchedSameContentIsClean(t *testing.T) {
	root := t.TempDir()
	occWriteTarget(t, root, "a.txt", "stable\n")
	v := NewOCCVerifier(root)
	b := v.SnapshotBaseline([]string{"a.txt"})

	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(filepath.Join(root, "a.txt"), future, future); err != nil {
		t.Skipf("cannot adjust mtime on this filesystem: %v", err)
	}
	if err := v.VerifyAgainst(b); err != nil {
		t.Fatalf("mtime-only touch flagged as conflict: %v", err)
	}
}

// TestFingerprintCacheHitsRecorded proves the operational cache-hit telemetry:
// unchanged targets short-circuit content re-reads on repeated observations.
func TestFingerprintCacheHitsRecorded(t *testing.T) {
	root := t.TempDir()
	occWriteTarget(t, root, "a.txt", "cached\n")
	v := NewOCCVerifier(root)

	before := v.Metrics()
	b := v.SnapshotBaseline([]string{"a.txt"}) // populates the hash cache
	_ = v.VerifyAgainst(b)                     // stat fast-path hit

	m := v.Metrics()
	if m.Snapshots != before.Snapshots+1 || m.Verifications != before.Verifications+1 {
		t.Fatalf("telemetry counters did not advance: %+v → %+v", before, m)
	}
	if m.CacheHits <= before.CacheHits {
		t.Fatalf("no cache hit recorded for unchanged target: %+v", m)
	}
	if m.Mismatches != before.Mismatches {
		t.Fatalf("clean verification counted as mismatch: %+v", m)
	}
	if m.SnapshotDuration() < 0 || m.VerifyDuration() < 0 {
		t.Fatalf("negative durations: %+v", m)
	}

	// A second verification of the same baseline hits the fast path again.
	if err := v.VerifyAgainst(b); err != nil {
		t.Fatal(err)
	}
	if v.Metrics().CacheHits <= m.CacheHits {
		t.Fatal("second clean verification recorded no cache hit")
	}
}

// TestMismatchTelemetryRecordsConflicts proves the mismatch-frequency metric.
func TestMismatchTelemetryRecordsConflicts(t *testing.T) {
	root := t.TempDir()
	occWriteTarget(t, root, "a.txt", "one\n")
	v := NewOCCVerifier(root)
	b := v.SnapshotBaseline([]string{"a.txt"})

	occWriteTarget(t, root, "a.txt", "two\n")
	if err := v.VerifyAgainst(b); err == nil {
		t.Fatal("expected conflict")
	}
	m := v.Metrics()
	if m.Mismatches != 1 || m.ConflictsFound != 1 {
		t.Fatalf("mismatch telemetry = %+v, want 1 mismatch / 1 conflict", m)
	}
}

// TestNilBaselineVerifiesTrivially documents the degenerate cases: no verifier
// or no admitted geometry protects nothing and passes through.
func TestNilBaselineVerifiesTrivially(t *testing.T) {
	var v *OCCVerifier
	if err := v.VerifyAgainst(nil); err != nil {
		t.Fatalf("nil verifier must be a no-op: %v", err)
	}
	real := NewOCCVerifier(t.TempDir())
	if err := real.VerifyAgainst(nil); err != nil {
		t.Fatalf("nil baseline must verify trivially: %v", err)
	}
	if err := real.VerifyAgainst(real.SnapshotBaseline(nil)); err != nil {
		t.Fatalf("empty baseline must verify trivially: %v", err)
	}
	if m := real.Metrics(); m.Verifications != 0 {
		t.Fatalf("trivial verifications must not count: %+v", m)
	}
}

// TestBaselineDigestTracksDivergence proves the baseline identity is
// content-addressed: identical states share a digest; any drift forks it.
func TestBaselineDigestTracksDivergence(t *testing.T) {
	root := t.TempDir()
	occWriteTarget(t, root, "a.txt", "same\n")
	v := NewOCCVerifier(root)
	b1 := v.SnapshotBaseline([]string{"a.txt"})
	b2 := v.SnapshotBaseline([]string{"a.txt"})
	if b1.Digest() != b2.Digest() {
		t.Fatal("identical states produced divergent baseline digests")
	}
	occWriteTarget(t, root, "a.txt", "changed\n")
	b3 := v.SnapshotBaseline([]string{"a.txt"})
	if b3.Digest() == b1.Digest() {
		t.Fatal("divergent state reused the baseline digest")
	}
}
