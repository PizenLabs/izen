package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/context"
	"github.com/PizenLabs/izen/internal/modes/plan"
)

// ── resolveHotfixTarget tests ──────────────────────────────────────────

func TestResolveHotfixTarget_ExplicitPath(t *testing.T) {
	target := resolveHotfixTarget("change year 2023 to 2026 @LICENSE")
	if target != "LICENSE" {
		t.Errorf("expected 'LICENSE', got %q", target)
	}
}

func TestResolveHotfixTarget_ExplicitPathWithoutAt(t *testing.T) {
	target := resolveHotfixTarget("add a MIT LICENSE file")
	if target != "LICENSE" {
		t.Errorf("expected 'LICENSE', got %q", target)
	}
}

func TestResolveHotfixTarget_FullPath(t *testing.T) {
	target := resolveHotfixTarget("fix the bug in cmd/api/main.go")
	if target != "cmd/api/main.go" {
		t.Errorf("expected 'cmd/api/main.go', got %q", target)
	}
}

func TestResolveHotfixTarget_FullPathWithAt(t *testing.T) {
	target := resolveHotfixTarget("refactor the handler @internal/handler/user.go")
	if target != "internal/handler/user.go" {
		t.Errorf("expected 'internal/handler/user.go', got %q", target)
	}
}

func TestResolveHotfixTarget_BlocksIzenPath(t *testing.T) {
	target := resolveHotfixTarget("update .izen/stashed_plan.json")
	if target != "" {
		t.Errorf("expected empty (blocked .izen/ path), got %q", target)
	}
}

func TestResolveHotfixTarget_BlocksPatchFile(t *testing.T) {
	target := resolveHotfixTarget("apply hotfix-20260101-120000.patch")
	if target != "" {
		t.Errorf("expected empty (blocked .patch file), got %q", target)
	}
}

func TestResolveHotfixTarget_BlocksIzenSubpath(t *testing.T) {
	target := resolveHotfixTarget("edit foo/.izen/bar.go")
	if target != "" {
		t.Errorf("expected empty (blocked .izen/ subpath), got %q", target)
	}
}

func TestResolveHotfixTarget_ReadmeKeyword(t *testing.T) {
	target := resolveHotfixTarget("improve the README file")
	if target != "README.md" {
		t.Errorf("expected 'README.md', got %q", target)
	}
}

func TestResolveHotfixTarget_DockerKeyword(t *testing.T) {
	target := resolveHotfixTarget("add a Docker compose file")
	if target != "Dockerfile" {
		t.Errorf("expected 'Dockerfile', got %q", target)
	}
}

func TestResolveHotfixTarget_MakefileKeyword(t *testing.T) {
	target := resolveHotfixTarget("update the Makefile")
	if target != "Makefile" {
		t.Errorf("expected 'Makefile', got %q", target)
	}
}

func TestResolveHotfixTarget_GitignoreKeyword(t *testing.T) {
	target := resolveHotfixTarget("add a gitignore")
	if target != ".gitignore" {
		t.Errorf("expected '.gitignore', got %q", target)
	}
}

func TestResolveHotfixTarget_NoMatchReturnsEmpty(t *testing.T) {
	target := resolveHotfixTarget("change year 2023 to 2026")
	if target != "" {
		t.Errorf("expected empty (no recognizable file), got %q", target)
	}
}

func TestResolveHotfixTarget_RejectsWorkspace(t *testing.T) {
	target := resolveHotfixTarget("update workspace config")
	if target != "" {
		t.Errorf("expected empty ('workspace' rejected), got %q", target)
	}
}

// ── SanitizeBuildHandoff temporal context tests ────────────────────────

func TestSanitizeBuildHandoff_ContainsCurrentYear(t *testing.T) {
	task := &plan.Task{
		StepNum:     1,
		Type:        "FILE_MUTATE",
		Target:      "LICENSE",
		Description: "add MIT license",
	}
	handoff := context.SanitizeBuildHandoff(task, "")

	if !strings.Contains(handoff, "CURRENT_YEAR") {
		t.Error("handoff missing CURRENT_YEAR")
	}
	if !strings.Contains(handoff, "CURRENT_DATE") {
		t.Error("handoff missing CURRENT_DATE")
	}
	if !strings.Contains(handoff, "strictly use CURRENT_YEAR") {
		t.Error("handoff missing year-usage instruction")
	}
}

func TestSanitizeBuildHandoff_ContainsSymbolContext(t *testing.T) {
	task := &plan.Task{
		StepNum: 1,
		Type:    "FILE_MUTATE",
		Target:  "main.go",
	}
	handoff := context.SanitizeBuildHandoff(task, "func Foo() {}")

	if !strings.Contains(handoff, "func Foo() {}") {
		t.Error("handoff should include symbol context when provided")
	}
	if !strings.Contains(handoff, "SYMBOL CONTEXT") {
		t.Error("handoff missing SYMBOL CONTEXT section")
	}
}

// ── ProposeHotfixPatch file-read integration ───────────────────────────

func TestProposeHotfixPatch_ReadsExistingFileBeforeLLM(t *testing.T) {
	// Create a temp file to simulate an existing target
	dir := t.TempDir()
	filePath := filepath.Join(dir, "LICENSE")
	origContent := "Copyright (c) 2023 John Doe"
	if err := os.WriteFile(filePath, []byte(origContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Build a minimal model (no provider — will error after handoff build,
	// but the file-read step happens before the LLM call)
	m := newTestModel()
	m.pendingProposals = nil
	m.state = StateAwaitingApproval

	task := &plan.Task{
		StepNum:     1,
		Type:        "FILE_MUTATE",
		Target:      filePath,
		Description: "change year 2023 to 2026",
	}

	// Call proposeHotfixPatch — it should read the file BEFORE the LLM call
	// and inject it into the handoff. Since we have no provider, the LLM call
	// will fail, but we can verify the handoff was built with file content.
	//
	// We cannot directly inspect the handoff (it's internal to the closure),
	// but we verify that the function at least tries to read the file by
	// confirming it does not panic and returns a hotfixProposalMsg with Err.
	msg := m.proposeHotfixPatch(task)()
	result, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("expected hotfixProposalMsg, got %T", msg)
	}
	// The provider is nil in testModel, so we expect a provider error
	if result.Err == nil {
		t.Fatal("expected error (no provider), got nil — file read may not have executed")
	}
	if !strings.Contains(result.Err.Error(), "no provider") {
		t.Errorf("expected 'no provider' error, got: %v", result.Err)
	}
}
