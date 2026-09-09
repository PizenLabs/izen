package checkpoint

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeWorkFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPhase3CreateCheckpointCapturesState(t *testing.T) {
	workDir := t.TempDir()
	writeWorkFile(t, workDir, "a.txt", "v1")
	writeWorkFile(t, workDir, "sub/b.txt", "b1")

	id, err := CreateCheckpoint(workDir, "test")
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}
	if !strings.HasPrefix(id, "cp-") {
		t.Fatalf("id = %q, want cp- prefix", id)
	}
	manifestPath := filepath.Join(workDir, ".izen", "checkpoints", id, "checkpoint.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var rec CheckpointRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if rec.ID != id || rec.Label != "test" {
		t.Fatalf("manifest = %+v, want id %s label test", rec, id)
	}
	if rec.Timestamp.IsZero() {
		t.Fatal("manifest timestamp zero")
	}
	// Snapshot blobs must exist.
	for _, rel := range []string{"a.txt", "sub/b.txt"} {
		if _, err := os.Stat(filepath.Join(workDir, ".izen", "checkpoints", id, "files", filepath.FromSlash(rel))); err != nil {
			t.Fatalf("snapshot %s missing: %v", rel, err)
		}
	}
}

func TestPhase3RollbackRestoresMutations(t *testing.T) {
	workDir := t.TempDir()
	writeWorkFile(t, workDir, "a.txt", "original")
	writeWorkFile(t, workDir, "keep.txt", "keep")

	id, err := CreateCheckpoint(workDir, "pre-mutation")
	if err != nil {
		t.Fatal(err)
	}

	// Synthetic mutations: modify, delete, create.
	writeWorkFile(t, workDir, "a.txt", "mutated")
	if err := os.Remove(filepath.Join(workDir, "keep.txt")); err != nil {
		t.Fatal(err)
	}
	writeWorkFile(t, workDir, "new.txt", "brand new")
	writeWorkFile(t, workDir, "sub/extra.txt", "extra")

	if err := Rollback(workDir, id); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	if got, _ := os.ReadFile(filepath.Join(workDir, "a.txt")); string(got) != "original" {
		t.Fatalf("a.txt = %q, want original", got)
	}
	if got, err := os.ReadFile(filepath.Join(workDir, "keep.txt")); err != nil || string(got) != "keep" {
		t.Fatalf("keep.txt = %q, err %v, want keep", got, err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "new.txt")); !os.IsNotExist(err) {
		t.Fatal("new.txt should have been removed by rollback")
	}
	if _, err := os.Stat(filepath.Join(workDir, "sub", "extra.txt")); !os.IsNotExist(err) {
		t.Fatal("sub/extra.txt should have been removed by rollback")
	}
}

func TestPhase3RollbackUnknownID(t *testing.T) {
	workDir := t.TempDir()
	if err := Rollback(workDir, "cp-does-not-exist"); err == nil {
		t.Fatal("expected error for unknown checkpoint")
	}
	if err := Rollback(workDir, "../evil"); err == nil {
		t.Fatal("expected error for path-traversal id")
	}
	if _, err := CreateCheckpoint("", "x"); err == nil {
		t.Fatal("expected error for empty workDir")
	}
}
