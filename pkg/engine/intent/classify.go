package intent

import "strings"

// keyword signal sets are the deterministic, no-LLM classifier used by
// Classify. Order matters: Classify scans from the most specific family
// (greenfield) toward the most general, and the first signal set that fires
// wins.
var (
	greenfieldSignals = []string{
		"generate", "create new project", "from scratch", "scaffold",
		"new project", "create a project", "create project", "start a new",
		"setup a new", "bootstrap", "create app", "new app", "new service",
		"initialize", "initialise", "init repo",
	}

	featureSignals = []string{
		"add feature", "new feature", "implement", "add support",
		"add endpoint", "add route", "add handler", "introduce",
		"extend to", "wire up", "build a", "build the", "add a",
	}

	bugFixSignals = []string{
		"bug", "crash", "panic", "stack trace", "traceback", "segfault",
		"nil pointer", "deadlock", "exception", "failing test", "test failure",
		"regression", "compile error", "compiler error", "undefined symbol",
		"not working", "doesn't work", "broken", "race condition", "fix",
	}

	refactorSignals = []string{
		"refactor", "restructure", "reorganize", "reorganise", "rename",
		"split", "extract", "move", "migrate", "simplify", "deduplicate",
		"clean up", "cleanup", "tidy up", "modularize", "modularise",
		"decouple", "isolate",
	}

	archSignals = []string{
		"architecture", "overview", "system design", "layers",
		"dependency graph", "dependency direction", "call graph", "route",
		"routes", "endpoint map", "api surface", "data flow", "control flow",
		"entry point", "how does the system", "module map",
	}

	questionSignals = []string{
		"explain", "what does", "what is", "what are", "why", "how does",
		"walk me through", "describe", "understand", "purpose", "meaning",
		"tell me about", "break down", "what's the difference",
	}
)

// Classify maps a raw user prompt onto an Intent. It is deterministic and
// requires no model: it scores the input against per-family keyword signals
// and picks the dominant match, then derives boolean facets from the chosen
// family (mutating families get Mutates/RunsTools/RequiresTest; read-only
// families get ReadOnly). An empty input yields the General family with no
// facets.
func Classify(raw string) Intent {
	lower := strings.ToLower(strings.TrimSpace(raw))

	switch {
	case lower == "":
		return Intent{family: FamilyGeneral, facets: map[Facet]bool{}}
	case hits(lower, greenfieldSignals) >= 1 || isGreenfieldWeb(lower):
		return derived(FamilyGreenfield)
	case hits(lower, bugFixSignals) >= 1:
		return derived(FamilyBugFix)
	case hits(lower, archSignals) >= 1:
		return derived(FamilyArchitecture)
	case hits(lower, refactorSignals) >= 1:
		return derived(FamilyRefactor)
	case hits(lower, featureSignals) >= 1:
		return derived(FamilyFeature)
	case hits(lower, questionSignals) >= 1:
		return derived(FamilyQuestion)
	default:
		return Intent{family: FamilyGeneral, facets: map[Facet]bool{}}
	}
}

// derived builds a fully-faceted intent for a family. It cannot fail because
// the family is always one of the defined constants.
func derived(f Family) Intent {
	in, err := New(f)
	if err != nil {
		panic(err) // unreachable: f is always a valid constant
	}
	return in
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

// greenfieldWebVerbs are generation verbs whose presence alongside a web
// noun marks a greenfield website request ("make a website introducing X").
var greenfieldWebVerbs = []string{
	"make ", "make the ", "make a ", "make an ",
	"create ", "create the ", "create a ", "create an ",
	"build ", "build the ", "build a ", "build an ",
	"design ", "design the ", "design a ", "design an ",
	"generate ", "generate the ", "generate a ", "generate an ",
	"write ", "write the ", "write a ", "write an ",
	"scaffold ", "new website", "new web page", "new webpage",
}

// webNouns are the nouns that identify a static-website target.
var webNouns = []string{
	"website", "web page", "webpage", "web site",
	"landing page", "portfolio page", "personal page",
}

// isGreenfieldWeb reports whether a prompt requests generating a website
// from scratch. The noun alone ("explain how websites work") is not enough;
// a generation verb must be present so refactor/explain requests stay in
// their own families.
func isGreenfieldWeb(lower string) bool {
	hasNoun := false
	for _, n := range webNouns {
		if strings.Contains(lower, n) {
			hasNoun = true
			break
		}
	}
	if !hasNoun {
		return false
	}
	for _, v := range greenfieldWebVerbs {
		if strings.Contains(lower, v) {
			return true
		}
	}
	return false
}
