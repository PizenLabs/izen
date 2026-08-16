// Execution projection: the human-facing view of a runtime execution is a pure
// function of the canonical runtime event stream. The Runtime emits complete
// machine truth (execution.started → strategy.selected → target.resolved →
// context.prepared → model.invoked → provider.response → artifact.produced →
// approval.required → mutation.completed → verification.completed →
// execution.finished); this package REDUCES that stream into a concise human
// narrative plus a single ExecutionViewState the renderer depends on.
//
// The UI never invents state: every narrative line and every state transition
// here is derived from an observed events.DomainEvent. A terminal event
// (execution.finished / execution.failed) ALWAYS transitions the state into a
// terminal phase, so no stale spinner can survive success, failure, or
// cancellation.
package presentation

import (
	"fmt"

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
	// PhaseRunning carries the current human step (Thinking..., Found target…,
	// Generated change…).
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
	// Step is the current human step while Phase == PhaseRunning (e.g.
	// "Thinking...", "Found target index.html", "Generated change").
	Step string
	// Outcome is the terminal outcome label when Phase is terminal (e.g.
	// "completed", "cancelled", "patch_generation_failed").
	Outcome string
	// RequestID is the execution this state projects.
	RequestID string
}

// NewIdle returns the resting execution view state.
func NewIdle() ExecutionViewState {
	return ExecutionViewState{Phase: PhaseIdle}
}

// Valid enforces the "no impossible states" invariant: a running state must
// carry a step, a terminal state must carry an outcome, and a terminal phase
// must never be followed by a running step (enforced by the reducer).
func (s ExecutionViewState) Valid() bool {
	switch s.Phase {
	case PhaseRunning:
		return s.Step != ""
	case PhaseWaitingApproval:
		return true
	case PhaseCompleted, PhaseFailed:
		return s.Outcome != ""
	default:
		return s.Step == "" && s.Outcome == "" && s.RequestID == ""
	}
}

// ExecutionProjection reduces the canonical runtime event stream into the
// execution view state plus Debug (machine) and Human (narrative) timelines.
// It is a single-execution projection: a new execution.started resets it.
type ExecutionProjection struct {
	state ExecutionViewState

	// human is the concise human narrative (Thinking…, ✓ Found target…, …).
	human []string
	// debug is the developer diagnostics projection (execution.started,
	// strategy.selected, context.prepared, model.invoked, artifact.produced).
	debug []string
}

// NewExecutionProjection returns an idle single-execution projection.
func NewExecutionProjection() *ExecutionProjection {
	return &ExecutionProjection{state: NewIdle()}
}

// Begin starts the projection for a fresh execution (Running "Thinking...") as
// the synchronous dispatch-time seed. The first execution.started event (which
// travels asynchronously on the bus) resets the projection to the identical
// initial state, so Begin never conflicts with the authoritative stream.
func (p *ExecutionProjection) Begin(requestID string) {
	if p == nil {
		return
	}
	*p = ExecutionProjection{
		state: ExecutionViewState{Phase: PhaseRunning, Step: "Thinking...", RequestID: requestID},
		human: []string{"Thinking..."},
		debug: []string{"execution.started"},
	}
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

// HumanTimeline returns the concise human narrative lines observed so far.
func (p *ExecutionProjection) HumanTimeline() []string {
	if p == nil {
		return nil
	}
	out := make([]string, len(p.human))
	copy(out, p.human)
	return out
}

// DebugTimeline returns the developer diagnostics lines observed so far.
func (p *ExecutionProjection) DebugTimeline() []string {
	if p == nil {
		return nil
	}
	out := make([]string, len(p.debug))
	copy(out, p.debug)
	return out
}

// HumanStep returns the current human step for a Running state ("" otherwise).
func (p *ExecutionProjection) HumanStep() string {
	if p == nil || p.state.Phase != PhaseRunning {
		return ""
	}
	return p.state.Step
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
	switch pl := payload.(type) {
	case events.ExecutionStartedPayload:
		// Fresh execution: reset the projection (single-execution scope).
		*p = ExecutionProjection{
			state: ExecutionViewState{Phase: PhaseRunning, Step: "Thinking...", RequestID: pl.RequestID},
			human: []string{"Thinking..."},
			debug: []string{"execution.started"},
		}
	case events.StrategySelectedPayload:
		if !p.matches(pl.RequestID) {
			return
		}
		p.debug = append(p.debug, fmt.Sprintf("strategy.selected: %s", pl.Strategy))
	case events.TargetResolvedPayload:
		if !p.matches(pl.RequestID) {
			return
		}
		if p.state.Phase == PhaseRunning {
			p.state.Step = "Found target " + pl.Target
		}
		p.human = append(p.human, "✓ Found target "+pl.Target)
		p.debug = append(p.debug, fmt.Sprintf("target.resolved: %s (exists=%t)", pl.Target, pl.Exists))
	case events.ContextPreparedPayload:
		if !p.matches(pl.RequestID) {
			return
		}
		p.debug = append(p.debug, fmt.Sprintf("context.prepared: %d channel(s), ~%d tokens", len(pl.Channels), pl.Tokens))
	case events.ModelInvokedPayload:
		if !p.matches(pl.RequestID) {
			return
		}
		if p.state.Phase == PhaseRunning {
			p.state.Step = "Thinking..."
		}
		p.debug = append(p.debug, "model.invoked: "+pl.Model)
	case events.ProviderResponsePayload:
		if !p.matches(pl.RequestID) {
			return
		}
		p.debug = append(p.debug, fmt.Sprintf("provider.response: %s (%d in / %d out)", pl.Model, pl.TokenInput, pl.TokenOutput))
	case events.ArtifactProducedPayload:
		if !p.matches(pl.RequestID) {
			return
		}
		if p.state.Phase == PhaseRunning {
			p.state.Step = "Generated change"
		}
		p.human = append(p.human, "✓ Generated change")
		p.debug = append(p.debug, fmt.Sprintf("artifact.produced: %s (%s)", pl.Kind, pl.Target))
	case events.ApprovalRequiredPayload:
		if !p.matches(pl.RequestID) {
			return
		}
		p.state.Phase = PhaseWaitingApproval
		p.state.Step = "Waiting for approval"
		p.human = append(p.human, "Waiting for approval")
		p.debug = append(p.debug, "approval.required: "+pl.Target)
	case events.MutationStartedPayload:
		if !p.matches(pl.RequestID) {
			return
		}
		if p.state.Phase == PhaseWaitingApproval {
			p.state.Phase = PhaseRunning
		}
		p.state.Step = "Applying..."
		p.debug = append(p.debug, fmt.Sprintf("mutation.started: %d target(s)", len(pl.Targets)))
	case events.MutationCompletedPayload:
		if !p.matches(pl.RequestID) {
			return
		}
		if mutationSucceeded(pl.Outcome) {
			p.human = append(p.human, "✓ Applied")
		}
		p.debug = append(p.debug, fmt.Sprintf("mutation.completed: %s (%s)", pl.Target, pl.Outcome))
	case events.VerificationCompletedPayload:
		if !p.matches(pl.RequestID) {
			return
		}
		if pl.Passed {
			p.human = append(p.human, "✓ Verified")
		}
		p.debug = append(p.debug, fmt.Sprintf("verification.completed: passed=%t (%d step(s))", pl.Passed, len(pl.Steps)))
	case events.ExecutionFinishedPayload:
		if !p.matches(pl.RequestID) {
			return
		}
		switch {
		case pl.Success:
			p.state = ExecutionViewState{
				Phase: PhaseCompleted, Outcome: pl.Outcome, RequestID: pl.RequestID,
			}
			p.human = append(p.human, "✓ Completed")
		case pl.Outcome == "cancelled":
			// A clean cancellation is a terminal, non-failure outcome.
			p.state = ExecutionViewState{
				Phase: PhaseCompleted, Outcome: "cancelled", RequestID: pl.RequestID,
			}
			p.human = append(p.human, "Cancelled")
		default:
			p.state = ExecutionViewState{
				Phase: PhaseFailed, Outcome: pl.Outcome, RequestID: pl.RequestID,
			}
			p.human = append(p.human, "✗ Failed")
		}
		p.debug = append(p.debug, fmt.Sprintf("execution.finished: success=%t (%s)", pl.Success, pl.Outcome))
	case events.ExecutionFailedPayload:
		// execution.failed may arrive before execution.finished; both are
		// terminal transitions. The finished event carries the authoritative
		// phase, but a failed event must never leave the state Running.
		if p.state.Phase == PhaseRunning || p.state.Phase == PhaseWaitingApproval {
			p.state = ExecutionViewState{
				Phase: PhaseFailed, Outcome: pl.Stage, RequestID: p.state.RequestID,
			}
		}
		p.debug = append(p.debug, fmt.Sprintf("execution.failed: %s (%s)", pl.Classification, pl.Stage))
	}
}

// matches reports whether the event belongs to the projected execution. An idle
// projection accepts the first lifecycle event it sees.
func (p *ExecutionProjection) matches(requestID string) bool {
	if p.state.RequestID == "" {
		return true
	}
	return p.state.RequestID == requestID
}

// mutationSucceeded reports whether a MutationOutcome string denotes success.
func mutationSucceeded(outcome string) bool {
	switch outcome {
	case "changed", "created", "committed":
		return true
	default:
		return false
	}
}
