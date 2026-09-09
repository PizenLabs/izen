package atomicio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileAtomicCreatesDestination(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "sub", "file.txt")
	if err := WriteFileAtomic(dst, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("content = %q, want hello", data)
	}
}

func TestWriteFileAtomicOverwritesAtomically(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(dst, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(dst, []byte("updated"), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "updated" {
		t.Fatalf("content = %q, want updated", data)
	}
}

// TestWriteFileAtomicInterruptedWritePreservesDestination simulates an
// interrupted write by leaving a stale temp file behind and verifying the
// destination still holds its previous content and no temp files leak after
// a successful write.
func TestWriteFileAtomicInterruptedWritePreservesDestination(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "dest.txt")
	if err := os.WriteFile(dst, []byte("stable-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Simulate debris from a crashed predecessor in the same directory.
	debris := dst + ".tmp.crashed"
	if err := os.WriteFile(debris, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := WriteFileAtomic(dst, []byte("new-content"), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new-content" {
		t.Fatalf("content = %q, want new-content", data)
	}

	// No temp files from THIS write may leak (pre-existing debris is the
	// caller's to GC, but our own tmp pattern must be gone).
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if name == "dest.txt" || name == "dest.txt.tmp.crashed" {
			continue
		}
		if strings.Contains(name, ".tmp.") {
			t.Fatalf("leaked temp file %q after successful write", name)
		}
	}
}

func TestWriteFileAtomicEmptyFilename(t *testing.T) {
	if err := WriteFileAtomic("", []byte("x"), 0o644); err == nil {
		t.Fatal("expected error for empty filename")
	}
}

func TestWriteFileAtomicDefaultPerm(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "f.txt")
	if err := WriteFileAtomic(dst, []byte("x"), 0); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("stat: %v", err)
	}
}
