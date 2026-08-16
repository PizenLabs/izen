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

// diagnosticSignals mark a root-cause investigation request.
var diagnosticSignals = []string{
	"why is", "why does", "why isn't", "why doesn't", "what caused",
	"root cause", "stack trace", "backtrace", "crash", "panic",
	"is broken", "is crashing", "is failing", "test failure",
}

// architecturalSignals mark a broad architectural request.
var architecturalSignals = []string{
	"architecture", "redesign", "restructure", "migrate", "schema",
	"database", "pipeline", "event-driven", "message queue",
	"cross-cutting", "multi-file", "distributed",
}

// explainSignals mark a read-only understanding request.
var explainSignals = []string{
	"explain", "describe", "what is", "what does", "how does",
	"understand", "summarize", "walk me through",
}

// createSignals mark a file-creation request.
var createSignals = []string{
	"create", "generate", "add a", "write a", "new file", "init a",
	"scaffold", "bootstrap",
}

// refactorSignals mark a structural/renaming change.
var refactorSignals = []string{
	"refactor", "rename", "extract", "restructure", "move",
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
		return profile
	}

	parsed, _ := parser.Parse(raw, nil)
	targets, fileSyntax := collectTargets(parsed, raw, deps)

	op := classifyOperation(raw, parsed)
	profile.Targets = targets

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
		return withBudgets(profile)
	}

	// ── 4. Explicit target(s) → targeted execution ────────────────────
	if len(explicit) > 0 || (op == OperationCreate && len(explicitSyntax) > 0) {
		named := explicit
		if len(named) == 0 {
			named = explicitSyntax
		}
		switch op {
		case OperationExplain:
			profile.Strategy = TargetedReasoning
			profile.ModelRequired = true
			profile.StrategyReason = "read-only understanding request with an explicit target"
			profile.ModelDecision = "answer the understanding question from the provided target context"
			profile.Artifact = ArtifactContract{Kind: "explanation", Bounded: true,
				Description: "focused explanation of the target"}
			profile.ContextKinds = []ContextKind{ContextUserIntent, ContextExplicitTargets, ContextTargetContent}
			profile.Complexity = Assess(ComplexityInputs{Operation: op, TargetCount: len(named),
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
		profile.Complexity = Assess(ComplexityInputs{Operation: op, TargetCount: len(inferred),
			FileCount: len(inferred), VerificationDepth: verifyDepth(op)})
		return withBudgets(profile)
	}

	// ── 6. Casual chat / direct greeting (never workspace planning) ────
	// A greeting, small talk, or direct question that resolved no target and
	// matched no coding operation is direct chat: exactly one bounded read-only
	// invocation with zero repository evidence. It must NEVER expand into
	// repository-level planning — "hi" is not a planning request. This guard
	// runs last so a stronger signal (create / clarify / diagnose / architect /
	// explicit or inferred target) always wins.
	if gateway.IsCasualChat(raw) {
		profile.Strategy = TargetedReasoning
		profile.ModelRequired = true
		profile.StrategyReason = "casual greeting / direct chat; answered directly, no workspace planning"
		profile.ModelDecision = "answer the greeting or question directly"
		profile.Artifact = ArtifactContract{Kind: "explanation", Bounded: true,
			Description: "direct chat reply"}
		profile.Complexity = Assess(ComplexityInputs{Operation: OperationExplain, TargetCount: 0, FileCount: 0})
		profile.ContextKinds = []ContextKind{ContextUserIntent}
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
	return withBudgets(profile)
}

// classifyOperation derives the coarse operation class from the request. It
// classifies the parsed Goal — the actual task text without @scope markers —
// so a filename like @architecture.md can never trigger the architectural
// signal. The operation family steers the strategy and the complexity base; it
// never alone decides complexity (see complexity.go).
func classifyOperation(raw string, parsed *parser.IntentAST) OperationKind {
	text := raw
	if parsed != nil && parsed.Goal != "" {
		text = parsed.Goal
	}
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
	return OperationContent
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
func extractBareTargets(raw string) []string {
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
// artifact contract. Budgets follow task complexity and artifact shape — a
// SEARCH/REPLACE block never needs a plan's budget, and a model is never
// forced to re-emit a complete file merely because the target is small.
func withBudgets(profile ExecutionStrategyProfile) ExecutionStrategyProfile {
	if !profile.ModelRequired {
		profile.ReasoningBudget = 0
		profile.MaxOutputTokens = 0
		return profile
	}

	profile.ReasoningBudget = reasoningForComplexity(profile.Complexity.Level)
	profile.MaxOutputTokens = outputForArtifact(profile.Artifact.Kind, profile.Complexity.Level)
	return profile
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

// outputForArtifact maps the artifact contract onto a bounded output budget.
// create_file is the only kind that legitimately needs a large budget because
// it must carry the new file's full content.
func outputForArtifact(kind string, level ComplexityLevel) int {
	switch kind {
	case "create_file":
		return 4096
	case "plan":
		return 1536
	case "investigation":
		return 2048
	case "explanation":
		return 1024
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
