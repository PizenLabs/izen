package plan

import (
	"strings"
	"testing"
)

func TestIsDocumentationTarget(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		taskTyp string
		want    bool
	}{
		{"readme file mutate", "README.md", "FILE_MUTATE", true},
		{"readme lower", "docs/readme.md", "FILE_MUTATE", true},
		{"contributing", "CONTRIBUTING.md", "FILE_MUTATE", true},
		{"changelog", "CHANGELOG", "FILE_MUTATE", true},
		{"security", "SECURITY.md", "FILE_MUTATE", true},
		{"license", "LICENSE", "FILE_MUTATE", true},
		{"code file", "internal/foo.go", "FILE_MUTATE", false},
		{"go.mod", "go.mod", "FILE_MUTATE", false},
		{"shell redirect to readme", "echo x > README.md", "SHELL_EXEC", true},
		{"shell go get", "go get github.com/foo/bar", "SHELL_EXEC", false},
		{"shell go mod tidy", "go mod tidy", "SHELL_EXEC", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsDocumentationTarget(tt.target, tt.taskTyp); got != tt.want {
				t.Fatalf("IsDocumentationTarget(%q, %q) = %v, want %v", tt.target, tt.taskTyp, got, tt.want)
			}
		})
	}
}

func TestParseMarkdownToTasks_DropsDocumentation(t *testing.T) {
	md := "- [ ] FILE_MUTATE: README.md | fix build docs\n" +
		"- [ ] SHELL_EXEC: go mod tidy | resolve deps\n" +
		"- [ ] FILE_MUTATE: internal/foo.go | add handler"
	tasks := ParseMarkdownToTasks(md)
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks (README dropped), got %d: %+v", len(tasks), tasks)
	}
	for _, tk := range tasks {
		if IsDocumentationTarget(tk.Target, tk.Type) {
			t.Fatalf("documentation task leaked past parser: %+v", tk)
		}
	}
}

func TestForceShellExecOnCompileError_ForcesShell(t *testing.T) {
	// Simulated compile/dep blocker with no shell task → must prepend SHELL_EXEC.
	ledger := "cmd/api/main.go:7:5: no required module provides package github.com/moby/moby/client"
	tasks := []Task{
		{StepNum: 1, Type: "FILE_MUTATE", Target: "internal/foo.go", Description: "patch"},
	}
	out := ForceShellExecOnCompileError(tasks, ledger, ledger)
	if len(out) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(out))
	}
	if out[0].Type != "SHELL_EXEC" {
		t.Fatalf("expected first task SHELL_EXEC, got %s", out[0].Type)
	}
	// No conclusion packet present → deterministic `go mod tidy` fallback.
	if out[0].Target != "go mod tidy" {
		t.Fatalf("expected go mod tidy fallback, got %q", out[0].Target)
	}
	if out[0].StepNum != 1 || out[1].StepNum != 2 {
		t.Fatalf("step numbers not renumbered: %+v", out)
	}
}

func TestForceShellExecOnCompileError_UsesConclusionDep(t *testing.T) {
	ledger := "[PKT-3] kind=conclusion title=\"Investigation conclusion\"\n" +
		"use github.com/moby/moby/client instead of the stale path\n" +
		"cmd/api/main.go:7:5: no required module provides package github.com/moby/moby/client"
	tasks := []Task{
		{StepNum: 1, Type: "FILE_MUTATE", Target: "internal/foo.go", Description: "patch"},
	}
	out := ForceShellExecOnCompileError(tasks, ledger, ledger)
	if len(out) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(out))
	}
	if out[0].Type != "SHELL_EXEC" {
		t.Fatalf("expected first task SHELL_EXEC, got %s", out[0].Type)
	}
	if !strings.Contains(out[0].Target, "go get github.com/moby/moby/client") {
		t.Fatalf("expected corrected dep from conclusion in shell target, got %q", out[0].Target)
	}
}

func TestForceShellExecOnCompileError_KeepsExistingShell(t *testing.T) {
	ledger := "build failed: undefined: Router"
	tasks := []Task{
		{StepNum: 1, Type: "SHELL_EXEC", Target: "go get github.com/foo/bar", Description: "dep"},
		{StepNum: 2, Type: "FILE_MUTATE", Target: "internal/foo.go", Description: "patch"},
	}
	out := ForceShellExecOnCompileError(tasks, ledger, ledger)
	if len(out) != 2 {
		t.Fatalf("expected 2 tasks unchanged, got %d", len(out))
	}
	if out[0].Target != "go get github.com/foo/bar" {
		t.Fatalf("existing shell task should be preserved, got %q", out[0].Target)
	}
}

func TestForceShellExecOnCompileError_NonCompile(t *testing.T) {
	ledger := "feature request: add dark mode"
	tasks := []Task{
		{StepNum: 1, Type: "FILE_MUTATE", Target: "internal/foo.go", Description: "patch"},
	}
	out := ForceShellExecOnCompileError(tasks, ledger, ledger)
	if len(out) != 1 {
		t.Fatalf("expected 1 task unchanged for non-compile error, got %d", len(out))
	}
}

func TestFilterUndefinedSymbolShellExec_DropsShellExec(t *testing.T) {
	ledger := "cmd/api/main.go:24:2: undefined: Log"
	tasks := []Task{
		{StepNum: 1, Type: "SHELL_EXEC", Target: "go mod tidy", Description: "tidy"},
		{StepNum: 2, Type: "FILE_MUTATE", Target: "cmd/api/main.go", Description: "fix"},
	}
	out := FilterUndefinedSymbolShellExec(tasks, ledger)
	if len(out) != 1 {
		t.Fatalf("expected 1 task (SHELL_EXEC dropped), got %d", len(out))
	}
	if out[0].Type != "FILE_MUTATE" {
		t.Fatalf("expected remaining task to be FILE_MUTATE, got %s", out[0].Type)
	}
	if out[0].Target != "cmd/api/main.go" {
		t.Fatalf("expected target cmd/api/main.go, got %s", out[0].Target)
	}
}

func TestFilterUndefinedSymbolShellExec_DropsGitAction(t *testing.T) {
	ledger := "cmd/api/main.go:24:2: undefined: Log"
	tasks := []Task{
		{StepNum: 1, Type: "GIT_ACTION", Target: "commit -m fix", Description: "commit"},
		{StepNum: 2, Type: "FILE_MUTATE", Target: "cmd/api/main.go", Description: "fix"},
	}
	out := FilterUndefinedSymbolShellExec(tasks, ledger)
	if len(out) != 1 {
		t.Fatalf("expected 1 task (GIT_ACTION dropped), got %d", len(out))
	}
	if out[0].Type != "FILE_MUTATE" {
		t.Fatalf("expected remaining task to be FILE_MUTATE, got %s", out[0].Type)
	}
}

func TestFilterUndefinedSymbolShellExec_PreservesHardcoded(t *testing.T) {
	ledger := "cmd/api/main.go:24:2: undefined: Log"
	tasks := []Task{
		{StepNum: 1, Type: "SHELL_EXEC", Target: "go mod tidy", Description: "tidy", IsHardcoded: true},
		{StepNum: 2, Type: "FILE_MUTATE", Target: "cmd/api/main.go", Description: "fix"},
	}
	out := FilterUndefinedSymbolShellExec(tasks, ledger)
	if len(out) != 2 {
		t.Fatalf("expected 2 tasks (hardcoded preserved), got %d", len(out))
	}
	if out[0].Type != "SHELL_EXEC" {
		t.Fatalf("expected hardcoded SHELL_EXEC preserved, got %s", out[0].Type)
	}
}

func TestFilterUndefinedSymbolShellExec_NoUndefinedSymbol(t *testing.T) {
	ledger := "no required module provides package github.com/foo/bar"
	tasks := []Task{
		{StepNum: 1, Type: "SHELL_EXEC", Target: "go get github.com/foo/bar", Description: "get"},
		{StepNum: 2, Type: "FILE_MUTATE", Target: "main.go", Description: "fix"},
	}
	out := FilterUndefinedSymbolShellExec(tasks, ledger)
	if len(out) != 2 {
		t.Fatalf("expected 2 tasks unchanged (no undefined symbol), got %d", len(out))
	}
}

func TestFilterUndefinedSymbolShellExec_AllShellExec(t *testing.T) {
	ledger := "cmd/api/main.go:24:2: undefined: Log"
	tasks := []Task{
		{StepNum: 1, Type: "SHELL_EXEC", Target: "go mod tidy", Description: "tidy"},
		{StepNum: 2, Type: "SHELL_EXEC", Target: "go get github.com/foo/bar", Description: "get"},
	}
	out := FilterUndefinedSymbolShellExec(tasks, ledger)
	if len(out) != 0 {
		t.Fatalf("expected 0 tasks (all SHELL_EXEC dropped), got %d", len(out))
	}
}

func TestFilterUndefinedSymbolShellExec_EmptyInput(t *testing.T) {
	if out := FilterUndefinedSymbolShellExec(nil, ""); out != nil {
		t.Fatalf("expected nil for nil input, got %d", len(out))
	}
	if out := FilterUndefinedSymbolShellExec(nil, "cmd/api/main.go:24:2: undefined: Log"); out != nil {
		t.Fatalf("expected nil for nil tasks, got %d", len(out))
	}
	if out := FilterUndefinedSymbolShellExec([]Task{}, ""); len(out) != 0 {
		t.Fatalf("expected empty for empty input, got %d", len(out))
	}
}
