package plan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
)

// TestEnforceFrontendDomainIsolation is the strict domain-isolation unit guard
// for Module D: ENV_DEPS tasks and Go toolchain SHELL_EXEC tasks must never
// survive staging for a frontend workspace.
func TestEnforceFrontendDomainIsolation(t *testing.T) {
	tasks := []Task{
		{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html"},
		{StepNum: 2, Type: "ENV_DEPS", Target: "deps"},
		{StepNum: 3, Type: "SHELL_EXEC", Target: "go get github.com/foo/bar"},
		{StepNum: 4, Type: "SHELL_EXEC", Target: "go mod tidy"},
		{StepNum: 5, Type: "SHELL_EXEC", Target: "go test ./..."},
		{StepNum: 6, Type: "GIT_ACTION", Target: "go vet"},
		{StepNum: 7, Type: "SHELL_EXEC", Target: "npm run build"},
	}

	clean := EnforceFrontendDomainIsolation(tasks)
	for _, tk := range clean {
		if tk.Type == "ENV_DEPS" {
			t.Errorf("ENV_DEPS task survived isolation: %+v", tk)
		}
		if isGoDependencyTask(tk) {
			t.Errorf("Go dependency task survived isolation: %+v", tk)
		}
	}
	if len(clean) != 2 {
		t.Fatalf("isolated tasks = %d, want 2 (FILE_MUTATE + npm): %+v", len(clean), clean)
	}
	if clean[0].Target != "index.html" {
		t.Errorf("first surviving task = %q, want index.html", clean[0].Target)
	}
}

// TestFrontendDomainIsolation_VanillaWebStagesNoGoDeps (DoD Test 4) plans an
// HTML fix on a VANILLA_WEB workspace and asserts that zero ENV_DEPS / go get /
// go mod tasks are staged — even when the LLM hallucinates Go dependency tasks.
func TestFrontendDomainIsolation_VanillaWebStagesNoGoDeps(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"),
		[]byte("<!DOCTYPE html>\n<html><body>Hello</body></html>\n"), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}

	e := NewEngine(NewPlanStore())
	e.SetRootPath(root)
	e.SetProvider(func(ctx context.Context, req ai.Request) (*ai.Response, error) {
		// The LLM hallucinates a plan mixing a legitimate frontend file mutation
		// with Go dependency tasks (go get / go mod tidy / go test).
		return &ai.Response{
			Content: `{"context_anchor":{"source":"user","target_packages":[]},"architectural_strategy":"fix the header layout","atomic_tasks":[
				{"task_id":1,"file":"index.html","strategy":"FILE_MUTATE","description":"update header","rationale":"layout","solution":"header updated"},
				{"task_id":2,"file":"go get github.com/foo/bar","strategy":"SHELL_EXEC","description":"install dependency","rationale":"dep","solution":"dep installed"},
				{"task_id":3,"file":"go mod tidy","strategy":"SHELL_EXEC","description":"tidy module","rationale":"tidy","solution":"tidy"},
				{"task_id":4,"file":"go test ./...","strategy":"SHELL_EXEC","description":"run tests","rationale":"verify","solution":"tests pass"}
			]}`,
		}, nil
	})

	tasks, err := e.ProcessFromLedger(context.Background(), "", "Update the page header layout in index.html", "test-model")
	if err != nil {
		t.Fatalf("ProcessFromLedger: %v", err)
	}
	if len(tasks) == 0 {
		t.Fatal("expected at least the frontend FILE_MUTATE task to be staged")
	}

	for _, tk := range tasks {
		if tk.Type == "ENV_DEPS" {
			t.Errorf("staged ENV_DEPS task on VANILLA_WEB: %+v", tk)
		}
		target := strings.TrimSpace(tk.Target)
		if strings.HasPrefix(target, "go get") || strings.HasPrefix(target, "go mod") {
			t.Errorf("staged Go dependency task on VANILLA_WEB: %+v", tk)
		}
	}

	// The surviving plan must be pure frontend: only file mutations.
	for _, tk := range tasks {
		if tk.Type != "FILE_MUTATE" && tk.Type != "FILE_EDIT" {
			t.Errorf("non-frontend task staged on VANILLA_WEB: %+v", tk)
		}
	}
}
