package session

import (
	"strings"
	"testing"
)

func TestGetLLMMessages_CasualStripsSystemTraces(t *testing.T) {
	s := New()
	s.AddMessage("user", "hi", 10)
	s.AddMessage("assistant", "hello", 10)
	s.AddMessage("system", "[event] PromptAdmitted intent=modification latency=42ms", 10)
	s.AddMessage("system", "command submit_prompt failed: handlers: empty prompt", 10)
	s.AddMessage("user", "how are you", 10)

	casualHist := s.GetLLMMessages(true)
	for _, m := range casualHist {
		if m.Role == "system" {
			t.Fatalf("casual history must not contain system, got %q", m.Content)
		}
		if strings.Contains(m.Content, "[event]") || strings.Contains(m.Content, "submit_prompt failed") {
			t.Fatalf("leaked internal trace: %q", m.Content)
		}
	}
	if len(casualHist) > 6 {
		t.Fatalf("casual len=%d want ≤6", len(casualHist))
	}
}

func TestGetLLMMessages_CasualStripsHeavyBlocks(t *testing.T) {
	s := New()
	s.AddMessage("user", "hi\n\n## GOVERNED FILE CONTEXT\nfile.go", 10)
	casual := s.GetLLMMessages(true)
	if len(casual) != 1 {
		t.Fatalf("expected 1, got %d", len(casual))
	}
	if strings.Contains(casual[0].Content, "GOVERNED FILE CONTEXT") {
		t.Fatalf("failed to strip GOVERNED: %q", casual[0].Content)
	}
	s2 := New()
	s2.AddMessage("user", "### ACTIVE OBJECTIVE\nID: obj-1\nIntent: build\n\nactual question", 10)
	casual2 := s2.GetLLMMessages(true)
	if len(casual2) != 1 || casual2[0].Content != "actual question" {
		t.Fatalf("ACTIVE OBJECTIVE not stripped: %#v", casual2)
	}
}

func TestGetLLMMessages_AgenticKeepsPolicyNotice(t *testing.T) {
	s := New()
	s.AddMessage("system", "Tool 'shell' rejected in /ask. You are in a Read-Only execution environment and must stop requesting system mutations.", 10)
	s.AddMessage("user", "do it", 10)
	agentic := s.GetLLMMessages(false)
	found := false
	for _, m := range agentic {
		if m.Role == "system" && strings.Contains(m.Content, "Read-Only") {
			found = true
		}
	}
	if !found {
		t.Fatal("agentic history should retain policy notice")
	}
}
