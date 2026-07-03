package plan

import (
	"strings"
	"testing"
)

func TestValidatePlanOutput_Valid(t *testing.T) {
	content := "- [ ] FILE_MUTATE: internal/foo.go | Add Foo handler\n- [ ] SHELL_EXEC: go build ./... | Check compilation\n- [x] GIT_ACTION: commit -m \"msg\" | Commit"
	result := ValidatePlanOutput(content)
	if !result.Valid {
		t.Fatalf("expected valid, got %d invalid lines", len(result.Invalid))
	}
	if len(result.Blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(result.Blocks))
	}
	if result.Blocks[0].Type != "FILE_MUTATE" {
		t.Fatalf("expected FILE_MUTATE, got %s", result.Blocks[0].Type)
	}
	if result.Blocks[2].Checked != true {
		t.Fatal("expected block 3 to be checked")
	}
}

func TestValidatePlanOutput_InvalidLines(t *testing.T) {
	content := "some prose\n- [ ] invalid line\n- [ ] FILE_MUTATE: fix bug\n- [ ] SHELL_EXEC: go test | Run tests"
	result := ValidatePlanOutput(content)
	if result.Valid {
		t.Fatal("expected invalid result")
	}
	if len(result.Invalid) != 2 {
		t.Fatalf("expected 2 invalid lines, got %d", len(result.Invalid))
	}
	if len(result.Blocks) != 1 {
		t.Fatalf("expected 1 valid block, got %d", len(result.Blocks))
	}
}

func TestValidatePlanOutput_Empty(t *testing.T) {
	result := ValidatePlanOutput("")
	if !result.Valid {
		t.Fatal("expected valid for empty")
	}
	if len(result.Blocks) != 0 {
		t.Fatalf("expected 0 blocks, got %d", len(result.Blocks))
	}
}

func TestTokenBudgetForModel(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		{"qwen2.5-coder:7b", 4000},
		{"qwen2.5-coder:14b", 6000},
		{"claude-sonnet-4-20250514", 16000},
		{"unknown-model", 4000},
	}
	for _, tt := range tests {
		got := TokenBudgetForModel(tt.model)
		if got != tt.want {
			t.Errorf("TokenBudgetForModel(%q) = %d, want %d", tt.model, got, tt.want)
		}
	}
}

func TestCheckTokenBudget(t *testing.T) {
	err := CheckTokenBudget("qwen2.5-coder:7b", 5000)
	if err == nil {
		t.Fatal("expected error for over-budget")
	}
	if err.Diff != 1000 {
		t.Fatalf("expected diff 1000, got %d", err.Diff)
	}

	err2 := CheckTokenBudget("qwen2.5-coder:7b", 3000)
	if err2 != nil {
		t.Fatalf("expected nil, got %v", err2)
	}
}

func TestEstimateTokens(t *testing.T) {
	if EstimateTokens("") != 0 {
		t.Fatal("expected 0 for empty")
	}
	got := EstimateTokens(strings.Repeat("a", 1000))
	if got != 250 {
		t.Fatalf("expected 250 for 1000 chars, got %d", got)
	}
}

func TestCollapsePlanSections(t *testing.T) {
	content := "# Header\nsome prose\n- [ ] FILE_MUTATE: a.go | change a\n- [ ] SHELL_EXEC: go test | run tests\n\ntrailing"
	collapsed := CollapsePlanSections(content)
	lines := strings.Split(collapsed, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), collapsed)
	}
}

func TestFormatValidationError(t *testing.T) {
	result := ValidatePlanOutput("- [ ] invalid task line")
	msg := FormatValidationError(result)
	if msg == "" {
		t.Fatal("expected non-empty error message")
	}
	if !strings.Contains(msg, "schema violation") {
		t.Fatalf("expected 'schema violation' in message, got: %s", msg)
	}
}

func TestSchemaInstruction(t *testing.T) {
	inst := SchemaInstruction()
	if !strings.Contains(inst, "FILE_MUTATE") {
		t.Fatal("expected FILE_MUTATE in schema instruction")
	}
	if !strings.Contains(inst, "SHELL_EXEC") {
		t.Fatal("expected SHELL_EXEC in schema instruction")
	}
	if !strings.Contains(inst, "GIT_ACTION") {
		t.Fatal("expected GIT_ACTION in schema instruction")
	}
}

func TestIsValidTaskLine(t *testing.T) {
	if !IsValidTaskLine("- [ ] FILE_MUTATE: a.go | desc") {
		t.Fatal("expected valid task line")
	}
	if IsValidTaskLine("invalid line") {
		t.Fatal("expected invalid task line")
	}
}

func TestBudgetExceededError(t *testing.T) {
	err := &BudgetExceededError{Model: "test", Budget: 100, Actual: 150, Diff: 50}
	if !strings.Contains(err.Error(), "token budget exceeded") {
		t.Fatal("expected 'token budget exceeded' in error")
	}
	hint := err.BudgetActionHint()
	if !strings.Contains(hint, "/drop") {
		t.Fatal("expected '/drop' in action hint")
	}
}
