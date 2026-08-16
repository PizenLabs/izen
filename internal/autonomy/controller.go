package autonomy

import (
	"fmt"
	"time"
)

// Decision is the output of the autonomy controller: whether the runtime may
// continue without asking, must ask the user, or must stop entirely.
type Decision string

const (
	// DecisionDirectResponse answers conversation input directly. No execution
	// workspace is entered, no timeline is produced.
	DecisionDirectResponse Decision = "direct_response"
	// DecisionAutoContinue proceeds autonomously inside the granted capability
	// boundary. No approval is required for this step.
	DecisionAutoContinue Decision = "auto_continue"
	// DecisionAskUser suspends the runtime and requests human authorization
	// (a capability grant, a target confirmation, or a risk acknowledgement).
	DecisionAskUser Decision = "ask_user"
	// DecisionBlock stops the runtime. The requested action is outside the
	// capability authority and cannot be performed.
	DecisionBlock Decision = "block"
)

// String returns the canonical decision label.
func (d Decision) String() string {
	return string(d)
}

// Continues reports whether the decision permits forward progress.
func (d Decision) Continues() bool {
	return d == DecisionAutoContinue || d == DecisionDirectResponse
}

// NeedsUser reports whether the decision requires a human turn.
func (d Decision) NeedsUser() bool {
	return d == DecisionAskUser
}

// MutationRiskInput carries the risk assessment of the mutation target. It is
// derived upstream (e.g. from the execution RiskClassifier) and normalized into
// the autonomy model so the controller never imports execution internals.
type MutationRiskInput struct {
	Level RiskLevel
	// Indicators is a compact list of risk indicators that produced the level
	// (e.g. "system path", "credential access"). Carried for observability.
	Indicators []string
}

// DecisionInput is the full decision model input: intent confidence, target
// confidence, mutation risk, affected scope, rollback availability and the
// granted capability set. The controller answers one question — "can I continue
// without asking?" — purely from these facts.
type DecisionInput struct {
	Intent            Intent
	IntentConfidence  float64
	TargetConfidence  float64
	Target            string
	MutationRisk      MutationRiskInput
	AffectedScope     int
	RollbackAvailable bool
	Granted           CapabilitySet
}

// DecisionOutput is the controller's verdict with the observable justification.
type DecisionOutput struct {
	Decision Decision
	Reason   string
	// Missing lists the required capabilities not covered by the grant. It is
	// empty when the decision does not demand new authority.
	Missing CapabilitySet
}

// AutonomyController is the decision runtime's gate. It is a pure function of
// DecisionInput: given identical facts it always produces the same verdict. It
// carries no mutable state — the grant ledger and loop are separate components
// that feed it inputs.
//
// Decision policy (optimize for correct decisions, not more messages):
//
//  1. Conversation is answered directly — no workspace, no ask, no block.
//  2. A mutation request whose required capabilities are NOT granted asks for
//     a capability grant exactly once. After the grant, subsequent steps in the
//     same boundary auto-continue.
//  3. A granted mutation auto-continues unless risk is high/critical, the
//     target is ambiguous, the affected scope is large, or rollback is
//     unavailable — each of those raises ASK_USER, not BLOCK.
//  4. BLOCK is reserved for genuinely impossible actions: a forbidden intent
//     (mutation with no grant path) or a critical-risk action with no
//     rollback.
type AutonomyController struct{}

// NewAutonomyController builds a controller. It is stateless and safe for
// concurrent use.
func NewAutonomyController() *AutonomyController {
	return &AutonomyController{}
}

// Scope thresholds. A change spanning more files than MaxAutonomousScope asks
// for confirmation even when everything else is granted.
const (
	MaxAutonomousScope = 3
	// TargetConfidenceThreshold is the minimum certainty about the target
	// before the runtime mutates autonomously.
	TargetConfidenceThreshold = 0.7
	// IntentConfidenceThreshold is the minimum intent certainty before any
	// autonomous action.
	IntentConfidenceThreshold = 0.6
)

// Decide evaluates the decision model and returns the verdict.
func (c *AutonomyController) Decide(in DecisionInput) DecisionOutput {
	// Rule 1: conversation never enters an execution workspace.
	if in.Intent == IntentConversation {
		return DecisionOutput{
			Decision: DecisionDirectResponse,
			Reason:   "conversation intent — direct response, no workspace",
		}
	}

	if in.Intent == IntentUnknown {
		return DecisionOutput{
			Decision: DecisionAskUser,
			Reason:   "intent could not be classified — clarify what the user wants",
		}
	}

	// Low intent confidence never mutates autonomously.
	if in.IntentConfidence < IntentConfidenceThreshold {
		return DecisionOutput{
			Decision: DecisionAskUser,
			Reason: fmt.Sprintf(
				"intent confidence %.0f%% below autonomous threshold %.0f%% — clarify",
				in.IntentConfidence*100, IntentConfidenceThreshold*100),
		}
	}

	// ── Read-only intent ─────────────────────────────────────────────
	// Read-only capabilities (read/analyze/propose/verify) are inherent to
	// their workspace contracts — no grant is ever required for them. Only a
	// genuinely ambiguous target asks.
	if !in.Intent.RequiresMutation() {
		if in.Target != "" && in.TargetConfidence < TargetConfidenceThreshold {
			return DecisionOutput{
				Decision: DecisionAskUser,
				Reason: fmt.Sprintf(
					"target %q ambiguous (confidence %.0f%%) — confirm target",
					in.Target, in.TargetConfidence*100),
			}
		}
		return DecisionOutput{
			Decision: DecisionAutoContinue,
			Reason:   "read-only intent — capabilities inherent to workspace contract",
		}
	}

	// ── Mutation intent ──────────────────────────────────────────────
	// The mutation capability is the ONLY capability that requires explicit
	// human authorization. This is the single grant request; once granted it
	// stays granted for the session/scope boundary.
	required := RequiredCapabilities(in.Intent)
	missing := missingCaps(required, in.Granted)
	if !in.Granted.Has(CapMutate) || len(missing) > 0 {
		if len(missing) == 0 {
			missing = CapabilitySet{CapMutate}
		}
		return DecisionOutput{
			Decision: DecisionAskUser,
			Missing:  missing,
			Reason: fmt.Sprintf(
				"capability %s not granted for scope — request BUILD authorization",
				missing.String()),
		}
	}

	switch in.MutationRisk.Level {
	case RiskCritical:
		if !in.RollbackAvailable {
			return DecisionOutput{
				Decision: DecisionBlock,
				Reason:   "critical-risk mutation with no rollback — blocked",
			}
		}
		return DecisionOutput{
			Decision: DecisionAskUser,
			Reason:   "critical-risk mutation — human acknowledgement required",
		}
	case RiskHigh:
		return DecisionOutput{
			Decision: DecisionAskUser,
			Reason:   "high-risk mutation — human acknowledgement required",
		}
	}

	if in.Target == "" || in.TargetConfidence < TargetConfidenceThreshold {
		return DecisionOutput{
			Decision: DecisionAskUser,
			Reason:   "mutation target missing or ambiguous — confirm before writing",
		}
	}

	if in.AffectedScope > MaxAutonomousScope {
		return DecisionOutput{
			Decision: DecisionAskUser,
			Reason: fmt.Sprintf(
				"affected scope %d files exceeds autonomous boundary %d — confirm",
				in.AffectedScope, MaxAutonomousScope),
		}
	}

	if !in.RollbackAvailable {
		return DecisionOutput{
			Decision: DecisionAskUser,
			Reason:   "no rollback checkpoint available — confirm before writing",
		}
	}

	// Everything granted and bounded → proceed.
	return DecisionOutput{
		Decision: DecisionAutoContinue,
		Reason: fmt.Sprintf(
			"mutation granted (risk %s, scope %d, rollback available)",
			in.MutationRisk.Level, in.AffectedScope),
	}
}

// missingCaps returns the required capabilities not present in the grant.
func missingCaps(required, granted CapabilitySet) CapabilitySet {
	var missing CapabilitySet
	for _, cap := range required {
		if !granted.Has(cap) {
			missing = append(missing, cap)
		}
	}
	return missing
}

// GrantRequest is a structured capability authorization request surfaced to the
// user. The runtime asks exactly once per (capability, scope) pair, never per
// file — that is the "no repeated approvals" guarantee.
type GrantRequest struct {
	Scope        string
	Required     CapabilitySet
	Intent       Intent
	Target       string
	Risk         RiskLevel
	AffectedFile int
	RequestedAt  time.Time
}

// NewGrantRequest builds the authorization request the user approves.
func NewGrantRequest(scope string, required CapabilitySet, intent Intent, target string, risk RiskLevel, files int) GrantRequest {
	return GrantRequest{
		Scope:        scope,
		Required:     required,
		Intent:       intent,
		Target:       target,
		Risk:         risk,
		AffectedFile: files,
		RequestedAt:  time.Now().UTC(),
	}
}
