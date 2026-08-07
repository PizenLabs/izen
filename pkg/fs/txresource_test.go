package txfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/pkg/resource/file"
)

func TestTxResourceStagesThroughTransaction(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "data.txt"), []byte("ORIGINAL"), 0o600); err != nil {
		t.Fatal(err)
	}

	tx := NewTxFS(root)
	if err := tx.Begin(); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	base, err := file.NewFileResource(root, "data.txt", 0o644)
	if err != nil {
		t.Fatalf("NewFileResource: %v", err)
	}
	res, err := NewTxResource(base, tx)
	if err != nil {
		t.Fatalf("NewTxResource: %v", err)
	}

	if err := res.Write([]byte("NEW")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "data.txt")); err != nil {
		t.Fatalf("live file vanished: %v", err)
	} else if string(data) != "ORIGINAL" {
		t.Fatalf("live file mutated before commit: %q", data)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "data.txt"))
	if err != nil {
		t.Fatalf("read after commit: %v", err)
	}
	if string(data) != "NEW" {
		t.Fatalf("content = %q, want %q", data, "NEW")
	}
}

func TestTxResourceDeleteAndRollback(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "keep.txt"), []byte("precious"), 0o644); err != nil {
		t.Fatal(err)
	}

	tx := NewTxFS(root)
	if err := tx.Begin(); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	base, err := file.NewFileResource(root, "keep.txt", 0o644)
	if err != nil {
		t.Fatalf("NewFileResource: %v", err)
	}
	res, err := NewTxResource(base, tx)
	if err != nil {
		t.Fatalf("NewTxResource: %v", err)
	}

	if err := res.Delete(); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "keep.txt")); err != nil {
		t.Fatalf("delete must stay staged: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "keep.txt"))
	if err != nil {
		t.Fatalf("read after rollback: %v", err)
	}
	if string(data) != "precious" {
		t.Fatalf("content = %q, want %q", data, "precious")
	}
}

func TestTxResourceWriteRequiresActiveTransaction(t *testing.T) {
	root := t.TempDir()
	tx := NewTxFS(root)
	base, err := file.NewFileResource(root, "a.txt", 0o644)
	if err != nil {
		t.Fatalf("NewFileResource: %v", err)
	}
	res, err := NewTxResource(base, tx)
	if err != nil {
		t.Fatalf("NewTxResource: %v", err)
	}
	if err := res.Write([]byte("x")); !errors.Is(err, ErrNoActiveTransaction) {
		t.Fatalf("Write without active transaction: expected ErrNoActiveTransaction, got %v", err)
	}
	if err := res.Delete(); !errors.Is(err, ErrNoActiveTransaction) {
		t.Fatalf("Delete without active transaction: expected ErrNoActiveTransaction, got %v", err)
	}
}

func TestTxResourceValidatesConstructor(t *testing.T) {
	root := t.TempDir()
	tx := NewTxFS(root)
	base, err := file.NewFileResource(root, "a.txt", 0o644)
	if err != nil {
		t.Fatalf("NewFileResource: %v", err)
	}
	if _, err := NewTxResource(nil, tx); err == nil {
		t.Fatal("expected error for nil base resource")
	}
	if _, err := NewTxResource(base, nil); err == nil {
		t.Fatal("expected error for nil transaction")
	}
}

func TestTxResourceRejectsEscapingPaths(t *testing.T) {
	root := t.TempDir()
	tx := NewTxFS(root)
	if err := tx.Begin(); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	base, err := file.NewFileResource(root, "safe.txt", 0o644)
	if err != nil {
		t.Fatalf("NewFileResource: %v", err)
	}
	res, err := NewTxResource(base, tx)
	if err != nil {
		t.Fatalf("NewTxResource: %v", err)
	}
	if err := res.Write([]byte("x")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "safe.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("staged write must not touch the workspace")
	}
}
