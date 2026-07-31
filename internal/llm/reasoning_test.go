package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/events"
)

func TestReasoningPublisherRoutesToBus(t *testing.T) {
	bus := events.NewBus(16)
	defer bus.Close()

	var got reasonChunks
	bus.Subscribe(events.EventReasoningStream, func(ev events.DomainEvent) {
		p := ev.Payload().(events.ReasoningPayload)
		got.add(p.Chunk)
	})

	handler := ReasoningPublisher(bus)
	_ = handler("chunk one")
	_ = handler("chunk two")
	_ = handler("chunk three")

	if !got.wait(3) {
		t.Fatalf("delivered %d chunks, want 3", got.Len())
	}

	if strings.Join(got.all(), "|") != "chunk one|chunk two|chunk three" {
		t.Errorf("delivered = %v", got.all())
	}
}

func TestReasoningPublisherWithCompletion(t *testing.T) {
	bus := events.NewBus(16)
	defer bus.Close()

	var got reasonPayloads
	bus.Subscribe(events.EventReasoningStream, func(ev events.DomainEvent) {
		got.add(ev.Payload().(events.ReasoningPayload))
	})

	handler, complete := ReasoningPublisherWithCompletion(bus)
	_ = handler("a")
	_ = handler("b")
	complete()

	if !got.wait(3) {
		t.Fatalf("delivered %d events, want 3", got.Len())
	}

	all := got.all()
	if all[0].Chunk != "a" || all[0].IsComplete {
		t.Errorf("event 0 = %+v", all[0])
	}
	if all[2].Chunk != "" || !all[2].IsComplete {
		t.Errorf("terminal event = %+v, want empty chunk + complete", all[2])
	}
}

type reasonChunks struct {
	mu sync.Mutex
	s  []string
}

func (r *reasonChunks) add(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.s = append(r.s, s)
}

func (r *reasonChunks) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.s))
	copy(out, r.s)
	return out
}

func (r *reasonChunks) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.s)
}

func (r *reasonChunks) wait(want int) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.Len() >= want {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

type reasonPayloads struct {
	mu sync.Mutex
	s  []events.ReasoningPayload
}

func (r *reasonPayloads) add(p events.ReasoningPayload) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.s = append(r.s, p)
}

func (r *reasonPayloads) all() []events.ReasoningPayload {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]events.ReasoningPayload, len(r.s))
	copy(out, r.s)
	return out
}

func (r *reasonPayloads) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.s)
}

func (r *reasonPayloads) wait(want int) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.Len() >= want {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func TestReasoningPublisherNilBusNoop(t *testing.T) {
	handler := ReasoningPublisher(nil)
	if err := handler("x"); err != nil {
		t.Errorf("nil-bus handler = %v, want nil", err)
	}
}

func TestOpenAIStreamRoutesReasoningSeparately(t *testing.T) {
	// SSE feed with interleaved reasoning_content and content deltas.
	sse := strings.Join([]string{
		"data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"deep \",\"content\":\"\"}}]}",
		"",
		"data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"think\",\"content\":\"\"}}]}",
		"",
		"data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello \"}}]}",
		"",
		"data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"world\"}}]}",
		"",
		"data: [DONE]",
		"",
	}, "\n")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, sse)
	}))
	defer srv.Close()

	client := NewOpenAIClient("test-key", "test-model", srv.URL)

	var reasoning, content strings.Builder
	req := PromptRequest{
		Messages:         []Message{{Role: "user", Content: "hi"}},
		ReasoningHandler: func(chunk string) error { reasoning.WriteString(chunk); return nil },
	}

	resp, err := client.StreamResponse(context.Background(), req, func(chunk string) error {
		content.WriteString(chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamResponse: %v", err)
	}

	if reasoning.String() != "deep think" {
		t.Errorf("reasoning = %q, want %q", reasoning.String(), "deep think")
	}
	if content.String() != "Hello world" {
		t.Errorf("content = %q, want %q", content.String(), "Hello world")
	}
	if resp.Content != "Hello world" {
		t.Errorf("resp.Content = %q, want %q (reasoning must not leak)", resp.Content, "Hello world")
	}
}

func TestAnthropicStreamRoutesThinkingSeparately(t *testing.T) {
	sse := strings.Join([]string{
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"analyze\"}}",
		"",
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\" this\"}}",
		"",
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Answer here\"}}",
		"",
		"data: {\"type\":\"message_stop\"}",
		"",
	}, "\n")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, sse)
	}))
	defer srv.Close()

	client := NewAnthropicClient("test-key", "test-model")
	client.baseURL = srv.URL

	var reasoning, content strings.Builder
	req := PromptRequest{
		Messages:         []Message{{Role: "user", Content: "hi"}},
		ReasoningHandler: func(chunk string) error { reasoning.WriteString(chunk); return nil },
	}

	resp, err := client.StreamResponse(context.Background(), req, func(chunk string) error {
		content.WriteString(chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamResponse: %v", err)
	}

	if reasoning.String() != "analyze this" {
		t.Errorf("reasoning = %q, want %q", reasoning.String(), "analyze this")
	}
	if content.String() != "Answer here" {
		t.Errorf("content = %q, want %q", content.String(), "Answer here")
	}
	if resp.Content != "Answer here" {
		t.Errorf("resp.Content = %q, want %q (thinking must not leak)", resp.Content, "Answer here")
	}
}
