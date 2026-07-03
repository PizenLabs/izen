package investigate

import (
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

	Problem   string
	root      string
	startedAt time.Time
	Result    *InvestigationResult

	retriever Retriever
	executor  TestExecutor
}

type InvestigationResult struct {
	Problem    string           `json:"problem"`
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
		Problem:    problem,
		root:       root,
		startedAt:  time.Now(),
		retriever:  retriever,
		executor:   executor,
	}
}

func (e *Engine) Run() (*InvestigationResult, error) {
	result := &InvestigationResult{
		Problem: e.Problem,
	}

	for !e.State.ShouldStop() {
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
		active := e.Hypotheses.Active()
		if len(active) > 0 {
			last := active[len(active)-1]
			e.Hypotheses.UpdateStatus(last.ID, HypothesisConfirmed)
			result.Resolved = true
			result.Conclusion = last.Theory
		} else {
			all := e.Hypotheses.All()
			if len(all) > 0 {
				last := all[len(all)-1]
				e.Hypotheses.UpdateStatus(last.ID, HypothesisConfirmed)
				e.Hypotheses.UpdateConfidence(last.ID, 0.6)
				result.Resolved = true
				result.Conclusion = last.Theory
			} else {
				result.Conclusion = "investigation exhausted — no hypothesis confirmed"
			}
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

	var hypothesisText string
	switch {
	case len(evidence) == 0:
		hypothesisText = "No initial evidence found. Need to gather more information."
	case e.State.IterationCount() == 0:
		hypothesisText = fmt.Sprintf("Based on %d pieces of evidence, the issue may be related to: %s",
			len(evidence), summarizeEvidence(evidence))
	default:
		best := e.Hypotheses.Best()
		if best != nil && best.Status == HypothesisRejected {
			hypothesisText = fmt.Sprintf("Previous hypothesis rejected. New theory: %s",
				summarizeEvidence(evidence))
		} else {
			hypothesisText = fmt.Sprintf("Refining hypothesis. Evidence count: %d. %s",
				len(evidence), summarizeEvidence(evidence))
		}
	}

	h := e.Hypotheses.Add(hypothesisText)
	for _, ev := range evidence {
		e.Hypotheses.LinkEvidence(h.ID, ev.ID)
	}

	return e.State.Transition(StateSearch)
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

	return e.State.Transition(StateHypothesize)
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
				e.State.Transition(StatePropose)
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
	result := e.Hypotheses.Best()
	if result != nil {
		e.Result.Conclusion = result.Theory
		e.Result.Resolved = true
	}
	return e.State.Transition(StateDone)
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
