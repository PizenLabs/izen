package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/domain/task"
	"github.com/PizenLabs/izen/internal/prompt"
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
			if got := IsDocumentationTarget(tt.target, task.TaskType(tt.taskTyp)); got != tt.want {
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

func TestFilterNonExistentMutationTargets(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<div>ok</div>"), 0644); err != nil {
		t.Fatal(err)
	}
	// script.js is the classic hallucinated speculative asset — absent on disk.
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		root  string
		tasks []Task
		want  []string
	}{
		{
			name: "drops non-existent speculative asset",
			root: root,
			tasks: []Task{
				{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Description: "fix duplicate"},
				{StepNum: 2, Type: "FILE_MUTATE", Target: "script.js", Description: "wire handler"},
			},
			want: []string{"index.html"},
		},
		{
			name: "keeps shell and git tasks",
			root: root,
			tasks: []Task{
				{StepNum: 1, Type: "SHELL_EXEC", Target: "go test ./...", Description: "verify"},
				{StepNum: 2, Type: "GIT_ACTION", Target: "commit -m fix", Description: "commit"},
			},
			want: []string{"go test ./...", "commit -m fix"},
		},
		{
			name: "drops FILE_EDIT on missing file too",
			root: root,
			tasks: []Task{
				{StepNum: 1, Type: "FILE_EDIT", Target: "styles.css", Description: "fix style"},
			},
			want: nil,
		},
		{
			name: "preserves hardcoded tasks",
			root: root,
			tasks: []Task{
				{StepNum: 1, Type: "FILE_MUTATE", Target: "not-there.go", Description: "hardcoded fix", IsHardcoded: true},
			},
			want: []string{"not-there.go"},
		},
		{
			name: "directory target dropped",
			root: root,
			tasks: []Task{
				{StepNum: 1, Type: "FILE_MUTATE", Target: "docs", Description: "patch dir"},
			},
			want: nil,
		},
		{
			name:  "empty root passes tasks through",
			root:  "",
			tasks: []Task{{StepNum: 1, Type: "FILE_MUTATE", Target: "anything.js", Description: "x"}},
			want:  []string{"anything.js"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := FilterNonExistentMutationTargets(tc.tasks, tc.root)
			var got []string
			for _, tk := range out {
				got = append(got, tk.Target)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d tasks %v, want %v", len(got), got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestFilterNonExistentMutationTargets_EmptyRootKeepsAll(t *testing.T) {
	tasks := []Task{
		{StepNum: 1, Type: "FILE_MUTATE", Target: "ghost.js", Description: "x"},
		{StepNum: 2, Type: "FILE_MUTATE", Target: "real.html", Description: "y"},
	}
	out := FilterNonExistentMutationTargets(tasks, "")
	if len(out) != 2 {
		t.Fatalf("expected both tasks preserved when root is empty, got %d", len(out))
	}
}

func TestEvidenceBasedPlanningDirectivePresentInPrompts(t *testing.T) {
	d := prompt.EvidenceBasedPlanningDirective()
	if d == "" {
		t.Fatal("expected non-empty evidence directive")
	}
	for _, want := range []string{"EVIDENCE-BASED", "FILE_MUTATE", "script.js", "styles.css"} {
		if !strings.Contains(d, want) {
			t.Fatalf("evidence directive missing %q:\n%s", want, d)
		}
	}

	jsonPrompt := prompt.BuildPlanJSONPrompt("problem", "ledger", "", false, "")
	if !strings.Contains(jsonPrompt, "EVIDENCE-BASED PLANNING") {
		t.Fatal("BuildPlanJSONPrompt missing evidence directive")
	}
	mdPrompt := prompt.BuildPlanPrompt("objective", "context", false, "")
	if !strings.Contains(mdPrompt, "EVIDENCE-BASED PLANNING") {
		t.Fatal("BuildPlanPrompt missing evidence directive")
	}
}
