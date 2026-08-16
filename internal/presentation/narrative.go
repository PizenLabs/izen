// Execution narrative layer: Claude-style UX separates MACHINE events from the
// HUMAN narrative. This type is the deterministic, side-effect-free reducer that
// turns the canonical runtime event stream into human sentences ("Inspecting
// index.html", "Preparing a targeted edit", "Generated a proposed change",
// "Waiting for approval").
//
// Rules:
//   - Narrative is generated from events — never from a UI-typed string.
//   - Narrative is deterministic: the same event always yields the same
//     sentence.
//   - No LLM call is ever used for narration.
//   - The UI reads the narrative; it does not author it.
package presentation

import (
	"fmt"

	"github.com/PizenLabs/izen/internal/events"
)

// narrativeLine is one deterministic narrative record: the machine event and
// the human sentence it maps to.
type narrativeLine struct {
	machine string
	human   string
}

// ExecutionNarrative separates machine events from the human narrative of one
// execution. It is pure and deterministic — given the same events it always
// yields the same sentences. It never invents a sentence it cannot attribute
// to an observed event.
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
// narrative record. Events of other types and stale-request events are ignored.
func (n *ExecutionNarrative) Project(ev events.DomainEvent) {
	if n == nil || ev == nil {
		return
	}
	payload := ev.Payload()
	if payload == nil {
		return
	}
	machine := machineRecord(ev)
	var human string
	switch p := payload.(type) {
	case events.ExecutionStartedPayload:
		// A fresh execution (new request) resets the narrative — a new
		// execution is a clean slate. The same request re-starting is a no-op.
		if n.requestID != "" && n.requestID != p.RequestID {
			n.lines = nil
			n.current = -1
		}
		n.requestID = p.RequestID
		human = "Understanding request"
	case events.StrategySelectedPayload:
		if !n.matches(p.RequestID) {
			return
		}
		human = strategySentence(p.Strategy)
	case events.TargetResolvedPayload:
		if !n.matches(p.RequestID) {
			return
		}
		human = "Inspecting " + p.Target
	case events.ContextPreparedPayload:
		if !n.matches(p.RequestID) {
			return
		}
		if len(p.Channels) > 0 {
			human = fmt.Sprintf("Gathering context (%d channels)", len(p.Channels))
		}
	case events.ModelInvokedPayload:
		if !n.matches(p.RequestID) {
			return
		}
		human = "Thinking..."
	case events.ProviderResponsePayload:
		if !n.matches(p.RequestID) {
			return
		}
	case events.ArtifactProducedPayload:
		if !n.matches(p.RequestID) {
			return
		}
		human = artifactSentence(p.Kind)
	case events.ApprovalRequiredPayload:
		if !n.matches(p.RequestID) {
			return
		}
		human = "Waiting for approval"
	case events.MutationStartedPayload:
		if !n.matches(p.RequestID) {
			return
		}
		human = "Applying changes"
	case events.MutationCompletedPayload:
		if !n.matches(p.RequestID) {
			return
		}
		if mutationOutcomeSucceeded(p.Outcome) {
			human = "Applied change to " + p.Target
		} else {
			human = "Change to " + p.Target + " not applied (" + p.Outcome + ")"
		}
	case events.VerificationCompletedPayload:
		if !n.matches(p.RequestID) {
			return
		}
		if p.Passed {
			human = "Verified the change"
		} else {
			human = "Verification failed"
		}
	case events.ExecutionFinishedPayload:
		if !n.matches(p.RequestID) {
			return
		}
		human = finishedSentence(p.Success, p.Outcome)
	case events.ExecutionFailedPayload:
		human = "Failed"
	}
	n.lines = append(n.lines, narrativeLine{machine: machine, human: human})
	if human != "" {
		n.current = len(n.lines) - 1
	}
}

// matches reports whether the event belongs to the narrated execution.
func (n *ExecutionNarrative) matches(requestID string) bool {
	if n.requestID == "" {
		return true
	}
	return n.requestID == requestID
}

// strategySentence is the deterministic human sentence for a strategy decision.
func strategySentence(s string) string {
	switch s {
	case "targeted_mutation":
		return "Preparing a targeted edit"
	case "direct_response":
		return "Answering directly"
	case "repository_investigation":
		return "Investigating the repository"
	case "multi_file_planning":
		return "Planning the change"
	default:
		return "Preparing execution"
	}
}

// artifactSentence is the deterministic human sentence for an artifact.
func artifactSentence(kind string) string {
	switch kind {
	case "patch":
		return "Generated a proposed change"
	case "plan":
		return "Drafted a plan"
	case "investigation":
		return "Completed the investigation"
	case "response":
		return "Generated response"
	default:
		return "Prepared the change"
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
