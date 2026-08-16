// Execution narrative layer: Claude-style UX separates MACHINE events from the
// HUMAN narrative. This type is the deterministic, side-effect-free reducer that
// turns the canonical runtime event stream into human sentences derived from
// ExecutionGraph transitions.
//
// Rules:
//   - Narrative is derived from the ExecutionGraph transitions (the canonical
//     event stream) — never from a UI-typed string and never a static
//     predefined step. A step exists only because a real transition occurred.
//   - Narrative is deterministic: the same transition always yields the same
//     sentence.
//   - No LLM call is ever used for narration.
//   - The UI reads the narrative; it does not author it.
package presentation

import (
	"fmt"

	"github.com/PizenLabs/izen/internal/events"
)

// narrativeLine is one deterministic narrative record: the canonical
// transition, the machine event, and the human sentence it derives from.
type narrativeLine struct {
	// transition is the canonical ExecutionGraph transition this line derives
	// from ("strategy.selected", "target.resolved", ...). It is the derivation
	// key — the same transition always yields the same sentence.
	transition string
	machine    string
	human      string
}

// transitionForEvent maps a canonical runtime event onto its ExecutionGraph
// transition name. This is the single derivation seam: every human sentence is
// keyed by a transition, so no step can exist without a real transition.
func transitionForEvent(ev events.DomainEvent) string {
	switch ev.Type() {
	case events.EventExecutionStarted:
		return "execution.started"
	case events.EventStrategySelected:
		return "strategy.selected"
	case events.EventTargetResolved:
		return "target.resolved"
	case events.EventContextPrepared:
		return "context.prepared"
	case events.EventModelInvoked, events.EventProviderResponse:
		return "provider.invoked"
	case events.EventArtifactProduced:
		return "artifact.produced"
	case events.EventApprovalRequired:
		return "approval.required"
	case events.EventMutationStarted:
		return "mutation.started"
	case events.EventMutationCompleted:
		return "mutation.completed"
	case events.EventVerificationCompleted:
		return "verification.completed"
	case events.EventExecutionFinished:
		return "execution.finished"
	case events.EventExecutionFailed:
		return "execution.failed"
	default:
		return ""
	}
}

// transitionNarrative is the canonical transition → human sentence mapping. It
// is the single source of truth for the human narrative. A transition that has
// no sentence still carries a machine record (DEBUG layer) but adds no human
// step — the narrative never invents a step that has no transition behind it.
var transitionNarrative = map[string]string{
	"execution.started":      "Understanding request",
	"strategy.selected":      "Understanding request",
	"target.resolved":        "Inspecting target",
	"context.prepared":       "Gathering context",
	"provider.invoked":       "Generating response",
	"artifact.produced":      "Preparing result",
	"approval.required":      "Waiting for approval",
	"mutation.started":       "Applying changes",
	"mutation.completed":     "Applied change",
	"verification.completed": "Verified changes",
	"execution.finished":     "Completed",
	"execution.failed":       "Failed",
}

// ExecutionNarrative separates machine events from the human narrative of one
// execution. It is pure and deterministic — given the same transitions it
// always yields the same sentences. It never invents a sentence it cannot
// attribute to an observed ExecutionGraph transition.
type ExecutionNarrative struct {
	lines []narrativeLine
	// current is the index of the most recent human sentence.
	current int
	// requestID binds the narrative to one execution; a fresh execution.started
	// resets it.
	requestID string
}

// NewExecutionNarrative returns an empty narrative bound to no request.
func NewExecutionNarrative() *ExecutionNarrative {
	return &ExecutionNarrative{current: -1}
}

// Human returns the ordered human narrative sentences.
func (n *ExecutionNarrative) Human() []string {
	if n == nil {
		return nil
	}
	out := make([]string, 0, len(n.lines))
	for _, l := range n.lines {
		if l.human != "" {
			out = append(out, l.human)
		}
	}
	return out
}

// Steps returns the ordered narrative steps with their canonical transitions.
// A human step exists only for transitions that actually occurred. The Current
// flag is not set here — the projection marks the live step based on phase.
func (n *ExecutionNarrative) Steps() []NarrativeStep {
	if n == nil {
		return nil
	}
	out := make([]NarrativeStep, 0, len(n.lines))
	for _, l := range n.lines {
		if l.human != "" {
			out = append(out, NarrativeStep{Transition: l.transition, Sentence: l.human})
		}
	}
	return out
}

// Machine returns the ordered machine event records.
func (n *ExecutionNarrative) Machine() []string {
	if n == nil {
		return nil
	}
	out := make([]string, 0, len(n.lines))
	for _, l := range n.lines {
		if l.machine != "" {
			out = append(out, l.machine)
		}
	}
	return out
}

// CurrentHuman returns the most recent human sentence ("" when none).
func (n *ExecutionNarrative) CurrentHuman() string {
	if n == nil || n.current < 0 || n.current >= len(n.lines) {
		return ""
	}
	return n.lines[n.current].human
}

// Project consumes one canonical runtime event and appends its deterministic
// narrative record. The human sentence is derived from the ExecutionGraph
// transition — events of other types and stale-request events are ignored.
func (n *ExecutionNarrative) Project(ev events.DomainEvent) {
	if n == nil || ev == nil {
		return
	}
	payload := ev.Payload()
	if payload == nil {
		return
	}
	transition := transitionForEvent(ev)
	if transition == "" {
		return
	}
	// Stale-request events are ignored once the narrative is bound to a
	// different execution — except execution.started, which IS the rebinding
	// event (a fresh execution is a clean slate).
	if _, isStart := payload.(events.ExecutionStartedPayload); !isStart {
		if rid := requestIDOf(payload); rid != "" && n.requestID != "" && n.requestID != rid {
			return
		}
	}
	machine := machineRecord(ev)
	var human string
	if sentence, ok := transitionNarrative[transition]; ok {
		human = sentence
	}
	switch p := payload.(type) {
	case events.ExecutionStartedPayload:
		// A fresh execution (new request) resets the narrative — a new
		// execution is a clean slate. The same request re-starting is a no-op.
		if n.requestID != "" && n.requestID != p.RequestID {
			n.lines = nil
			n.current = -1
		}
		n.requestID = p.RequestID
	case events.TargetResolvedPayload:
		// Enrich the derived step with the actual resolved target — still
		// derived from the transition payload, never a static label.
		if p.Target != "" {
			human = "Inspecting " + p.Target
		}
	case events.MutationCompletedPayload:
		if mutationOutcomeSucceeded(p.Outcome) {
			human = "Applied change to " + p.Target
		} else {
			human = "Change to " + p.Target + " not applied (" + p.Outcome + ")"
		}
	case events.VerificationCompletedPayload:
		if !p.Passed {
			human = "Verification failed"
		}
	case events.ExecutionFinishedPayload:
		human = finishedSentence(p.Success, p.Outcome)
	}
	if human == "" {
		return
	}
	// Derive only: a step identical to the last one (e.g. two targets) is
	// recorded as a machine event but adds no duplicate human step.
	if n.current >= 0 && n.lines[n.current].human == human {
		n.lines = append(n.lines, narrativeLine{transition: transition, machine: machine})
		return
	}
	n.lines = append(n.lines, narrativeLine{transition: transition, machine: machine, human: human})
	n.current = len(n.lines) - 1
}

// requestIDOf returns the RequestID carried by a lifecycle payload ("" when the
// payload has no request binding).
func requestIDOf(payload interface{}) string {
	switch p := payload.(type) {
	case events.ExecutionStartedPayload:
		return p.RequestID
	case events.StrategySelectedPayload:
		return p.RequestID
	case events.TargetResolvedPayload:
		return p.RequestID
	case events.ContextPreparedPayload:
		return p.RequestID
	case events.ModelInvokedPayload:
		return p.RequestID
	case events.ProviderResponsePayload:
		return p.RequestID
	case events.ArtifactProducedPayload:
		return p.RequestID
	case events.ApprovalRequiredPayload:
		return p.RequestID
	case events.MutationStartedPayload:
		return p.RequestID
	case events.MutationCompletedPayload:
		return p.RequestID
	case events.VerificationCompletedPayload:
		return p.RequestID
	case events.ExecutionFinishedPayload:
		return p.RequestID
	default:
		return ""
	}
}

// finishedSentence is the deterministic terminal human sentence.
func finishedSentence(success bool, outcome string) string {
	if success {
		return "Completed"
	}
	if outcome == "cancelled" {
		return "Cancelled"
	}
	return "Failed"
}

// mutationOutcomeSucceeded reports whether a MutationOutcome string denotes
// success.
func mutationOutcomeSucceeded(outcome string) bool {
	switch outcome {
	case "changed", "created", "committed":
		return true
	default:
		return false
	}
}

// machineRecord is the compact deterministic machine record of an event.
func machineRecord(ev events.DomainEvent) string {
	if ev == nil {
		return ""
	}
	switch p := ev.Payload().(type) {
	case events.StrategySelectedPayload:
		return fmt.Sprintf("%s: %s", ev.Type(), p.Strategy)
	case events.TargetResolvedPayload:
		return fmt.Sprintf("%s: %s", ev.Type(), p.Target)
	case events.ContextPreparedPayload:
		return fmt.Sprintf("%s: %d channel(s), %d tokens", ev.Type(), len(p.Channels), p.Tokens)
	case events.ModelInvokedPayload:
		return fmt.Sprintf("%s: %s", ev.Type(), p.Model)
	case events.ProviderResponsePayload:
		return fmt.Sprintf("%s: %s (%d in / %d out)", ev.Type(), p.Model, p.TokenInput, p.TokenOutput)
	case events.ArtifactProducedPayload:
		return fmt.Sprintf("%s: %s", ev.Type(), p.Kind)
	case events.ExecutionFinishedPayload:
		return fmt.Sprintf("%s: success=%t (%s)", ev.Type(), p.Success, p.Outcome)
	default:
		return ev.Type()
	}
}
