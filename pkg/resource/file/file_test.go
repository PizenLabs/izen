package file

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeSnapshot is a minimal resource.Snapshot used to verify Restore rejects
// foreign snapshot types.
type fakeSnapshot struct{}

func (fakeSnapshot) Hash() string { return "" }
func (fakeSnapshot) Data() any    { return nil }

// newTestFileResource creates a workspace with a pre-existing target file.
func newTestFileResource(t *testing.T) (*FileResource, string) {
	t.Helper()
	root := t.TempDir()
	target := "src/app.go"
	r, err := NewFileResource(root, target, 0)
	if err != nil {
		t.Fatalf("NewFileResource: %v", err)
	}
	path := filepath.Join(root, target)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatalf("write original: %v", err)
	}
	return r, path
}

func TestFileResourceSnapshotModifyRestore(t *testing.T) {
	ctx := t.Context()
	r, path := newTestFileResource(t)

	snap, err := r.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Hash() == "" {
		t.Fatal("expected non-empty hash")
	}
	data, ok := snap.Data().(fileSnapshotData)
	if !ok {
		t.Fatalf("unexpected snapshot data type %T", snap.Data())
	}
	if string(data.Content) != "original" {
		t.Fatalf("expected content %q, got %q", "original", data.Content)
	}

	if err := os.WriteFile(path, []byte("mutated"), 0o644); err != nil {
		t.Fatalf("mutate: %v", err)
	}
	got, err := r.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(got) != "mutated" {
		t.Fatalf("expected mutated content %q, got %q", "mutated", got)
	}

	if err := r.Restore(ctx, snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	got, err = r.Read()
	if err != nil {
		t.Fatalf("Read after restore: %v", err)
	}
	if string(got) != "original" {
		t.Fatalf("expected restored content %q, got %q", "original", got)
	}
}

func TestFileResourceSnapshotRestoresDeletedFile(t *testing.T) {
	ctx := t.Context()
	r, path := newTestFileResource(t)

	snap, err := r.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := r.Restore(ctx, snap); err != nil {
		t.Fatalf("Restore after deletion: %v", err)
	}
	got, err := r.Read()
	if err != nil {
		t.Fatalf("Read after restore: %v", err)
	}
	if string(got) != "original" {
		t.Fatalf("expected restored content %q, got %q", "original", got)
	}
}

func TestFileResourceValidateState(t *testing.T) {
	ctx := t.Context()
	r, _ := newTestFileResource(t)
	if err := r.ValidateState(ctx); err != nil {
		t.Fatalf("ValidateState on existing file: %v", err)
	}

	missing, err := NewFileResource(t.TempDir(), "does/not/exist.go", 0)
	if err != nil {
		t.Fatalf("NewFileResource: %v", err)
	}
	if err := missing.ValidateState(ctx); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestFileResourceWrite(t *testing.T) {
	r, path := newTestFileResource(t)
	if err := r.Write([]byte("written")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "written" {
		t.Fatalf("expected %q, got %q", "written", got)
	}
}

func TestFileResourceRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if _, err := NewFileResource(root, "../escape.go", 0); err == nil {
		t.Fatal("expected error for path escaping workspace root")
	}
	if _, err := NewFileResource(root, "/abs/path.go", 0); err == nil {
		t.Fatal("expected error for absolute path")
	}
}

func TestFileResourceRestoreRejectsForeignSnapshot(t *testing.T) {
	ctx := t.Context()
	r, _ := newTestFileResource(t)
	if err := r.Restore(ctx, nil); err == nil {
		t.Fatal("expected error restoring a nil snapshot")
	}
	if err := r.Restore(ctx, fakeSnapshot{}); err == nil {
		t.Fatal("expected error restoring a foreign snapshot type")
	}
}

func TestFileResourceDelete(t *testing.T) {
	r, path := newTestFileResource(t)
	if err := r.Delete(); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected file removed, stat err %v", err)
	}
}

func TestFileResourceDeleteIdempotent(t *testing.T) {
	r, _ := newTestFileResource(t)
	if err := r.Delete(); err != nil {
		t.Fatalf("first Delete: %v", err)
	}
	if err := r.Delete(); err != nil {
		t.Fatalf("second Delete should be a no-op: %v", err)
	}
}
