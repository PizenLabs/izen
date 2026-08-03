package gateway

import (
	"strings"
	"testing"
)

func TestIsCasualChat_Greetings(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"hi", true},
		{"hello", true},
		{"hey there", true},
		{"greetings", true},
		{"good morning", true},
		{"good afternoon", true},
		{"good evening", true},
		{"good night", true},
		{"hi!", true},
		{"hello!", true},
		{"hey!", true},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := IsCasualChat(tc.input)
			if got != tc.want {
				t.Errorf("IsCasualChat(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestIsCasualChat_SmallTalk(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"how are you", true},
		{"how's it going", true},
		{"what's up", true},
		{"who are you", true},
		{"what are you", true},
		{"tell me about yourself", true},
		{"what can you do", true},
		{"help me", true},
		{"can you help", true},
		{"i need help", true},
		{"i don't know", true},
		{"is that you", true},
		{"are you there", true},
		{"got it", true},
		{"ok", true},
		{"okay", true},
		{"thanks", true},
		{"thank you", true},
		{"thanks!", true},
		{"thank you!", true},
		{"cheers", true},
		{"bye", true},
		{"goodbye", true},
		{"see you", true},
		{"nice", true},
		{"great", true},
		{"awesome", true},
		{"cool", true},
		{"lol", true},
		{"ahaha", true},
		{"haha", true},
		{"hmm", true},
		{"oh", true},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := IsCasualChat(tc.input)
			if got != tc.want {
				t.Errorf("IsCasualChat(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestIsCasualChat_CodingTasks(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"$prompt fix the bug", false},
		{"$ask what is this", false},
		{"/build", false},
		{"/plan", false},
		{"/hotfix", false},
		{"/investigate", false},
		{"/review", false},
		{"$prompt", false},
		{"$ask", false},
		{"fix the bug in main.go", false},
		{"update @README.md", false},
		{"refactor handler.go", false},
		{"compile error in src/main.go", false},
		{"undefined symbol Log", false},
		{"npm install lodash", false},
		{"go mod tidy", false},
		{"import json", false},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := IsCasualChat(tc.input)
			if got != tc.want {
				t.Errorf("IsCasualChat(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestIsCasualChat_QuestionPatterns(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"wikipedia", true},
		{"what is the capital of france", true},
		{"how does a compiler work", true},
		{"explain recursion", false},
		{"define polymorphism", true},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := IsCasualChat(tc.input)
			if got != tc.want {
				t.Errorf("IsCasualChat(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestIsCasualChat_Empty(t *testing.T) {
	if IsCasualChat("") {
		t.Error("IsCasualChat(\"\") = true, want false")
	}
	if IsCasualChat("  ") {
		t.Error("IsCasualChat(\"  \") = true, want false")
	}
}

func TestCasualChatSystemPrompt(t *testing.T) {
	p := CasualChatSystemPrompt()
	required := "Always identify as IZEN if asked about your name, role, or identity."
	if !strings.Contains(p, required) {
		t.Errorf("CasualChatSystemPrompt() missing identity contract %q:\n%s", required, p)
	}
	// P4: the hardcoded "Respond concisely in 1-2 short sentences." directive is
	// gone — verbosity is now injected dynamically via the active StylePolicy.
	if strings.Contains(p, "Respond concisely") {
		t.Errorf("CasualChatSystemPrompt() still carries a hardcoded conciseness directive:\n%s", p)
	}
	if !strings.Contains(p, "OUTPUT STYLE") {
		t.Errorf("CasualChatSystemPrompt() missing active style directive:\n%s", p)
	}
}

func TestCasualChatMaxTokens(t *testing.T) {
	got := CasualChatMaxTokens()
	if got != 2048 {
		t.Errorf("CasualChatMaxTokens() = %d, want 2048", got)
	}
}
