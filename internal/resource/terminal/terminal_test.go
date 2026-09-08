package terminal

import (
	"path/filepath"
	"strings"
	"testing"
)

// fakeSnapshot is a minimal resource.Snapshot used to verify Restore rejects
// foreign snapshot types.
type fakeSnapshot struct{}

func (fakeSnapshot) Hash() string { return "" }
func (fakeSnapshot) Data() any    { return nil }

func TestTerminalResourceValidateState(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	tr, err := NewTerminalResource(dir, []string{"KEY=VALUE"}, "")
	if err != nil {
		t.Fatalf("NewTerminalResource: %v", err)
	}
	if err := tr.ValidateState(ctx); err != nil {
		t.Fatalf("ValidateState on valid environment: %v", err)
	}
	if tr.shell != DefaultShell {
		t.Fatalf("expected default shell %q, got %q", DefaultShell, tr.shell)
	}
	if tr.ID() != dir {
		t.Fatalf("unexpected ID %q", tr.ID())
	}
	if tr.Kind().String() != "res.terminal" {
		t.Fatalf("unexpected kind %q", tr.Kind())
	}
}

func TestTerminalResourceValidateStateMissingDir(t *testing.T) {
	ctx := t.Context()
	missing := filepath.Join(t.TempDir(), "nope")
	tr, err := NewTerminalResource(missing, nil, "")
	if err != nil {
		t.Fatalf("NewTerminalResource: %v", err)
	}
	if err := tr.ValidateState(ctx); err == nil {
		t.Fatal("expected error for missing working directory")
	}
}

func TestTerminalResourceValidateStateMissingShell(t *testing.T) {
	ctx := t.Context()
	tr, err := NewTerminalResource(t.TempDir(), nil, "/nonexistent/shell")
	if err != nil {
		t.Fatalf("NewTerminalResource: %v", err)
	}
	if err := tr.ValidateState(ctx); err == nil {
		t.Fatal("expected error for unavailable shell binary")
	}
}

func TestTerminalResourceSnapshotRestore(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	tr, err := NewTerminalResource(dir, []string{"A=1", "B=2"}, "/bin/sh")
	if err != nil {
		t.Fatalf("NewTerminalResource: %v", err)
	}

	snap, err := tr.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Hash() == "" {
		t.Fatal("expected non-empty hash")
	}
	data, ok := snap.Data().(terminalSnapshotData)
	if !ok {
		t.Fatalf("unexpected snapshot data type %T", snap.Data())
	}
	if data.Dir != dir || data.Shell != "/bin/sh" {
		t.Fatalf("snapshot mismatch: %+v", data)
	}
	if len(data.Env) != 2 {
		t.Fatalf("expected 2 env entries, got %d", len(data.Env))
	}

	tr.dir = filepath.Join(dir, "other")
	tr.env = []string{"X=1"}
	tr.shell = "/bin/bash"

	if err := tr.Restore(ctx, snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if tr.dir != data.Dir {
		t.Fatalf("expected dir %q, got %q", data.Dir, tr.dir)
	}
	if tr.shell != data.Shell {
		t.Fatalf("expected shell %q, got %q", data.Shell, tr.shell)
	}
	if len(tr.env) != len(data.Env) || tr.env[0] != data.Env[0] {
		t.Fatalf("expected env %v, got %v", data.Env, tr.env)
	}
}

func TestTerminalResourceSnapshotHashDeterministic(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()

	a, err := NewTerminalResource(dir, []string{"A=1", "B=2"}, "/bin/sh")
	if err != nil {
		t.Fatalf("NewTerminalResource: %v", err)
	}
	b, err := NewTerminalResource(dir, []string{"B=2", "A=1"}, "/bin/sh")
	if err != nil {
		t.Fatalf("NewTerminalResource: %v", err)
	}

	snapA, err := a.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	snapB, err := b.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapA.Hash() != snapB.Hash() {
		t.Fatalf("expected identical hashes for shuffled env, got %q vs %q", snapA.Hash(), snapB.Hash())
	}
}

func TestTerminalResourceRestoreRejectsForeignSnapshot(t *testing.T) {
	ctx := t.Context()
	tr, err := NewTerminalResource(t.TempDir(), nil, "")
	if err != nil {
		t.Fatalf("NewTerminalResource: %v", err)
	}
	if err := tr.Restore(ctx, nil); err == nil {
		t.Fatal("expected error restoring a nil snapshot")
	}
	if err := tr.Restore(ctx, fakeSnapshot{}); err == nil {
		t.Fatal("expected error restoring a foreign snapshot type")
	}
}

func TestTerminalResourceRun(t *testing.T) {
	ctx := t.Context()
	tr, err := NewTerminalResource(t.TempDir(), nil, "")
	if err != nil {
		t.Fatalf("NewTerminalResource: %v", err)
	}
	out, err := tr.Run(ctx, "echo hello")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("unexpected output %q", out)
	}
}

func TestTerminalResourceRunFailure(t *testing.T) {
	ctx := t.Context()
	tr, err := NewTerminalResource(t.TempDir(), nil, "")
	if err != nil {
		t.Fatalf("NewTerminalResource: %v", err)
	}
	if _, err := tr.Run(ctx, "exit 1"); err == nil {
		t.Fatal("expected error for a failing command")
	}
}
