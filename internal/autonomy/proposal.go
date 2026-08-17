package autonomy

import "fmt"

// ProposalAction is one selectable action in the ask_user decision surface.
type ProposalAction string

// The canonical ask_user actions. They are the ONLY ways a human may resolve
// a proposal: authorize execution, inspect the proposal in detail, or cancel.
// There is no /grant command — granting is an internal authorization
// operation performed when Execute is selected.
const (
	ActionExecute ProposalAction = "execute"
	ActionInspect ProposalAction = "inspect"
	ActionCancel  ProposalAction = "cancel"
)

// String returns the canonical action label.
func (a ProposalAction) String() string { return string(a) }

// Proposal is the human decision surface for a DecisionAskUser verdict. It is
// a pure projection of the decision trace: intent, workspace, target, risk,
// the requested capability vector, rollback availability, and the planned
// high-level actions the runtime will take inside the granted boundary.
//
// The proposal is a UI concern only — the decision engine is authoritative and
// unchanged. A proposal exists ONLY when the controller returned ask_user
// (mutation authorization, risk acknowledgement, or target confirmation).
type Proposal struct {
	// Input is the original user objective the decision was made on. It is
	// preserved so the runtime can re-run/revalidate the decision after the
	// human authorizes, WITHOUT re-submitting the prompt through a command
	// parser.
	Input string
	// Intent is the classified intent.
	Intent Intent
	// Workspace is the selected capability domain.
	Workspace Workspace
	// Target is the resolved mutation target ("" when none).
	Target string
	// Risk is the classified mutation risk.
	Risk RiskLevel
	// Scope is the grant scope (workspace root).
	Scope string
	// Required is the full capability vector the intent demands.
	Required CapabilitySet
	// Missing is the capability vector the human must authorize.
	Missing CapabilitySet
	// Rollback reports whether a rollback checkpoint is available.
	Rollback bool
	// AffectedScope is the number of files the change would touch.
	AffectedScope int
	// Reason is the controller's justification.
	Reason string
	// Actions is the planned high-level execution chain.
	Actions []string
	// Decision is the original verdict (always ask_user when a proposal is
	// produced; carried for provenance).
	Decision Decision
}

// CapabilityLabel renders the requested capabilities for the proposal.
func (p *Proposal) CapabilityLabel() string {
	if len(p.Missing) > 0 {
		return p.Missing.String()
	}
	return p.Required.String()
}

// PlannedActions projects the high-level execution chain the runtime will
// follow inside the granted boundary. It is a pure description of the loop
// contract — it executes nothing.
func PlannedActions(i Intent) []string {
	switch i {
	case IntentModification, IntentRefactoring:
		return []string{
			"inspect target",
			"analyze structure",
			"propose change",
			"apply mutation",
			"verify + diagnose loop",
		}
	case IntentInvestigation, IntentDebugging:
		return []string{
			"gather evidence",
			"trace root cause",
			"report findings",
		}
	case IntentPlanning:
		return []string{
			"analyze context",
			"propose plan",
			"estimate impact",
		}
	case IntentVerification:
		return []string{
			"inspect target",
			"run verification",
			"report results",
		}
	default:
		return nil
	}
}

// Proposal builds the ask_user decision surface from a full decision trace.
// It is nil-safe: a zero Trace yields a zero Proposal.
func (t Trace) Proposal() *Proposal {
	return &Proposal{
		Input:         t.Input,
		Intent:        t.Intent.Intent,
		Workspace:     t.Route.Workspace,
		Target:        t.Intent.Target(),
		Risk:          t.Risk,
		Scope:         t.Grant.Scope,
		Required:      t.Intent.Required,
		Missing:       t.Decision.Missing,
		Rollback:      t.Rollback,
		AffectedScope: t.ScopeSize,
		Reason:        t.Decision.Reason,
		Actions:       PlannedActions(t.Intent.Intent),
		Decision:      t.Decision.Decision,
	}
}

// String renders the proposal compactly for logs and tests.
func (p *Proposal) String() string {
	return fmt.Sprintf(
		"proposal{intent=%s workspace=%s target=%q risk=%s missing=%s rollback=%t}",
		p.Intent, p.Workspace, p.Target, p.Risk, p.Missing, p.Rollback,
	)
}

// ── Mutation proposal contract ───────────────────────────────────────────────
//
// The MutationProposal is the engine-truth mutation contract a mutation MUST
// carry before it may be executed: the compiled structural evidence, the
// reason, the compiled diff, the classified risk and rollback availability.
// Nothing is executed from a proposal that is not Complete().

// MutationProposal bundles the five facts every mutation must be justified by:
// evidence, reason, diff, risk and rollback. It is a plain value record — the
// runtime fills it at the mutation boundary and the renderer projects it.
type MutationProposal struct {
	// File is the mutation target.
	File string `json:"file"`
	// Evidence is the compiled structural evidence the pipeline produced for
	// the target. It may be empty when the artifact is structurally clean.
	Evidence []Finding `json:"evidence"`
	// EvidenceLedger is the rendered evidence ledger. It is non-empty whenever
	// the context intelligence pipeline ran, even for clean artifacts, so it
	// doubles as the "evidence stage ran" flag.
	EvidenceLedger string `json:"evidence_ledger"`
	// Reason is the justification for the mutation.
	Reason string `json:"reason"`
	// Diff is the compiled unified diff the mutation will apply.
	Diff string `json:"diff"`
	// Risk is the classified mutation risk.
	Risk RiskLevel `json:"risk"`
	// Rollback reports whether a rollback checkpoint protects the mutation.
	Rollback bool `json:"rollback"`
}

// Complete reports whether the proposal carries the full mutation contract:
// compiled evidence, reason, diff, a classified risk and rollback coverage.
// A proposal that is not Complete() must never reach the mutation pipeline.
func (p MutationProposal) Complete() bool {
	return p.EvidenceLedger != "" &&
		p.Reason != "" &&
		p.Diff != "" &&
		p.Risk != RiskUnknown &&
		p.Rollback
}

// MutationProposalInput carries the raw ingredients BuildMutationProposal
// assembles into a MutationProposal. The context is the compiled artifact
// understanding produced by the pipeline.
type MutationProposalInput struct {
	File     string
	Context  ArtifactContext
	Reason   string
	Diff     string
	Risk     RiskLevel
	Rollback bool
}

// BuildMutationProposal assembles a MutationProposal from the compiled
// artifact context and the mutation facts. The evidence ledger is always
// derived from the context — a proposal never fabricates evidence.
func BuildMutationProposal(in MutationProposalInput) MutationProposal {
	file := in.File
	if file == "" {
		file = in.Context.Path
	}
	return MutationProposal{
		File:           file,
		Evidence:       in.Context.Evidence(),
		EvidenceLedger: in.Context.FormatEvidenceLedger(),
		Reason:         in.Reason,
		Diff:           in.Diff,
		Risk:           in.Risk,
		Rollback:       in.Rollback,
	}
}
