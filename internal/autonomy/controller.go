package autonomy

import (
	"errors"
	"fmt"
	"sync"
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

// ── Canonical Intent Authority (Phase 14) ────────────────────────────────────
//
// A single execution lifecycle has EXACTLY ONE canonical resolved intent. The
// failure this type exists to prevent is a lifecycle in which three components
// concurrently believe different things:
//
//	Preflight/Parser  → "ask"        (read-only, no workspace mutation)
//	Context Compiler   → "ask"        (read-only context policy compiled)
//	Autonomy           → "modification" (mutation authority requested)
//
// Under that split the compiler is fitting a read-only context for a request
// that then mutates the workspace, and no component can say which contract the
// run is really under. The fix is a BLOCKING REVISION: elevating the intent is
// a transaction that invalidates the previous intent AND every artefact derived
// from it (the cached context descriptor, the compiled payload, the contract
// descriptor), synchronizes the canonical intent, and refuses to hand the run
// back to the driver until the context has been re-compiled under the new one.
//
// The transaction is deliberately fail-closed: a revision that cannot complete
// leaves the authority in the REVISING state, and `Ready()` reports false. No
// caller can proceed on a half-revised intent.

// IntentPhase is the lifecycle position of the canonical intent authority.
type IntentPhase string

const (
	// IntentUnresolved: no intent has been resolved yet.
	IntentUnresolved IntentPhase = "UNRESOLVED"
	// IntentResolved: one intent is canonical and its context is valid for it.
	IntentResolved IntentPhase = "RESOLVED"
	// IntentRevising: a blocking revision is in flight. The previous intent and
	// its derived artefacts are INVALID until the revision completes.
	IntentRevising IntentPhase = "REVISING"
)

// IntentRevision is the record of ONE blocking intent revision. It is the audit
// trail that explains why a compiled context was discarded.
type IntentRevision struct {
	// From is the invalidated intent.
	From Intent
	// To is the canonical intent after the revision.
	To Intent
	// Reason is the human/machine-readable justification.
	Reason string
	// Revision is the 1-indexed revision counter of the lifecycle.
	Revision int
	// At is the wall-clock instant of the revision.
	At time.Time
	// ContextInvalidated names the derived artefacts the revision dropped. A
	// revision that invalidated nothing was not a revision.
	ContextInvalidated bool
}

// ErrIntentRevisionIncomplete is returned when a caller asks for the canonical
// intent while a blocking revision is still in flight. It exists so a stale
// caller fails loudly instead of proceeding on the previous intent.
var ErrIntentRevisionIncomplete = errors.New("autonomy: intent revision in flight — the canonical intent is not yet usable")

// ErrIntentDowngradeRejected is returned when a caller attempts to LOWER the
// canonical intent (e.g. modification → ask) through the revision path. A
// mutation authority, once granted inside a lifecycle, cannot be silently
// withdrawn by a later classifier reading; only a fresh lifecycle can.
var ErrIntentDowngradeRejected = errors.New("autonomy: intent downgrade rejected — a new lifecycle is required to lower authority")

// IntentAuthority is the single canonical intent holder for one execution
// lifecycle. It is safe for concurrent use: preflight, the context compiler and
// the driver all read it, and only the authority advances it.
type IntentAuthority struct {
	mu       sync.RWMutex
	phase    IntentPhase
	intent   Intent
	revision int
	// contextRevision binds the compiled context to the intent revision that
	// produced it. A context compiled under an older revision is invalid.
	contextRevision int
	// contextValid records whether the currently compiled context satisfies the
	// ACTIVE intent's semantic contract (see ValidateContextProvenance).
	contextValid bool
	// contextIntent is the intent the currently compiled context was built for.
	contextIntent Intent
	last          IntentRevision
}

// NewIntentAuthority returns an authority with no resolved intent.
func NewIntentAuthority() *IntentAuthority {
	return &IntentAuthority{phase: IntentUnresolved}
}

// Phase returns the authority's lifecycle position.
func (a *IntentAuthority) Phase() IntentPhase {
	if a == nil {
		return IntentUnresolved
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.phase
}

// Resolve installs the FIRST canonical intent of the lifecycle. It is a no-op
// when an intent is already resolved, because the first resolution is the
// preflight/parser's classification and a second, un-revised call must never
// silently overwrite it.
func (a *IntentAuthority) Resolve(classified Intent) Intent {
	if a == nil {
		return IntentUnknown
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.phase == IntentUnresolved {
		a.intent = classified
		a.phase = IntentResolved
	}
	return a.intent
}

// Current returns the canonical intent. It fails CLOSED while a revision is in
// flight: the caller must wait for the transaction instead of reading the
// invalidated value.
func (a *IntentAuthority) Current() (Intent, error) {
	if a == nil {
		return IntentUnknown, nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.phase == IntentRevising {
		return IntentUnknown, ErrIntentRevisionIncomplete
	}
	return a.intent, nil
}

// Revision returns the current revision counter (0 before the first revision).
func (a *IntentAuthority) Revision() int {
	if a == nil {
		return 0
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.revision
}

// LastRevision returns the most recent revision record.
func (a *IntentAuthority) LastRevision() IntentRevision {
	if a == nil {
		return IntentRevision{}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.last
}

// Elevate performs the BLOCKING INTENT REVISION: it elevates the canonical
// intent to `to`, invalidates the previous intent together with every artefact
// derived from it, and synchronizes the canonical intent in one transaction.
//
// The consequences, all of them mandatory:
//
//   - the previous intent is INVALID — Current() no longer returns it;
//   - the cached context descriptor and the compiled payload are DROPPED, so no
//     read-only context compiled for the old intent can be reused;
//   - the context is marked INVALID for the new intent, so `Ready()` is false
//     until it is re-compiled under the new contract;
//   - the caller MUST re-compile before invoking the driver, and the returned
//     record is the proof that it did so.
//
// Elevating to the intent that is already canonical is a no-op: a revision that
// changes nothing would otherwise invalidate a perfectly valid context for no
// reason. Lowering the intent is rejected outright (ErrIntentDowngradeRejected)
// — authority, once held, is not withdrawn inside a lifecycle.
func (a *IntentAuthority) Elevate(to Intent, reason string) (IntentRevision, error) {
	if a == nil {
		return IntentRevision{}, errors.New("autonomy: nil intent authority")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.phase == IntentRevising {
		return IntentRevision{}, ErrIntentRevisionIncomplete
	}
	from := a.intent
	if to == from {
		return IntentRevision{From: from, To: to, Reason: reason, Revision: a.revision}, nil
	}
	if from.RequiresMutation() && !to.RequiresMutation() {
		return IntentRevision{}, fmt.Errorf("%w: %s -> %s", ErrIntentDowngradeRejected, from, to)
	}
	a.revision++
	a.phase = IntentRevising
	// SYNCHRONIZE: the canonical intent becomes the elevated one. This is the
	// single point where the lifecycle's intent changes, so a concurrent reader
	// can never observe a half-revised value.
	a.intent = to
	// Invalidate EVERY artefact derived from the previous intent. The context is
	// marked invalid for the NEW intent as well, because nothing has been
	// compiled under it yet.
	a.contextValid = false
	a.contextIntent = IntentUnknown
	a.contextRevision = 0
	rev := IntentRevision{
		From:               from,
		To:                 to,
		Reason:             reason,
		Revision:           a.revision,
		At:                 time.Now().UTC(),
		ContextInvalidated: true,
	}
	a.last = rev
	return rev, nil
}

// CommitContext records that the workspace context was (re-)compiled under the
// ACTIVE canonical intent and satisfies its semantic provenance contract. It is
// the second half of the blocking revision transaction: the lifecycle leaves
// REVISING only here, and only with an intent the compiler actually named.
func (a *IntentAuthority) CommitContext(under Intent, provenanceValid bool) error {
	if a == nil {
		return errors.New("autonomy: nil intent authority")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.phase != IntentRevising {
		// A commit outside a revision simply re-asserts the binding.
		a.contextIntent = under
		a.contextValid = provenanceValid
		a.contextRevision = a.revision
		if a.phase == IntentUnresolved && under != IntentUnknown {
			a.intent = under
			a.phase = IntentResolved
		}
		return nil
	}
	if under != a.intent {
		return fmt.Errorf("%w: context compiled under %q but the canonical intent is %q",
			ErrIntentRevisionIncomplete, under, a.intent)
	}
	a.contextIntent = under
	a.contextValid = provenanceValid
	a.contextRevision = a.revision
	if provenanceValid {
		a.phase = IntentResolved
	} else {
		// Fail closed: the revision stays open until a context that actually
		// satisfies the contract is committed.
		a.phase = IntentRevising
		return fmt.Errorf("%w: compiled context for %q does not satisfy its provenance contract", ErrIntentRevisionIncomplete, under)
	}
	return nil
}

// InvalidateContext drops the compiled context without changing the canonical
// intent. It is used when the workspace moved underneath a still-valid intent.
func (a *IntentAuthority) InvalidateContext() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.contextValid = false
	a.contextIntent = IntentUnknown
	a.contextRevision = 0
	if a.phase == IntentResolved {
		a.phase = IntentRevising
	}
}

// Ready reports whether the run may proceed: one canonical intent is resolved
// and the compiled context is valid FOR THAT INTENT. It is the gate the driver
// consults before dispatching.
func (a *IntentAuthority) Ready() bool {
	if a == nil {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.phase == IntentResolved && a.contextValid && a.contextIntent == a.intent
}

// ContextValid reports whether the compiled context currently satisfies the
// active intent's semantic provenance contract.
func (a *IntentAuthority) ContextValid() bool {
	if a == nil {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.contextValid && a.contextIntent == a.intent
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
