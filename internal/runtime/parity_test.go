package runtime_test

// STEP 2 parity tests: RuntimeEngine support for domain execution contracts.
//
// ZERO-MOCK for the store path: every assertion runs against a real
// RuntimeEngine on a temp workdir with a real ledger.ndjson.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	domainorch "github.com/PizenLabs/izen/internal/domain/orchestration"
	domaintask "github.com/PizenLabs/izen/internal/domain/task"
	izenruntime "github.com/PizenLabs/izen/internal/runtime"
	"github.com/PizenLabs/izen/internal/runtime/durable"
	"github.com/PizenLabs/izen/internal/runtime/ephemeral"
	"github.com/PizenLabs/izen/internal/runtime/scopeguard"
)

// parityFixture wires a real RuntimeEngine bound to task-parity-1 with the
// build workspace (WRITE allowed) on an isolated temp workdir.
func parityFixture(t *testing.T) (*izenruntime.RuntimeEngine, string) {
	t.Helper()
	dir := t.TempDir()
	seed := filepath.Join(dir, "pkg", "auth", "login.go")
	if err := os.MkdirAll(filepath.Dir(seed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(seed, []byte("package auth\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	eng, err := izenruntime.NewRuntimeEngine(dir, "task-parity-1", []string{"pkg/auth"},
		scopeguard.WorkspaceBuild, scopeguard.NewSimpleBudget(10, 1000, 100000),
		ephemeral.NewWorkerRouter(defaultWorkers()), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Store().CreateTask("task-parity-1", "parity", []string{"pkg/auth"}); err != nil {
		t.Fatal(err)
	}
	return eng, dir
}

func parityProposal(targets []string, opID string) scopeguard.Proposal {
	return scopeguard.Proposal{
		ID: "pp-" + opID, TaskID: "task-parity-1", Workspace: scopeguard.WorkspaceBuild,
		Op: scopeguard.OpWrite, TargetFiles: targets,
		OperationID: opID, StepID: "step-1", Detail: "parity write",
	}
}

// ExecuteProposalResult maps a committed proposal onto a terminal
// TaskResult carrying the durable TaskState as Data.
func TestParityExecuteProposalResultCompleted(t *testing.T) {
	ctx := context.Background()
	eng, dir := parityFixture(t)

	res := eng.ExecuteProposalResult(ctx,
		parityProposal([]string{"pkg/auth/login.go"}, "op-ok"),
		[]string{"pkg/auth/login.go"},
		engineWriteEffect(t, dir, "pkg/auth/login.go", "package auth\n// parity\n"),
		okVerifier)
	if res.Status != domaintask.ExecStatusCompleted {
		t.Fatalf("status = %s, want completed (err=%v)", res.Status, res.Error)
	}
	if res.Error != nil {
		t.Fatalf("unexpected error: %v", res.Error)
	}
	st, ok := res.Data.(durable.TaskState)
	if !ok || st.ID != "task-parity-1" {
		t.Fatalf("Data carries no TaskState for task-parity-1: %#v", res.Data)
	}
	if !res.Status.IsTerminal() {
		t.Fatal("result status is not terminal")
	}
}

// A scope denial maps onto ExecStatusFailed (never Completed), preserving
// the denial cause.
func TestParityExecuteProposalResultDenial(t *testing.T) {
	ctx := context.Background()
	eng, _ := parityFixture(t)

	res := eng.ExecuteProposalResult(ctx,
		parityProposal([]string{"pkg/billing/invoice.go"}, "op-deny"),
		[]string{"pkg/auth/login.go"},
		func(context.Context) (string, error) { return "x", nil },
		okVerifier)
	if res.Status != domaintask.ExecStatusFailed {
		t.Fatalf("status = %s, want failed", res.Status)
	}
	if res.Error == nil || !strings.Contains(res.Error.Error(), "scope violation") {
		t.Fatalf("expected scope violation cause, got %v", res.Error)
	}
}

// A canceled context maps onto ExecStatusCanceled.
func TestParityExecuteProposalResultCanceled(t *testing.T) {
	eng, dir := parityFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res := eng.ExecuteProposalResult(ctx,
		parityProposal([]string{"pkg/auth/login.go"}, "op-cancel"),
		[]string{"pkg/auth/login.go"},
		engineWriteEffect(t, dir, "pkg/auth/login.go", "package auth\n// cancel\n"),
		okVerifier)
	if res.Status != domaintask.ExecStatusCanceled && res.Status != domaintask.ExecStatusFailed {
		t.Fatalf("status = %s, want canceled or failed", res.Status)
	}
	if res.Error == nil {
		t.Fatal("expected a cancellation cause")
	}
}

// Valid hops persist PHASE_TRANSITION (+ WORKSPACE_SWITCHED for mapped
// phases); forbidden edges return *TransitionError and persist nothing.
func TestParityTransitionPhase(t *testing.T) {
	eng, _ := parityFixture(t)

	if err := eng.TransitionPhase(domainorch.PhaseIdle, domainorch.PhasePlan); err != nil {
		t.Fatalf("Idle -> Plan: %v", err)
	}
	raw, err := os.ReadFile(eng.Store().LedgerPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), string(durable.EventPhaseTransition)) {
		t.Fatalf("ledger missing PHASE_TRANSITION:\n%s", raw)
	}
	if !strings.Contains(string(raw), string(durable.EventWorkspaceSwitched)) {
		t.Fatalf("ledger missing WORKSPACE_SWITCHED for mapped phase:\n%s", raw)
	}

	before := string(raw)
	err = eng.TransitionPhase(domainorch.PhaseIdle, domainorch.PhaseReview)
	var te *domainorch.TransitionError
	if !errors.As(err, &te) {
		t.Fatalf("Idle -> Review err = %v, want *TransitionError", err)
	}
	after, _ := os.ReadFile(eng.Store().LedgerPath())
	if string(after) != before {
		t.Fatal("forbidden hop persisted ledger lineage")
	}

	// Unknown phases are rejected fail-closed.
	if err := eng.TransitionPhase(domainorch.PhaseIdle, domainorch.Phase(99)); err == nil {
		t.Fatal("expected error for unknown phase, got nil")
	}
}

// Capability validation is fail-closed: unknown ops and policy-denied ops
// error; the build workspace grants WRITE.
func TestParityValidateCapability(t *testing.T) {
	eng, _ := parityFixture(t)

	if err := eng.ValidateCapability(scopeguard.OpWrite, "pkg/auth/login.go"); err != nil {
		t.Fatalf("build workspace should grant WRITE: %v", err)
	}
	if err := eng.ValidateCapability(scopeguard.OpClass("EXECUTE")); err == nil {
		t.Fatal("expected fail-closed error for unknown capability, got nil")
	}
	var nilEng *izenruntime.RuntimeEngine
	if err := nilEng.ValidateCapability(scopeguard.OpRead); err == nil {
		t.Fatal("expected fail-closed error for nil engine, got nil")
	}
}

// AnalyzeFailure preserves the timeout cause for errors.Is and carries the
// failure taxonomy as Data.
func TestParityAnalyzeFailure(t *testing.T) {
	eng, _ := parityFixture(t)

	got := eng.AnalyzeFailure(domaintask.ErrTaskTimeout)
	if got.Status != domaintask.ExecStatusFailed {
		t.Fatalf("status = %s, want failed", got.Status)
	}
	if !errors.Is(got.Error, domaintask.ErrTaskTimeout) {
		t.Fatalf("expected ErrTaskTimeout cause, got %v", got.Error)
	}

	got = eng.AnalyzeFailure(context.Canceled)
	if got.Status != domaintask.ExecStatusCanceled {
		t.Fatalf("status = %s, want canceled", got.Status)
	}

	if got := eng.AnalyzeFailure(nil); got.Status != domaintask.ExecStatusCompleted {
		t.Fatalf("nil error status = %s, want completed", got.Status)
	}
}
