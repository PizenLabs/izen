// Package live_r4 is the R4 BOUNDED CONTINUATION experiment.
//
// It runs the production composition (compose.Wire → the real bounded autonomy
// Driver → the real RuntimeExecutor → a REAL local Ollama model) against a tiny
// isolated workspace, with an objective that requires more output than the
// bounded per-invocation budget can hold.
//
// The chain under test:
//
//	MODEL CALL           finish_reason = length
//	  → runtime observes exhaustion
//	  → runtime-owned continuation decision
//	  → MODEL CALL (same execution)
//	  → mutation → evidence → verification → PROVEN
//
// It is opt-in via IZEN_LIVE_FORENSICS=1, exactly like test/live_r1 and
// test/live_r2, and is deliberately NOT part of `go test ./...`.
package live_r4

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

	// r4Objective NAMES the target explicitly (so this isolates CONTINUATION
	// rather than discovery authority) and requires an output larger than the
	// bounded per-invocation budget. The target is deliberately sized just under
	// the Boundary-2 complete-file preflight ceiling, so a complete-file rewrite
	// IS dispatched to the model. The complete-file response addresses the
	// greeting and an expanded list; it is larger than the 1024-token
	// replace_block request, so the first mutation call terminates with
	// finish_reason=length. The runtime's continued invocation is a bounded
	// SEARCH/REPLACE patch, which re-emits only the changed block and can
	// converge.
	r4Objective = `rewrite the complete @index.html file so the <ul id="items"> list contains exactly 80 entries written out literally as <li>item N</li> lines for N=1 through N=80; keep every other line byte-for-byte; output the entire updated file; then verify the page renders`

	// r4ArtifactDirEnv points at a directory the harness writes the reconstructed
	// trace and raw event stream into, so the evidence survives the test log.
	r4ArtifactDirEnv = "IZEN_R4_ARTIFACTS"
)

// r4IndexHTML is a tiny servable workspace with a wrong greeting and a short
// list. It is deliberately sized just under the Boundary-2 complete-file
// preflight threshold (bytes/4 × 3 ≤ 1024), so a complete-file rewrite IS
// dispatched to the model and any exhaustion is a genuine provider
// finish_reason, not a preflight refusal.
const r4IndexHTML = `<!DOCTYPE html>
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
<li>item 5</li>
<li>item 6</li>
<li>item 7</li>
<li>item 8</li>
</ul>
<p id="intro">Welcome to the demo page. This static document is served by the
workspace runtime and inspected by the behavioural gate.</p>
<section id="notes">
<h2>Notes</h2>
<p class="note">This page exists to exercise the bounded execution loop. The
greeting is intentionally stale and the item list is intentionally short, so a
successful run has something real to change and something real to observe.</p>
<p class="note">The runtime owns the execution state: it decides when a model
invocation has been cut short by its output ceiling, and it decides whether the
task may continue, must ask a human, or must stop. The model never decides.</p>
<p class="note">Every mutation is applied only after the approval boundary and
is verified against the workspace on disk, never against the model's own claim.</p>
</section>
</body>
</html>
`

func requireLiveModel(t *testing.T) {
	t.Helper()
	if os.Getenv(liveOptIn) != "1" {
		t.Skipf("set %s=1 to run the live R4 experiment (needs a local model server)", liveOptIn)
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
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(r4IndexHTML), 0o644); err != nil {
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
		appruntime.SubmitPromptCmd{Prompt: r4Objective, Mode: "build"}); err != nil {
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

// runObjective executes one full run and reconstructs its trace. It answers only
// an APPROVAL boundary (a review of a produced candidate); a clarification park
// is recorded and left parked.
func runObjective(t *testing.T) result {
	t.Helper()
	app, root := liveApp(t)

	rec := forensics.NewRecorder()
	sub := app.Bus.SubscribeAll(rec.Handle)

	d := app.Autonomous
	d.SetScope("$prompt") // the EXACT production call runAutonomousDriver makes

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	term, err := d.Run(ctx, r4Objective)
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
			t.Logf("PARKED at a non-approval boundary (%s): reason=%q options=%v — NOT answered",
				b.Action, b.Reason, b.Options)
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
	dir := os.Getenv(r4ArtifactDirEnv)
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("r4 artifacts dir: %v", err)
	}
	if err := tr.WriteNDJSON(filepath.Join(dir, "r4-trace.ndjson")); err != nil {
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
	if err := os.WriteFile(filepath.Join(dir, "r4-events.ndjson"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write raw events: %v", err)
	}
	t.Logf("R4 artifacts written to %s", dir)
}

// TestLiveR4_Observation is the measurement arm: it runs the objective against
// the real model and prints the authoritative trace. It asserts NOTHING about
// success so the first run measures behaviour instead of a hypothesis.
func TestLiveR4_Observation(t *testing.T) {
	requireLiveModel(t)
	r := runObjective(t)
	t.Logf("\n%s", r.Trace.Render())
	t.Logf("WORKSPACE index.html = %q", r.IndexHTML)
	t.Logf("FINAL state=%s boundary=%s", r.State, fmtBoundary(r.Boundary))
	t.Logf("model calls=%d output_exhausted=%d continuations=%d",
		r.Trace.ModelCallsTotal, r.Trace.OutputExhausted, r.Trace.Continuations)
	for i, c := range r.Trace.ModelCalls {
		t.Logf("CALL #%d requested=%d effective=%d known=%t finish=%q truncated=%t error=%q tokens=%d",
			i+1, c.RequestedOutputTokens, c.EffectiveOutputTokens, c.EffectiveOutputKnown,
			c.FinishReason, c.Truncated, c.ErrorCode, c.CompletionTokens)
	}
}

// TestLiveR4_Acceptance asserts the R4 required chain, read from authoritative
// runtime events and the on-disk workspace — never from model prose.
//
// The RUNTIME chain is model-independent and every attempt must show it:
//
//	real finish_reason=length → exhaustion recorded → continuation explicitly
//	evaluated/selected → a second call in the SAME execution → completion
//	remains evidence-gated (PROVEN only when a real mutation landed).
//
// Whether the 7B model's CONTINUED artifact is good enough to converge is
// model-dependent, so the experiment is allowed a small number of attempts. The
// runtime behaviour — including the honest NON-convergence when the model's
// artifact is incomplete — is asserted on every attempt.
func TestLiveR4_Acceptance(t *testing.T) {
	requireLiveModel(t)
	const maxAttempts = 3
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		t.Logf("=== R4 live attempt %d/%d ===", attempt, maxAttempts)
		r := runObjective(t)
		tr := r.Trace
		t.Logf("attempt %d: model_calls=%d output_exhausted=%d continuations=%d state=%s",
			attempt, tr.ModelCallsTotal, tr.OutputExhausted, tr.Continuations, r.State)

		// LINK 1: a real model call reached a genuine output ceiling. Without it
		// there is no continuation to investigate; retry the live model.
		if tr.OutputExhausted == 0 {
			t.Logf("attempt %d: no finish_reason=length (the model finished within budget) — retrying", attempt)
			continue
		}
		var exhausted *events.ProviderExecutionPayload
		for i := range tr.ModelCalls {
			if tr.ModelCalls[i].Truncated || tr.ModelCalls[i].FinishReason == "length" {
				exhausted = &tr.ModelCalls[i]
				break
			}
		}
		if exhausted == nil || exhausted.RequestedOutputTokens <= 0 {
			t.Fatalf("R4: exhaustion was not attributed to a requested budget: %+v\n%s", exhausted, tr)
		}
		t.Logf("attempt %d exhaustion: requested=%d effective=%d known=%t finish=%s",
			attempt, exhausted.RequestedOutputTokens, exhausted.EffectiveOutputTokens,
			exhausted.EffectiveOutputKnown, exhausted.FinishReason)

		// LINK 2: continuation was explicitly evaluated and selected.
		hasEvaluated, hasSelected := false, false
		for _, d := range tr.Decisions {
			if d.ProposedAction != "" {
				hasEvaluated = true
			}
			if d.SelectedAction != "" {
				hasSelected = true
			}
		}
		if !hasEvaluated || !hasSelected {
			t.Fatalf("R4: continuation was not explicitly evaluated/selected (evaluated=%t selected=%t)\n%s",
				hasEvaluated, hasSelected, tr)
		}

		// LINK 3: a second real call happened INSIDE the same execution.
		if tr.ModelCallsTotal < 2 {
			t.Fatalf("R4: exhaustion produced no continuation call (model_calls=%d)\n%s", tr.ModelCallsTotal, tr)
		}
		root := tr.RunID
		for i, c := range tr.ModelCalls {
			// The read-only requirement pass carries no run-scoped RequestID;
			// every run-scoped call (the mutation and its continuations) must
			// belong to the SAME bounded run.
			if c.RequestID == "" {
				continue
			}
			if !strings.HasPrefix(c.RequestID, root) {
				t.Fatalf("R4: model call #%d RequestID=%q is not part of run %q\n%s", i+1, c.RequestID, root, tr)
			}
		}

		proven := false
		for _, o := range tr.ObjectiveStates {
			if o.Granted && strings.EqualFold(strings.TrimSpace(o.State), execution.ObjectiveProven.String()) {
				proven = true
			}
		}

		// LINK 4 — NO FALSE COMPLETION. A successful terminal state and a PROVEN
		// objective must be inseparable, and PROVEN requires real applied evidence.
		if proven {
			if r.State != autonomy.RuntimeCompleted {
				t.Fatalf("R4: PROVEN objective with non-completed state %s\n%s", r.State, tr)
			}
			applied := false
			for _, m := range tr.Mutations {
				if m.ApplyExecuted && m.FilesystemChanged {
					applied = true
				}
			}
			if !applied || r.IndexHTML == r4IndexHTML {
				t.Fatalf("R4: PROVEN without an applied filesystem mutation\n%s", tr)
			}
			for _, p := range tr.Patterns {
				if p == forensics.PatternRepeatedIdentical || p == forensics.PatternNonProgressingContinue {
					t.Fatalf("R4: continuation exhibited %s\n%s", p, tr)
				}
			}
			t.Logf("\n%s", tr.Render())
			t.Logf("R4 CONVERGED on attempt %d: index.html = %q", attempt, r.IndexHTML)
			return
		}
		if r.State == autonomy.RuntimeCompleted {
			t.Fatalf("R4: the run completed without an objective PROVEN by the authority\n%s", tr)
		}
		t.Logf("attempt %d: runtime correctly did NOT complete (state=%s) — the model's continued artifact was not sufficient; retrying",
			attempt, r.State)
	}
	t.Fatalf("R4: no attempt converged to PROVEN in %d attempts", maxAttempts)
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
