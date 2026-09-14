package providers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/gateway"
)

func TestZeroToolPayloadOnGreeting_OpenRouter(t *testing.T) {
	minimal := gateway.BuildMinimalSystemPrompt()
	req := ai.Request{
		Model:    "openai/gpt-4o",
		System:   minimal,
		Messages: []ai.Message{{Role: "user", Content: "hi"}},
		Tools: []ai.ToolDefinition{
			{Type: "function", Function: ai.ToolFunction{Name: "write_file", Description: "test"}},
		},
	}
	p := NewOpenRouterProvider("fake", "openai/gpt-4o", "https://api.openai.com/v1")
	msgs := p.buildMessages(req)
	body := p.buildRequest("openai/gpt-4o", msgs, req, false)
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "\"tools\"") {
		t.Fatalf("casual payload must omit tools, got %s", string(data))
	}
	// Agentic must retain tools
	agentic := gateway.BuildAgenticSystemPrompt("ask", "Tester")
	req2 := ai.Request{
		Model:    "openai/gpt-4o",
		System:   agentic,
		Messages: []ai.Message{{Role: "user", Content: "refactor file.go"}},
		Tools: []ai.ToolDefinition{
			{Type: "function", Function: ai.ToolFunction{Name: "write_file", Description: "test"}},
		},
	}
	body2 := p.buildRequest("openai/gpt-4o", p.buildMessages(req2), req2, false)
	data2, _ := json.Marshal(body2)
	if !strings.Contains(string(data2), "\"tools\"") {
		t.Fatalf("agentic payload must include tools, got %s", string(data2))
	}
}
