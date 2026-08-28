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
type ASTStatus string

// DependencyStatus classifies whether the target's local file references
// (<link href="...">, <script src="...">) resolve to existing files.
type DependencyStatus string

// BudgetStatus classifies whether the estimated generation cost fits the
// declared output budget.
type BudgetStatus string

const (
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
	// modification prompts ($prompt / $hot) on markup targets are expected to
	// issue bounded patches, so their budget uses the bounded patch multiplier
	// instead of the full-rewrite multiplier ($3×).
	Subcommand string
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

// EstimateScopeTokensForSubcommand computes the estimated generation cost of a
// TARGETED modification prompt under a policy scope. A targeted modification
// prompt ($prompt / $hot) on a markup (HTML/template) target is expected to
// produce a bounded SEARCH/REPLACE patch — not a whole-file rewrite — so the
// bounded patch multiplier replaces the full-rewrite multiplier ($3×) for that
// target class. Every other combination keeps the canonical full-rewrite
// accounting.
func EstimateScopeTokensForSubcommand(targetBytes, maxOutputTokens int, target, subcommand string) (estimated int, exceeded bool) {
	if targetBytes <= 0 || maxOutputTokens <= 0 {
		return 0, false // creation / unbounded budget: not provably infeasible
	}
	if (IsSystemic(subcommand) || IsHot(subcommand)) && execution.IsMarkupTarget(target) {
		estimated = (targetBytes / 4) * execution.BoundedPatchTokenMultiplier
		return estimated, estimated > maxOutputTokens
	}
	return EstimateScopeTokens(targetBytes, maxOutputTokens)
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
	// Targeted modification prompts ($prompt / $hot) on markup targets are
	// expected to issue bounded patches, so their estimate uses the bounded
	// patch multiplier instead of the full-rewrite multiplier ($3×).
	eval.BudgetStatus = BudgetWithinLimits
	estimated, exceeded := EstimateScopeTokensForSubcommand(len(in.Content), in.MaxOutputTokens, in.Target, in.Subcommand)
	if exceeded {
		eval.BudgetStatus = BudgetExceeded
		multiplier := execution.FullRewriteTokenMultiplier
		if (IsSystemic(in.Subcommand) || IsHot(in.Subcommand)) && execution.IsMarkupTarget(in.Target) {
			multiplier = execution.BoundedPatchTokenMultiplier
		}
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

// barrierReason renders a compact bounded summary of why the gate closed.
func barrierReason(e PreflightEvaluation) string {
	var parts []string
	switch e.ASTStatus {
	case ASTCorrupt:
		parts = append(parts, "corrupt AST")
	case ASTUnknown:
		parts = append(parts, "unverified AST")
	}
	if e.DependencyStatus == DependenciesUnresolved {
		parts = append(parts, "unresolved dependencies")
	}
	if e.BudgetStatus == BudgetExceeded {
		parts = append(parts, "budget exceeded")
	}
	if len(parts) == 0 {
		return "scope not executable as-is"
	}
	return strings.Join(parts, ", ")
}
