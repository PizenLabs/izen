package scope

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestVerifyAllowsRegularFile(t *testing.T) {
	ws := t.TempDir()
	writeTemp(t, ws, "foo.txt", "hello")
	r, err := Open(ws)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = r.Close() }()
	if err := r.Verify("foo.txt"); err != nil {
		t.Fatalf("Verify(foo.txt): %v", err)
	}
}

func TestVerifyPermitsInternalSymlink(t *testing.T) {
	ws := t.TempDir()
	writeTemp(t, ws, "real.txt", "inside")
	// Internal symlink (relative) whose target stays within the root.
	if err := os.Symlink("real.txt", filepath.Join(ws, "link.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	r, err := Open(ws)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = r.Close() }()
	if err := r.Verify("link.txt"); err != nil {
		t.Fatalf("internal symlink must be permitted: %v", err)
	}
	// Internal absolute symlink pointing inside the root is permitted too.
	if err := os.Symlink(filepath.Join(ws, "real.txt"), filepath.Join(ws, "abslink.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := r.Verify("abslink.txt"); err != nil {
		t.Fatalf("internal absolute symlink must be permitted: %v", err)
	}
}

func TestVerifyDeniesAbsoluteEscapeSymlink(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	evil := filepath.Join(outside, "evil.txt")
	writeTemp(t, outside, "evil.txt", "secret")
	writeTemp(t, ws, "foo.txt", "hello")
	// Swap foo.txt with an absolute symlink pointing outside.
	if err := os.Remove(filepath.Join(ws, "foo.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.Symlink(evil, filepath.Join(ws, "foo.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	r, err := Open(ws)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = r.Close() }()
	if err := r.Verify("foo.txt"); !errors.Is(err, ErrWorkspaceEscape) {
		t.Fatalf("Verify = %v, want ErrWorkspaceEscape", err)
	}
}

func TestVerifyDeniesRelativeEscapeSymlink(t *testing.T) {
	ws := t.TempDir()
	writeTemp(t, ws, "foo.txt", "hello")
	if err := os.Remove(filepath.Join(ws, "foo.txt")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	// Relative symlink climbing out of the workspace.
	if err := os.Symlink("../outside.txt", filepath.Join(ws, "foo.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	r, err := Open(ws)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = r.Close() }()
	if err := r.Verify("foo.txt"); !errors.Is(err, ErrWorkspaceEscape) {
		t.Fatalf("Verify = %v, want ErrWorkspaceEscape", err)
	}
}

func TestAtomicWriteDeniesEscapeWithoutOutsideMutation(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	evil := filepath.Join(outside, "evil.txt")
	writeTemp(t, outside, "evil.txt", "untouched")
	r, err := Open(ws)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = r.Close() }()
	// Plant the escape symlink, then attempt the write.
	if err := os.Symlink(evil, filepath.Join(ws, "foo.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := r.AtomicWrite("foo.txt", []byte("pwned"), 0o644); !errors.Is(err, ErrWorkspaceEscape) {
		t.Fatalf("AtomicWrite = %v, want ErrWorkspaceEscape", err)
	}
	data, err := os.ReadFile(evil)
	if err != nil {
		t.Fatalf("read evil: %v", err)
	}
	if string(data) != "untouched" {
		t.Fatalf("outside file mutated: %q", data)
	}
}

func TestVerifyRejectsTraversalAndAbsolute(t *testing.T) {
	ws := t.TempDir()
	r, err := Open(ws)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = r.Close() }()
	for _, rel := range []string{"../x", "/etc/passwd", ""} {
		if err := r.Verify(rel); !errors.Is(err, ErrWorkspaceEscape) {
			t.Fatalf("Verify(%q) = %v, want ErrWorkspaceEscape", rel, err)
		}
	}
}
