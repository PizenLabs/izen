package planner

import "strings"

// Intent is the primary classification of a user request. It selects which
// context engines are queried and how the assembled token budget is split
// between them (see allocationFor).
type Intent string

const (
	// IntentBugFix prioritizes panic/error logs (Phase 1 Tee logs), Lea
	// call-chain reconstructions, and localized bug snippets.
	IntentBugFix Intent = "BUG_FIX"
	// IntentArchitecture prioritizes the Lea architecture summary and route
	// map, strictly ignoring verbose file implementations.
	IntentArchitecture Intent = "ARCHITECTURE_QUESTION"
	// IntentRefactor prioritizes Lea call trees, interface implementations,
	// and target struct/function definitions.
	IntentRefactor Intent = "REFACTOR"
	// IntentExplanation answers "what does X do / why is Y" questions with a
	// balanced budget across files and symbols.
	IntentExplanation Intent = "EXPLANATION"
	// IntentGeneral is the fallback for inputs with no dominant signal; it
	// uses the same balanced budget as EXPLANATION.
	IntentGeneral Intent = "GENERAL"
)

// String returns the stable serialized form of the intent.
func (i Intent) String() string { return string(i) }

// keyword sets are the deterministic, no-LLM signal used by ClassifyIntent.
// Order matters: classifyIntentScore scans from the most specific (BUG_FIX)
// toward the most general, and the first signal set that fires wins.
var (
	bugFixSignals = []string{
		"panic", "crash", "stack trace", "traceback", "segfault", "nil pointer",
		"deadlock", "exception", "failing test", "test failure", "test fail",
		"regression", "compile error", "compiler error", "error log", "error message",
		"debug", "why does it fail", "why is it failing", "bug report", "stack overflow",
	}

	archSignals = []string{
		"architecture", "overview", "system design", "layers", "layer",
		"dependency graph", "dependency direction", "call graph", "route",
		"routes", "endpoint", "endpoints", "api surface", "data flow",
		"control flow", "entry point", "architecture question",
	}

	// weakArchSignals are structural nouns that only imply an architecture
	// question when framed as one (paired with an explanation/question verb).
	weakArchSignals = []string{
		"module", "component", "package structure", "package layout",
		"how the system", "how does the system",
	}

	refactorSignals = []string{
		"refactor", "restructure", "reorganize", "reorganise", "rename", "split",
		"extract", "move", "migrate", "simplify", "deduplicate", "clean up",
		"cleanup", "tidy up", "modularize", "modularise", "decouple", "isolate",
		"interface", "abstraction", "separate concerns",
	}

	explainSignals = []string{
		"explain", "what does", "what is", "what are", "why", "how does",
		"walk me through", "describe", "understand", "purpose", "meaning",
		"tell me about", "break down",
	}
)

// ClassifyIntent maps a raw user input onto a primary Intent. It is
// deterministic and requires no LLM dependency: it scores the input against
// per-intent keyword signals and picks the dominant match. Ties resolve in
// favor of the most specific intent (BUG_FIX > ARCHITECTURE > REFACTOR >
// EXPLANATION > GENERAL).
func ClassifyIntent(input string) Intent {
	lower := strings.ToLower(strings.TrimSpace(input))
	if lower == "" {
		return IntentGeneral
	}

	bugHits := hits(lower, bugFixSignals)
	switch {
	case bugHits >= 2 || (bugHits == 1 && hasStrongBugSignal(lower)):
		return IntentBugFix
	case hits(lower, archSignals) >= 1:
		return IntentArchitecture
	case hits(lower, refactorSignals) >= 1:
		return IntentRefactor
	case hits(lower, weakArchSignals) >= 1 && isStructuralQuestion(lower):
		return IntentArchitecture
	case hits(lower, explainSignals) >= 1:
		return IntentExplanation
	default:
		return IntentGeneral
	}
}

// isStructuralQuestion reports whether the input is framed as a structural
// (architecture) question rather than a mutation or casual request. It is the
// gate for weak architecture signals such as "module" or "component".
func isStructuralQuestion(lower string) bool {
	for _, frame := range []string{
		"what are", "what is", "how does", "how is", "show", "describe",
		"overview of", "map of", "where is", "where are",
	} {
		if strings.Contains(lower, frame) {
			return true
		}
	}
	return false
}

// hits counts how many of the given signals appear in the input.
func hits(lower string, signals []string) int {
	n := 0
	for _, s := range signals {
		if strings.Contains(lower, s) {
			n++
		}
	}
	return n
}

// hasStrongBugSignal reports whether the input carries a hard failure marker
// (panic, crash, stack trace, regression...) that outweighs generic
// explanation verbs such as "why" or "debug".
func hasStrongBugSignal(lower string) bool {
	for _, s := range []string{
		"panic", "crash", "stack trace", "traceback", "segfault", "deadlock",
		"nil pointer", "regression", "exception", "failing", "fail",
	} {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}
