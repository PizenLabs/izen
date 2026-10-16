// Package realworld is the REAL-WORLD AGENT RUNTIME validation harness.
//
// It is not a simulator and it fabricates nothing. It drives the production
// composition — compose.Wire → the real bounded autonomy Driver → the real
// RuntimeExecutor → a REAL local Ollama model — against small but meaningful
// repository fixtures, plays the human at the boundaries the TUI would surface,
// and records the RAW runtime event stream plus the filesystem delta.
//
// Every claim in its output is read from an authoritative runtime event or from
// the real filesystem. Model prose is captured but never treated as evidence.
//
// It is opt-in via IZEN_LIVE_FORENSICS=1, exactly like test/live_r1..r6, and is
// not part of `go test ./...`.
package realworld

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
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

	artifactDirEnv = "IZEN_REALWORLD_ARTIFACTS"
)

// Task is one representative real-world objective plus the fixture it runs
// against. A fixture is a set of repository-relative files; gitInit makes the
// workspace a real repository, which is a production precondition (build
// execution requires a checkpoint and a checkpoint is a git commit).
type Task struct {
	// ID is the short stable label used for artifact filenames.
	ID string
	// Name is the human title used in the report.
	Name string
	// Prompt is the exact objective text handed to the runtime (the text after
	// the `$prompt` directive). It deliberately names no file where discovery is
	// under test.
	Prompt string
	// Files is the fixture, keyed by repository-relative path.
	Files map[string]string
	// AnswerApprovals controls whether the harness plays the human at an
	// approval (patch review) boundary. It never answers a clarification or
	// ambiguity boundary unless ClarifyAnswer is set: resolving discovery for
	// the runtime would defeat the experiment.
	AnswerApprovals bool
	// ClarifyAnswer, when non-empty, is the target the harness names at a
	// clarify boundary. It exists ONLY for the diagnostic arm that isolates
	// "discovery is blocked" from "the downstream chain is broken"; the main
	// arms leave it empty so discovery stays the runtime's job.
	ClarifyAnswer string

	// Surface is the command surface the run is admitted under: "$prompt"
	// (default), "$build" (an ordinary prompt inside /build), or "$hot". It is
	// the same value the TUI hands the driver via SetScope, so a live arm can
	// exercise a specific entry point's authority without driving the TUI.
	Surface string

	// Provider, when non-nil, overrides the default local Ollama provider for
	// this task. It lets the benchmark drive the EXACT configured production
	// provider (e.g. OpenRouter) without changing the composition.
	Provider ai.Provider
	// Config, when non-nil, replaces config.Default() in the wiring.
	Config *config.Config
}

// requireLiveModel skips unless the operator opted in AND a local model server
// answers. A fake model here would defeat the experiment's purpose.
func requireLiveModel(t *testing.T) {
	t.Helper()
	if os.Getenv(liveOptIn) != "1" {
		t.Skipf("set %s=1 to run the real-world validation (needs a local model server)", liveOptIn)
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

// ── workspace provisioning ──────────────────────────────────────────────────

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

// provision writes the fixture and makes it a real repository + IZEN workspace.
func provision(t *testing.T, task Task) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range task.Files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitInit(t, root)
	if err := os.MkdirAll(filepath.Join(root, ".izen"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// wireAppForTask is wireApp with an optional provider/config override so the
// benchmark can drive the exact configured production provider.
func wireAppForTask(t *testing.T, root string, task Task) *compose.Application {
	t.Helper()
	cfg := task.Config
	if cfg == nil {
		cfg = config.Default()
		cfg.Bindings.Active.Provider = "ollama"
		cfg.Bindings.Active.Model = liveModel
	}
	opts := []compose.Option{
		compose.WithRoot(root),
		compose.WithConfig(cfg),
	}
	if task.Provider != nil {
		opts = append(opts, compose.WithProvider(task.Provider))
	} else {
		opts = append(opts, compose.WithProvider(providers.NewOllamaProvider(liveBaseURL, "ollama", liveModel)))
	}
	app, err := compose.Wire(opts...)
	if err != nil {
		t.Fatalf("wire: %v", err)
	}
	t.Cleanup(app.Close)

	if _, err := app.Execution.ShadowCP.CreateSessionStartSnapshot(); err != nil {
		t.Fatalf("session-start checkpoint (production precondition): %v", err)
	}
	app.Runtime.Start()
	if err := app.Runtime.Execute(context.Background(),
		appruntime.SubmitPromptCmd{Prompt: task.Prompt, Mode: "build"}); err != nil {
		t.Fatalf("SubmitPromptCmd (production preflight dispatch): %v", err)
	}
	return app
}

// ── run record ──────────────────────────────────────────────────────────────

// RunRecord is the truthful summary of one real-world run. It is derived only
// from authoritative runtime events and the real filesystem.
type RunRecord struct {
	Task       string
	Name       string
	Prompt     string
	StartedAt  time.Time
	EndedAt    time.Time
	WallTimeMS int64

	FinalState        string
	Termination       string
	TermReason        string
	BoundaryAction    string
	BoundaryReason    string
	BoundaryTargets   []string
	BoundaryOptions   []string
	BoundaryResumable bool

	ModelCalls      int
	ModelFailures   int
	OutputExhausted int
	Continuations   int
	InputTokens     int
	OutputTokens    int
	UsageKnown      bool

	Mutations     []string
	Verifications []string
	Objective     []string
	Capabilities  []string
	Patterns      []string

	EventCounts map[string]int

	FilesBefore map[string]string
	FilesAfter  map[string]string
	Delta       []string
	// ChangedContents holds the post-run bytes of added/changed files, bounded
	// so the evidence cannot be grown by a large artifact.
	ChangedContents map[string]string

	// Notes records honest observations that are not runtime facts (e.g. "no
	// approval boundary was ever presented").
	Notes []string

	RawStream []events.DomainEvent
	Trace     *forensics.Trace
}

// FileState is a content digest of one workspace file.
type FileState struct {
	Digest string
	Size   int
}

const snapshotExclude = ".git"

// snapshot walks the workspace and returns a path→digest map, excluding the
// VCS directory and IZEN's own state (neither is the task artifact).
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // best-effort snapshot: skip unreadable paths and keep walking
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		top := strings.SplitN(rel, "/", 2)[0]
		if top == snapshotExclude || top == ".izen" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil //nolint:nilerr // best-effort snapshot: skip unreadable files and keep walking
		}
		sum := sha256.Sum256(b)
		out[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	return out
}

// changedContents reads the post-run bytes of every added/changed file, bounded
// so the evidence cannot be grown by a large artifact.
func changedContents(root string, before, after map[string]string) map[string]string {
	const maxBytes = 4096
	out := map[string]string{}
	for p, d := range after {
		if b, ok := before[p]; ok && b == d {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			continue
		}
		body := string(b)
		if len(body) > maxBytes {
			body = body[:maxBytes] + "\n… [truncated]"
		}
		out[p] = body
	}
	return out
}

func diffSnapshots(before, after map[string]string) []string {
	var out []string
	for p, d := range after {
		if b, ok := before[p]; !ok {
			out = append(out, "ADDED   "+p)
		} else if b != d {
			out = append(out, "CHANGED "+p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			out = append(out, "REMOVED "+p)
		}
	}
	sort.Strings(out)
	return out
}

// run executes one task end to end and returns its evidence. It answers ONLY an
// approval boundary (a review of a produced candidate). A clarify / inform /
// proposal boundary is recorded and left parked: answering it would substitute
// human discovery for runtime discovery.
func run(t *testing.T, task Task) *RunRecord {
	t.Helper()
	root := provision(t, task)
	before := snapshot(t, root)

	app := wireAppForTask(t, root, task)

	rec := forensics.NewRecorder()
	sub := app.Bus.SubscribeAll(rec.Handle)

	d := app.Autonomous
	// The EXACT production call runAutonomousDriver makes: the recorded command
	// surface of the admitting entry point ($prompt / $build / $hot).
	surface := task.Surface
	if surface == "" {
		surface = "$prompt"
	}
	d.SetScope(surface)

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	term, err := d.Run(ctx, task.Prompt)
	if err != nil {
		t.Fatalf("[%s] driver.Run: %v", task.Name, err)
	}
	t.Logf("[%s] driver.Run -> %s", task.Name, fmtTerm(term))

	approvals := 0
	for i := 0; i < 8 && d.State() == autonomy.RuntimeAwaitingHuman; i++ {
		b := d.Boundary()
		if b == nil {
			break
		}
		if b.Action != autonomy.HumanBoundaryApproval {
			if b.Action == autonomy.HumanBoundaryClarify && strings.TrimSpace(task.ClarifyAnswer) != "" {
				t.Logf("[%s] DIAGNOSTIC: answering clarify %v with %q (main arm leaves this unanswered)",
					task.Name, b.Options, task.ClarifyAnswer)
				term, err = d.ResumeClarify(ctx, task.ClarifyAnswer)
				if err != nil {
					t.Fatalf("[%s] driver.ResumeClarify: %v", task.Name, err)
				}
				t.Logf("[%s] driver.ResumeClarify -> %s", task.Name, fmtTerm(term))
				continue
			}
			t.Logf("[%s] PARKED at %s boundary: reason=%q options=%v — NOT answered (discovery stays the runtime's job)",
				task.Name, b.Action, b.Reason, b.Options)
			break
		}
		if !task.AnswerApprovals {
			t.Logf("[%s] approval boundary presented but task disables answering: %q", task.Name, b.Reason)
			break
		}
		if err := app.Orchestrator.Transition(domainorch.PhaseBuild,
			workflow.TransitionContext{HasPlan: true, HasCapabilities: true}); err != nil {
			t.Logf("[%s] orchestrator transition to build: %v", task.Name, err)
		}
		preview, ok := app.Executor.CandidatePreview(b.PatchID)
		if !ok {
			t.Fatalf("[%s] approval boundary holds no reviewable candidate: %+v", task.Name, b)
		}
		auth, aerr := app.Auth.AuthorizeBuildCandidateContent(
			b.Targets, app.Caps, app.Budget, app.MicroBudget, false, true,
			b.CandidateID, preview.Digest())
		if aerr != nil {
			t.Fatalf("[%s] authorize reviewed candidate: %v", task.Name, aerr)
		}
		approvals++
		app.Executor.SetAuthorization(auth)
		term, err = d.ResumeApprove(ctx)
		if err != nil {
			t.Fatalf("[%s] driver.ResumeApprove: %v", task.Name, err)
		}
		t.Logf("[%s] driver.ResumeApprove -> %s", task.Name, fmtTerm(term))
	}
	_ = approvals

	rec.WaitFor(events.EventExecutionSummary, 1, 30*time.Second)
	rec.WaitQuiet(250*time.Millisecond, 10*time.Second)
	sub.Cancel()
	end := time.Now()

	stream := rec.Stream()
	tr, terr := forensics.NewTrace(stream)
	if terr != nil {
		t.Fatalf("[%s] reconstruct trace: %v", task.Name, terr)
	}
	after := snapshot(t, root)

	rec0 := &RunRecord{
		Task:            task.ID,
		Name:            task.Name,
		Prompt:          task.Prompt,
		StartedAt:       start,
		EndedAt:         end,
		WallTimeMS:      end.Sub(start).Milliseconds(),
		FinalState:      string(d.State()),
		Termination:     fmtTerm(term),
		FilesBefore:     before,
		FilesAfter:      after,
		Delta:           diffSnapshots(before, after),
		ChangedContents: changedContents(root, before, after),
		RawStream:       stream,
		Trace:           tr,
		EventCounts:     map[string]int{},
	}
	if term != nil {
		rec0.TermReason = term.Reason
	}
	if b := d.Boundary(); b != nil {
		rec0.BoundaryAction = string(b.Action)
		rec0.BoundaryReason = b.Reason
		rec0.BoundaryTargets = append([]string{}, b.Targets...)
		rec0.BoundaryOptions = append([]string{}, b.Options...)
		rec0.BoundaryResumable = b.Resumable
	}
	rec0.ModelCalls = len(tr.ModelCalls)
	rec0.ModelFailures = tr.ModelFailures
	rec0.OutputExhausted = tr.OutputExhausted
	rec0.Continuations = tr.Continuations
	rec0.Patterns = append([]string{}, tr.Patterns...)
	rec0.Capabilities = append([]string{}, tr.Authorization.Capabilities...)
	for _, m := range tr.Mutations {
		rec0.Mutations = append(rec0.Mutations, fmt.Sprintf("target=%s outcome=%s applied=%t fs_changed=%t adds=%d removes=%d",
			m.Target, m.Outcome, m.ApplyExecuted, m.FilesystemChanged, m.DiffAdds, m.DiffRemoves))
	}
	for _, v := range tr.Verifications {
		rec0.Verifications = append(rec0.Verifications, fmt.Sprintf("applicable=%t passed=%t reason=%q", v.Applicable, v.Passed, v.Reason))
	}
	for _, o := range tr.ObjectiveStates {
		rec0.Objective = append(rec0.Objective, fmt.Sprintf("state=%s granted=%t reason=%q", o.State, o.Granted, o.Reason))
	}
	for _, ev := range stream {
		rec0.EventCounts[ev.Type()]++
	}
	in, out, known := d.AggregatedUsage()
	rec0.InputTokens, rec0.OutputTokens, rec0.UsageKnown = in, out, known

	// Invariant guard (truth, not benchmark): a COMPLETED terminal state must
	// carry a PROVEN objective. If it does not, the trace itself is the defect.
	if rec0.FinalState == string(autonomy.RuntimeCompleted) {
		proven := false
		for _, o := range tr.ObjectiveStates {
			if o.Granted && strings.EqualFold(strings.TrimSpace(o.State), "PROVEN") {
				proven = true
			}
		}
		if !proven {
			rec0.Notes = append(rec0.Notes,
				"VIOLATION: terminal state COMPLETED with no PROVEN objective event")
		}
	}

	renderRecord(t, rec0)
	persist(t, rec0)
	return rec0
}

func fmtTerm(term *autonomy.LoopTermination) string {
	if term == nil {
		return "<nil: parked at a human boundary>"
	}
	return fmt.Sprintf("%s reason=%q class=%s", term.State, term.Reason, term.Class)
}

// renderRecord prints the raw-evidence summary. It never prints the word
// "success"; the objective axis is the verdict.
func renderRecord(t *testing.T, r *RunRecord) {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "\n═══ %s — %s ═══\n", r.Task, r.Name)
	fmt.Fprintf(&b, "prompt:     %s\n", r.Prompt)
	fmt.Fprintf(&b, "wall:       %dms\n", r.WallTimeMS)
	fmt.Fprintf(&b, "state:      %s\n", r.FinalState)
	fmt.Fprintf(&b, "termination:%s\n", r.Termination)
	fmt.Fprintf(&b, "boundary:   action=%s resumable=%t targets=%v options=%v reason=%q\n",
		r.BoundaryAction, r.BoundaryResumable, r.BoundaryTargets, r.BoundaryOptions, r.BoundaryReason)
	fmt.Fprintf(&b, "model:      calls=%d failures=%d output_exhausted=%d continuations=%d tokens=%d/%d known=%t\n",
		r.ModelCalls, r.ModelFailures, r.OutputExhausted, r.Continuations, r.InputTokens, r.OutputTokens, r.UsageKnown)
	fmt.Fprintf(&b, "caps:       %v\n", r.Capabilities)
	fmt.Fprintf(&b, "mutations:  %v\n", r.Mutations)
	fmt.Fprintf(&b, "verify:     %v\n", r.Verifications)
	fmt.Fprintf(&b, "objective:  %v\n", r.Objective)
	fmt.Fprintf(&b, "patterns:   %v\n", r.Patterns)
	fmt.Fprintf(&b, "delta:      %v\n", r.Delta)
	if len(r.ChangedContents) > 0 {
		keys := make([]string, 0, len(r.ChangedContents))
		for k := range r.ChangedContents {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "content[%s]: %q\n", k, r.ChangedContents[k])
		}
	}
	if len(r.Notes) > 0 {
		fmt.Fprintf(&b, "notes:      %v\n", r.Notes)
	}
	fmt.Fprintf(&b, "events:\n")
	keys := make([]string, 0, len(r.EventCounts))
	for k := range r.EventCounts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "  %-36s %d\n", k, r.EventCounts[k])
	}
	t.Logf("%s", b.String())
	t.Logf("\n%s", r.Trace.Render())
}

// persist writes the raw event stream, the reconstructed trace and the summary
// so the evidence survives the test log and can be re-read for the report.
func persist(t *testing.T, r *RunRecord) {
	t.Helper()
	dir := os.Getenv(artifactDirEnv)
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("artifacts dir: %v", err)
	}
	// Raw events.
	var lines []string
	for _, ev := range r.RawStream {
		payload := map[string]any{"type": ev.Type(), "at": ev.Timestamp()}
		if b, err := json.Marshal(ev.Payload()); err == nil {
			payload["payload"] = json.RawMessage(b)
		}
		if line, err := json.Marshal(payload); err == nil {
			lines = append(lines, string(line))
		}
	}
	writeFile(t, filepath.Join(dir, r.Task+"-events.ndjson"), strings.Join(lines, "\n")+"\n")
	if err := r.Trace.WriteNDJSON(filepath.Join(dir, r.Task+"-trace.ndjson")); err != nil {
		t.Fatalf("write trace: %v", err)
	}
	summary := map[string]any{
		"task": r.Task, "name": r.Name, "prompt": r.Prompt,
		"started_at": r.StartedAt, "ended_at": r.EndedAt, "wall_ms": r.WallTimeMS,
		"final_state": r.FinalState, "termination": r.Termination, "term_reason": r.TermReason,
		"boundary_action": r.BoundaryAction, "boundary_reason": r.BoundaryReason,
		"boundary_targets": r.BoundaryTargets, "boundary_options": r.BoundaryOptions,
		"boundary_resumable": r.BoundaryResumable,
		"model_calls":        r.ModelCalls, "model_failures": r.ModelFailures,
		"output_exhausted": r.OutputExhausted, "continuations": r.Continuations,
		"input_tokens": r.InputTokens, "output_tokens": r.OutputTokens, "usage_known": r.UsageKnown,
		"capabilities": r.Capabilities, "mutations": r.Mutations, "verifications": r.Verifications,
		"objective": r.Objective, "patterns": r.Patterns, "delta": r.Delta, "notes": r.Notes,
		"changed_contents": r.ChangedContents,
		"events":           r.EventCounts,
	}
	b, _ := json.MarshalIndent(summary, "", "  ")
	writeFile(t, filepath.Join(dir, r.Task+"-summary.json"), string(b)+"\n")
	t.Logf("[%s] evidence written to %s", r.Task, dir)
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
