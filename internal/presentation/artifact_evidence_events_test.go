package presentation

import "github.com/PizenLabs/izen/internal/events"

// Compact event constructors for the artifact-evidence tests. They exist so each
// test reads as a list of observed RUNTIME FACTS rather than a wall of struct
// literals — the events are still the canonical ones, built by the canonical
// constructors in internal/events.

func events_ExecutionStarted() events.DomainEvent {
	return events.NewExecutionStarted("r1", "build", "redesign portfolio", "")
}

func events_ArtifactProduced(requestID, target string) events.DomainEvent {
	return events.NewArtifactProduced(requestID, "patch", target)
}

func events_MutationStarted(requestID string, targets []string) events.DomainEvent {
	return events.NewMutationStarted(requestID, targets)
}

// events_MutationCompleted builds the boundary-evidenced mutation.completed the
// executor emits after an apply.
func events_MutationCompleted(requestID, target, outcome string, changed bool, adds, removes int) events.DomainEvent {
	return events.NewMutationCompletedWithEvidence(requestID, events.MutationEvidence{
		Target:            target,
		Outcome:           outcome,
		ArtifactPresent:   true,
		DiffPresent:       adds > 0 || removes > 0,
		DiffAdds:          adds,
		DiffRemoves:       removes,
		ApplyExecuted:     true,
		FilesystemChanged: changed,
	})
}

func events_ModelInvoked(requestID string) events.DomainEvent {
	return events.NewModelInvoked(requestID, "mock", 0, 0)
}

func events_ProviderResponse(requestID string, in, out int, finishReason string) events.DomainEvent {
	return events.NewProviderResponseWithTelemetry(events.ProviderResponsePayload{
		RequestID: requestID, Model: "mock",
		TokenInput: in, TokenOutput: out, FinishReason: finishReason,
	})
}
