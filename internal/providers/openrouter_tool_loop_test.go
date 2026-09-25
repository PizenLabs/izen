package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
)

type stubToolRunner struct {
	calls []ai.ToolCall
}

func (s *stubToolRunner) Run(_ context.Context, call ai.ToolCall) (string, error) {
	s.calls = append(s.calls, call)
	return "TOOL-OUTPUT", nil
}

// TestExecute_AgenticToolLoopCompletes is the end-to-end adaptive loop: the
// agentic model responds with a read-only tool_call, the runtime executes it,
// feeds the result back, and the final answer is returned. Both HTTP payloads
// carry the read-only tool schemas.
func TestExecute_AgenticToolLoopCompletes(t *testing.T) {
	var (
		mu       sync.Mutex
		requests []map[string]any
		hits     int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		requests = append(requests, body)
		hits++
		n := hits
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			_, _ = w.Write([]byte(`{"id":"g1","object":"chat.completion","created":1,"model":"x","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"t1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"note.txt\"}"}}]},"finish_reason":"tool_calls"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"g2","object":"chat.completion","created":1,"model":"x","choices":[{"index":0,"message":{"role":"assistant","content":"final answer"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	p := NewOpenRouterProvider("test-key", "thinkingmachines/inkling-small:free", server.URL)
	p.client = server.Client()
	runner := &stubToolRunner{}
	p.SetToolRunner(runner)

	resp, err := p.Execute(context.Background(), ai.Request{
		Model:    "thinkingmachines/inkling-small:free",
		Messages: []ai.Message{{Role: "user", Content: "read note.txt"}},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if resp == nil || resp.Content != "final answer" {
		t.Fatalf("resp = %+v, want final answer", resp)
	}
	if len(runner.calls) != 1 || runner.calls[0].Function.Name != ai.ToolReadFile {
		t.Fatalf("runner calls = %+v, want one read_file", runner.calls)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("HTTP requests = %d, want 2 (tool turn + final turn)", len(requests))
	}
	for i, body := range requests {
		if names := requestToolNames(t, body); len(names) == 0 {
			t.Fatalf("request %d must carry read-only tool schemas, body=%v", i+1, body)
		}
	}
	// The second request must replay the assistant tool_call and the tool
	// result bound by tool_call_id.
	msgs, _ := requests[1]["messages"].([]any)
	sawToolResult := false
	for _, m := range msgs {
		entry, _ := m.(map[string]any)
		if entry["role"] == "tool" && entry["tool_call_id"] == "t1" && entry["content"] == "TOOL-OUTPUT" {
			sawToolResult = true
		}
	}
	if !sawToolResult {
		t.Fatalf("second payload must carry the tool result, messages=%v", msgs)
	}
}
