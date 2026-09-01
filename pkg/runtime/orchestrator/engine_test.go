package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/pkg/projection/diff"
	"github.com/PizenLabs/izen/pkg/runtime/authorization"
	runtimectx "github.com/PizenLabs/izen/pkg/runtime/context"
	"github.com/PizenLabs/izen/pkg/runtime/executor"
	"github.com/PizenLabs/izen/pkg/runtime/preflight"
	"github.com/PizenLabs/izen/pkg/runtime/target"
)

// fakeResolver is a scriptable target.Resolver for isolating the orchestrator
// from real VCS/filesystem resolution.
type fakeResolver struct {
	ref *target.TargetRef
	err error
}

func (f *fakeResolver) Resolve(_ string, _ string) (*target.TargetRef, error) {
	return f.ref, f.err
}

// stubProvider is a scriptable ProposalProvider that records the compiled
// request it was given.
type stubProvider struct {
	proposal *executor.ProposedMutation
	err      error
	got      *preflight.CompiledRequest
	calls    int
}

func (p *stubProvider) GenerateProposal(_ context.Context, req *preflight.CompiledRequest) (*executor.ProposedMutation, error) {
	p.calls++
	p.got = req
	return p.proposal, p.err
}

// stubBridge is a scriptable UIProjectionBridge. WaitForApproval stamps the
// armed epoch onto the next scripted action; the stale flag forces it to emit
// one epoch older so the gate rejects it as stale. The log records the order
// of bridge invocations to verify the arming invariant.
type stubBridge struct {
	actions   []authorization.ApprovalAction
	stale     bool
	renderErr error
	waitErr   error

	rendered   []diff.MutationEvidence
	armedEpoch []authorization.InteractionEpoch
	epoch      authorization.InteractionEpoch
	waitCalls  int
	log        []string
}

func (b *stubBridge) RenderProposal(evidence diff.MutationEvidence, _ diff.ViewportConfig) error {
	b.log = append(b.log, "render")
	b.rendered = append(b.rendered, evidence)
	return b.renderErr
}

func (b *stubBridge) OnSessionArmed(epoch authorization.InteractionEpoch) {
	b.log = append(b.log, "arm")
	b.epoch = epoch
	b.armedEpoch = append(b.armedEpoch, epoch)
}

func (b *stubBridge) WaitForApproval(_ context.Context) (authorization.ApprovalEvent, error) {
	b.log = append(b.log, "wait")
	b.waitCalls++
	if b.waitErr != nil {
		return authorization.ApprovalEvent{}, b.waitErr
	}
	action := authorization.ActionCancel
	if len(b.actions) > 0 {
		action = b.actions[0]
		if len(b.actions) > 1 {
			b.actions = b.actions[1:]
		}
	}
	epoch := b.epoch
	if b.stale {
		epoch-- // strictly one epoch older: always stale for the current session
	}
	return authorization.ApprovalEvent{Epoch: epoch, Action: action}, nil
}

// targetRef builds a TargetRef pointing at an absolute path inside dir.
func targetRef(dir, name string, exists bool) *target.TargetRef {
	path := filepath.Join(dir, name)
	return &target.TargetRef{Raw: name, Canonical: path, Exists: exists, Tracked: false, Source: target.ResolutionRaw}
}

// writeFile writes content into dir and fails the test on error.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// readFile reads content from path and fails the test on error.
func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// assertNoTempOrphans fails the test if any .tmp.izen.* file survives in dir.
func assertNoTempOrphans(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp.izen.") {
			t.Errorf("orphaned temp file %q in %s", e.Name(), dir)
		}
	}
}

// newStack wires an Orchestrator with a real preflight engine (scriptable
// resolver), validator, executor, and a zero-delay approval gate.
func newStack(ref *target.TargetRef) (*Orchestrator, *authorization.ApprovalGate) {
	gate := authorization.NewGate(authorization.WithMinDelayWindow(0))
	pf := preflight.NewEngine(&fakeResolver{ref: ref}, runtimectx.NewCompiler())
	orch := NewOrchestrator(pf, executor.NewValidator(), executor.NewExecutor(), gate)
	return orch, gate
}

// happyProposal returns a valid whole-file proposal for targetPath.
func happyProposal(id string, ref *target.TargetRef, content string) *executor.ProposedMutation {
	return &executor.ProposedMutation{
		ProposalID: id,
		TargetRef:  ref,
		RawPatch:   content,
	}
}

// baseRequest returns a preflight request targeting ref with a valid budget.
func baseRequest(ref *target.TargetRef) preflight.PreflightRequest {
	return preflight.PreflightRequest{
		RawInput:    "update " + ref.Canonical,
		WorkDir:     filepath.Dir(ref.Canonical),
		TokenBudget: 1000,
	}
}

func TestSuccessfulExecutionCycle(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeFile(t, dir, "README.md", "# Old\n")
	ref := targetRef(dir, "README.md", true)

	orch, gate := newStack(ref)
	provider := &stubProvider{proposal: happyProposal("p-exec", ref, "# Updated by orchestrator\n")}
	bridge := &stubBridge{actions: []authorization.ApprovalAction{authorization.ActionExecute}}

	res, err := orch.RunCycle(context.Background(), baseRequest(ref), provider, bridge, OrchestratorConfig{})
	if err != nil {
		t.Fatalf("RunCycle returned error: %v", err)
	}

	if res == nil {
		t.Fatal("expected a non-nil result")
	}
	if !res.Committed {
		t.Error("expected Committed = true")
	}
	if res.ProposalID != "p-exec" {
		t.Errorf("ProposalID = %q, want %q", res.ProposalID, "p-exec")
	}
	if res.Target != path {
		t.Errorf("Target = %q, want %q", res.Target, path)
	}
	if res.Action != authorization.ActionExecute {
		t.Errorf("Action = %v, want ActionExecute", res.Action)
	}
	if res.Evidence.Added != 1 || res.Evidence.Deleted != 1 {
		t.Errorf("evidence = +%d -%d, want +1 -1", res.Evidence.Added, res.Evidence.Deleted)
	}

	// The file must be updated atomically on disk.
	if got := readFile(t, path); got != "# Updated by orchestrator\n" {
		t.Errorf("content = %q, want %q", got, "# Updated by orchestrator\n")
	}
	assertNoTempOrphans(t, dir)

	// UI projection and arming notifications must have fired.
	if len(bridge.rendered) != 1 {
		t.Fatalf("RenderProposal calls = %d, want 1", len(bridge.rendered))
	}
	if bridge.rendered[0].TargetFile != path {
		t.Errorf("rendered target = %q, want %q", bridge.rendered[0].TargetFile, path)
	}
	if len(bridge.armedEpoch) != 1 || bridge.armedEpoch[0] != gate.CurrentSession().Epoch {
		t.Errorf("armed epoch = %v, want session epoch %v", bridge.armedEpoch, gate.CurrentSession().Epoch)
	}
	if gate.CurrentSession().State != authorization.StateAuthorized {
		t.Errorf("session state = %v, want StateAuthorized", gate.CurrentSession().State)
	}
}

func TestStaleEventPreemptionInOrchestrator(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeFile(t, dir, "README.md", "# Old\n")
	ref := targetRef(dir, "README.md", true)

	orch, gate := newStack(ref)
	// Establish a real prior epoch (N-1) before the cycle opens epoch N.
	prev := gate.NewSession("previous", authorization.ActionExecute)
	_ = prev

	provider := &stubProvider{proposal: happyProposal("p-stale", ref, "# Mutated\n")}
	bridge := &stubBridge{stale: true, actions: []authorization.ApprovalAction{authorization.ActionExecute}}

	_, err := orch.RunCycle(context.Background(), baseRequest(ref), provider, bridge, OrchestratorConfig{})
	if err == nil {
		t.Fatal("expected an error for a stale approval event")
	}
	if !errors.Is(err, authorization.ErrStaleEpoch) {
		t.Errorf("error = %v, want authorization.ErrStaleEpoch", err)
	}

	// No file modifications may occur and the workspace must remain untouched.
	if got := readFile(t, path); got != "# Old\n" {
		t.Errorf("content = %q, want original %q", got, "# Old\n")
	}
	if gate.CurrentSession().State != authorization.StateArmed {
		t.Errorf("session state = %v, want StateArmed", gate.CurrentSession().State)
	}
}

func TestValidationFailureAbortsCycle(t *testing.T) {
	t.Parallel()

	ref := &target.TargetRef{Raw: "../../etc/passwd", Canonical: "../../etc/passwd", Exists: false, Source: target.ResolutionRaw}
	orch, gate := newStack(ref)
	provider := &stubProvider{proposal: happyProposal("p-bad", ref, "root:x:0:0:")}
	bridge := &stubBridge{}

	res, err := orch.RunCycle(context.Background(), baseRequest(ref), provider, bridge, OrchestratorConfig{})
	if res != nil {
		t.Fatalf("expected nil result, got %+v", res)
	}
	if !errors.Is(err, ErrProposalValidationFailed) {
		t.Errorf("error = %v, want ErrProposalValidationFailed", err)
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Errorf("error = %q, want traversal description", err.Error())
	}

	// The cycle must halt at Step 3: no approval session and no snapshot may
	// exist, and no authorization event may have been evaluated.
	if gate.CurrentSession() != nil {
		t.Errorf("gate has a session %+v, want none", gate.CurrentSession())
	}
	if len(bridge.log) != 0 {
		t.Errorf("bridge log = %v, want empty (no projection/arming)", bridge.log)
	}
}

func TestRejectionLeavesFilesystemUnchanged(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeFile(t, dir, "README.md", "# Original\n")
	ref := targetRef(dir, "README.md", true)

	orch, gate := newStack(ref)
	provider := &stubProvider{proposal: happyProposal("p-cancel", ref, "# Would have changed\n")}
	bridge := &stubBridge{actions: []authorization.ApprovalAction{authorization.ActionCancel}}

	res, err := orch.RunCycle(context.Background(), baseRequest(ref), provider, bridge, OrchestratorConfig{})
	if res == nil {
		t.Fatal("expected a non-nil result for a user cancellation")
	}
	if res.Committed {
		t.Error("expected Committed = false on cancellation")
	}
	if res.Action != authorization.ActionCancel {
		t.Errorf("Action = %v, want ActionCancel", res.Action)
	}
	if !errors.Is(err, ErrExecutionRejected) {
		t.Errorf("error = %v, want ErrExecutionRejected", err)
	}

	// The target file must remain identical to its pre-cycle snapshot.
	if got := readFile(t, path); got != "# Original\n" {
		t.Errorf("content = %q, want untouched %q", got, "# Original\n")
	}
	if gate.CurrentSession().State != authorization.StateRejected {
		t.Errorf("session state = %v, want StateRejected", gate.CurrentSession().State)
	}
}

func TestInspectActionAuthorizesWithoutExecuting(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeFile(t, dir, "README.md", "# Original\n")
	ref := targetRef(dir, "README.md", true)

	orch, gate := newStack(ref)
	provider := &stubProvider{proposal: happyProposal("p-inspect", ref, "# Would have changed\n")}
	bridge := &stubBridge{actions: []authorization.ApprovalAction{authorization.ActionInspect}}

	res, err := orch.RunCycle(context.Background(), baseRequest(ref), provider, bridge, OrchestratorConfig{})
	if err != nil {
		t.Fatalf("inspection must not be an error, got %v", err)
	}
	if res.Committed {
		t.Error("expected Committed = false for inspect-only authorization")
	}
	if res.Action != authorization.ActionInspect {
		t.Errorf("Action = %v, want ActionInspect", res.Action)
	}
	if got := readFile(t, path); got != "# Original\n" {
		t.Errorf("content = %q, want untouched %q", got, "# Original\n")
	}
	if gate.CurrentSession().State != authorization.StateAuthorized {
		t.Errorf("session state = %v, want StateAuthorized", gate.CurrentSession().State)
	}
}

func TestActionNoneKeepsWaitingForExplicitDecision(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := writeFile(t, dir, "README.md", "# Old\n")
	ref := targetRef(dir, "README.md", true)

	orch, _ := newStack(ref)
	provider := &stubProvider{proposal: happyProposal("p-loop", ref, "# New\n")}
	bridge := &stubBridge{actions: []authorization.ApprovalAction{authorization.ActionNone, authorization.ActionExecute}}

	res, err := orch.RunCycle(context.Background(), baseRequest(ref), provider, bridge, OrchestratorConfig{})
	if err != nil {
		t.Fatalf("RunCycle returned error: %v", err)
	}
	if !res.Committed {
		t.Error("expected Committed = true after the second explicit decision")
	}
	if bridge.waitCalls != 2 {
		t.Errorf("WaitForApproval calls = %d, want 2 (no-op followed by decision)", bridge.waitCalls)
	}
	if got := readFile(t, path); got != "# New\n" {
		t.Errorf("content = %q, want %q", got, "# New\n")
	}
}

func TestArmingInvariantOrdering(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, dir, "README.md", "# Old\n")
	ref := targetRef(dir, "README.md", true)

	orch, gate := newStack(ref)
	provider := &stubProvider{proposal: happyProposal("p-order", ref, "# New\n")}
	bridge := &stubBridge{actions: []authorization.ApprovalAction{authorization.ActionExecute}}

	if _, err := orch.RunCycle(context.Background(), baseRequest(ref), provider, bridge, OrchestratorConfig{}); err != nil {
		t.Fatalf("RunCycle returned error: %v", err)
	}

	// The state-machine arming invariant: the diff is rendered, then the
	// session is armed, and only then are authorization events evaluated.
	want := []string{"render", "arm", "wait"}
	if len(bridge.log) != len(want) {
		t.Fatalf("bridge log = %v, want %v", bridge.log, want)
	}
	for i, step := range want {
		if bridge.log[i] != step {
			t.Errorf("bridge log[%d] = %q, want %q (order %v)", i, bridge.log[i], step, want)
		}
	}
	if len(bridge.armedEpoch) != 1 || bridge.armedEpoch[0] == 0 {
		t.Errorf("armed epoch = %v, want a non-zero epoch", bridge.armedEpoch)
	}
	if gate.CurrentSession().State != authorization.StateAuthorized {
		t.Errorf("session state = %v, want StateAuthorized", gate.CurrentSession().State)
	}
}

func TestTokenBudgetFallbackFromConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ref := targetRef(dir, "new.txt", false)

	orch, _ := newStack(ref)
	provider := &stubProvider{proposal: happyProposal("p-budget", ref, "content\n")}
	bridge := &stubBridge{actions: []authorization.ApprovalAction{authorization.ActionExecute}}

	// A zero request budget must fall back to the config token budget.
	req := baseRequest(ref)
	req.TokenBudget = 0
	if _, err := orch.RunCycle(context.Background(), req, provider, bridge, OrchestratorConfig{TokenBudget: 500}); err != nil {
		t.Fatalf("RunCycle returned error: %v", err)
	}
	if provider.got == nil || provider.got.Context == nil {
		t.Fatal("provider received no compiled request")
	}
	if provider.got.Context.Budget != 500 {
		t.Errorf("compiled budget = %d, want 500 from config", provider.got.Context.Budget)
	}

	// An explicit request budget must win over the config.
	provider2 := &stubProvider{proposal: happyProposal("p-budget2", ref, "content\n")}
	bridge2 := &stubBridge{actions: []authorization.ApprovalAction{authorization.ActionExecute}}
	req2 := baseRequest(ref)
	req2.TokenBudget = 250
	if _, err := orch.RunCycle(context.Background(), req2, provider2, bridge2, OrchestratorConfig{TokenBudget: 500}); err != nil {
		t.Fatalf("RunCycle returned error: %v", err)
	}
	if provider2.got.Context.Budget != 250 {
		t.Errorf("compiled budget = %d, want 250 from request", provider2.got.Context.Budget)
	}
}

func TestRunCycleErrorPaths(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ref := targetRef(dir, "README.md", true)

	t.Run("nil orchestrator receiver", func(t *testing.T) {
		t.Parallel()
		var nilOrch *Orchestrator
		_, err := nilOrch.RunCycle(context.Background(), preflight.PreflightRequest{}, nil, nil, OrchestratorConfig{})
		if err == nil || !strings.Contains(err.Error(), "nil Orchestrator") {
			t.Errorf("expected nil orchestrator error, got %v", err)
		}
	})

	t.Run("nil preflight engine", func(t *testing.T) {
		t.Parallel()
		gate := authorization.NewGate(authorization.WithMinDelayWindow(0))
		orch := NewOrchestrator(nil, executor.NewValidator(), executor.NewExecutor(), gate)
		_, err := orch.RunCycle(context.Background(), baseRequest(ref), &stubProvider{}, &stubBridge{}, OrchestratorConfig{})
		if err == nil || !strings.Contains(err.Error(), "preflight") {
			t.Errorf("expected preflight wiring error, got %v", err)
		}
	})

	t.Run("nil validator", func(t *testing.T) {
		t.Parallel()
		gate := authorization.NewGate(authorization.WithMinDelayWindow(0))
		orch := NewOrchestrator(preflight.NewEngine(&fakeResolver{ref: ref}, runtimectx.NewCompiler()), nil, executor.NewExecutor(), gate)
		_, err := orch.RunCycle(context.Background(), baseRequest(ref), &stubProvider{}, &stubBridge{}, OrchestratorConfig{})
		if err == nil || !strings.Contains(err.Error(), "validator") {
			t.Errorf("expected validator wiring error, got %v", err)
		}
	})

	t.Run("nil executor", func(t *testing.T) {
		t.Parallel()
		gate := authorization.NewGate(authorization.WithMinDelayWindow(0))
		orch := NewOrchestrator(preflight.NewEngine(&fakeResolver{ref: ref}, runtimectx.NewCompiler()), executor.NewValidator(), nil, gate)
		_, err := orch.RunCycle(context.Background(), baseRequest(ref), &stubProvider{}, &stubBridge{}, OrchestratorConfig{})
		if err == nil || !strings.Contains(err.Error(), "executor") {
			t.Errorf("expected executor wiring error, got %v", err)
		}
	})

	t.Run("nil gate", func(t *testing.T) {
		t.Parallel()
		orch := NewOrchestrator(preflight.NewEngine(&fakeResolver{ref: ref}, runtimectx.NewCompiler()), executor.NewValidator(), executor.NewExecutor(), nil)
		_, err := orch.RunCycle(context.Background(), baseRequest(ref), &stubProvider{}, &stubBridge{}, OrchestratorConfig{})
		if err == nil || !strings.Contains(err.Error(), "approval gate") {
			t.Errorf("expected gate wiring error, got %v", err)
		}
	})

	t.Run("nil provider", func(t *testing.T) {
		t.Parallel()
		orch, _ := newStack(ref)
		_, err := orch.RunCycle(context.Background(), baseRequest(ref), nil, &stubBridge{}, OrchestratorConfig{})
		if err == nil || !strings.Contains(err.Error(), "proposal provider") {
			t.Errorf("expected provider error, got %v", err)
		}
	})

	t.Run("nil UI bridge", func(t *testing.T) {
		t.Parallel()
		orch, _ := newStack(ref)
		_, err := orch.RunCycle(context.Background(), baseRequest(ref), &stubProvider{}, nil, OrchestratorConfig{})
		if err == nil || !strings.Contains(err.Error(), "UI projection bridge") {
			t.Errorf("expected UI bridge error, got %v", err)
		}
	})

	t.Run("preflight error propagates", func(t *testing.T) {
		t.Parallel()
		sentinel := errors.New("resolve exploded")
		gate := authorization.NewGate(authorization.WithMinDelayWindow(0))
		orch := NewOrchestrator(preflight.NewEngine(&fakeResolver{err: sentinel}, runtimectx.NewCompiler()), executor.NewValidator(), executor.NewExecutor(), gate)
		_, err := orch.RunCycle(context.Background(), baseRequest(ref), &stubProvider{}, &stubBridge{}, OrchestratorConfig{})
		if err == nil || !errors.Is(err, sentinel) {
			t.Errorf("expected resolver error propagation, got %v", err)
		}
	})

	t.Run("provider error propagates", func(t *testing.T) {
		t.Parallel()
		orch, _ := newStack(ref)
		sentinel := errors.New("llm exploded")
		_, err := orch.RunCycle(context.Background(), baseRequest(ref), &stubProvider{err: sentinel}, &stubBridge{}, OrchestratorConfig{})
		if err == nil || !errors.Is(err, sentinel) {
			t.Errorf("expected provider error propagation, got %v", err)
		}
	})

	t.Run("nil proposal rejected", func(t *testing.T) {
		t.Parallel()
		orch, _ := newStack(ref)
		_, err := orch.RunCycle(context.Background(), baseRequest(ref), &stubProvider{proposal: nil}, &stubBridge{}, OrchestratorConfig{})
		if err == nil || !strings.Contains(err.Error(), "nil proposal") {
			t.Errorf("expected nil proposal error, got %v", err)
		}
	})

	t.Run("render failure aborts before arming", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, dir, "README.md", "# Old\n")
		ref := targetRef(dir, "README.md", true)
		orch, gate := newStack(ref)
		provider := &stubProvider{proposal: happyProposal("p-render", ref, "# New\n")}
		sentinel := errors.New("tty broken")
		bridge := &stubBridge{renderErr: sentinel}
		res, err := orch.RunCycle(context.Background(), baseRequest(ref), provider, bridge, OrchestratorConfig{})
		if res != nil {
			t.Fatalf("expected nil result, got %+v", res)
		}
		if err == nil || !errors.Is(err, sentinel) {
			t.Errorf("expected render error propagation, got %v", err)
		}
		// The session was created but must never have been armed or evaluated.
		if gate.CurrentSession().State != authorization.StateUnarmed {
			t.Errorf("session state = %v, want StateUnarmed", gate.CurrentSession().State)
		}
		if len(bridge.armedEpoch) != 0 {
			t.Errorf("armed epochs = %v, want none", bridge.armedEpoch)
		}
	})

	t.Run("wait for approval error propagates", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := writeFile(t, dir, "README.md", "# Old\n")
		ref := targetRef(dir, "README.md", true)
		orch, _ := newStack(ref)
		provider := &stubProvider{proposal: happyProposal("p-wait", ref, "# New\n")}
		sentinel := errors.New("input closed")
		bridge := &stubBridge{waitErr: sentinel}
		res, err := orch.RunCycle(context.Background(), baseRequest(ref), provider, bridge, OrchestratorConfig{})
		if res != nil {
			t.Fatalf("expected nil result, got %+v", res)
		}
		if err == nil || !errors.Is(err, sentinel) {
			t.Errorf("expected wait error propagation, got %v", err)
		}
		if got := readFile(t, path); got != "# Old\n" {
			t.Errorf("content = %q, want untouched %q", got, "# Old\n")
		}
	})
}
