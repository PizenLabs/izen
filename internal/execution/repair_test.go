package execution

import (
	"testing"
)

func TestRepairRecoveryPrompt_ContainsStrictInstructions(t *testing.T) {
	prompt := RepairRecoveryPrompt("output_budget_exhausted: previous FULL_REWRITE exceeded budget")
	if prompt == "" {
		t.Fatal("RepairRecoveryPrompt returned empty string")
	}
	if !contains(prompt, "ONLY valid SEARCH/REPLACE") {
		t.Errorf("prompt must demand ONLY valid SEARCH/REPLACE blocks")
	}
	if !contains(prompt, "PROHIBITED") {
		t.Errorf("prompt must prohibit explaining text and full-file output")
	}
	if !contains(prompt, "<<<<<<< SEARCH") {
		t.Errorf("prompt must reference SEARCH marker format")
	}
	if !contains(prompt, ">>>>>>> REPLACE") {
		t.Errorf("prompt must reference REPLACE marker format")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && len(sub) > 0 && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
