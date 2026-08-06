package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/events"
)

// TestStreamResponseTimeoutPublishesUsageEnvelope verifies the "Explicit Over
// Implicit" guarantee: when a stream dies on a context deadline, the partial
// token usage is published as a StreamUsage envelope BEFORE the timeout error
// is returned, so consumed tokens never vanish from telemetry.
func TestStreamResponseTimeoutPublishesUsageEnvelope(t *testing.T) {
	sse := strings.Join([]string{
		"data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial answer \"}}]}",
		"",
		"data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"that never finishes\"}}]}",
		"",
	}, "\n")

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = fmt.Fprint(w, sse)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Hold the connection open so the client read blocks until its
		// context deadline fires mid-stream.
		<-release
	}))
	defer srv.Close()
	defer close(release)

	bus := events.NewBus(64)
	defer bus.Close()

	client := NewOpenAIClient("test-key", "test-model", srv.URL).WithEventBus(bus)

	type envelopeResult struct {
		env  events.Envelope
		ok   bool
		done chan struct{}
	}
	res := &envelopeResult{done: make(chan struct{})}
	sub := bus.Subscribe("envelope.telemetry", func(ev events.DomainEvent) {
		if env, ok := events.EnvelopeFromEvent(ev); ok {
			if p, ok := env.Payload.(events.StreamUsagePayload); ok && p.Interrupted {
				res.env = env
				res.ok = true
				close(res.done)
			}
		}
	})
	defer sub.Cancel()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := client.StreamResponse(ctx, PromptRequest{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "hi"}},
	}, func(string) error { return nil })

	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "openai: stream") {
		t.Errorf("unexpected error: %v", err)
	}

	select {
	case <-res.done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for StreamUsage envelope")
	}
	if !res.ok {
		t.Fatal("envelope did not carry an interrupted StreamUsagePayload")
	}
	p := res.env.Payload.(events.StreamUsagePayload)
	if p.Model != "test-model" {
		t.Errorf("envelope model = %q, want test-model", p.Model)
	}
	// "partial answer that never finishes" is 31 chars → 7 estimated tokens.
	if p.OutputTokens < 1 {
		t.Errorf("envelope OutputTokens = %d, want a non-zero partial estimate", p.OutputTokens)
	}
}

// TestStreamResponseTimeoutErrorCarriesPartialTokens verifies the returned
// error path still reports the partial token usage on the LLMResponse value.
func TestStreamResponseTimeoutErrorCarriesPartialTokens(t *testing.T) {
	sse := strings.Join([]string{
		"data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"half-written output\"}}]}",
		"",
	}, "\n")

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = fmt.Fprint(w, sse)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release
	}))
	defer srv.Close()
	defer close(release)

	client := NewOpenAIClient("test-key", "test-model", srv.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	resp, err := client.StreamResponse(ctx, PromptRequest{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "hi"}},
	}, func(string) error { return nil })

	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
	// "half-written output" is 18 chars → 4 estimated output tokens.
	if resp.TokenOutput < 1 {
		t.Errorf("LLMResponse.TokenOutput = %d, want a non-zero partial estimate", resp.TokenOutput)
	}
}
