// Package live_r1 is the R1 PROOF experiment.
//
// It runs the production composition (compose.Wire → the real bounded
// autonomy Driver → the real RuntimeExecutor → a REAL local Ollama model)
// against a tiny isolated workspace, twice:
//
//	arm "scoped"    the TUI's exact production call, driver.SetScope("$prompt")
//	arm "withheld"  the pre-fix condition — the directive never reaches the run
//
// and asserts the divergence the R1 fix claims to remove. It is a build-tagged
// live experiment: it needs a running Ollama and is not part of `go test ./...`.
package live_r1

import (
	"context"
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
	"github.com/PizenLabs/izen/internal/execution/capability"
	"github.com/PizenLabs/izen/internal/forensics"
	"github.com/PizenLabs/izen/internal/providers"
	appruntime "github.com/PizenLabs/izen/internal/runtime"
	"github.com/PizenLabs/izen/internal/runtime/compose"
	domainorch "github.com/PizenLabs/izen/internal/runtime/orchestrator"
)

const (
	liveModel   = "qwen2.5-coder:7b"
	liveBaseURL = "http://127.0.0.1:11434/v1"

	// objective requires BOTH a mutation and a behavioural proof: "verify" and
	// "renders" match BehaviorRequired, so the run reaches the behavioural
	// completion gate, and that gate's capability vector is derived from the
	// scope provenance — which is exactly what R1 changed.
	objective = "fix the greeting in @index.html so it displays hello and verify the page renders correctly"
)

// indexHTML is a tiny servable workspace whose greeting is WRONG, so a
// successful run has something real to change and something real to observe.
const indexHTML = `<!DOCTYPE html>
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

// liveOptIn is the environment variable that arms this package.
//
// A live model run is not hermetic: it needs a local server, real wall-clock
// minutes, and a model whose output is not reproducible. `go test ./...` must
// stay hermetic and deterministic, so the experiment is opt-in rather than a
// permanently-slow, environment-dependent member of the default suite. The
// DETERMINISTIC proof of R1 lives in test/forensics and runs always; this package
// is the live confirmation.
const liveOptIn = "IZEN_LIVE_FORENSICS"

// requireLiveModel skips unless the operator opted in AND a local model server
// answers. The experiment substitutes NOTHING else: the model is the variable
// under observation, so a fake here would defeat its own purpose.
func requireLiveModel(t *testing.T) {
	t.Helper()
	if os.Getenv(liveOptIn) != "1" {
		t.Skipf("set %s=1 to run the live R1 experiment (needs a local model server)", liveOptIn)
	}
	// The native Ollama tag list, not the OpenAI-compatible path the provider
	// itself uses: this check asks "is a model server reachable at all", and only
	// /api/tags answers that without a configured model.
	endpoint := strings.TrimSuffix(liveBaseURL, "/v1") + "/api/tags"
	// The context is the outer bound on a subprocess the harness cannot
	// otherwise interrupt: --max-time covers curl's own connect/read stalls, but
	// nothing bounds the time curl spends waiting to be spawned.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := exec.CommandContext(ctx, "curl", "-sS", "--max-time", "5", endpoint)
	client.Env = append(os.Environ(), "NO_PROXY=*")
	out, err := client.CombinedOutput()
	if err != nil {
		t.Skipf("no local model server at %s: %v", endpoint, err)
	}
	if !strings.Contains(string(out), `"models"`) {
		t.Skipf("local model server at %s did not answer /api/tags: %s", endpoint, out)
	}
	if !strings.Contains(string(out), liveModel) {
		t.Skipf("model %q is not installed locally: %s", liveModel, out)
	}
}

// gitInit makes the workspace a real repository with one commit.
//
// Production build execution requires a checkpoint, and a checkpoint IS a git
// commit. A bare temp directory is therefore not a faithful stand-in: without
// this the approval admission gate refuses every mutation ("no valid checkpoint
// exists"), which is correct runtime behaviour and would make the experiment
// measure the harness instead of the runtime.
func gitInit(t *testing.T, root string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		// Local repository setup, but a hung git is still a hung test: bound it
		// so a stuck child process fails the run instead of blocking the suite.
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

// liveApp is the production composition over an isolated workspace.
func liveApp(t *testing.T) (*compose.Application, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(indexHTML), 0o644); err != nil {
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

	// The TUI creates the session-start shadow checkpoint during init
	// (model.initSessionStartCheckpoint). Without it the checkpoint-verification
	// clause refuses every build mutation, so this is a production
	// precondition rather than harness scaffolding.
	if _, err := os.Stat(filepath.Join(root, ".izen")); err != nil {
		if err := os.MkdirAll(filepath.Join(root, ".izen"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.Execution.ShadowCP.CreateSessionStartSnapshot(); err != nil {
		t.Fatalf("session-start checkpoint (production precondition): %v", err)
	}

	// The TUI batches the Application-layer SubmitPromptCmd alongside the
	// autonomy route for every non-slash input line. That command is the only
	// dispatcher of BackgroundPreflight, which is the only writer of the
	// PreflightSyncBarrier the driver waits on at observing→deciding.
	app.Runtime.Start()
	if err := app.Runtime.Execute(context.Background(),
		appruntime.SubmitPromptCmd{Prompt: objective, Mode: "build"}); err != nil {
		t.Fatalf("SubmitPromptCmd (production preflight dispatch): %v", err)
	}
	return app, root
}

// result is one arm's authoritative outcome.
type result struct {
	Trace        *forensics.Trace
	State        autonomy.RuntimeState
	Term         *autonomy.LoopTermination
	IndexHTML    string
	Behavioral   string
	Capabilities []string
}

// runArm executes one full run under the given scope and reconstructs its trace.
func runArm(t *testing.T, scope string) result {
	t.Helper()
	app, root := liveApp(t)

	rec := forensics.NewRecorder()
	sub := app.Bus.SubscribeAll(rec.Handle)

	d := app.Autonomous
	if scope != "" {
		// The EXACT production call `runAutonomousDriver` makes per input.
		d.SetScope(scope)
		t.Logf("driver.SetScope(%q) — the production push", scope)
	} else {
		t.Logf("scope withheld — the pre-fix condition (ScopeNone)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	term, err := d.Run(ctx, objective)
	if err != nil {
		t.Fatalf("driver.Run: %v", err)
	}
	t.Logf("driver.Run -> term=%s", fmtTerm(term))

	// Answer the human boundary exactly as the TUI's resumeAutonomousApprove
	// does: authorize the reviewed candidate through the same AuthorizationEngine
	// the rest of the runtime uses, then resume.
	if d.State() == autonomy.RuntimeAwaitingHuman && d.Boundary() != nil {
		b := d.Boundary()
		t.Logf("boundary: action=%s patch=%s targets=%v reason=%q",
			b.Action, b.PatchID, b.Targets, b.Reason)
		if err := app.Orchestrator.Transition(domainorch.PhaseBuild,
			workflow.TransitionContext{HasPlan: true, HasCapabilities: true}); err != nil {
			t.Logf("orchestrator transition to build: %v", err)
		}
		preview, ok := app.Executor.CandidatePreview(b.PatchID)
		if ok {
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
	}

	rec.WaitFor(events.EventExecutionSummary, 1, 30*time.Second)
	rec.WaitQuiet(250*time.Millisecond, 10*time.Second)
	sub.Cancel()

	tr, terr := forensics.NewTrace(rec.Stream())
	if terr != nil {
		t.Fatalf("reconstruct trace: %v", terr)
	}
	body, _ := os.ReadFile(filepath.Join(root, "index.html"))

	res := result{Trace: tr, State: d.State(), Term: term, IndexHTML: string(body)}
	res.Capabilities = tr.Authorization.Capabilities
	for _, e := range tr.Entries {
		if strings.Contains(e.Summary, "[behavior]") || strings.Contains(e.Event, "behavior") {
			res.Behavioral = e.Summary
		}
	}
	return res
}

// TestLiveR1_ScopedPromptExecutesUnderExecuteGrant is the PROOF arm.
//
// The chain under test, each link read from runtime evidence:
//
//	$prompt → ScopeDynamic → LoopRequest.Scope → Execute granted
//	→ runtime executes → mutation occurs → verification runs
//	→ behaviour satisfied → objective PROVEN → execution finishes normally
func TestLiveR1_ScopedPromptExecutesUnderExecuteGrant(t *testing.T) {
	requireLiveModel(t)
	r := runArm(t, "$prompt")
	tr := r.Trace
	t.Logf("\n%s", tr.Render())
	t.Logf("WORKSPACE index.html = %q", r.IndexHTML)

	// ── LINK 1+2: the directive reached the runtime and was classified dynamic.
	if tr.Authorization.Mode != "$prompt" {
		t.Fatalf("authorization mode = %q, want $prompt — the directive did not travel", tr.Authorization.Mode)
	}

	// ── LINK 3: the behavioral gate was GRANTED Execute and actually used it.
	// The proof is not the grant derivation (already proven by reading it) but a
	// capability execution that ONLY an Execute grant can perform: serving the
	// workspace runtime and fetching it. A read-only grant returns
	// AUTHORIZATION_BLOCKED at runtime.serve and never reaches a URL.
	//
	// The assertion reads the structured behavioral record, not a prose fragment of
	// a decision reason — the reason is bounded and truncates exactly the clause
	// that proves the runtime served the workspace.
	if len(tr.Behaviorals) == 0 {
		t.Fatalf("no behavioral observation recorded — the gate never engaged\n%s", tr)
	}
	pass := tr.Behaviorals[len(tr.Behaviorals)-1]
	for _, id := range []capability.ID{capability.RuntimeServe, capability.RuntimeFetch} {
		if !containsString(pass.Executed, string(id)) {
			t.Fatalf("behavioural stage never executed %s — Execute was not granted in practice\n%s", id, tr)
		}
	}
	t.Logf("behavioural pass: proven=%t provenance=%s granted=%v executed=%v",
		pass.Proven, pass.GrantProvenance, pass.Granted, pass.Executed)

	// ── LINK 4: the runtime executed and produced a mutation.
	applied := false
	for _, m := range tr.Mutations {
		if m.ApplyExecuted && m.FilesystemChanged {
			applied = true
		}
	}
	if !applied {
		t.Fatalf("no mutation reported apply_executed + fs_changed\n%s", tr)
	}

	// ── LINK 5: the mutation is visible on FILESYSTEM, not merely claimed.
	if strings.Contains(r.IndexHTML, "goodbye") || !strings.Contains(r.IndexHTML, "hello") {
		t.Fatalf("workspace does not hold the mutation: %q", r.IndexHTML)
	}

	// ── LINK 6: verification published a verdict of some kind (never silence).
	if len(tr.Verifications) == 0 {
		t.Fatalf("applied mutation published NO verification verdict\n%s", tr)
	}
	t.Logf("verification verdict: applicable=%t passed=%t reason=%q steps=%v",
		tr.Verifications[0].Applicable, tr.Verifications[0].Passed,
		tr.Verifications[0].Reason, tr.Verifications[0].Steps)

	// ── LINK 7+8: the behavioural gate PROVEN the objective, from observation.
	if !strings.Contains(behavioralEvidence(tr), "PROVEN") &&
		!strings.Contains(termReason(r.Term), "PROVEN") {
		t.Fatalf("behavioural proof absent from the record\n%s", tr)
	}

	// ── LINK 9: the objective authority, not the model, granted completion.
	//
	// The comparison is against the runtime's own ObjectiveProven constant. The
	// live run is what exposed that the forensic detector's own copy of this
	// comparison was lowercase and could therefore never fire — so this assertion
	// uses the authority rather than a re-spelling of it.
	proven := false
	for _, o := range tr.ObjectiveStates {
		if strings.EqualFold(strings.TrimSpace(o.State), execution.ObjectiveProven.String()) && o.Granted {
			proven = true
		}
	}
	if !proven {
		t.Fatalf("objective was never PROVEN by the completion authority\n%s", tr)
	}

	// ── LINK 10: the run finished normally.
	if r.State != autonomy.RuntimeCompleted {
		t.Fatalf("final state = %s, want completed\n%s", r.State, tr)
	}
	if len(tr.Patterns) != 0 {
		t.Fatalf("proven run exhibits forensic patterns %v\n%s", tr.Patterns, tr)
	}
}

// TestLiveR1_WithheldScopeIsTheControl is the CONTROL arm: the identical run
// with the directive withheld. It documents what the fix changed, so the
// scoped arm's success cannot be explained by the workspace or the model.
func TestLiveR1_WithheldScopeIsTheControl(t *testing.T) {
	requireLiveModel(t)
	r := runArm(t, "")
	tr := r.Trace
	t.Logf("\n%s", tr.Render())
	t.Logf("WORKSPACE index.html = %q", r.IndexHTML)

	if tr.Authorization.Mode != "read_only" {
		t.Fatalf("control arm authorization mode = %q, want read_only", tr.Authorization.Mode)
	}
	if len(tr.Behaviorals) == 0 {
		t.Fatalf("control arm recorded no behavioral pass\n%s", tr)
	}
	pass := tr.Behaviorals[len(tr.Behaviorals)-1]
	// The behavioural gate could not observe, because it may not execute.
	// Whatever the loop then decides, the behavioural PROVEN claim is absent and
	// no process was ever started.
	if pass.Proven {
		t.Fatalf("control arm claims behavioural PROVEN under a read-only grant\n%s", tr)
	}
	for _, id := range []capability.ID{capability.RuntimeServe, capability.RuntimeFetch} {
		if containsString(pass.Executed, string(id)) {
			t.Fatalf("a read-only scope must never execute %s\n%s", id, tr)
		}
	}
	t.Logf("control arm: proven=%t provenance=%s executed=%v block=%s %s",
		pass.Proven, pass.GrantProvenance, pass.Executed, pass.BlockClass, pass.BlockReason)
	t.Logf("control terminal state: %s (%s)", r.State, termReason(r.Term))
}

// containsString reports membership in a small list.
func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// behavioralEvidence joins every behavioural-stage record on the trace.
func behavioralEvidence(tr *forensics.Trace) string {
	var b strings.Builder
	for _, e := range tr.Entries {
		if strings.Contains(e.Event, "activity") {
			b.WriteString(e.Summary)
			b.WriteString("\n")
		}
	}
	// The termination reason carries the behavioural evidence line verbatim, so
	// it is part of the behavioural record a reader can trust.
	if tr.Summary.TerminationReason != "" {
		b.WriteString(tr.Summary.TerminationReason)
	}
	return b.String()
}

func termReason(term *autonomy.LoopTermination) string {
	if term == nil {
		return ""
	}
	return term.Reason
}

func fmtTerm(term *autonomy.LoopTermination) string {
	if term == nil {
		return "<nil: parked at a human boundary>"
	}
	return string(term.State) + " / " + term.Reason + " / " + string(term.Class)
}
