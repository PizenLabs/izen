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
)

// Valid reports whether the intent is a member of the closed vocabulary.
func (i ProposalIntent) Valid() bool {
	switch i {
	case ProposalInlineDeps, ProposalExpandScope, ProposalRepairFirst,
		ProposalReduceScope, ProposalRescopeBoundedPatch,
		ProposalRetryExplicitBudget, ProposalInspect, ProposalCancel:
		return true
	}
	return false
}

// IsRecovery reports whether the intent is a recovery action that creates a
// NEW execution contract (as opposed to a terminal/abort or a read-only hold).
func (i ProposalIntent) IsRecovery() bool {
	return i == ProposalRescopeBoundedPatch || i == ProposalRetryExplicitBudget
}

// IsCancel reports whether the intent abandons the objective.
func (i ProposalIntent) IsCancel() bool { return i == ProposalCancel }

// IsInspect reports whether the intent is the read-only diagnostic hold.
func (i ProposalIntent) IsInspect() bool { return i == ProposalInspect }

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
