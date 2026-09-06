package substrate

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/core/domain"
)

func TestSubstrate_FileWrite(t *testing.T) {
	root := t.TempDir()
	s := NewConcreteSubstrate(root)
	prop := Proposal{
		ID:     "test-1",
		Intent: "create file",
		Operations: []Operation{
			{Type: OpFileWrite, Target: "hello.txt", Content: []byte("hello world")},
		},
	}
	proof, err := s.Execute(context.Background(), prop)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if proof.Status != "committed" {
		t.Fatalf("status %q", proof.Status)
	}
	data, err := os.ReadFile(filepath.Join(root, "hello.txt"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "hello world" {
		t.Fatalf("content %q", string(data))
	}
	if proof.TransactionID == "" {
		t.Fatal("missing transaction id")
	}
}

func TestSubstrate_FileDelete(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "del.txt"), []byte("to delete"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewConcreteSubstrate(root)
	prop := Proposal{
		ID:         "test-del",
		Intent:     "delete",
		Operations: []Operation{{Type: OpFileDelete, Target: "del.txt"}},
	}
	if _, err := s.Execute(context.Background(), prop); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "del.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected deleted, err %v", err)
	}
}

func TestSubstrate_RollbackOnFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewConcreteSubstrate(root)
	prop := Proposal{
		ID:     "test-rollback",
		Intent: "bad",
		Operations: []Operation{
			{Type: OpFileWrite, Target: "keep.txt", Content: []byte("changed")},
			{Type: "UNKNOWN", Target: "x"},
		},
	}
	_, err := s.Execute(context.Background(), prop)
	if err == nil {
		t.Fatal("expected error")
	}
	data, _ := os.ReadFile(filepath.Join(root, "keep.txt"))
	if string(data) != "keep" {
		t.Fatalf("rollback failed, got %q", string(data))
	}
}

func TestSubstrate_ReadScope(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	rs := NewFSReadScope(root)
	data, err := rs.ReadFile("a.txt")
	if err != nil || string(data) != "a" {
		t.Fatalf("readscope %v %q", err, string(data))
	}
	if _, err := rs.Snapshot(); err != nil {
		t.Fatalf("snapshot %v", err)
	}
}

func TestSubstrate_ExecCmd(t *testing.T) {
	root := t.TempDir()
	s := NewConcreteSubstrate(root)
	prop := Proposal{
		ID:         "test-exec",
		Intent:     "exec",
		Operations: []Operation{{Type: OpExecCmd, Args: []string{"echo", "hi"}}},
	}
	if _, err := s.Execute(context.Background(), prop); err != nil {
		t.Fatalf("exec: %v", err)
	}
}

func TestSubstrate_BudgetExhaustionCancelsProcess(t *testing.T) {
	root := t.TempDir()
	// Budget with very short timeout to enforce cancellation.
	sub := NewSubstrate(root, nil, nil)
	unit := mkUnit("unit-budget", []string{"a.go", "b.go"})
	// Create a context that is already cancelled to simulate budget overflow.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := sub.ExecuteUnit(ctx, unit)
	if err == nil {
		t.Fatal("expected cancellation error from budget exhaustion")
	}
}

func TestSubstrate_AtomicRollbackOnUnitBatchFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "keep2.txt"), []byte("keep2"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := NewSubstrate(root, nil, nil)
	// Second target contains FAIL marker that triggers injected failure and rollback.
	unit := mkUnit("unit-rollback", []string{"keep2.txt", "FAIL_trigger.go"})
	_, err := sub.ExecuteUnit(context.Background(), unit)
	if err == nil {
		t.Fatal("expected injected failure")
	}
	// First file must be rolled back to original content.
	data, _ := os.ReadFile(filepath.Join(root, "keep2.txt"))
	if string(data) != "keep2" {
		t.Fatalf("atomic rollback failed, keep2.txt = %q", string(data))
	}
	// Failed target must not exist.
	if _, err := os.Stat(filepath.Join(root, "FAIL_trigger.go")); !os.IsNotExist(err) {
		t.Fatalf("failed batch left partial file, err %v", err)
	}
}

func mkUnit(id string, targets []string) domain.ExecutionUnit {
	// Helper to build a minimal ExecutionUnit for substrate tests.
	return domain.ExecutionUnit{
		UnitID:             domain.UnitID(id),
		FrameID:            domain.FrameID("frame-1"),
		ObjectiveSlice:     domain.ObjectiveSlice{Targets: targets},
		OutputBudget:       domain.OutputPolicy{MaxTokens: 10000},
		CapabilityBoundary: domain.DomainCapabilitySet(domain.CapWrite),
	}
}
