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
	"time"

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
	case events.EventModelInvoked:
		return "provider.invoked"
	case events.EventProviderResponse:
		return "provider.completed"
	case events.EventProviderWaiting:
		return "provider.waiting"
	case events.EventProviderFirstToken:
		return "provider.first_token"
	case events.EventProviderUsageUpdate:
		return "provider.usage_update"
	case events.EventReasoningTelemetry:
		return "reasoning.telemetry"
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
//
// NORMAL-layer visibility rule: only transitions that represent MEANINGFUL
// human progress carry a sentence. execution.started / strategy.selected are
// deterministic plumbing (request receipt + a decision the engine already made)
// and deliberately produce NO human step — the engine has done nothing visible
// yet. The first human step appears when the runtime actually touches a target
// (target.resolved → "Reading {target}") or invokes the model.
var transitionNarrative = map[string]string{
	"target.resolved":        "Reading target",
	"context.prepared":       "Gathering context",
	"provider.invoked":       "Analyzing",
	"provider.waiting":       "Waiting for model",
	"provider.first_token":   "Model responding",
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

// RewriteHuman replaces the human sentence of the most recently recorded line.
//
// The terminal sentence is the ONE narrative line whose truth does not follow
// from its own transition: whether an execution COMPLETED depends on the sealed
// evidence accumulated across the whole attempt, not on execution.finished's
// success flag alone (see completion_gate.go). The projection holds that gate,
// so it supplies the sentence here while the narrative keeps the machine record
// untouched — the event stream is never falsified, only the human sentence is
// corrected to the evidence.
//
// A machine-only line (no human sentence, e.g. a plumbing transition) gains the
// sentence, because the reducer recorded the transition and the sentence
// describes it.
func (n *ExecutionNarrative) RewriteHuman(sentence string) {
	if n == nil || len(n.lines) == 0 || sentence == "" {
		return
	}
	last := len(n.lines) - 1
	if n.lines[last].human == sentence {
		return
	}
	n.lines[last].human = sentence
	if n.current != last {
		n.current = last
	}
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
		// derived from the transition payload, never a static label. The
		// runtime reads the resolved target content, so the truthful human
		// sentence is "Reading {target}", never a fabricated "thinking".
		if p.Target != "" {
			human = "Reading " + p.Target
		}
	case events.ContextPreparedPayload:
		// Zero-context policies (direct_response / casual chat) compiled no
		// context: nothing was gathered, so no human step exists. A real
		// context envelope (target_file_only / repository) produces the step.
		if len(p.Channels) == 0 {
			human = ""
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
		// The terminal sentence is evidence-gated, not transition-derived: the
		// projection rewrites it once the completion gate has ruled. What is
		// recorded here is the provisional reading of the transition alone, and
		// it is deliberately NOT "Completed" for a mutation execution — the
		// projector owns the final wording.
		human = provisionalFinishedSentence(p.Success, p.Outcome)
	}
	if human == "" {
		// Machine-only record: the transition carries no meaningful human
		// progress (e.g. execution.started / strategy.selected), but the raw
		// record must still be kept for the DEBUG layer — dropping it would
		// falsify the event stream. A machine-only line never becomes a human
		// step (Human/Steps filter on the sentence).
		n.lines = append(n.lines, narrativeLine{transition: transition, machine: machine})
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
	case events.ProviderWaitingPayload:
		return p.RequestID
	case events.ProviderFirstTokenPayload:
		return p.RequestID
	case events.ProviderStreamDeltaPayload:
		return p.RequestID
	case events.ProviderUsageUpdatePayload:
		return p.RequestID
	case events.ReasoningTelemetryPayload:
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

// provisionalFinishedSentence is the terminal human sentence derived from the
// execution.finished transition ALONE, before the evidence gate has ruled.
//
// It is deliberately conservative: success=true never yields "Completed" here,
// because a provider returning and a loop ending do not prove the objective was
// met. The projection replaces this sentence with the gated verdict
// (ExecutionProjection.terminalState → terminalSentence) as soon as it reduces
// the event, so a mutation execution that terminates without granted evidence
// reads "Not completed — <reason>" rather than a fabricated success.
//
// A cancellation is stated plainly and a failure is stated plainly: neither is
// provisional, both are facts of the transition.
func provisionalFinishedSentence(success bool, outcome string) string {
	if outcome == "cancelled" {
		return "Cancelled"
	}
	if !success {
		return "Failed"
	}
	// Unsubstantiated until the gate confirms; the projection immediately
	// rewrites this to either "Completed" or the refusal reason.
	return "Execution finished"
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
	case events.ProviderWaitingPayload:
		return fmt.Sprintf("%s: %s", ev.Type(), p.Model)
	case events.ProviderFirstTokenPayload:
		return fmt.Sprintf("%s: %s (first token after %s)", ev.Type(), p.Model, p.Latency.Round(time.Millisecond))
	case events.ProviderUsageUpdatePayload:
		return fmt.Sprintf("%s: %s (%d in / %d out, %d reasoning)", ev.Type(), p.Model, p.InputTokens, p.OutputTokens, p.ReasoningTokens)
	case events.ReasoningTelemetryPayload:
		return fmt.Sprintf("%s: %s for %s (%d tokens)", ev.Type(), p.Model, p.Duration.Round(time.Millisecond), p.Tokens)
	case events.ArtifactProducedPayload:
		return fmt.Sprintf("%s: %s", ev.Type(), p.Kind)
	case events.ExecutionFinishedPayload:
		return fmt.Sprintf("%s: success=%t (%s)", ev.Type(), p.Success, p.Outcome)
	default:
		return ev.Type()
	}
}
