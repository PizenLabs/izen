package autonomy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/planner"
)

// ── Pass 1 Manifest Auto-Hook (Preflight Autonomy Loop) ─────────────────────
//
// When Boundary 2 refuses a target because its full-file rewrite estimate
// exceeds max_output (I5, preflight_infeasible), the loop no longer jumps
// straight to a line-chunked DAG. It first issues a lightweight READ-ONLY
// Pass 1 Manifest Request to the model:
//
//	Pass 1 (Manifest)  — a tiny provider invocation that emits ONLY a raw
//	                     MutationManifest JSON naming the minimal mutation
//	                     surface for the objective. Strictly read-only: it
//	                     never reads the workspace beyond the caller-provided
//	                     target bytes and never writes anything.
//	Pass 2 (Bounded)    — the parsed manifest is fed to AdaptiveDecompose, so
//	                     the DAG is scoped to the mutation surface. If the
//	                     surface fits the budget the file executes as ONE
//	                     atomic unit (1 API call); otherwise sub-tasks are
//	                     grouped along the semantic blocks the manifest marked
//	                     for modification — never a naive line slicer.
//
// When Pass 1 fails (transport error, malformed JSON, or an empty mutation
// list) the loop falls back to a SINGLE-PASS BOUNDED INSPECTION task. The
// fallback NEVER re-slices the file into arbitrary line ranges: pruning
// discipline is preserved even without a usable manifest.

// ManifestPassFunc is the injectable Pass 1 manifest generator. It returns a
// parsed MutationManifest or an error; the preflight hook treats an error or
// an empty mutation list as "no manifest available" and falls back to the
// single-pass bounded inspection. Implementations MUST be read-only.
type ManifestPassFunc func(ctx context.Context, objective string, targetContent []byte) (*MutationManifest, error)

// ManifestPassForExecutor binds a ManifestPassFunc to the RuntimeExecutor's
// provider. It is the production wiring of the Pass 1 auto-hook: the executor
// remains the single authority that invokes the provider.
func ManifestPassForExecutor(exec *execution.RuntimeExecutor) ManifestPassFunc {
	exec.SetManifestSystemPrompt(buildManifestPrompt())
	return func(ctx context.Context, objective string, targetContent []byte) (*MutationManifest, error) {
		return ExecuteManifestPass(ctx, exec, objective, targetContent)
	}
}

// ExecuteManifestPass performs the read-only Pass 1 manifest request against
// the executor's provider and parses the raw response into a MutationManifest.
// It is strictly read-only: the provider receives the caller-provided target
// bytes and a minimal system prompt enforcing raw MutationManifest JSON output;
// nothing is ever written to the workspace. The parsed manifest is the only
// thing that crosses back to the DAG strategy decision.
func ExecuteManifestPass(ctx context.Context, exec *execution.RuntimeExecutor, prompt string, targetContent []byte) (*MutationManifest, error) {
	if exec == nil {
		return nil, errors.New("autonomy: manifest pass requires a runtime executor")
	}
	raw, err := exec.InvokeManifestPass(ctx, prompt, targetContent)
	if err != nil {
		return nil, fmt.Errorf("autonomy: manifest pass: %w", err)
	}
	manifest, err := ParseMutationManifest([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("autonomy: manifest pass: %w", err)
	}
	return manifest, nil
}

// PreflightRequiresManifest reports whether the canonical Boundary-2 estimate
// (target_bytes/4 × FullRewriteTokenMultiplier) exceeds max_output, i.e.
// whether the preflight autonomy loop MUST issue the automatic Pass 1 manifest
// request before determining the DAG strategy. It mirrors execution's
// EvaluatePreflight formula so the hook fires on exactly the same condition
// the preflight guard refuses the objective under.
func PreflightRequiresManifest(targetBytes, maxOutputTokens int) bool {
	if maxOutputTokens <= 0 {
		return false // unbounded budget: not provably infeasible at this boundary
	}
	if targetBytes <= 0 {
		return false // creation intent: no baseline to size-estimate
	}
	estimated := (targetBytes / 4) * execution.FullRewriteTokenMultiplier
	return estimated > maxOutputTokens
}

// ── Compact Manifest Prompt (Minimal Manifest Schema) ───────────────────────
//
// Free-tier / weak models (Cohere North Mini Code and friends) exhaust the
// Pass 1 output budget when the manifest prompt is verbose: they ramble past
// max_output, truncate mid-JSON, and the gate surfaces OUTPUT_EXHAUSTED. The
// compact manifest prompt DEMANDS a MINIFIED JSON payload with ZERO prose and
// a hard 200-token ceiling, so a compliant model completes with a tiny valid
// manifest on the first attempt. An output that still exceeds the 512-token
// rejection threshold is rejected as INVALID JSON (a silent fallback), never
// routed through the output gate.

// ManifestPassCompactDirective is the strict conciseness instruction injected
// verbatim into the Pass 1 manifest system prompt.
const ManifestPassCompactDirective = "OUTPUT ONLY VALID MINIFIED JSON ARRAY OF MUTATION TARGETS. DO NOT WRITE CODE, DO NOT EXPLAIN, DO NOT INCLUDE MARKDOWN FENCES. MAX 200 TOKENS."

// ManifestPassMaxTokens is the fixed output ceiling of the read-only Pass 1
// manifest generation. It matches the "MAX 200 TOKENS" directive so a verbose
// model physically cannot ramble past the compact budget.
const ManifestPassMaxTokens = 200

// ManifestPassRejectTokens is the post-hoc rejection threshold: a manifest
// response whose token estimate still exceeds this ceiling (a provider that
// ignores max_tokens) is rejected as invalid JSON instead of exhausting the
// output gate.
const ManifestPassRejectTokens = 512

// buildManifestPrompt renders the COMPACT Pass 1 manifest system prompt. It is
// the single authority on the manifest wire contract: the strict minified-JSON
// directive, the exact schema, and the hard token budget. ManifestPassForExecutor
// injects it into the runtime executor at bootstrap.
func buildManifestPrompt() string {
	return "You are the Pass 1 manifest generator of a read-only planning stage. " +
		"Analyze the target file below against the user's objective and propose the MINIMAL set of mutations that achieve it. " +
		ManifestPassCompactDirective + "\n" +
		"Output a single raw JSON object (minified, no newlines) conforming exactly to:\n" +
		`{"targetFile":"<workspace-relative path>","intent":"<one-line objective>","mutations":[{"selector":"<css selector or symbol, e.g. #hero or section#hero>","action":"delete|modify|insert","estimatedLines":<positive int>}]}` + "\n" +
		"Rules: every mutation MUST name a selector or symbol that exists in the file; " +
		"omit any content the objective does not touch; " +
		"if the objective requires NO change, emit {\"targetFile\":\"<path>\",\"intent\":\"...\",\"mutations\":[]}. " +
		"This pass never writes to the workspace."
}

// runPreflight is the automatic Pass 1 manifest hook of the preflight autonomy
// loop. Given a target the Boundary-2 guard refused as infeasible, it decides
// the DAG strategy:
//
//  1. When a ManifestPassFunc is wired, it issues the read-only manifest
//     request and feeds the parsed manifest into AdaptiveDecompose. A valid
//     manifest with at least one mutation yields either a single atomic task
//     (surface ≤ budget) or a semantic DAG pruned to the mutated blocks.
//  2. On manifest failure or an empty mutation list it falls back to a
//     SINGLE-PASS BOUNDED INSPECTION task — never a naive line slicer.
//  3. When no manifest pass is wired (backward-compatible test/CLI paths) it
//     defers to the injected DecomposeFunc unchanged.
func (d *Driver) runPreflight(ctx context.Context, objective, target string, source []byte, baseDigest string, maxOut int) (*planner.ExecutionDAG, error) {
	if d.decompose == nil {
		return nil, errors.New("autonomy: decomposition disabled")
	}
	// ── PREFLIGHT BASELINE SYNTAX SNAPSHOT ──────────────────────────────
	// Capture the target's syntax validity BEFORE any sub-task executes and
	// attach it to the staged DAG. The post-DAG global audit consults it to
	// distinguish a pre-existing baseline defect (an unchanged no-op document
	// that was already broken) from a mutation regression — the former must
	// never fail the run as if the DAG introduced it.
	baselineValid := execution.ValidateDocumentSyntax(target, source) == nil //nolint:contextcheck // document syntax validation is pure content checking, no context needed
	// stage attaches the snapshot to whatever DAG the strategy decision yields.
	stage := func(dag *planner.ExecutionDAG, err error) (*planner.ExecutionDAG, error) {
		if err == nil && dag != nil {
			dag.BaselineSyntaxValid = baselineValid
			diagnosticf("[preflight] baseline syntax snapshot target=%s valid=%v", dag.Target, baselineValid)
		}
		return dag, err
	}
	if d.manifestPass == nil {
		return stage(d.decompose(objective, target, source, baseDigest, maxOut))
	}
	if !PreflightRequiresManifest(len(source), maxOut) {
		return stage(d.decompose(objective, target, source, baseDigest, maxOut))
	}
	// ── AUTOMATIC PASS 1 MANIFEST REQUEST ─────────────────────────────
	manifest, mErr := d.manifestPass(ctx, objective, source)
	if mErr == nil && manifest != nil && len(manifest.Mutations) > 0 {
		dag, err := AdaptiveDecompose(objective, target, source, baseDigest, maxOut, manifest)
		if err != nil {
			diagnosticf("[preflight] manifest-scoped decomposition failed: %v — falling back to single-pass bounded inspection", err)
		} else {
			diagnosticf("[preflight] Pass 1 manifest staged manifest_target=%s mutations=%d sub_tasks=%d manifest_scoped=%v",
				manifest.TargetFile, len(manifest.Mutations), len(dag.SubTasks), dag.ManifestScoped)
			return stage(dag, nil)
		}
	}
	// Manifest generation failed or returned empty mutations: fall back to the
	// bounded window decomposition. Deliberately NOT one whole-file atomic unit —
	// a manifest-less single task would force the model to rewrite the whole
	// file in one block; the fallback slices into ≤ fallbackWindowMaxLines
	// windows so every sub-task stays a small targeted delta.
	if mErr != nil {
		diagnosticf("[preflight] Pass 1 manifest unavailable (%v) — bounded window decomposition", mErr)
	} else {
		diagnosticf("[preflight] Pass 1 manifest returned no mutations — bounded window decomposition")
	}
	return stage(fallbackDecompose(objective, target, source, baseDigest, maxOut))
}

// ── Zero-Token EVALUATING_SCOPE Contract ────────────────────────────────────
//
// The Pre-Execution Feasibility Invariant: NO LLM inference, DAG decomposition,
// manifest generation, or mutation planning may begin until a LOCAL, ZERO-TOKEN
// preflight establishes that the target context is structurally valid,
// sufficiently resolved, and executable within the declared budget. Infeasibility
// detection MUST occur prior to inference — zero LLM tokens may be spent if the
// target AST is corrupted, dependencies are unresolved, or the budget is
// exceeded. Enforcement is fail-closed: if ExecutionGate() returns false the
// engine MUST halt the transition to DECIDING/STAGING and divert to
// AWAITING_HUMAN_PROPOSAL.

// ASTStatus classifies the target's structural (AST/syntax) validity, judged
// by the deterministic local validators — never by a model.
//
// ZERO-VALUE INVARIANT: ASTStatus is a string type whose zero value is the
// empty string, declared as the explicit ASTUnspecified constant below. An
// UNINITIALIZED PreflightEvaluation therefore reads ASTUnspecified — NEVER
// ASTCorrupt. No code path may ever treat the zero value as corruption; an
// unspecified status is fail-closed (the ExecutionGate refuses to pass) but is
// semantically "unknown", not "broken".
type ASTStatus string

// DependencyStatus classifies whether the target's local file references
// (<link href="...">, <script src="...">) resolve to existing files.
type DependencyStatus string

// BudgetStatus classifies whether the estimated generation cost fits the
// declared output budget.
type BudgetStatus string

const (
	// ASTUnspecified is the zero value: an uninitialized ASTStatus. It must
	// NEVER be read as ASTCorrupt. The gate treats it as unknown — fail-closed
	// but never labeled "corrupt AST baseline".
	ASTUnspecified ASTStatus = ""
	// ASTValid: the target parses cleanly under its registered validator.
	ASTValid ASTStatus = "valid"
	// ASTCorrupt: the target fails its structural validator (unclosed tags,
	// non-code prose headers, unterminated raw-text elements, ...).
	ASTCorrupt ASTStatus = "corrupt"
	// ASTUnknown: no structural validator serves the target's format, or the
	// target is empty/absent — the gate cannot prove validity, so it treats
	// the target as unknown (not a hard failure).
	ASTUnknown ASTStatus = "unknown"

	// DependenciesResolved: every referenced local file exists.
	DependenciesResolved DependencyStatus = "resolved"
	// DependenciesUnresolved: at least one referenced local file is missing.
	DependenciesUnresolved DependencyStatus = "unresolved"

	// BudgetWithinLimits: the estimated generation cost fits the budget.
	BudgetWithinLimits BudgetStatus = "within_limits"
	// BudgetExceeded: the estimated cost exceeds the declared budget.
	BudgetExceeded BudgetStatus = "exceeded"
)

// Known reports whether the status is one of the explicit vocabulary values
// (valid / corrupt / unknown) rather than the zero-value ASTUnspecified.
func (s ASTStatus) Known() bool {
	switch s {
	case ASTValid, ASTCorrupt, ASTUnknown:
		return true
	default:
		return false
	}
}

// ProposalRequirement is a placeholder for a change the evaluation concludes
// cannot proceed without an explicit human-approved re-scope (a required
// proposal). The gate treats any non-empty set as a hard barrier: a target that
// demands a proposal is NOT executable as-is. This is plain data; the autonomy
// loop renders it on the AWAITING_HUMAN_PROPOSAL boundary.
type ProposalRequirement struct {
	// Reason is the bounded evidence of WHY a human proposal is required.
	Reason string `json:"reason"`
	// Target is the workspace-relative file that needs the proposal.
	Target string `json:"target"`
}

// PreflightEvaluation is the Zero-Token EVALUATING_SCOPE verdict. It is built
// entirely from deterministic local heuristics — never from model inference —
// so it is the authoritative input to the fail-closed ExecutionGate.
type PreflightEvaluation struct {
	Target            string                `json:"target"`
	ASTStatus         ASTStatus             `json:"ast_status"`
	DependencyStatus  DependencyStatus      `json:"dependency_status"`
	BudgetStatus      BudgetStatus          `json:"budget_status"`
	Findings          []string              `json:"findings"`
	RequiredProposals []ProposalRequirement `json:"required_proposals"`
	// EstimatedTokens is the deterministic generation estimate of the evaluated
	// strategy (0 when the target is empty/absent or the budget is unbounded).
	// It is the SAME canonical accounting Boundary 2 uses, so the DecisionSurface
	// can show the human the exact estimate that closed the gate.
	EstimatedTokens int `json:"estimated_tokens"`
	// MaxOutputTokens is the declared output ceiling the estimate was judged
	// against (0 = unbounded).
	MaxOutputTokens int `json:"max_output_tokens"`
}

// ExecutionGate is the hard invariant gate. It returns true ONLY when every
// precondition of safe, bounded execution is provably satisfied locally:
// the AST is valid, dependencies resolve, the budget fits, and NO human
// proposal is required. Any other combination is a fail-closed barrier.
func (e PreflightEvaluation) ExecutionGate() bool {
	return e.ASTStatus == ASTValid &&
		e.DependencyStatus == DependenciesResolved &&
		e.BudgetStatus == BudgetWithinLimits &&
		len(e.RequiredProposals) == 0
}

// AddFinding appends a bounded, human-readable evidence line to the verdict.
func (e *PreflightEvaluation) AddFinding(format string, args ...interface{}) {
	if e == nil {
		return
	}
	e.Findings = append(e.Findings, fmt.Sprintf(format, args...))
}

// ScopeInput carries the local facts the zero-token evaluation inspects. All
// inputs are workspace-local; nothing here invokes a provider.
type ScopeInput struct {
	// Target is the workspace-relative path of the file being evaluated.
	Target string
	// Content is the raw bytes of the target (nil/empty when the file is
	// absent or a creation intent).
	Content []byte
	// MaxOutputTokens is the declared output budget (0 = unbounded).
	MaxOutputTokens int
	// Root is the workspace root used to resolve local file references.
	Root string
	// Subcommand is the policy scope ($prompt / $hot / ""). Targeted
	// modification prompts ($prompt / $hot) are expected to issue bounded
	// patches, so their budget uses the bounded patch multiplier instead of
	// the full-rewrite multiplier ($3×) — unless the prompt itself demands an
	// explicit whole-file rewrite (see PreflightMultiplierForTarget).
	Subcommand string
	// Prompt is the raw admitted prompt. The budget estimator inspects it to
	// distinguish a targeted modification request (remove/fix/refactor →
	// BoundedPatchTokenMultiplier) from an explicit full-rewrite or
	// scaffolding request (→ FullRewriteTokenMultiplier). Empty keeps the
	// subcommand + markup-target default.
	Prompt string
}

// EstimateScopeTokens computes the estimated generation cost of a target using
// the SAME canonical accounting as Boundary 2: target_bytes/4 ×
// FullRewriteTokenMultiplier. A cost that exceeds MaxOutputTokens is provably
// infeasible BEFORE any provider request (invariant I5, zero-token boundary).
func EstimateScopeTokens(targetBytes, maxOutputTokens int) (estimated int, exceeded bool) {
	if targetBytes <= 0 || maxOutputTokens <= 0 {
		return 0, false // creation / unbounded budget: not provably infeasible
	}
	estimated = (targetBytes / 4) * execution.FullRewriteTokenMultiplier
	return estimated, estimated > maxOutputTokens
}

// fullRewriteSignalRe matches an EXPLICIT whole-file rewrite or scaffolding
// request. Only such intents reserve the FullRewriteTokenMultiplier ($3×);
// everything else on a targeted modification scope assumes a bounded patch.
var fullRewriteSignalRe = regexp.MustCompile(`(?i)\b(rewrite|regenerate|recreate|rebuild|scaffold|from\s+scratch)\b`)

// targetedModificationSignalRe matches the verbs of a TARGETED modification
// request: remove/fix/refactor/… — the action class whose generation cost is a
// bounded SEARCH/REPLACE patch, not a whole-file regeneration.
var targetedModificationSignalRe = regexp.MustCompile(`(?i)\b(remove|delete|fix|refactor|update|modify|change|edit|insert|replace|simplify|trim|strip|clean|reduce|shorten|cut)\b`)

// IsFullRewriteIntent reports whether the raw prompt demands an explicit
// whole-file rewrite or scaffolding/recreation — the ONLY intent class that
// keeps the FullRewriteTokenMultiplier ($3×) on a targeted modification scope.
func IsFullRewriteIntent(prompt string) bool {
	return fullRewriteSignalRe.MatchString(prompt)
}

// IsTargetedModificationIntent reports whether the raw prompt is a targeted
// modification request (remove redundant content, refactor function, fix bug).
// Such prompts issue bounded SEARCH/REPLACE patches and default to the
// BoundedPatchTokenMultiplier ($2×).
func IsTargetedModificationIntent(prompt string) bool {
	return targetedModificationSignalRe.MatchString(prompt)
}

// PreflightMultiplierForTarget resolves the generation multiplier a preflight
// scope uses for the given target under the subcommand policy and raw prompt.
// It is the single authority on the bounded-patch-vs-full-rewrite split:
//
//   - outside $prompt/$hot there is no bounded-patch contract → $3×;
//   - an explicit full-rewrite / scaffolding prompt keeps $3× even under
//     $prompt/$hot (IntentFullRewrite is never silently downgraded);
//   - a markup target under $prompt/$hot defaults to a bounded patch → $2×;
//   - a non-markup targeted modification prompt (remove/fix/refactor) also
//     issues a bounded patch → $2×;
//   - every other combination keeps the canonical full-rewrite accounting.
func PreflightMultiplierForTarget(target, subcommand, prompt string) int {
	if !IsSystemic(subcommand) && !IsHot(subcommand) {
		return execution.FullRewriteTokenMultiplier
	}
	if IsFullRewriteIntent(prompt) {
		return execution.FullRewriteTokenMultiplier
	}
	if execution.IsMarkupTarget(target) || IsTargetedModificationIntent(prompt) {
		return execution.BoundedPatchTokenMultiplier
	}
	return execution.FullRewriteTokenMultiplier
}

// EstimateScopeTokensForSubcommand computes the estimated generation cost of a
// TARGETED modification prompt under a policy scope and raw prompt context. A
// targeted modification prompt ($prompt / $hot — remove, fix, refactor) on a
// markup (HTML/template) target is expected to produce a bounded
// SEARCH/REPLACE patch — not a whole-file rewrite — so the bounded patch
// multiplier replaces the full-rewrite multiplier ($3×). An explicit full
// rewrite / scaffolding request keeps the canonical full-rewrite accounting.
func EstimateScopeTokensForSubcommand(targetBytes, maxOutputTokens int, target, subcommand, prompt string) (estimated int, exceeded bool) {
	if targetBytes <= 0 || maxOutputTokens <= 0 {
		return 0, false // creation / unbounded budget: not provably infeasible
	}
	multiplier := PreflightMultiplierForTarget(target, subcommand, prompt)
	estimated = (targetBytes / 4) * multiplier
	return estimated, estimated > maxOutputTokens
}

// localRefRe matches local file references in HTML documents:
// <link href="..."> and <script src="...">. Only same-origin relative or
// root-relative paths are considered resolvable; absolute URLs are skipped.
var localRefRe = mustCompileLocalRef()

// mustCompileLocalRef builds the local-reference matcher. It captures the
// href/src attribute value (group 1) for <link> and <script> elements.
func mustCompileLocalRef() *regexp.Regexp {
	return regexp.MustCompile(`(?i)<(?:link|script)\b[^>]*\b(?:href|src)\s*=\s*["']([^"']+)["']`)
}

// EvaluateScope runs the ZERO-TOKEN preflight over one target. It performs only
// local, deterministic heuristics: AST/syntax validity, local file-reference
// resolution, and budget estimation. It NEVER issues an LLM call, builds a DAG,
// or generates a manifest. The returned PreflightEvaluation feeds the
// fail-closed ExecutionGate.
func EvaluateScope(in ScopeInput) PreflightEvaluation {
	eval := PreflightEvaluation{
		Target:    in.Target,
		ASTStatus: ASTValid,
	}
	if in.Target == "" {
		eval.ASTStatus = ASTUnknown
		eval.DependencyStatus = DependenciesResolved
		eval.BudgetStatus = BudgetWithinLimits
		eval.AddFinding("no target resolved — scope evaluation deferred")
		return eval
	}

	// ── 1. AST / SYNTAX VALIDITY (0 tokens) ────────────────────────────
	if len(in.Content) == 0 {
		// Absent/empty target: creation intent or missing file. Not corrupt by
		// construction, but not provably valid either — an absent target cannot
		// be executed as a mutation.
		eval.ASTStatus = ASTUnknown
		eval.AddFinding("target %q has no content (creation intent or missing file)", in.Target)
	} else if err := execution.ValidateDocumentSyntax(in.Target, in.Content); err != nil {
		eval.ASTStatus = ASTCorrupt
		eval.AddFinding("target %q is structurally corrupt: %v", in.Target, err)
	}

	// ── 2. LOCAL FILE REFERENCES (0 tokens) ────────────────────────────
	eval.DependencyStatus = DependenciesResolved
	for _, ref := range localRefRe.FindAllStringSubmatch(string(in.Content), -1) {
		path := ref[1]
		if path == "" || strings.Contains(path, "://") || strings.HasPrefix(path, "//") {
			continue // absolute or protocol-relative URL: not a local dependency
		}
		abs := filepath.Join(in.Root, filepath.FromSlash(strings.TrimPrefix(path, "/")))
		if _, err := os.Stat(abs); err != nil {
			eval.DependencyStatus = DependenciesUnresolved
			eval.AddFinding("target %q references missing local file %q", in.Target, path)
		}
	}

	// ── 3. BUDGET ESTIMATION (0 tokens) ────────────────────────────────
	// Targeted modification prompts ($prompt / $hot — remove/fix/refactor) are
	// expected to issue bounded SEARCH/REPLACE patches, so their estimate uses
	// the bounded patch multiplier instead of the full-rewrite multiplier
	// ($3×). An explicit full-rewrite / scaffolding request keeps $3×.
	eval.BudgetStatus = BudgetWithinLimits
	eval.MaxOutputTokens = in.MaxOutputTokens
	estimated, exceeded := EstimateScopeTokensForSubcommand(len(in.Content), in.MaxOutputTokens, in.Target, in.Subcommand, in.Prompt)
	eval.EstimatedTokens = estimated
	if exceeded {
		eval.BudgetStatus = BudgetExceeded
		multiplier := PreflightMultiplierForTarget(in.Target, in.Subcommand, in.Prompt)
		eval.AddFinding("target %q estimates ~%d tokens (bytes/4 × %d) but max_output=%d — budget exceeded",
			in.Target, estimated, multiplier, in.MaxOutputTokens)
	}

	// ── FAIL-CLOSED BARRIER ────────────────────────────────────────────
	// When any precondition fails, a required human proposal is recorded so the
	// gate can never pass. The engine diverts to AWAITING_HUMAN_PROPOSAL.
	if !eval.ExecutionGate() {
		eval.RequiredProposals = append(eval.RequiredProposals, ProposalRequirement{
			Reason: "scope evaluation barrier: " + barrierReason(eval),
			Target: in.Target,
		})
	}
	return eval
}

// barrierReason renders a compact bounded summary of why the gate closed. It
// formats EXACT reason strings from the actual state so a valid-AST target is
// never mislabeled as corrupt:
//
//   - ASTStatus == ASTCorrupt                    → "corrupt AST baseline"
//   - ASTStatus == ASTValid + BudgetExceeded     → "output budget exceeded
//     (bounded patch required)" (never "corrupt AST")
func barrierReason(e PreflightEvaluation) string {
	var parts []string
	switch e.ASTStatus {
	case ASTCorrupt:
		parts = append(parts, "corrupt AST baseline")
	case ASTUnknown, ASTUnspecified:
		// ASTUnspecified is the zero value: an uninitialized status is
		// "unverified", NEVER a corrupt baseline.
		parts = append(parts, "unverified AST")
	}
	if e.DependencyStatus == DependenciesUnresolved {
		parts = append(parts, "unresolved dependencies")
	}
	if e.BudgetStatus == BudgetExceeded {
		if e.ASTStatus == ASTValid {
			parts = append(parts, "output budget exceeded (bounded patch required)")
		} else {
			parts = append(parts, "budget exceeded")
		}
	}
	if len(parts) == 0 {
		return "scope not executable as-is"
	}
	return strings.Join(parts, ", ")
}

// ── Typed Preflight Failure (invariant: no stringly-typed control flow) ─────
//
// A preflight rejection is a CONTROL-PLANE outcome, never an execution result.
// The state machine branches on the typed category below — never on log text or
// on `err.Error()` substring matching.

// PreflightFailureCategory is the typed taxonomy of a preflight rejection.
type PreflightFailureCategory string

const (
	// PreflightBudgetExceeded: the estimated generation cost exceeds max_output.
	PreflightBudgetExceeded PreflightFailureCategory = "budget_exceeded"
	// PreflightASTCorrupt: the target baseline fails its structural validator.
	PreflightASTCorrupt PreflightFailureCategory = "ast_corrupt"
	// PreflightTargetAmbiguous: no unique target was resolved.
	PreflightTargetAmbiguous PreflightFailureCategory = "target_ambiguous"
	// PreflightCapabilityDenied: the target's local references do not resolve
	// (or a capability the mutation requires is not granted).
	PreflightCapabilityDenied PreflightFailureCategory = "capability_denied"
	// PreflightRollbackUnavailable: a recovery that requires rollback cannot
	// be made safe.
	PreflightRollbackUnavailable PreflightFailureCategory = "rollback_unavailable"
	// PreflightVerificationInfeasible: the mutation cannot be verified.
	PreflightVerificationInfeasible PreflightFailureCategory = "verification_infeasible"
	// PreflightInternalError: the gate closed for an unrecognized reason.
	PreflightInternalError PreflightFailureCategory = "internal_error"
	// PreflightCancelled: the run was cancelled before/at the gate.
	PreflightCancelled PreflightFailureCategory = "cancelled"
)

// ClassifyPreflightFailure derives the typed failure category from a
// Zero-Token evaluation. It is deterministic and never inspects log text.
func ClassifyPreflightFailure(eval PreflightEvaluation) PreflightFailureCategory {
	switch {
	case eval.ASTStatus == ASTCorrupt:
		return PreflightASTCorrupt
	case eval.DependencyStatus == DependenciesUnresolved:
		return PreflightCapabilityDenied
	case eval.BudgetStatus == BudgetExceeded:
		return PreflightBudgetExceeded
	case eval.Target == "":
		return PreflightTargetAmbiguous
	default:
		return PreflightInternalError
	}
}

// Recoverable reports whether the category can be recovered through an
// explicit human choice (a typed decision surface action).
func (c PreflightFailureCategory) Recoverable() bool {
	switch c {
	case PreflightBudgetExceeded, PreflightASTCorrupt, PreflightCapabilityDenied:
		return true
	default:
		return false
	}
}

// PreflightFailure is the typed record of one preflight rejection. It exposes
// exactly the bounded facts the human decision surface and the state machine
// branch on — never a free-form error string.
type PreflightFailure struct {
	Category        PreflightFailureCategory
	Reason          string
	Target          string
	Strategy        string
	EstimatedTokens int
	MaxOutputTokens int
	ASTStatus       ASTStatus
	Recoverable     bool
	RecoveryOptions []ProposalOption
}

// BuildPreflightFailure derives a typed PreflightFailure from a closed-gate
// evaluation. It is pure data: it never emits an event and never runs a policy.
func BuildPreflightFailure(eval PreflightEvaluation) PreflightFailure {
	cat := ClassifyPreflightFailure(eval)
	return PreflightFailure{
		Category:        cat,
		Reason:          barrierReason(eval),
		Target:          eval.Target,
		EstimatedTokens: eval.EstimatedTokens,
		MaxOutputTokens: eval.MaxOutputTokens,
		ASTStatus:       eval.ASTStatus,
		Recoverable:     cat.Recoverable(),
		RecoveryOptions: buildProposalOptions(eval, ""),
	}
}
