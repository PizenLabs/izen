// Package autonomy implements the Izen decision runtime: the layer that sits
// ABOVE the execution modes and answers four questions — what the user wants
// (intent), where the problem is solved (workspace), what actions are allowed
// (capability authority), and whether the runtime can continue without asking
// (autonomy).
//
// The package is intentionally mode-agnostic. A mode is an execution contract;
// an intent is the user's goal. Classifying intent independently from workspace
// selection is the core separation this package enforces.
//
// Dependency rule: the package imports the standard library, golang.org/x/net
// (for HTML structural understanding) and internal/events (for observability).
// It never imports modes, router, pipeline, or UI packages.
package autonomy

import (
	"regexp"
	"strings"
)

// Intent is a canonical user-goal category. An intent is NOT an execution mode:
// it describes what the user wants, never where the system solves it.
type Intent string

// Canonical intent categories. Order is meaningful for the deterministic
// classifier: more specific intents are matched before generic ones.
const (
	IntentConversation  Intent = "conversation"
	IntentExplanation   Intent = "explanation"
	IntentInvestigation Intent = "investigation"
	IntentPlanning      Intent = "planning"
	IntentModification  Intent = "modification"
	IntentVerification  Intent = "verification"
	IntentDebugging     Intent = "debugging"
	IntentRefactoring   Intent = "refactoring"
	IntentUnknown       Intent = ""
)

// String returns the canonical intent label.
func (i Intent) String() string {
	if i == "" {
		return "unknown"
	}
	return string(i)
}

// RequiresMutation reports whether acting on the intent writes to the
// workspace (modification, refactoring) as opposed to read-only analysis.
func (i Intent) RequiresMutation() bool {
	return i == IntentModification || i == IntentRefactoring
}

// RequiresWorkspace reports whether acting on the intent needs an execution
// workspace at all. Conversation is answered directly: no workspace, no
// timeline, no evidence collection.
func (i Intent) RequiresWorkspace() bool {
	return i != IntentConversation && i != IntentUnknown
}

// Capability is a single permission bit for the autonomy decision model. It
// names what the runtime may do in a workspace, independent of any mode.
type Capability string

const (
	CapRead    Capability = "read"
	CapAnalyze Capability = "analyze"
	CapPropose Capability = "propose"
	CapMutate  Capability = "mutate"
	CapVerify  Capability = "verify"
)

// CapabilitySet is a compact collection of granted/required capabilities.
type CapabilitySet []Capability

// Has reports whether the set contains cap.
func (cs CapabilitySet) Has(cap Capability) bool {
	for _, c := range cs {
		if c == cap {
			return true
		}
	}
	return false
}

// RequiresMutate reports whether the set demands the mutation capability.
func (cs CapabilitySet) RequiresMutate() bool { return cs.Has(CapMutate) }

// String joins the capabilities in canonical order.
func (cs CapabilitySet) String() string {
	var order = []Capability{CapRead, CapAnalyze, CapPropose, CapMutate, CapVerify}
	parts := make([]string, 0, len(cs))
	for _, c := range order {
		if cs.Has(c) {
			parts = append(parts, string(c))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "+")
}

// RequiredCapabilities returns the capability vector an intent requires to be
// acted on. A mode is never consulted: the vector is a pure function of intent.
func RequiredCapabilities(i Intent) CapabilitySet {
	switch i {
	case IntentConversation:
		return CapabilitySet{}
	case IntentExplanation:
		return CapabilitySet{CapRead}
	case IntentInvestigation, IntentDebugging:
		return CapabilitySet{CapRead, CapAnalyze}
	case IntentPlanning:
		return CapabilitySet{CapRead, CapAnalyze, CapPropose}
	case IntentVerification:
		return CapabilitySet{CapRead, CapAnalyze, CapVerify}
	case IntentModification:
		return CapabilitySet{CapRead, CapAnalyze, CapPropose, CapMutate}
	case IntentRefactoring:
		return CapabilitySet{CapRead, CapAnalyze, CapPropose, CapMutate}
	default:
		return CapabilitySet{}
	}
}

// IntentResult is the outcome of classifying a raw prompt: the canonical
// intent category, a normalized confidence, the resolved target references
// (e.g. @index.html), the required capability vector, and a justification.
// It carries the user's goal ONLY — never an execution plan or a mode.
type IntentResult struct {
	Intent      Intent
	Confidence  float64
	Targets     []string
	Required    CapabilitySet
	Explanation string
}

// Target returns the first resolved target, or "".
func (r IntentResult) Target() string {
	if len(r.Targets) == 0 {
		return ""
	}
	return r.Targets[0]
}

// RequiresMutation reports whether acting on the classified intent writes to
// the workspace.
func (r IntentResult) RequiresMutation() bool {
	return r.Intent.RequiresMutation()
}

// HasTarget reports whether the classification resolved at least one explicit
// target reference.
func (r IntentResult) HasTarget() bool {
	return len(r.Targets) > 0
}

// ClassifyFunc is the pluggable semantic fallback for intent classification.
// It returns a canonical intent label and a confidence in [0.0, 1.0]. When nil,
// the deterministic classifier is authoritative and confidence is heuristic.
type ClassifyFunc func(input string) (Intent, float64, string)

// targetRefPattern matches an explicit @file or bare file path with a known
// source/document extension. It is deliberately conservative: only concrete,
// targetable references are extracted.
var targetRefPattern = regexp.MustCompile(`(?i)@([a-zA-Z0-9_./\\-]+(?:\.(?:html|htm|css|js|jsx|ts|tsx|go|py|rs|java|c|cc|cpp|h|hpp|md|json|yaml|yml|toml|sql|proto|xml|sh)))`)

var barePathPattern = regexp.MustCompile(`(?i)(?:^|[\s,])([a-zA-Z0-9_./\\-]+\.(?:html|htm|css|js|jsx|ts|tsx|go|py|rs|java|c|cc|cpp|h|hpp|md|json|yaml|yml|toml|sql|proto|xml|sh))`)

// greetings are standalone conversational openers. They classify as
// conversation only when the input is short (≤3 words) or an exact match.
var greetings = []string{
	"hi", "hello", "hey", "yo", "good morning", "good evening", "good afternoon",
}

// conversationPhrases are full conversational statements.
var conversationPhrases = []string{
	"thanks", "thank you", "thank you so much", "how are you",
	"who are you", "what can you do", "bye", "goodbye",
}

// investigationPatterns signal read-only evidence collection: search, trace,
// inspect, analyze — the runtime gathers evidence but never mutates.
var investigationPatterns = []string{
	"inspect", "search for", "trace", "where is", "where are", "find ",
	"locate", "list ", "show me the", "what files", "which file",
	"analyze the codebase", "gather evidence", "collect evidence",
}

// debuggingPatterns signal root-cause analysis of a failure.
var debuggingPatterns = []string{
	"debug", "why is", "why does", "why did", "what caused", "root cause",
	"stack trace", "backtrace", "is broken", "is crashing", "is failing",
	"crash", "panic", "bug", "regression",
}

// verificationPatterns signal checking an existing state. They only win when
// NO mutation-like verb is present: "check @index.html and remove extra
// contents" is a mutation request, not a verification request.
var verificationPatterns = []string{
	"verify", "check ", "validate", "confirm", "is it correct", "does it work",
	"run the tests", "are the tests", "ensure", "make sure",
}

// planningPatterns signal design/architecture work with no execution.
var planningPatterns = []string{
	"plan", "design", "architecture", "how should", "what's the best way",
	"what is the best way", "blueprint", "roadmap", "strategy",
}

// refactoringPatterns signal structural improvement without new behavior.
var refactoringPatterns = []string{
	"refactor", "restructure", "reorganize", "rename ", "extract ",
	"simplify", "clean up", "modernize", "reduce duplication",
}

// modificationPatterns signal a concrete change to the workspace. They are
// matched after diagnostic/verification/planning signals so "why is the build
// failing" never routes to mutation, but "check @index.html and remove extra
// contents" (check + remove) does.
var modificationPatterns = []string{
	"remove ", "delete ", "add ", "create ", "generate ", "implement ",
	"write ", "update ", "modify ", "change ", "fix ", "correct ",
	"edit ", "insert ", "replace ", "rewrite ", "build ",
}

func containsAny(lower string, patterns []string) bool {
	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

func startsWithAny(lower string, patterns []string) bool {
	for _, p := range patterns {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// IsConversation reports whether the input is pure conversational input that
// must be answered directly — never routed through a workspace.
func IsConversation(input string) bool {
	i, _, _ := classifyDeterministic(input)
	return i == IntentConversation
}

// classifyDeterministic is the allocation-cheap fast path of the classifier.
// It inspects syntax/signals only and never invokes the semantic fallback.
func classifyDeterministic(input string) (Intent, float64, string) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return IntentUnknown, 0.0, "empty input"
	}
	lower := strings.ToLower(trimmed)

	// Conversation only fires on short/complete statements: a greeting opener
	// with a long tail ("hi, can you remove the footer") is a task, not chat.
	words := len(strings.Fields(trimmed))
	for _, p := range conversationPhrases {
		if lower == p || strings.HasPrefix(lower, p+" ") {
			return IntentConversation, 0.95, "direct conversational signal"
		}
	}
	if words <= 3 && startsWithAny(lower, greetings) {
		return IntentConversation, 0.95, "direct conversational signal"
	}

	// Mutation verbs dominate read-only investigation phrasing. A request like
	// "inspect and remove redundant content from @index.html" is a MUTATION —
	// "remove" decides the intent, never "inspect". Investigation keeps its
	// read-only classification only when the objective carries no mutation verb
	// (e.g. "inspect @index.html" is pure investigation).
	hasMutationLike := containsAny(lower, modificationPatterns) ||
		containsAny(lower, refactoringPatterns)

	// Investigation signals dominate pure diagnostic phrasing — but only when
	// the objective carries no mutation verb.
	if !hasMutationLike {
		for _, p := range investigationPatterns {
			if strings.Contains(lower, p) {
				return IntentInvestigation, 0.9, "evidence-collection signal"
			}
		}
	}

	// Debugging: failure/crash phrasing is never a mutation request. It stays
	// dominant even when a trailing mutation verb exists ("why is the build
	// failing after the fix") — the question form owns the intent.
	for _, p := range debuggingPatterns {
		if strings.Contains(lower, p) {
			return IntentDebugging, 0.9, "failure root-cause signal"
		}
	}

	// Verification wins only when the input carries no mutation verb.
	if !hasMutationLike && containsAny(lower, verificationPatterns) {
		return IntentVerification, 0.85, "verification signal"
	}

	for _, p := range planningPatterns {
		if strings.Contains(lower, p) {
			return IntentPlanning, 0.85, "architecture/design signal"
		}
	}

	for _, p := range refactoringPatterns {
		if strings.Contains(lower, p) {
			return IntentRefactoring, 0.85, "refactoring signal"
		}
	}

	for _, p := range modificationPatterns {
		if strings.Contains(lower, p) {
			return IntentModification, 0.85, "mutation signal"
		}
	}

	// "Explain X", "what is X", "how does X work" — default to explanation.
	if strings.Contains(lower, "explain") || strings.Contains(lower, "what is") ||
		strings.Contains(lower, "what does") || strings.Contains(lower, "how does") ||
		strings.Contains(lower, "how do") || strings.Contains(lower, "meaning of") {
		return IntentExplanation, 0.8, "explanation signal"
	}

	return IntentExplanation, 0.6, "default read-only interpretation"
}

// Classify runs the deterministic classifier first; when no deterministic
// signal matches it delegates to the injected semantic fallback. It extracts
// target references and derives the required capability vector. The classifier
// is a pure function of the input: it never touches the workspace.
func Classify(input string, semantic ClassifyFunc) IntentResult {
	det, conf, expl := classifyDeterministic(input)

	if det == IntentUnknown && semantic != nil {
		det, conf, expl = semantic(input)
	}

	extracted := extractTargets(input)
	return IntentResult{
		Intent:      det,
		Confidence:  clampConfidence(conf),
		Targets:     extracted,
		Required:    RequiredCapabilities(det),
		Explanation: expl,
	}
}

// extractTargets pulls explicit @file references and bare file paths from the
// input. Deduplicated, preserving first-appearance order.
func extractTargets(input string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, m := range targetRefPattern.FindAllStringSubmatch(input, -1) {
		if len(m) > 1 {
			t := strings.TrimSuffix(strings.TrimSpace(m[1]), ",")
			if t != "" && !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	for _, m := range barePathPattern.FindAllStringSubmatch(input, -1) {
		if len(m) > 1 {
			t := strings.TrimSuffix(strings.TrimSpace(m[1]), ",")
			if t != "" && !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out
}

func clampConfidence(c float64) float64 {
	if c < 0.0 {
		return 0.0
	}
	if c > 1.0 {
		return 1.0
	}
	return c
}

// ParseIntent parses a canonical intent label back into an Intent. Unknown or
// empty labels yield IntentUnknown.
func ParseIntent(s string) Intent {
	switch Intent(strings.ToLower(strings.TrimSpace(s))) {
	case IntentConversation, IntentExplanation, IntentInvestigation,
		IntentPlanning, IntentModification, IntentVerification,
		IntentDebugging, IntentRefactoring:
		return Intent(strings.ToLower(strings.TrimSpace(s)))
	default:
		return IntentUnknown
	}
}
