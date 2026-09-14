package capabilities

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/domain/ports"
	"github.com/PizenLabs/izen/internal/runtime/scope"
)

// TestPatchAdapterTOCTOUSymlinkSwapFailsClosed is the adversarial race
// test at the patch-application tier: a valid target foo.txt is staged,
// then swapped on disk with a symlink pointing outside the workspace
// root immediately before PatchPort.Apply. Execution must halt with
// ErrWorkspaceEscape and no bytes may be written outside the boundary.
func TestPatchAdapterTOCTOUSymlinkSwapFailsClosed(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	evil := filepath.Join(outside, "evil.txt")
	if err := os.WriteFile(evil, []byte("untouched"), 0o644); err != nil {
		t.Fatalf("write evil: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, "foo.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write foo: %v", err)
	}

	root, err := scope.Open(ws)
	if err != nil {
		t.Fatalf("scope.Open: %v", err)
	}
	defer func() { _ = root.Close() }()
	adapter := NewPatchAdapterWithRoot(root)

	// Resolve the valid target (planning-time observation).
	current, err := os.ReadFile(filepath.Join(ws, "foo.txt"))
	if err != nil {
		t.Fatalf("read foo: %v", err)
	}
	if string(current) != "hello\n" {
		t.Fatalf("unexpected content: %q", current)
	}

	// Adversary swaps foo.txt with an absolute symlink escape before Apply.
	if err := os.Remove(filepath.Join(ws, "foo.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.Symlink(evil, filepath.Join(ws, "foo.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	patch := ports.PatchPayload{
		File:          "foo.txt",
		Modified:      "pwned\n",
		IsFullRewrite: true,
	}
	if _, err := adapter.Apply(context.Background(), patch); !errors.Is(err, ErrWorkspaceEscape) {
		t.Fatalf("Apply = %v, want ErrWorkspaceEscape", err)
	}

	// Relative escape variant must also fail closed.
	if err := os.Remove(filepath.Join(ws, "foo.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.Symlink(filepath.Join("..", "evil.txt"), filepath.Join(ws, "foo.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := adapter.Apply(context.Background(), patch); !errors.Is(err, scope.ErrWorkspaceEscape) {
		t.Fatalf("Apply (relative) = %v, want ErrWorkspaceEscape", err)
	}

	// No bytes written outside the boundary.
	data, err := os.ReadFile(evil)
	if err != nil {
		t.Fatalf("read evil: %v", err)
	}
	if string(data) != "untouched" {
		t.Fatalf("outside file mutated: %q", data)
	}
}

// TestPatchAdapterInternalSymlinkPermitted pins the no-blanket-ban rule:
// a whole-file rewrite through an internal symlink succeeds.
func TestPatchAdapterInternalSymlinkPermitted(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "real.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink("real.txt", filepath.Join(ws, "foo.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	adapter := NewPatchAdapter(ws)
	res, err := adapter.Apply(context.Background(), ports.PatchPayload{
		File:          "foo.txt",
		Modified:      "b\n",
		IsFullRewrite: true,
	})
	if err != nil {
		t.Fatalf("internal symlink Apply must succeed: %v", err)
	}
	if !res.Applied {
		t.Fatal("expected Applied=true")
	}
}
