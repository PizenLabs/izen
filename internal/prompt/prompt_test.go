package prompt

import (
	"strings"
	"testing"
)

func TestAskPromptHandoffContract_NoRoleplay(t *testing.T) {
	contract := AskPromptHandoffContract()

	forbidden := []string{
		"Senior DevOps",
		"Senior Architect",
		"Forensic Handoff",
		"Symptoms / Motivation",
		"FORENSIC HANDOFF VECTOR",
		"Smart Analysis",
		"Tradeoff",
	}
	for _, word := range forbidden {
		if strings.Contains(contract, word) {
			t.Errorf("AskPromptHandoffContract contains forbidden text %q", word)
		}
	}

	required := []string{
		"Goal:",
		"Targets:",
		"Steps:",
	}
	for _, word := range required {
		if !strings.Contains(contract, word) {
			t.Errorf("AskPromptHandoffContract missing required text %q", word)
		}
	}
}

func TestPlanContract_NoRoleplay(t *testing.T) {
	contract := PlanContract()

	forbidden := []string{
		"Senior Principal Structural Architect",
		"Forensic Ledger",
		"Forensic data compressor",
	}
	for _, word := range forbidden {
		if strings.Contains(contract, word) {
			t.Errorf("PlanContract contains forbidden text %q", word)
		}
	}
}

func TestCompactPlanContract_NoRoleplay(t *testing.T) {
	contract := CompactPlanContract()

	forbidden := []string{
		"Execution Mapper",
		"Senior Architect",
	}
	for _, word := range forbidden {
		if strings.Contains(contract, word) {
			t.Errorf("CompactPlanContract contains forbidden text %q", word)
		}
	}
}

func TestInvestigateContract_NoRoleplay(t *testing.T) {
	contract := InvestigateContract()

	forbidden := []string{
		"forensic data compressor",
		"Forensic Ledger",
	}
	for _, word := range forbidden {
		if strings.Contains(contract, word) {
			t.Errorf("InvestigateContract contains forbidden text %q", word)
		}
	}
}

func TestPlanSynthesisSystemPrompt_Compact(t *testing.T) {
	p := PlanSynthesisSystemPrompt()

	required := []string{
		"atomic_tasks",
		"architectural_strategy",
		"SHELL_EXEC",
		"FILE_MUTATE",
		"rationale",
		"no <think>",
	}
	for _, word := range required {
		if !strings.Contains(p, word) {
			t.Errorf("PlanSynthesisSystemPrompt missing required text %q", word)
		}
	}

	forbidden := []string{
		"Senior Architect",
		"Forensic Ledger",
		"CommonContract",
		"# PRINCIPLES",
	}
	for _, word := range forbidden {
		if strings.Contains(p, word) {
			t.Errorf("PlanSynthesisSystemPrompt contains forbidden text %q", word)
		}
	}

	// Compact enough for Mini/7B models: the whole instruction block stays
	// under ~220 words (a fraction of the composed prompt + schema block).
	if words := len(strings.Fields(p)); words > 220 {
		t.Fatalf("PlanSynthesisSystemPrompt too heavy for Mini models: %d words", words)
	}
}

// TestSchemaConsolidation verifies the prompt layer's output schemas are
// single-sourced in schema.go and referenced (not re-inlined) by the contracts
// that consume them. This is the P4 schema-consolidation seam check.
func TestSchemaConsolidation(t *testing.T) {
	// Every consolidated schema is defined exactly once, non-empty.
	schemas := []struct {
		name string
		body string
	}{
		{"PlanTaskSchema", PlanTaskSchema()},
		{"PlanSynthesisSchema", PlanSynthesisSchema()},
		{"PlanContractSchema", PlanContractSchema()},
		{"DispatchSchema", DispatchSchema()},
		{"TaskBlockSchemaInstruction", TaskBlockSchemaInstruction()},
	}
	for _, s := range schemas {
		if strings.TrimSpace(s.body) == "" {
			t.Errorf("%s returned empty schema", s.name)
		}
	}

	// The consumers must reference the consolidated definitions.
	if !strings.Contains(PlanSynthesisSystemPrompt(), PlanSynthesisSchema()) {
		t.Error("PlanSynthesisSystemPrompt no longer references PlanSynthesisSchema()")
	}
	if !strings.Contains(PlanContract(), PlanContractSchema()) {
		t.Error("PlanContract no longer references PlanContractSchema()")
	}
	jsonPrompt := BuildPlanJSONPrompt("problem", "ledger", "", false, "")
	if !strings.Contains(jsonPrompt, PlanTaskSchema()) {
		t.Error("BuildPlanJSONPrompt no longer references PlanTaskSchema()")
	}
	if !strings.Contains(TaskBlockSchemaInstruction(), TaskBlockSchemaTemplate) {
		t.Error("TaskBlockSchemaInstruction no longer references TaskBlockSchemaTemplate")
	}
}

// TestCasualChatRespondsToStyleToggle verifies the P4 unify-prompts seam: the
// casual chat system prompt is composed through the active StylePolicy, so
// `izen config style` adapts it exactly like every other mode — with no
// hardcoded verbosity directive.
func TestCasualChatRespondsToStyleToggle(t *testing.T) {
	prev := SetActiveStyle(StyleBalanced)
	defer SetActiveStyle(prev)

	balanced := CasualChatSystemPrompt()
	if !strings.Contains(balanced, "OUTPUT STYLE: Balanced") {
		t.Fatalf("balanced casual chat prompt missing style directive:\n%s", balanced)
	}

	SetActiveStyle(StyleUltra)
	ultra := CasualChatSystemPrompt()
	if !strings.Contains(ultra, "OUTPUT STYLE: Ultra") {
		t.Fatalf("ultra casual chat prompt missing style directive:\n%s", ultra)
	}
	if balanced == ultra {
		t.Error("casual chat prompt did not adapt to the style toggle")
	}
}

func TestMiniModelJSONConstraint(t *testing.T) {
	miniModels := []string{
		"cohere/command-r-mini",
		"openai/gpt-4o-mini",
		"gemini-2.0-flash",
		"Cohere North Mini",
		"meta-llama/llama-3.2-1b-instruct",
	}
	for _, m := range miniModels {
		if !IsMiniModel(m) {
			t.Errorf("IsMiniModel(%q) = false, want true", m)
		}
		if c := MiniModelJSONConstraint(m); !strings.Contains(c, "raw JSON") {
			t.Errorf("MiniModelJSONConstraint(%q) = %q, want raw-JSON directive", m, c)
		}
	}

	fullModels := []string{"claude-sonnet-4-20250514", "openai/gpt-4o", "deepseek-chat", ""}
	for _, m := range fullModels {
		if IsMiniModel(m) {
			t.Errorf("IsMiniModel(%q) = true, want false", m)
		}
		if c := MiniModelJSONConstraint(m); c != "" {
			t.Errorf("MiniModelJSONConstraint(%q) = %q, want empty", m, c)
		}
	}
}
