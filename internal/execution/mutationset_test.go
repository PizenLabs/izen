package execution

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/engine"
)

// ── MutationSet aggregate contract (Phase 9A) ───────────────────────────────
//
// These tests pin the MutationSet lifecycle directly: pending → applying →
// verifying → committed / rolled_back / failed / cancelled, single-owner
// transaction lifetime, and the terminal-set rules (no double commit, no
// double rollback, committed can never roll back). They exercise the aggregate
// itself — the engine/UI integration around the REAL call path lives in the ui
// package tests (mutationset_test.go).

func TestMutationSet_RecordsTargetsAndTransaction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	ms := NewMutationSetWithID("ms-test")
	if ms.State != MutationPending {
		t.Fatalf("new set state = %q, want pending", ms.State)
	}
	if ms.Transaction == nil {
		t.Fatal("new set must own a transaction")
	}
	if err := ms.Record(path); err != nil {
		t.Fatalf("Record failed: %v", err)
	}
	if !ms.HasTarget(path) {
		t.Fatalf("recorded target missing: %v", ms.Targets)
	}
	if len(ms.Targets) != 1 {
		t.Fatalf("targets = %v, want exactly one (dedup)", ms.Targets)
	}
	if err := ms.Record(path); err != nil {
		t.Fatalf("re-record same target must be idempotent: %v", err)
	}
	if len(ms.Targets) != 1 {
		t.Fatalf("targets after re-record = %v, want still one", ms.Targets)
	}
	if len(ms.Transaction.Snapshots) != 1 {
		t.Fatalf("snapshots = %d, want 1 recorded", len(ms.Transaction.Snapshots))
	}
}

func TestMutationSet_CommittedIsTerminal_NoDoubleCommit(t *testing.T) {
	ms := NewMutationSetWithID("ms-commit")
	if err := ms.Commit(); err != nil {
		t.Fatalf("first commit failed: %v", err)
	}
	if !ms.Committed() {
		t.Fatal("set must be committed after Commit()")
	}
	if !ms.Terminal() {
		t.Fatal("committed set must be terminal")
	}
	if err := ms.Commit(); !errors.Is(err, ErrMutationSetTerminal) {
		t.Fatalf("second commit = %v, want ErrMutationSetTerminal", err)
	}
	if !ms.Committed() {
		t.Fatal("double commit must not change the committed state")
	}
	if ms.Transaction == nil || !ms.Transaction.Committed() {
		t.Fatal("owned transaction must be committed and terminal")
	}
}

func TestMutationSet_CommittedCannotRollback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(path, []byte("orig"), 0o644); err != nil {
		t.Fatal(err)
	}
	ms := NewMutationSetWithID("ms-committed-norb")
	if err := ms.Record(path); err != nil {
		t.Fatalf("Record failed: %v", err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ms.Commit(); err != nil {
		t.Fatalf("commit failed: %v", err)
	}
	// A committed mutation can never be rolled back.
	if errs := ms.Rollback(); len(errs) != 0 {
		t.Fatalf("rollback of committed set returned errors: %v", errs)
	}
	if !ms.Committed() {
		t.Fatal("rollback must not flip a committed set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "changed" {
		t.Fatalf("committed file was rolled back: %q", data)
	}
}

func TestMutationSet_RollbackRestores_NoDoubleRollback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.txt")
	if err := os.WriteFile(path, []byte("orig"), 0o644); err != nil {
		t.Fatal(err)
	}
	ms := NewMutationSetWithID("ms-rollback")
	if err := ms.Record(path); err != nil {
		t.Fatalf("Record failed: %v", err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if errs := ms.Rollback(); len(errs) != 0 {
		t.Fatalf("rollback errors: %v", errs)
	}
	if !ms.RolledBack() {
		t.Fatalf("set state = %q, want rolled_back", ms.State)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "orig" {
		t.Fatalf("rollback did not restore: %q", data)
	}
	// No double rollback: the second rollback is a no-op and cannot error.
	if errs := ms.Rollback(); len(errs) != 0 {
		t.Fatalf("double rollback returned errors: %v", errs)
	}
	if !ms.RolledBack() {
		t.Fatal("double rollback must not change the rolled_back state")
	}
	if len(ms.Transaction.Snapshots) != 0 {
		t.Fatalf("snapshots remain staged after rollback: %d", len(ms.Transaction.Snapshots))
	}
}

func TestMutationSet_RollbackToSetsFailureOutcome(t *testing.T) {
	ms := NewMutationSetWithID("ms-fail")
	if errs := ms.RollbackTo(MutationFailed); len(errs) != 0 {
		t.Fatalf("rollback-to-failed errors: %v", errs)
	}
	if ms.State != MutationFailed || !ms.Terminal() {
		t.Fatalf("state = %q, want failed+terminal", ms.State)
	}
	ms2 := NewMutationSetWithID("ms-cancel")
	if errs := ms2.RollbackTo(MutationCancelled); len(errs) != 0 {
		t.Fatalf("rollback-to-cancelled errors: %v", errs)
	}
	if ms2.State != MutationCancelled || !ms2.Terminal() {
		t.Fatalf("state = %q, want cancelled+terminal", ms2.State)
	}
}

func TestMutationSet_RecordIntoTerminalSetRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "d.txt")
	ms := NewMutationSetWithID("ms-terminal-record")
	if err := ms.Commit(); err != nil {
		t.Fatal(err)
	}
	err := ms.Record(path)
	if !errors.Is(err, ErrMutationSetTerminal) {
		t.Fatalf("Record into committed set = %v, want ErrMutationSetTerminal", err)
	}
	if len(ms.Targets) != 0 {
		t.Fatalf("terminal set accepted a target: %v", ms.Targets)
	}
}

func TestTransaction_ExplicitTerminalState(t *testing.T) {
	tx := engine.NewTransaction()
	if !tx.Active() {
		t.Fatal("new transaction must be active")
	}
	tx.Commit()
	if !tx.Committed() {
		t.Fatal("commit must mark the transaction committed")
	}
	// Commit is a no-op on a committed transaction.
	tx.Commit()
	if !tx.Committed() {
		t.Fatal("double commit must not change state")
	}
	// Rollback of a committed transaction is impossible (no-op).
	if errs := tx.Rollback(); len(errs) != 0 {
		t.Fatalf("rollback of committed transaction returned errors: %v", errs)
	}
	if !tx.Committed() {
		t.Fatal("rollback must not change a committed transaction")
	}

	tx2 := engine.NewTransaction()
	if errs := tx2.Rollback(); len(errs) != 0 {
		t.Fatalf("rollback errors: %v", errs)
	}
	if !tx2.RolledBack() {
		t.Fatal("rollback must mark the transaction rolled back")
	}
	if errs := tx2.Rollback(); len(errs) != 0 {
		t.Fatalf("double rollback returned errors: %v", errs)
	}
}
