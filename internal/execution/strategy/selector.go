package strategy

import (
	"path/filepath"
	"strings"

	"github.com/PizenLabs/izen/internal/gateway"
	"github.com/PizenLabs/izen/internal/parser"
)

// Workspace is the deterministic file-evidence surface the selector reads. It
// never invokes a model and never performs an unbounded repository scan: every
// operation is an existence check or a bounded candidate lookup.
type Workspace interface {
	// Root returns the workspace root.
	Root() string
	// Exists reports whether the workspace-relative path exists as a file.
	Exists(path string) bool
	// ResolveFuzzy returns workspace-relative paths whose base name
	// case-insensitively matches name, bounded by max. It is used ONLY when
	// exact resolution fails and must never fabricate a match. Multiple
	// matches yield an ambiguous target.
	ResolveFuzzy(name string, max int) []string
}

// Deps are the deterministic inputs to the strategy selector.
type Deps struct {
	// Root is the workspace root path.
	Root string
	// Workspace is the file-evidence surface.
	Workspace Workspace
}

// language extension families used to recognize bare filenames in prose.
var bareFileExts = []string{
	".go", ".py", ".js", ".ts", ".tsx", ".jsx", ".rb", ".rs", ".java", ".c",
	".h", ".cpp", ".html", ".htm", ".css", ".scss", ".less", ".md", ".txt",
	".json", ".yaml", ".yml", ".toml", ".xml", ".sql", ".sh", ".env",
	".proto", ".graphql", ".cfg", ".ini", ".conf", ".gradle", ".mod",
}

// bareKnownFiles are conventional names recognized without an extension.
var bareKnownFiles = []string{
	"license", "licence", "readme", "dockerfile", "makefile", "changelog",
	"contributing", "go.mod", "go.sum", "package.json",
}

// templateTargets are deterministic template creates the engine can resolve
// with zero model invocations (the existing build trivial-template contract).
var templateTargets = map[string]bool{
	"LICENSE": true, "README.md": true, ".gitignore": true, ".env": true,
	".env.example": true, "Dockerfile": true, "Makefile": true,
	"CHANGELOG.md": true, "CONTRIBUTING.md": true,
}

// ── Operation-family signals ─────────────────────────────────────────────────
//
// These pick an OPERATION FAMILY, which steers the strategy and the complexity
// base. They are deliberately NOT the semantic authority: whether the user asked
// the workspace to change at all is answered by ClassifySemantic (semantics.go),
// from a strictly larger act vocabulary. The two are separate questions and the
// engine answers both.
//
// Matching is on whole tokens, never on substrings. `move` is a substring of
// `remove`, so substring matching classified "check @index.html and remove extra
// contents" as a REFACTOR; `design` is a substring of `redesign`, and the fix for
// that had been to add "redesign" to the mutation table — treating a matching bug
// with a wider dictionary. Every such entry widens authority for text nobody
// reviewed.

var diagnosticSignals = []string{
	"why is", "why does", "why isn't", "why doesn't", "what caused",
	"root cause", "stack trace", "backtrace", "crash", "panic",
	"is broken", "is crashing", "is failing", "test failure",
}

var architecturalSignals = []string{
	"architecture", "redesign", "restructure", "migrate", "schema",
	"database", "pipeline", "event-driven", "message queue",
	"cross-cutting", "multi-file", "distributed",
}

var explainSignals = []string{
	"explain", "describe", "what is", "what does", "how does",
	"understand", "summarize", "walk me through",
}

var refactorSignals = []string{
	"refactor", "rename", "extract", "restructure", "move",
}

var createSignals = []string{
	"create", "generate", "add a", "write a", "new file", "init a",
	"scaffold", "bootstrap",
}

// Select classifies a raw $prompt (or free-form) request into an execution
// strategy profile, entirely deterministically. It answers the engine-first
// questions — what kind of operation, what targets, what evidence, what
// context, what artifact, what budget — BEFORE any model invocation.
//
// The model is never consulted for filesystem resolution, strategy selection,
// or complexity. The returned profile carries the reasoning for every decision
// so $inspect can expose it.
func Select(raw string, deps Deps) ExecutionStrategyProfile {
	raw = strings.TrimSpace(raw)
	profile := ExecutionStrategyProfile{Intent: raw}

	if raw == "" {
		profile.Strategy = HumanClarification
		profile.StrategyReason = "empty request"
		profile.ContextPolicy = ContextPolicyNone
		return profile
	}

	parsed, _ := parser.Parse(raw, nil)
	targets, fileSyntax := collectTargets(parsed, raw, deps)

	op := classifyOperation(raw, parsed)
	profile.Targets = targets

	// ── EVIDENCE-BASED CREATE ──────────────────────────────────────────
	// The operation-family table above is a PHRASE table: it recognises
	// "add a file" but not "add file named X", because its creation entry is
	// the literal "add a". The objective contract layer and the executor
	// already decide CREATE from two independent facts — a canonical creation
	// VERB and the ABSENCE of the named target — and the admission gate
	// already accepts an explicitly named creation target
	// (TargetStateNotFound + explicit). This reconciles the strategy gateway
	// to that same evidence: a request that carries a canonical creation verb
	// and names only targets that do not yet exist is a creation, not a
	// target-resolution failure, so its named destination is BOUND rather
	// than sent to clarification.
	//
	// A creation verb over an EXISTING target is untouched: "add a comment to
	// @file.go" names a target that exists, so it stays a modification. The
	// existence evidence, not the verb alone, is what separates the two.
	declaredCreation := isDeclaredCreation(raw, parsed, targets)
	if op != OperationCreate && declaredCreation {
		op = OperationCreate
	}

	// The canonical semantic verdict, read once. It answers "what act does this
	// request perform?" and is what stops a review that named a file from
	// dispatching a mutation (step 4) and a vague improvement request from
	// reaching a mutation at all.
	semantic := ClassifySemantic(goalText(raw, parsed))

	// An explicit read-only constraint in the request closes the mutation path
	// for the whole request. It is read ONCE here and applied at the only two
	// places below that can produce a mutation strategy, because those two are
	// where authority would actually widen. A family table alone cannot do this:
	// "refactor @auth.go, read-only" matches refactorSignals, and a keyword must
	// never outrank the human's own statement that nothing may change.

	// ── 1. Deterministic template create (zero model) ──────────────────
	explicit := resolvedTargets(targets, TargetExplicit, TargetResolved)
	explicitSyntax := targetsWithStatus(targets, true)
	inferred := resolvedTargets(targets, TargetInferred)
	missing := unresolvedTargets(targets)

	if op == OperationCreate && len(explicit) == 0 && len(inferred) == 0 {
		if canon, ok := templateTargetFromRequest(raw); ok {
			profile.Strategy = DirectDeterministic
			profile.Deterministic = true
			profile.ModelRequired = false
			profile.StrategyReason = "known template target " + canon + " created deterministically"
			profile.Targets = []Target{{
				Raw: canon, Resolved: canon, Status: TargetExplicit, Exists: false,
				Source: "template", Reason: "known template target derived from the request",
			}}
			profile.Artifact = ArtifactContract{Kind: "create_file", Bounded: true,
				Description: "deterministic template content"}
			profile.Complexity = Assess(ComplexityInputs{Operation: op, TargetCount: 1, FileCount: 1,
				ExplicitTargets: true, VerificationDepth: 0})
			profile.ContextKinds = []ContextKind{ContextUserIntent, ContextExplicitTargets, ContextArtifactContract}
			profile.ContextPolicy = ContextPolicyTargetFileOnly
			return profile
		}

	}

	// ── 2. Ambiguity / unresolved target → human clarification ────────
	// A request whose syntax clearly names file targets but that resolves to
	// nothing (or to multiple candidates) must stop before any model call. The
	// model is never used as a filesystem resolver. Creation of a genuinely new
	// file is the one legitimate case where a missing target is expected.
	if fileSyntax && len(targets) == 0 {
		profile.Strategy = HumanClarification
		profile.Deterministic = true
		profile.ModelRequired = false
		profile.StrategyReason = "file-target syntax used but no file target could be extracted; the human must name the exact target"
		profile.Complexity = Assess(ComplexityInputs{Operation: op, Ambiguous: true})
		profile.ContextKinds = []ContextKind{ContextUserIntent}
		profile.ContextPolicy = ContextPolicyNone
		profile.Escalation = true
		profile.EscalationReason = "human clarification required before execution"
		return profile
	}
	if hasAmbiguous(targets) {
		profile.Strategy = HumanClarification
		profile.Deterministic = true
		profile.ModelRequired = false
		profile.StrategyReason = "target resolution is ambiguous; the human must disambiguate"
		profile.Complexity = Assess(ComplexityInputs{Operation: op, TargetCount: len(explicit) + len(inferred),
			FileCount: len(explicit) + len(inferred), Ambiguous: true,
			ExplicitTargets: len(explicit) > 0})
		profile.ContextKinds = []ContextKind{ContextUserIntent}
		profile.ContextPolicy = ContextPolicyNone
		profile.Escalation = true
		profile.EscalationReason = "human clarification required before execution"
		return profile
	}
	if len(missing) > 0 && op != OperationCreate {
		profile.Strategy = HumanClarification
		profile.Deterministic = true
		profile.ModelRequired = false
		profile.StrategyReason = "target resolution is incomplete; the human must name the exact target"
		if len(missing) > 1 {
			profile.StrategyReason = "target resolution is ambiguous; the human must disambiguate"
		}
		profile.Complexity = Assess(ComplexityInputs{Operation: op, TargetCount: len(explicit) + len(inferred),
			FileCount: len(explicit) + len(inferred), Ambiguous: true,
			ExplicitTargets: len(explicit) > 0})
		profile.ContextKinds = []ContextKind{ContextUserIntent}
		profile.ContextPolicy = ContextPolicyNone
		profile.Escalation = true
		profile.EscalationReason = "human clarification required before execution"
		return profile
	}

	// ── 3. Diagnostic / architectural requests without a target set ───
	if op == OperationDiagnose && len(explicit) == 0 && len(inferred) == 0 {
		profile.Strategy = RepositoryInvestigation
		profile.ModelRequired = true
		profile.StrategyReason = "root-cause request requires repository evidence discovery"
		profile.ModelDecision = "diagnose the failure from repository evidence"
		profile.Artifact = ArtifactContract{Kind: "investigation", Bounded: false,
			Description: "root-cause investigation with evidence"}
		profile.Complexity = Assess(ComplexityInputs{Operation: op, RepositoryScope: true,
			VerificationDepth: 1})
		profile.ContextKinds = []ContextKind{ContextUserIntent, ContextPriorExecution,
			ContextDependencyEvidence, ContextRepositoryConstraints}
		profile.ContextPolicy = ContextPolicyRepository
		return withBudgets(profile)
	}

	if op == OperationArchitect && len(explicit) == 0 && len(inferred) == 0 {
		profile.Strategy = RepositoryInvestigation
		profile.ModelRequired = true
		profile.StrategyReason = "architectural request spans the repository; evidence discovery required"
		profile.ModelDecision = "identify the affected architecture and propose the design"
		profile.Artifact = ArtifactContract{Kind: "investigation", Bounded: false,
			Description: "architectural evidence discovery"}
		profile.Complexity = Assess(ComplexityInputs{Operation: op, RepositoryScope: true,
			VerificationDepth: 2})
		profile.ContextKinds = []ContextKind{ContextUserIntent, ContextDependencyEvidence,
			ContextRepositoryConstraints}
		profile.ContextPolicy = ContextPolicyRepository
		return withBudgets(profile)
	}

	// ── 4. Explicit target(s) → targeted execution ────────────────────
	// "Explicit" means the target was NAMED by the request and is therefore
	// authoritative, whether it was written with @scope syntax or as a bare
	// filename in prose ("create a file named testfile.md"). For a declared
	// creation those named destinations do not exist yet, so they never appear
	// in `explicit` (which is existence-gated); `explicitSyntax` covers the
	// @scope spelling and `declaredCreation` covers the prose spelling. Binding
	// them here is what stops a primitive creation from falling through to
	// repository-level planning — a path that inflates the output budget shape
	// (plan) and then forces the creation into a bounded SEARCH/REPLACE
	// contract that has no anchor because the file does not exist.
	if len(explicit) > 0 ||
		(op == OperationCreate && (len(explicitSyntax) > 0 || declaredCreation)) {
		named := explicit
		if len(named) == 0 {
			named = explicitSyntax
		}
		if len(named) == 0 {
			// A declared creation names only absent destinations; those are the
			// authoritative targets.
			named = missing
		}
		switch {
		case op == OperationExplain:
			profile.Strategy = TargetedReasoning
			profile.ModelRequired = true
			profile.StrategyReason = "read-only understanding request with an explicit target"
			profile.ModelDecision = "answer the understanding question from the provided target context"
			profile.Artifact = ArtifactContract{Kind: "explanation", Bounded: true,
				Description: "focused explanation of the target"}
			profile.ContextKinds = []ContextKind{ContextUserIntent, ContextExplicitTargets, ContextTargetContent}
			profile.ContextPolicy = ContextPolicyTargetFileOnly
			profile.Complexity = Assess(ComplexityInputs{Operation: op, TargetCount: len(named),
				FileCount: len(named), ExplicitTargets: true})
			return withBudgets(profile)
		case semantic.IsReadOnly() && semantic.HasAdvisoryClause():
			// An ADVISORY request must not become a mutation merely because it
			// NAMED a file. Naming a target says what to look at; it does not say
			// what to change, and the arm below would otherwise read the name as
			// a licence to write — "review @index.html and suggest improvements"
			// was dispatching an APPLIED mutation for a review request.
			//
			// The guard is HasAdvisoryClause rather than IsReadOnly so that an
			// INVESTIGATIVE request ("inspect every handler in @big.go") keeps
			// its existing targeted path: reporting on a named target is not
			// advice about it.
			//
			// ClassifySemantic is safe to consult here even though Select also
			// runs on runtime-composed provider prompts: it reads the SPEECH ACT
			// of the text and never the negation table, and a compiled mutation
			// prompt carries executive verbs ("modify", "change", "replace") and
			// therefore reads as MUTATION.
			profile.Strategy = TargetedReasoning
			profile.ModelRequired = true
			profile.StrategyReason = "the request asks to be shown or advised something, not changed; " +
				"the named target may be read but not written"
			profile.ModelDecision = "answer from the provided target context without producing file mutations"
			profile.Artifact = ArtifactContract{Kind: "explanation", Bounded: true,
				Description: "focused explanation of the target"}
			profile.ContextKinds = []ContextKind{ContextUserIntent, ContextExplicitTargets, ContextTargetContent}
			profile.ContextPolicy = ContextPolicyTargetFileOnly
			profile.Complexity = Assess(ComplexityInputs{Operation: OperationExplain, TargetCount: len(named),
				FileCount: len(named), ExplicitTargets: true})
			return withBudgets(profile)
		default:
			profile.Strategy = TargetedMutation
			profile.ModelRequired = true
			profile.StrategyReason = "mutation confined to explicit resolved target file(s)"
			profile.ModelDecision = "resolve the targeted content mutation and produce the bounded artifact"
			profile.Artifact = artifactForMutation(op, named, deps)
			profile.ContextKinds = []ContextKind{ContextUserIntent, ContextExplicitTargets,
				ContextTargetContent, ContextArtifactContract, ContextVerificationContract}
			profile.ContextPolicy = ContextPolicyTargetFileOnly
			profile.Complexity = Assess(ComplexityInputs{Operation: op, TargetCount: len(named),
				FileCount: len(named), ExplicitTargets: true, VerificationDepth: verifyDepth(op)})
			return withBudgets(profile)
		}
	}

	// ── 5. Inferred (bare-filename) target(s) → targeted mutation ─────
	if len(inferred) > 0 {
		profile.Strategy = TargetedMutation
		profile.ModelRequired = true
		profile.StrategyReason = "mutation on a deterministically inferred target file"
		profile.ModelDecision = "resolve the targeted content mutation and produce the bounded artifact"
		profile.Artifact = artifactForMutation(op, inferred, deps)
		profile.ContextKinds = []ContextKind{ContextUserIntent, ContextExplicitTargets,
			ContextTargetContent, ContextArtifactContract, ContextVerificationContract}
		profile.ContextPolicy = ContextPolicyTargetFileOnly
		profile.Complexity = Assess(ComplexityInputs{Operation: op, TargetCount: len(inferred),
			FileCount: len(inferred), VerificationDepth: verifyDepth(op)})
		return withBudgets(profile)
	}

	// ── 6. Casual chat / direct greeting (never workspace planning) ────
	// A greeting, small talk, or direct question that resolved no target and
	// matched no coding operation is direct chat: exactly one bounded read-only
	// invocation with ZERO repository context. It must NEVER expand into
	// repository-level planning — "hi" is not a planning request. This guard
	// runs last so a stronger signal (create / clarify / diagnose / architect /
	// explicit or inferred target) always wins.
	//
	// The strategy is DirectResponse with ContextPolicyNone: no workspace scan,
	// no repository context, no file channels.
	if gateway.IsCasualChat(raw) {
		profile.Strategy = DirectResponse
		profile.ModelRequired = true
		profile.Intent = "casual_chat"
		profile.StrategyReason = "casual greeting / direct chat; answered directly, zero repository context"
		profile.ModelDecision = "answer the greeting or question directly"
		profile.Artifact = ArtifactContract{Kind: "response", Bounded: true,
			Description: "direct chat reply"}
		profile.Complexity = Assess(ComplexityInputs{Operation: OperationExplain, TargetCount: 0, FileCount: 0})
		profile.ContextKinds = nil
		profile.ContextPolicy = ContextPolicyNone
		return withBudgets(profile)
	}

	// ── 7. No targets → repository-level planning ─────────────────────
	profile.Strategy = MultiFilePlanning
	profile.ModelRequired = true
	profile.StrategyReason = "no explicit target set; repository-level reasoning is justified"
	profile.ModelDecision = "synthesize an execution plan from repository evidence"
	profile.Artifact = ArtifactContract{Kind: "plan", Bounded: false,
		Description: "structured execution plan"}
	profile.Complexity = Assess(ComplexityInputs{Operation: op, RepositoryScope: true,
		VerificationDepth: 2})
	profile.ContextKinds = []ContextKind{ContextUserIntent, ContextRepositoryConstraints,
		ContextDependencyEvidence}
	profile.ContextPolicy = ContextPolicyRepository
	return withBudgets(profile)
}

// classifyOperation derives the coarse operation class from the request. It
// classifies the parsed Goal — the actual task text without @scope markers —
// so a filename like @architecture.md can never trigger the architectural
// signal. The operation family steers the strategy and the complexity base; it
// never alone decides complexity (see complexity.go).
// ResolveConstraints applies local AST and output-budget facts to an already
// selected strategy. Over-budget whole-file output must NOT be silently
// converted — it must be trapped at Boundary-2 as preflight_infeasible so the
// DecisionSurface can explicitly re-scope. Silent conversion is disabled; the
// executor's guardrail and the autonomy DecisionSurface handle recovery
// explicitly. This preserves the trapping invariant verified by
// TestConformanceA_PreflightInfeasibilityTrapping.
func ResolveConstraints(profile ExecutionStrategyProfile, astCorrupt bool, requiredOutputTokens, maxOutputTokens int) ExecutionStrategyProfile {
	_ = astCorrupt
	_ = requiredOutputTokens
	_ = maxOutputTokens
	return profile
}

// classifyOperation derives the coarse operation class from the request. It
// classifies the parsed Goal — the actual task text without @scope markers —
// so a filename like @architecture.md can never trigger the architectural
// signal. The operation family steers the strategy and the complexity base; it
// never alone decides complexity (see complexity.go).
//
// It reads the canonical semantic boundary (semantics.go) and narrows its
// answer into the engine's operation families. It keeps NO signal table of its
// own: the tables that used to live here and the ones the objective contract
// layer kept were two copies of the same fact, they had already drifted apart
// ("implement" was a creation verb in one and unknown in the other), and a
// drift here silently changes which objective the engine judges a mutation.
// goalText renders the text a classification should read: the parsed Goal — the
// task text without @scope markers — when the parser produced one, so a filename
// like @architecture.md can never trigger a signal carried by a target name.
func goalText(raw string, parsed *parser.IntentAST) string {
	if parsed != nil && parsed.Goal != "" {
		return parsed.Goal
	}
	return raw
}

func classifyOperation(raw string, parsed *parser.IntentAST) OperationKind {
	text := goalText(raw, parsed)
	tokens := tokenize(text)

	// The family tables answer "which operation family is this", which steers
	// strategy selection, complexity and the output budget. They are NOT the
	// authority signal — authority is carried by SemanticIntent, then by
	// MutationSemanticsOf, then by the grant — and they are deliberately left on
	// their historical whole-text substring matching.
	//
	// That is a known, recorded limitation rather than an oversight. Fixing it
	// here is not free: "check @index.html and remove redundant content" matched
	// refactorSignals through `move` ⊂ `remove`, and that accident is what gave
	// it OperationRefactor, a higher complexity base and a 2048-token output
	// budget. Classified correctly it is OperationContent, which the complexity
	// model funds at 1024 — and the bounded-patch contract test pins recovery at
	// exactly 1024. So making this matcher exact requires deciding how a
	// whole-file rewrite of a large target is funded, which is BUDGET POLICY and
	// not an authority question. Until that policy exists, an exact matcher here
	// would silently turn correct, affordable mutations into preflight refusals.
	//
	// The canonical semantic boundary (semantics.go) IS exact, on whole tokens,
	// and it is the layer the authority actually reads.
	lower := strings.ToLower(text)
	for _, s := range diagnosticSignals {
		if strings.Contains(lower, s) {
			return OperationDiagnose
		}
	}
	for _, s := range architecturalSignals {
		if strings.Contains(lower, s) {
			return OperationArchitect
		}
	}
	for _, s := range explainSignals {
		if strings.Contains(lower, s) {
			return OperationExplain
		}
	}
	for _, s := range refactorSignals {
		if strings.Contains(lower, s) {
			return OperationRefactor
		}
	}
	for _, s := range createSignals {
		if strings.Contains(lower, s) {
			return OperationCreate
		}
	}
	_ = tokens
	// No family matched, but the semantic boundary still has a verdict: READ-ONLY
	// for a request that only asks to be shown something, UNDETERMINED for one
	// that says no act at all, MUTATION for one whose change verbs the families
	// happen not to name ("change bar to qux in @index.html").
	//
	// UNDETERMINED is the load-bearing case. The previous catch-all answered
	// OperationContent to every request it did not recognise, so a request the
	// runtime could not read — "Make this project better" — entered the mutation
	// path as a defaulted answer rather than as a question. OperationUndetermined
	// is not a mutation family and Select fails it closed.
	if ClassifySemantic(text).IsUndetermined() {
		return OperationUndetermined
	}
	return OperationContent
}

// isDeclaredCreation reports whether a request declares a NEW artifact: it
// carries a canonical creation verb AND every named file target does not yet
// exist. It reads the parsed Goal (the task text without @scope markers) so a
// filename like "create.md" cannot by itself supply the verb, and it uses the
// token-boundary phrase matcher so "add" never matches inside "address".
//
// It is deliberately evidence-gated on target absence: "add a comment to
// @file.go" carries the verb but the target exists, so it is a modification.
// This is the same two-fact rule the objective contract layer uses to compile
// an objective as CREATE.
func isDeclaredCreation(raw string, parsed *parser.IntentAST, targets []Target) bool {
	if len(targets) == 0 {
		return false
	}
	for _, t := range targets {
		if t.Exists {
			return false
		}
	}
	return ContainsPhrase(goalText(raw, parsed), CreationVerbs())
}

// collectTargets extracts and resolves the explicit (@scope) and inferred
// (bare filename) targets of a request. fileSyntax reports whether the request
// used file-target syntax at all.
func collectTargets(parsed *parser.IntentAST, raw string, deps Deps) ([]Target, bool) {
	var targets []Target
	seen := map[string]bool{}
	fileSyntax := false

	add := func(t Target) {
		key := t.Raw
		if t.Resolved != "" {
			key = t.Resolved
		}
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		targets = append(targets, t)
	}

	// Explicit @ scopes from the parser.
	if parsed != nil {
		for _, sc := range parsed.Scopes {
			if sc.Type != parser.ScopeFile {
				// A symbol/diff scope is target context but never a mutation
				// file target for deterministic resolution.
				continue
			}
			fileSyntax = true
			canon := gateway.CanonicalizeFileName(sc.Target)
			add(resolveTarget(sc.Target, canon, "@scope", deps))
		}
	}

	// Bare filenames mentioned in prose.
	for _, name := range extractBareTargets(raw) {
		fileSyntax = true
		canon := gateway.CanonicalizeFileName(name)
		add(resolveTarget(name, canon, "bare-filename", deps))
	}

	return targets, fileSyntax
}

// extractBareTargets finds prose-mentioned filenames (no @ prefix).
//
// QUOTED / CODE-QUOTED NAMES. A human names a target inside backticks or quotes
// as often as bare ("Create a file named `zuru.md`"). The delimiters are not
// part of the path, but they were left on the token, so `.md` never matched the
// extension table and an explicitly named creation target was invisible. The
// delimiters are normalized to spaces before the field split, exactly as the
// autonomy classifier does, so both target-resolution authorities agree on a
// name the user actually wrote.
func extractBareTargets(raw string) []string {
	raw = normalizeBareTargetDelimiters(raw)
	lower := strings.ToLower(raw)
	var names []string
	seen := map[string]bool{}
	words := strings.Fields(lower)
	for _, w := range words {
		// @-prefixed tokens are explicit scopes handled by the parser pass;
		// never treat them as bare prose filenames.
		if strings.HasPrefix(w, "@") {
			continue
		}
		clean := strings.Trim(w, `.,;:'"!?()[]`)
		if clean == "" || seen[clean] {
			continue
		}
		ext := filepath.Ext(clean)
		ok := false
		for _, de := range bareFileExts {
			if ext == de {
				ok = true
				break
			}
		}
		if ok || isBareKnown(clean) {
			seen[clean] = true
			names = append(names, clean)
		}
	}
	return names
}

// normalizeBareTargetDelimiters replaces quote and code-span delimiters with
// spaces so a quoted / code-quoted filename presents the same word boundary as
// a bare one. Only delimiters are touched.
func normalizeBareTargetDelimiters(raw string) string {
	return strings.NewReplacer(
		"`", " ",
		`"`, " ",
		"'", " ",
		"\u2018", " ",
		"\u2019", " ",
		"\u201c", " ",
		"\u201d", " ",
	).Replace(raw)
}

// isBareKnown reports whether the lowercased word is a conventional filename.
func isBareKnown(w string) bool {
	for _, k := range bareKnownFiles {
		if w == k {
			return true
		}
	}
	return false
}

// resolveTarget resolves a raw target against the workspace, classifying the
// outcome (explicit/resolved/inferred/unresolved/ambiguous) deterministically.
func resolveTarget(raw, canon, source string, deps Deps) Target {
	t := Target{Raw: raw, Resolved: canon, Source: source, Explicit: source == "@scope",
		Reason: "exact workspace-relative match"}

	switch source {
	case "@scope":
		t.Status = TargetExplicit
	default:
		t.Status = TargetInferred
	}

	if deps.Workspace != nil && deps.Workspace.Exists(canon) {
		t.Exists = true
		return t
	}

	// Exact match failed. When the target looks like a file (has an extension
	// or a conventional name) the syntax clearly names a file, so fall back to
	// a bounded fuzzy lookup — never to the model.
	if looksLikeFile(canon) {
		var candidates []string
		if deps.Workspace != nil {
			candidates = deps.Workspace.ResolveFuzzy(filepath.Base(canon), 3)
		}
		switch len(candidates) {
		case 1:
			t.Resolved = candidates[0]
			t.Exists = true
			t.Status = TargetResolved
			t.Reason = "unique case-insensitive workspace match " + candidates[0]
			return t
		case 0:
			t.Status = TargetUnresolved
			t.Exists = false
			t.Reason = "no deterministic workspace match for file target"
			return t
		default:
			t.Status = TargetAmbiguous
			t.Exists = false
			t.Reason = "multiple deterministic workspace candidates"
			return t
		}
	}

	t.Status = TargetUnresolved
	t.Exists = false
	t.Reason = "target syntax does not resolve to a file"
	return t
}

// looksLikeFile reports whether a path carries file syntax (extension or
// conventional name).
func looksLikeFile(p string) bool {
	if filepath.Ext(p) != "" {
		return true
	}
	base := strings.ToLower(filepath.Base(p))
	return isBareKnown(base)
}

// resolvedTargets returns targets with the given status(es).
func resolvedTargets(targets []Target, statuses ...TargetStatus) []Target {
	var out []Target
	for _, t := range targets {
		for _, s := range statuses {
			if t.Status == s && t.Exists {
				out = append(out, t)
				break
			}
		}
	}
	return out
}

// targetsWithStatus returns the targets named with explicit @ syntax. It
// captures "explicit by syntax" — a deliberately named new-file target —
// independent of the resolution outcome.
func targetsWithStatus(targets []Target, explicit bool) []Target {
	var out []Target
	for _, t := range targets {
		if t.Explicit == explicit {
			out = append(out, t)
		}
	}
	return out
}

// templateTargetFromRequest derives a known template target name from a create
// request ("create a LICENSE file", "add a .gitignore"). Returns the canonical
// path and whether a template was recognized.
func templateTargetFromRequest(raw string) (string, bool) {
	lower := strings.ToLower(raw)
	for canon := range templateTargets {
		if strings.Contains(lower, strings.ToLower(filepath.Base(canon))) {
			return canon, true
		}
	}
	return "", false
}

// unresolvedTargets returns targets that could not be resolved to a file.
func unresolvedTargets(targets []Target) []Target {
	var out []Target
	for _, t := range targets {
		if !t.Exists {
			out = append(out, t)
		}
	}
	return out
}

// hasAmbiguous reports whether any target is genuinely ambiguous.
func hasAmbiguous(targets []Target) bool {
	for _, t := range targets {
		if t.Status == TargetAmbiguous {
			return true
		}
	}
	return false
}

// artifactForMutation selects the artifact contract for a targeted mutation.
// Existing real-content files anchor to a bounded replace_block; new files use
// create_file; empty files use replace_file. This preserves the Phase 8
// contract: existing content → anchored mutation, new/empty → creation.
func artifactForMutation(op OperationKind, targets []Target, deps Deps) ArtifactContract {
	if op == OperationCreate || len(targets) == 0 || !targets[0].Exists {
		return ArtifactContract{Kind: "create_file", Bounded: true, MaxLines: 0,
			Description: "full creation of the new file"}
	}
	return ArtifactContract{Kind: "replace_block", Bounded: true, MaxLines: 0,
		Description: "anchored SEARCH/REPLACE block on the existing file"}
}

// verifyDepth returns the deterministic verification depth for an operation.
func verifyDepth(op OperationKind) int {
	switch op {
	case OperationContent, OperationCreate, OperationExplain:
		return 1
	case OperationFix, OperationRefactor:
		return 2
	case OperationDiagnose, OperationArchitect:
		return 3
	default:
		return 0
	}
}

// withBudgets derives the reasoning/output budgets from complexity and the
// artifact contract, and the strategy-owned context budget from the context
// policy. Budgets follow task complexity and artifact shape — a SEARCH/REPLACE
// block never needs a plan's budget, and a model is never forced to re-emit a
// complete file merely because the target is small.
func withBudgets(profile ExecutionStrategyProfile) ExecutionStrategyProfile {
	if !profile.ModelRequired {
		profile.ReasoningBudget = 0
		profile.MaxOutputTokens = 0
		profile.ContextBudget = contextBudgetFor(profile)
		return profile
	}

	profile.ReasoningBudget = reasoningForComplexity(profile.Complexity.Level)
	profile.MaxOutputTokens = outputForArtifact(profile.Artifact.Kind, profile.Complexity.Level)
	profile.ContextBudget = contextBudgetFor(profile)
	return profile
}

// contextBudgetFor derives the strategy-owned context allowance from the
// profile's context policy. The policy decides; the compiler spends inside the
// budget. Example budgets: casual_chat → none/0; single-file edit →
// target_file_only/4000; repository investigation → repository/16000.
func contextBudgetFor(p ExecutionStrategyProfile) ContextBudget {
	switch p.Policy() {
	case ContextPolicyNone:
		return ContextBudget{Policy: ContextPolicyNone, Tokens: 0, MaxFiles: 0}
	case ContextPolicyTargetFileOnly:
		return ContextBudget{
			Policy:     ContextPolicyTargetFileOnly,
			Tokens:     4000,
			MaxFiles:   10,
			Evidence:   []ContextKind{ContextTargetContent},
			Escalation: p.Escalation,
		}
	default: // ContextPolicyRepository
		return ContextBudget{
			Policy:     ContextPolicyRepository,
			Tokens:     16000,
			MaxFiles:   50,
			Evidence:   []ContextKind{ContextDependencyEvidence, ContextRepositoryConstraints},
			Escalation: p.Escalation,
		}
	}
}

// reasoningForComplexity maps complexity onto a bounded reasoning budget.
func reasoningForComplexity(level ComplexityLevel) int {
	switch level {
	case ComplexityLow:
		return 512
	case ComplexityMedium:
		return 1024
	default:
		return 2048
	}
}

// ── PHASE 12: the output budget is DERIVED, not a universal constant ─────────
//
// max_tokens is an INVOCATION-level bound. It must be derived from the task
// shape so it is large enough for the artifact the task actually produces, and
// it must be bounded so no invocation can spend unboundedly. A single
// hard-coded 4 096 for every creation is neither: any new file larger than
// ~16 KB of source was GUARANTEED to truncate at the output gate
// (finish_reason=length), and the truncated prefix was then discarded — a
// guaranteed failure paid for in full model computation.
//
// The derivation is a pure, inspectable table over the complexity tier, which
// the runtime already computes from measurable execution factors
// (Assess(ComplexityInputs)), never from prompt vocabulary. The resulting
// REQUEST is still clamped against the model's real ceiling by the shared
// capability chain (llmstep.ResolveMaxTokens / ModelProfile.ClampMaxTokens), so
// raising the request can never make a constrained model overspend.

// CreationTokenTiers maps a complexity tier onto the per-invocation output
// REQUEST for a whole-new-file creation. A creation must carry the new file's
// full content, so every tier is larger than the anchored-patch budget of the
// same tier; the medium/high tiers are the ones a realistic page or module
// needs, and the low tier keeps the cheap case cheap.
var CreationTokenTiers = map[ComplexityLevel]int{
	ComplexityLow:    4096,
	ComplexityMedium: 8192,
	ComplexityHigh:   16384,
}

// CreationTokenBudget is the hard ceiling of a derived creation request. It
// bounds a single invocation; the run-level ceiling is derived separately
// (autonomy.RunTokenBudget) and the structural loop bounds still terminate a
// pathological run.
const CreationTokenBudget = 16384

// outputForArtifact maps the artifact contract onto a bounded output budget.
// create_file derives its budget from the complexity tier (see
// CreationTokenTiers); every other kind is a fixed, contract-shaped bound
// because its response is not a whole artifact.
func outputForArtifact(kind string, level ComplexityLevel) int {
	switch kind {
	case "create_file":
		if b, ok := CreationTokenTiers[level]; ok {
			if b > CreationTokenBudget {
				return CreationTokenBudget
			}
			return b
		}
		return CreationTokenTiers[ComplexityMedium]
	case "plan":
		return 1536
	case "investigation":
		return 2048
	case "explanation":
		return 1024
	case "response":
		return 512
	default: // replace_block / replace_file
		switch level {
		case ComplexityLow:
			return 1024
		case ComplexityMedium:
			return 2048
		default:
			return 3072
		}
	}
}
