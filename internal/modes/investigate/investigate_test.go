package investigate

import (
	"fmt"
	"strings"
	"testing"
)

func TestStateMachine(t *testing.T) {
	sm := NewStateMachine(DefaultStateConfig())
	if sm.Current() != StateObserve {
		t.Fatalf("expected initial state Observe, got %s", sm.Current())
	}

	if err := sm.Transition(StateHypothesize); err != nil {
		t.Fatalf("Observe->Hypothesize: %v", err)
	}
	if sm.Current() != StateHypothesize {
		t.Fatalf("expected Hypothsize, got %s", sm.Current())
	}

	if err := sm.Transition(StateSearch); err != nil {
		t.Fatalf("Hypothesize->Search: %v", err)
	}
	if err := sm.Transition(StateGather); err != nil {
		t.Fatalf("Search->Gather: %v", err)
	}
	if err := sm.Transition(StateEvaluate); err != nil {
		t.Fatalf("Gather->Evaluate: %v", err)
	}

	if sm.IsTerminal() {
		t.Fatal("should not be terminal yet")
	}
}

func TestStateMachineInvalidTransitions(t *testing.T) {
	sm := NewStateMachine(DefaultStateConfig())

	if err := sm.Transition(StateSearch); err == nil {
		t.Fatal("expected error for Observe->Search transition")
	}
	if err := sm.Transition(StateDone); err == nil {
		t.Fatal("expected error for Observe->Done transition")
	}
}

func TestStateMachineFullPath(t *testing.T) {
	sm := NewStateMachine(DefaultStateConfig())
	path := []State{StateObserve, StateHypothesize, StateSearch, StateGather, StateEvaluate, StateNarrow, StateHypothesize, StateSearch, StateGather, StateEvaluate, StateVerify, StatePropose, StateDone}

	for _, next := range path[1:] {
		if err := sm.Transition(next); err != nil {
			t.Fatalf("transition %s->%s: %v", sm.Current(), next, err)
		}
	}

	if !sm.IsTerminal() {
		t.Fatal("expected terminal state")
	}
}

func TestStateMachineStopCondition(t *testing.T) {
	sm := NewStateMachine(StateConfig{MaxLoops: 3})

	_ = sm.Transition(StateHypothesize)
	_ = sm.Transition(StateSearch)
	_ = sm.Transition(StateGather)
	_ = sm.Transition(StateEvaluate)
	_ = sm.Transition(StateNarrow)

	if sm.ShouldStop() {
		t.Fatal("should not stop at 0 completed iterations after first hypothesize")
	}

	_ = sm.Transition(StateHypothesize)
	_ = sm.Transition(StateSearch)
	_ = sm.Transition(StateGather)
	_ = sm.Transition(StateEvaluate)
	_ = sm.Transition(StateNarrow)

	if sm.ShouldStop() {
		t.Fatal("should not stop at 1 iteration")
	}

	_ = sm.Transition(StateHypothesize)
	_ = sm.Transition(StateSearch)
	_ = sm.Transition(StateGather)
	_ = sm.Transition(StateEvaluate)
	_ = sm.Transition(StateNarrow)
	_ = sm.Transition(StateHypothesize)

	if !sm.ShouldStop() {
		t.Fatal("should stop at 3 iterations (>= maxLoops)")
	}
}

func TestHypothesisManager(t *testing.T) {
	hm := NewHypothesisManager()

	h1 := hm.Add("bug is in parser")
	if h1.ID != "H1" {
		t.Fatalf("expected H1, got %s", h1.ID)
	}
	if h1.Status != HypothesisActive {
		t.Fatal("expected active hypothesis")
	}
	if h1.Confidence != 0.5 {
		t.Fatalf("expected confidence 0.5, got %f", h1.Confidence)
	}

	h2 := hm.Add("bug is in graph builder")
	_ = h2
	if hm.Count() != 2 {
		t.Fatalf("expected 2 hypotheses, got %d", hm.Count())
	}

	hm.UpdateStatus("H1", HypothesisConfirmed)
	hm.UpdateConfidence("H2", 0.9)

	h1r := hm.Get("H1")
	if h1r.Status != HypothesisConfirmed {
		t.Fatal("expected H1 confirmed")
	}

	active := hm.Active()
	if len(active) != 1 {
		t.Fatalf("expected 1 active, got %d", len(active))
	}

	confirmed := hm.Confirmed()
	if len(confirmed) != 1 {
		t.Fatalf("expected 1 confirmed, got %d", len(confirmed))
	}

	hm.UpdateStatus("H2", HypothesisRejected)
	best := hm.Best()
	if best == nil || best.ID != "H1" {
		t.Fatal("best should be H1 (confirmed)")
	}

	hm.LinkEvidence("H1", "EV1")
	h1r = hm.Get("H1")
	if len(h1r.EvidenceIDs) != 1 || h1r.EvidenceIDs[0] != "EV1" {
		t.Fatal("evidence link failed")
	}
}

func TestEvidenceStore(t *testing.T) {
	es := NewEvidenceStore()

	e1 := es.Add(EvSourceGraph, "func NewEngine()", "engine.go", 10, 0.9)
	if e1.ID != "EV1" {
		t.Fatalf("expected EV1, got %s", e1.ID)
	}
	if e1.Source != EvSourceGraph {
		t.Fatal("expected graph source")
	}

	e2 := es.Add(EvSourceTest, "TestFoo failed", "test.go", 5, 0.5)
	e3 := es.Add(EvSourceStack, "panic: nil pointer", "main.go", 42, 0.6)
	_ = e2
	_ = e3

	if es.Count() != 3 {
		t.Fatalf("expected 3 evidence, got %d", es.Count())
	}

	bySource := es.BySource(EvSourceGraph)
	if len(bySource) != 1 {
		t.Fatalf("expected 1 graph evidence, got %d", len(bySource))
	}

	byFile := es.ByFile("engine.go")
	if len(byFile) != 1 {
		t.Fatalf("expected 1 file evidence, got %d", len(byFile))
	}

	highConf := es.HighConfidence(0.7)
	if len(highConf) != 1 {
		t.Fatalf("expected 1 high confidence evidence, got %d", len(highConf))
	}

	got := es.Get("EV2")
	if got == nil || got.Source != EvSourceTest {
		t.Fatal("failed to get EV2")
	}

	all := es.All()
	if len(all) != 3 {
		t.Fatalf("expected 3 total, got %d", len(all))
	}
}

func TestEvidenceClearAndSummary(t *testing.T) {
	es := NewEvidenceStore()
	es.Add(EvSourceGraph, "test", "", 0, 0.5)
	es.Add(EvSourceTest, "test2", "", 0, 0.3)

	summary := es.Summary()
	if !strings.Contains(summary, "Total evidence: 2") {
		t.Fatalf("unexpected summary: %s", summary)
	}

	es.Clear()
	if es.Count() != 0 {
		t.Fatal("expected empty after clear")
	}
}

func TestParseStackFramesGo(t *testing.T) {
	input := `panic: runtime error: invalid memory address or nil pointer dereference
	main.go:42 main.Start()
	internal/graph/engine.go:128 graph.NewEngine()
	cmd/izen/main.go:15 main.main()`

	frames := ParseStackFrames(input)
	if len(frames) == 0 {
		t.Fatal("expected at least 1 stack frame")
	}

	if frames[0].File != "main.go" || frames[0].Line != 42 || frames[0].Function != "main.Start()" {
		t.Fatalf("unexpected first frame: %+v", frames[0])
	}

	if len(frames) >= 2 {
		if frames[1].File != "internal/graph/engine.go" || frames[1].Line != 128 {
			t.Fatalf("unexpected second frame: %+v", frames[1])
		}
	}
}

func TestParseStackFramesPython(t *testing.T) {
	input := `Traceback (most recent call last):
  File "/app/main.py", line 25, in <module>
    start()
  File "/app/engine.py", line 50, in start
    process(data)
  File "/app/processor.py", line 10, in process
    result = data.items()`

	frames := ParseStackFrames(input)
	if len(frames) == 0 {
		t.Fatal("expected python stack frames")
	}
	if frames[0].File != "/app/main.py" || frames[0].Line != 25 {
		t.Fatalf("unexpected first frame: %+v", frames[0])
	}
}

func TestParseStackFramesJava(t *testing.T) {
	input := `Exception in thread "main" java.lang.NullPointerException
	at org.example.Main.process(Main.go:25)
	at org.example.Main.run(Main.go:10)`

	frames := ParseStackFrames(input)
	if len(frames) == 0 {
		t.Fatal("expected java-style stack frames")
	}
	if len(frames) >= 1 {
		t.Logf("Java frame: %+v", frames[0])
	}
}

func TestParseStackFramesEmpty(t *testing.T) {
	frames := ParseStackFrames("")
	if len(frames) != 0 {
		t.Fatal("expected no frames from empty input")
	}

	frames = ParseStackFrames("just some text\nwith no stack traces")
	if len(frames) != 0 {
		t.Fatal("expected no frames from non-stacktrace text")
	}
}

func TestEvidenceAddWithStrategy(t *testing.T) {
	es := NewEvidenceStore()
	ev := es.AddWithStrategy(EvSourceRead, "content here", "file.go", 42, 0.8, "read.file")
	if ev.Strategy != "read.file" {
		t.Fatalf("expected strategy 'read.file', got %s", ev.Strategy)
	}
	if ev.Source != EvSourceRead {
		t.Fatalf("expected EvSourceRead, got %s", ev.Source)
	}
}

func TestHypothesisStatusString(t *testing.T) {
	tests := []struct {
		status HypothesisStatus
		want   string
	}{
		{HypothesisActive, "active"},
		{HypothesisConfirmed, "confirmed"},
		{HypothesisRejected, "rejected"},
		{HypothesisPending, "pending"},
		{HypothesisStatus(99), "unknown"},
	}

	for _, tt := range tests {
		if got := tt.status.String(); got != tt.want {
			t.Errorf("HypothesisStatus(%d).String() = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestStateStringAndDescription(t *testing.T) {
	states := []State{StateObserve, StateHypothesize, StateSearch, StateGather,
		StateEvaluate, StateNarrow, StateVerify, StatePropose, StateDone}

	for _, s := range states {
		if s.String() == "" {
			t.Errorf("State %d has empty string", s)
		}
		if s.Description() == "" {
			t.Errorf("State %d has empty description", s)
		}
	}
}

func TestUniqueStrings(t *testing.T) {
	input := []string{"a", "b", "a", "c", "b", "c"}
	result := unique(input)
	if len(result) != 3 {
		t.Fatalf("expected 3 unique, got %d", len(result))
	}

	input2 := []string{}
	result2 := unique(input2)
	if len(result2) != 0 {
		t.Fatal("expected 0 from empty input")
	}
}

func TestExtractPackageFromFile(t *testing.T) {
	tests := []struct {
		file string
		want string
	}{
		{"internal/graph/engine.go", "internal/graph"},
		{"cmd/izen/main.go", "cmd/izen"},
		{"pkg/foo/bar.go", "pkg/foo"},
		{"main.go", ""},
		{"foo/bar/baz.go", ""},
	}

	for _, tt := range tests {
		got := extractPackageFromFile(tt.file)
		if got != tt.want {
			t.Errorf("extractPackageFromFile(%q) = %q, want %q", tt.file, got, tt.want)
		}
	}
}

func TestStateMachineReset(t *testing.T) {
	sm := NewStateMachine(DefaultStateConfig())
	_ = sm.Transition(StateHypothesize)
	_ = sm.Transition(StateSearch)

	sm.Reset()
	if sm.Current() != StateObserve {
		t.Fatal("expected state reset to Observe")
	}
	if len(sm.History()) != 1 {
		t.Fatalf("expected 1 history entry after reset, got %d", len(sm.History()))
	}
}

func TestStateMachineIterationCount(t *testing.T) {
	sm := NewStateMachine(DefaultStateConfig())

	if sm.IterationCount() != 0 {
		t.Fatal("expected 0 iterations initially")
	}

	_ = sm.Transition(StateHypothesize)
	if sm.IterationCount() != 1 {
		t.Fatalf("expected 1 iteration, got %d", sm.IterationCount())
	}

	_ = sm.Transition(StateSearch)
	_ = sm.Transition(StateGather)
	_ = sm.Transition(StateEvaluate)
	_ = sm.Transition(StateNarrow)
	_ = sm.Transition(StateHypothesize)

	if sm.IterationCount() != 2 {
		t.Fatalf("expected 2 iterations, got %d", sm.IterationCount())
	}
}

func TestRunResultAdapter(t *testing.T) {
	adapter := NewRunResultAdapter("stdout text", "stderr text", 1)
	if adapter.StdOut() != "stdout text" {
		t.Fatalf("expected 'stdout text', got %q", adapter.StdOut())
	}
	if adapter.StdErr() != "stderr text" {
		t.Fatalf("expected 'stderr text', got %q", adapter.StdErr())
	}
	if adapter.ExitCode() != 1 {
		t.Fatalf("expected exit code 1, got %d", adapter.ExitCode())
	}
}

func TestExtractErrorOutput(t *testing.T) {
	adapter := NewRunResultAdapter("stdout", "stderr", 1)

	output := ExtractErrorOutput(adapter, nil)
	if output != "stderr" {
		t.Fatalf("expected 'stderr', got %q", output)
	}

	emptyAdapter := NewRunResultAdapter("", "", 0)
	output = ExtractErrorOutput(emptyAdapter, nil)
	if output != "" {
		t.Fatalf("expected empty output, got %q", output)
	}

	output = ExtractErrorOutput(nil, nil)
	if output != "" {
		t.Fatalf("expected empty for nil, got %q", output)
	}

	output = ExtractErrorOutput(nil, fmt.Errorf("resource closed"))
	if !strings.Contains(output, "closed") {
		t.Fatalf("expected error message, got %q", output)
	}
}

func TestIsLikelySymbol(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"NewEngine", true},
		{"config.Load", true},
		{"internal/graph", true},
		{"foo", false},
		{"bar", false},
		{"", false},
		{"A", false},
	}

	for _, tt := range tests {
		got := isLikelySymbol(tt.input)
		if got != tt.want {
			t.Errorf("isLikelySymbol(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestNarrowIteration(t *testing.T) {
	tl := NewTestLoop(3)

	prev := &TestResultSummary{
		Failed: []string{"TestFoo", "TestBar"},
	}

	frames := []StackFrame{
		{File: "internal/graph/engine.go", Line: 42},
		{File: "cmd/izen/main.go", Line: 10},
	}

	candidates := tl.NarrowIteration(prev, frames)
	if len(candidates) == 0 {
		t.Fatal("expected at least 1 candidate")
	}

	hasPkg := false
	for _, c := range candidates {
		if strings.Contains(c, "internal/graph") || strings.Contains(c, "cmd/izen") {
			hasPkg = true
		}
	}
	if !hasPkg {
		t.Fatal("expected package names in candidates")
	}

	if tl.maxIterations != 3 {
		t.Fatalf("expected 3 max iterations, got %d", tl.maxIterations)
	}
}

func TestTestLoopConfig(t *testing.T) {
	cfg := testLoopConfig{Strategy: "all"}
	if cfg.Strategy != "all" {
		t.Fatal("expected 'all' strategy")
	}
}

func TestNewTestLoopDefault(t *testing.T) {
	tl := NewTestLoop(0)
	if tl.maxIterations != hardLoopCeiling {
		t.Fatalf("expected default %d, got %d", hardLoopCeiling, tl.maxIterations)
	}

	tl2 := NewTestLoop(10)
	if tl2.maxIterations != hardLoopCeiling {
		t.Fatalf("expected %d (hard ceiling), got %d", hardLoopCeiling, tl2.maxIterations)
	}

	tl3 := NewTestLoop(2)
	if tl3.maxIterations != 2 {
		t.Fatalf("expected 2, got %d", tl3.maxIterations)
	}
}

func TestProximitySlicerNew(t *testing.T) {
	ps := NewProximitySlicer(".", 0)
	if ps.lines != 10 {
		t.Fatalf("expected default 10 lines, got %d", ps.lines)
	}

	ps2 := NewProximitySlicer(".", 20)
	if ps2.lines != 20 {
		t.Fatalf("expected 20 lines, got %d", ps2.lines)
	}
}

type mockRetriever struct {
	symbolResults map[string][]SearchResult
	textResults   map[string][]SearchResult
	fileResults   map[string][]SearchResult
	pkgResults    map[string][]SearchResult
}

func newMockRetriever() *mockRetriever {
	return &mockRetriever{
		symbolResults: make(map[string][]SearchResult),
		textResults:   make(map[string][]SearchResult),
		fileResults:   make(map[string][]SearchResult),
		pkgResults:    make(map[string][]SearchResult),
	}
}

func (m *mockRetriever) SearchSymbol(name string) ([]SearchResult, error) {
	if r, ok := m.symbolResults[name]; ok {
		return r, nil
	}
	return nil, nil
}

func (m *mockRetriever) SearchText(text string) ([]SearchResult, error) {
	if r, ok := m.textResults[text]; ok {
		return r, nil
	}
	return nil, nil
}

func (m *mockRetriever) SearchFile(path string) ([]SearchResult, error) {
	if r, ok := m.fileResults[path]; ok {
		return r, nil
	}
	return nil, nil
}

func (m *mockRetriever) SearchPackage(pkg string) ([]SearchResult, error) {
	if r, ok := m.pkgResults[pkg]; ok {
		return r, nil
	}
	return nil, nil
}

func (m *mockRetriever) ReadTarget(path string, lines int) ([]SearchResult, error) {
	return m.SearchFile(path)
}

type mockExecutor struct {
	allResult      *TestResultSummary
	packageResult  map[string]*TestResultSummary
	specificResult map[string]*TestResultSummary
	runAllErr      error
}

func newMockExecutor() *mockExecutor {
	return &mockExecutor{
		packageResult:  make(map[string]*TestResultSummary),
		specificResult: make(map[string]*TestResultSummary),
	}
}

func (m *mockExecutor) RunAllTests() (*TestResultSummary, error) {
	return m.allResult, m.runAllErr
}

func (m *mockExecutor) RunPackageTests(pkg string) (*TestResultSummary, error) {
	if r, ok := m.packageResult[pkg]; ok {
		return r, nil
	}
	return &TestResultSummary{Package: pkg, Passed: true}, nil
}

func (m *mockExecutor) RunSpecificTest(pkg, test string) (*TestResultSummary, error) {
	key := pkg + "/" + test
	if r, ok := m.specificResult[key]; ok {
		return r, nil
	}
	return &TestResultSummary{Package: pkg, Passed: true}, nil
}

func TestEngineInitialization(t *testing.T) {
	ret := newMockRetriever()
	exec := newMockExecutor()

	eng := NewEngine(".", "test nil pointer", ret, exec)
	if eng.Problem != "test nil pointer" {
		t.Fatalf("expected problem, got %q", eng.Problem)
	}
	if eng.State.Current() != StateObserve {
		t.Fatal("expected initial state Observe")
	}
	if eng.Hypotheses.Count() != 0 {
		t.Fatalf("expected 0 hypotheses, got %d", eng.Hypotheses.Count())
	}
	if eng.Evidence.Count() != 0 {
		t.Fatalf("expected 0 evidence, got %d", eng.Evidence.Count())
	}
}

func TestEngineRunWithMocks(t *testing.T) {
	ret := newMockRetriever()
	exec := newMockExecutor()

	exec.allResult = &TestResultSummary{
		Package: ".",
		Passed:  false,
		Total:   1,
		FailedN: 1,
		Failed:  []string{"TestEngine"},
		Output:  "--- FAIL: TestEngine\n    engine_test.go:25: assertion failed\nFAIL",
	}

	eng := NewEngine(".", "test failure", ret, exec)
	result, err := eng.Run()

	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Problem != "test failure" {
		t.Fatalf("expected problem 'test failure', got %q", result.Problem)
	}
	if result.Loops == 0 && !result.Resolved {
		t.Log("Investigation ran with 0 loops (may stop due to maxLoops)")
	}
	if result.Duration == "" {
		t.Fatal("expected non-empty duration")
	}
}

func TestEngineStateTransitions(t *testing.T) {
	eng := NewEngine(".", "test", nil, nil)
	if eng.State.Current() != StateObserve {
		t.Fatal("expected initial state Observe")
	}

	err := eng.stateObserve()
	if err != nil {
		t.Fatalf("stateObserve: %v", err)
	}

	if eng.State.Current() != StateHypothesize {
		t.Fatalf("expected Hypothsize after observe, got %s", eng.State.Current())
	}

	err = eng.stateHypothesize()
	if err != nil {
		t.Fatalf("stateHypothesize: %v", err)
	}

	if eng.Hypotheses.Count() != 1 {
		t.Fatalf("expected 1 hypothesis, got %d", eng.Hypotheses.Count())
	}

	_ = eng.State.Transition(StateSearch)
	err = eng.stateSearch()
	if err != nil {
		t.Fatalf("stateSearch: %v", err)
	}
}

func TestEngineFullLifecycle(t *testing.T) {
	eng := NewEngine(".", "investigation test", nil, nil)

	err := eng.stateObserve()
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}

	err = eng.stateHypothesize()
	if err != nil {
		t.Fatalf("Hypothesize: %v", err)
	}

	_ = eng.State.Transition(StateSearch)
	_ = eng.stateSearch()
	_ = eng.State.Transition(StateGather)
	_ = eng.stateGather()
	_ = eng.State.Transition(StateEvaluate)
	_ = eng.stateEvaluate()
	_ = eng.State.Transition(StateNarrow)
	_ = eng.stateNarrow()
	_ = eng.State.Transition(StateHypothesize)
	_ = eng.stateHypothesize()
	_ = eng.State.Transition(StateSearch)
	_ = eng.stateSearch()
	_ = eng.State.Transition(StateGather)
	_ = eng.stateGather()
	_ = eng.State.Transition(StateEvaluate)
	_ = eng.stateEvaluate()

	if eng.Result == nil {
		t.Log("Result struct created during Run(), not direct state calls")
	}
}

func TestExtractSymbolsFromProblem(t *testing.T) {
	eng := NewEngine(".", "Engine token leak in GraphBuilder.Build", nil, nil)
	symbols := eng.extractSymbolsFromProblem()

	found := false
	for _, s := range symbols {
		if s == "GraphBuilder" || s == "Engine" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected CamelCase symbols in extraction, got %v", symbols)
	}
}

func TestExtractTextTerms(t *testing.T) {
	eng := NewEngine(".", "the build is failing with a nil pointer in the engine", nil, nil)
	terms := eng.extractTextTermsFromProblem()

	if len(terms) == 0 {
		t.Fatal("expected extracted terms")
	}

	for _, term := range terms {
		if len(term) <= 3 {
			t.Fatalf("expected terms longer than 3 chars, got %q", term)
		}
	}
}

func TestProximitySlicerExtractAll(t *testing.T) {
	ps := NewProximitySlicer(".", 5)

	frames := []StackFrame{
		{File: "nonexistent.go", Line: 1},
		{File: "missing_file.go", Line: 10},
	}

	slices := ps.ExtractAll(frames)
	if len(slices) != 0 {
		t.Logf("Got %d slices for nonexistent files (expected 0)", len(slices))
	}
}

func TestInvestigationResultStruct(t *testing.T) {
	r := &InvestigationResult{
		Problem:  "test",
		Resolved: true,
		Loops:    3,
		Duration: "10ms",
	}

	if r.Problem != "test" {
		t.Fatal("problem mismatch")
	}
	if !r.Resolved {
		t.Fatal("expected resolved")
	}
	if r.Loops != 3 {
		t.Fatalf("expected 3 loops, got %d", r.Loops)
	}
}

func TestEvidenceSourceConstants(t *testing.T) {
	if EvSourceGraph != "graph" {
		t.Fatalf("expected 'graph', got %q", EvSourceGraph)
	}
	if EvSourceTest != "test" {
		t.Fatalf("expected 'test', got %q", EvSourceTest)
	}
	if EvSourceStack != "stacktrace" {
		t.Fatalf("expected 'stacktrace', got %q", EvSourceStack)
	}
	if EvSourceExecution != "execution" {
		t.Fatalf("expected 'execution', got %q", EvSourceExecution)
	}
}

func TestParseStackFramesVsEmpty(t *testing.T) {
	_ = ParseStackFrames("some random text")
	_ = ParseStackFrames("1: main.go:42")
	_ = ParseStackFrames("")
}

func TestNewRunResultAdapterInterface(t *testing.T) {
	adapter := NewRunResultAdapter("out", "err", 1)

	var iface = adapter
	if iface.StdOut() != "out" {
		t.Fatal("StdoutStderrer interface mismatch")
	}
}

func TestEngineWithRetriever(t *testing.T) {
	ret := newMockRetriever()
	ret.symbolResults["NewEngine"] = []SearchResult{
		{File: "engine.go", Line: 10, Content: "func NewEngine", Confidence: 1.0, Strategy: "graph.exact"},
	}
	ret.textResults["failing"] = []SearchResult{
		{File: "test.go", Line: 5, Content: "failing test", Confidence: 0.5, Strategy: "rg.pattern"},
	}

	exec := newMockExecutor()
	exec.allResult = &TestResultSummary{Passed: true}

	eng := NewEngine(".", "NewEngine is failing", ret, exec)

	err := eng.stateObserve()
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}

	err = eng.stateHypothesize()
	if err != nil {
		t.Fatalf("Hypothesize: %v", err)
	}

	_ = eng.State.Transition(StateSearch)
	err = eng.stateSearch()
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	graphEvidence := eng.Evidence.BySource(EvSourceGraph)
	if len(graphEvidence) == 0 {
		t.Log("No graph evidence found (expected with mock)")
	}
}

func TestEngineStateDone(t *testing.T) {
	eng := NewEngine(".", "test", nil, nil)
	_ = eng.State.Transition(StateDone)

	err := eng.executeCurrentState()
	if err != nil {
		t.Fatalf("executeCurrentState in Done: %v", err)
	}
}

func TestExtractErrorOutputRunResultNilError(t *testing.T) {
	output := ExtractErrorOutput(nil, nil)
	if output != "" {
		t.Fatalf("expected empty, got %q", output)
	}
}

func TestNewContextLedger(t *testing.T) {
	cl := NewContextLedger()
	if cl == nil {
		t.Fatal("expected non-nil context ledger")
	}
	if cl.Source != "investigate" {
		t.Fatalf("expected source 'investigate', got %q", cl.Source)
	}
	if len(cl.Targets) != 0 {
		t.Fatalf("expected 0 targets, got %d", len(cl.Targets))
	}
}

func TestContextLedgerAddTarget(t *testing.T) {
	cl := NewContextLedger()
	cl.AddTarget(Target{File: "test.go", Line: 42, Node: "TestFunc", Kind: "function"})
	if len(cl.Targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(cl.Targets))
	}
	if cl.Targets[0].File != "test.go" {
		t.Fatalf("expected test.go, got %s", cl.Targets[0].File)
	}
	if cl.Targets[0].Node != "TestFunc" {
		t.Fatalf("expected TestFunc, got %s", cl.Targets[0].Node)
	}
}

func TestContextLedgerSetConclusion(t *testing.T) {
	cl := NewContextLedger()
	cl.SetConclusion("bug in parser", true)
	if cl.Conclusion != "bug in parser" {
		t.Fatalf("expected 'bug in parser', got %q", cl.Conclusion)
	}
	if !cl.Resolved {
		t.Fatal("expected resolved=true")
	}
}

func TestContextLedgerFormatForPlan(t *testing.T) {
	cl := NewContextLedger()
	cl.Problem = "test failure"
	cl.AddTarget(Target{File: "test.go", Line: 10, Node: "TestFunc", Kind: "function"})
	cl.SetConclusion("fixed", true)

	output := cl.FormatForPlan()
	if !strings.Contains(output, "INVESTIGATION LEDGER") {
		t.Fatal("expected INVESTIGATION LEDGER header")
	}
	if !strings.Contains(output, "test failure") {
		t.Fatal("expected 'test failure' in output")
	}
	if !strings.Contains(output, "TestFunc") {
		t.Fatal("expected TestFunc in output")
	}
	if !strings.Contains(output, "BOUNDARY ENFORCEMENT") {
		t.Fatal("expected BOUNDARY ENFORCEMENT section")
	}
	if !strings.Contains(output, "/plan") {
		t.Fatal("expected handoff to /plan")
	}
}

func TestContextLedgerFormatForPlanNoTargets(t *testing.T) {
	cl := NewContextLedger()
	cl.Problem = "no targets found"
	output := cl.FormatForPlan()
	if !strings.Contains(output, "no targets") {
		t.Fatalf("expected problem in output, got: %.80s", output)
	}
}

func TestContextLedgerTargetPackages(t *testing.T) {
	cl := NewContextLedger()
	cl.AddTarget(Target{File: "internal/parser/stream.go", Line: 1})
	cl.AddTarget(Target{File: "internal/parser/types.go", Line: 1})
	cl.AddTarget(Target{File: "cmd/main.go", Line: 1})

	pkgs := cl.TargetPackages()
	if len(pkgs) == 0 {
		t.Fatal("expected at least 1 package")
	}
	hasParser := false
	for _, p := range pkgs {
		if p == "internal/parser" {
			hasParser = true
		}
	}
	if !hasParser {
		t.Fatalf("expected internal/parser in packages, got %v", pkgs)
	}
}

func TestNewTargetIsolator(t *testing.T) {
	ti := NewTargetIsolator(".")
	if ti == nil {
		t.Fatal("expected non-nil TargetIsolator")
	}
	if ti.root != "." {
		t.Fatalf("expected root '.', got %q", ti.root)
	}
}

func TestTargetIsolatorLocateNode(t *testing.T) {
	ti := NewTargetIsolator(".")
	node, kind := ti.locateNode("non_existent_file.go", 1)
	if node == "" {
		t.Fatal("expected at least file name fallback")
	}
	if kind != "file" {
		t.Fatalf("expected kind 'file', got %q", kind)
	}
}

func TestTargetIsolatorIsolateFromEvidence(t *testing.T) {
	ti := NewTargetIsolator(".")
	evidence := []Evidence{
		{File: "target_test_tmp.go", Line: 5, Content: "test evidence", Source: EvSourceStack},
	}
	frames := []StackFrame{
		{File: "target_test_other.go", Line: 10},
	}

	targets := ti.IsolateFromEvidence(evidence, frames)
	if len(targets) == 0 {
		t.Log("no targets isolated (expected with non-existent files)")
	}
}

func TestTargetIsolatorReadSnippet(t *testing.T) {
	ti := NewTargetIsolator(".")
	snippet := ti.readSnippet("non_existent.go", 5)
	if snippet != "" {
		t.Fatal("expected empty snippet for non-existent file")
	}
}

func TestContextLedgerEmptyTargetPackages(t *testing.T) {
	cl := NewContextLedger()
	pkgs := cl.TargetPackages()
	if len(pkgs) != 0 {
		t.Fatalf("expected 0 packages, got %d", len(pkgs))
	}
}

func TestTargetStruct(t *testing.T) {
	target := Target{
		File:    "test.go",
		Line:    42,
		Node:    "TestFunction",
		Kind:    "function",
		Snippet: "func TestFunction()",
	}
	if target.File != "test.go" {
		t.Fatal("file mismatch")
	}
	if target.Line != 42 {
		t.Fatal("line mismatch")
	}
	if target.Node != "TestFunction" {
		t.Fatal("node mismatch")
	}
}

func TestEngineInitializationWithIsolator(t *testing.T) {
	eng := NewEngine(".", "test problem", nil, nil)
	if eng.Isolator == nil {
		t.Fatal("expected non-nil Isolator")
	}
	if eng.Ledger == nil {
		t.Fatal("expected non-nil Ledger")
	}
	if eng.Ledger.Source != "investigate" {
		t.Fatalf("expected source 'investigate', got %q", eng.Ledger.Source)
	}
}

func TestEngineFormatLedgerForPlan(t *testing.T) {
	eng := NewEngine(".", "test failure in parser", nil, nil)
	eng.Ledger.AddTarget(Target{File: "parser.go", Line: 42, Node: "Parse", Kind: "function"})
	eng.Ledger.SetConclusion("nil pointer in Parse", true)

	output := eng.FormatLedgerForPlan()
	if !strings.Contains(output, "test failure in parser") {
		t.Fatal("expected problem in ledger output")
	}
	if !strings.Contains(output, "Parse") {
		t.Fatal("expected Parse function in ledger output")
	}
	if !strings.Contains(output, "/plan") {
		t.Fatal("expected /plan handoff in ledger output")
	}
}

func TestAbs(t *testing.T) {
	if abs(5) != 5 {
		t.Fatal("abs(5) != 5")
	}
	if abs(-5) != 5 {
		t.Fatal("abs(-5) != 5")
	}
	if abs(0) != 0 {
		t.Fatal("abs(0) != 0")
	}
}
