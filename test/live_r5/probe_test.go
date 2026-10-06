// Package live_r5 is the R5 NON-PROGRESS AND LOOP-CONTROL experiment.
//
// R4 proved that a bounded continuation is RUNTIME-OWNED and continues the SAME
// execution. R5 asks the next question: what prevents a continuation-capable
// execution from continuing forever when each attempt makes no meaningful
// progress?
//
// The benchmark deliberately uses an objective a 7B local model reliably
// under-delivers on: rewrite the complete file with a long, literal list, then
// verify it renders. The initial complete-file response exceeds the bounded
// per-invocation budget, so the runtime continues per R4; the continued work is
// frequently incomplete or redundant, so the objective stays unproven.
//
// The property under test is NOT convergence. It is BOUNDEDNESS:
//
//	the run ends in a typed terminal/parked state;
//	the number of model calls stays under the runtime-owned ceiling;
//	every continuation decision carries an authoritative progress
//	classification;
//	completion is still gated on PROVEN evidence.
//
// It is opt-in via IZEN_LIVE_FORENSICS=1, exactly like test/live_r1/live_r2/
// live_r4, and is deliberately NOT part of `go test ./...`.
package live_r5

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/forensics"
	"github.com/PizenLabs/izen/internal/providers"
	appruntime "github.com/PizenLabs/izen/internal/runtime"
	"github.com/PizenLabs/izen/internal/runtime/compose"
	domainorch "github.com/PizenLabs/izen/internal/runtime/orchestrator"
)

const (
	liveModel   = "qwen2.5-coder:7b"
	liveBaseURL = "http://127.0.0.1:11434/v1"
	liveOptIn   = "IZEN_LIVE_FORENSICS"

	// r5Objective NAMES the target explicitly (isolating progress/loop control
	// from discovery authority) and requires a literal rewrite the bounded
	// per-invocation budget cannot hold. It is a LEGITIMATE low-progress
	// objective: no prompt injection, no provider bug. The list length is chosen
	// so the continuation work is reliably incomplete, which is the case R5
	// exists to bound.
	r5Objective = `rewrite the complete @index.html file so the <ul id="items"> list contains exactly 2000 entries written out literally as <li>item N</li> lines for N=1 through N=2000; keep every other line byte-for-byte; output the entire updated file; then verify the page renders`

	// r5ArtifactDirEnv points at a directory the harness writes the reconstructed
	// trace and raw event stream into, so the evidence survives the test log.
	r5ArtifactDirEnv = "IZEN_R5_ARTIFACTS"

	// r5MaxModelCalls is the runtime-owned ceiling beyond which the run is
	// looping. Bounds are per-objective (MaxAttempts=3) but a single execution
	// legitimately makes several bounded invocations, so the observed ceiling is
	// a small multiple of the attempt bound. Exceeding it means the runtime is
	// not bounding the low-progress case.
	r5MaxModelCalls = 12
)

// r5IndexHTML is the same tiny servable workspace R4 uses: a wrong greeting and
// a short list, sized so a complete-file rewrite is dispatched rather than
// refused at Boundary-2.
const r5IndexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Greeting</title>
<style>
body { font-family: system-ui, sans-serif; margin: 2rem; color: #1a1a2e; }
h1 { color: #16213e; }
ul { line-height: 1.6; }
#notes { color: #333; max-width: 40rem; }
.note { margin-bottom: 0.75rem; }
</style>
</head>
<body>
<h1 id="greeting">goodbye</h1>
<ul id="items">
<li>item 1</li>
<li>item 2</li>
<li>item 3</li>
<li>item 4</li>
</ul>
<p id="intro">Welcome to the demo page. This static document is served by the
workspace runtime and inspected by the behavioural gate.</p>
<section id="notes">
<h2>Notes</h2>
<p class="note">This page exists to exercise the bounded execution loop. The
greeting is intentionally stale and the item list is intentionally short, so a
successful run has something real to change and something real to observe.</p>
</section>
</body>
</html>
`

func requireLiveModel(t *testing.T) {
	t.Helper()
	if os.Getenv(liveOptIn) != "1" {
		t.Skipf("set %s=1 to run the live R5 experiment (needs a local model server)", liveOptIn)
	}
	endpoint := strings.TrimSuffix(liveBaseURL, "/v1") + "/api/tags"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := exec.CommandContext(ctx, "curl", "-sS", "--max-time", "5", endpoint)
	client.Env = append(os.Environ(), "NO_PROXY=*")
	out, err := client.CombinedOutput()
	if err != nil {
		t.Skipf("no local model server at %s: %v", endpoint, err)
	}
	if !strings.Contains(string(out), liveModel) {
		t.Skipf("model %q is not installed locally: %s", liveModel, out)
	}
}

func gitInit(t *testing.T, root string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=izen", "GIT_AUTHOR_EMAIL=izen@local",
			"GIT_COMMITTER_NAME=izen", "GIT_COMMITTER_EMAIL=izen@local")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("add", "-A")
	run("commit", "-q", "-m", "baseline")
}

func liveApp(t *testing.T) (*compose.Application, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(r5IndexHTML), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInit(t, root)

	cfg := config.Default()
	cfg.Bindings.Active.Provider = "ollama"
	cfg.Bindings.Active.Model = liveModel
	app, err := compose.Wire(
		compose.WithRoot(root),
		compose.WithConfig(cfg),
		compose.WithProvider(providers.NewOllamaProvider(liveBaseURL, "ollama", liveModel)),
	)
	if err != nil {
		t.Fatalf("wire: %v", err)
	}
	t.Cleanup(app.Close)

	if _, err := os.Stat(filepath.Join(root, ".izen")); err != nil {
		if err := os.MkdirAll(filepath.Join(root, ".izen"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.Execution.ShadowCP.CreateSessionStartSnapshot(); err != nil {
		t.Fatalf("session-start checkpoint (production precondition): %v", err)
	}
	app.Runtime.Start()
	if err := app.Runtime.Execute(context.Background(),
		appruntime.SubmitPromptCmd{Prompt: r5Objective, Mode: "build"}); err != nil {
		t.Fatalf("SubmitPromptCmd (production preflight dispatch): %v", err)
	}
	return app, root
}

type result struct {
	Trace     *forensics.Trace
	State     autonomy.RuntimeState
	Term      *autonomy.LoopTermination
	IndexHTML string
	Boundary  *autonomy.HumanBoundary
	RawStream []events.DomainEvent
}

// runObjective executes one full run and reconstructs its trace. It answers an
// APPROVAL boundary (a review of a produced candidate); any other park is
// recorded and left parked — it IS the bounded stop R5 is measuring.
func runObjective(t *testing.T) result {
	t.Helper()
	app, root := liveApp(t)

	rec := forensics.NewRecorder()
	sub := app.Bus.SubscribeAll(rec.Handle)

	d := app.Autonomous
	d.SetScope("$prompt")

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	term, err := d.Run(ctx, r5Objective)
	if err != nil {
		t.Fatalf("driver.Run: %v", err)
	}
	t.Logf("driver.Run -> term=%s boundary=%s", fmtTerm(term), fmtBoundary(d.Boundary()))

	for i := 0; i < 8 && d.State() == autonomy.RuntimeAwaitingHuman; i++ {
		b := d.Boundary()
		if b == nil {
			break
		}
		if b.Action != autonomy.HumanBoundaryApproval {
			t.Logf("PARKED at a non-approval boundary (%s): reason=%q — NOT answered",
				b.Action, b.Reason)
			break
		}
		if err := app.Orchestrator.Transition(domainorch.PhaseBuild,
			workflow.TransitionContext{HasPlan: true, HasCapabilities: true}); err != nil {
			t.Logf("orchestrator transition to build: %v", err)
		}
		preview, ok := app.Executor.CandidatePreview(b.PatchID)
		if !ok {
			t.Fatalf("approval boundary holds no reviewable candidate: %+v", b)
		}
		auth, aerr := app.Auth.AuthorizeBuildCandidateContent(
			b.Targets, app.Caps, app.Budget, app.MicroBudget, false, true,
			b.CandidateID, preview.Digest())
		if aerr != nil {
			t.Fatalf("authorize reviewed candidate: %v", aerr)
		}
		app.Executor.SetAuthorization(auth)
		term, err = d.ResumeApprove(ctx)
		if err != nil {
			t.Fatalf("driver.ResumeApprove: %v", err)
		}
		t.Logf("driver.ResumeApprove -> term=%s", fmtTerm(term))
	}

	rec.WaitFor(events.EventExecutionSummary, 1, 30*time.Second)
	rec.WaitQuiet(250*time.Millisecond, 10*time.Second)
	sub.Cancel()

	stream := rec.Stream()
	tr, terr := forensics.NewTrace(stream)
	if terr != nil {
		t.Fatalf("reconstruct trace: %v", terr)
	}
	body, _ := os.ReadFile(filepath.Join(root, "index.html"))

	persistArtifacts(t, tr, stream)
	return result{
		Trace:     tr,
		State:     d.State(),
		Term:      term,
		IndexHTML: string(body),
		Boundary:  d.Boundary(),
		RawStream: stream,
	}
}

func persistArtifacts(t *testing.T, tr *forensics.Trace, stream []events.DomainEvent) {
	t.Helper()
	dir := os.Getenv(r5ArtifactDirEnv)
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("r5 artifacts dir: %v", err)
	}
	if err := tr.WriteNDJSON(filepath.Join(dir, "r5-trace.ndjson")); err != nil {
		t.Fatalf("write trace: %v", err)
	}
	var lines []string
	for _, ev := range stream {
		payload := map[string]any{"type": ev.Type(), "at": ev.Timestamp()}
		b, err := json.Marshal(ev.Payload())
		if err == nil {
			payload["payload"] = json.RawMessage(b)
		}
		line, err := json.Marshal(payload)
		if err != nil {
			continue
		}
		lines = append(lines, string(line))
	}
	if err := os.WriteFile(filepath.Join(dir, "r5-events.ndjson"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write raw events: %v", err)
	}
	t.Logf("R5 artifacts written to %s", dir)
}

// TestLiveR5_Observation is the measurement arm: it runs the low-progress
// objective and prints the authoritative trace, including the per-attempt
// progress classification. It asserts nothing about success.
func TestLiveR5_Observation(t *testing.T) {
	requireLiveModel(t)
	r := runObjective(t)
	t.Logf("\n%s", r.Trace.Render())
	t.Logf("WORKSPACE index.html = %q", r.IndexHTML)
	t.Logf("FINAL state=%s boundary=%s", r.State, fmtBoundary(r.Boundary))
	t.Logf("model calls=%d continuations=%d", r.Trace.ModelCallsTotal, r.Trace.Continuations)
	for i, d := range r.Trace.Decisions {
		t.Logf("DECISION #%d step=%d attempt=%d progress=%s prev=%s proposed=%s selected=%s obj_adv=%t mut=%t reason=%q",
			i+1, d.Step, d.Attempt, d.Progress, d.PreviousProgress,
			d.ProposedAction, d.SelectedAction, d.ObjectiveAdvanced, d.MutationApplied, d.SelectedReason)
	}
}

// TestLiveR5_Acceptance asserts the R5 boundedness contract on a real model:
//
//	the run terminates or parks in a typed state;
//	model calls never exceed the runtime-owned ceiling (no unbounded loop);
//	continuation decisions carry an authoritative progress classification;
//	completion remains gated on PROVEN evidence.
//
// It does NOT require the model to converge: a low-progress run that stops
// boundedly is the measurement.
func TestLiveR5_Acceptance(t *testing.T) {
	requireLiveModel(t)
	r := runObjective(t)
	tr := r.Trace
	t.Logf("\n%s", tr.Render())
	t.Logf("R5: state=%s model_calls=%d decisions=%d", r.State, tr.ModelCallsTotal, len(tr.Decisions))

	// INVARIANT 1 — BOUNDED. A low-progress objective must not produce an
	// unbounded number of provider calls.
	if tr.ModelCallsTotal > r5MaxModelCalls {
		t.Fatalf("R5: %d model calls exceeds the bounded ceiling %d — the runtime looped\n%s",
			tr.ModelCallsTotal, r5MaxModelCalls, tr)
	}
	if tr.ModelCallsTotal == 0 {
		t.Fatal("R5: the run made no model call at all")
	}

	// INVARIANT 2 — TYPED STOP. The run must be in a typed terminal or parked
	// state, never a phantom "still running".
	if !r.State.IsTerminal() && r.State != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("R5: run ended in a non-terminal, non-parked state %s", r.State)
	}

	// INVARIANT 3 — PROGRESS IS OBSERVABLE. Every continuation decision names a
	// progress classification from the runtime's own vocabulary.
	known := map[string]bool{}
	for _, p := range []execution.ObjectiveProgress{
		execution.ProgressDiscovered, execution.ProgressUnderstood,
		execution.ProgressRequirementsDerived, execution.ProgressReady,
		execution.ProgressInProgress, execution.ProgressPartiallySatisfied,
		execution.ProgressRequiresContinuation, execution.ProgressBlocked,
		execution.ProgressProven, execution.ProgressFailed,
		execution.ProgressUnsubstantiated, execution.ProgressRequiresAuthorization,
	} {
		known[string(p)] = true
	}
	if len(tr.Decisions) == 0 {
		t.Fatal("R5: the run published no continuation decisions")
	}
	for i, d := range tr.Decisions {
		if d.Progress == "" {
			t.Fatalf("R5: decision #%d carries no progress classification", i+1)
		}
		if !known[d.Progress] {
			t.Fatalf("R5: decision #%d progress %q is not in the closed vocabulary", i+1, d.Progress)
		}
	}

	// INVARIANT 4 — COMPLETION IS EVIDENCE-GATED. PROVEN iff completed, and only
	// with a real applied mutation.
	proven := false
	for _, o := range tr.ObjectiveStates {
		if o.Granted && strings.EqualFold(strings.TrimSpace(o.State), execution.ObjectiveProven.String()) {
			proven = true
		}
	}
	if proven {
		if r.State != autonomy.RuntimeCompleted {
			t.Fatalf("R5: PROVEN objective with non-completed state %s", r.State)
		}
		applied := false
		for _, m := range tr.Mutations {
			if m.ApplyExecuted && m.FilesystemChanged {
				applied = true
			}
		}
		if !applied {
			t.Fatalf("R5: PROVEN without an applied filesystem mutation\n%s", tr)
		}
		t.Logf("R5 converged: the low-progress objective was genuinely satisfied")
		return
	}
	if r.State == autonomy.RuntimeCompleted {
		t.Fatalf("R5: the run completed without an objective PROVEN by the authority\n%s", tr)
	}
	// The honest non-convergence: the model could not satisfy the objective
	// within the runtime's bounds, and the runtime stopped instead of looping.
	t.Logf("R5 bounded non-convergence: state=%s (the model's repeated work did not satisfy the objective)", r.State)
}

func fmtBoundary(b *autonomy.HumanBoundary) string {
	if b == nil {
		return "<nil>"
	}
	return "action=" + string(b.Action) +
		" patch=" + b.PatchID +
		" targets=" + strings.Join(b.Targets, ",") +
		" reason=" + b.Reason
}

func fmtTerm(term *autonomy.LoopTermination) string {
	if term == nil {
		return "<nil: parked at a human boundary>"
	}
	return string(term.State) + " / " + term.Reason + " / " + string(term.Class)
}
