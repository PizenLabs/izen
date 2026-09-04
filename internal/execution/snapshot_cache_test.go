package execution

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/core/authorization"
)

// ── Mutation Cache Invalidation Tests ─────────────────────────────────────
//
// These tests verify that the observeSnapshot cache is invalidated/updated
// immediately upon a successful file write, preventing stale-read conditions
// where post-mutation verification reads pre-mutation bytes from memory.

func TestSnapshotCache_InvalidatedAfterApply(t *testing.T) {
	dir := t.TempDir()
	target := "index.html"
	original := "<html><body><p>original</p></body></html>"
	modified := "<html><body><p>modified</p></body></html>"

	// Create the target file.
	if err := os.WriteFile(filepath.Join(dir, target), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a RuntimeExecutor (we only need the snapshot cache + patch manager).
	x := NewRuntimeExecutor(dir, nil, nil, nil, "")

	// Populate the snapshot cache (simulating observeTargets).
	x.observeTargets([]string{target})

	// Verify the cache has the original content.
	if data, ok := x.getSnapshotContent(target); !ok || string(data) != original {
		t.Fatalf("precondition: cache should have original content, got ok=%v data=%q", ok, data)
	}

	// Apply a patch through the PatchManager (which triggers onMutation).
	pm := NewPatchManager(dir)
	pm.SetAuthorization(&authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(1 * time.Hour),
	})
	pm.SetOnMutation(func(t string, written []byte) {
		x.invalidateSnapshot(t)
	})

	patch := &Patch{
		ID:       "test-patch-1",
		File:     target,
		Original: original,
		Modified: modified,
	}
	if err := pm.Apply(patch); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	// ── ASSERT: cache now has the NEW content, not the stale original. ──
	data, ok := x.getSnapshotContent(target)
	if !ok {
		t.Fatal("cache should still have an entry for the target after mutation")
	}
	if string(data) != modified {
		t.Fatalf("stale snapshot! cache has %q, expected %q (the freshly written content)", string(data), modified)
	}
}

func TestSnapshotCache_UpdatedAfterFileCreate(t *testing.T) {
	dir := t.TempDir()
	target := "newfile.txt"
	content := "line 1\nline 2\nline 3\nline 4"

	// Create a RuntimeExecutor.
	x := NewRuntimeExecutor(dir, nil, nil, nil, "")

	// Populate the snapshot cache with some other file.
	x.observeTargets([]string{"other.txt"})
	_ = os.WriteFile(filepath.Join(dir, "other.txt"), []byte("other"), 0o644)

	// Apply a FILE_CREATE patch.
	pm := NewPatchManager(dir)
	pm.SetAuthorization(&authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(1 * time.Hour),
	})
	pm.SetOnMutation(func(t string, written []byte) {
		x.invalidateSnapshot(t)
	})

	patch := &Patch{
		ID:       "test-patch-2",
		File:     target,
		Original: "",
		Modified: "<<<<<<< FILE_CREATE: " + target + "\n" + content + "\n>>>>>>> END_FILE",
	}
	if err := pm.Apply(patch); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	// ── ASSERT: cache now has the newly created file's content. ──
	data, ok := x.getSnapshotContent(target)
	if !ok {
		t.Fatal("cache should have an entry for the newly created file")
	}
	if string(data) != content {
		t.Fatalf("cache has %q, expected %q", string(data), content)
	}
}

func TestSnapshotCache_InvalidationIsExplicit(t *testing.T) {
	dir := t.TempDir()
	target := "data.json"
	original := `{"key": "old"}`

	if err := os.WriteFile(filepath.Join(dir, target), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	x := NewRuntimeExecutor(dir, nil, nil, nil, "")
	x.observeTargets([]string{target})

	// Verify original is cached.
	if data, ok := x.getSnapshotContent(target); !ok || string(data) != original {
		t.Fatalf("precondition failed: cache should have original")
	}

	// Explicit invalidation purges the cache keys (key erasure).
	x.invalidateSnapshot(target)

	// ── ASSERT: every path alias is purged from the cache map. ──
	x.observeSnapshotMu.RLock()
	purged := func() bool {
		_, rel := x.observeSnapshot[target]
		_, base := x.observeSnapshot[filepath.Base(target)]
		_, abs := x.observeSnapshot[filepath.Join(dir, target)]
		return !rel && !base && !abs
	}()
	x.observeSnapshotMu.RUnlock()
	if !purged {
		t.Fatal("cache keys should be purged after invalidation (key erasure, not value update)")
	}

	// ── ASSERT: the next observation pulls through disk truth. ──
	// Rewrite the file on disk to a NEW value AFTER invalidation; the
	// pull-through must observe the new disk state, never the stale cache.
	updated := `{"key": "new"}`
	if err := os.WriteFile(filepath.Join(dir, target), []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
	data, ok := x.getSnapshotContent(target)
	if !ok {
		t.Fatal("pull-through: cache should be re-populated from disk after invalidation")
	}
	if string(data) != updated {
		t.Fatalf("stale pull-through: got %q, want %q (fresh disk read)", string(data), updated)
	}
}

func TestSnapshotCache_NoStaleReadAfterMutation(t *testing.T) {
	// This is the core invariant: post-mutation reads must see the NEW state.
	dir := t.TempDir()
	target := "main.go"
	original := "package main\n\nfunc main() {}\n"
	modified := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"

	if err := os.WriteFile(filepath.Join(dir, target), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	x := NewRuntimeExecutor(dir, nil, nil, nil, "")
	x.observeTargets([]string{target})

	// Wire the cache invalidation hook.
	pm := NewPatchManager(dir)
	pm.SetAuthorization(&authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(1 * time.Hour),
	})
	pm.SetOnMutation(func(t string, written []byte) {
		x.invalidateSnapshot(t)
	})

	// Apply the patch.
	patch := &Patch{
		ID:       "test-patch-3",
		File:     target,
		Original: original,
		Modified: modified,
	}
	if err := pm.Apply(patch); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}

	// Simulate post-mutation verification reading from the cache.
	// It MUST see the modified content, not the stale original.
	cachedData, ok := x.getSnapshotContent(target)
	if !ok {
		t.Fatal("post-mutation: cache should have the target")
	}
	if string(cachedData) == original {
		t.Fatal("STALE READ: cache still has pre-mutation content after apply")
	}
	if string(cachedData) != modified {
		t.Fatalf("post-mutation: cache has %q, expected %q", string(cachedData), modified)
	}

	// Also verify the actual disk state matches.
	diskData, err := os.ReadFile(filepath.Join(dir, target))
	if err != nil {
		t.Fatalf("read disk: %v", err)
	}
	if string(diskData) != modified {
		t.Fatalf("disk has %q, expected %q", string(diskData), modified)
	}
	if string(cachedData) != string(diskData) {
		t.Fatalf("cache/disk divergence: cache=%q disk=%q", string(cachedData), string(diskData))
	}
}
