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
	// terminal success (or a clean cancellation). No running step may follow.
	PhaseCompleted
	// PhaseFailed is a terminal state: the execution failed. No running step
	// may follow.
	PhaseFailed
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
	default:
		return "idle"
	}
}

// Terminal reports whether the phase is terminal (Completed or Failed).
func (p ViewPhase) Terminal() bool {
	return p == PhaseCompleted || p == PhaseFailed
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
	case PhaseCompleted, PhaseFailed:
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
	// narrative is the deterministic human/machine narrative layer. The UI
	// reads it; it never authors narration text.
	narrative *ExecutionNarrative
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
		p.syncDetails()
		if p.state.Phase == PhaseRunning {
			p.state.Step = p.narrative.CurrentHuman()
		}
	case events.ModelInvokedPayload:
		if !p.matches(pl.RequestID) {
			return
		}
		p.details.Model = pl.Model
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
		if p.state.Phase == PhaseWaitingApproval {
			p.state.Phase = PhaseRunning
		}
		p.state.Step = p.narrative.CurrentHuman()
	case events.MutationCompletedPayload:
		if !p.matches(pl.RequestID) {
			return
		}
		if p.state.Phase == PhaseRunning {
			p.state.Step = p.narrative.CurrentHuman()
		}
	case events.VerificationCompletedPayload:
		if !p.matches(pl.RequestID) {
			return
		}
		if p.state.Phase == PhaseRunning {
			p.state.Step = p.narrative.CurrentHuman()
		}
	case events.ExecutionFinishedPayload:
		if !p.matches(pl.RequestID) {
			return
		}
		p.details.FinishedAt = ev.Timestamp()
		switch {
		case pl.Success:
			p.state = ExecutionViewState{
				Phase: PhaseCompleted, Outcome: pl.Outcome, RequestID: pl.RequestID, Details: p.details,
			}
		case pl.Outcome == "cancelled":
			// A clean cancellation is a terminal, non-failure outcome.
			p.state = ExecutionViewState{
				Phase: PhaseCompleted, Outcome: "cancelled", RequestID: pl.RequestID, Details: p.details,
			}
		default:
			p.state = ExecutionViewState{
				Phase: PhaseFailed, Outcome: pl.Outcome, RequestID: pl.RequestID, Details: p.details,
			}
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
