package gateway

import "testing"

func TestCompressPrompt_RefactorLicense(t *testing.T) {
	input := "refactor MIT LICENSE to APACHE 2.0 LICENSE @LICENSE"
	result := CompressPrompt(input)
	if result == nil {
		t.Fatal("CompressPrompt returned nil for refactor license prompt")
	}
	if result.Action != "REFACTOR_FILE" {
		t.Errorf("Action = %q, want REFACTOR_FILE", result.Action)
	}
	if result.Target != "LICENSE" {
		t.Errorf("Target = %q, want LICENSE", result.Target)
	}
	if result.SourceFormat != "MIT" {
		t.Errorf("SourceFormat = %q, want MIT", result.SourceFormat)
	}
	if result.TargetFormat != "APACHE_2.0" {
		t.Errorf("TargetFormat = %q, want APACHE_2.0", result.TargetFormat)
	}
	if !result.BypassInvest {
		t.Error("BypassInvest = false, want true")
	}
}

func TestCompressPrompt_TaskSpec(t *testing.T) {
	input := `[TASK_SPEC]
ACTION: REFACTOR_FILE
TARGET: LICENSE
SOURCE_FORMAT: MIT
TARGET_FORMAT: APACHE_2.0
BYPASS_INVESTIGATION: TRUE
[CONSTRAINT]
Return ONLY the minimal JSON proposal spec.`
	result := CompressPrompt(input)
	if result == nil {
		t.Fatal("CompressPrompt returned nil for task spec prompt")
	}
	if result.Action != "REFACTOR_FILE" {
		t.Errorf("Action = %q, want REFACTOR_FILE", result.Action)
	}
	if result.Target != "LICENSE" {
		t.Errorf("Target = %q, want LICENSE", result.Target)
	}
	if result.SourceFormat != "MIT" {
		t.Errorf("SourceFormat = %q, want MIT", result.SourceFormat)
	}
	if result.TargetFormat != "APACHE_2.0" {
		t.Errorf("TargetFormat = %q, want APACHE_2.0", result.TargetFormat)
	}
	if !result.BypassInvest {
		t.Error("BypassInvest = false, want true")
	}
}

func TestCompressPrompt_FlatJSON(t *testing.T) {
	input := `{"target": "LICENSE", "action": "REPLACE", "template_key": "apache-2.0"}`
	result := CompressPrompt(input)
	if result == nil {
		t.Fatal("CompressPrompt returned nil for flat JSON prompt")
	}
	if result.Target != "LICENSE" {
		t.Errorf("Target = %q, want LICENSE", result.Target)
	}
	if result.Action != "REPLACE" {
		t.Errorf("Action = %q, want REPLACE", result.Action)
	}
}

func TestCompressPrompt_JSONCodeFence(t *testing.T) {
	input := "```json\n{\"target\": \"LICENSE\", \"action\": \"REPLACE\", \"template_key\": \"apache-2.0\"}\n```"
	result := CompressPrompt(input)
	if result == nil {
		t.Fatal("CompressPrompt returned nil for JSON code fence prompt")
	}
	if result.Target != "LICENSE" {
		t.Errorf("Target = %q, want LICENSE", result.Target)
	}
}

func TestCompressPrompt_PlanBlock(t *testing.T) {
	input := "[PLAN] refactor MIT to APACHE"
	result := CompressPrompt(input)
	if result == nil {
		t.Fatal("CompressPrompt returned nil for plan block prompt")
	}
	if result.Action != "REFACTOR_FILE" {
		t.Errorf("Action = %q, want REFACTOR_FILE", result.Action)
	}
	if result.Target != "MIT" {
		t.Errorf("Target = %q, want MIT", result.Target)
	}
	if result.TargetFormat != "APACHE" {
		t.Errorf("TargetFormat = %q, want APACHE", result.TargetFormat)
	}
	if !result.BypassInvest {
		t.Error("BypassInvest = false, want true")
	}
}

func TestCompressPrompt_Empty(t *testing.T) {
	result := CompressPrompt("")
	if result != nil {
		t.Error("CompressPrompt(\"\") should return nil")
	}
}

func TestCompressPrompt_NoMatch(t *testing.T) {
	input := "write a poem about the ocean"
	result := CompressPrompt(input)
	if result != nil {
		t.Error("CompressPrompt should return nil for non-mutation prompt")
	}
}

func TestCompressPrompt_ChangeLicense(t *testing.T) {
	input := "change license from MIT to Apache 2.0 in LICENSE file"
	result := CompressPrompt(input)
	if result == nil {
		t.Fatal("CompressPrompt returned nil for change license prompt")
	}
	if result.Target != "LICENSE" {
		t.Errorf("Target = %q, want LICENSE", result.Target)
	}
	if !result.BypassInvest {
		t.Error("BypassInvest = false, want true")
	}
}

func TestCompressPrompt_ConvertLicense(t *testing.T) {
	input := "convert MIT license to Apache license"
	result := CompressPrompt(input)
	if result == nil {
		t.Fatal("CompressPrompt returned nil for convert license prompt")
	}
	if result.SourceFormat != "MIT" {
		t.Errorf("SourceFormat = %q, want MIT", result.SourceFormat)
	}
	if result.TargetFormat != "APACHE_2.0" {
		t.Errorf("TargetFormat = %q, want APACHE_2.0", result.TargetFormat)
	}
}

func TestCompressPrompt_ReplaceLicense(t *testing.T) {
	input := "replace MIT license with Apache 2.0 license"
	result := CompressPrompt(input)
	if result == nil {
		t.Fatal("CompressPrompt returned nil for replace license prompt")
	}
	if result.Target != "LICENSE" {
		t.Errorf("Target = %q, want LICENSE", result.Target)
	}
	if !result.BypassInvest {
		t.Error("BypassInvest = false, want true")
	}
}
