// Execution projection: the human-facing view of a runtime execution is a pure
// function of the canonical runtime event stream. The Runtime emits complete
// machine truth (execution.started → strategy.selected → target.resolved →
// context.prepared → model.invoked → provider.response → artifact.produced →
// approval.required → mutation.completed → verification.completed →
// execution.finished); this package REDUCES that stream into a concise human
// narrative (ExecutionNarrative) plus a single ExecutionViewState the renderer
// depends on.
//
// The UI never invents state: every narrative line and every state transition
// here is derived from an observed events.DomainEvent. A terminal event
// (execution.finished / execution.failed) ALWAYS transitions the state into a
// terminal phase, so no stale spinner can survive success, failure, or
// cancellation.
package presentation

import (
	"strings"

	"github.com/PizenLabs/izen/internal/events"
)

// ViewPhase is the phase of the single execution view state. The renderer
// depends ONLY on ExecutionViewState — never on scattered busy/spinner flags.
type ViewPhase uint8

// Canonical execution view phases.
const (
	// PhaseIdle is the resting state: no execution in flight and no terminal
	// result rendered.
	PhaseIdle ViewPhase = iota
	// PhaseRunning carries the current human step (Reading index.html,
	// Analyzing, Applying changes…) derived from the observed transitions. The
	// step is empty in the brief pre-transition window after execution.started.
	PhaseRunning
	// PhaseWaitingApproval blocks on the human approval gate.
	PhaseWaitingApproval
	// PhaseCompleted is a terminal state: the execution reached a real
	// terminal success (or a clean cancellation) AND the sealed evidence
	// substantiates it. No running step may follow.
	PhaseCompleted
	// PhaseFailed is a terminal state: the execution failed. No running step
	// may follow.
	PhaseFailed
	// PhaseUnsubstantiated is a terminal NON-success state: the runtime
	// terminated, but the sealed evidence does not substantiate a completion
	// claim (no record was published, the outcome is not COMMITTED, or the
	// mutation set is tainted). It exists so an unsubstantiated attempt can
	// never be rendered as a completed one. State carries the refusal reason.
	PhaseUnsubstantiated
)

// String returns the canonical phase name.
func (p ViewPhase) String() string {
	switch p {
	case PhaseRunning:
		return "running"
	case PhaseWaitingApproval:
		return "waiting-approval"
	case PhaseCompleted:
		return "completed"
	case PhaseFailed:
		return "failed"
	case PhaseUnsubstantiated:
		return "unsubstantiated"
	default:
		return "idle"
	}
}

// Terminal reports whether the phase is terminal (Completed, Failed or
// Unsubstantiated).
func (p ViewPhase) Terminal() bool {
	return p == PhaseCompleted || p == PhaseFailed || p == PhaseUnsubstantiated
}

// ExecutionViewState is the SINGLE projection state of a runtime execution. It
// is a pure function of the observed event stream — never independently mutated
// by the renderer.
type ExecutionViewState struct {
	// Phase is the execution phase.
	Phase ViewPhase
	// Step is the current human narrative step while Phase == PhaseRunning
	// (e.g. "Reading index.html", "Analyzing", "Applying changes").
	Step string
	// Outcome is the terminal outcome label when Phase is terminal (e.g.
	// "completed", "cancelled", "patch_failed").
	Outcome string
	// RequestID is the execution this state projects.
	RequestID string
	// Details is the accumulated runtime metadata (strategy, context policy,
	// model, token usage, duration, artifacts). It is populated by the reducer
	// from the observed payloads and is never authored by the renderer.
	Details ExecutionDetails
}

// NewIdle returns the resting execution view state.
func NewIdle() ExecutionViewState {
	return ExecutionViewState{Phase: PhaseIdle}
}

// Valid enforces the "no impossible states" invariant: a running state may be
// step-less only in the pre-first-human-step window (execution.started arrived
// but no human-visible transition has occurred yet), a terminal state must
// carry an outcome, and a terminal phase must never be followed by a running
// step (enforced by the reducer).
func (s ExecutionViewState) Valid() bool {
	switch s.Phase {
	case PhaseRunning:
		return true
	case PhaseWaitingApproval:
		return true
	case PhaseCompleted, PhaseFailed, PhaseUnsubstantiated:
		return s.Outcome != ""
	default:
		return s.Step == "" && s.Outcome == ""
	}
}

// ExecutionProjection reduces the canonical runtime event stream into the
// execution view state plus the ExecutionNarrative (human + machine). It is a
// single-execution projection: a new execution.started resets it.
type ExecutionProjection struct {
	state ExecutionViewState
	// details is the accumulated runtime metadata of the current execution. It
	// survives the terminal state reassignment (which rebuilds ExecutionViewState
	// wholesale) so the EXPANDED layer keeps its metadata at completion.
	details ExecutionDetails
	// gate accumulates the observed facts a completion claim is judged against.
	// See completion_gate.go: it is a reducer over published evidence, not an
	// authority of its own.
	gate CompletionGate
	// targetIndex maps a mutation target to its row in details.Targets.
	targetIndex map[string]int
	// narrative is the deterministic human/machine narrative layer. The UI
	// reads it; it never authors narration text.
	narrative *ExecutionNarrative
	// objective is the authoritative completion verdict the runtime published
	// for this execution (objective.evaluated). When observed it — not the raw
	// execution.finished success flag — decides whether the projection may show
	// a completed state. It is a projection of runtime truth, never a second
	// authority.
	objective objectiveVerdict
	// lastFinished retains the terminal execution.finished payload so a late
	// objective verdict can re-derive the terminal state without having to keep
	// the whole event stream.
	lastFinished *events.ExecutionFinishedPayload
}

// objectiveVerdict is the authoritative objective.evaluated projection.
type objectiveVerdict struct {
	observed bool
	outcome  string
	granted  bool
	reason   string
}

// NewExecutionProjection returns an idle single-execution projection.
func NewExecutionProjection() *ExecutionProjection {
	return &ExecutionProjection{state: NewIdle(), narrative: NewExecutionNarrative()}
}

// State returns the current projection state (read-only copy).
func (p *ExecutionProjection) State() ExecutionViewState {
	if p == nil {
		return NewIdle()
	}
	return p.state
}

// Active reports whether the projection is projecting a live or terminal
// execution (any state other than Idle).
func (p *ExecutionProjection) Active() bool {
	if p == nil {
		return false
	}
	return p.state.Phase != PhaseIdle
}

// HumanTimeline returns the human narrative sentences observed so far.
func (p *ExecutionProjection) HumanTimeline() []string {
	if p == nil {
		return nil
	}
	return p.narrative.Human()
}

// DebugTimeline returns the machine event records observed so far (the debug
// projection).
func (p *ExecutionProjection) DebugTimeline() []string {
	if p == nil {
		return nil
	}
	return p.narrative.Machine()
}

// HumanStep returns the current human narrative step for a Running state ("").
func (p *ExecutionProjection) HumanStep() string {
	if p == nil || p.state.Phase != PhaseRunning {
		return ""
	}
	return p.narrative.CurrentHuman()
}

// Narrative returns the execution narrative layer (for consumers that need the
// full machine/human separation). It is never nil after construction.
func (p *ExecutionProjection) Narrative() *ExecutionNarrative {
	if p == nil {
		return NewExecutionNarrative()
	}
	return p.narrative
}

// Begin binds the projection to a fresh execution BEFORE any event arrives. It
// is a pure reset — it never fabricates a narrative step or a running state:
// no real runtime event has been observed yet, so there is nothing truthful to
// render. The state stays Idle (Active() == false) until the first
// execution.started event is projected. RequestID is bound eagerly so a stale
// lifecycle event from a prior execution can never seed a fresh projection.
func (p *ExecutionProjection) Begin(requestID string) {
	if p == nil {
		return
	}
	*p = ExecutionProjection{
		state:     ExecutionViewState{RequestID: requestID},
		narrative: NewExecutionNarrative(),
	}
	// targetIndex is the target → ledger-position index of details.Targets. It
	// is rebuilt whenever the ledger is reset so a fresh execution can never
	// inherit a prior attempt's target rows.
	p.reindexTargets()
}

// reindexTargets rebuilds the target → ledger-position index.
func (p *ExecutionProjection) reindexTargets() {
	p.targetIndex = make(map[string]int, len(p.details.Targets))
	for i := range p.details.Targets {
		p.targetIndex[p.details.Targets[i].Target] = i
	}
}

// targetSlot returns the ledger row for a target, creating it on first
// observation. A target's row is created from an OBSERVED event only, so the
// ledger never contains a row for work that was never announced.
func (p *ExecutionProjection) targetSlot(target string) int {
	if p.targetIndex == nil {
		p.targetIndex = make(map[string]int)
	}
	if i, ok := p.targetIndex[target]; ok {
		return i
	}
	p.details.Targets = append(p.details.Targets, TargetEvidence{Target: target})
	p.targetIndex[target] = len(p.details.Targets) - 1
	return p.targetIndex[target]
}

// recountTargets recomputes the candidate and mutated-file counters from the
// ledger. Both are counts of OBSERVED evidence, so they are derived once, here,
// rather than incremented at each event site where a double arrival would
// inflate them.
func (p *ExecutionProjection) recountTargets() {
	candidates, mutated := 0, 0
	for i := range p.details.Targets {
		if p.details.Targets[i].Candidate {
			candidates++
		}
		if p.details.Targets[i].Mutated() {
			mutated++
		}
	}
	p.details.CandidateCount = candidates
	p.details.MutatedFiles = mutated
}

// Project consumes one canonical runtime lifecycle event and advances the
// projection. Events of other types and events for a stale (already-terminal)
// execution are ignored. A new execution.started (fresh request) resets the
// projection.
//
// INVARIANT: a terminal event ALWAYS transitions the state into a terminal
// phase. After a terminal phase, no running step can be rendered.
func (p *ExecutionProjection) Project(ev events.DomainEvent) {
	if p == nil || ev == nil {
		return
	}
	payload := ev.Payload()
	if payload == nil {
		return
	}
	// The narrative always records the event (it manages request binding and
	// reset internally).
	p.narrative.Project(ev)

	switch pl := payload.(type) {
	case events.ExecutionStartedPayload:
		// Fresh execution: reset the projection (single-execution scope). The
		// step stays empty — no human-visible transition has occurred yet; the
		// step is derived from the narrative as real transitions arrive.
		*p = ExecutionProjection{
			state:     ExecutionViewState{Phase: PhaseRunning, RequestID: pl.RequestID},
			narrative: p.narrative,
		}
		p.details.StartedAt = ev.Timestamp()
		p.syncDetails()
	case events.StrategySelectedPayload:
		if !p.matches(pl.RequestID) {
			return
		}
		p.details.Strategy = pl.Strategy
		p.syncDetails()
		if p.state.Phase == PhaseRunning {
			p.state.Step = p.narrative.CurrentHuman()
		}
	case events.TargetResolvedPayload:
		if !p.matches(pl.RequestID) {
			return
		}
		if p.state.Phase == PhaseRunning {
			p.state.Step = p.narrative.CurrentHuman()
		}
	case events.ContextPreparedPayload:
		if !p.matches(pl.RequestID) {
			return
		}
		p.details.ContextChannels = append([]string(nil), pl.Channels...)
		p.details.ContextTokens = pl.Tokens
		p.details.ContextCacheHit = pl.CacheHit
		p.syncDetails()
		if p.state.Phase == PhaseRunning {
			p.state.Step = p.narrative.CurrentHuman()
		}
	case events.ModelInvokedPayload:
		if !p.matches(pl.RequestID) {
			return
		}
		p.details.Model = pl.Model
		// ProviderCalls counts OBSERVED invocations. It is the denominator for
		// "useful outcome / model computation" and the basis for detecting a
		// duplicated invocation — it is never estimated.
		p.details.ProviderCalls++
		p.syncDetails()
		if p.state.Phase == PhaseRunning {
			p.state.Step = p.narrative.CurrentHuman()
		}
	case events.ProviderResponsePayload:
		if !p.matches(pl.RequestID) {
			return
		}
		p.details.Model = pl.Model
		p.details.TokenInput = pl.TokenInput
		p.details.TokenOutput = pl.TokenOutput
		p.details.ProviderState = "done"
		// The finish reason describes the GENERATION, not the objective. It is
		// kept explicitly so "model invocation complete" can never be read as
		// "task complete" from the same field.
		p.details.FinishReason = pl.FinishReason
		p.syncDetails()
		if p.state.Phase == PhaseRunning {
			p.state.Step = p.narrative.CurrentHuman()
		}
	case events.ProviderWaitingPayload:
		if !p.matches(pl.RequestID) {
			return
		}
		p.details.Model = pl.Model
		p.details.ProviderState = "waiting"
		p.syncDetails()
		if p.state.Phase == PhaseRunning {
			p.state.Step = p.narrative.CurrentHuman()
		}
	case events.ProviderFirstTokenPayload:
		if !p.matches(pl.RequestID) {
			return
		}
		p.details.Model = pl.Model
		p.details.ProviderState = "streaming"
		p.syncDetails()
		if p.state.Phase == PhaseRunning {
			p.state.Step = p.narrative.CurrentHuman()
		}
	case events.ProviderStreamDeltaPayload:
		if !p.matches(pl.RequestID) {
			return
		}
		p.details.ProviderState = "streaming"
		p.syncDetails()
	case events.ProviderUsageUpdatePayload:
		if !p.matches(pl.RequestID) {
			return
		}
		p.details.Model = pl.Model
		p.details.TokenInput = pl.InputTokens
		p.details.TokenOutput = pl.OutputTokens
		p.details.ReasoningTokens = pl.ReasoningTokens
		p.details.ProviderState = "streaming"
		p.syncDetails()
	case events.ReasoningTelemetryPayload:
		if !p.matches(pl.RequestID) {
			return
		}
		p.details.ReasoningTokens = pl.Tokens
		p.details.ReasoningDuration = pl.Duration
		p.syncDetails()
	case events.ArtifactProducedPayload:
		if !p.matches(pl.RequestID) {
			return
		}
		p.details.Artifacts = append(p.details.Artifacts, ArtifactView{
			Type:   ClassifyArtifact(pl.Kind),
			Kind:   pl.Kind,
			Target: pl.Target,
		})
		// An artifact record for a target IS the candidate state: the model
		// produced a concrete artifact for that path. This is what makes the
		// target "active" in the artifact ledger — it is never inferred.
		if pl.Target != "" {
			slot := p.targetSlot(pl.Target)
			p.details.Targets[slot].Candidate = true
			p.recountTargets()
		}
		p.syncDetails()
		if p.state.Phase == PhaseRunning {
			p.state.Step = p.narrative.CurrentHuman()
		}
	case events.ApprovalRequiredPayload:
		if !p.matches(pl.RequestID) {
			return
		}
		p.state.Phase = PhaseWaitingApproval
		p.state.Step = p.narrative.CurrentHuman()
	case events.MutationStartedPayload:
		if !p.matches(pl.RequestID) {
			return
		}
		// Entering the mutation boundary is the fact that makes a completion
		// claim require sealed evidence (completion_gate.go).
		p.gate.ObserveMutation()
		// Each announced target becomes a ledger row BEFORE any candidate or
		// outcome exists for it — that is what lets the UI show a real
		// per-artifact progress ledger (active vs pending) instead of a generic
		// activity line.
		//
		// The boundary ANNOUNCING a target is not the same as a candidate
		// existing for it, so this does not set Candidate. Only an
		// artifact.produced record or an apply that observed an artifact
		// proves a candidate is real; everything else stays honestly pending.
		for _, t := range pl.Targets {
			if t == "" {
				continue
			}
			p.targetSlot(t)
		}
		p.recountTargets()
		if p.state.Phase == PhaseWaitingApproval {
			p.state.Phase = PhaseRunning
		}
		p.syncDetails()
		p.state.Step = p.narrative.CurrentHuman()
	case events.MutationCompletedPayload:
		if !p.matches(pl.RequestID) {
			return
		}
		// Transport the apply boundary's REAL evidence. The diff metrics are
		// stored verbatim; when DiffPresent is false they are absent, and every
		// renderer is required to render no diff statistics at all rather than
		// a fabricated zero.
		slot := p.targetSlot(pl.Target)
		row := &p.details.Targets[slot]
		row.Outcome = pl.Outcome
		row.ArtifactPresent = pl.ArtifactPresent
		row.ApplyExecuted = pl.ApplyExecuted
		row.FilesystemChanged = pl.FilesystemChanged
		row.DiffPresent = pl.DiffPresent
		if pl.DiffPresent {
			row.DiffAdds = pl.DiffAdds
			row.DiffRemoves = pl.DiffRemoves
		} else {
			row.DiffAdds, row.DiffRemoves = 0, 0
		}
		if pl.ArtifactPresent || pl.ApplyExecuted {
			row.Candidate = true
		}
		p.recountTargets()
		p.syncDetails()
		if p.state.Phase == PhaseRunning {
			p.state.Step = p.narrative.CurrentHuman()
		}
	case events.VerificationCompletedPayload:
		if !p.matches(pl.RequestID) {
			return
		}
		p.details.VerificationRan = true
		p.details.VerificationPassed = pl.Passed
		p.details.VerificationSteps = append([]string(nil), pl.Steps...)
		p.syncDetails()
		if p.state.Phase == PhaseRunning {
			p.state.Step = p.narrative.CurrentHuman()
		}
	case events.ExecutionEvidencePayload:
		if !p.matches(pl.RequestID) {
			return
		}
		// The sealed terminal record. It is the ONLY authority for a completion
		// claim on a mutation execution, and the runtime publishes it BEFORE
		// execution.finished so the gate is always populated by the time the
		// completion event is reduced.
		p.gate.ObserveEvidence(pl.Outcome, pl.Tainted, pl.FilesMutated)
		p.details.EvidenceObserved = true
		p.details.EvidenceOutcome = pl.Outcome
		p.details.EvidenceTainted = pl.Tainted
		p.details.FilesMutated = pl.FilesMutated
		p.syncDetails()
	case events.ExecutionFinishedPayload:
		if !p.matches(pl.RequestID) {
			return
		}
		finished := pl
		p.lastFinished = &finished
		p.details.FinishedAt = ev.Timestamp()
		p.state = p.terminalState(pl)
		// The terminal sentence is the one narrative line whose truth depends on
		// accumulated evidence rather than on a single transition, so the
		// projection — which holds the gate — supplies it. The narrative keeps
		// the machine record either way, so the event stream is never falsified.
		p.narrative.RewriteHuman(terminalSentence(p.state))
	case events.ObjectiveEvaluatedPayload:
		// The runtime's AUTHORITATIVE objective verdict. It is published after
		// execution.finished (the executor seals and completes, then the driver's
		// completion authority rules), so it typically ARRIVES LATE. Recording it
		// and re-deriving a provisional terminal state is what makes
		// `UI.Completed ⇔ ObjectiveState == PROVEN` structural rather than
		// accidental: a read-only run that terminated "successfully" but was
		// never proven can no longer be rendered as Completed.
		if pl.RunID != "" && !p.matches(pl.RunID) {
			return
		}
		p.objective = objectiveVerdict{
			observed: true,
			outcome:  pl.State,
			granted:  pl.Granted,
			reason:   pl.Reason,
		}
		if p.state.Phase.Terminal() && p.lastFinished != nil {
			p.state = p.terminalState(*p.lastFinished)
			p.narrative.RewriteHuman(terminalSentence(p.state))
		}
	case events.ExecutionFailedPayload:
		// execution.failed may arrive before execution.finished; both are
		// terminal transitions. The finished event carries the authoritative
		// phase, but a failed event must never leave the state Running.
		if p.state.Phase == PhaseRunning || p.state.Phase == PhaseWaitingApproval {
			p.state = ExecutionViewState{
				Phase: PhaseFailed, Outcome: pl.Stage, RequestID: p.state.RequestID, Details: p.details,
			}
		}
	}
}

// terminalState reduces a terminal execution.finished event into the ONE
// canonical user-facing terminal state.
//
// The runtime's `success` flag answers "did the execution terminate without an
// error". It does NOT answer "is the task done". Those are different questions,
// and only the second one gates the completed state:
//
//   - A clean cancellation is terminal and non-failure, but it is labelled
//     cancelled — never completed. Nothing was achieved and nothing is claimed.
//   - success=false is a failure.
//   - success=true is judged by the completion gate. Granted → completed.
//     Refused → PhaseUnsubstantiated carrying the deterministic reason, which
//     is an explicit NON-complete state rather than a fabricated success.
func (p *ExecutionProjection) terminalState(pl events.ExecutionFinishedPayload) ExecutionViewState {
	base := ExecutionViewState{RequestID: pl.RequestID, Details: p.details}
	switch {
	case pl.Outcome == "cancelled":
		// A clean cancellation is a terminal, non-failure outcome. It is NOT a
		// task completion: no work was achieved, so the state says cancelled.
		base.Phase = PhaseCompleted
		base.Outcome = "cancelled"
		return base
	case !pl.Success:
		base.Phase = PhaseFailed
		base.Outcome = pl.Outcome
		return base
	}
	verdict := p.gate.Verdict()
	if !verdict.Granted {
		base.Phase = PhaseUnsubstantiated
		base.Outcome = verdict.Reason
		return base
	}
	// The authoritative objective verdict, when the runtime published one, is the
	// ONLY licence for a completed state. The execution gate above is necessary
	// for the workspace claim, but it does not answer "was the objective
	// achieved": that is the completion authority's ruling. Requiring it is what
	// makes UI.Completed ⇔ ObjectiveState == PROVEN structural.
	if p.objective.observed {
		switch {
		case p.objective.granted && strings.EqualFold(p.objective.outcome, "PROVEN"):
			base.Phase = PhaseCompleted
			base.Outcome = "completed"
			return base
		case strings.EqualFold(p.objective.outcome, "FAILED"):
			base.Phase = PhaseFailed
			base.Outcome = "failed"
			return base
		default:
			base.Phase = PhaseUnsubstantiated
			reason := p.objective.reason
			if strings.TrimSpace(reason) == "" {
				reason = "the objective was not proven by evidence"
			}
			base.Outcome = reason
			return base
		}
	}
	base.Phase = PhaseCompleted
	base.Outcome = pl.Outcome
	return base
}

// terminalSentence is the deterministic human sentence for a terminal state. It
// is the only place a completion sentence is produced, and it reads the
// evidence-gated phase — never the raw success flag.
func terminalSentence(s ExecutionViewState) string {
	switch s.Phase {
	case PhaseCompleted:
		if s.Outcome == "cancelled" {
			return "Cancelled"
		}
		return "Completed"
	case PhaseUnsubstantiated:
		return "Not completed — " + s.Outcome
	default:
		return "Failed"
	}
}

// syncDetails mirrors the accumulated metadata onto the live view state so the
// EXPANDED layer always reflects the observed payloads.
func (p *ExecutionProjection) syncDetails() {
	if p == nil {
		return
	}
	p.state.Details = p.details
}

// matches reports whether the event belongs to the projected execution. An idle
// projection accepts the first lifecycle event it sees.
func (p *ExecutionProjection) matches(requestID string) bool {
	if p.state.RequestID == "" {
		return true
	}
	return p.state.RequestID == requestID
}

// Reset clears the projection to idle, discarding stale step trees and
// progress state. It is the deterministic cleanup invoked when the engine
// reaches terminal idle (StateIdle/StateChat) or recovers from interrupts.
//
//nolint:unused // Staged contract: projection determinism (see ADR-004)
func (p *ExecutionProjection) Reset() {
	if p == nil {
		return
	}
	p.state = NewIdle()
	p.details = ExecutionDetails{}
	p.narrative = NewExecutionNarrative()
}

// Frame computes the renderer-ready presentation slice for the given
// visibility layer. It is a pure function of the projection state + narrative —
// the presentation layer decides what belongs in each layer, the renderer only
// formats the frame.
//
// NORMAL: human narrative milestones + the live current step. No providers,
// strategies, tokens, or event names.
// EXPANDED: NORMAL + accumulated runtime metadata (strategy, context policy,
// model, token usage, duration, artifacts).
// DEBUG: EXPANDED metadata + the full machine event stream.
func (p *ExecutionProjection) Frame(v Visibility) ExecutionFrame {
	if p == nil {
		return ExecutionFrame{Visibility: v}
	}
	frame := ExecutionFrame{Visibility: v, State: p.State()}
	frame.Steps = p.narrative.Steps()
	// Mark the live step only while the execution is actually in flight (running
	// or awaiting approval). A terminal phase has no live step — the renderer
	// never shows a spinner/current marker after completion.
	if st := frame.State; st.Phase == PhaseRunning || st.Phase == PhaseWaitingApproval {
		if len(frame.Steps) > 0 {
			frame.Steps[len(frame.Steps)-1].Current = true
		}
	}
	if v == VisibilityExpanded || v == VisibilityDebug {
		frame.Details = p.State().Details
	}
	if v == VisibilityDebug {
		frame.Events = p.narrative.Machine()
	}
	return frame
}
