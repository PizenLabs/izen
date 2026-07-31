package llm

import "github.com/PizenLabs/izen/internal/events"

// ReasoningPublisher returns a ReasoningHandler (see PromptRequest) that
// publishes every reasoning chunk as an EventReasoningStream domain event on
// the given bus. It lets any LLM client emit thinking as it arrives while the
// UI subscribes as a pure projection. A nil bus yields a no-op handler.
func ReasoningPublisher(bus *events.Bus) func(chunk string) error {
	return func(chunk string) error {
		if bus != nil {
			bus.Publish(events.NewReasoningStream(chunk, false))
		}
		return nil
	}
}

// ReasoningPublisherWithCompletion returns a ReasoningHandler that publishes
// each reasoning chunk as an incomplete EventReasoningStream and, once
// returned, publishes the terminal complete event (empty chunk, IsComplete) so
// projections can collapse the reasoning block.
func ReasoningPublisherWithCompletion(bus *events.Bus) (handler func(chunk string) error, complete func()) {
	handler = ReasoningPublisher(bus)
	complete = func() {
		if bus != nil {
			bus.Publish(events.NewReasoningStream("", true))
		}
	}
	return handler, complete
}
