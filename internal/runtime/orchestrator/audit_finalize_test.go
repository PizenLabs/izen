package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/core/domain/evidence"
	"github.com/PizenLabs/izen/internal/events"
	eventaudit "github.com/PizenLabs/izen/internal/events/audit"
	"github.com/PizenLabs/izen/internal/runtime/authorization"
)

// failingFlusher simulates an I/O disk write error / read-only permission on
// .izen/audit/events.ndjson at session finalization time.
type failingFlusher struct {
	err   error
	calls int
}

func (f *failingFlusher) Flush() error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	return errors.New("orchestrator test: simulated audit disk write failure")
}

// workingFlusher records finalization flushes that succeed.
type workingFlusher struct {
	calls int
}

func (f *workingFlusher) Flush() error {
	f.calls++
	return nil
}

// TestAuditFlushFailureInvalidatesSuccess is the adversarial test for the
// Audit Persistence Failure Invariant: injecting a simulated write/permission
// error on .izen/audit/events.ndjson right before session finalization MUST
// force the runtime to return Completed=false / Verdict != VerdictPass with
// ErrAuditPersistenceFailed — regardless of whether file mutations succeeded.
// The disk mutation MUST stand (Mutation Non-Rollback Isolation) while
// evidence integrity is marked compromised.
func TestAuditFlushFailureInvalidatesSuccess(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeFile(t, dir, "README.md", "# Old\n")
	ref := targetRef(dir, "README.md", true)

	orch, _ := newStack(ref)
	orch.WithAuditFlusher(&failingFlusher{})
	provider := &stubProvider{proposal: happyProposal("p-audit-fail", ref, "# Updated despite audit failure\n")}
	bridge := &stubBridge{actions: []authorization.ApprovalAction{authorization.ActionExecute}}

	res, err := orch.RunCycle(context.Background(), baseRequest(ref), provider, bridge, OrchestratorConfig{})

	// The error MUST carry the explicit audit cause — never swallowed.
	if err == nil {
		t.Fatal("expected ErrAuditPersistenceFailed, got nil error")
	}
	if !errors.Is(err, ErrAuditPersistenceFailed) {
		t.Fatalf("error = %v, want ErrAuditPersistenceFailed in chain", err)
	}

	if res == nil {
		t.Fatal("expected non-nil result carrying the degraded terminal state")
	}

	// Mutation Non-Rollback Isolation: the filesystem mutation that actually
	// occurred on disk MUST NOT be reverted or disguised.
	if !res.Committed {
		t.Error("Committed = false, want true (failed audit flush must not roll back the disk mutation)")
	}
	if got := readFile(t, path); got != "# Updated despite audit failure\n" {
		t.Errorf("content = %q, want mutated content (no rollback on audit failure)", got)
	}
	if !res.EvidenceCompromised {
		t.Error("EvidenceCompromised = false, want true (evidence integrity must be marked compromised)")
	}
	if res.AuditError == nil || !errors.Is(res.AuditError, ErrAuditPersistenceFailed) {
		t.Errorf("AuditError = %v, want ErrAuditPersistenceFailed", res.AuditError)
	}

	// Truthful State Transition: success is structurally invalidated.
	if res.Completed {
		t.Error("Completed = true, want false on audit persistence failure")
	}
	if res.Verdict == evidence.VerdictPassed {
		t.Errorf("Verdict = %v, want != VerdictPassed (Failed/Inconclusive)", res.Verdict)
	}
	if res.Terminal.Workflow == domain.StateVerified {
		t.Errorf("Terminal.Workflow = %v, want StateFailed (never StateVerified on audit failure)", res.Terminal.Workflow)
	}
	if res.Terminal.Verdict == evidence.VerdictPass {
		t.Errorf("Terminal.Verdict = %v, want != VerdictPass", res.Terminal.Verdict)
	}
	// At least one structural invalidator must hold: an invalid
	// TerminalState product or a non-PASS verdict.
	if res.Terminal.Valid() && res.Verdict == evidence.VerdictPassed {
		t.Error("terminal state claims success (Valid + VerdictPassed) despite audit persistence failure")
	}
}

// TestAuditFlushFailureViaRealLoggerInjectedIOError drives the same invariant
// through the REAL async audit substrate: an events/audit AuditLogger rooted
// at .izen/audit with a deterministically injected I/O error right before
// session finalization. This simulates a read-only permission / disk write
// error on events.ndjson without chmod races.
func TestAuditFlushFailureViaRealLoggerInjectedIOError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeFile(t, dir, "README.md", "# Old\n")
	ref := targetRef(dir, "README.md", true)

	bus := events.NewBus(64)
	defer bus.Close()
	auditDir := filepath.Join(dir, ".izen", "audit")
	logger, err := eventaudit.NewLogger(auditDir, bus)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	if err := logger.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = logger.Close() }()

	// One real envelope crosses the bus so the trail is non-empty, then the
	// I/O error is injected right before session finalization.
	bus.Publish(events.NewExecutionStarted("req-audit", "build", "update README", ""))
	logger.InjectWriteError(errors.New("simulated disk write error: read-only file system"))

	orch, _ := newStack(ref)
	orch.WithAuditFlusher(logger)
	provider := &stubProvider{proposal: happyProposal("p-audit-io", ref, "# Mutated\n")}
	bridge := &stubBridge{actions: []authorization.ApprovalAction{authorization.ActionExecute}}

	res, runErr := orch.RunCycle(context.Background(), baseRequest(ref), provider, bridge, OrchestratorConfig{})

	if runErr == nil || !errors.Is(runErr, ErrAuditPersistenceFailed) {
		t.Fatalf("error = %v, want ErrAuditPersistenceFailed", runErr)
	}
	if res == nil {
		t.Fatal("expected non-nil degraded result")
	}
	// Isolation holds through the real substrate too.
	if got := readFile(t, path); got != "# Mutated\n" {
		t.Errorf("content = %q, want mutated content (no rollback)", got)
	}
	if res.Completed || res.Verdict == evidence.VerdictPassed {
		t.Errorf("terminal not invalidated: Completed=%v Verdict=%v", res.Completed, res.Verdict)
	}
	if res.Terminal.Valid() && res.Verdict == evidence.VerdictPassed {
		t.Error("terminal claims success despite injected I/O failure")
	}
}

// TestAuditFlushSuccessYieldsVerifiedTerminal pins the happy path: a working
// synchronous flush preserves StateVerified / VerdictPass / Completed=true.
func TestAuditFlushSuccessYieldsVerifiedTerminal(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeFile(t, dir, "README.md", "# Old\n")
	ref := targetRef(dir, "README.md", true)

	orch, _ := newStack(ref)
	flusher := &workingFlusher{}
	orch.WithAuditFlusher(flusher)
	provider := &stubProvider{proposal: happyProposal("p-audit-ok", ref, "# New\n")}
	bridge := &stubBridge{actions: []authorization.ApprovalAction{authorization.ActionExecute}}

	res, err := orch.RunCycle(context.Background(), baseRequest(ref), provider, bridge, OrchestratorConfig{})
	if err != nil {
		t.Fatalf("RunCycle: %v", err)
	}
	if flusher.calls != 1 {
		t.Errorf("flush calls = %d, want exactly 1 terminal finalization flush", flusher.calls)
	}
	if res == nil || !res.Committed || !res.Completed {
		t.Fatalf("expected committed+completed success, got %+v", res)
	}
	if res.Verdict != evidence.VerdictPassed {
		t.Errorf("Verdict = %v, want VerdictPassed", res.Verdict)
	}
	if !res.Terminal.Valid() {
		t.Errorf("Terminal = %s should be Valid on success", res.Terminal.String())
	}
	if res.AuditError != nil || res.EvidenceCompromised {
		t.Errorf("AuditError = %v compromised = %v, want clean success", res.AuditError, res.EvidenceCompromised)
	}
	if got := readFile(t, path); got != "# New\n" {
		t.Errorf("content = %q, want mutated", got)
	}
}

// TestEvaluateTerminalStateMatrix pins the pure mapping: audit failure always
// degrades, commit success verifies, and no path yields Verified+Pass without
// both commit and durable audit.
func TestEvaluateTerminalStateMatrix(t *testing.T) {
	t.Parallel()

	terminal, verdict := EvaluateTerminalState(true, errors.New("disk full"))
	if terminal.Completed || verdict == evidence.VerdictPassed {
		t.Errorf("audit failure must invalidate: Completed=%v Verdict=%v", terminal.Completed, verdict)
	}
	if terminal.Workflow == domain.StateVerified || terminal.Verdict == evidence.VerdictPass {
		t.Errorf("audit failure must not be Verified/Pass: %s", terminal.String())
	}
	if terminal.Valid() && verdict == evidence.VerdictPassed {
		t.Error("audit failure must not present as valid success")
	}

	terminal, verdict = EvaluateTerminalState(true, nil)
	if !terminal.Completed || verdict != evidence.VerdictPassed {
		t.Errorf("commit success must verify: %+v %v", terminal, verdict)
	}
	if !terminal.Valid() {
		t.Errorf("success terminal should be Valid: %s", terminal.String())
	}

	terminal, verdict = EvaluateTerminalState(false, nil)
	if terminal.Completed || verdict == evidence.VerdictPassed {
		t.Errorf("non-commit must not verify: %+v %v", terminal, verdict)
	}
}

// TestAuditFlushErrorNeverSwallowed ensures the orchestrator surfaces the
// flush error to the caller on the inspect path too (no silent log-and-continue).
func TestAuditFlushErrorNeverSwallowed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ref := targetRef(dir, "README.md", true)
	_ = os.WriteFile(ref.Canonical, []byte("# Old\n"), 0o644)

	orch, _ := newStack(ref)
	flusher := &failingFlusher{}
	orch.WithAuditFlusher(flusher)
	provider := &stubProvider{proposal: happyProposal("p-audit-inspect", ref, "# New\n")}
	bridge := &stubBridge{actions: []authorization.ApprovalAction{authorization.ActionInspect}}

	res, err := orch.RunCycle(context.Background(), baseRequest(ref), provider, bridge, OrchestratorConfig{})
	if err == nil || !errors.Is(err, ErrAuditPersistenceFailed) {
		t.Fatalf("inspect-path flush error swallowed: err = %v", err)
	}
	if res == nil || res.Completed || res.Verdict == evidence.VerdictPassed {
		t.Fatalf("inspect terminal not degraded: %+v", res)
	}
}
