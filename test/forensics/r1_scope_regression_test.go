package forensics_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/core/authorization"
	coredomain "github.com/PizenLabs/izen/internal/core/domain"
	domaincap "github.com/PizenLabs/izen/internal/domain/capability"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/capability"
	"github.com/PizenLabs/izen/internal/forensics"
	rtAutonomy "github.com/PizenLabs/izen/internal/runtime/autonomy"
)

// ── REGRESSION R1: $prompt → ScopeDynamic → Execute grant ─────────────────────
//
// This is the regression for the defect the live experiment closed. It is written
// against the PRODUCTION PATH, not against a helper:
//
//   - the driver is the real `runtimeAutonomy.Driver`, constructed the way
//     `compose.Wire` constructs it — including `WithBehaviorProposer`, which is
//     what makes the behavioral completion gate engage at all;
//   - the behavioral stage is wired over the real `execution.BehavioralRuntime`
//     against a real temp workspace with a real servable HTML entry document;
//   - the objective is phrased so `BehaviorRequired` matches, so the behavioral
//     gate actually runs;
//   - the scope reaches the driver through the SAME push the TUI performs
//     (`SetScope`), and nothing else sets it.
//
// Nothing in the test writes a Scope into a LoopRequest. If `Driver.Run` stopped
// copying the scope onto the request it builds, every assertion below fails,
// because the behavioral gate would then derive a read-only grant and could not
// start the workspace runtime.

// servableIndex is a real, servable workspace: a document that declares a
// document element, so capability discovery selects it as the entry and the
// behavioral runtime has something real to serve and inspect.
const servableIndex = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Greeting</title>
</head>
<body>
<h1 id="greeting">goodbye</h1>
</body>
</html>
`

// behavioralObjective demands BOTH a mutation and an observable result, so the
// behavioral completion gate engages and its grant decides the outcome.
const behavioralObjective = "fix the greeting in @index.html so it displays hello and verify the page renders correctly"

// behavioralHarness wires the production driver over a real executor against a
// workspace that can actually be served, with the behavioral stage bound exactly
// as the composition root binds it.
type behavioralHarness struct {
	*run
	Root string
}

func newBehavioralHarness(t *testing.T, p *scriptedProvider) *behavioralHarness {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(servableIndex), 0o644); err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus(events.DefaultBufferSize)
	rec := forensics.NewRecorder()
	sub := bus.SubscribeAll(rec.Handle)
	t.Cleanup(sub.Cancel)
	t.Cleanup(bus.Close)

	x := execution.NewRuntimeExecutor(root, config.Default(), p, bus, "")
	x.SetVerifier(execution.NewVerifier(root))
	x.SetAuthorization(&authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(time.Hour),
	})
	adapter := rtAutonomy.NewExecutorAdapter(root, execution.NewIntentGateway(root), x)

	// The production capability set. Execute is granted HERE by the control
	// plane; the scope provenance only decides whether the behavioral gate is
	// ALLOWED to use it. A regression that drops the scope therefore loses
	// Execute at the gate even though the control plane still holds it — which
	// is exactly the shape of the original defect.
	caps := domaincap.NewCapabilitySet()
	caps.Grant(domaincap.CapabilityRead)
	caps.Grant(domaincap.CapabilityWrite)
	caps.Grant(domaincap.CapabilityExecute)
	adapter.SetCapabilities(caps)

	// The behavioral stage, wired as compose.Wire wires it: over the SAME
	// provider the executor uses. The proposer is a stub that DECLINES, because
	// this regression is about whether the gate can OBSERVE, not about whether a
	// repair can be proposed. A declined repair must not affect the grant.
	d := rtAutonomy.NewDriver(
		adapter,
		bus,
		rtAutonomy.WithBehaviorProposer(decliningProposer{}),
	)

	return &behavioralHarness{
		run:  &run{t: t, Root: root, Bus: bus, Driver: d, Prov: p, recorder: rec, sub: sub},
		Root: root,
	}
}

// decliningProposer refuses every repair. The behavioral loop then proves or
// fails purely on OBSERVATION, so the test measures the grant and not the
// repair backend.
type decliningProposer struct{}

func (decliningProposer) ProposeRepair(context.Context, capability.Defect, execution.Observation) (execution.RepairProposal, error) {
	return execution.RepairProposal{}, execution.ErrNoProposalReturned
}

// execute drives the full production lifecycle: push the directive, run, answer
// the approval boundary through the driver's own resume path.
func (h *behavioralHarness) execute(scope, objective string) (*forensics.Trace, autonomy.RuntimeState) {
	h.t.Helper()
	// The TUI's exact production push. Nothing else in this test supplies a
	// scope, so the value can only reach the run if production wired it.
	h.Driver.SetScope(scope)
	h.t.Logf("driver.SetScope(%q)", scope)

	if _, err := h.Driver.Run(context.Background(), objective); err != nil {
		h.t.Fatalf("Run: %v", err)
	}
	// Answer the approval gate the way the TUI does: resume the driver.
	if h.Driver.State() == autonomy.RuntimeAwaitingHuman && h.Driver.Boundary() != nil {
		if b := h.Driver.Boundary(); b.PatchID != "" {
			if _, err := h.Driver.ResumeApprove(context.Background()); err != nil {
				h.t.Fatalf("ResumeApprove: %v", err)
			}
		}
	}
	tr := h.report()
	return tr, h.Driver.State()
}

// TestR1_ProductionPromptScopeGrantsExecuteAndTheRuntimeExecutes is the R1
// regression proper.
//
// It fails if `LoopRequest.Scope` becomes zero-valued again, because the
// behavioral completion gate — the only consumer of that field — would then
// derive a read-only grant and could not serve the workspace it is being asked
// to observe. The run would still "succeed" at the mutation, so every assertion
// here is about the OBSERVATION, not about the file.
//
// The capability assertions read the structured `execution.behavior.observed`
// record, never a prose fragment of a decision reason. That is deliberate: the
// reason string is bounded and truncated exactly the clause that proves the
// runtime served the workspace, so an assertion against it is an assertion
// against a rendering accident.
func TestR1_ProductionPromptScopeGrantsExecuteAndTheRuntimeExecutes(t *testing.T) {
	p := &scriptedProvider{name: "scripted", responses: []*ai.Response{
		answered(1800, 260, searchReplaceHello),
	}}
	h := newBehavioralHarness(t, p)

	tr, state := h.execute("$prompt", behavioralObjective)
	t.Logf("final state: %s", state)

	// ── The authorizing directive reached the runtime as $prompt. This is the
	// observable projection of `LoopRequest.Scope`, read back from the run's own
	// authorization record rather than from the field itself.
	if tr.Authorization.Mode != "$prompt" {
		t.Fatalf("authorization mode = %q, want $prompt — LoopRequest.Scope did not travel\n%s",
			tr.Authorization.Mode, tr)
	}

	// ── The behavioral gate ran, and ran under a $prompt-derived grant.
	if len(tr.Behaviorals) == 0 {
		t.Fatalf("no behavioral observation recorded — the gate never engaged\n%s", tr)
	}
	pass := tr.Behaviorals[len(tr.Behaviorals)-1]
	if pass.GrantProvenance != "$prompt" {
		t.Fatalf("behavioural grant provenance = %q, want $prompt\n%s", pass.GrantProvenance, tr)
	}

	// ── The grant PERMITTED process execution and egress…
	for _, id := range []capability.ID{capability.RuntimeServe, capability.RuntimeFetch,
		capability.RuntimeInspect, capability.CommandRun} {
		if !containsString(pass.Granted, string(id)) {
			t.Errorf("$prompt grant must permit %s, granted=%v", id, pass.Granted)
		}
	}

	// ── …and the runtime ACTUALLY EXECUTED them.
	//
	// This is the assertion that cannot be satisfied by reading the grant
	// derivation. `Granted` is a permission; `Executed` is the pass's own
	// evidence of what it did, and it is empty under a read-only grant because
	// the capability layer refuses at Invoke.
	for _, id := range []capability.ID{capability.RuntimeServe, capability.RuntimeFetch} {
		if !containsString(pass.Executed, string(id)) {
			t.Fatalf("behavioural stage never executed %s — Execute was not granted in practice\n%s",
				id, tr)
		}
	}

	// ── No authorization refusal occurred: the capabilities ran because they were
	// granted, not because a refusal was ignored.
	if pass.BlockClass != "" {
		t.Fatalf("behavioural pass was blocked (%s: %s) under a $prompt scope\n%s",
			pass.BlockClass, pass.BlockReason, tr)
	}

	// ── And the gate PROVED the objective by real observation.
	if !pass.Proven {
		t.Fatalf("behavioural gate did not prove the objective: %s\n%s", pass.Defects, tr)
	}
}

// TestR1_WithoutTheDirectiveTheGateCannotObserve is the CONTROL arm: the same
// production path with no directive pushed.
//
// It documents what R1 fixed and, just as importantly, proves the assertion
// above is not vacuous — if the behavioral gate could execute under a read-only
// grant, the scoped arm would prove nothing.
func TestR1_WithoutTheDirectiveTheGateCannotObserve(t *testing.T) {
	p := &scriptedProvider{name: "scripted", responses: []*ai.Response{
		answered(1800, 260, searchReplaceHello),
	}}
	h := newBehavioralHarness(t, p)

	tr, state := h.execute("", behavioralObjective)
	t.Logf("final state: %s", state)

	if tr.Authorization.Mode != "read_only" {
		t.Fatalf("control arm mode = %q, want read_only — the withheld directive must read as read-only",
			tr.Authorization.Mode)
	}
	if len(tr.Behaviorals) == 0 {
		t.Fatalf("control arm recorded no behavioral pass\n%s", tr)
	}
	pass := tr.Behaviorals[len(tr.Behaviorals)-1]
	if pass.GrantProvenance != "read_only" {
		t.Fatalf("control arm grant provenance = %q, want read_only", pass.GrantProvenance)
	}
	if containsString(pass.Executed, string(capability.RuntimeServe)) {
		t.Fatalf("a read-only scope must never start a runtime\n%s", tr)
	}
	// The refusal is the truthful outcome, and it is what makes the scoped arm
	// meaningful: the same workspace, the same objective, the same provider.
	if pass.BlockClass != string(capability.FailureAuthorizationBlocked) {
		t.Fatalf("control arm block class = %q, want %s — a read-only grant must refuse, not fail obscurely",
			pass.BlockClass, capability.FailureAuthorizationBlocked)
	}
	if pass.Proven {
		t.Fatalf("a read-only gate must never report PROVEN\n%s", tr)
	}
	t.Logf("control arm refused at %s: %s", pass.BlockClass, pass.BlockReason)
}

// TestR1_ScopeProvenanceDrivesTheBehavioralCapabilityVector pins the mapping the
// regression above depends on: $prompt widens to Execute, read-only does not.
//
// It is here so that a future change to `GrantFor` breaks a NAMED test rather
// than silently making the end-to-end assertion above pass or fail for an
// unrelated reason.
func TestR1_ScopeProvenanceDrivesTheBehavioralCapabilityVector(t *testing.T) {
	caps := domaincap.NewCapabilitySet()
	caps.Grant(domaincap.CapabilityRead)
	caps.Grant(domaincap.CapabilityExecute)

	prompt := execution.GrantFor(coredomain.ScopeDynamic, caps)
	for _, id := range []capability.ID{capability.WorkspaceDiscover, capability.FileRead,
		capability.RuntimeServe, capability.CommandRun, capability.RuntimeFetch, capability.RuntimeInspect} {
		if !prompt.Permits(id) {
			t.Errorf("$prompt (ScopeDynamic) must permit %s with execute authority in the workspace set", id)
		}
	}

	readOnly := execution.GrantFor(coredomain.ScopeNone, caps)
	if readOnly.Permits(capability.RuntimeServe) || readOnly.Permits(capability.CommandRun) ||
		readOnly.Permits(capability.RuntimeFetch) || readOnly.Permits(capability.RuntimeInspect) {
		t.Errorf("read-only must never permit process execution or egress, got %v", readOnly.Names())
	}
}

// searchReplaceHello is the artifact contract for the behavioural objective: a
// SEARCH/REPLACE block that turns the wrong greeting into the right one.
var searchReplaceHello = `<<<<<<< SEARCH
<h1 id="greeting">goodbye</h1>
=======
<h1 id="greeting">hello</h1>
>>>>>>> REPLACE`

// containsString reports membership in a small list.
func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
