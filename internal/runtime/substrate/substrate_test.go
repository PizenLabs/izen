package substrate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
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
