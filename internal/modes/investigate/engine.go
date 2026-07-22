package investigate

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/retrieval"
)

// forensicLog is the activity sink for /investigate. It defaults to the
// standard logger so forensic execution is always observable; the UI can
// redirect it via SetForensicLog to surface it in the session activity stream.
var forensicLog = log.Printf

// SetForensicLog overrides the forensic activity sink.
func SetForensicLog(fn func(format string, args ...interface{})) {
	if fn != nil {
		forensicLog = fn
	}
}

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

	// forensicsRan records whether the engine actually invoked the diagnostic
	// toolchain (test executor and/or retriever searches) during this run. It
	// is the guard against the short-circuit that produced 0s durations.
	forensicsRan bool

	// runCtx is the parent context for this investigation run, threaded into
	// the mandatory forensic probe so its deadline is inherited (and so the
	// context-aware executor call satisfies static analysis).
	runCtx context.Context

	provider ai.Provider
	model    string

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
	// Score is the raw BM25 relevance score from the LX Rust daemon.
	// Populated only for LX-tier results; zero for other tiers.
	Score float64 `json:"score,omitempty"`
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
		runCtx:     context.Background(),
		retriever:  retriever,
		executor:   executor,
	}
}

// NewEngineWithAI constructs an investigate engine wired to the AI orchestrator.
// The provider/model power the dispatch classifier and the $diagnose fallback.
// When provider is nil the engine falls back to offline heuristic routing.
func NewEngineWithAI(root, problem string, retriever Retriever, executor TestExecutor, provider ai.Provider, model string) *Engine {
	eng := NewEngine(root, problem, retriever, executor)
	eng.provider = provider
	eng.model = model
	return eng
}

func (e *Engine) Run() (*InvestigationResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	return e.RunContext(ctx)
}

func (e *Engine) RunContext(ctx context.Context) (*InvestigationResult, error) {
	e.runCtx = ctx

	// Wipe stale conclusion caches from any prior (possibly broken) run so the
	// plan handoff can never inherit leftover REMOTE DEPENDENCY BLOCKER tokens
	// or other ghost state from a previous cycle.
	e.Ledger.Conclusion = ""
	if e.Result != nil {
		e.Result.Conclusion = ""
	}

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

		if err := e.executeCurrentState(ctx); err != nil {
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

	// Inject the REMOTE DEPENDENCY BLOCKER token into every conclusion layer
	// that FormatLedgerForPlan or downstream serializers may read. Each call
	// scans raw e.Ledger.Diagnostics directly rather than relying on the
	// high-level conclusion strings being already populated — this guarantees
	// the blocker is inserted on the very first investigation pass.
	injectDependencyBlocker(e, &e.Ledger.Conclusion)
	injectDependencyBlocker(e, &e.Result.Conclusion)
	injectDependencyBlocker(e, &result.Conclusion)

	// MANDATORY FORENSIC EVIDENCE: /investigate MUST have actually executed the
	// diagnostic toolchain (LX/graph search and/or the test shell) before it is
	// allowed to declare completion. If neither tool ran, force a diagnostic
	// shell probe of the project so the engine never short-circuits with a 0s
	// "no findings" result. This guarantees the developer-visible duration is
	// real and that tool usage is on the record.
	if !e.forensicsExecuted() {
		forensicLog("[forensic] no diagnostic toolchain executed — forcing probe")
		e.forceProbe(ctx)
		result.Duration = time.Since(e.startedAt).Round(time.Millisecond).String()
	}

	// MANDATORY TIMING LOG: prove the forensic pass actually did work.
	forensicLog("Forensic analysis executed in %s (%d evidence, %d loops)",
		result.Duration, len(result.Evidence), result.Loops)

	e.Result = result
	return result, nil
}

func (e *Engine) executeCurrentState(ctx context.Context) error {
	switch e.State.Current() {
	case StateObserve:
		return e.stateObserve(ctx)
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

// forensicsExecuted reports whether this run actually invoked the diagnostic
// toolchain (test executor or any retriever search) rather than merely reasoning
// over the initial problem statement.
func (e *Engine) forensicsExecuted() bool {
	return e.forensicsRan
}

// forceProbe performs a guaranteed diagnostic shell invocation (go test) so the
// engine can never declare "Investigation complete" in 0s with no tool usage.
// It is the last-resort forensic path when both the retriever and the primary
// executor were unavailable during the state machine.
func (e *Engine) forceProbe(ctx context.Context) {
	probe := NewShellTestExecutor(e.root)
	summary, err := probe.RunAllTestsContext(ctx)
	if err == nil && summary != nil {
		e.forensicsRan = true
		if summary.Output != "" {
			e.Ledger.SetDiagnostics(summary.Output)
		}
		output := BoundedLogPreprocessor(summary.Output)
		summary.Output = output
		e.Evidence.Add(EvSourceTest, output, summary.Package, 0, 0.5)
		if !summary.Passed {
			e.Evidence.Add(EvSourceTest,
				fmt.Sprintf("Failed tests: %s", strings.Join(summary.Failed, ", ")),
				summary.Package, 0, 0.7)
		}
	}
}

func (e *Engine) stateObserve(ctx context.Context) error {
	observed := fmt.Sprintf("Observing problem: %s", e.Problem)
	e.Evidence.Add(EvSourceUser, observed, "", 0, 0.2)

	if e.executor != nil {
		e.forensicsRan = true
		summary, _ := e.TestLoop.Run(e.executor, testLoopConfig{Strategy: "all"})
		if summary != nil {
			rawOutput := summary.Output
			if rawOutput != "" {
				e.Ledger.SetDiagnostics(rawOutput)
			}
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

	// ── AI ORCHESTRATOR DISPATCH ───────────────────────────────────────
	// Replace blind token spamming with a single, focused tool chain derived
	// from the failure signature. Strict fallback guarantees ≤3 actions and
	// always terminates at $diagnose rather than retrying broken tokens.
	e.dispatchForensics(ctx)

	return e.State.Transition(StateHypothesize)
}

// dispatchForensics runs the AI-orchestrated forensic chain. It classifies the
// failure log, runs the chosen tool, then — on any hard failure — instantly
// aborts that path and falls back along the strict chain (lx→trace→env→diagnose).
// The entire chain is capped at MaxActionsPerRun to honor the 2-5s budget and
// the "never spam more than 3 actions" contract.
func (e *Engine) dispatchForensics(ctx context.Context) {
	e.forensicsRan = true

	diagnostics := e.Ledger.Diagnostics
	if diagnostics == "" {
		diagnostics = e.Problem
	}

	dctx, dcancel := boundedDispatchCtx(ctx)
	defer dcancel()

	// DispatchStrategy selects the tool but its Rationale field is NOT logged —
	// it is a pre-decision classification label, not a verified fact.
	// Actual outcomes are logged after execution below.
	strategy := DispatchStrategy(dctx, e.provider, e.model, diagnostics)
	dispatchLog("[orchestrator] classify -> tool=%s target=%q",
		strategy.Tool, strategy.Target)

	runner := NewToolRunner(e.root, e.provider, e.model, e.retriever, diagnostics)

	actions := 0
	current := strategy.Tool
	target := strategy.Target

	// If the orchestrator returned no explicit target, try to recover one from
	// a concrete file:line coordinate already present in the diagnostics (e.g.
	// a compiler error "cmd/api/main.go:7:5"). This never spawns LX — it only
	// reuses coordinates the diagnostics already contain.
	if target == "" {
		if ct := ParseCompilerTargets(diagnostics); len(ct) > 0 {
			target = ct[0].File
		}
	}

	// Sanitize the target: strip any :line:col suffix from compiler error
	// coordinates (e.g., "cmd/api/main.go:24" → "cmd/api/main.go") so the
	// tool runner never attempts to open a file whose name includes ":24".
	if target != "" {
		if clean, _ := retrieval.SplitTargetPath(target); clean != "" {
			target = clean
		}
	}

	for current != "" && actions < MaxActionsPerRun {
		actions++
		dispatchLog("[orchestrator] action %d/%d -> %s (target=%q)",
			actions, MaxActionsPerRun, current, target)

		res := runner.Run(dctx, current, target)
		if res.Ok {
			e.ingestToolResult(res)
			// Log actual outcome based on tool + evidence.
			// This is the ONLY place where outcome facts are recorded — no
			// pre-decision rationales leak into the log.
			e.logToolOutcome(current, res)
			// The chosen tool succeeded — the chain is complete.
			return
		}

		// Strict fallback: drop this path, never retry the broken token.
		// Log explicitly which tool failed and which fallback is next so the
		// operator sees the chain (violates "silent" — principle 2: Explicit Over Implicit).
		next := nextFallback(current)
		if next != "" {
			forensicLog("[orchestrator] %s failed → falling back to %s", current, next)
		} else {
			forensicLog("[orchestrator] %s failed → chain exhausted (terminal)", current)
		}
		current = next
	}

	// If we exhausted the chain without a success, still surface whatever the
	// diagnose fallback produced (it always returns Ok=true with raw evidence).
	if current == ToolDiagnose {
		res := runner.Run(dctx, ToolDiagnose, target)
		e.ingestToolResult(res)
		e.logToolOutcome(current, res)
	}
}

// logToolOutcome logs the factual outcome of a forensic tool execution.
// It uses deterministic fields from the ToolResult and Evidence — never
// pre-decision rationales or hardcoded guesswork strings.
func (e *Engine) logToolOutcome(tool Tool, res ToolResult) {
	if !res.Ok {
		forensicLog("[orchestrator] %s returned no results", tool)
		return
	}

	// Extract BM25 scores from evidence for LX tool outcomes.
	if tool == ToolLX {
		maxScore := 0.0
		for _, ev := range res.Evidence {
			if ev.Confidence > maxScore {
				maxScore = ev.Confidence
			}
		}
		if maxScore > 0 {
			forensicLog("[orchestrator] %s succeeded (evidence=%d, max_BM25=%.3f)",
				tool, len(res.Evidence), maxScore)
			if maxScore < 0.3 {
				forensicLog("[lx] BM25=%.3f BELOW THRESHOLD (0.3) for target %q — context may be insufficient for /plan synthesis", maxScore, res.Target)
			} else {
				forensicLog("[lx] BM25=%.3f >= 0.3 for target %q — passing structured context to /plan", maxScore, res.Target)
			}
		} else {
			forensicLog("[orchestrator] %s succeeded (evidence=%d, BM25=0)",
				tool, len(res.Evidence))
		}
	} else {
		forensicLog("[orchestrator] %s succeeded (evidence=%d)",
			tool, len(res.Evidence))
	}
}

// ingestToolResult digests a tool result into the ledger, preserving the full
// payload as monotonic [PKT-N] packets for /plan consumption.
func (e *Engine) ingestToolResult(res ToolResult) {
	for _, ev := range res.Evidence {
		e.Evidence.Add(ev.Source, ev.Content, ev.File, ev.Line, ev.Confidence)
	}
	if res.Content != "" {
		e.Ledger.SetDiagnostics(res.Content)
	}
	e.Ledger.AddTarget(Target{
		File:    res.Target,
		Node:    string(res.Tool),
		Kind:    "tool",
		Snippet: res.Content,
	})
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
	// Forensics are performed EXCLUSIVELY by the AI/heuristic dispatcher in
	// stateObserve (dispatchForensics). This state is intentionally a no-op:
	// the legacy raw-token lx --search/--resolve loops have been removed so the
	// engine never spawns LX for arbitrary log filler (e.g. "Investigate",
	// "cause", "failure:").
	return e.State.Transition(StateGather)
}

func (e *Engine) stateGather() error {
	// e.Result is normally allocated in RunContext before the state loop, but
	// stateGather may be reached via direct state transitions (or a custom
	// entry point) where it is still nil. Lazily initialize it here so we never
	// dereference a nil *InvestigationResult when appending proximity slices.
	if e.Result == nil {
		e.Result = &InvestigationResult{Problem: e.Problem}
	}

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
	// NOTE: no LX brute-force loop here. Forensics are performed exclusively by
	// the AI/heuristic dispatcher (stateObserve -> dispatchForensics). This
	// state only refines already-gathered evidence into precise targets.

	// Target isolation: pinpoint exact file boundary and AST node from the
	// evidence collected by the orchestrator.
	allEvidence := e.Evidence.All()
	frames := e.parseStackFramesFromEvidence()
	isolated := e.Isolator.IsolateFromEvidence(allEvidence, frames)
	for _, t := range isolated {
		e.Ledger.AddTarget(t)
		e.Evidence.AddWithStrategy(EvSourceRead, fmt.Sprintf("%s (%s) at %s:%d", t.Node, t.Kind, t.File, t.Line),
			t.File, t.Line, 0.8, "isolator.node")
	}

	// Resolve exact file:line:col coordinates directly from the raw diagnostic
	// output so the ledger never ends up empty. Reads only, no LX spawn.
	for _, ev := range e.Evidence.All() {
		for _, t := range ParseCompilerTargets(ev.Content) {
			e.Ledger.AddTarget(t)
		}
	}

	// If compilation blocker is detected and there's also environment evidence,
	// short-circuit — go straight to Propose.
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

// extractPackageName extracts the Go module/package path from a Go compilation
// error of the form "no required module provides package <PKG>".
// Returns the fully-qualified package path or empty string.
//
// It also trims trailing characters like colons, commas, and semicolons that
// may appear after the package path in compiler output.
func extractPackageName(rawError string) string {
	needle := "no required module provides package "
	idx := strings.Index(rawError, needle)
	if idx < 0 {
		needle = "cannot find module providing package "
		idx = strings.Index(rawError, needle)
	}
	if idx < 0 {
		return ""
	}
	rest := rawError[idx+len(needle):]
	end := strings.IndexAny(rest, " \t\n\r")
	if end < 0 {
		end = len(rest)
	}
	pkg := rest[:end]
	// Trim common trailing punctuation (colon, comma, semicolon) that some
	// Go compiler versions or build tools append after the package path.
	pkg = strings.TrimRight(pkg, ":;,.")
	return strings.TrimSpace(pkg)
}

// injectDependencyBlocker scans the raw diagnostics for Go dependency errors
// and injects the exact package path directly into the target conclusion pointer
// using the canonical "lx bypassed" token format that /plan recognises.
// It scans e.Ledger.Diagnostics directly (the raw compiler/test output) rather
// than depending on high-level conclusion strings being already populated, so
// the REMOTE DEPENDENCY BLOCKER token is guaranteed on the very first pass.
func injectDependencyBlocker(e *Engine, targetConclusion *string) {
	raw := e.Ledger.Diagnostics
	if raw == "" {
		return
	}
	if !strings.Contains(raw, "no required module provides package") &&
		!strings.Contains(raw, "cannot find module providing package") {
		return
	}
	if strings.Contains(*targetConclusion, "REMOTE DEPENDENCY BLOCKER") {
		return
	}
	pkg := extractPackageName(raw)
	if pkg == "" {
		return
	}
	blocker := fmt.Sprintf("## REMOTE DEPENDENCY BLOCKER (lx bypassed): %s", pkg)
	if *targetConclusion != "" {
		*targetConclusion += "\n" + blocker
	} else {
		*targetConclusion = blocker
	}
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
