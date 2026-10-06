// Package live_r2 is the R2 REAL AGENTIC REPAIR experiment.
//
// It runs the production composition (compose.Wire → the real bounded autonomy
// Driver → the real RuntimeExecutor → a REAL local Ollama model) against a tiny
// isolated workspace holding an intentionally incorrect artifact, using an
// objective that does NOT name the target file:
//
//	index.html          <h1 id="greeting">Helo</h1>     (the defect)
//	objective           inspect this project, find the incorrect greeting,
//	                    fix it to "Hello", and verify the result.
//
// The purpose is to force actual discovery and reasoning rather than replay of
// an already-known mutation, and to read the answer out of the AUTHORITATIVE
// runtime events — never out of model prose.
//
// It is opt-in via IZEN_LIVE_FORENSICS=1, exactly like test/live_r1, and is not
// part of `go test ./...`.
package live_r2

import (
	"context"
	"encoding/json"
	"fmt"
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

	// r2Objective NAMES NO FILE. Discovery is the thing under test.
	r2Objective = `inspect this project, find the incorrect greeting, fix it to "Hello", and verify the result.`

	// r2ArtifactDirEnv points at a repo-relative directory the harness writes the
	// reconstructed trace and the raw event stream into, so the evidence survives
	// the test log and can be re-read.
	r2ArtifactDirEnv = "IZEN_R2_ARTIFACTS"
)

// defectiveIndexHTML holds an obvious defect: the greeting is misspelled.
const defectiveIndexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Greeting</title>
</head>
<body>
<h1 id="greeting">Helo</h1>
</body>
</html>
`

func requireLiveModel(t *testing.T) {
	t.Helper()
	if os.Getenv(liveOptIn) != "1" {
		t.Skipf("set %s=1 to run the live R2 experiment (needs a local model server)", liveOptIn)
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

func liveApp(t *testing.T, objective string) (*compose.Application, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(defectiveIndexHTML), 0o644); err != nil {
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
		appruntime.SubmitPromptCmd{Prompt: objective, Mode: "build"}); err != nil {
		t.Fatalf("SubmitPromptCmd (production preflight dispatch): %v", err)
	}
	return app, root
}

type result struct {
	Trace      *forensics.Trace
	State      autonomy.RuntimeState
	Term       *autonomy.LoopTermination
	IndexHTML  string
	Boundary   *autonomy.HumanBoundary
	RawStream  []events.DomainEvent
	ArtifactIn []string
}

// runObservation executes one full run of the R2 objective and reconstructs its
// trace. It does NOT auto-answer a discovery/clarification boundary: answering
// with the filename would substitute human discovery for runtime discovery, which
// is the exact confusion the experiment exists to avoid. It DOES answer an
// approval boundary (that is a review of a produced candidate, not discovery).
func runObservation(t *testing.T) result {
	t.Helper()
	return runObjective(t, r2Objective)
}

func runObjective(t *testing.T, objective string) result {
	t.Helper()
	app, root := liveApp(t, objective)
	before, _ := os.ReadFile(filepath.Join(root, "index.html"))

	rec := forensics.NewRecorder()
	sub := app.Bus.SubscribeAll(rec.Handle)

	d := app.Autonomous
	d.SetScope("$prompt") // the EXACT production call runAutonomousDriver makes

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	term, err := d.Run(ctx, objective)
	if err != nil {
		t.Fatalf("driver.Run: %v", err)
	}
	t.Logf("driver.Run -> term=%s boundary=%s", fmtTerm(term), fmtBoundary(d.Boundary()))

	// Answer ONLY an approval boundary, exactly as the TUI's
	// resumeAutonomousApprove does. A clarification/disambiguation boundary is
	// recorded and left parked.
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
	after, _ := os.ReadFile(filepath.Join(root, "index.html"))

	res := result{
		Trace:     tr,
		State:     d.State(),
		Term:      term,
		IndexHTML: string(after),
		Boundary:  d.Boundary(),
		RawStream: stream,
	}
	if string(before) != string(after) {
		res.ArtifactIn = append(res.ArtifactIn, "index.html CHANGED")
	} else {
		res.ArtifactIn = append(res.ArtifactIn, "index.html UNCHANGED")
	}
	persistArtifacts(t, artifactSlug(objective), tr, stream)
	return res
}

// artifactSlug is a filesystem-safe short label for one run's artifacts.
func artifactSlug(objective string) string {
	switch {
	case strings.EqualFold(objective, r2Objective):
		return "r2"
	case strings.Contains(objective, "HTML"):
		return "diagnostic-declared-kind"
	default:
		return "run"
	}
}

// persistArtifacts writes the reconstructed trace and the raw event stream into
// the directory named by IZEN_R2_ARTIFACTS, so the R2 evidence survives the test
// log. A failure to write is a test failure: evidence that silently fails to
// persist is worse than no evidence.
func persistArtifacts(t *testing.T, name string, tr *forensics.Trace, stream []events.DomainEvent) {
	t.Helper()
	dir := os.Getenv(r2ArtifactDirEnv)
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("r2 artifacts dir: %v", err)
	}
	if err := tr.WriteNDJSON(filepath.Join(dir, name+"-trace.ndjson")); err != nil {
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
	if err := os.WriteFile(filepath.Join(dir, name+"-events.ndjson"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write raw events: %v", err)
	}
	t.Logf("R2 artifacts written to %s (%s)", dir, name)
}

// TestLiveR2_Observation is the measurement arm: it runs the objective against
// the real model and prints the authoritative trace. It asserts NOTHING about
// success so the first run measures behaviour instead of a hypothesis.
func TestLiveR2_Observation(t *testing.T) {
	requireLiveModel(t)
	r := runObservation(t)
	t.Logf("\n%s", r.Trace.Render())
	t.Logf("WORKSPACE index.html = %q", r.IndexHTML)
	t.Logf("FINAL state=%s boundary=%s", r.State, fmtBoundary(r.Boundary))
}

// TestLiveR2_DiagnosticDeclaredKind is a DIAGNOSTIC, not the benchmark. It uses
// the same workspace and the same no-filename shape, but the objective declares
// the artifact kind ("HTML"), which is the ONE condition under which the
// runtime's evidence-bound scope derivation can bind a target without a human.
//
// It exists to separate "the targetless objective cannot be resolved" from "the
// downstream inspect→mutate→observe→verify chain is broken". The benchmark arm
// above does not declare a kind, so it may differ only at discovery.
func TestLiveR2_DiagnosticDeclaredKind(t *testing.T) {
	requireLiveModel(t)
	objective := `inspect this project, find the incorrect message in the HTML, fix it to "Welcome", and verify the result.`
	r := runObjective(t, objective)
	t.Logf("\n%s", r.Trace.Render())
	t.Logf("WORKSPACE index.html = %q", r.IndexHTML)
	t.Logf("FINAL state=%s boundary=%s", r.State, fmtBoundary(r.Boundary))
}

// TestLiveR2_AcceptanceChain is the R2 ACCEPTANCE test for the benchmark
// objective, which names no file. It asserts the required chain:
//
//	discover → inspect → reason → mutate → observe → verify → PROVEN
//
// It reads every link from AUTHORITATIVE runtime events and the on-disk
// workspace, never from model prose. It is deliberately allowed to FAIL: the
// current runtime result is BLOCKED at discovery→inspection, and an acceptance
// test that asserted the block as success would hide the gap.
func TestLiveR2_AcceptanceChain(t *testing.T) {
	requireLiveModel(t)
	r := runObjective(t, r2Objective)
	tr := r.Trace
	t.Logf("\n%s", tr.Render())
	t.Logf("WORKSPACE index.html = %q", r.IndexHTML)

	// LINK 1: the authorizing directive reached the runtime.
	if tr.Authorization.Mode != "$prompt" {
		t.Fatalf("R2 BLOCKED at objective→authorization: mode=%q, want $prompt", tr.Authorization.Mode)
	}

	// LINK 2: DISCOVERY actually observed the workspace. The evidence is the
	// candidate the bounded scan offered (the only way a no-filename objective
	// can learn the file's existence).
	discovered := false
	if r.Boundary != nil {
		for _, o := range r.Boundary.Options {
			if strings.Contains(o, "index.html") {
				discovered = true
			}
		}
	}
	for _, tgt := range tr.Spec.Targets {
		if strings.Contains(tgt, "index.html") {
			discovered = true
		}
	}
	if !discovered {
		t.Fatalf("R2 BLOCKED at discovery: no candidate was observed (spec=%+v boundary=%s)",
			tr.Spec, fmtBoundary(r.Boundary))
	}

	// LINK 3: INSPECTION. The runtime must have put the target's bytes in front
	// of the model, which it does by binding the target as a context channel.
	if len(tr.Spec.ContextChannels) == 0 {
		t.Fatalf("R2 BLOCKED at discovery→inspection: the runtime discovered %v but bound no target and inspected nothing; "+
			"verdict=%s reason=%q",
			r.Boundary.Options, tr.Authorization.Verdict, tr.Authorization.Reason)
	}

	// LINK 4: a model computation produced the repair (the mutation call carries
	// the target content in its prompt).
	if len(tr.ModelCalls) == 0 {
		t.Fatalf("R2 BLOCKED at model computation: no provider call was made\n%s", tr)
	}

	// LINK 5: the mutation actually applied and changed the filesystem.
	applied := false
	for _, m := range tr.Mutations {
		if m.ApplyExecuted && m.FilesystemChanged {
			applied = true
		}
	}
	if !applied {
		t.Fatalf("R2 BLOCKED at mutation: no applied mutation with fs_changed\n%s", tr)
	}

	// LINK 6: the change is visible on disk, not merely claimed.
	if strings.Contains(r.IndexHTML, "Helo") || !strings.Contains(r.IndexHTML, "Hello") {
		t.Fatalf("R2 BLOCKED at observation: workspace does not hold the repair: %q", r.IndexHTML)
	}

	// LINK 7: verification was entered and published a verdict.
	if state := tr.VerificationState(); state == events.VerificationUnknown {
		t.Fatalf("R2 BLOCKED at verification: no verification record\n%s", tr)
	}

	// LINK 8: the objective was PROVEN by the completion authority.
	proven := false
	for _, o := range tr.ObjectiveStates {
		if o.Granted && strings.EqualFold(strings.TrimSpace(o.State), "PROVEN") {
			proven = true
		}
	}
	if !proven {
		t.Fatalf("R2 BLOCKED at objective evaluation: completion was never PROVEN\n%s", tr)
	}
	if r.State != autonomy.RuntimeCompleted {
		t.Fatalf("R2 BLOCKED at completion: final state=%s, want completed\n%s", r.State, tr)
	}
}

func fmtBoundary(b *autonomy.HumanBoundary) string {
	if b == nil {
		return "<nil>"
	}
	return fmt.Sprintf("action=%s patch=%s targets=%v reason=%q options=%v",
		b.Action, b.PatchID, b.Targets, b.Reason, b.Options)
}

func fmtTerm(term *autonomy.LoopTermination) string {
	if term == nil {
		return "<nil: parked at a human boundary>"
	}
	return string(term.State) + " / " + term.Reason + " / " + string(term.Class)
}
