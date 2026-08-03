package planner

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// ── fakes ─────────────────────────────────────────────────────────────────────

// fakeGraph is a scripted GraphSource that records which methods the planner
// invoked so tests can assert intent-driven engine selection.
type fakeGraph struct {
	mu          sync.Mutex
	symbolCalls []string
	chainCalls  []string
	archCalls   int
	routeCalls  int

	symbols map[string][]SymbolRef
	chains  map[string]string
	summary string
	routes  string
}

func (f *fakeGraph) ResolveSymbol(_ context.Context, symbol string) ([]SymbolRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.symbolCalls = append(f.symbolCalls, symbol)
	return f.symbols[symbol], nil
}

func (f *fakeGraph) CallChain(_ context.Context, symbol string, _ int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chainCalls = append(f.chainCalls, symbol)
	return f.chains[symbol], nil
}

func (f *fakeGraph) ArchitectureSummary(_ context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.archCalls++
	return f.summary, nil
}

func (f *fakeGraph) Routes(_ context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routeCalls++
	return f.routes, nil
}

// fakeLogs is a scripted LogSource.
type fakeLogs struct {
	mu     sync.Mutex
	calls  int
	bodies []string
}

func (f *fakeLogs) LatestLogs(_ context.Context, limit int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if len(f.bodies) > limit {
		return f.bodies[:limit], nil
	}
	return f.bodies, nil
}

// fakeFiles is a scripted FileSource that records whether the planner asked
// for file context at all.
type fakeFiles struct {
	mu    sync.Mutex
	calls int
	hits  []SearchHit
}

func (f *fakeFiles) Search(_ context.Context, _ string) ([]SearchHit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.hits, nil
}

func (f *fakeFiles) FocusedContext(_ context.Context, _ string, _, _ int) (string, error) {
	return "", nil
}

// newFakePlanner wires the planner to scripted fakes for deterministic tests.
func newFakePlanner(fg *fakeGraph, fl *fakeLogs, ff *fakeFiles, maxTokens int) *Planner {
	opts := []Option{
		WithTokenEstimator(NewTokenEstimator()),
	}
	if fg != nil {
		opts = append(opts, WithGraphSource(fg))
	}
	if fl != nil {
		opts = append(opts, WithLogSource(fl))
	}
	if ff != nil {
		opts = append(opts, WithFileSource(ff))
	}
	opts = append(opts, WithMaxTokens(maxTokens))
	return New(opts...)
}

// ── 1. Intent classification ──────────────────────────────────────────────────

func TestClassifyIntent(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  Intent
	}{
		{"panic message", "panic: nil pointer dereference in main.go", IntentBugFix},
		{"crash with stack", "the server crashes with a stack trace on startup", IntentBugFix},
		{"failing test", "why is my test failing after the last change", IntentBugFix},
		{"regression", "this regression breaks the payment flow", IntentBugFix},

		{"architecture", "explain the architecture of this project", IntentArchitecture},
		{"routes and entry points", "show me the HTTP routes and entry points", IntentArchitecture},
		{"layers", "what are the layers and component boundaries", IntentArchitecture},
		{"system flow", "how does the system handle a new request end to end", IntentArchitecture},

		{"refactor handler", "refactor the handler into smaller functions", IntentRefactor},
		{"split module", "extract the interface and split the module", IntentRefactor},
		{"clean up", "clean up the duplicate logic in this package", IntentRefactor},

		{"explain function", "explain what this function does", IntentExplanation},
		{"what is goroutine", "what is a goroutine in go", IntentExplanation},
		{"why usage", "why would I use a channel here", IntentExplanation},

		{"casual chat", "hello there, how are you", IntentGeneral},
		{"empty", "", IntentGeneral},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyIntent(tc.input); got != tc.want {
				t.Errorf("ClassifyIntent(%q) = %s, want %s", tc.input, got, tc.want)
			}
		})
	}
}

func TestClassifyIntentSignalStrength(t *testing.T) {
	// A single strong bug marker outweighs generic explanation verbs.
	if got := ClassifyIntent("why does this panic occur"); got != IntentBugFix {
		t.Errorf("strong panic signal misclassified: got %s, want BUG_FIX", got)
	}
	// Refactor verbs beat explanation-only phrasing when both are present.
	if got := ClassifyIntent("explain how to refactor this"); got != IntentRefactor {
		t.Errorf("refactor intent lost to explanation: got %s, want REFACTOR", got)
	}
}

// ── 2. Budget allocation ──────────────────────────────────────────────────────

func TestAllocate(t *testing.T) {
	cases := []struct {
		name   string
		intent Intent
		total  int
		want   map[SourceType]int
	}{
		{
			"bug fix split",
			IntentBugFix, 4000,
			map[SourceType]int{SourceLog: 2000, SourceCallTree: 1200, SourceFile: 800},
		},
		{
			"architecture split",
			IntentArchitecture, 4000,
			map[SourceType]int{SourceArch: 2400, SourceGraph: 1600},
		},
		{
			"explanation split",
			IntentExplanation, 4000,
			map[SourceType]int{SourceGraph: 2000, SourceFile: 2000},
		},
		{
			"general split",
			IntentGeneral, 4000,
			map[SourceType]int{SourceGraph: 2000, SourceFile: 2000},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := Allocate(tc.intent, tc.total)
			if b.Total != tc.total {
				t.Errorf("Total = %d, want %d", b.Total, tc.total)
			}
			for src, want := range tc.want {
				if got := b.Source(src); got != want {
					t.Errorf("Source(%s) = %d, want %d", src, got, want)
				}
			}
		})
	}
}

func TestAllocateDefaultCap(t *testing.T) {
	if b := Allocate(IntentGeneral, 0); b.Total != DefaultMaxContextTokens {
		t.Errorf("zero cap did not fall back to default: got %d, want %d", b.Total, DefaultMaxContextTokens)
	}
}

// ── 3. Budget enforcement ─────────────────────────────────────────────────────

func TestPlanBudgetEnforced(t *testing.T) {
	fg := &fakeGraph{
		symbols: map[string][]SymbolRef{
			"Handler": {{Name: "Handler", Kind: "func", QualName: "pkg.Handler", File: "handler.go", Line: 10, Signature: "func Handler(r *Request)"}},
		},
	}
	ff := &fakeFiles{
		hits: []SearchHit{
			{File: "a.go", Line: 1, Content: strings.Repeat("file-a payload ", 200), Score: 0.9},
			{File: "b.go", Line: 2, Content: strings.Repeat("file-b payload ", 200), Score: 0.8},
			{File: "c.go", Line: 3, Content: strings.Repeat("file-c payload ", 200), Score: 0.7},
		},
	}
	// A tight budget: the graph symbol fits, the raw file reads must be
	// dropped by the budget enforcer.
	const budget = 200
	p := newFakePlanner(fg, nil, ff, budget)

	plan, err := p.Plan(context.Background(), "explain the Handler symbol")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	if plan.Intent != IntentExplanation {
		t.Fatalf("intent = %s, want EXPLANATION", plan.Intent)
	}
	if plan.TokenTotal > plan.Budget.Total {
		t.Errorf("TokenTotal %d exceeds budget %d", plan.TokenTotal, plan.Budget.Total)
	}
	if est := NewTokenEstimator().Estimate(plan.Assemble()); est > plan.Budget.Total {
		t.Errorf("assembled context %d tokens exceeds budget %d", est, plan.Budget.Total)
	}
	if !plan.Truncated || plan.Dropped == 0 {
		t.Errorf("expected truncation under tight budget: Truncated=%v Dropped=%d", plan.Truncated, plan.Dropped)
	}

	// Low-priority file reads are dropped in favor of the graph symbol.
	for _, c := range plan.Chunks {
		if c.Source == SourceFile {
			t.Errorf("file chunk survived budget enforcement: %+v", c)
		}
	}
	if len(plan.Chunks) == 0 {
		t.Fatal("no graph symbol chunk kept under budget")
	}
}

func TestPlanBudgetRespectedWithRoom(t *testing.T) {
	fg := &fakeGraph{
		symbols: map[string][]SymbolRef{
			"Handler": {{Name: "Handler", Kind: "func", QualName: "pkg.Handler", File: "handler.go", Line: 10}},
		},
	}
	ff := &fakeFiles{
		hits: []SearchHit{{File: "a.go", Line: 1, Content: "func Handle() { }", Score: 0.9}},
	}
	p := newFakePlanner(fg, nil, ff, DefaultMaxContextTokens)

	plan, err := p.Plan(context.Background(), "explain the Handler symbol")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.TokenTotal > DefaultMaxContextTokens {
		t.Errorf("TokenTotal %d exceeds default budget", plan.TokenTotal)
	}
	if est := NewTokenEstimator().Estimate(plan.Assemble()); est > DefaultMaxContextTokens {
		t.Errorf("assembled %d tokens exceeds default budget", est)
	}
}

func TestPlanTruncatesOversizedLog(t *testing.T) {
	fg := &fakeGraph{
		chains: map[string]string{"main": "main calls loadConfig (config.go:12)"},
	}
	fl := &fakeLogs{
		bodies: []string{strings.Repeat("line of panic stack ", 200)}, // ~4k tokens
	}
	// BUG_FIX log share is 50% of 400 = 200 tokens.
	p := newFakePlanner(fg, fl, nil, 400)

	plan, err := p.Plan(context.Background(), "fix the panic in main")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Intent != IntentBugFix {
		t.Fatalf("intent = %s, want BUG_FIX", plan.Intent)
	}
	if plan.TokenTotal > plan.Budget.Total {
		t.Errorf("TokenTotal %d exceeds budget %d", plan.TokenTotal, plan.Budget.Total)
	}
	// The oversized log is head-truncated to its allocation, never dropped whole.
	logFound := false
	for _, c := range plan.Chunks {
		if c.Source == SourceLog {
			logFound = true
			if c.Tokens > plan.Budget.Source(SourceLog) {
				t.Errorf("log chunk %d tokens exceeds log allocation %d", c.Tokens, plan.Budget.Source(SourceLog))
			}
			if !strings.Contains(c.Content, "[truncated]") {
				t.Errorf("oversized log missing truncation marker")
			}
		}
	}
	if !logFound {
		t.Fatal("no log chunk in BUG_FIX plan")
	}
}

// ── 4. Architecture questions trigger graph queries, not file reads ──────────

func TestArchitectureQuestionUsesGraphNotFiles(t *testing.T) {
	fg := &fakeGraph{
		summary: "Root: /repo\nPackages:\n  - core (internal/core): 12 files",
		routes:  "HTTP routes:\n  GET /health -> HealthHandler",
	}
	ff := &fakeFiles{
		hits: []SearchHit{{File: "big.go", Line: 1, Content: strings.Repeat("verbose implementation ", 500), Score: 0.99}},
	}
	p := newFakePlanner(fg, nil, ff, DefaultMaxContextTokens)

	plan, err := p.Plan(context.Background(), "show me the architecture overview and routes")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Intent != IntentArchitecture {
		t.Fatalf("intent = %s, want ARCHITECTURE_QUESTION", plan.Intent)
	}

	// Lea graph queries were used...
	if fg.archCalls != 1 {
		t.Errorf("ArchitectureSummary called %d times, want 1", fg.archCalls)
	}
	if fg.routeCalls != 1 {
		t.Errorf("Routes called %d times, want 1", fg.routeCalls)
	}
	// ...and the file source was NOT queried at all (strictly ignoring
	// verbose file implementations).
	if ff.calls != 0 {
		t.Errorf("file source queried %d times for an architecture question, want 0", ff.calls)
	}
	// No file chunks may reach the assembled context.
	for _, c := range plan.Chunks {
		if c.Source == SourceFile {
			t.Errorf("file chunk leaked into architecture context: %+v", c)
		}
	}
	assembled := plan.Assemble()
	if !strings.Contains(assembled, "ARCHITECTURE") {
		t.Errorf("assembled context missing architecture section:\n%s", assembled)
	}
}

// ── 5. Bug fix intent uses logs and call chains ───────────────────────────────

func TestBugFixUsesLogsAndCallChains(t *testing.T) {
	fg := &fakeGraph{
		chains: map[string]string{"main": "main\n  loadConfig (config.go:12)\n  handle (server.go:40)"},
	}
	fl := &fakeLogs{
		bodies: []string{"panic: nil pointer dereference\nmain.handle (server.go:42)"},
	}
	p := newFakePlanner(fg, fl, nil, DefaultMaxContextTokens)

	plan, err := p.Plan(context.Background(), "fix the panic in main handle")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Intent != IntentBugFix {
		t.Fatalf("intent = %s, want BUG_FIX", plan.Intent)
	}
	if len(fg.chainCalls) == 0 {
		t.Error("BUG_FIX did not trace any call chains via the Lea graph")
	}
	hasLog, hasChain := false, false
	for _, c := range plan.Chunks {
		if c.Source == SourceLog {
			hasLog = true
		}
		if c.Source == SourceCallTree {
			hasChain = true
		}
	}
	if !hasLog {
		t.Error("BUG_FIX plan missing tool log chunk")
	}
	if !hasChain {
		t.Error("BUG_FIX plan missing call chain chunk")
	}
}

// ── 6. Assembly formatting ────────────────────────────────────────────────────

func TestAssembleHeadersAndOrdering(t *testing.T) {
	fg := &fakeGraph{
		summary: "Root: /repo",
	}
	p := newFakePlanner(fg, nil, nil, DefaultMaxContextTokens)

	plan, err := p.Plan(context.Background(), "describe the architecture")
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	out := plan.Assemble()
	if out == "" {
		t.Fatal("empty assembled context")
	}
	if !strings.HasPrefix(out, "### ARCHITECTURE") {
		t.Errorf("architecture section must be first, got:\n%s", out)
	}
}

// ── 7. PlanAssembled convenience ──────────────────────────────────────────────

func TestPlanAssembled(t *testing.T) {
	fg := &fakeGraph{
		summary: "Root: /repo\nPackages:\n  - core (internal/core)",
	}
	p := newFakePlanner(fg, nil, nil, DefaultMaxContextTokens)

	out, err := p.PlanAssembled(context.Background(), "show the architecture overview")
	if err != nil {
		t.Fatalf("PlanAssembled: %v", err)
	}
	if out == "" {
		t.Fatal("PlanAssembled returned empty context")
	}
	if !strings.Contains(out, "ARCHITECTURE") {
		t.Errorf("PlanAssembled missing architecture section:\n%s", out)
	}
	// The assembled output respects the budget.
	if est := NewTokenEstimator().Estimate(out); est > DefaultMaxContextTokens {
		t.Errorf("PlanAssembled output %d tokens exceeds budget", est)
	}
}

func TestPlanAssembledNoChunks(t *testing.T) {
	// A planner with no sources yields no chunks → empty output, no error.
	p := New(WithMaxTokens(100))
	out, err := p.PlanAssembled(context.Background(), "explain this")
	if err != nil {
		t.Fatalf("PlanAssembled: %v", err)
	}
	if out != "" {
		t.Errorf("expected empty output for source-less planner, got %q", out)
	}
}
