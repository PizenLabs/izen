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
	if len(result.Invalid) != 1 {
		t.Fatalf("expected 1 invalid line, got %d", len(result.Invalid))
	}
	if len(result.Blocks) != 2 {
		t.Fatalf("expected 2 valid blocks, got %d", len(result.Blocks))
	}
	if result.Blocks[0].Target != "fix bug" {
		t.Fatalf("expected target 'fix bug', got %q", result.Blocks[0].Target)
	}
	if result.Blocks[0].Rationale != "Code mutation requested by system plan" {
		t.Fatalf("expected fallback rationale, got %q", result.Blocks[0].Rationale)
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

func TestValidatePlanOutput_Resilience_MarkdownPrefix(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		wantTgt string
		wantRat string
	}{
		{"asterisk_prefix", "* FILE_MUTATE: internal/foo.go | Add handler", "internal/foo.go", "Add handler"},
		{"dash_prefix", "- FILE_MUTATE: internal/bar.go | Fix bug", "internal/bar.go", "Fix bug"},
		{"checked_dash_prefix", "- [x] SHELL_EXEC: go vet ./... | Vet check", "go vet ./...", "Vet check"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidatePlanOutput(tt.line)
			if !result.Valid {
				t.Fatalf("expected valid, got invalid: %v", result.Invalid)
			}
			if len(result.Blocks) != 1 {
				t.Fatalf("expected 1 block, got %d", len(result.Blocks))
			}
			if result.Blocks[0].Target != tt.wantTgt {
				t.Errorf("target = %q, want %q", result.Blocks[0].Target, tt.wantTgt)
			}
			if result.Blocks[0].Rationale != tt.wantRat {
				t.Errorf("rationale = %q, want %q", result.Blocks[0].Rationale, tt.wantRat)
			}
		})
	}
}

func TestValidatePlanOutput_Resilience_QuoteStripping(t *testing.T) {
	content := "- [ ] FILE_MUTATE: 'internal/auth/jwt_service.go' | Add JWT auth"
	result := ValidatePlanOutput(content)
	if !result.Valid {
		t.Fatalf("expected valid, got %v", result.Invalid)
	}
	if len(result.Blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(result.Blocks))
	}
	if result.Blocks[0].Target != "internal/auth/jwt_service.go" {
		t.Fatalf("target = %q, want %q (quotes should be stripped)", result.Blocks[0].Target, "internal/auth/jwt_service.go")
	}
}

func TestValidatePlanOutput_Resilience_FallbackRationale(t *testing.T) {
	content := "- [ ] FILE_MUTATE: cmd/main.go"
	result := ValidatePlanOutput(content)
	if !result.Valid {
		t.Fatalf("expected valid with fallback rationale, got %v", result.Invalid)
	}
	if len(result.Blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(result.Blocks))
	}
	if result.Blocks[0].Rationale != "Code mutation requested by system plan" {
		t.Fatalf("rationale = %q, want fallback", result.Blocks[0].Rationale)
	}
}

func TestValidatePlanOutput_Resilience_BareTypeLine(t *testing.T) {
	content := "FILE_MUTATE: internal/handler.go | Refactor handler"
	result := ValidatePlanOutput(content)
	if !result.Valid {
		t.Fatalf("expected valid for bare TYPE line, got %v", result.Invalid)
	}
	if len(result.Blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(result.Blocks))
	}
	if result.Blocks[0].Type != "FILE_MUTATE" {
		t.Fatalf("type = %q, want FILE_MUTATE", result.Blocks[0].Type)
	}
}

func TestValidatePlanOutput_Resilience_CombinedDecorations(t *testing.T) {
	content := `* FILE_MUTATE: 'pkg/config/loader.go' | Update config loader
- [x] SHELL_EXEC: 'go test ./...' | Run all tests
- [ ] GIT_ACTION: commit -m "fix" | Commit fix`
	result := ValidatePlanOutput(content)
	if !result.Valid {
		t.Fatalf("expected valid, got %d invalid lines: %v", len(result.Invalid), result.Invalid)
	}
	if len(result.Blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(result.Blocks))
	}
	if result.Blocks[0].Target != "pkg/config/loader.go" {
		t.Fatalf("block 0 target = %q, want %q", result.Blocks[0].Target, "pkg/config/loader.go")
	}
	if result.Blocks[1].Checked != true {
		t.Fatal("expected block 1 to be checked")
	}
	if result.Blocks[1].Target != "go test ./..." {
		t.Fatalf("block 1 target = %q, want %q (quotes stripped)", result.Blocks[1].Target, "go test ./...")
	}
}

func TestValidatePlanOutput_Resilience_BacktickTarget(t *testing.T) {
	content := "- [ ] FILE_MUTATE: `internal/auth/jwt_service.go` | Add JWT auth"
	result := ValidatePlanOutput(content)
	if !result.Valid {
		t.Fatalf("expected valid, got %v", result.Invalid)
	}
	if len(result.Blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(result.Blocks))
	}
	if result.Blocks[0].Target != "internal/auth/jwt_service.go" {
		t.Fatalf("target = %q, want %q (backticks should be stripped)", result.Blocks[0].Target, "internal/auth/jwt_service.go")
	}
}

func TestValidatePlanOutput_Resilience_VerboseFormat(t *testing.T) {
	content := "- [ ] TYPE: FILE_MUTATE | Target: internal/handler.go | Rationale: Refactor handler"
	result := ValidatePlanOutput(content)
	if !result.Valid {
		t.Fatalf("expected valid for verbose format, got %v", result.Invalid)
	}
	if len(result.Blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(result.Blocks))
	}
	if result.Blocks[0].Type != "FILE_MUTATE" {
		t.Fatalf("type = %q, want FILE_MUTATE", result.Blocks[0].Type)
	}
	if result.Blocks[0].Target != "internal/handler.go" {
		t.Fatalf("target = %q, want %q", result.Blocks[0].Target, "internal/handler.go")
	}
	if result.Blocks[0].Rationale != "Refactor handler" {
		t.Fatalf("rationale = %q, want %q", result.Blocks[0].Rationale, "Refactor handler")
	}
}

func TestValidatePlanOutput_Resilience_VerboseFormatBare(t *testing.T) {
	// Verbose format without Rationale should get fallback
	content := "- [ ] TYPE: FILE_MUTATE | Target: cmd/main.go"
	result := ValidatePlanOutput(content)
	if !result.Valid {
		t.Fatalf("expected valid for verbose format without rationale, got %v", result.Invalid)
	}
	if len(result.Blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(result.Blocks))
	}
	if result.Blocks[0].Target != "cmd/main.go" {
		t.Fatalf("target = %q, want %q", result.Blocks[0].Target, "cmd/main.go")
	}
	if result.Blocks[0].Rationale != "Code mutation requested by system plan" {
		t.Fatalf("rationale = %q, want fallback", result.Blocks[0].Rationale)
	}
}

func TestValidatePlanOutput_Resilience_FragmentedLines(t *testing.T) {
	content := "- [ ] TYPE: FILE_MUTATE | Target: `internal/auth/jwt_service.go`\n- Implement JWT service."
	result := ValidatePlanOutput(content)
	if !result.Valid {
		t.Fatalf("expected valid after line merge, got %v", result.Invalid)
	}
	if len(result.Blocks) != 1 {
		t.Fatalf("expected 1 merged block, got %d", len(result.Blocks))
	}
	if result.Blocks[0].Type != "FILE_MUTATE" {
		t.Fatalf("type = %q, want FILE_MUTATE", result.Blocks[0].Type)
	}
	if result.Blocks[0].Target != "internal/auth/jwt_service.go" {
		t.Fatalf("target = %q, want %q (backticks stripped)", result.Blocks[0].Target, "internal/auth/jwt_service.go")
	}
	if result.Blocks[0].Rationale != "Implement JWT service." {
		t.Fatalf("rationale = %q, want %q", result.Blocks[0].Rationale, "Implement JWT service.")
	}
}

func TestValidatePlanOutput_Resilience_FragmentedDashLine(t *testing.T) {
	// L2 with dash prefix stripped during merge
	content := "- [ ] TYPE: FILE_MUTATE | Target: pkg/foo.go\n- Add foo feature"
	result := ValidatePlanOutput(content)
	if !result.Valid {
		t.Fatalf("expected valid after line merge, got %v", result.Invalid)
	}
	if len(result.Blocks) != 1 {
		t.Fatalf("expected 1 merged block, got %d", len(result.Blocks))
	}
	if result.Blocks[0].Rationale != "Add foo feature" {
		t.Fatalf("rationale = %q, want %q", result.Blocks[0].Rationale, "Add foo feature")
	}
}

func TestValidatePlanOutput_Resilience_FragmentedWithLeadingTasks(t *testing.T) {
	// Mix of standard tasks and fragmented verbose tasks
	content := `- [ ] SHELL_EXEC: go vet ./... | Run vet
- [ ] TYPE: FILE_MUTATE | Target: internal/db.go
- Add DB migration
- [x] GIT_ACTION: commit -m "init" | Initial commit`
	result := ValidatePlanOutput(content)
	if !result.Valid {
		t.Fatalf("expected valid, got %v", result.Invalid)
	}
	if len(result.Blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(result.Blocks))
	}
	if result.Blocks[0].Type != "SHELL_EXEC" {
		t.Fatalf("block 0 type = %q", result.Blocks[0].Type)
	}
	if result.Blocks[1].Target != "internal/db.go" {
		t.Fatalf("block 1 target = %q, want %q", result.Blocks[1].Target, "internal/db.go")
	}
	if result.Blocks[1].Rationale != "Add DB migration" {
		t.Fatalf("block 1 rationale = %q, want %q", result.Blocks[1].Rationale, "Add DB migration")
	}
	if result.Blocks[2].Checked != true {
		t.Fatal("expected block 2 checked")
	}
}

func TestCollapsePlanSections_Fragmented(t *testing.T) {
	content := "# Plan\nsome prose\n- [ ] TYPE: FILE_MUTATE | Target: a.go\n- change a\n- [ ] SHELL_EXEC: go test | run tests\n\ntrailing"
	collapsed := CollapsePlanSections(content)
	lines := strings.Split(collapsed, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), collapsed)
	}
	if !strings.Contains(lines[0], "change a") {
		t.Fatalf("expected merged rationale in line 0, got: %s", lines[0])
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

func TestParseJSONPlan_Valid(t *testing.T) {
	input := `{
  "context_anchor": {
    "source": "user-request",
    "target_packages": ["internal/parser", "tui/viewport"]
  },
  "architectural_strategy": "Isolate the parser logic to prevent streaming leakage",
  "atomic_tasks": [
    {
      "task_id": 1,
      "file": "internal/parser/stream.go",
      "strategy": "ATOMIC_REPLACE",
      "description": "Rewrite stream parser"
    },
    {
      "task_id": 2,
      "file": "internal/parser/types.go",
      "strategy": "DIFF_PATCH",
      "description": "Add new stream types"
    }
  ]
}`
	result := ParseJSONPlan(input)
	if !result.Valid {
		t.Fatalf("expected valid JSON plan, got error: %s", result.Error)
	}
	if result.Plan == nil {
		t.Fatal("expected non-nil PlanOutput")
	}
	if result.Plan.ArchitecturalStrategy != "Isolate the parser logic to prevent streaming leakage" {
		t.Fatalf("unexpected strategy: %s", result.Plan.ArchitecturalStrategy)
	}
	if len(result.Tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(result.Tasks))
	}
	if result.Tasks[0].Target != "internal/parser/stream.go" {
		t.Fatalf("expected target internal/parser/stream.go, got %s", result.Tasks[0].Target)
	}
	if result.Tasks[0].Type != "FILE_MUTATE" {
		t.Fatalf("expected FILE_MUTATE type, got %s", result.Tasks[0].Type)
	}
	if result.Tasks[1].Target != "internal/parser/types.go" {
		t.Fatalf("expected second target internal/parser/types.go, got %s", result.Tasks[1].Target)
	}
}

func TestParseJSONPlan_InvalidJSON(t *testing.T) {
	input := `{invalid json}`
	result := ParseJSONPlan(input)
	if result.Valid {
		t.Fatal("expected invalid for malformed JSON")
	}
	if !strings.Contains(result.Error, "JSON parse error") {
		t.Fatalf("expected JSON parse error, got: %s", result.Error)
	}
}

func TestParseJSONPlan_EmptyTasks(t *testing.T) {
	input := `{
  "context_anchor": {
    "source": "test",
    "target_packages": []
  },
  "architectural_strategy": "test",
  "atomic_tasks": []
}`
	result := ParseJSONPlan(input)
	if result.Valid {
		t.Fatal("expected invalid for empty atomic_tasks")
	}
	if !strings.Contains(result.Error, "at least one") {
		t.Fatalf("expected 'at least one' error, got: %s", result.Error)
	}
}

func TestParseJSONPlan_MissingStrategy(t *testing.T) {
	input := `{
  "context_anchor": {
    "source": "test",
    "target_packages": []
  },
  "architectural_strategy": "",
  "atomic_tasks": [
    {
      "task_id": 1,
      "file": "test.go",
      "strategy": "ATOMIC_REPLACE",
      "description": "test"
    }
  ]
}`
	result := ParseJSONPlan(input)
	if result.Valid {
		t.Fatal("expected invalid for empty architectural_strategy")
	}
}

func TestParseJSONPlan_CodeFences(t *testing.T) {
	input := "```json\n{\n  \"context_anchor\": {\n    \"source\": \"user\",\n    \"target_packages\": [\"pkg\"]\n  },\n  \"architectural_strategy\": \"test strategy\",\n  \"atomic_tasks\": [\n    {\n      \"task_id\": 1,\n      \"file\": \"main.go\",\n      \"strategy\": \"ATOMIC_REPLACE\",\n      \"description\": \"replace main\"\n    }\n  ]\n}\n```"
	result := ParseJSONPlan(input)
	if !result.Valid {
		t.Fatalf("expected valid despite code fences, got: %s", result.Error)
	}
	if len(result.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(result.Tasks))
	}
}

func TestParseJSONPlan_UnknownStrategy(t *testing.T) {
	input := `{
  "context_anchor": {
    "source": "test",
    "target_packages": []
  },
  "architectural_strategy": "test",
  "atomic_tasks": [
    {
      "task_id": 1,
      "file": "test.go",
      "strategy": "SHELL_EXEC",
      "description": "run test"
    }
  ]
}`
	result := ParseJSONPlan(input)
	if !result.Valid {
		t.Fatalf("expected valid for SHELL_EXEC strategy, got: %s", result.Error)
	}
	if result.Tasks[0].Type != "SHELL_EXEC" {
		t.Fatalf("expected SHELL_EXEC type, got %s", result.Tasks[0].Type)
	}
}

func TestParseJSONPlan_MissingDescription(t *testing.T) {
	input := `{
  "context_anchor": {
    "source": "test",
    "target_packages": []
  },
  "architectural_strategy": "test",
  "atomic_tasks": [
    {
      "task_id": 1,
      "file": "test.go",
      "strategy": "ATOMIC_REPLACE",
      "description": ""
    }
  ]
}`
	result := ParseJSONPlan(input)
	if !result.Valid {
		t.Fatalf("expected valid with empty description, got: %s", result.Error)
	}
	if !strings.Contains(result.Tasks[0].Description, "ATOMIC_REPLACE") {
		t.Fatalf("expected fallback description, got: %s", result.Tasks[0].Description)
	}
}

func TestParseJSONPlan_StripCodeFences(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"```json\n{\"a\":1}\n```", "{\"a\":1}"},
		{"```\n{\"a\":1}\n```", "{\"a\":1}"},
		{"{\"a\":1}", "{\"a\":1}"},
	}
	for _, tt := range tests {
		got := stripCodeFences(tt.input)
		if got != tt.want {
			t.Errorf("stripCodeFences(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestMapStrategyToType(t *testing.T) {
	tests := []struct {
		strategy string
		want     string
	}{
		{"ATOMIC_REPLACE", "FILE_MUTATE"},
		{"DIFF_PATCH", "FILE_MUTATE"},
		{"SHELL_EXEC", "SHELL_EXEC"},
		{"GIT_ACTION", "GIT_ACTION"},
		{"unknown", "FILE_MUTATE"},
		{"", "FILE_MUTATE"},
	}
	for _, tt := range tests {
		got := mapStrategyToType(tt.strategy)
		if got != tt.want {
			t.Errorf("mapStrategyToType(%q) = %q, want %q", tt.strategy, got, tt.want)
		}
	}
}

func TestSchemaJSONInstruction(t *testing.T) {
	inst := SchemaJSONInstruction()
	if !strings.Contains(inst, "context_anchor") {
		t.Fatal("expected context_anchor in JSON schema instruction")
	}
	if !strings.Contains(inst, "ATOMIC_REPLACE") {
		t.Fatal("expected ATOMIC_REPLACE in JSON schema instruction")
	}
	if !strings.Contains(inst, "atomic_tasks") {
		t.Fatal("expected atomic_tasks in JSON schema instruction")
	}
}

func TestParsePlanContent_JSONPreferred(t *testing.T) {
	input := `{"context_anchor":{"source":"user","target_packages":["pkg"]},"architectural_strategy":"test","atomic_tasks":[{"task_id":1,"file":"a.go","strategy":"ATOMIC_REPLACE","description":"test"}]}`
	tasks := parsePlanContent(input)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task from JSON, got %d", len(tasks))
	}
	if tasks[0].Target != "a.go" {
		t.Fatalf("expected target a.go, got %s", tasks[0].Target)
	}
}

func TestParsePlanContent_MarkdownFallback(t *testing.T) {
	input := "- [ ] FILE_MUTATE: fallback.go | fallback test"
	tasks := parsePlanContent(input)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task from markdown fallback, got %d", len(tasks))
	}
	if tasks[0].Target != "fallback.go" {
		t.Fatalf("expected target fallback.go, got %s", tasks[0].Target)
	}
}

func TestParsePlanContent_Empty(t *testing.T) {
	tasks := parsePlanContent("")
	if tasks != nil {
		t.Fatal("expected nil for empty content")
	}
}

func TestPlanSchemaError(t *testing.T) {
	err := &PlanSchemaError{Message: "test violation"}
	if !strings.Contains(err.Error(), "schema violation") {
		t.Fatalf("expected 'schema violation' in error, got: %s", err.Error())
	}
}
