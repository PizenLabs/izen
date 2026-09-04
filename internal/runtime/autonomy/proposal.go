package autonomy

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
)

// ── DecisionSurface Lifecycle ────────────────────────────────────────────────
//
// A DecisionSurface moves through exactly four lifecycle positions:
// CREATED → PUBLISHED → ACTIVATED → RESOLVED. "Staged" is NOT the same as
// "active": a surface is PUBLISHED when the runtime emits the typed proposal
// payload on the bus (BEFORE parking), and ACTIVATED only when the runtime
// parks at awaiting_human with that surface as the live decision gate.

// SurfaceLifecycle is the lifecycle position of a DecisionSurface.
type SurfaceLifecycle string

const (
	// SurfaceLifecycleCreated: the surface was constructed from a closed-gate
	// evaluation.
	SurfaceLifecycleCreated SurfaceLifecycle = "created"
	// SurfaceLifecyclePublished: the surface was emitted on the bus as a typed
	// proposal payload. This MUST happen BEFORE the runtime parks.
	SurfaceLifecyclePublished SurfaceLifecycle = "published"
	// SurfaceLifecycleActivated: the runtime parked at awaiting_human and the
	// surface is the live human decision gate.
	SurfaceLifecycleActivated SurfaceLifecycle = "activated"
	// SurfaceLifecycleResolved: a human choice resolved the surface.
	SurfaceLifecycleResolved SurfaceLifecycle = "resolved"
)

// ── Interactive Proposal Strategy Gateway ───────────────────────────────────
//
// The DecisionSurface is the pure, data-only contract between the ZERO-TOKEN
// scope gate (EvaluateScope / PreflightEvaluation) and the interactive TUI
// proposal modal. It carries NO functional callbacks: it is plain serializable
// data a Bubble Tea handler renders as a selection menu and collapses back into
// a single ProposalIntent. Selecting an option NEVER mutates workspace state —
// it only returns an intent the Engine routes across the RuntimeExecutor
// boundary.
//
// Subcommand policy isolation:
//
//	$prompt  — Systemic / Advisory. May suggest scope expansion
//	           (ProposalExpandScope) or inline asset conversion
//	           (ProposalInlineDeps).
//	$hot     — Strict local target boundary. ABSOLUTELY FORBIDDEN from offering
//	           scope expansion. Unresolved dependencies cause immediate
//	           fail-fast or local-only proposals (ProposalRepairFirst,
//	           ProposalCancel). Never ProposalExpandScope.
//
// Anti-loop protection: selecting a proposal strategy updates execution-context
// metadata. If the same proposal fails twice without altering state, the engine
// forces the ABORTED state (see Driver.ResumeWithProposal).

// ProposalIntent is the typed, closed vocabulary of a human-selected proposal
// strategy. It is pure data — never a callback — and is the ONLY value a TUI
// option selection returns to the engine.
type ProposalIntent string

const (
	// ProposalInlineDeps converts an unresolved external reference into an
	// inline/local asset (e.g. bundle a referenced asset into the target).
	ProposalInlineDeps ProposalIntent = "inline_deps"
	// ProposalExpandScope widens the target boundary to cover additional files
	// (SYSTEMIC: $prompt ONLY. Never offered under $hot).
	ProposalExpandScope ProposalIntent = "expand_scope"
	// ProposalRepairFirst repairs a corrupt AST before any mutation.
	ProposalRepairFirst ProposalIntent = "repair_first"
	// ProposalReduceScope narrows the target boundary to stay within budget.
	// It is retained for backward compatibility; a budget-infeasible target is
	// now primarily offered the typed recovery intents below.
	ProposalReduceScope ProposalIntent = "reduce_scope"
	// ProposalRescopeBoundedPatch re-scopes an infeasible full-rewrite contract
	// into an explicitly authorized bounded SEARCH/REPLACE patch contract. It
	// MUST create a NEW execution contract and re-run preflight — it never
	// bypasses a safety gate (hence "rescope", never "force").
	ProposalRescopeBoundedPatch ProposalIntent = "rescope_bounded_patch"
	// ProposalRetryExplicitBudget re-runs the same objective under an
	// explicitly authorized, validated output budget. It creates a NEW
	// execution contract with the raised ceiling and re-runs preflight; a
	// budget is never silently raised.
	ProposalRetryExplicitBudget ProposalIntent = "retry_with_explicit_budget"
	// ProposalInspect exposes the diagnostic details of the preflight failure
	// and remains safe: zero mutation, zero execution. It is a read-only hold.
	ProposalInspect ProposalIntent = "inspect"
	// ProposalCancel abandons the objective with zero mutation and zero spend.
	ProposalCancel ProposalIntent = "cancel"
	// ProposalInjectLineOffset appends explicit line ranges [L<start>-L<end>]
	// to the active target context and re-triggers preflight with restricted
	// bounds. It is the N>1 ambiguous-anchor recovery that disambiguates
	// via line-offset injection.
	ProposalInjectLineOffset ProposalIntent = "inject_line_offset"
	// ProposalFullFileFallback dynamically updates execution scope capabilities
	// to allow full-file overwrite (overwrite_allowed = true), bypasses RMAH
	// Tier 3 bounded patch requirement, and routes payload to the direct writer.
	ProposalFullFileFallback ProposalIntent = "full_file_fallback"
	// ProposalRepromptFullText re-prompts the model with full text context
	// for N=0 hallucinated anchor failures (zero match).
	ProposalRepromptFullText ProposalIntent = "reprompt_full_text"
	// ProposalAbortRun is the human-friendly hard-block abort: it cancels
	// the run gracefully (no SIGINT/Ctrl+C required) and returns the
	// session to a clean idle state. It is offered on HARD-BLOCK
	// DecisionSurfaces (output budget exhausted twice, format
	// failures >=2, ambiguous anchors). Distinct from ProposalCancel
	// (the fail-fast path on every preflight closure) so the UI can
	// render it with a human-readable label that promises "Return to
	// Idle" semantics.
	ProposalAbortRun ProposalIntent = "abort_run"
	// ProposalForceBoundedPatch is the human-authorized escape from a
	// hard-block DecisionSurface: it OVERRIDES the syntax check and
	// forces the executor to attempt a strictly local SEARCH/REPLACE
	// patch on the AST error offset, even when the structural
	// validator would otherwise refuse. It creates a NEW execution
	// contract and re-runs preflight under the patched shape; the
	// safety gate is bypassed ONLY because the human explicitly
	// authorized it.
	ProposalForceBoundedPatch ProposalIntent = "force_bounded_patch"
	// ProposalSwitchModel opens the model picker modal so the human
	// can re-target the current run at a model with a higher output
	// token ceiling. It is offered on HARD-BLOCK DecisionSurfaces
	// where the only viable path is a different model. The
	// composition root binds the picker — the runtime never resolves
	// a model outside the explicitly authorized human choice.
	ProposalSwitchModel ProposalIntent = "switch_model"
)

// Valid reports whether the intent is a member of the closed vocabulary.
func (i ProposalIntent) Valid() bool {
	switch i {
	case ProposalInlineDeps, ProposalExpandScope, ProposalRepairFirst,
		ProposalReduceScope, ProposalRescopeBoundedPatch,
		ProposalRetryExplicitBudget, ProposalInspect, ProposalCancel,
		ProposalInjectLineOffset, ProposalFullFileFallback, ProposalRepromptFullText,
		ProposalAbortRun, ProposalForceBoundedPatch, ProposalSwitchModel:
		return true
	}
	return false
}

// IsRecovery reports whether the intent is a recovery action that creates a
// NEW execution contract (as opposed to a terminal/abort or a read-only hold).
func (i ProposalIntent) IsRecovery() bool {
	return i == ProposalRescopeBoundedPatch || i == ProposalRetryExplicitBudget ||
		i == ProposalInjectLineOffset || i == ProposalFullFileFallback || i == ProposalRepromptFullText ||
		i == ProposalForceBoundedPatch || i == ProposalSwitchModel
}

// IsCancel reports whether the intent abandons the objective.
func (i ProposalIntent) IsCancel() bool {
	return i == ProposalCancel || i == ProposalAbortRun
}

// IsInspect reports whether the intent is the read-only diagnostic hold.
func (i ProposalIntent) IsInspect() bool { return i == ProposalInspect }

// ParseProposalIntent normalizes a raw intent string into a ProposalIntent.
// It accepts exact String values, option index strings ("1", "2"), and raw ID
// strings, mapping them to the canonical closed vocabulary. It handles the TUI
// DecisionSurface Option IDs ("full_file_fallback", "reprompt_full_text",
// "inject_line_offset") and legacy alias variants.
func ParseProposalIntent(raw string) ProposalIntent {
	s := strings.TrimSpace(raw)
	// Direct canonical match is the fast path.
	switch ProposalIntent(s) {
	case ProposalInlineDeps, ProposalExpandScope, ProposalRepairFirst,
		ProposalReduceScope, ProposalRescopeBoundedPatch,
		ProposalRetryExplicitBudget, ProposalInspect, ProposalCancel,
		ProposalInjectLineOffset, ProposalFullFileFallback, ProposalRepromptFullText,
		ProposalAbortRun, ProposalForceBoundedPatch, ProposalSwitchModel:
		return ProposalIntent(s)
	}
	lower := strings.ToLower(s)
	// Normalized alias handling.
	switch lower {
	case "1", "full_file_fallback", "intentfullfilefallback", "proposalfullfilefallback":
		return ProposalFullFileFallback
	case "2", "reprompt_full_text", "intentrepromptfulltext", "proposalrepromptfulltext":
		return ProposalRepromptFullText
	case "inject_line_offset", "intentinjectlineoffset", "proposalinjectlineoffset":
		return ProposalInjectLineOffset
	case "inline_deps", "intentinline_deps", "intentinlineDeps":
		return ProposalInlineDeps
	case "expand_scope", "intentexpand_scope":
		return ProposalExpandScope
	case "repair_first", "intentrepair_first":
		return ProposalRepairFirst
	case "reduce_scope", "intentreduce_scope":
		return ProposalReduceScope
	case "rescope_bounded_patch", "rescope_textual_patch", "intentrescopeboundedpatch":
		return ProposalRescopeBoundedPatch
	case "retry_with_explicit_budget", "intentretryexplicitbudget":
		return ProposalRetryExplicitBudget
	case "inspect", "intentinspect":
		return ProposalInspect
	case "cancel", "intentcancel":
		return ProposalCancel
	case "abort_run", "intentabort_run", "intentabortrun":
		return ProposalAbortRun
	case "force_bounded_patch", "intentforce_bounded_patch", "intentforceboundedpatch":
		return ProposalForceBoundedPatch
	case "switch_model", "intentswitch_model", "intentswitchmodel":
		return ProposalSwitchModel
	}
	// Fallback: case-insensitive canonical check.
	for _, valid := range []ProposalIntent{
		ProposalInlineDeps, ProposalExpandScope, ProposalRepairFirst,
		ProposalReduceScope, ProposalRescopeBoundedPatch,
		ProposalRetryExplicitBudget, ProposalInspect, ProposalCancel,
		ProposalInjectLineOffset, ProposalFullFileFallback, ProposalRepromptFullText,
		ProposalAbortRun, ProposalForceBoundedPatch, ProposalSwitchModel,
	} {
		if strings.EqualFold(string(valid), s) {
			return valid
		}
	}
	return ProposalIntent(s)
}

// ProposalOption is one selectable entry on the decision surface. It carries
// only presentation + intent data — NO functional callback.
type ProposalOption struct {
	ID          string         `json:"id"`
	Label       string         `json:"label"`
	Description string         `json:"description"`
	Intent      ProposalIntent `json:"intent"`
}

// DecisionSurface is the pure data surface the TUI modal renders. It is the
// ONLY user-facing strategy gateway for a preflight barrier. It never holds a
// callback and never mutates state.
type DecisionSurface struct {
	Target            string    `json:"target"`
	ASTStatus         ASTStatus `json:"ast_status"`
	ExternalRefsCount int       `json:"external_refs_count"`
	EstimatedTokens   int       `json:"estimated_tokens"`
	CurrentBudget     int       `json:"current_budget"`
	// Reason names the TRUE cause of the closed gate (barrierReason), so the
	// human sees exactly why execution was refused.
	Reason string `json:"reason"`
	// FailureCategory is the typed preflight failure category
	// (budget_exceeded / ast_corrupt / ...). It is the semantic key the state
	// machine and the UI branch on — never a log string.
	FailureCategory PreflightFailureCategory `json:"failure_category"`
	// ExplicitBudget is the minimum explicitly authorized output ceiling that
	// would make the failed estimate feasible (estimate rounded up to the next
	// 1024-token boundary). It is 0 unless the gate closed on budget
	// infeasibility. It is NEVER applied silently: the human explicitly selects
	// retry_with_explicit_budget and the driver creates a NEW contract carrying
	// it, then re-runs preflight.
	ExplicitBudget int              `json:"explicit_budget,omitempty"`
	Options        []ProposalOption `json:"options"`
}

// Option returns the first option with the given intent, or nil.
func (s DecisionSurface) Option(intent ProposalIntent) *ProposalOption {
	for i := range s.Options {
		if s.Options[i].Intent == intent {
			return &s.Options[i]
		}
	}
	return nil
}

// Has reports whether an option with the given intent is present.
func (s DecisionSurface) Has(intent ProposalIntent) bool { return s.Option(intent) != nil }

// IsSystemic reports whether the subcommand is $prompt (systemic/advisory).
// It is the single authority on the systemic-vs-local split.
func IsSystemic(subcommand string) bool {
	return strings.TrimSpace(subcommand) == "$prompt"
}

// IsHot reports whether the subcommand is $hot (strict local target boundary).
func IsHot(subcommand string) bool { return strings.TrimSpace(subcommand) == "$hot" }

// countExternalRefs tallies unresolved external file references carried by the
// evaluation's bounded evidence findings. It is a read-only projection — never
// an I/O call.
func countExternalRefs(eval PreflightEvaluation) int {
	n := 0
	for _, f := range eval.Findings {
		if strings.Contains(f, "missing local file") {
			n++
		}
	}
	if n == 0 && eval.DependencyStatus == DependenciesUnresolved {
		n = 1
	}
	return n
}

// buildProposalOptions implements the per-subcommand policy. It is extracted so
// the public BuildDecisionSurface stays a thin projection over the policy.
//
// Recovery actions are typed, never lexical: a budget-infeasible target is
// offered ProposalRescopeBoundedPatch / ProposalRetryExplicitBudget (NOT a
// "force_bounded_patch" bypass — recovery never implies weakening a safety
// gate), and a corrupt-AST target is offered a bounded TEXTUAL patch as an
// explicitly authorized strategy — never structural DAG decomposition.
func buildProposalOptions(eval PreflightEvaluation, subcommand string) []ProposalOption {
	opts := make([]ProposalOption, 0, 6)

	unresolved := eval.DependencyStatus == DependenciesUnresolved
	corrupt := eval.ASTStatus == ASTCorrupt
	overBudget := eval.BudgetStatus == BudgetExceeded

	// ── BUDGET INFEASTBILITY RECOVERY (valid-AST, budget-exceeded) ─────
	// The estimate exceeded max_output under the full-rewrite accounting, so
	// the recoverable strategies are the bounded SEARCH/REPLACE re-scope and
	// an explicitly authorized higher budget — never a silently raised ceiling
	// and never a keyword-faked multiplier.
	if overBudget && !corrupt {
		opts = append(opts,
			ProposalOption{
				ID:          "rescope_bounded_patch",
				Label:       "Re-scope to bounded SEARCH/REPLACE",
				Description: "Create a new bounded-patch execution contract and re-run preflight under it.",
				Intent:      ProposalRescopeBoundedPatch,
			},
			ProposalOption{
				ID:          "retry_with_explicit_budget",
				Label:       "Retry with explicit max_output",
				Description: "Create a new contract under an explicitly authorized output budget and re-run preflight.",
				Intent:      ProposalRetryExplicitBudget,
			},
		)
	}

	switch {
	case IsSystemic(subcommand):
		// $prompt: systemic/advisory. Scope expansion and inline conversion
		// are legitimate advisory strategies.
		if unresolved {
			opts = append(opts,
				ProposalOption{
					ID:          "inline_deps",
					Label:       "Inline dependencies",
					Description: "Convert unresolved external references into local/inline assets within the target.",
					Intent:      ProposalInlineDeps,
				},
				ProposalOption{
					ID:          "expand_scope",
					Label:       "Expand scope",
					Description: "Widen the target boundary to include the missing external files.",
					Intent:      ProposalExpandScope,
				},
			)
		}
		if corrupt {
			opts = append(opts,
				ProposalOption{
					ID:          "repair_first",
					Label:       "Repair AST first",
					Description: "Repair the structurally corrupt target before mutating.",
					Intent:      ProposalRepairFirst,
				},
				ProposalOption{
					ID:          "rescope_bounded_patch",
					Label:       "Bounded textual SEARCH/REPLACE",
					Description: "Explicitly authorize a bounded TEXTUAL patch (never structural DAG) and re-run preflight.",
					Intent:      ProposalRescopeBoundedPatch,
				},
			)
		}
		if overBudget {
			opts = append(opts, ProposalOption{
				ID:          "reduce_scope",
				Label:       "Reduce scope",
				Description: "Narrow the target to fit the declared output budget.",
				Intent:      ProposalReduceScope,
			})
		}
		opts = append(opts, ProposalOption{
			ID:          "cancel",
			Label:       "Cancel",
			Description: "Abandon the objective with zero mutation and zero spend.",
			Intent:      ProposalCancel,
		})

	case IsHot(subcommand):
		// $hot: STRICT LOCAL TARGET BOUNDARY. ProposalExpandScope is filtered
		// out COMPLETELY under every condition. Unresolved dependencies yield
		// only fail-fast (cancel) or a local-only inline conversion. Corrupt
		// AST yields a local repair-first or fail-fast.
		if unresolved {
			opts = append(opts, ProposalOption{
				ID:          "inline_deps",
				Label:       "Inline dependencies (local)",
				Description: "Convert unresolved references into local assets within THIS file only.",
				Intent:      ProposalInlineDeps,
			})
		}
		if corrupt {
			opts = append(opts,
				ProposalOption{
					ID:          "repair_first",
					Label:       "Repair AST first (local)",
					Description: "Repair the corrupt target in place before mutating.",
					Intent:      ProposalRepairFirst,
				},
				ProposalOption{
					ID:          "rescope_bounded_patch",
					Label:       "Bounded textual SEARCH/REPLACE (local)",
					Description: "Explicitly authorize a bounded TEXTUAL patch within this file and re-run preflight.",
					Intent:      ProposalRescopeBoundedPatch,
				},
			)
		}
		// $hot NEVER offers reduce_scope for budget overflow that would imply
		// widening or systemic re-scoping; a strict local target stays put.
		if overBudget {
			opts = append(opts, ProposalOption{
				ID:          "reduce_scope",
				Label:       "Reduce scope (local)",
				Description: "Narrow the local target to fit the output budget.",
				Intent:      ProposalReduceScope,
			})
		}
		opts = append(opts, ProposalOption{
			ID:          "cancel",
			Label:       "Cancel / fail-fast",
			Description: "Abandon the objective immediately — no local mutation, zero spend.",
			Intent:      ProposalCancel,
		})

	default:
		// Unknown subcommand: conservative, closed-surface fallback. Offer only
		// the local repair and cancel — never scope expansion.
		if corrupt {
			opts = append(opts,
				ProposalOption{
					ID:          "repair_first",
					Label:       "Repair AST first",
					Description: "Repair the structurally corrupt target before mutating.",
					Intent:      ProposalRepairFirst,
				},
				ProposalOption{
					ID:          "rescope_bounded_patch",
					Label:       "Bounded textual SEARCH/REPLACE",
					Description: "Explicitly authorize a bounded TEXTUAL patch and re-run preflight.",
					Intent:      ProposalRescopeBoundedPatch,
				},
			)
		}
		opts = append(opts, ProposalOption{
			ID:          "cancel",
			Label:       "Cancel",
			Description: "Abandon the objective with zero mutation and zero spend.",
			Intent:      ProposalCancel,
		})
	}

	// The read-only inspect hold is offered on every closed gate: the human can
	// review the diagnostics without mutating anything.
	opts = append(opts, ProposalOption{
		ID:          "inspect",
		Label:       "Inspect diagnostics",
		Description: "Review the preflight failure details. No mutation, no execution.",
		Intent:      ProposalInspect,
	})

	return opts
}

// explicitBudgetFor computes the minimum explicitly authorized output ceiling
// that would make the failed estimate feasible: the estimate rounded up to the
// next 1024-token boundary (never below estimate+1). It is deterministic and
// advisory-only — the human must explicitly select retry_with_explicit_budget
// for it to be applied, and the driver re-runs preflight under it.
func explicitBudgetFor(estimated int) int {
	if estimated <= 0 {
		return 0
	}
	return ((estimated + 1023) / 1024) * 1024
}

// BuildDecisionSurface derives the interactive proposal surface from a
// ZERO-TOKEN PreflightEvaluation and the subcommand policy ($prompt vs $hot).
// It is a pure function: it never invokes a provider, never touches the
// filesystem, and never registers a callback. The returned DecisionSurface is
// plain data for the TUI modal to render and collapse into one ProposalIntent.
//
// The surface carries the authoritative budget facts: EstimatedTokens is the
// deterministic generation estimate that was judged against CurrentBudget
// (both 0 when the target is absent or the budget unbounded). The Reason names
// the TRUE cause of the closed gate.
func BuildDecisionSurface(eval PreflightEvaluation, subcommand string) DecisionSurface {
	opts := buildProposalOptions(eval, subcommand)
	// HARD-BLOCK OPTION ENFORCEMENT (Task 2): when the runtime parks
	// awaiting_human on a HARD-BLOCK failure (output exhausted twice,
	// format failures >=2, ambiguous anchors, output-budget guardrail
	// refusal), the surface MUST offer at least the three recovery
	// options so the UI can never deadlock:
	//   1) Abort Run & Return to Idle — graceful exit, no SIGINT
	//   2) Force Bounded Patch — local SEARCH/REPLACE, bypass checks
	//   3) Switch Model — re-target the run at a higher-budget model
	// The function appends the missing ones idempotently (never
	// duplicates an already-present intent), guaranteeing the surface
	// is resolvable from the keyboard no matter what the policy
	// produced. The hard-block options are policy-neutral: they are
	// offered on every failure category that the recovery matrix
	// can park awaiting_human on.
	if isHardBlockCategory(ClassifyPreflightFailure(eval)) {
		opts = appendHardBlockOptions(opts)
	}
	// MANDATORY INTERACTIVE FALLBACK (deadlock guard): the DecisionSurface is
	// the ONLY resolution path for a parked awaiting_human barrier. It must
	// NEVER carry an empty action set — an empty surface cannot be resolved via
	// keyboard input, which would strand the run forever. Cancel is always a
	// valid, zero-spend resolution; force it in as the guaranteed fallback even
	// when the policy produced nothing (a corrupt-AST / closed-gate surface
	// still offers Abort).
	if len(opts) == 0 {
		opts = append(opts, ProposalOption{
			ID:          "cancel",
			Label:       "Cancel",
			Description: "Abandon the objective with zero mutation and zero spend.",
			Intent:      ProposalCancel,
		})
	}
	surface := DecisionSurface{
		Target:            eval.Target,
		ASTStatus:         eval.ASTStatus,
		ExternalRefsCount: countExternalRefs(eval),
		EstimatedTokens:   eval.EstimatedTokens,
		CurrentBudget:     eval.MaxOutputTokens,
		Reason:            barrierReason(eval),
		FailureCategory:   ClassifyPreflightFailure(eval),
		Options:           opts,
	}
	// Enrich the retry_with_explicit_budget option with the concrete ceiling
	// the human is authorizing — it must be visible BEFORE selection, never a
	// silent raise.
	if eval.BudgetStatus == BudgetExceeded {
		surface.ExplicitBudget = explicitBudgetFor(eval.EstimatedTokens)
		for i := range surface.Options {
			if surface.Options[i].Intent == ProposalRetryExplicitBudget {
				surface.Options[i].Label = fmt.Sprintf("Retry with explicit max_output (%d)", surface.ExplicitBudget)
				surface.Options[i].Description = fmt.Sprintf(
					"Create a new contract under the explicitly authorized output budget of %d tokens and re-run preflight.",
					surface.ExplicitBudget)
			}
		}
	}
	return surface
}

// optionsFromSurface projects the runtime DecisionSurface options onto the
// scalar HumanProposalOption shape a HumanBoundary can carry across the
// autonomy boundary. It is the transport the UI renders from the authoritative
// parked boundary — no bus race, no log parsing.
func optionsFromSurface(s DecisionSurface) []autonomy.HumanProposalOption {
	out := make([]autonomy.HumanProposalOption, 0, len(s.Options))
	for _, opt := range s.Options {
		out = append(out, autonomy.HumanProposalOption{
			ID:          opt.ID,
			Label:       opt.Label,
			Description: opt.Description,
			Intent:      string(opt.Intent),
		})
	}
	return out
}

// surfaceOptionsToEvents projects the runtime DecisionSurface options onto the
// bus-transportable DecisionSurfaceOption shape.
func surfaceOptionsToEvents(s DecisionSurface) []events.DecisionSurfaceOption {
	out := make([]events.DecisionSurfaceOption, 0, len(s.Options))
	for _, opt := range s.Options {
		out = append(out, events.DecisionSurfaceOption{
			ID:          opt.ID,
			Label:       opt.Label,
			Description: opt.Description,
			Intent:      string(opt.Intent),
		})
	}
	return out
}

// ── Hard-Block Recovery Options (Task 2) ───────────────────────────────────
//
// A HARD-BLOCK parking is one the recovery matrix cannot resolve
// automatically: output exhausted twice, format failures >=2, ambiguous
// anchors, output-budget guardrail refusal. The runtime parks at
// awaiting_human with a typed HumanBoundaryProposal. An empty or
// unresolvable surface would deadlock the run — the human would have no
// way to recover short of SIGINT. The runtime's contract is:
//
//	1. The DecisionSurface MUST carry at least one selectable option.
//	2. The three hard-block recovery options MUST be available on every
//	   hard-block parking so the human can always:
//	     - Abort Run & Return to Idle (no SIGINT required)
//	     - Force Bounded Patch on the AST error offset
//	     - Switch Model to one with a higher output token ceiling
//	3. The options are appended idempotently: an already-present intent
//	   is never duplicated, so callers may invoke the helper on a
//	   pre-populated surface without surprises.

// isHardBlockCategory reports whether the typed preflight failure category
// is one the runtime parks awaiting_human on without an automatic
// recovery. The classification is a closed taxonomy: budget_exceeded,
// ast_corrupt, capability_denied, and the catch-all internal_error all
// can park. The hard-block guarantee fires on the entire recoverable
// surface so the human always sees the escape options.
func isHardBlockCategory(c PreflightFailureCategory) bool {
	switch c {
	case PreflightBudgetExceeded, PreflightASTCorrupt,
		PreflightCapabilityDenied, PreflightInternalError:
		return true
	default:
		return false
	}
}

// appendHardBlockOptions PREPENDS the three hard-block recovery options
// to an existing option list, preserving the original ordering of the
// policy-derived options, and skipping any intent that is already
// present. It is the canonical helper the BuildDecisionSurface caller
// and the recovery matrix both reach for when they need to ensure a
// parked surface is keyboard-resolvable.
//
// The options are prepended — not appended — so the human sees the safe
// default (Abort Run & Return to Idle) as Option [1] on the TUI modal,
// exactly as the directive specifies. The original policy options
// follow, retaining the $prompt/$hot semantics the policy layer chose.
func appendHardBlockOptions(opts []ProposalOption) []ProposalOption {
	hardBlockOptions := []ProposalOption{
		{
			ID:          "abort_run",
			Label:       "Abort Run & Return to Idle",
			Description: "Gracefully cancel the objective and return the session to a clean idle state — no SIGINT required.",
			Intent:      ProposalAbortRun,
		},
		{
			ID:          "force_bounded_patch",
			Label:       "Force Bounded Patch",
			Description: "Override the syntax check and attempt a strictly local SEARCH/REPLACE patch on the AST error offset.",
			Intent:      ProposalForceBoundedPatch,
		},
		{
			ID:          "switch_model",
			Label:       "Switch Model",
			Description: "Open the model picker to re-target the run at a model with a higher output token ceiling.",
			Intent:      ProposalSwitchModel,
		},
	}
	existing := make(map[ProposalIntent]struct{}, len(opts))
	for _, opt := range opts {
		existing[opt.Intent] = struct{}{}
	}
	out := make([]ProposalOption, 0, len(opts)+len(hardBlockOptions))
	for _, hbo := range hardBlockOptions {
		if _, ok := existing[hbo.Intent]; ok {
			continue
		}
		out = append(out, hbo)
	}
	out = append(out, opts...)
	return out
}

// EnsureHardBlockOptions is the public entry point. It is exported so the
// recovery matrix, the surface builder, and the test suite share one
// vocabulary for the hard-block guarantee. Callers may pass any surface
// state; the function is idempotent.
func EnsureHardBlockOptions(opts []ProposalOption) []ProposalOption {
	return appendHardBlockOptions(opts)
}
