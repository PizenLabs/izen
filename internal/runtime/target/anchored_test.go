package target

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/runtime/scope"
)

// TestAnchoredResolveThenSwapFailsClosed is the TOCTOU race test at the
// resolution tier: a valid target is resolved, then swapped on disk with
// a symlink pointing outside the workspace root before use. The
// use-time verification must halt with ErrWorkspaceEscape.
func TestAnchoredResolveThenSwapFailsClosed(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	evil := filepath.Join(outside, "evil.txt")
	if err := os.WriteFile(evil, []byte("untouched"), 0o644); err != nil {
		t.Fatalf("write evil: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, "foo.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write foo: %v", err)
	}

	root, err := scope.Open(ws)
	if err != nil {
		t.Fatalf("scope.Open: %v", err)
	}
	defer func() { _ = root.Close() }()

	r := NewTargetResolver()
	ref, err := r.ResolveAnchored(ws, "foo.txt", root)
	if err != nil {
		t.Fatalf("ResolveAnchored: %v", err)
	}
	if ref.Canonical != "foo.txt" {
		t.Fatalf("Canonical = %q, want foo.txt", ref.Canonical)
	}

	// Adversary swaps the resolved file with an absolute symlink escape.
	if err := os.Remove(filepath.Join(ws, "foo.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.Symlink(evil, filepath.Join(ws, "foo.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if err := VerifyAtUse(root, ref); !errors.Is(err, ErrWorkspaceEscape) {
		t.Fatalf("VerifyAtUse = %v, want ErrWorkspaceEscape", err)
	}
	if err := VerifyAtUse(root, ref); !errors.Is(err, scope.ErrWorkspaceEscape) {
		t.Fatalf("VerifyAtUse = %v, want scope.ErrWorkspaceEscape", err)
	}

	// Relative escape variant.
	if err := os.Remove(filepath.Join(ws, "foo.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.Symlink(filepath.Join("..", "outside.txt"), filepath.Join(ws, "foo.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := VerifyAtUse(root, ref); !errors.Is(err, ErrWorkspaceEscape) {
		t.Fatalf("VerifyAtUse (relative) = %v, want ErrWorkspaceEscape", err)
	}

	// Outside file untouched.
	data, err := os.ReadFile(evil)
	if err != nil {
		t.Fatalf("read evil: %v", err)
	}
	if string(data) != "untouched" {
		t.Fatalf("outside file mutated: %q", data)
	}
}

// TestAnchoredInternalSymlinkPermitted pins the no-blanket-ban rule:
// internal symlinks resolving within the root verify cleanly at use time.
func TestAnchoredInternalSymlinkPermitted(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "real.txt"), []byte("inside"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink("real.txt", filepath.Join(ws, "foo.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	root, err := scope.Open(ws)
	if err != nil {
		t.Fatalf("scope.Open: %v", err)
	}
	defer func() { _ = root.Close() }()

	r := NewTargetResolver()
	ref, err := r.ResolveAnchored(ws, "foo.txt", root)
	if err != nil {
		t.Fatalf("ResolveAnchored: %v", err)
	}
	if err := VerifyAtUse(root, ref); err != nil {
		t.Fatalf("internal symlink must verify: %v", err)
	}
}
