package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/pkg/event"
	"github.com/PizenLabs/izen/pkg/ir"
	"github.com/PizenLabs/izen/pkg/op"
)

// scriptedGenerator serves a scripted sequence of responses, consuming each
// in order; the last response repeats so a repair loop beyond the script does
// not panic.
type scriptedGenerator struct {
	mu   sync.Mutex
	resp []string
}

func (g *scriptedGenerator) Complete(_ context.Context, _, _ string, _ int) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.resp) == 0 {
		return "", errors.New("no scripted response left")
	}
	out := g.resp[0]
	if len(g.resp) > 1 {
		g.resp = g.resp[1:]
	}
	return out, nil
}

// fenced builds a fenced extraction block for the given path and body.
func fenced(lang, path, body string) string {
	return "```" + lang + ":" + path + "\n" + body + "\n```\n"
}

// portfolioPage is a validated portfolio HTML body.
const portfolioPage = `<!DOCTYPE html>
<html><head><title>My Portfolio</title></head><body>
<header><nav><a href="#about">About</a><a href="#projects">Projects</a></nav></header>
<main>
<section id="about"><h1>About me</h1></section>
<section id="projects"><article><h2>Project one</h2></article></section>
<section id="contact"><h2>Contact</h2></section>
</main>
<footer>&copy; 2026</footer>
</body></html>`

// todoPage is a to-do application HTML that must fail portfolio validation.
const todoPage = `<!DOCTYPE html>
<html><head><title>Todo App</title></head><body>
<div><input id="newTodo" placeholder="Add a task"></div>
<div><button onclick="addTask()">Add</button></div>
<div id="taskList"></div>
<script>
let todos = [];
function addTask() { todos.push(document.getElementById("newTodo").value); }
</script>
</body></html>`

func mustPipeline(t *testing.T, opts ...Option) *Pipeline {
	t.Helper()
	p, err := NewPipeline(opts...)
	if err != nil {
		t.Fatalf("NewPipeline: %v", err)
	}
	return p
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestPipelineGreenfieldWritesValidatedArtifacts(t *testing.T) {
	root := t.TempDir()
	gen := &scriptedGenerator{resp: []string{fenced("html", "index.html", portfolioPage)}}
	p := mustPipeline(t, WithRoot(root), WithGenerator(gen))

	events := collectBusEvents(t, p.Bus())

	res, err := p.Run(t.Context(), Request{Intent: "build a portfolio website"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Artifacts) != 1 || res.Artifacts[0].Path != "index.html" {
		t.Fatalf("artifacts = %+v, want index.html", res.Artifacts)
	}
	if got := readFile(t, filepath.Join(root, "index.html")); !strings.Contains(got, "My Portfolio") {
		t.Fatalf("index.html content = %q", got)
	}
	if res.Mode != ModeGreenfield {
		t.Errorf("mode = %s, want greenfield", res.Mode)
	}
	if res.ExtractionAttempts != 1 {
		t.Errorf("extraction attempts = %d, want 1", res.ExtractionAttempts)
	}
	if res.RepairRounds != 0 {
		t.Errorf("repair rounds = %d, want 0", res.RepairRounds)
	}
	if len(res.Capabilities) == 0 {
		t.Error("expected resolved capabilities")
	}
	for _, v := range res.Validations {
		if !v.Passed {
			t.Errorf("validation failed for %s: %v", v.Artifact.Path, v.Reasons)
		}
	}
	if !strings.Contains(res.SystemPrompt, "portfolio") {
		t.Error("system prompt must carry capability constraints")
	}

	// The kernel must have emitted task lifecycle events on the bus.
	waitForEvent(t, events, event.TypeTaskStarted, "")
	waitForEvent(t, events, event.TypeTaskCompleted, "")
}

func TestPipelineExtractionRetryOnMalformedOutput(t *testing.T) {
	root := t.TempDir()
	gen := &scriptedGenerator{resp: []string{
		"Sure! Here is your portfolio description.", // prose, no fences -> reject
		fenced("html", "index.html", portfolioPage),
	}}
	p := mustPipeline(t, WithRoot(root), WithGenerator(gen))

	res, err := p.Run(t.Context(), Request{Intent: "build a portfolio website"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExtractionAttempts != 2 {
		t.Fatalf("extraction attempts = %d, want 2", res.ExtractionAttempts)
	}
	if got := readFile(t, filepath.Join(root, "index.html")); !strings.Contains(got, "My Portfolio") {
		t.Fatalf("index.html content = %q", got)
	}
	if !strings.Contains(res.SystemPrompt, "EXTRACTION REJECTED") {
		t.Error("retry system prompt must carry the extraction failure payload")
	}
}

func TestPipelineValidationGateRejectsTodoAppAndRepairs(t *testing.T) {
	root := t.TempDir()
	gen := &scriptedGenerator{resp: []string{
		fenced("html", "index.html", todoPage),      // fails the portfolio gate
		fenced("html", "index.html", portfolioPage), // passes
	}}
	p := mustPipeline(t, WithRoot(root), WithGenerator(gen))

	res, err := p.Run(t.Context(), Request{Intent: "build a portfolio website"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.RepairRounds != 1 {
		t.Fatalf("repair rounds = %d, want 1", res.RepairRounds)
	}
	if res.ExtractionAttempts != 2 {
		t.Fatalf("extraction attempts = %d, want 2", res.ExtractionAttempts)
	}
	got := readFile(t, filepath.Join(root, "index.html"))
	if strings.Contains(got, "Todo") {
		t.Fatal("to-do app was written to disk; validation gate failed to block it")
	}
	if !strings.Contains(got, "My Portfolio") {
		t.Fatalf("repaired portfolio not written: %q", got)
	}
	if !strings.Contains(res.SystemPrompt, "VALIDATION REJECTED") {
		t.Error("repair system prompt must carry the validation rejection payload")
	}
}

func TestPipelineFailsWhenValidationNeverPasses(t *testing.T) {
	root := t.TempDir()
	gen := &scriptedGenerator{resp: []string{
		fenced("html", "index.html", todoPage),
		fenced("html", "index.html", todoPage),
		fenced("html", "index.html", todoPage),
	}}
	p := mustPipeline(t, WithRoot(root), WithGenerator(gen), WithMaxRepairs(2))

	_, err := p.Run(t.Context(), Request{Intent: "build a portfolio website"})
	if err == nil {
		t.Fatal("expected error when validation never passes")
	}
	var vErr *ValidationError
	if !errors.As(err, &vErr) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "index.html")); statErr == nil {
		t.Fatal("rejected to-do app must never be written to disk")
	}
}

func TestPipelineFailsWhenExtractionNeverAccepts(t *testing.T) {
	root := t.TempDir()
	gen := &scriptedGenerator{resp: []string{"ok", "ok", "ok"}}
	p := mustPipeline(t, WithRoot(root), WithGenerator(gen), WithMaxAttempts(2))

	_, err := p.Run(t.Context(), Request{Intent: "build a portfolio website"})
	if err == nil {
		t.Fatal("expected error when extraction never accepts")
	}
	var eErr *ExtractionError
	if !errors.As(err, &eErr) {
		t.Fatalf("expected ExtractionError, got %T: %v", err, err)
	}
	if eErr.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", eErr.Attempts)
	}
}

func TestPipelineRejectsEscapingArtifactPaths(t *testing.T) {
	root := t.TempDir()
	gen := &scriptedGenerator{resp: []string{fenced("html", "../evil.html", todoPage)}}
	p := mustPipeline(t, WithRoot(root), WithGenerator(gen))

	_, err := p.Run(t.Context(), Request{Intent: "build a portfolio website"})
	if err == nil {
		t.Fatal("expected error for escaping artifact path")
	}
	if _, statErr := os.Stat(filepath.Join(root, "..", "evil.html")); statErr == nil {
		t.Fatal("escaping artifact was written outside the workspace root")
	}
}

func TestPipelineConversationalShortCircuit(t *testing.T) {
	gen := &scriptedGenerator{resp: []string{"Hello! I'm Izen, your coding engine."}}
	p := mustPipeline(t, WithRoot(t.TempDir()), WithGenerator(gen))

	res, err := p.Run(t.Context(), Request{Intent: "hello"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Answer, "coding engine") {
		t.Errorf("answer = %q", res.Answer)
	}
	if len(res.Artifacts) != 0 {
		t.Error("conversational run must not extract artifacts")
	}
	if res.Mode != "" {
		t.Errorf("conversational run must not plan, mode = %s", res.Mode)
	}
}

func TestPipelineBrownfieldUsesRepairPlanner(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module demo\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gen := &scriptedGenerator{resp: []string{fenced("html", "index.html", portfolioPage)}}
	p := mustPipeline(t, WithRoot(root), WithGenerator(gen),
		WithVerifyCommand(func(string) string { return "true" }))

	events := collectBusEvents(t, p.Bus())

	res, err := p.Run(t.Context(), Request{Intent: "build a portfolio website"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Mode != ModeBrownfield {
		t.Fatalf("mode = %s, want brownfield", res.Mode)
	}
	if res.Plan == nil || res.Plan.Metadata["planner"] != "brownfield" {
		t.Fatalf("expected brownfield planner, metadata = %v", res.Plan.Metadata)
	}
	if got := readFile(t, filepath.Join(root, "index.html")); !strings.Contains(got, "My Portfolio") {
		t.Fatalf("index.html content = %q", got)
	}
	waitForEvent(t, events, event.TypeTaskCompleted, "bf-verify")
}

func TestPipelineRejectsEscapingPathBeforeAnyWrite(t *testing.T) {
	root := t.TempDir()
	gen := &scriptedGenerator{resp: []string{fenced("html", "ok.html", portfolioPage) + fenced("html", "../../escape.html", todoPage)}}
	p := mustPipeline(t, WithRoot(root), WithGenerator(gen))

	if _, err := p.Run(t.Context(), Request{Intent: "build a portfolio website"}); err == nil {
		t.Fatal("expected error for multi-artifact escape")
	}
	if _, statErr := os.Stat(filepath.Join(root, "..", "escape.html")); statErr == nil {
		t.Fatal("escaping artifact written outside workspace")
	}
}

// collectBusEvents subscribes to every bus event before Run and returns a
// channel for assertions.
func collectBusEvents(t *testing.T, bus *event.MemoryEventBus) <-chan event.Event {
	t.Helper()
	ch := make(chan event.Event, 64)
	bus.Subscribe(nil, func(e event.Event) { ch <- e })
	return ch
}

// waitForEvent drains ch until an event of the given type (and optional task
// id) arrives.
func waitForEvent(t *testing.T, ch <-chan event.Event, typ event.EventType, taskID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case e := <-ch:
			if e.Type == typ && (taskID == "" || e.TaskID == taskID) {
				return
			}
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatalf("timed out waiting for event %s (task %s)", typ, taskID)
}

func TestPipelineRequiresGenerator(t *testing.T) {
	p := mustPipeline(t, WithRoot(t.TempDir()))
	if _, err := p.Run(t.Context(), Request{Intent: "build a portfolio website"}); !errors.Is(err, ErrNoGenerator) {
		t.Fatalf("expected ErrNoGenerator, got %v", err)
	}
}

func TestPipelineRequiresIntent(t *testing.T) {
	p := mustPipeline(t, WithRoot(t.TempDir()), WithGenerator(&scriptedGenerator{}))
	if _, err := p.Run(t.Context(), Request{Intent: "   "}); !errors.Is(err, ErrEmptyIntent) {
		t.Fatalf("expected ErrEmptyIntent, got %v", err)
	}
}

func TestResolveCapabilitiesForIntent(t *testing.T) {
	p := mustPipeline(t)
	caps, err := ResolveCapabilitiesForIntent(p.registry, "redesign my portfolio website")
	if err != nil {
		t.Fatal(err)
	}
	if len(caps) == 0 {
		t.Fatal("no capabilities resolved")
	}
	ids := make(map[string]bool, len(caps))
	for _, c := range caps {
		ids[string(c.ID())] = true
	}
	if !ids["portfolio_website"] || !ids["semantic_html"] {
		t.Errorf("portfolio intent should resolve portfolio+semantic, got %v", ids)
	}
	caps, err = ResolveCapabilitiesForIntent(p.registry, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(caps) != 1 || string(caps[0].ID()) != "generic_code" {
		t.Errorf("unmatched intent should resolve generic_code, got %v", caps)
	}
}

// TestPipelineRewriteStripsObsoleteContentFromPromptContext proves Phase 3's
// core invariant: under a Redesign intent the StrategyResolver compiles
// PolicyRewrite, which strips the obsolete workspace file contents from the
// LLM prompt context (paths + directive only), and derives the execution mode
// from that policy (one-shot greenfield, not the brownfield repair loop).
func TestPipelineRewriteStripsObsoleteContentFromPromptContext(t *testing.T) {
	root := t.TempDir()
	const obsolete = `<div id="old">OUTDATED LAYOUT CONTENT</div>`
	if err := os.WriteFile(filepath.Join(root, "index.html"),
		[]byte("<!DOCTYPE html><html><body>"+obsolete+"</body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	gen := &scriptedGenerator{resp: []string{fenced("html", "index.html", portfolioPage)}}
	p := mustPipeline(t, WithRoot(root), WithGenerator(gen))

	intentIR := &ir.IntentIR{
		Category:          ir.CategoryRedesign,
		TargetType:        "portfolio",
		PreserveWorkspace: false,
	}
	res, err := p.Run(t.Context(), Request{Intent: "redesign my portfolio website", IntentIR: intentIR})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(res.SystemPrompt, "index.html") {
		t.Error("rewrite context must name the target path")
	}
	if !strings.Contains(res.SystemPrompt, "Do NOT preserve existing code") {
		t.Error("rewrite context must inject the obsolete-content directive")
	}
	if strings.Contains(res.SystemPrompt, "OUTDATED LAYOUT CONTENT") {
		t.Error("obsolete file contents leaked into the LLM prompt context")
	}
	if res.Mode != ModeGreenfield {
		t.Errorf("mode = %s, want greenfield under PolicyRewrite", res.Mode)
	}
	got := readFile(t, filepath.Join(root, "index.html"))
	if strings.Contains(got, "OUTDATED LAYOUT CONTENT") || !strings.Contains(got, "My Portfolio") {
		t.Fatalf("index.html was not rewritten: %q", got)
	}
}

// TestSemanticsFlowEntirelyThroughIntentIR proves OperationSemantics is derived
// strictly from the compiled IntentIR.Category — there is no keyword
// classification left in the pipeline layer.
func TestSemanticsFlowEntirelyThroughIntentIR(t *testing.T) {
	cases := []struct {
		category ir.Category
		want     op.OperationSemantics
	}{
		{ir.CategoryCreate, op.SemanticCreateProject},
		{ir.CategoryRedesign, op.SemanticRewriteProject},
		{ir.CategoryRefactor, op.SemanticRefactor},
		{ir.CategoryFixBug, op.SemanticFixBug},
	}
	for _, c := range cases {
		req := Request{IntentIR: &ir.IntentIR{Category: c.category}}
		if got := semanticsForRequest(req); got != c.want {
			t.Errorf("semanticsForRequest(category=%s) = %s, want %s", c.category, got, c.want)
		}
	}
}

// stubIntentCompiler is an injectable IntentCompiler for pipeline tests. It
// records that it was triggered and returns a fixed compile result.
type stubIntentCompiler struct {
	out    ir.IntentIR
	err    error
	called bool
}

func (s *stubIntentCompiler) Compile(_ context.Context, _ string) (ir.IntentIR, error) {
	s.called = true
	return s.out, s.err
}

// TestPipelineHeadlessCompileTriggered proves a request without a compiled
// IntentIR triggers the wired IntentCompiler, and that the compiled category
// drives the policy and execution mode entirely through IntentIR — even for a
// gibberish prompt with no recognizable keywords.
func TestPipelineHeadlessCompileTriggered(t *testing.T) {
	root := t.TempDir()
	gen := &scriptedGenerator{resp: []string{fenced("html", "index.html", portfolioPage)}}
	comp := &stubIntentCompiler{
		out: ir.IntentIR{Category: ir.CategoryRedesign, TargetType: "portfolio", PreserveWorkspace: false},
	}
	p := mustPipeline(t, WithRoot(root), WithGenerator(gen), WithIntentCompiler(comp))

	res, err := p.Run(t.Context(), Request{Intent: "lam lai cai website nay"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !comp.called {
		t.Fatal("wired IntentCompiler was not triggered for an uncompiled request")
	}
	if res.IntentIR == nil || res.IntentIR.Category != ir.CategoryRedesign {
		t.Fatalf("compiled intent not surfaced, got %+v", res.IntentIR)
	}
	if !strings.Contains(res.SystemPrompt, "Do NOT preserve existing code") {
		t.Error("compiled redesign must compile to PolicyRewrite and strip obsolete context")
	}
	if res.Mode != ModeGreenfield {
		t.Errorf("mode = %s, want greenfield under rewrite policy", res.Mode)
	}
}

// TestPipelineHeadlessCompileFallbackDeterministic proves that when no
// IntentCompiler is wired, or compilation fails, the pipeline falls back to the
// compiler package's deterministic greenfield intent — never to keyword
// guessing in the pipeline layer.
func TestPipelineHeadlessCompileFallbackDeterministic(t *testing.T) {
	t.Run("no compiler wired", func(t *testing.T) {
		root := t.TempDir()
		gen := &scriptedGenerator{resp: []string{fenced("html", "index.html", portfolioPage)}}
		p := mustPipeline(t, WithRoot(root), WithGenerator(gen))
		res, err := p.Run(t.Context(), Request{Intent: "zxb qwv redesign nothing recognizable"})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if res.IntentIR == nil || res.IntentIR.Category != ir.CategoryCreate {
			t.Fatalf("expected deterministic greenfield fallback, got %+v", res.IntentIR)
		}
		if strings.Contains(res.SystemPrompt, "Do NOT preserve existing code") {
			t.Fatal("deterministic fallback must not compile to a rewrite policy")
		}
	})

	t.Run("compilation fails", func(t *testing.T) {
		root := t.TempDir()
		gen := &scriptedGenerator{resp: []string{fenced("html", "index.html", portfolioPage)}}
		failing := &stubIntentCompiler{err: errors.New("model timeout")}
		p := mustPipeline(t, WithRoot(root), WithGenerator(gen), WithIntentCompiler(failing))
		res, err := p.Run(t.Context(), Request{Intent: "build a portfolio website"})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if !failing.called {
			t.Fatal("IntentCompiler must be triggered before the fallback")
		}
		if res.IntentIR == nil || res.IntentIR.Category != ir.CategoryCreate {
			t.Fatalf("expected deterministic greenfield fallback on compile failure, got %+v", res.IntentIR)
		}
	})
}
