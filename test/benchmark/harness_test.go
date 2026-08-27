package benchmark

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/execution/planner"
)

// block renders one SEARCH/REPLACE block.
func block(search, replace string) string {
	return "<<<<<<< SEARCH\n" + search + "\n=======\n" + replace + "\n>>>>>>>"
}

// ── registry ────────────────────────────────────────────────────────────────

func TestBenchmarkModelsRegistry(t *testing.T) {
	models := BenchmarkModels()
	want := []string{
		"cohere/north-mini-code:free",
		"qwen/qwen-2.5-coder-32b",
		"deepseek/deepseek-r1",
	}
	if len(models) != len(want) {
		t.Fatalf("roster = %d models, want %d", len(models), len(want))
	}
	for i, m := range models {
		if m.ID != want[i] {
			t.Fatalf("model[%d] = %s, want %s", i, m.ID, want[i])
		}
		if m.Provider != "openrouter" || m.Label == "" {
			t.Fatalf("model %s incompletely specified: %+v", m.ID, m)
		}
	}
	SortModels(models)
	for i := 1; i < len(models); i++ {
		if models[i-1].ID > models[i].ID {
			t.Fatal("SortModels left roster unsorted")
		}
	}
}

// ── artifact contract ───────────────────────────────────────────────────────

func TestParseAndApplyBlocks(t *testing.T) {
	content := "before\n<<<<<<< SEARCH\nold\n=======\nnew\n>>>>>>> \nafter\n"
	blocks, err := parseSearchReplaceBlocks(content)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(blocks) != 1 || blocks[0].search != "old" || blocks[0].replace != "new" {
		t.Fatalf("blocks = %+v", blocks)
	}
	got, err := applyBlocks("x\nold\ny", blocks)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got != "x\nnew\ny" {
		t.Fatalf("applied = %q", got)
	}

	if _, err := parseSearchReplaceBlocks("no markers at all"); err == nil {
		t.Fatal("marker-less content must be rejected")
	}
	if _, err := parseSearchReplaceBlocks("<<<<<<< SEARCH\nunterminated"); err == nil {
		t.Fatal("unterminated block must be rejected")
	}
	if _, err := applyBlocks("dup\ndup\n", []patchBlock{{search: "dup", replace: "z"}}); err == nil {
		t.Fatal("non-unique anchor must be rejected")
	}
	if _, err := applyBlocks("same\n", []patchBlock{{search: "same", replace: "same"}}); err == nil {
		t.Fatal("no-op block must be rejected")
	}
}

// ── scripted backends ───────────────────────────────────────────────────────

// garbageResponder always returns non-artifact content.
type garbageResponder struct{ mu sync.Mutex }

func (g *garbageResponder) Respond(_ context.Context, _ Request) (Response, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return Response{Content: "not a SEARCH/REPLACE artifact", InputTokens: 3, OutputTokens: 5}, nil
}

// coordinatedRenamer renames old→new on every matching line of the LIVE
// workspace file inside the requested window — the same anchored flow the
// runtime uses. Pill-less windows answer with the no-op sentinel.
type coordinatedRenamer struct {
	root   string
	target string
	repl   []string // ordered (from, to) pairs
}

func (c *coordinatedRenamer) Respond(_ context.Context, req Request) (Response, error) {
	start, end, ok := windowBounds(req.Prompt)
	if !ok {
		return Response{}, fmt.Errorf("no change window in prompt")
	}
	data, err := os.ReadFile(filepath.Join(c.root, c.target))
	if err != nil {
		return Response{}, fmt.Errorf("read workspace: %w", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	r := strings.NewReplacer(c.repl...)
	var content strings.Builder
	for i := start - 1; i < end && i < len(lines); i++ {
		if r.Replace(lines[i]) != lines[i] {
			content.WriteString(block(lines[i], r.Replace(lines[i])))
			content.WriteByte('\n')
		}
	}
	if content.Len() == 0 {
		return Response{Content: "NO_CHANGES_REQUIRED", InputTokens: 10, OutputTokens: 2}, nil
	}
	return Response{
		Content:      strings.TrimSuffix(content.String(), "\n"),
		InputTokens:  15,
		OutputTokens: 15,
	}, nil
}

// windowBounds extracts "Change window: lines X–Y." from a sub-task prompt.
func windowBounds(prompt string) (int, int, bool) {
	const marker = "Change window: lines "
	idx := strings.Index(prompt, marker)
	if idx < 0 {
		return 0, 0, false
	}
	rest := prompt[idx+len(marker):]
	dash := strings.Index(rest, "–")
	if dash < 0 {
		return 0, 0, false
	}
	from, err := strconv.Atoi(strings.TrimSpace(rest[:dash]))
	if err != nil {
		return 0, 0, false
	}
	toPart := rest[dash+len("–"):]
	endIdx := strings.IndexAny(toPart, ".\n")
	if endIdx < 0 {
		return 0, 0, false
	}
	to, err := strconv.Atoi(strings.TrimSpace(toPart[:endIdx]))
	if err != nil {
		return 0, 0, false
	}
	return from, to, true
}

// ── end-to-end harness sweeps ───────────────────────────────────────────────

func TestHarnessCountsRetriesOnGarbageArtifacts(t *testing.T) {
	h := NewHarness()
	m := BenchmarkModels()[0]
	h.Register(m, &garbageResponder{})

	report, err := h.Run(context.Background(), []Scenario{htmlRenameComponent()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	res := report.Results[0]
	if res.Metrics.SubTasks < 1 {
		t.Fatal("planner produced no sub-tasks")
	}
	// The first sub-task exhausts maxArtifactAttempts and aborts the scenario:
	// exactly 3 invocations, of which 2 were contract retries.
	if res.Metrics.Invocations != maxArtifactAttempts {
		t.Fatalf("invocations = %d, want %d", res.Metrics.Invocations, maxArtifactAttempts)
	}
	if res.Metrics.Retries != maxArtifactAttempts-1 {
		t.Fatalf("retries = %d, want %d", res.Metrics.Retries, maxArtifactAttempts-1)
	}
	if res.Err == "" {
		t.Fatal("exhausted attempts must surface an error")
	}
	if res.Accuracy >= 0 {
		t.Fatal("an unfinished scenario has no accuracy")
	}
	if res.Metrics.InputTokens != maxArtifactAttempts*3 || res.Metrics.OutputTokens != maxArtifactAttempts*5 {
		t.Fatalf("usage not accumulated per invocation: %+v", res.Metrics)
	}
}

// TestHarnessCoordinatedRename is the positive-control sweep: a model
// that renames every pill line (and no-ops elsewhere) scores accuracy 1,
// passes the global verifier, and burns zero retries.
func TestHarnessCoordinatedRename(t *testing.T) {
	sc := htmlRenameComponent()
	root := t.TempDir()
	h := NewHarness()
	h.Register(BenchmarkModels()[1], &coordinatedRenamer{
		root:   root,
		target: sc.Target,
		repl:   []string{".pill", ".chip", `class="pill"`, `class="chip"`},
	})

	report, err := h.Run(context.Background(), []Scenario{sc}, WithWorkRoot(root))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	res := report.Results[0]
	if res.Err != "" {
		t.Fatalf("coordinated rename failed: %s", res.Err)
	}
	if res.Accuracy != 1 {
		t.Fatalf("accuracy = %.2f, want 1", res.Accuracy)
	}
	if !res.VerifierPass {
		t.Fatal("global verifier must pass a coordinated rename")
	}
	if res.Metrics.Retries != 0 || res.Metrics.Applied != res.Metrics.SubTasks {
		t.Fatalf("clean-run metrics wrong: %+v", res.Metrics)
	}
	final, err := os.ReadFile(filepath.Join(root, sc.Target))
	if err != nil {
		t.Fatalf("read final: %v", err)
	}
	if strings.Contains(string(final), "pill") || !strings.Contains(string(final), `.chip {`) {
		t.Fatalf("rename not fully applied:\n%s", final)
	}
}

// TestHarnessHalfRenameFailsVerifier proves the verifier integration earns
// its place in the benchmark: a model that renames only SOME occurrences
// loses accuracy AND fails the post-DAG audit when it orphans references.
type halfRenamer struct {
	root   string
	target string
	calls  int
	mu     sync.Mutex
}

func (hlf *halfRenamer) Respond(_ context.Context, req Request) (Response, error) {
	start, end, ok := windowBounds(req.Prompt)
	if !ok {
		return Response{}, fmt.Errorf("no change window")
	}
	data, err := os.ReadFile(filepath.Join(hlf.root, hlf.target))
	if err != nil {
		return Response{}, fmt.Errorf("read workspace: %w", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	hlf.mu.Lock()
	hlf.calls++
	mode := hlf.calls
	hlf.mu.Unlock()
	for i := start - 1; i < end && i < len(lines); i++ {
		if !strings.Contains(lines[i], "pill") {
			continue
		}
		var repl string
		if mode == 1 {
			// First unit: rename correctly...
			repl = strings.NewReplacer(".pill", ".chip", `class="pill"`, `class="chip"`).Replace(lines[i])
		} else {
			// ...later units: BREAK the refactor by renaming only the CSS
			// selector side, orphaning the class definitions.
			repl = strings.NewReplacer(".pill", ".tag").Replace(lines[i])
		}
		if repl == lines[i] {
			continue // nothing this backend can change on that line
		}
		return Response{Content: block(lines[i], repl), InputTokens: 15, OutputTokens: 15}, nil
	}
	return Response{Content: "NO_CHANGES_REQUIRED", InputTokens: 10, OutputTokens: 2}, nil
}

func TestHarnessHalfRenameFailsVerifier(t *testing.T) {
	sc := htmlRenameComponent()
	root := t.TempDir()
	h := NewHarness()
	h.Register(BenchmarkModels()[2], &halfRenamer{root: root, target: sc.Target})

	report, err := h.Run(context.Background(), []Scenario{sc}, WithWorkRoot(root))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	res := report.Results[0]
	if res.VerifierPass {
		t.Fatal("a half rename that orphans definitions must fail the global audit")
	}
	if !strings.Contains(res.Err, "orphaned_definition") && !strings.Contains(res.Err, "dangling_reference") {
		t.Fatalf("audit evidence missing from error: %q", res.Err)
	}
	if res.Accuracy >= 1 {
		t.Fatalf("partial accuracy = %.2f, must be < 1", res.Accuracy)
	}
}

// ── report aggregation ──────────────────────────────────────────────────────

func TestReportSummariesAndRender(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	report := &Report{
		GeneratedAt: now,
		Results: []ScenarioResult{
			{Model: "a", Scenario: "s1", Accuracy: 1, VerifierPass: true,
				Metrics: Metrics{Invocations: 4, Retries: 1, InputTokens: 100, OutputTokens: 50, Latency: 2 * time.Second}},
			{Model: "a", Scenario: "s2", Accuracy: 0.5,
				Metrics: Metrics{Invocations: 6, Retries: 2, InputTokens: 200, OutputTokens: 100, Latency: 4 * time.Second}},
		},
	}
	sums := report.Summaries()
	if len(sums) != 1 {
		t.Fatalf("summaries = %d, want 1", len(sums))
	}
	s := sums[0]
	if s.Runs != 2 || s.Completed != 2 || s.VerifierPass != 1 {
		t.Fatalf("counts wrong: %+v", s)
	}
	if diff := s.MeanAccuracy - 0.75; diff < -1e-9 || diff > 1e-9 {
		t.Fatalf("mean accuracy = %f, want 0.75", s.MeanAccuracy)
	}
	if diff := s.RetryRate - 0.3; diff < -1e-9 || diff > 1e-9 {
		t.Fatalf("retry rate = %f, want 0.3", s.RetryRate)
	}
	if s.TotalTokens != 450 || s.InputTokens != 300 || s.OutputTokens != 150 {
		t.Fatalf("token totals wrong: %+v", s)
	}
	if s.MeanLatency != 3*time.Second {
		t.Fatalf("mean latency = %s, want 3s", s.MeanLatency)
	}
	rendered := report.Render()
	if !strings.Contains(rendered, "MODEL CAPABILITY & COST BENCHMARK") ||
		!strings.Contains(rendered, "75.00%") {
		t.Fatalf("render missing headline rows:\n%s", rendered)
	}
	blob, err := report.JSON()
	if err != nil || !strings.Contains(string(blob), `"mean_accuracy": 0.75`) {
		t.Fatalf("json = %s, err = %v", blob, err)
	}
}

func TestExpectationsScore(t *testing.T) {
	e := Expectations{
		MustContain:    []string{"keep"},
		MustNotContain: []string{"gone"},
	}
	if got := e.Score("has keep only"); got != 1 {
		t.Fatalf("score = %f, want 1", got)
	}
	if got := e.Score("gone"); got != 0 {
		t.Fatalf("score = %f, want 0", got)
	}
	if got := e.Score("neither"); got != 0.5 {
		t.Fatalf("score = %f, want 0.5", got)
	}
	if got := (Expectations{}).Score(""); got != 1 {
		t.Fatalf("empty oracle = %f, want 1", got)
	}
}

// TestStandardSuiteDecomposesIntoMultipleSubTasks guards the suite's reason
// to exist: every standard scenario must stage a REAL DAG (≥2 sub-tasks)
// under its tight output ceiling, not a degenerate single-shot plan.
func TestStandardSuiteDecomposesIntoMultipleSubTasks(t *testing.T) {
	for _, sc := range StandardScenarios() {
		dag, err := planner.DecomposeTarget(sc.Objective, sc.Target, []byte(sc.Source),
			"bench", sc.MaxOutputTokens)
		if err != nil {
			t.Fatalf("%s: DecomposeTarget: %v", sc.Name, err)
		}
		if len(dag.SubTasks) < 2 {
			t.Errorf("%s: staged %d sub-task(s), want >= 2", sc.Name, len(dag.SubTasks))
		}
		if err := dag.Validate(); err != nil {
			t.Errorf("%s: invalid DAG: %v", sc.Name, err)
		}
	}
}

// ── live mode gate ──────────────────────────────────────────────────────────

func TestLiveRespondersRequireCredentials(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	resp, reason := LiveRespondersFromEnv(BenchmarkModels())
	if resp != nil || !strings.Contains(reason, "OPENROUTER_API_KEY") {
		t.Fatalf("live responders built without credentials: %v (%s)", resp, reason)
	}
}

// TestLiveSweepManual executes the real sweep against OpenRouter. It is
// skipped unless BOTH IZEN_BENCH_LIVE=1 and OPENROUTER_API_KEY are set, so
// CI never spends tokens.
//
//	Run: IZEN_BENCH_LIVE=1 OPENROUTER_API_KEY=... go test ./test/benchmark/ -run TestLiveSweepManual -v
func TestLiveSweepManual(t *testing.T) {
	if os.Getenv("IZEN_BENCH_LIVE") != "1" || os.Getenv("OPENROUTER_API_KEY") == "" {
		t.Skip("live benchmark disabled: set IZEN_BENCH_LIVE=1 and OPENROUTER_API_KEY to run")
	}
	models := BenchmarkModels()
	responders, reason := LiveRespondersFromEnv(models)
	if responders == nil {
		t.Skipf("live mode unavailable: %s", reason)
	}
	h := NewHarness()
	for _, r := range responders {
		h.Register(r.Model, r.Responder)
	}
	report, err := h.Run(context.Background(), StandardScenarios())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Log("\n" + report.Render())
	blob, err := report.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	out := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(out, blob, 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
}
