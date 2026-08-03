package capabilities

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/domain/ports"
)

func TestOSFileRoundTrip(t *testing.T) {
	root := t.TempDir()
	f := NewOSFile(root)
	ctx := context.Background()

	var _ ports.FilePort = f

	path := filepath.Join("src", "deep", "file.go")
	content := "package main\n"

	if f.Exists(ctx, path) {
		t.Fatal("file exists before write")
	}

	if err := f.Write(ctx, path, content); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !f.Exists(ctx, path) {
		t.Fatal("file missing after write")
	}

	got, err := f.Read(ctx, path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != content {
		t.Errorf("Read = %q, want %q", got, content)
	}

	names, err := f.List(ctx, "src")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(names) != 1 || names[0] != "deep" {
		t.Errorf("List(src) = %v, want [deep]", names)
	}
}

func TestOSFileReadMissing(t *testing.T) {
	f := NewOSFile(t.TempDir())
	if _, err := f.Read(context.Background(), "nope.go"); err == nil {
		t.Fatal("Read on missing file returned nil error")
	}
}

func TestOSFileUnconfinedRoot(t *testing.T) {
	f := NewOSFile("")
	ctx := context.Background()
	if err := f.Write(ctx, filepath.Join(t.TempDir(), "a.txt"), "x"); err != nil {
		t.Fatalf("Write without root: %v", err)
	}
}

func TestOSFileCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := NewOSFile(t.TempDir())
	if _, err := f.Read(ctx, "a.go"); err == nil {
		t.Error("Read with cancelled context returned nil error")
	}
	if err := f.Write(ctx, "a.go", "x"); err == nil {
		t.Error("Write with cancelled context returned nil error")
	}
	if f.Exists(ctx, "a.go") {
		t.Error("Exists with cancelled context returned true")
	}
}
