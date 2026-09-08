package control

import (
	"errors"
	"testing"
)

func TestValidateStagedPlan_AllAllowed(t *testing.T) {
	tasks := []TaskTarget{
		{Target: "index.html", Type: "FILE_MUTATE"},
		{Target: "style.css", Type: "FILE_MUTATE"},
	}
	allowed := []string{"index.html", "style.css", "app.js"}
	err := ValidateStagedPlan(tasks, allowed)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateStagedPlan_HallucinatedFile(t *testing.T) {
	tasks := []TaskTarget{
		{Target: "index.html", Type: "FILE_MUTATE"},
		{Target: "frontend/components/navigation_bar.go", Type: "FILE_MUTATE"},
	}
	allowed := []string{"index.html", "style.css"}
	err := ValidateStagedPlan(tasks, allowed)
	if err == nil {
		t.Fatal("expected ScopeViolationError for hallucinated file path")
	}
	var sv *ScopeViolationError
	if !errors.As(err, &sv) {
		t.Fatalf("expected *ScopeViolationError, got %T", err)
	}
	if sv.Target != "frontend/components/navigation_bar.go" {
		t.Fatalf("expected target 'frontend/components/navigation_bar.go', got %q", sv.Target)
	}
}

func TestValidateStagedPlan_ShellExecSkipped(t *testing.T) {
	tasks := []TaskTarget{
		{Target: "go mod tidy", Type: "SHELL_EXEC"},
		{Target: "go get github.com/foo/bar", Type: "SHELL_EXEC"},
	}
	allowed := []string{"index.html", "style.css"}
	err := ValidateStagedPlan(tasks, allowed)
	if err != nil {
		t.Fatalf("expected SHELL_EXEC tasks to be skipped, got: %v", err)
	}
}

func TestValidateStagedPlan_GoModAllowed(t *testing.T) {
	tasks := []TaskTarget{
		{Target: "go.mod", Type: "FILE_MUTATE"},
		{Target: "go.sum", Type: "FILE_MUTATE"},
	}
	allowed := []string{"index.html", "style.css"}
	err := ValidateStagedPlan(tasks, allowed)
	if err != nil {
		t.Fatalf("expected go.mod/go.sum to be implicitly allowed, got: %v", err)
	}
}

func TestValidateStagedPlan_EmptyTasks(t *testing.T) {
	err := ValidateStagedPlan(nil, []string{"index.html"})
	if err != nil {
		t.Fatalf("expected no error for nil tasks, got: %v", err)
	}
	err = ValidateStagedPlan([]TaskTarget{}, []string{"index.html"})
	if err != nil {
		t.Fatalf("expected no error for empty tasks, got: %v", err)
	}
}

func TestValidateStagedPlan_EmptyAllowed(t *testing.T) {
	tasks := []TaskTarget{
		{Target: "index.html", Type: "FILE_MUTATE"},
	}
	err := ValidateStagedPlan(tasks, nil)
	if err == nil {
		t.Fatal("expected error when allowed files is empty and target is not go.mod/go.sum")
	}
	err = ValidateStagedPlan(tasks, []string{})
	if err == nil {
		t.Fatal("expected error when allowed files is empty and target is not go.mod/go.sum")
	}
}

func TestValidateStagedPlan_EmptyAllowedWithGoMod(t *testing.T) {
	tasks := []TaskTarget{
		{Target: "go.mod", Type: "FILE_MUTATE"},
	}
	err := ValidateStagedPlan(tasks, nil)
	if err != nil {
		t.Fatalf("expected go.mod to be allowed even with empty allowed list, got: %v", err)
	}
}

func TestValidateStagedPlan_ATOMIC_REPLACE(t *testing.T) {
	tasks := []TaskTarget{
		{Target: "index.html", Type: "ATOMIC_REPLACE"},
		{Target: "style.css", Type: "ATOMIC_REPLACE"},
	}
	allowed := []string{"index.html", "style.css"}
	err := ValidateStagedPlan(tasks, allowed)
	if err != nil {
		t.Fatalf("expected no error for ATOMIC_REPLACE with allowed targets, got: %v", err)
	}
}

func TestValidateStagedPlan_ATOMIC_REPLACE_Hallucinated(t *testing.T) {
	tasks := []TaskTarget{
		{Target: "navigation_bar.tsx", Type: "ATOMIC_REPLACE"},
	}
	allowed := []string{"index.html", "style.css"}
	err := ValidateStagedPlan(tasks, allowed)
	if err == nil {
		t.Fatal("expected error for ATOMIC_REPLACE with hallucinated target")
	}
}

func TestValidateStagedPlan_EmptyTarget(t *testing.T) {
	tasks := []TaskTarget{
		{Target: "", Type: "FILE_MUTATE"},
	}
	allowed := []string{"index.html"}
	err := ValidateStagedPlan(tasks, allowed)
	if err != nil {
		t.Fatalf("expected empty target to be skipped, got: %v", err)
	}
}

func TestScopeViolationError_Message(t *testing.T) {
	err := &ScopeViolationError{
		Target:       "fake.go",
		AllowedFiles: []string{"real.go"},
	}
	msg := err.Error()
	if !stringsContains(msg, "SCOPE_VIOLATION") {
		t.Fatalf("expected error message to contain SCOPE_VIOLATION, got: %s", msg)
	}
	if !stringsContains(msg, "fake.go") {
		t.Fatalf("expected error message to contain target, got: %s", msg)
	}
}

func stringsContains(s, substr string) bool {
	return len(s) >= len(substr) && contains(s, substr)
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
