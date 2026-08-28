package autonomy

import "strings"

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
	ProposalReduceScope ProposalIntent = "reduce_scope"
	// ProposalCancel abandons the objective with zero mutation and zero spend.
	ProposalCancel ProposalIntent = "cancel"
)

// Valid reports whether the intent is a member of the closed vocabulary.
func (i ProposalIntent) Valid() bool {
	switch i {
	case ProposalInlineDeps, ProposalExpandScope, ProposalRepairFirst,
		ProposalReduceScope, ProposalCancel:
		return true
	}
	return false
}

// IsCancel reports whether the intent abandons the objective.
func (i ProposalIntent) IsCancel() bool { return i == ProposalCancel }

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
	Target            string           `json:"target"`
	ASTStatus         ASTStatus        `json:"ast_status"`
	ExternalRefsCount int              `json:"external_refs_count"`
	EstimatedTokens   int              `json:"estimated_tokens"`
	CurrentBudget     int              `json:"current_budget"`
	Options           []ProposalOption `json:"options"`
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
func buildProposalOptions(eval PreflightEvaluation, subcommand string) []ProposalOption {
	opts := make([]ProposalOption, 0, 5)

	unresolved := eval.DependencyStatus == DependenciesUnresolved
	corrupt := eval.ASTStatus == ASTCorrupt
	overBudget := eval.BudgetStatus == BudgetExceeded

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
			opts = append(opts, ProposalOption{
				ID:          "repair_first",
				Label:       "Repair AST first",
				Description: "Repair the structurally corrupt target before mutating.",
				Intent:      ProposalRepairFirst,
			})
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
			opts = append(opts, ProposalOption{
				ID:          "repair_first",
				Label:       "Repair AST first (local)",
				Description: "Repair the corrupt target in place before mutating.",
				Intent:      ProposalRepairFirst,
			})
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
			opts = append(opts, ProposalOption{
				ID:          "repair_first",
				Label:       "Repair AST first",
				Description: "Repair the structurally corrupt target before mutating.",
				Intent:      ProposalRepairFirst,
			})
		}
		opts = append(opts, ProposalOption{
			ID:          "cancel",
			Label:       "Cancel",
			Description: "Abandon the objective with zero mutation and zero spend.",
			Intent:      ProposalCancel,
		})
	}

	return opts
}

// BuildDecisionSurface derives the interactive proposal surface from a
// ZERO-TOKEN PreflightEvaluation and the subcommand policy ($prompt vs $hot).
// It is a pure function: it never invokes a provider, never touches the
// filesystem, and never registers a callback. The returned DecisionSurface is
// plain data for the TUI modal to render and collapse into one ProposalIntent.
//
// The surface carries the budget status projection as a coarse numeric:
// CurrentBudget is 0 (unknown/unbounded) unless a bounded budget overflow was
// recorded, in which case it is surfaced as the over-budget marker. Callers
// that hold the authoritative budget may overwrite it before rendering.
func BuildDecisionSurface(eval PreflightEvaluation, subcommand string) DecisionSurface {
	return DecisionSurface{
		Target:            eval.Target,
		ASTStatus:         eval.ASTStatus,
		ExternalRefsCount: countExternalRefs(eval),
		EstimatedTokens:   estimatedFromBudget(eval.BudgetStatus),
		CurrentBudget:     budgetFromStatus(eval.BudgetStatus),
		Options:           buildProposalOptions(eval, subcommand),
	}
}

// estimatedFromBudget projects a coarse estimated-token figure from the
// zero-token budget status. It is advisory only — the authoritative per-attempt
// budget lives on the execution request.
func estimatedFromBudget(status BudgetStatus) int {
	if status == BudgetExceeded {
		return -1 // marker: exceeded the declared ceiling (exact figure unknown)
	}
	return 0
}

// budgetFromStatus projects the CurrentBudget field. 0 means unknown/unbounded.
func budgetFromStatus(status BudgetStatus) int {
	if status == BudgetExceeded {
		return -1
	}
	return 0
}
