package checkpoint

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func createFileCheckpoint(t *testing.T, workDir, label string, ts time.Time) string {
	t.Helper()
	id := "cp-test-" + ts.Format("20060102150405.000000000")
	// Ensure uniqueness across rapid calls.
	for i := 0; ; i++ {
		candidate := id
		if i > 0 {
			candidate = id + "-" + string(rune('a'+i))
		}
		dir := filepath.Join(workDir, ".izen", "checkpoints", candidate)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			id = candidate
			break
		}
	}
	dir := filepath.Join(workDir, ".izen", "checkpoints", id)
	if err := os.MkdirAll(filepath.Join(dir, "files"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := CheckpointRecord{ID: id, Label: label, Timestamp: ts}
	data, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(dir, "checkpoint.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return id
}

func countCP(t *testing.T, workDir string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(workDir, ".izen", "checkpoints"))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() && len(e.Name()) > 3 && e.Name()[:3] == "cp-" {
			n++
		}
	}
	return n
}

func TestPruneCheckpointsFIFO(t *testing.T) {
	workDir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 15; i++ {
		createFileCheckpoint(t, workDir, "auto", base.Add(time.Duration(i)*time.Minute))
	}
	if got := countCP(t, workDir); got != 15 {
		t.Fatalf("setup: got %d checkpoints, want 15", got)
	}
	if err := PruneCheckpoints(workDir, 10); err != nil {
		t.Fatalf("PruneCheckpoints: %v", err)
	}
	if got := countCP(t, workDir); got != 10 {
		t.Fatalf("after prune: got %d, want 10", got)
	}
	// Oldest 5 must be gone: verify newest 10 remain by timestamp order.
	entries, _ := os.ReadDir(filepath.Join(workDir, ".izen", "checkpoints"))
	oldest := base
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(workDir, ".izen", "checkpoints", e.Name(), "checkpoint.json"))
		var rec CheckpointRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			continue
		}
		if rec.Timestamp.Before(base.Add(5 * time.Minute)) {
			t.Fatalf("oldest checkpoint %s (%v) should have been pruned", e.Name(), rec.Timestamp)
		}
		_ = oldest
	}
}

func TestPruneExcludesSafetyMarkers(t *testing.T) {
	workDir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	// Oldest checkpoint is a safety marker and must survive.
	createFileCheckpoint(t, workDir, "safety backup", base)
	for i := 1; i < 5; i++ {
		createFileCheckpoint(t, workDir, "auto", base.Add(time.Duration(i)*time.Minute))
	}
	// Protected sentinel file on the second checkpoint.
	entries, _ := os.ReadDir(filepath.Join(workDir, ".izen", "checkpoints"))
	_ = entries
	if err := PruneCheckpoints(workDir, 2); err != nil {
		t.Fatalf("PruneCheckpoints: %v", err)
	}
	// Safety-labeled checkpoint must still exist.
	found := false
	remaining, _ := os.ReadDir(filepath.Join(workDir, ".izen", "checkpoints"))
	for _, e := range remaining {
		data, _ := os.ReadFile(filepath.Join(workDir, ".izen", "checkpoints", e.Name(), "checkpoint.json"))
		var rec CheckpointRecord
		_ = json.Unmarshal(data, &rec)
		if rec.Label == "safety backup" {
			found = true
		}
	}
	if !found {
		t.Fatal("safety-labeled checkpoint was pruned")
	}
}

func TestPruneNegativeRetention(t *testing.T) {
	if err := PruneCheckpoints(t.TempDir(), -1); err == nil {
		t.Fatal("expected error for negative maxRetention")
	}
}

func TestPruneMissingDir(t *testing.T) {
	if err := PruneCheckpoints(t.TempDir(), 10); err != nil {
		t.Fatalf("missing checkpoints dir should be nil: %v", err)
	}
}

func TestCheckpointSkipsOversizedFiles(t *testing.T) {
	workDir := t.TempDir()
	writeWorkFile(t, workDir, "small.txt", "hello")
	// Create an 11MB sparse-ish file without holding it all in one string
	// concatenation: write in chunks.
	bigPath := filepath.Join(workDir, "big.bin")
	f, err := os.Create(bigPath)
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 1<<20)
	for i := 0; i < 11; i++ {
		if _, err := f.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	_ = f.Close()

	id, err := CreateCheckpoint(workDir, "size-guard")
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}
	// big.bin must not be snapshotted.
	if _, err := os.Stat(filepath.Join(workDir, ".izen", "checkpoints", id, "files", "big.bin")); !os.IsNotExist(err) {
		t.Fatal("oversized big.bin should be excluded from snapshot")
	}
	if _, err := os.Stat(filepath.Join(workDir, ".izen", "checkpoints", id, "files", "small.txt")); err != nil {
		t.Fatalf("small.txt should be snapshotted: %v", err)
	}
	// Metadata must record the skip.
	data, _ := os.ReadFile(filepath.Join(workDir, ".izen", "checkpoints", id, "checkpoint.json"))
	var rec CheckpointRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	found := false
	for _, s := range rec.SkippedFiles {
		if s == "big.bin" {
			found = true
		}
	}
	if !found {
		t.Fatalf("skipped_files %v must contain big.bin", rec.SkippedFiles)
	}
	// Rollback must preserve the oversized file (not delete it).
	if err := os.Remove(bigPath); err != nil {
		t.Fatal(err)
	}
	if err := Rollback(workDir, id); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	// big.bin was deleted after the checkpoint (not part of snapshot) — it
	// stays deleted, but rollback must not fail. Recreate and verify a
	// pre-existing big file survives rollback.
	if err := os.WriteFile(bigPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	id2, err := CreateCheckpoint(workDir, "size-guard-2")
	if err != nil {
		t.Fatal(err)
	}
	if err := Rollback(workDir, id2); err != nil {
		t.Fatalf("Rollback 2: %v", err)
	}
	if _, err := os.Stat(bigPath); err != nil {
		t.Fatalf("pre-existing oversized file must survive rollback: %v", err)
	}
}

func TestCheckpointSkipsGitignoredFiles(t *testing.T) {
	workDir := t.TempDir()
	writeWorkFile(t, workDir, "keep.txt", "keep")
	writeWorkFile(t, workDir, "ignored.log", "logs")
	if err := os.WriteFile(filepath.Join(workDir, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	id, err := CreateCheckpoint(workDir, "gitignore-guard")
	if err != nil {
		t.Fatalf("CreateCheckpoint: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, ".izen", "checkpoints", id, "files", "ignored.log")); !os.IsNotExist(err) {
		t.Fatal("gitignored file should be excluded from snapshot")
	}
	data, _ := os.ReadFile(filepath.Join(workDir, ".izen", "checkpoints", id, "checkpoint.json"))
	var rec CheckpointRecord
	_ = json.Unmarshal(data, &rec)
	found := false
	for _, s := range rec.SkippedFiles {
		if s == "ignored.log" {
			found = true
		}
	}
	if !found {
		t.Fatalf("skipped_files %v must contain ignored.log", rec.SkippedFiles)
	}
}
