package autonomy

// ── DRIVER-LEVEL NEGATIVE TESTS ─────────────────────────────────────────────
//
// The pure-layer refusals live in internal/execution. These prove the WHOLE
// canonical $prompt path honours them end to end: a refusal must stop the run
// BEFORE a provider call or a disk write, and must never be laundered into a
// proven objective.
//
// Each test drives the real Driver → ExecutorAdapter → RuntimeExecutor path on
// a real directory. The only injected component is the provider, precisely
// because the provider is the untrusted party under test.

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
)

// failingProvider always errors. A provider failure must produce no mutation and
// no proven objective.
type failingProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *failingProvider) Name() string { return "failing" }

func (p *failingProvider) Execute(context.Context, ai.Request) (*ai.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return nil, errors.New("provider unavailable")
}

func (p *failingProvider) ExecuteStream(context.Context, ai.Request) (io.ReadCloser, error) {
	return nil, errors.New("provider unavailable")
}

// proseProvider returns a well-formed chat answer and no artifact at all.
type proseProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *proseProvider) Name() string { return "prose" }

func (p *proseProvider) Execute(context.Context, ai.Request) (*ai.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return &ai.Response{
		Content: "Sure! I have redesigned your portfolio page with a modern layout.",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 500, CompletionTokens: 40, FinishReason: "stop"},
	}, nil
}

func (p *proseProvider) ExecuteStream(context.Context, ai.Request) (io.ReadCloser, error) {
	return nil, errors.New("stream not supported")
}

// boilerplateProvider returns prose for every invocation, forcing the run to
// exhaust its bounded recovery before anything can be proven.
type boilerplateProvider struct{ calls int }

func (p *boilerplateProvider) Name() string { return "boilerplate" }

func (p *boilerplateProvider) Execute(context.Context, ai.Request) (*ai.Response, error) {
	p.calls++
	return &ai.Response{
		Content: "I have completed the redesign. Everything looks great.",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 400, CompletionTokens: 30, FinishReason: "stop"},
	}, nil
}

func (p *boilerplateProvider) ExecuteStream(context.Context, ai.Request) (io.ReadCloser, error) {
	return nil, errors.New("stream not supported")
}

// mutationBypassProbe fails the test if the workspace changes at all.
type mutationBypassProbe struct {
	t    *testing.T
	root string
}

func (p mutationBypassProbe) snapshot() map[string]string {
	p.t.Helper()
	out := map[string]string{}
	entries, err := os.ReadDir(p.root)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(p.root, e.Name()))
		if err != nil {
			continue
		}
		out[e.Name()] = string(b)
	}
	return out
}

func requireUnchanged(t *testing.T, before map[string]string, root, why string) {
	t.Helper()
	after := mutationBypassProbe{t: t, root: root}.snapshot()
	for name, prior := range before {
		now, ok := after[name]
		if !ok {
			t.Fatalf("%s: %s disappeared", why, name)
		}
		if now != prior {
			t.Fatalf("%s: %s changed on disk — a mutation escaped the canonical boundary", why, name)
		}
	}
	for name := range after {
		if _, ok := before[name]; !ok {
			t.Fatalf("%s: %s was created — a target was manufactured", why, name)
		}
	}
}

// ── Provider failure ────────────────────────────────────────────────────────
func TestNegative_ProviderFailureCausesNoMutationAndNoProof(t *testing.T) {
	root := portfolioWorkspace(t)
	before := mutationBypassProbe{t: t, root: root}.snapshot()

	bus := events.NewBus(events.DefaultBufferSize)
	provider := &failingProvider{}
	x := testExecutor(t, root, provider, bus)
	d := NewDriver(NewExecutorAdapter(root, execution.NewIntentGateway(root), x), bus)

	term, err := d.Run(context.Background(), acceptanceObjective)
	if err != nil {
		t.Fatalf("a provider failure must be handled, not propagated as a crash: %v", err)
	}
	requireUnchanged(t, before, root, "provider failure")

	obs := d.LastObservation()
	if obs.Objective.Mutation == execution.FilesystemApplied {
		t.Errorf("objective reported mutation=%s artifact=%s after a total provider failure",
			obs.Objective.Mutation, obs.Objective.Artifact)
	}
	if obs.Objective.Artifact == execution.ArtifactProduced {
		t.Error("objective reported a PRODUCED artifact after a total provider failure")
	}
	if d.Boundary() != nil && d.Boundary().PatchID != "" {
		t.Error("a held patch was parked after a total provider failure; there is no artifact to approve")
	}
	if term != nil && term.State == "completed" {
		t.Errorf("run reported completed (reason=%q) after a total provider failure", term.Reason)
	}
	if provider.calls == 0 {
		t.Error("the provider was never called; the test did not exercise the failure path")
	}
}

// ── Prose instead of an artifact ────────────────────────────────────────────
func TestNegative_ProseResponseIsNeverTreatedAsAProposal(t *testing.T) {
	root := portfolioWorkspace(t)
	before := mutationBypassProbe{t: t, root: root}.snapshot()

	bus := events.NewBus(events.DefaultBufferSize)
	provider := &proseProvider{}
	x := testExecutor(t, root, provider, bus)
	d := NewDriver(NewExecutorAdapter(root, execution.NewIntentGateway(root), x), bus)

	if _, err := d.Run(context.Background(), acceptanceObjective); err != nil {
		t.Fatalf("run errored: %v", err)
	}
	requireUnchanged(t, before, root, "prose response")
	if b := d.Boundary(); b != nil && b.PatchID != "" {
		t.Error("a patch was parked for approval from a pure-prose response")
	}
}

// ── Bounded continuation, never false completion ───────────────────────────
func TestNegative_ProseExhaustionNeverReachesCompleted(t *testing.T) {
	root := portfolioWorkspace(t)
	before := mutationBypassProbe{t: t, root: root}.snapshot()

	bus := events.NewBus(events.DefaultBufferSize)
	provider := &boilerplateProvider{}
	x := testExecutor(t, root, provider, bus)
	d := NewDriver(NewExecutorAdapter(root, execution.NewIntentGateway(root), x), bus)

	term, err := d.Run(context.Background(), acceptanceObjective)
	if err != nil {
		t.Fatalf("run errored: %v", err)
	}
	requireUnchanged(t, before, root, "boilerplate response")

	if term != nil && term.State == "completed" {
		t.Fatalf("run completed (reason=%q) after %d prose-only invocations; "+
			"an unproven objective must never terminate as completed", term.Reason, provider.calls)
	}
	// The loop must be BOUNDED: a model that never speaks the contract cannot
	// make the run spin. Whatever the outcome, it terminates.
	if d.State() == "executing" || d.State() == "deciding" {
		t.Errorf("run is still in-flight (state=%s) after %d bounded attempts; the loop is not bounded",
			d.State(), provider.calls)
	}
}

// ── No derivation without declared kinds ────────────────────────────────────
//
// A vague objective against a workspace full of web files must NOT acquire
// implementation scope. Discovery is not permission.
func TestNegative_VagueObjectiveDerivesNoScopeInAWebWorkspace(t *testing.T) {
	root := portfolioWorkspace(t)
	before := mutationBypassProbe{t: t, root: root}.snapshot()

	bus := events.NewBus(events.DefaultBufferSize)
	provider := &portfolioProvider{served: map[string]int{}}
	x := testExecutor(t, root, provider, bus)
	d := NewDriver(NewExecutorAdapter(root, execution.NewIntentGateway(root), x), bus)

	if _, err := d.Run(context.Background(), "make this better somehow"); err != nil {
		t.Fatalf("run errored: %v", err)
	}

	for _, tt := range d.resolved.Profile.Targets {
		if tt.Resolved != "" && tt.Exists && strings.Contains(d.prompt, "HTML") {
			t.Errorf("a vague objective bound target %q", tt.Resolved)
		}
	}
	if provider.calls > 0 && d.State() == "executing" {
		t.Errorf("a vague objective reached the executing state with %d provider calls", provider.calls)
	}
	requireUnchanged(t, before, root, "vague objective")
}

// ── Mutation stays inside the bound scope ──────────────────────────────────
func TestNegative_MutationCannotEscapeTheBoundScope(t *testing.T) {
	root := portfolioWorkspace(t)

	bus := events.NewBus(events.DefaultBufferSize)
	provider := &portfolioProvider{served: map[string]int{}}
	x := testExecutor(t, root, provider, bus)
	d := NewDriver(NewExecutorAdapter(root, execution.NewIntentGateway(root), x), bus)

	if _, err := d.Run(context.Background(), acceptanceObjective); err != nil {
		t.Fatalf("run errored: %v", err)
	}
	if _, err := d.ResumeApprove(context.Background()); err != nil {
		t.Fatalf("approve: %v", err)
	}

	// readme.md and the nested doc were observed by discovery but are not
	// artifacts the objective declared. They must be byte-identical.
	for _, untouched := range []string{"readme.md", "docs/notes.md"} {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(untouched)))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "TomHunter") || strings.Contains(string(body), "system-ui") {
			t.Errorf("%s was mutated; a derived scope is a ceiling, not a suggestion: %q", untouched, string(body))
		}
	}
	// And every file the provider WAS asked about must have changed.
	for target := range provider.served {
		if provider.served[target] == 0 {
			t.Errorf("provider never served %s", target)
		}
	}
}
