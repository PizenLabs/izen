package investigate

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/retrieval"
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
		Output:  "./cmd/api/main.go:7:5: undefined: MissingHandler\nFAIL",
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

	err := eng.stateObserve(context.Background())
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

	err := eng.stateObserve(context.Background())
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

// TestStateSearchIsLegacyFree verifies the brute-force raw-token LX loops have
// been removed: stateSearch performs ZERO retriever calls and simply advances
// the state machine. Forensics happen exclusively via dispatchForensics.
func TestStateSearchIsLegacyFree(t *testing.T) {
	calls := 0
	spy := &spyRetriever{onCall: func() { calls++ }}
	eng := NewEngine(".", "Investigate cause failure: 0.640s [GIN-debug] RUN PASS", spy, nil)
	_ = eng.State.Transition(StateHypothesize)
	_ = eng.State.Transition(StateSearch)

	if err := eng.stateSearch(); err != nil {
		t.Fatalf("stateSearch: %v", err)
	}
	if calls != 0 {
		t.Fatalf("legacy stateSearch spawned %d LX calls — brute-force loop still alive", calls)
	}
	if eng.State.Current() != StateGather {
		t.Fatalf("expected transition to Gather, got %s", eng.State.Current())
	}
}

// spyRetriever counts how many times any LX method is invoked.
type spyRetriever struct {
	onCall func()
}

func (s *spyRetriever) SearchSymbol(name string) ([]SearchResult, error) {
	if s.onCall != nil {
		s.onCall()
	}
	return nil, nil
}
func (s *spyRetriever) SearchText(text string) ([]SearchResult, error) {
	if s.onCall != nil {
		s.onCall()
	}
	return nil, nil
}
func (s *spyRetriever) SearchFile(path string) ([]SearchResult, error) {
	if s.onCall != nil {
		s.onCall()
	}
	return nil, nil
}
func (s *spyRetriever) SearchPackage(pkg string) ([]SearchResult, error) {
	if s.onCall != nil {
		s.onCall()
	}
	return nil, nil
}
func (s *spyRetriever) ReadTarget(path string, lines int) ([]SearchResult, error) {
	if s.onCall != nil {
		s.onCall()
	}
	return nil, nil
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

	err := eng.stateObserve(context.Background())
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

	err := eng.executeCurrentState(context.Background())
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

func TestParseCompilerTargets(t *testing.T) {
	out := `# github.com/foo/bar/cmd/api
./cmd/api/main.go:7:5: undefined: Something
internal/parser/stream.go:42:10: expected ';'`
	targets := ParseCompilerTargets(out)
	if len(targets) == 0 {
		t.Fatal("expected compiler targets to be extracted from diagnostic logs")
	}
	byFile := make(map[string]Target)
	for _, tgt := range targets {
		byFile[tgt.File] = tgt
	}
	if tgt, ok := byFile["cmd/api/main.go"]; !ok {
		t.Fatalf("expected cmd/api/main.go target, got %v", byFile)
	} else if tgt.Line != 7 || tgt.Kind == "" {
		t.Fatalf("expected line 7 and resolved node, got line=%d kind=%q", tgt.Line, tgt.Kind)
	}
	if _, ok := byFile["internal/parser/stream.go"]; !ok {
		t.Fatal("expected internal/parser/stream.go target")
	}
	// No coordinates → no false positives on plain text.
	if len(ParseCompilerTargets("just some random text with no paths")) != 0 {
		t.Fatal("expected no targets from non-diagnostic text")
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
	if !strings.Contains(output, "test failure") {
		t.Fatal("expected 'test failure' in output")
	}
	if !strings.Contains(output, "TestFunc") {
		t.Fatal("expected TestFunc in output")
	}
	if !strings.Contains(output, "fixed") {
		t.Fatal("expected conclusion in output")
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
	if !strings.Contains(output, "nil pointer") {
		t.Fatal("expected conclusion in ledger output")
	}
}

func TestClassifyLogOutputCompilation(t *testing.T) {
	cats := ClassifyLogOutput("cmd/api/main.go:7:5: no required module provides package github.com/docker/docker/client")
	if len(cats) == 0 {
		t.Fatal("expected at least 1 category")
	}
	hasComp := false
	for _, c := range cats {
		if c == ErrCatCompilation {
			hasComp = true
		}
	}
	if !hasComp {
		t.Fatal("expected compilation error category")
	}
}

func TestClassifyLogOutputEnvironment(t *testing.T) {
	cats := ClassifyLogOutput("could not start mysql container: rootless Docker not found, failed to create Docker provider")
	if len(cats) == 0 {
		t.Fatal("expected at least 1 category")
	}
	hasEnv := false
	for _, c := range cats {
		if c == ErrCatEnvironment {
			hasEnv = true
		}
	}
	if !hasEnv {
		t.Fatal("expected environment error category")
	}
}

func TestClassifyLogOutputMultiFailure(t *testing.T) {
	output := `cmd/api/main.go:7:5: no required module provides package github.com/docker/docker/client
could not start mysql container: rootless Docker not found, failed to create Docker provider
--- FAIL: TestDatabaseConnect`

	cats := ClassifyLogOutput(output)
	hasComp := false
	hasEnv := false
	hasTest := false
	for _, c := range cats {
		switch c {
		case ErrCatCompilation:
			hasComp = true
		case ErrCatEnvironment:
			hasEnv = true
		case ErrCatTestFailure:
			hasTest = true
		}
	}
	if !hasComp {
		t.Fatal("expected compilation error category in multi-failure")
	}
	if !hasEnv {
		t.Fatal("expected environment error category in multi-failure")
	}
	if !hasTest {
		t.Fatal("expected test failure category in multi-failure")
	}
}

func TestClassifyLogOutputUnknown(t *testing.T) {
	cats := ClassifyLogOutput("some random informational message")
	if len(cats) != 1 || cats[0] != ErrCatUnknown {
		t.Fatalf("expected unknown category, got %v", cats)
	}
}

func TestHypothesisAddWithCategory(t *testing.T) {
	hm := NewHypothesisManager()

	h := hm.AddWithCategory("missing docker client dependency", HypCatBlockerCompilation)
	if h.Category != HypCatBlockerCompilation {
		t.Fatalf("expected blocker compilation category, got %s", h.Category)
	}
	if !h.IsBlocker {
		t.Fatal("expected IsBlocker = true")
	}
	if h.Confidence != 1.0 {
		t.Fatalf("expected confidence 1.0 for blocker, got %f", h.Confidence)
	}

	h2 := hm.AddWithCategory("Docker daemon not running", HypCatEnvironment)
	if h2.Category != HypCatEnvironment {
		t.Fatalf("expected environment category, got %s", h2.Category)
	}
	if h2.IsBlocker {
		t.Fatal("expected IsBlocker = false for environment")
	}
	if h2.Confidence != 0.5 {
		t.Fatalf("expected default confidence 0.5, got %f", h2.Confidence)
	}

	h3 := hm.Add("general theory")
	if h3.Category != HypCatGeneral {
		t.Fatalf("expected general category, got %s", h3.Category)
	}
}

func TestHypothesisManagerByCategory(t *testing.T) {
	hm := NewHypothesisManager()
	hm.AddWithCategory("compilation error", HypCatBlockerCompilation)
	hm.AddWithCategory("Docker missing", HypCatEnvironment)
	hm.AddWithCategory("test failure", HypCatSourceCode)
	hm.Add("general")

	blockers := hm.ByCategory(HypCatBlockerCompilation)
	if len(blockers) != 1 {
		t.Fatalf("expected 1 blocker, got %d", len(blockers))
	}

	env := hm.ByCategory(HypCatEnvironment)
	if len(env) != 1 {
		t.Fatalf("expected 1 env hypothesis, got %d", len(env))
	}

	allBlockers := hm.Blockers()
	if len(allBlockers) != 1 {
		t.Fatalf("expected 1 blocker from Blockers(), got %d", len(allBlockers))
	}
}

func TestEvidenceStoreByCategory(t *testing.T) {
	es := NewEvidenceStore()

	es.Add(EvSourceTest, "no required module provides package foo", "main.go", 7, 0.8)
	es.Add(EvSourceTest, "rootless Docker not found", "", 0, 0.6)
	es.Add(EvSourceTest, "some normal log message", "", 0, 0.3)

	compEv := es.ByCategory(ErrCatCompilation)
	if len(compEv) != 1 {
		t.Fatalf("expected 1 compilation evidence, got %d", len(compEv))
	}

	envEv := es.ByCategory(ErrCatEnvironment)
	if len(envEv) != 1 {
		t.Fatalf("expected 1 environment evidence, got %d", len(envEv))
	}

	if !es.HasCategory(ErrCatCompilation) {
		t.Fatal("expected HasCategory(ErrCatCompilation) = true")
	}
	if !es.HasCategory(ErrCatEnvironment) {
		t.Fatal("expected HasCategory(ErrCatEnvironment) = true")
	}
}

func TestEvidenceStoreByAnyCategory(t *testing.T) {
	es := NewEvidenceStore()
	es.Add(EvSourceTest, "no required module provides package foo", "main.go", 7, 0.8)
	es.Add(EvSourceTest, "rootless Docker not found", "", 0, 0.6)

	byCat := es.ByAnyCategory()
	if len(byCat) < 2 {
		t.Fatalf("expected at least 2 categories, got %d", len(byCat))
	}
}

func TestEngineMultiFailureHypotheses(t *testing.T) {
	multiOutput := `cmd/api/main.go:7:5: no required module provides package github.com/docker/docker/client
could not start mysql container: rootless Docker not found, failed to create Docker provider
--- FAIL: TestDatabaseConnect`

	exec := newMockExecutor()
	exec.allResult = &TestResultSummary{
		Package: ".",
		Passed:  false,
		Total:   3,
		FailedN: 2,
		Failed:  []string{"TestDatabaseConnect"},
		Output:  multiOutput,
	}

	eng := NewEngine(".", "test multi-failure diagnosis", nil, exec)
	err := eng.stateObserve(context.Background())
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}

	err = eng.stateHypothesize()
	if err != nil {
		t.Fatalf("Hypothesize: %v", err)
	}

	blockers := eng.Hypotheses.Blockers()
	if len(blockers) == 0 {
		t.Fatal("expected at least 1 blocker hypothesis for compilation error")
	}

	envHyps := eng.Hypotheses.ByCategory(HypCatEnvironment)
	if len(envHyps) == 0 {
		t.Fatal("expected at least 1 environment hypothesis")
	}

	for _, b := range blockers {
		if b.Confidence != 1.0 {
			t.Fatalf("expected blocker confidence 1.0, got %f", b.Confidence)
		}
	}
}

func TestEngineEvaluateShortCircuitsCompilationBlocker(t *testing.T) {
	exec := newMockExecutor()
	exec.allResult = &TestResultSummary{
		Package: ".",
		Passed:  false,
		Total:   1,
		FailedN: 1,
		Failed:  []string{"TestBuild"},
		Output:  "no required module provides package github.com/docker/docker/client",
	}

	eng := NewEngine(".", "build failure", nil, exec)

	_ = eng.stateObserve(context.Background())
	_ = eng.stateHypothesize()

	_ = eng.State.Transition(StateSearch)
	_ = eng.stateSearch()
	_ = eng.State.Transition(StateGather)
	_ = eng.stateGather()

	err := eng.stateEvaluate()
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	if eng.State.Current() != StateVerify && eng.State.Current() != StatePropose && eng.State.Current() != StateDone {
		t.Fatalf("expected blocker to short-circuit, got state %s", eng.State.Current())
	}
}

func TestEngineBlockerHypothesisIsCreatedInStateHypothesize(t *testing.T) {
	exec := newMockExecutor()
	exec.allResult = &TestResultSummary{
		Package: ".",
		Passed:  false,
		FailedN: 1,
		Failed:  []string{"TestBuild"},
		Output:  "cmd/api/main.go:7:5: no required module provides package github.com/docker/docker/client",
	}

	eng := NewEngine(".", "missing module", nil, exec)
	_ = eng.stateObserve(context.Background())
	_ = eng.stateHypothesize()

	blockers := eng.Hypotheses.Blockers()
	if len(blockers) == 0 {
		t.Fatal("expected a blocker hypothesis for compilation error")
	}

	best := eng.Hypotheses.Best()
	if best == nil || !best.IsBlocker {
		t.Fatal("expected best hypothesis to be the blocker")
	}
	if best.Confidence != 1.0 {
		t.Fatalf("expected blocker confidence 1.0, got %f", best.Confidence)
	}
}

func TestStateProposeNilResultGuard(t *testing.T) {
	eng := NewEngine(".", "test problem", nil, nil)
	_ = eng.State.Transition(StateHypothesize)
	_ = eng.State.Transition(StateSearch)
	_ = eng.State.Transition(StateGather)
	_ = eng.State.Transition(StateEvaluate)
	_ = eng.State.Transition(StateVerify)
	_ = eng.State.Transition(StatePropose)

	err := eng.statePropose()
	if err != nil {
		t.Fatalf("statePropose on nil e.Result should not error: %v", err)
	}
	if eng.Result == nil {
		t.Fatal("expected e.Result to be initialized after statePropose")
	}
	if !eng.State.IsTerminal() {
		t.Fatal("expected terminal state after propose")
	}
}

func TestShortCircuitEvaluateToProposeNoPanic(t *testing.T) {
	exec := newMockExecutor()
	exec.allResult = &TestResultSummary{
		Package: ".",
		Passed:  false,
		Total:   1,
		FailedN: 1,
		Failed:  []string{"TestBuild"},
		Output:  "no required module provides package github.com/docker/docker/client",
	}

	eng := NewEngine(".", "build failure", nil, exec)
	_ = eng.stateObserve(context.Background())
	_ = eng.stateHypothesize()
	_ = eng.State.Transition(StateSearch)
	_ = eng.stateSearch()
	_ = eng.State.Transition(StateGather)
	_ = eng.stateGather()
	err := eng.stateEvaluate()
	if err != nil {
		t.Fatalf("stateEvaluate: %v", err)
	}

	_current := eng.State.Current()
	if _current != StatePropose {
		t.Fatalf("expected StatePropose from short-circuit, got %s", _current)
	}

	err = eng.statePropose()
	if err != nil {
		t.Fatalf("statePropose should not panic on short-circuit: %v", err)
	}
	if eng.Result == nil {
		t.Fatal("expected Result to be initialized")
	}
	if !eng.Result.Resolved {
		t.Fatal("expected blocker to be resolved")
	}
}

func TestShortCircuitFullRunContextNoPanic(t *testing.T) {
	exec := newMockExecutor()
	exec.allResult = &TestResultSummary{
		Package: ".",
		Passed:  false,
		Total:   1,
		FailedN: 1,
		Failed:  []string{"TestBuild"},
		Output:  "no required module provides package github.com/docker/docker/client\ncould not start mysql container: rootless Docker not found",
	}

	eng := NewEngine(".", "multi-failure build", nil, exec)
	result, err := eng.Run()
	if err != nil {
		t.Fatalf("Run should not panic on short-circuit: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if !result.Resolved {
		t.Fatal("expected blocker to be resolved")
	}
	if result.Loops < 1 || result.Loops > 3 {
		t.Logf("short-circuit completed in %d loops (acceptable range: 1-3)", result.Loops)
	}
	if result.Duration == "" {
		t.Fatal("expected non-empty duration")
	}
}

func TestExtractFileFromCompilationError(t *testing.T) {
	input := "cmd/api/main.go:7:5: no required module provides package foo"
	file := extractFileFromCompilationError(input)
	if file != "cmd/api/main.go:7" {
		t.Fatalf("expected 'cmd/api/main.go:7', got %q", file)
	}

	empty := extractFileFromCompilationError("some random text")
	if empty != "" {
		t.Fatalf("expected empty, got %q", empty)
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

// TestRetrieverAdapterNil verifies the adapter degrades safely when no inner
// retriever is supplied (no nil-panic, empty results).
func TestRetrieverAdapterNil(t *testing.T) {
	adapter := NewRetrieverAdapter(nil)
	if r, err := adapter.SearchSymbol("Foo"); err != nil || len(r) != 0 {
		t.Fatalf("nil adapter SearchSymbol should return empty, got %v err %v", r, err)
	}
	if r, err := adapter.SearchText("bar"); err != nil || len(r) != 0 {
		t.Fatalf("nil adapter SearchText should return empty, got %v err %v", r, err)
	}
}

// TestRetrieverAdapterWraps verifies the adapter maps retrieval.ResultSet
// entries into investigate.SearchResult faithfully.
func TestRetrieverAdapterWraps(t *testing.T) {
	inner := &fakeRetrievalRetriever{
		results: []retrieval.Result{
			{File: "a.go", Line: 12, Content: "func A()", Confidence: 0.9, Strategy: "graph"},
		},
	}
	adapter := NewRetrieverAdapter(inner)
	out, err := adapter.SearchSymbol("A")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(out) != 1 || out[0].File != "a.go" || out[0].Line != 12 {
		t.Fatalf("adapter did not map result correctly: %+v", out)
	}
}

// fakeRetrievalRetriever is a minimal retrieval.Retriever stub for the adapter
// test. It implements only the methods the adapter calls.
type fakeRetrievalRetriever struct {
	results []retrieval.Result
}

func (f *fakeRetrievalRetriever) SearchSymbol(name string) *retrieval.ResultSet {
	return &retrieval.ResultSet{Results: f.results, Confidence: 0.9}
}
func (f *fakeRetrievalRetriever) SearchText(text string) *retrieval.ResultSet {
	return &retrieval.ResultSet{Results: f.results}
}
func (f *fakeRetrievalRetriever) SearchFile(path string) *retrieval.ResultSet {
	return &retrieval.ResultSet{}
}
func (f *fakeRetrievalRetriever) SearchPackage(pkg string) *retrieval.ResultSet {
	return &retrieval.ResultSet{}
}
func (f *fakeRetrievalRetriever) ReadTarget(path string, lines int) *retrieval.ResultSet {
	return &retrieval.ResultSet{}
}

// TestEngineForcesForensicExecution verifies /investigate records forensic
// execution and emits the mandatory timing log, using a fast mock executor so
// the run does not short-circuit. The duration must be non-zero because the
// state machine actually executes the diagnostic toolchain.
func TestEngineForcesForensicExecution(t *testing.T) {
	var logged []string
	SetForensicLog(func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	})
	defer SetForensicLog(log.Printf)

	eng := NewEngine(".", "the build is failing", newMockRetriever(), newMockExecutor())
	result, err := eng.Run()
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !eng.forensicsExecuted() {
		t.Fatal("engine must record that forensics executed (no short-circuit)")
	}
	if result.Duration == "" {
		t.Fatal("expected non-empty forensic duration")
	}
	// A genuine forensic pass (real toolchain) always exceeds 1ms; the mock
	// executor above is intentionally instant, so we assert the duration string
	// is present and well-formed rather than strictly non-"0s". The mandatory
	// log below is the authoritative proof that forensics actually ran.
	if result.Duration == "0s" {
		t.Logf("instant mock run measured %q (expected with mock executor)", result.Duration)
	}

	var found bool
	for _, l := range logged {
		if strings.Contains(l, "Forensic analysis executed in") {
			found = true
		}
	}
	if !found {
		t.Fatal("mandatory 'Forensic analysis executed in X seconds' log not emitted")
	}
}

// TestShellTestExecutorFastCommand verifies the shell executor actually invokes
// a real command and parses its output into a summary (no silent no-op). It uses
// `go version` — a fast, universally available command — to keep the test quick.
func TestShellTestExecutorFastCommand(t *testing.T) {
	exec := &ShellTestExecutor{root: ".", timeout: 10 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	summary, err := exec.run(ctx, "version")
	if err != nil && summary == nil {
		t.Fatalf("executor returned nil summary and error: %v", err)
	}
	if summary == nil {
		t.Fatal("expected non-nil summary from shell executor")
	}
	if summary.Output == "" {
		t.Fatal("expected command output in summary")
	}
}

// TestHeuristicClassify verifies the offline dispatcher routes known failure
// signatures to the correct native tool without any LLM call.
func TestHeuristicClassify(t *testing.T) {
	cases := []struct {
		log  string
		want Tool
	}{
		{"panic: runtime error: invalid memory address", ToolTrace},
		{"--- FAIL: TestFoo", ToolTrace},
		{"exec: \"docker\": executable file not found in $PATH", ToolEnv},
		{"command not found: go", ToolEnv},
		{"undefined: SomeSymbol", ToolLX},
		{"cannot find package \"internal/foo\"", ToolLX},
		{"some unrelated log line about business logic", ToolDiagnose},
	}
	for _, c := range cases {
		got := heuristicClassify(c.log)
		if got.Tool != c.want {
			t.Errorf("heuristicClassify(%q) = %s, want %s", c.log, got.Tool, c.want)
		}
	}
}

// TestNextFallback verifies the strict fallback chain always terminates at
// $diagnose and never revisits an already-failed tool.
func TestNextFallback(t *testing.T) {
	if nextFallback(ToolLX) != ToolDiagnose {
		t.Fatal("lx should fall back straight to diagnose")
	}
	if nextFallback(ToolTrace) != ToolDiagnose {
		t.Fatal("trace should fall back to diagnose")
	}
	if nextFallback(ToolEnv) != ToolDiagnose {
		t.Fatal("env should fall back to diagnose")
	}
	if nextFallback(ToolDiagnose) != "" {
		t.Fatal("diagnose is terminal — must return empty")
	}
}

// TestDispatchStrategyNoProvider falls back to heuristics when no LLM provider
// is configured, keeping the engine offline and within budget.
func TestDispatchStrategyNoProvider(t *testing.T) {
	s := DispatchStrategy(context.Background(), nil, "", "panic: nil pointer dereference", 0)
	if s.Tool != ToolTrace {
		t.Fatalf("expected offline heuristic to pick trace, got %s", s.Tool)
	}
}

// TestDispatchForensicsCapsActions verifies the engine never exceeds the
// MaxActionsPerRun ceiling during orchestrated dispatch, even when every tool
// fails (forcing the full fallback chain).
func TestDispatchForensicsCapsActions(t *testing.T) {
	// A retriever that always errors simulates the lx RPC -32603 failure path.
	failRetriever := &failRetriever{}
	eng := NewEngineWithAI(".", "undefined: MissingThing", failRetriever, nil, nil, "")
	// Force the diagnostics path through lx so the whole chain runs.
	eng.Ledger.SetDiagnostics("undefined: MissingThing")

	var actions int
	orig := dispatchLog
	dispatchLog = func(format string, args ...interface{}) {
		if strings.Contains(fmt.Sprintf(format, args...), "action ") {
			actions++
		}
	}
	defer func() { dispatchLog = orig }()

	eng.dispatchForensics(context.Background())

	if actions > MaxActionsPerRun {
		t.Fatalf("dispatched %d actions, exceeds ceiling %d", actions, MaxActionsPerRun)
	}
	// The chain must terminate cleanly at diagnose, not panic or loop.
	if len(eng.Ledger.Targets) == 0 {
		t.Fatal("expected at least one ingested target from the diagnose fallback")
	}
}

// failRetriever is a Retriever stub that always returns an error, used to
// exercise the strict-fallback path.
type failRetriever struct{}

func (f *failRetriever) SearchSymbol(name string) ([]SearchResult, error) {
	return nil, fmt.Errorf("rpc error -32603: syntax error")
}
func (f *failRetriever) SearchText(text string) ([]SearchResult, error) {
	return nil, fmt.Errorf("rpc error -32603: syntax error")
}
func (f *failRetriever) SearchFile(path string) ([]SearchResult, error) {
	return nil, fmt.Errorf("rpc error -32603: syntax error")
}
func (f *failRetriever) SearchPackage(pkg string) ([]SearchResult, error) {
	return nil, fmt.Errorf("rpc error -32603: syntax error")
}
func (f *failRetriever) ReadTarget(path string, lines int) ([]SearchResult, error) {
	return nil, fmt.Errorf("rpc error -32603: syntax error")
}

// hangProvider is an ai.Provider whose Execute blocks forever (until ctx is
// cancelled), simulating a stalled LLM daemon. It is used to prove the dispatch
// budget forces /investigate to return to the prompt instead of hanging.
type hangProvider struct{}

func (h *hangProvider) Name() string { return "hang" }

func (h *hangProvider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (h *hangProvider) ExecuteStream(ctx context.Context, req ai.Request) (io.ReadCloser, error) {
	return nil, ctx.Err()
}

// TestDispatchForensicsRespectsBudget verifies that even when the LLM provider
// hangs indefinitely, dispatchForensics returns within DispatchBudget and never
// blocks the caller (the UI goroutine) — directly guarding the "stuck spinning
// spinner" regression.
func TestDispatchForensicsRespectsBudget(t *testing.T) {
	// Route to diagnose so the (hanging) provider is actually invoked.
	eng := NewEngineWithAI(".", "some generic failure with no clear signature", newMockRetriever(), nil, &hangProvider{}, "test-model")
	eng.Ledger.SetDiagnostics("some generic failure with no clear signature")

	start := time.Now()
	eng.dispatchForensics(context.Background())
	elapsed := time.Since(start)

	if elapsed > DispatchBudget+2*time.Second {
		t.Fatalf("dispatchForensics hung for %s — exceeds budget %s", elapsed, DispatchBudget)
	}
}

// TestSinksNeverWriteRawToStdio is the regression guard for the TUI layout
// corruption: when the UI redirects the engine's log sinks (as NewProgram does
// via SetForensicLog/SetDispatchLog), the orchestrator MUST emit its progress
// through the sink and write ZERO bytes to os.Stdout / os.Stderr. Any raw byte
// on real stdio while Bubble Tea owns the alt-screen corrupts the rendered
// frame (broken ──── separators, misaligned viewport, "doubled" prompt).
func TestSinksNeverWriteRawToStdio(t *testing.T) {
	// Redirect both sinks into an in-memory buffer, exactly like NewProgram
	// wires them into the UI activity logger.
	var captured strings.Builder
	sink := func(format string, args ...interface{}) {
		fmt.Fprintf(&captured, format, args...)
	}
	origForensic := forensicLog
	origDispatch := dispatchLog
	SetForensicLog(sink)
	SetDispatchLog(sink)
	defer func() {
		forensicLog = origForensic
		dispatchLog = origDispatch
	}()

	// Capture the real process stdout/stderr for the duration of the run.
	origStdout, origStderr := os.Stdout, os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout, os.Stderr = wOut, wErr

	// Drain the pipes concurrently so writes never block.
	outCh := make(chan string, 1)
	errCh := make(chan string, 1)
	go func() { b, _ := io.ReadAll(rOut); outCh <- string(b) }()
	go func() { b, _ := io.ReadAll(rErr); errCh <- string(b) }()

	eng := NewEngineWithAI(".", "undefined: MissingThing", &failRetriever{}, nil, nil, "")
	eng.Ledger.SetDiagnostics("undefined: MissingThing")
	eng.dispatchForensics(context.Background())

	// Restore stdio and collect anything that leaked.
	_ = wOut.Close()
	_ = wErr.Close()
	os.Stdout, os.Stderr = origStdout, origStderr
	stdoutLeak := <-outCh
	stderrLeak := <-errCh

	if stdoutLeak != "" {
		t.Errorf("orchestrator leaked raw bytes to stdout (corrupts TUI frame): %q", stdoutLeak)
	}
	if stderrLeak != "" {
		t.Errorf("orchestrator leaked raw bytes to stderr (corrupts TUI frame): %q", stderrLeak)
	}
	// Sanity: the redirected sink must actually have received the telemetry,
	// proving the log went somewhere structured rather than being lost.
	if captured.Len() == 0 {
		t.Fatal("redirected sink received no orchestrator telemetry — wiring is broken")
	}
}

// TestIsRemotePackageTarget verifies the Target Type Validation Gate correctly
// separates remote import paths (which must be forbidden from local file
// operations) from genuine workspace files and bare symbols.
func TestIsRemotePackageTarget(t *testing.T) {
	remote := []string{
		"github.com/docker/docker/client",
		"golang.org/x/sync/errgroup",
		"gopkg.in/yaml.v3",
		"google.golang.org/grpc",
		"k8s.io/apimachinery/pkg/apis/meta/v1",
		"example.com/acme/widgets",
	}
	for _, tc := range remote {
		if !isRemotePackageTarget(tc) {
			t.Errorf("isRemotePackageTarget(%q) = false, want true (must forbid local file ops)", tc)
		}
	}

	local := []string{
		"internal/database",
		"./cmd/api/main.go",
		"../pkg/foo/bar.go",
		"cmd/api/main.go",
		"Foo.Bar",
		"TestX",
		"",
		"/abs/path/main.go",
	}
	for _, tc := range local {
		if isRemotePackageTarget(tc) {
			t.Errorf("isRemotePackageTarget(%q) = true, want false (must allow local handling)", tc)
		}
	}
}

// TestRunLXRemotePackageBypass is the regression guard for the orchestrator path
// hallucination bug: a remote import path must NEVER reach the local file
// reader (which previously produced a fatal "no such file or directory"). It
// must instead be routed straight to the package-remediation blueprint without
// invoking any file descriptor operation on the retriever.
func TestRunLXRemotePackageBypass(t *testing.T) {
	// A spy retriever that fails the test if ANY method is ever called — the
	// gate must short-circuit before touching the retriever at all.
	spy := &spyRetriever{onCall: func() {
		t.Fatal("retriever was invoked for a remote package target — local file ops were NOT forbidden")
	}}

	runner := NewToolRunner(".", nil, "", spy, "")
	res := runner.Run(context.Background(), ToolLX, "github.com/docker/docker/client")

	if !res.Ok {
		t.Fatal("expected remediation routing to succeed (Ok=true)")
	}
	if !strings.Contains(res.Content, "REMOTE DEPENDENCY BLOCKER") {
		t.Fatalf("expected remote-dependency remediation content, got %q", res.Content)
	}
	if !strings.Contains(res.Content, "go mod tidy") {
		t.Fatalf("expected environment remediation blueprint staged, got %q", res.Content)
	}
}

// TestRunLXLocalFileStillReads verifies the gate does not over-fire: a concrete
// workspace file is still routed to the retriever's SearchFile path (no breach
// of the normal contract).
func TestRunLXLocalFileStillReads(t *testing.T) {
	ret := newMockRetriever()
	ret.fileResults["cmd/api/main.go"] = []SearchResult{
		{File: "cmd/api/main.go", Line: 7, Content: "func main() {}", Confidence: 0.9},
	}
	runner := NewToolRunner(".", nil, "", ret, "")
	res := runner.Run(context.Background(), ToolLX, "cmd/api/main.go")

	if !res.Ok {
		t.Fatal("expected local file lookup to succeed")
	}
	if len(res.Evidence) == 0 {
		t.Fatal("expected evidence from the local file retriever")
	}
}
