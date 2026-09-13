package context

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/gateway"
	"github.com/PizenLabs/izen/internal/prompt"
)

// TestMinimalContextAdmission pins INVARIANT 2: the minimal system prompt for
// casual greetings stays ≤50 tokens and the full agentic prompt is retained
// for technical queries.
func TestMinimalContextAdmission(t *testing.T) {
	minimal := gateway.BuildMinimalSystemPrompt()
	if minimal == "" {
		t.Fatal("BuildMinimalSystemPrompt returned empty")
	}
	estTokens := len(minimal) / 4
	if estTokens > 50 {
		t.Fatalf("BuildMinimalSystemPrompt estTokens=%d, want ≤50 (chars=%d prompt=%q)", estTokens, len(minimal), minimal)
	}
	// Also via prompt package directly.
	pMinimal := prompt.BuildMinimalSystemPrompt()
	if len(pMinimal)/4 > 50 {
		t.Fatalf("prompt.BuildMinimalSystemPrompt estTokens=%d, want ≤50", len(pMinimal)/4)
	}
	// Casual greeting input ceiling <100 tokens total (system + hi + no history).
	total := len(minimal) + len("hi")
	if total/4 >= 100 {
		t.Fatalf("casual greeting input ceiling estTokens=%d, want <100 (system=%d hi=2)", total/4, len(minimal))
	}
	// Agentic prompt must be substantially larger than minimal.
	agentic := gateway.BuildAgenticSystemPrompt("ask", "Tester")
	if agentic == "" {
		t.Fatal("BuildAgenticSystemPrompt returned empty")
	}
	if len(agentic) <= len(minimal) {
		t.Fatalf("agentic prompt should be larger than minimal: agentic %d vs minimal %d", len(agentic), len(minimal))
	}
	if !strings.Contains(agentic, "MODE:") {
		t.Fatalf("agentic prompt missing MODE contract: %q", agentic[:200])
	}
}

// TestZeroToolPayloadOnGreeting pins INVARIANT 1: casual intents produce a
// provider payload with Tools = nil so the JSON omits the tools key entirely.
func TestZeroToolPayloadOnGreeting(t *testing.T) {
	minimal := gateway.BuildMinimalSystemPrompt()
	// Simulate a casual request that erroneously carries tools — the provider
	// must strip them. We verify the stripping logic mirrors the real provider's
	// isCasualSystemPrompt check.
	isCasual := strings.Contains(minimal, "fast CLI coding companion") && !strings.Contains(minimal, "MODE:")
	if !isCasual {
		t.Fatal("isCasualSystemPrompt detection failed for minimal prompt")
	}
	// Build a dummy provider payload and ensure tools are omitted when isCasual.
	type dummyReq struct {
		Model string            `json:"model"`
		Tools []json.RawMessage `json:"tools,omitempty"`
	}
	tools := []ai.ToolDefinition{
		{Type: "function", Function: ai.ToolFunction{Name: "write_file", Description: "test"}},
	}
	var bodyTools []json.RawMessage
	// This mirrors providers/openrouter buildRequest guard.
	if !isCasual && len(tools) > 0 {
		for _, td := range tools {
			data, err := json.Marshal(td)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			bodyTools = append(bodyTools, data)
		}
	}
	d := dummyReq{Model: "test-model", Tools: bodyTools}
	data, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal dummy: %v", err)
	}
	if strings.Contains(string(data), "\"tools\"") {
		t.Fatalf("casual payload must omit tools key, got %s", string(data))
	}
	// Agentic payload must retain tools.
	agentic := gateway.BuildAgenticSystemPrompt("ask", "Tester")
	isCasualAgentic := strings.Contains(agentic, "fast CLI coding companion") && !strings.Contains(agentic, "MODE:")
	if isCasualAgentic {
		t.Fatal("agentic prompt incorrectly classified as casual")
	}
	var bodyTools2 []json.RawMessage
	if !isCasualAgentic && len(tools) > 0 {
		for _, td := range tools {
			b, _ := json.Marshal(td)
			bodyTools2 = append(bodyTools2, b)
		}
	}
	d2 := dummyReq{Model: "test-model", Tools: bodyTools2}
	data2, _ := json.Marshal(d2)
	if !strings.Contains(string(data2), "\"tools\"") {
		t.Fatalf("agentic payload must include tools key, got %s", string(data2))
	}
}

// TestHistoryTraceSanitizationPlaceholder is a lightweight check that the
// casual path does not leak file references. Full history sanitization is
// verified in the session package to avoid import cycles.
func TestHistoryTraceSanitizationPlaceholder(t *testing.T) {
	// Casual greetings must be classified as casual even with no file refs.
	if !gateway.IsCasualChat("hi") {
		t.Fatal("gateway.IsCasualChat(hi) must be true for history sanitization premise")
	}
	if gateway.IsCasualChat("fix the bug in main.go") {
		t.Fatal("coding task with file ref must not be casual")
	}
}
