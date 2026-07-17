package investigate

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type Engine struct {
	State      *StateMachine
	Hypotheses *HypothesisManager
	Evidence   *EvidenceStore
	Slicer     *ProximitySlicer
	TestLoop   *TestLoop
	Isolator   *TargetIsolator
	Ledger     *ContextLedger

	Problem   string
	root      string
	startedAt time.Time
	Result    *InvestigationResult

	retriever Retriever
	executor  TestExecutor
}

type InvestigationResult struct {
	Problem    string           `json:"problem"`
	RootCause  string           `json:"root_cause,omitempty"`
	Resolved   bool             `json:"resolved"`
	Conclusion string           `json:"conclusion"`
	Hypotheses []Hypothesis     `json:"hypotheses"`
	Evidence   []Evidence       `json:"evidence"`
	Proximity  []ProximitySlice `json:"proximity,omitempty"`
	Loops      int              `json:"loops"`
	Duration   string           `json:"duration"`
	Error      string           `json:"error,omitempty"`
}

type Retriever interface {
	SearchSymbol(name string) ([]SearchResult, error)
	SearchText(text string) ([]SearchResult, error)
	SearchFile(path string) ([]SearchResult, error)
	SearchPackage(pkg string) ([]SearchResult, error)
	ReadTarget(path string, lines int) ([]SearchResult, error)
}

type SearchResult struct {
	File       string  `json:"file"`
	Line       int     `json:"line"`
	Column     int     `json:"column"`
	Content    string  `json:"content"`
	Confidence float64 `json:"confidence"`
	Strategy   string  `json:"strategy"`
	SymbolName string  `json:"symbol_name,omitempty"`
	SymbolKind string  `json:"symbol_kind,omitempty"`
}

func NewEngine(root, problem string, retriever Retriever, executor TestExecutor) *Engine {
	return &Engine{
		State:      NewStateMachine(DefaultStateConfig()),
		Hypotheses: NewHypothesisManager(),
		Evidence:   NewEvidenceStore(),
		Slicer:     NewProximitySlicer(root, 10),
		TestLoop:   NewTestLoop(3),
		Isolator:   NewTargetIsolator(root),
		Ledger:     NewContextLedger(),
		Problem:    problem,
		root:       root,
		startedAt:  time.Now(),
		retriever:  retriever,
		executor:   executor,
	}
}

func (e *Engine) Run() (*InvestigationResult, error) {
	return e.RunContext(context.Background())
}

func (e *Engine) RunContext(ctx context.Context) (*InvestigationResult, error) {
	result := &InvestigationResult{
		Problem: e.Problem,
	}

	for !e.State.ShouldStop() {
		select {
		case <-ctx.Done():
			result.Error = fmt.Sprintf("investigation cancelled: %v", ctx.Err())
			result.Hypotheses = e.Hypotheses.All()
			result.Evidence = e.Evidence.All()
			result.Loops = e.State.IterationCount()
			result.Duration = time.Since(e.startedAt).Round(time.Millisecond).String()
			e.Result = result
			return result, ctx.Err()
		default:
		}

		if err := e.executeCurrentState(); err != nil {
			result.Error = err.Error()
			break
		}
	}

	result.Hypotheses = e.Hypotheses.All()
	result.Evidence = e.Evidence.All()
	result.Loops = e.State.IterationCount()
	result.Duration = time.Since(e.startedAt).Round(time.Millisecond).String()

	best := e.Hypotheses.Best()
	if best != nil {
		result.Resolved = true
		result.Conclusion = best.Theory
	} else if result.Loops >= e.State.config.MaxLoops {
		result.Conclusion = "investigation exhausted — no hypothesis could be confirmed"
	}

	// Merge: preserve fields set by statePropose (RootCause, Proximity, etc.)
	// that the local result variable does not carry.
	if e.Result != nil {
		if e.Result.RootCause != "" {
			result.RootCause = e.Result.RootCause
		}
		if len(e.Result.Proximity) > 0 {
			result.Proximity = e.Result.Proximity
		}
		if e.Result.Conclusion != "" && result.Conclusion == "" {
			result.Conclusion = e.Result.Conclusion
		}
	}

	e.Result = result
	return result, nil
}

func (e *Engine) executeCurrentState() error {
	switch e.State.Current() {
	case StateObserve:
		return e.stateObserve()
	case StateHypothesize:
		return e.stateHypothesize()
	case StateSearch:
		return e.stateSearch()
	case StateGather:
		return e.stateGather()
	case StateEvaluate:
		return e.stateEvaluate()
	case StateNarrow:
		return e.stateNarrow()
	case StateVerify:
		return e.stateVerify()
	case StatePropose:
		return e.statePropose()
	case StateDone:
		return nil
	default:
		return e.State.Transition(StateDone)
	}
}

func (e *Engine) stateObserve() error {
	observed := fmt.Sprintf("Observing problem: %s", e.Problem)
	e.Evidence.Add(EvSourceUser, observed, "", 0, 0.2)

	if e.executor != nil {
		summary, _ := e.TestLoop.Run(e.executor, testLoopConfig{Strategy: "all"})
		if summary != nil {
			output := BoundedLogPreprocessor(summary.Output)
			summary.Output = output
			e.Evidence.Add(EvSourceTest, output, summary.Package, 0, 0.5)
			e.Evidence.Add(EvSourceExecution, fmt.Sprintf("Tests: %d passed, %d failed, %d skipped",
				summary.PassedN, summary.FailedN, summary.Skipped), "", 0, 0.6)
			if !summary.Passed {
				e.Evidence.Add(EvSourceTest,
					fmt.Sprintf("Failed tests: %s", strings.Join(summary.Failed, ", ")),
					summary.Package, 0, 0.7)
			}

			frames := ParseStackFrames(output)
			for _, f := range frames {
				e.Evidence.Add(EvSourceStack,
					fmt.Sprintf("%s:%d", f.File, f.Line),
					f.File, f.Line, 0.6)
			}
		}
	}

	return e.State.Transition(StateHypothesize)
}

func (e *Engine) stateHypothesize() error {
	evidence := e.Evidence.All()

	if len(evidence) == 0 {
		h := e.Hypotheses.AddWithCategory("No initial evidence found. Need to gather more information.", HypCatGeneral)
		_ = h
		return e.State.Transition(StateSearch)
	}

	catHypotheses := e.buildHypothesesByCategory(evidence)

	if len(catHypotheses) == 0 {
		h := e.Hypotheses.Add(summarizeEvidence(evidence))
		for _, ev := range evidence {
			e.Hypotheses.LinkEvidence(h.ID, ev.ID)
		}
	}

	return e.State.Transition(StateSearch)
}

func (e *Engine) buildHypothesesByCategory(evidence []Evidence) map[HypothesisCategory]*Hypothesis {
	byCat := make(map[ErrorCategory][]Evidence)
	for _, ev := range evidence {
		for _, c := range ev.Categories {
			byCat[c] = append(byCat[c], ev)
		}
	}

	created := make(map[HypothesisCategory]*Hypothesis)

	if compEv, ok := byCat[ErrCatCompilation]; ok && len(compEv) > 0 {
		text := fmt.Sprintf("BLOCKER: Compilation/dependency error detected — %s",
			summarizeEvidence(compEv))
		h := e.Hypotheses.AddWithCategory(text, HypCatBlockerCompilation)
		for _, ev := range compEv {
			e.Hypotheses.LinkEvidence(h.ID, ev.ID)
		}
		created[HypCatBlockerCompilation] = h
	}

	if envEv, ok := byCat[ErrCatEnvironment]; ok && len(envEv) > 0 {
		text := fmt.Sprintf("Environment setup issue detected — %s",
			summarizeEvidence(envEv))
		h := e.Hypotheses.AddWithCategory(text, HypCatEnvironment)
		for _, ev := range envEv {
			e.Hypotheses.LinkEvidence(h.ID, ev.ID)
		}
		created[HypCatEnvironment] = h
	}

	if testEv, ok := byCat[ErrCatTestFailure]; ok && len(testEv) > 0 {
		if _, hasBlocker := byCat[ErrCatCompilation]; !hasBlocker {
			text := fmt.Sprintf("Source code test failure detected — %s",
				summarizeEvidence(testEv))
			h := e.Hypotheses.AddWithCategory(text, HypCatSourceCode)
			for _, ev := range testEv {
				e.Hypotheses.LinkEvidence(h.ID, ev.ID)
			}
			created[HypCatSourceCode] = h
		}
	}

	if len(byCat) == 1 {
		if _, onlyUnknown := byCat[ErrCatUnknown]; onlyUnknown {
			text := summarizeEvidence(evidence)
			h := e.Hypotheses.AddWithCategory(text, HypCatGeneral)
			for _, ev := range evidence {
				e.Hypotheses.LinkEvidence(h.ID, ev.ID)
			}
			created[HypCatGeneral] = h
		}
	}

	return created
}

func (e *Engine) stateSearch() error {
	if e.retriever == nil {
		return e.State.Transition(StateGather)
	}

	symbols := e.extractSymbolsFromProblem()
	for _, sym := range symbols {
		if sym == "" {
			continue
		}
		results, err := e.retriever.SearchSymbol(sym)
		if err == nil {
			for _, r := range results {
				e.Evidence.AddWithStrategy(EvSourceGraph, r.Content,
					r.File, r.Line, r.Confidence, r.Strategy)
			}
		}
	}

	texts := e.extractTextTermsFromProblem()
	for _, txt := range texts {
		if txt == "" {
			continue
		}
		results, err := e.retriever.SearchText(txt)
		if err == nil {
			for _, r := range results {
				e.Evidence.AddWithStrategy(EvSourceRipgrep, r.Content,
					r.File, r.Line, r.Confidence, r.Strategy)
			}
		}
	}

	stackEvidence := e.Evidence.BySource(EvSourceStack)
	for _, ev := range stackEvidence {
		if ev.File != "" && ev.Line > 0 {
			results, err := e.retriever.ReadTarget(ev.File, 30)
			if err == nil {
				for _, r := range results {
					e.Evidence.AddWithStrategy(EvSourceRead, r.Content,
						r.File, r.Line, r.Confidence, r.Strategy)
				}
			}
		}
	}

	return e.State.Transition(StateGather)
}

func (e *Engine) stateGather() error {
	evidence := e.Evidence.All()
	_ = evidence

	stackEvidence := e.Evidence.BySource(EvSourceStack)
	for _, ev := range stackEvidence {
		if ev.File != "" {
			slice := e.Slicer.Extract(StackFrame{File: ev.File, Line: ev.Line})
			if slice != nil {
				e.Result.Proximity = append(e.Result.Proximity, *slice)
			}
		}
	}

	return e.State.Transition(StateEvaluate)
}

func (e *Engine) stateEvaluate() error {
	blockers := e.Hypotheses.Blockers()
	if len(blockers) > 0 {
		activeBlockers := 0
		for _, b := range blockers {
			if b.Status == HypothesisActive || b.Status == HypothesisPending {
				e.Hypotheses.UpdateConfidence(b.ID, 1.0)
				e.Hypotheses.UpdateStatus(b.ID, HypothesisConfirmed)
				activeBlockers++
			}
		}
		if activeBlockers > 0 {
			return e.State.Transition(StatePropose)
		}
	}

	envHyps := e.Hypotheses.ByCategory(HypCatEnvironment)
	if len(envHyps) > 0 {
		for _, hyp := range envHyps {
			if hyp.Status == HypothesisActive {
				e.Hypotheses.UpdateConfidence(hyp.ID, 0.8)
				e.Hypotheses.UpdateStatus(hyp.ID, HypothesisConfirmed)
			}
		}
		return e.State.Transition(StateVerify)
	}

	highConf := e.Evidence.HighConfidence(0.7)
	activeHyp := e.Hypotheses.Active()

	if len(highConf) > 0 && len(activeHyp) > 0 {
		for _, hyp := range activeHyp {
			e.Hypotheses.UpdateConfidence(hyp.ID, 0.8)
			e.Hypotheses.UpdateStatus(hyp.ID, HypothesisConfirmed)
		}
		return e.State.Transition(StateVerify)
	}

	mediumConf := e.Evidence.HighConfidence(0.4)
	if len(mediumConf) == 0 && len(activeHyp) > 0 {
		for _, hyp := range activeHyp {
			e.Hypotheses.UpdateStatus(hyp.ID, HypothesisRejected)
		}
		return e.State.Transition(StateNarrow)
	}

	if len(activeHyp) > 0 {
		for _, hyp := range activeHyp {
			e.Hypotheses.UpdateConfidence(hyp.ID, 0.6)
		}
		return e.State.Transition(StateNarrow)
	}

	return e.State.Transition(StateHypothesize)
}

func (e *Engine) stateNarrow() error {
	failedTests := e.Evidence.BySource(EvSourceTest)
	stackFrames := e.Evidence.BySource(EvSourceStack)

	var targets []string
	for _, ev := range failedTests {
		if ev.File != "" {
			targets = append(targets, ev.File)
		}
	}
	for _, ev := range stackFrames {
		if ev.File != "" {
			targets = append(targets, ev.File)
		}
	}

	// For compilation-only evidence (no stack frames or test files),
	// extract file references from the error output
	if len(targets) == 0 {
		compEv := e.Evidence.ByCategory(ErrCatCompilation)
		for _, ev := range compEv {
			file := extractFileFromCompilationError(ev.Content)
			if file != "" {
				targets = append(targets, file)
			}
		}
	}

	if len(targets) > 0 && e.retriever != nil {
		for _, target := range unique(targets) {
			results, err := e.retriever.SearchText(target)
			if err == nil {
				for _, r := range results {
					e.Evidence.AddWithStrategy(EvSourceRipgrep, r.Content,
						r.File, r.Line, r.Confidence, r.Strategy)
				}
			}
		}
	}

	// Target isolation: pinpoint exact file boundary and AST node.
	allEvidence := e.Evidence.All()
	frames := e.parseStackFramesFromEvidence()
	isolated := e.Isolator.IsolateFromEvidence(allEvidence, frames)
	for _, t := range isolated {
		e.Ledger.AddTarget(t)
		e.Evidence.AddWithStrategy(EvSourceRead, fmt.Sprintf("%s (%s) at %s:%d", t.Node, t.Kind, t.File, t.Line),
			t.File, t.Line, 0.8, "isolator.node")
	}

	// TASK 1 (build-freeze fix): even on a dependency/compilation short-circuit,
	// resolve exact file:line:col coordinates directly from the raw diagnostic
	// output so the ledger never ends up empty. Written synchronously here,
	// before the engine finishes.
	for _, ev := range e.Evidence.All() {
		for _, t := range ParseCompilerTargets(ev.Content) {
			e.Ledger.AddTarget(t)
		}
	}

	// If compilation blocker is detected and there's also environment evidence,
	// short-circuit the loop — don't go back to Hypothesize, go to Propose.
	if e.Evidence.HasCategory(ErrCatCompilation) && e.Evidence.HasCategory(ErrCatEnvironment) {
		return e.State.Transition(StatePropose)
	}

	return e.State.Transition(StateHypothesize)
}

func extractFileFromCompilationError(content string) string {
	idx := strings.Index(content, ".go:")
	if idx < 0 {
		return ""
	}
	start := idx
	for start > 0 && content[start] != ' ' && content[start] != '\n' && content[start] != '\t' {
		start--
	}
	if content[start] == ' ' || content[start] == '\n' || content[start] == '\t' {
		start++
	}
	end := idx + 4
	for end < len(content) && content[end] >= '0' && content[end] <= '9' {
		end++
	}
	return content[start:end]
}

func (e *Engine) parseStackFramesFromEvidence() []StackFrame {
	stackEvidence := e.Evidence.BySource(EvSourceStack)
	var allLines string
	for _, ev := range stackEvidence {
		allLines += ev.Content + "\n"
	}
	return ParseStackFrames(allLines)
}

func (e *Engine) stateVerify() error {
	if e.executor != nil {
		summary, _ := e.TestLoop.Run(e.executor, testLoopConfig{Strategy: "all"})
		if summary != nil {
			output := BoundedLogPreprocessor(summary.Output)
			summary.Output = output
			e.Evidence.Add(EvSourceTest, output, summary.Package, 0, 0.5)
			e.Evidence.Add(EvSourceExecution, fmt.Sprintf("Verify: %d passed, %d failed",
				summary.PassedN, summary.FailedN), "", 0, 0.6)

			if summary.Passed && summary.FailedN == 0 {
				_ = e.State.Transition(StatePropose)
				return nil
			}
		}
	}

	currentLoop := e.State.IterationCount()
	if currentLoop >= hardLoopCeiling {
		return e.State.Transition(StatePropose)
	}

	return e.State.Transition(StateHypothesize)
}

func (e *Engine) statePropose() error {
	if e.Result == nil {
		e.Result = &InvestigationResult{Problem: e.Problem}
	}

	result := e.Hypotheses.Best()
	e.Ledger.Problem = e.Problem
	e.Ledger.Source = "investigate"

	if result != nil {
		e.Result.Conclusion = result.Theory
		e.Result.Resolved = true
		e.Result.RootCause = deriveRootCause(e.Result)
		e.Ledger.SetRootCause(e.Result.RootCause)
		e.Ledger.SetConclusion(result.Theory, true)

		confirmed := e.Hypotheses.Confirmed()
		if len(confirmed) > 1 {
			parts := make([]string, 0, len(confirmed))
			for _, h := range confirmed {
				parts = append(parts, h.Theory)
			}
			e.Result.Conclusion = strings.Join(parts, "; ")
			e.Result.RootCause = deriveRootCause(e.Result)
			e.Ledger.SetRootCause(e.Result.RootCause)
			e.Ledger.SetConclusion(e.Result.Conclusion, true)
		}
	} else {
		e.Result.RootCause = "no root cause identified — investigation exhausted"
		e.Result.Conclusion = "investigation exhausted — no hypothesis confirmed"
		e.Result.Resolved = false
		e.Ledger.SetRootCause(e.Result.RootCause)
		e.Ledger.SetConclusion(e.Result.Conclusion, false)
	}
	return e.State.Transition(StateDone)
}

// deriveRootCause extracts a root cause description from the investigation result.
// It synthesizes evidence and targets into a concise root cause statement.
// This is the ONLY structural mutation /investigate performs — atomic task
// generation is strictly forbidden and is exclusively owned by /plan.
func deriveRootCause(r *InvestigationResult) string {
	if r == nil {
		return ""
	}

	var blockerCause string
	var otherCauses []string
	for _, h := range r.Hypotheses {
		if h.Status == HypothesisConfirmed && h.IsBlocker {
			blockerCause = h.Theory
		} else if h.Status == HypothesisConfirmed {
			otherCauses = append(otherCauses, h.Theory)
		}
	}

	var cause string
	switch {
	case blockerCause != "" && len(otherCauses) > 0:
		cause = fmt.Sprintf("BLOCKER: %s; additionally: %s", blockerCause, strings.Join(otherCauses, "; "))
	case blockerCause != "":
		cause = blockerCause
	case r.Conclusion != "":
		cause = r.Conclusion
	case len(otherCauses) > 0:
		cause = strings.Join(otherCauses, "; ")
	}

	if cause != "" && len(r.Proximity) > 0 {
		p := r.Proximity[0]
		cause += fmt.Sprintf(" (located at %s:%d)", p.File, p.Line)
	}
	if cause == "" {
		cause = "unable to determine root cause"
	}
	return cause
}

func (e *Engine) FormatLedgerForPlan() string {
	e.Ledger.Problem = e.Problem
	e.Ledger.Source = "investigate"
	if e.Result != nil {
		e.Ledger.SetRootCause(e.Result.RootCause)
		e.Ledger.SetConclusion(e.Result.Conclusion, e.Result.Resolved)
	}
	return e.Ledger.FormatForPlan()
}

func (e *Engine) extractSymbolsFromProblem() []string {
	var symbols []string
	words := strings.Fields(e.Problem)
	for _, w := range words {
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		if isLikelySymbol(w) {
			symbols = append(symbols, w)
		}
	}
	return symbols
}

func (e *Engine) extractTextTermsFromProblem() []string {
	var terms []string
	words := strings.Fields(e.Problem)
	for _, w := range words {
		w = strings.TrimSpace(w)
		if w == "" {
			continue
		}
		if len(w) > 3 {
			terms = append(terms, w)
		}
	}
	return terms
}

func isLikelySymbol(s string) bool {
	if len(s) < 2 {
		return false
	}
	if strings.Contains(s, ".") || strings.Contains(s, "/") {
		return true
	}
	upper := 0
	for _, c := range s {
		if c >= 'A' && c <= 'Z' {
			upper++
		}
	}
	return upper > 0
}

func summarizeEvidence(evidence []Evidence) string {
	if len(evidence) == 0 {
		return "no evidence"
	}
	var parts []string
	for _, ev := range evidence[:min(3, len(evidence))] {
		content := ev.Content
		if len(content) > 80 {
			content = content[:80] + "..."
		}
		parts = append(parts, content)
	}
	return strings.Join(parts, "; ")
}
