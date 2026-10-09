package ui

// ── EXECUTION OWNERSHIP CONVERGENCE ─────────────────────────────────────────
//
// OBSERVED: `$prompt` entered the bounded autonomy Driver (the lifecycle owner),
// while an ordinary `/build` prompt dispatched the RuntimeExecutor DIRECTLY, so
// the UI composition owned a second execution lifecycle.
//
// CORRECTION: after admission, both `$prompt` and an ordinary `/build` prompt
// enter the SAME authoritative runtime (the autonomy Driver) when it is wired.
// The command surfaces stay distinct — `$prompt` and `/build` share their dynamic
// execution authority but are carried as different surfaces, and `$hot` remains
// its own bounded, human-declared authority.

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes"
)

const convergenceCreatePrompt = "Create a new file named zuru.md with the content \"Hello everyone\"."

func convergenceModel() (*model, *fakeAutonomousDriver) {
	m := readyChatModel(newTestModel())
	m.resolver.Set(modes.ModeBuild)
	m.autonomy = autonomy.NewEngine(autonomy.WithScope("repository"))
	m.autonomy.GrantDefault(autonomy.CapRead, autonomy.CapAnalyze, autonomy.CapPropose, autonomy.CapMutate, autonomy.CapVerify)
	m.gateway = execution.NewIntentGateway(".")
	m.executor = execution.NewRuntimeExecutor(".", m.cfg, &mockProvider{}, nil, "")
	drv := &fakeAutonomousDriver{
		runID: "run-convergence",
		term:  &autonomy.LoopTermination{State: autonomy.RuntimeCompleted, Reason: "done"},
	}
	m.autonomousDriver = drv
	return m, drv
}

// TestBuildOrdinaryPromptEntersAuthoritativeRuntime proves an ordinary /build
// prompt enters the autonomy runtime owner instead of the direct executor path.
func TestBuildOrdinaryPromptEntersAuthoritativeRuntime(t *testing.T) {
	m, drv := convergenceModel()

	cmd := m.runRuntimePrompt(convergenceCreatePrompt)

	// The authoritative runtime was entered (driver run initiated + scope bound).
	if !m.autonomousActive {
		t.Fatal("/build ordinary prompt did not start the authoritative runtime lifecycle")
	}
	if drv.scope != "$build" {
		t.Fatalf("driver scope = %q, want %q (the /build surface did not reach the runtime)", drv.scope, "$build")
	}
	if m.executionSurface != "$build" {
		t.Fatalf("execution surface = %q, want %q", m.executionSurface, "$build")
	}
	// The direct executor path sets lastExecutionStrategy; the runtime owner does not.
	if m.lastExecutionStrategy.Strategy != "" {
		t.Fatalf("/build took the direct executor path (strategy=%s)", m.lastExecutionStrategy.Strategy)
	}
	if !strings.Contains(recordsText(m), "AUTONOMY DECISION") {
		t.Error("/build did not cross the authoritative runtime's admission/decision seam")
	}
	if cmd == nil && m.pendingAutonomyProposal == nil {
		t.Fatal("/build produced neither a runtime command nor an explicit parked proposal")
	}
}

// TestPromptAndBuildEnterTheSameRuntimeWithDistinctSurfaces pins both halves of
// the invariant: one runtime owner, two preserved command surfaces.
func TestPromptAndBuildEnterTheSameRuntimeWithDistinctSurfaces(t *testing.T) {
	buildModel, buildDrv := convergenceModel()
	_ = buildModel.runRuntimePrompt(convergenceCreatePrompt)
	if !buildDrv.ran() && !buildModel.autonomousActive {
		t.Fatal("/build did not enter the runtime lifecycle")
	}
	if buildDrv.scope != "$build" {
		t.Fatalf("/build scope = %q, want $build", buildDrv.scope)
	}

	promptModel, promptDrv := convergenceModel()
	_ = promptModel.routePromptDirective(convergenceCreatePrompt)
	if !promptDrv.ran() && !promptModel.autonomousActive {
		t.Fatal("$prompt did not enter the runtime lifecycle")
	}
	if promptDrv.scope != "$prompt" {
		t.Fatalf("$prompt scope = %q, want $prompt", promptDrv.scope)
	}

	if buildDrv.scope == promptDrv.scope {
		t.Fatal("$prompt and /build collapsed to one surface")
	}
}

func (f *fakeAutonomousDriver) ran() bool { return f.runCount > 0 }
