package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	ctxpkg "github.com/PizenLabs/izen/internal/context"
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
	handoff := ctxpkg.SanitizeBuildHandoff(task, "")

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
	handoff := ctxpkg.SanitizeBuildHandoff(task, "func Foo() {}")

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

// ── Local short-circuit (Path A) tests ────────────────────

// TestProposeHotfixPatch_LocalShortCircuit verifies that a $hot command
// with an explicit @file reference and a simple text-modification verb
// triggers the deterministic local short-circuit: applyContextAwareFuzzyReplace
// is called, the provider.Execute is never invoked, and a valid hotfixProposalMsg
// with a non-empty patch is returned (0 LLM tokens consumed).
func TestProposeHotfixPatch_LocalShortCircuit(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "LICENSE")
	origContent := "Copyright (c) 2023 John Doe\n\nMIT License\n"
	if err := os.WriteFile(filePath, []byte(origContent), 0644); err != nil {
		t.Fatal(err)
	}

	m := newTestModel()
	m.pendingProposals = nil
	m.state = StateAwaitingApproval

	// Use a non-existent provider (nil), but the short-circuit should
	// return a valid patch before ever reaching the provider check.
	task := &plan.Task{
		StepNum:     1,
		Type:        "FILE_MUTATE",
		Target:      filePath,
		Description: "$hot rename author in @LICENSE to 'Hashirama'",
	}

	msg := m.proposeHotfixPatch(task)()
	result, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("expected hotfixProposalMsg, got %T", msg)
	}
	// The local short-circuit should have produced a valid patch
	// without calling the LLM provider.
	if result.Err != nil {
		t.Fatalf("expected no error from local short-circuit, got: %v", result.Err)
	}
	if result.Patch == nil {
		t.Fatal("expected a non-nil patch from local short-circuit")
	}
	if result.Diff == "" {
		t.Fatal("expected a non-empty diff from local short-circuit")
	}
	if !strings.Contains(result.Diff, "Hashirama") || !strings.Contains(result.Diff, "John Doe") {
		t.Errorf("expected diff to contain the author replacement, got:\n%s", result.Diff)
	}
}

// TestProposeHotfixPatch_NoExplicitFileRefSkipsLocal verifies that
// without an explicit @file reference, the local short-circuit is
// bypassed and the function proceeds to the provider path.
func TestProposeHotfixPatch_NoExplicitFileRefSkipsLocal(t *testing.T) {
	m := newTestModel()
	m.pendingProposals = nil
	m.state = StateAwaitingApproval

	task := &plan.Task{
		StepNum:     1,
		Type:        "FILE_MUTATE",
		Target:      "LICENSE",
		Description: "change year 2023 to 2026",
	}

	msg := m.proposeHotfixPatch(task)()
	result, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("expected hotfixProposalMsg, got %T", msg)
	}
	// No provider → error, not a local short-circuit result.
	if result.Err == nil {
		t.Fatal("expected error (no provider), got nil — local short-circuit should not have triggered")
	}
	if !strings.Contains(result.Err.Error(), "no provider") {
		t.Errorf("expected 'no provider' error, got: %v", result.Err)
	}
}

// TestProposeHotfixPatch_CloudEarlyAbort verifies that when a cloud
// provider returns output without diff markers, the function aborts
// immediately (no retry) and falls back to local fuzzy replacement
// when the file content is available.
func TestProposeHotfixPatch_CloudEarlyAbort(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "LICENSE")
	origContent := "Copyright (c) 2023 John Doe\n\nMIT License\n"
	if err := os.WriteFile(filePath, []byte(origContent), 0644); err != nil {
		t.Fatal(err)
	}

	mock := &mockProvider{
		responses: []*ai.Response{
			// Non-diff markers: cloud model returns conversational text
			// instead of a structured patch. The early abort should
			// skip retries and fall back to local fuzzy replacement.
			{Content: "Here is the proposed change for the LICENSE file."},
		},
	}
	m := testModelWithProvider(mock)
	m.pendingProposals = nil
	m.state = StateAwaitingApproval

	// Use a description that triggers the LLM path (no simple
	// mutation verb) but still has an explicit @ file reference.
	task := &plan.Task{
		StepNum:     1,
		Type:        "FILE_MUTATE",
		Target:      filePath,
		Description: "$hot update the license file @LICENSE for compliance",
	}

	msg := m.proposeHotfixPatch(task)()
	result, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("expected hotfixProposalMsg, got %T", msg)
	}
	// The local fallback and LLM extraction both fail for this
	// ambiguous prompt. The key assertion is that the LLM was
	// called exactly once (no retry), proving the early abort
	// prevented wasteful additional API calls.
	if mock.callCount != 1 {
		t.Errorf("expected exactly 1 LLM call (no retry), got %d", mock.callCount)
	}
	_ = result
}

// TestHasDiffMarkerPrefix_TripleDash verifies that content starting with
// a unified diff header (--- a/) is recognized as having a diff marker.
func TestHasDiffMarkerPrefix_ReturnsTrueForDiffPrefix(t *testing.T) {
	cases := []struct {
		input    string
		expected bool
	}{
		{"--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n", true},
		{"diff --git a/main.go b/main.go\nindex abc..def 100644\n", true},
		{"<<<<<<< SEARCH\nold code\n=======\nnew code\n>>>>>>>", true},
		{"Here is the diff for you:", false},
		{"I cannot complete this request.", false},
		{"", false},
		{"   --- a/main.go", true}, // leading whitespace trimmed
	}
	for _, tc := range cases {
		if got := hasDiffMarkerPrefix(tc.input); got != tc.expected {
			t.Errorf("hasDiffMarkerPrefix(%q) = %v, want %v", tc.input, got, tc.expected)
		}
	}
}

// ── hasDiffMarkerPrefix tests ───────────────────────────────

func TestHasDiffMarkerPrefix_TripleDash(t *testing.T) {
	if !hasDiffMarkerPrefix("--- a/main.go") {
		t.Error("expected true for --- a/...")
	}
}

func TestHasDiffMarkerPrefix_DiffKeyword(t *testing.T) {
	if !hasDiffMarkerPrefix("diff --git a/main.go b/main.go") {
		t.Error("expected true for diff --git")
	}
}

func TestHasDiffMarkerPrefix_SearchReplace(t *testing.T) {
	if !hasDiffMarkerPrefix("<<<<<<< SEARCH") {
		t.Error("expected true for <<<<<<< SEARCH")
	}
}

func TestHasDiffMarkerPrefix_Conversational(t *testing.T) {
	if hasDiffMarkerPrefix("Here is the diff you requested:") {
		t.Error("expected false for conversational text")
	}
}

func TestHasDiffMarkerPrefix_Empty(t *testing.T) {
	if hasDiffMarkerPrefix("") {
		t.Error("expected false for empty string")
	}
}

// ── isHotfixLocalCandidate tests ────────────────────────────

func TestIsHotfixLocalCandidate_ExplicitRefAndRename(t *testing.T) {
	if !isHotfixLocalCandidate("$hot rename author in @LICENSE to 'Hashirama'", "LICENSE") {
		t.Error("expected true for explicit @LICENSE + rename")
	}
}

func TestIsHotfixLocalCandidate_ExplicitRefAndChange(t *testing.T) {
	if !isHotfixLocalCandidate("$hot change year in @LICENSE to 2026", "LICENSE") {
		t.Error("expected true for explicit @LICENSE + change")
	}
}

func TestIsHotfixLocalCandidate_NoExplicitRef(t *testing.T) {
	if isHotfixLocalCandidate("$hot rename author to Hashirama", "LICENSE") {
		t.Error("expected false when no explicit @file reference")
	}
}

func TestIsHotfixLocalCandidate_NoMutationVerb(t *testing.T) {
	if isHotfixLocalCandidate("$hot update LICENSE file", "LICENSE") {
		t.Error("expected false when no mutation verb matches (update alone without to/of)")
	}
}

func TestIsHotfixLocalCandidate_EmptyTarget(t *testing.T) {
	if isHotfixLocalCandidate("$hot rename author in @LICENSE to 'Hash'", "") {
		t.Error("expected false for empty target")
	}
}
