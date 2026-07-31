package build

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/core/contract"
	wschk "github.com/PizenLabs/izen/internal/workspace/checkpoint"
)

func approve(t *testing.T, e *Engine, file string, taskID int) {
	t.Helper()
	id := e.QueueProposal(Proposal{File: file, TaskID: taskID, Strategy: "ATOMIC_REPLACE"})
	if err := e.ApproveProposal(id); err != nil {
		t.Fatal(err)
	}
}

func TestExecutor_ApplyMutation_RollbackRestoresOriginal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a // v1"), 0644); err != nil {
		t.Fatal(err)
	}

	e := NewEngine()
	ex := NewExecutor(dir, e)
	mgr := wschk.NewManager(dir)
	ex.WithCheckpointManager(mgr)

	approve(t, e, "a.go", 1)

	if err := ex.ApplyMutation(t.Context(), FileMutation{File: "a.go", Content: "package a // BROKEN", TaskID: 1}); err != nil {
		t.Fatalf("ApplyMutation: %v", err)
	}
	if got := mustRead(t, filepath.Join(dir, "a.go")); got != "package a // BROKEN" {
		t.Fatalf("mutation not applied: %q", got)
	}
	// The checkpoint must stay open until compilation verification.
	if mgr.Open() != 1 {
		t.Fatalf("expected 1 open checkpoint, got %d", mgr.Open())
	}

	if err := ex.RollbackOpenCheckpoints(); err != nil {
		t.Fatalf("RollbackOpenCheckpoints: %v", err)
	}
	if got := mustRead(t, filepath.Join(dir, "a.go")); got != "package a // v1" {
		t.Fatalf("rollback did not restore original: %q", got)
	}
	if mgr.Open() != 0 {
		t.Fatalf("expected checkpoints consumed, Open() = %d", mgr.Open())
	}
}

func TestExecutor_ApplyMutation_RollbackDeletesNewFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "created.go")

	e := NewEngine()
	ex := NewExecutor(dir, e)
	mgr := wschk.NewManager(dir)
	ex.WithCheckpointManager(mgr)

	approve(t, e, "created.go", 1)

	if err := ex.ApplyMutation(t.Context(), FileMutation{File: "created.go", Content: "package main\n", TaskID: 1}); err != nil {
		t.Fatalf("ApplyMutation: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected created.go to exist after mutation: %v", err)
	}

	if err := ex.RollbackOpenCheckpoints(); err != nil {
		t.Fatalf("RollbackOpenCheckpoints: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("expected created.go deleted after rollback, stat err = %v", err)
	}
}

func TestExecutor_ApplyMutation_CommitKeepsChange(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a // v1"), 0644); err != nil {
		t.Fatal(err)
	}

	e := NewEngine()
	ex := NewExecutor(dir, e)
	mgr := wschk.NewManager(dir)
	ex.WithCheckpointManager(mgr)

	approve(t, e, "a.go", 1)

	if err := ex.ApplyMutation(t.Context(), FileMutation{File: "a.go", Content: "package a // v2 FINAL", TaskID: 1}); err != nil {
		t.Fatalf("ApplyMutation: %v", err)
	}
	if err := ex.CommitOpenCheckpoints(); err != nil {
		t.Fatalf("CommitOpenCheckpoints: %v", err)
	}
	if got := mustRead(t, filepath.Join(dir, "a.go")); got != "package a // v2 FINAL" {
		t.Fatalf("commit must keep applied mutation: %q", got)
	}
	if mgr.Open() != 0 {
		t.Fatalf("expected checkpoints consumed after commit, Open() = %d", mgr.Open())
	}
}

func TestExecutor_ApplyMutation_AutoRollbackOnWriteFailure(t *testing.T) {
	dir := t.TempDir()
	// A directory at the target path makes the write fail after the checkpoint
	// was created — the executor must auto-rollback and consume the checkpoint.
	if err := os.MkdirAll(filepath.Join(dir, "blocked.go"), 0755); err != nil {
		t.Fatal(err)
	}

	e := NewEngine()
	ex := NewExecutor(dir, e)
	mgr := wschk.NewManager(dir)
	ex.WithCheckpointManager(mgr)

	approve(t, e, "blocked.go", 1)

	err := ex.ApplyMutation(t.Context(), FileMutation{File: "blocked.go", Content: "package x", TaskID: 1})
	if err == nil {
		t.Fatal("expected write failure for directory target")
	}
	if mgr.Open() != 0 {
		t.Fatalf("expected auto-rollback to consume checkpoint, Open() = %d", mgr.Open())
	}
	// The directory must be intact (rollback restores the "original" non-file).
	if info, statErr := os.Stat(filepath.Join(dir, "blocked.go")); statErr != nil || !info.IsDir() {
		t.Fatalf("expected blocked.go directory intact, stat err = %v", statErr)
	}
}

func TestExecutor_ApplyMutation_PermissionEnforced(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a // v1"), 0644); err != nil {
		t.Fatal(err)
	}

	e := NewEngine()
	ex := NewExecutor(dir, e)
	// CREATE_CHECKPOINT (PermCheckpoint) is NOT granted → mutation must block.
	mgr := wschk.NewManager(dir).WithPermissions([]contract.PermissionLevel{
		contract.PermWorkspace, contract.PermPatch,
	})
	ex.WithCheckpointManager(mgr)

	approve(t, e, "a.go", 1)

	err := ex.ApplyMutation(t.Context(), FileMutation{File: "a.go", Content: "package a // BROKEN", TaskID: 1})
	if err == nil {
		t.Fatal("expected mutation blocked without CREATE_CHECKPOINT permission")
	}
	if got := mustRead(t, filepath.Join(dir, "a.go")); got != "package a // v1" {
		t.Fatalf("permission-denied mutation must not touch the file: %q", got)
	}
	if mgr.Open() != 0 {
		t.Fatalf("no checkpoint should be created, Open() = %d", mgr.Open())
	}
}

func TestExecutor_WithCheckpointPermissionGranted(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a // v1"), 0644); err != nil {
		t.Fatal(err)
	}

	e := NewEngine()
	ex := NewExecutor(dir, e)
	mgr := wschk.NewManager(dir).WithPermissions([]contract.PermissionLevel{
		contract.PermWorkspace, contract.PermPatch, contract.PermCheckpoint,
	})
	ex.WithCheckpointManager(mgr)

	approve(t, e, "a.go", 1)

	if err := ex.ApplyMutation(t.Context(), FileMutation{File: "a.go", Content: "package a // v2", TaskID: 1}); err != nil {
		t.Fatalf("ApplyMutation with CREATE_CHECKPOINT granted: %v", err)
	}
	if got := mustRead(t, filepath.Join(dir, "a.go")); got != "package a // v2" {
		t.Fatalf("mutation not applied: %q", got)
	}
}

func TestExecutor_WithoutManager_BackwardsCompatible(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a // v1"), 0644); err != nil {
		t.Fatal(err)
	}

	e := NewEngine()
	ex := NewExecutor(dir, e) // no checkpoint manager

	approve(t, e, "a.go", 1)

	if err := ex.ApplyMutation(t.Context(), FileMutation{File: "a.go", Content: "package a // v2", TaskID: 1}); err != nil {
		t.Fatalf("ApplyMutation without manager: %v", err)
	}
	if got := mustRead(t, filepath.Join(dir, "a.go")); got != "package a // v2" {
		t.Fatalf("mutation not applied: %q", got)
	}
	// Lifecycle helpers must be safe no-ops without a manager.
	if err := ex.CommitOpenCheckpoints(); err != nil {
		t.Fatalf("CommitOpenCheckpoints without manager: %v", err)
	}
	if err := ex.RollbackOpenCheckpoints(); err != nil {
		t.Fatalf("RollbackOpenCheckpoints without manager: %v", err)
	}
}

func TestBuildStageContract_PermitsCheckpoint(t *testing.T) {
	e := NewEngine()
	ex := NewExecutor(t.TempDir(), e)
	stage := NewBuildStage(e, ex)
	c := stage.Contract()
	if !c.Permitted(contract.PermCheckpoint) {
		t.Fatal("executor stage contract must permit CREATE_CHECKPOINT")
	}
	if !c.Permitted(contract.PermPatch) || !c.Permitted(contract.PermWorkspace) {
		t.Fatal("existing stage permissions must remain")
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
