package execution

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPatchManagerApply_RefusesSymlinkEscapeThroughTheKernel is the functional
// proof that the canonical RuntimeExecutor mutation seam now crosses the Runtime
// Kernel.
//
// Before this change, PatchManager.apply placed bytes with a bare os.WriteFile.
// That call follows a symlink wherever it points, so a target whose parent is a
// symlink out of the workspace wrote outside the grant with no event, no
// evidence and no verification. The kernel's filesystem capability resolves the
// real path and refuses the escape. This test therefore observes a behaviour the
// legacy write could not produce: the mutation is refused and the file beyond
// the boundary is byte-for-byte unchanged.
//
// It is a behaviour assertion rather than a spy on an internal call precisely
// because a spy could be satisfied by a seam that was wired up and then not used.
func TestPatchManagerApply_RefusesSymlinkEscapeThroughTheKernel(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{root, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	victim := filepath.Join(outside, "escaped.txt")
	if err := os.WriteFile(victim, []byte("before"), 0o644); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}

	pm := NewPatchManager(root)
	pm.SetAuthorization(testAuth())

	patch := &Patch{
		ID:       "kernel-seam-1",
		File:     "link/escaped.txt",
		Original: "before",
		Modified: "after",
	}

	err := pm.ApplyContext(context.Background(), patch)
	if err == nil {
		t.Fatal("a write whose target resolves outside the workspace succeeded; the kernel seam was bypassed")
	}
	if !strings.Contains(err.Error(), "outside the workspace root") {
		t.Errorf("error = %v; want the kernel's confinement refusal to name the boundary", err)
	}

	body, readErr := os.ReadFile(victim)
	if readErr != nil {
		t.Fatalf("reading the outside file: %v", readErr)
	}
	if string(body) != "before" {
		t.Fatalf("the workspace boundary was crossed: outside file = %q, want %q", string(body), "before")
	}
}

// TestPatchManagerApply_StillWritesConfinedTargets proves the seam did not
// simply refuse everything. A confined target must still be written and its
// bytes must match the resolved patch content.
func TestPatchManagerApply_StillWritesConfinedTargets(t *testing.T) {
	dir := t.TempDir()
	pm := NewPatchManager(dir)
	pm.SetAuthorization(testAuth())

	target := filepath.Join("sub", "file.txt")
	fullPath := filepath.Join(dir, target)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(fullPath, []byte("original content"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	patch := &Patch{
		ID:       "kernel-seam-2",
		File:     target,
		Original: "original content",
		Modified: "patched content",
	}
	if err := pm.ApplyContext(context.Background(), patch); err != nil {
		t.Fatalf("ApplyContext: %v", err)
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "patched content" {
		t.Fatalf("content = %q; want %q", string(data), "patched content")
	}
}
