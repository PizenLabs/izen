package plan

import (
	"testing"
)

func TestParseFlatPlanSpec_Basic(t *testing.T) {
	input := `{"target": "LICENSE", "action": "REPLACE", "template_key": "apache-2.0"}`
	spec, err := ParseFlatPlanSpec(input)
	if err != nil {
		t.Fatalf("ParseFlatPlanSpec error: %v", err)
	}
	if spec == nil {
		t.Fatal("ParseFlatPlanSpec returned nil")
	}
	if spec.Target != "LICENSE" {
		t.Errorf("Target = %q, want LICENSE", spec.Target)
	}
	if spec.Action != "REPLACE" {
		t.Errorf("Action = %q, want REPLACE", spec.Action)
	}
	if spec.TemplateKey != "apache-2.0" {
		t.Errorf("TemplateKey = %q, want apache-2.0", spec.TemplateKey)
	}
}

func TestParseFlatPlanSpec_CodeFence(t *testing.T) {
	input := "```json\n{\"target\": \"LICENSE\", \"action\": \"REPLACE\", \"template_key\": \"apache-2.0\"}\n```"
	spec, err := ParseFlatPlanSpec(input)
	if err != nil {
		t.Fatalf("ParseFlatPlanSpec error: %v", err)
	}
	if spec == nil {
		t.Fatal("ParseFlatPlanSpec returned nil for code fence input")
	}
	if spec.Target != "LICENSE" {
		t.Errorf("Target = %q, want LICENSE", spec.Target)
	}
}

func TestParseFlatPlanSpec_Empty(t *testing.T) {
	spec, err := ParseFlatPlanSpec("")
	if err != nil {
		t.Fatalf("ParseFlatPlanSpec error: %v", err)
	}
	if spec != nil {
		t.Error("ParseFlatPlanSpec(\"\") should return nil")
	}
}

func TestParseFlatPlanSpec_MissingTarget(t *testing.T) {
	input := `{"action": "REPLACE", "template_key": "apache-2.0"}`
	spec, err := ParseFlatPlanSpec(input)
	if err != nil {
		t.Fatalf("ParseFlatPlanSpec error: %v", err)
	}
	if spec != nil {
		t.Error("ParseFlatPlanSpec with missing target should return nil")
	}
}

func TestFlatSpecToTasks_Basic(t *testing.T) {
	spec := &FlatPlanSpec{
		Target:      "LICENSE",
		Action:      "REPLACE",
		TemplateKey: "apache-2.0",
	}
	tasks := FlatSpecToTasks(spec)
	if len(tasks) != 1 {
		t.Fatalf("FlatSpecToTasks returned %d tasks, want 1", len(tasks))
	}
	if tasks[0].Type != "FILE_MUTATE" {
		t.Errorf("Type = %q, want FILE_MUTATE", tasks[0].Type)
	}
	if tasks[0].Target != "LICENSE" {
		t.Errorf("Target = %q, want LICENSE", tasks[0].Target)
	}
	if !tasks[0].IsHardcoded {
		t.Error("IsHardcoded = false, want true")
	}
}

func TestFlatSpecToTasks_ShellExec(t *testing.T) {
	spec := &FlatPlanSpec{
		Target: "go mod tidy",
		Action: "SHELL_EXEC",
	}
	tasks := FlatSpecToTasks(spec)
	if len(tasks) != 1 {
		t.Fatalf("FlatSpecToTasks returned %d tasks, want 1", len(tasks))
	}
	if tasks[0].Type != "SHELL_EXEC" {
		t.Errorf("Type = %q, want SHELL_EXEC", tasks[0].Type)
	}
	if tasks[0].Target != "go mod tidy" {
		t.Errorf("Target = %q, want go mod tidy", tasks[0].Target)
	}
}

func TestFlatSpecToTasks_Nil(t *testing.T) {
	tasks := FlatSpecToTasks(nil)
	if tasks != nil {
		t.Errorf("FlatSpecToTasks(nil) should return nil, got %v", tasks)
	}
}

func TestFlatSpecToTasks_EmptyTarget(t *testing.T) {
	spec := &FlatPlanSpec{
		Action: "REPLACE",
	}
	tasks := FlatSpecToTasks(spec)
	if tasks != nil {
		t.Errorf("FlatSpecToTasks with empty target should return nil, got %v", tasks)
	}
}
