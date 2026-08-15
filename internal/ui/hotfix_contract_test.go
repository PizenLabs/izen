package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes/plan"
)

// largeHotfixIndexHTML is a structurally complete HTML document exceeding the
// small-file boundary (100 newlines). The KEY architectural invariant under
// test: a $hot request against a LARGE file must NOT yield a full-file
// generation contract — neither in the prompt Izen sends nor in the artifact
// the changeset pipeline accepts.
func largeHotfixIndexHTML() string {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n")
	b.WriteString("<html lang=\"en\">\n")
	b.WriteString("<head>\n")
	b.WriteString("  <title>Large Landing</title>\n")
	b.WriteString("</head>\n")
	b.WriteString("<body>\n")
	b.WriteString("  <header>\n")
	b.WriteString("    <h3>Old Delta</h3>\n")
	b.WriteString("    <p>Stable content</p>\n")
	b.WriteString("  </header>\n")
	b.WriteString("  <main>\n")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "    <section id=\"sect-%02d\">\n      <p>Section %02d body text</p>\n    </section>\n", i, i)
	}
	b.WriteString("  </main>\n")
	b.WriteString("  <footer>\n")
	b.WriteString("    <p>Footer note</p>\n")
	b.WriteString("  </footer>\n")
	b.WriteString("</body>\n")
	b.WriteString("</html>\n")
	return b.String()
}

// writeHotfixFixture persists content as a temp file and returns its path.
func writeHotfixFixture(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestHotfixLargeFileFullFileDumpRejected is the KEY architectural invariant at
// the $hot boundary: a LARGE index.html plus a model that (wrongly) re-emits
// the whole file must NOT produce a full-file generation artifact. Izen pins
// the snippet-only contract in the prompt, and the changeset pipeline rejects
// the out-of-contract whole-file block with the bounded-contract error — the
// hotfix fails bounded, never proposing a whole-file ReplaceFile patch.
func TestHotfixLargeFileFullFileDumpRejected(t *testing.T) {
	large := largeHotfixIndexHTML()
	if execution.LineCount(large) < 100 {
		t.Fatalf("fixture must exceed the small-file boundary, got %d lines", execution.LineCount(large))
	}
	indexPath := writeHotfixFixture(t, "index.html", large)

	mock := &mockProvider{
		responses: []*ai.Response{
			// The model ignores the snippet contract and dumps the entire file.
			{Content: "```html\n" + large + "\n```", TokenOutput: 1200},
		},
	}
	m := newTestModel()
	m.provider = mock

	task := &plan.Task{
		StepNum:     1,
		Type:        "FILE_MUTATE",
		Target:      indexPath,
		Description: "Fix an HTML syntax error in @index.html",
	}

	msg := m.proposeHotfixPatch(task)()
	result, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("expected hotfixProposalMsg, got %T", msg)
	}
	if mock.callCount != 1 {
		t.Fatalf("provider calls = %d, want 1 (full-file dump is not an empty-response retry)", mock.callCount)
	}
	if result.Patch != nil {
		t.Fatalf("large-file hotfix must NOT produce a full-file patch, got: %+v", result.Patch)
	}
	if result.Err == nil {
		t.Fatal("expected the bounded-contract rejection for a whole-file re-emission")
	}
	if !strings.Contains(result.Err.Error(), "bounded hotfix contract") {
		t.Errorf("error missing bounded-contract reason: %v", result.Err)
	}

	// The prompt Izen sent must carry the snippet-only contract and must NEVER
	// invite a complete-file rewrite.
	if len(mock.requests) != 1 {
		t.Fatalf("captured requests = %d, want 1", len(mock.requests))
	}
	system := mock.requests[0].System
	if !strings.Contains(system, "corrected snippet") {
		t.Errorf("system prompt missing the snippet contract:\n%s", system)
	}
	if !strings.Contains(system, "Do NOT output the entire file") {
		t.Errorf("system prompt missing the whole-file prohibition:\n%s", system)
	}
	user := ""
	if len(mock.requests[0].Messages) > 0 {
		user = mock.requests[0].Messages[0].Content
	}
	if strings.Contains(user, "COMPLETE corrected file content") {
		t.Errorf("user prompt must not invite a complete-file rewrite for a large file:\n%s", user)
	}
	if !strings.Contains(user, "Do NOT output the entire file") {
		t.Errorf("user prompt missing the snippet-only output contract:\n%s", user)
	}
}

// TestHotfixLargeFileSnippetProducesBoundedPatch is the positive half of the
// invariant: a LARGE file IS editable via $hot, but only through the anchored
// snippet contract. A bounded snippet must compile into a valid unified diff
// (REPLACE_BLOCK), never a full-file replacement.
func TestHotfixLargeFileSnippetProducesBoundedPatch(t *testing.T) {
	large := largeHotfixIndexHTML()
	indexPath := writeHotfixFixture(t, "index.html", large)

	mock := &mockProvider{
		responses: []*ai.Response{
			{Content: "```html\n  <h3>Project Delta</h3>\n  <p>Stable content</p>\n```", TokenOutput: 80},
		},
	}
	m := newTestModel()
	m.provider = mock

	task := &plan.Task{
		StepNum:     1,
		Type:        "FILE_MUTATE",
		Target:      indexPath,
		Description: "Fix the syntax error in @index.html",
	}

	msg := m.proposeHotfixPatch(task)()
	result, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("expected hotfixProposalMsg, got %T", msg)
	}
	if result.Err != nil {
		t.Fatalf("bounded snippet hotfix failed: %v", result.Err)
	}
	if result.Patch == nil {
		t.Fatal("expected a non-nil patch from the bounded snippet contract")
	}
	// The patch payload must be the authoritative unified diff (REPLACE_BLOCK),
	// not a full-file rewrite.
	if result.Patch.IsFullRewrite {
		t.Fatal("large-file snippet hotfix must NOT be flagged as a full rewrite")
	}
	if !strings.Contains(result.Patch.Modified, "@@") {
		t.Fatalf("expected a compiled unified diff, got:\n%s", result.Patch.Modified)
	}
	if !strings.Contains(result.Diff, "Project Delta") {
		t.Errorf("approval diff missing the corrected header:\n%s", result.Diff)
	}
}

// TestHotfixSmallFileBoundedSnippetContract is the Phase 8 regression for the
// exact observed problem: a SMALL but real-content index.html must NOT force a
// full-file re-emission. The contract Izen pins is the bounded snippet — the
// prompt carries "output ONLY the exact lines that must change" — so a model
// that follows it produces a tiny, bounded patch. A model that (wrongly) dumps
// the complete small file still yields a valid bounded REPLACE_FILE artifact
// (the pipeline accepts a whole-file block only because the file is small), but
// the PROMPT never invites it.
func TestHotfixSmallFileBoundedSnippetContract(t *testing.T) {
	small := hotfixIndexHTML
	if execution.LineCount(small) >= 100 {
		t.Fatalf("fixture must be below the small-file boundary, got %d lines", execution.LineCount(small))
	}
	indexPath := writeHotfixFixture(t, "index.html", small)

	// The model follows the snippet contract: only the changed lines.
	snippet := "```html\n  <h3>Project Delta</h3>\n  <p>Stable content</p>\n```"
	mock := &mockProvider{
		responses: []*ai.Response{{Content: snippet, TokenOutput: 30}},
	}
	m := newTestModel()
	m.provider = mock

	task := &plan.Task{
		StepNum:     1,
		Type:        "FILE_MUTATE",
		Target:      indexPath,
		Description: "Remove extra text from @index.html",
	}

	msg := m.proposeHotfixPatch(task)()
	result, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("expected hotfixProposalMsg, got %T", msg)
	}
	if mock.callCount != 1 {
		t.Fatalf("provider calls = %d, want 1", mock.callCount)
	}
	if result.Err != nil {
		t.Fatalf("bounded snippet hotfix on a small file failed: %v", result.Err)
	}
	if result.Patch == nil || !strings.Contains(result.Patch.Modified, "@@") {
		t.Fatalf("expected a compiled diff, got: %+v", result.Patch)
	}

	// The prompt Izen sent must carry the bounded snippet contract and must
	// NEVER invite a complete-file re-emission.
	if len(mock.requests) != 1 {
		t.Fatalf("captured requests = %d, want 1", len(mock.requests))
	}
	user := ""
	if len(mock.requests[0].Messages) > 0 {
		user = mock.requests[0].Messages[0].Content
	}
	if strings.Contains(user, "COMPLETE, FULLY IMPLEMENTED") {
		t.Errorf("small real-content file prompt must not invite a complete-file rewrite:\n%s", user)
	}
	if !strings.Contains(user, "Do NOT output the entire file") {
		t.Errorf("small real-content file prompt missing the snippet-only output contract:\n%s", user)
	}
}

// TestHotfixNewFileKeepsFullCreationContract is the negative control for the
// Phase 8 contract fix: a genuinely NEW/empty file keeps the complete-file
// creation contract — bounded artifact economy applies only to real content.
func TestHotfixNewFileKeepsFullCreationContract(t *testing.T) {
	dir := t.TempDir()
	indexPath := filepath.Join(dir, "index.html")

	mock := &mockProvider{
		responses: []*ai.Response{
			{Content: "```html\n<!DOCTYPE html>\n<html><body><p>New</p></body></html>\n```", TokenOutput: 40},
		},
	}
	m := newTestModel()
	m.provider = mock

	task := &plan.Task{
		StepNum:     1,
		Type:        "FILE_MUTATE",
		Target:      indexPath,
		Description: "create an index page",
	}

	msg := m.proposeHotfixPatch(task)()
	result, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("expected hotfixProposalMsg, got %T", msg)
	}
	if result.Err != nil {
		t.Fatalf("new-file creation failed: %v", result.Err)
	}
	if len(mock.requests) != 1 {
		t.Fatalf("captured requests = %d, want 1", len(mock.requests))
	}
	user := ""
	if len(mock.requests[0].Messages) > 0 {
		user = mock.requests[0].Messages[0].Content
	}
	if !strings.Contains(user, "COMPLETE file content") {
		t.Errorf("new-file prompt must keep the complete-file creation contract:\n%s", user)
	}
}

// largeMismatchedIndexHTML is a large HTML document (> 100 lines) carrying the
// exact production failure signature: a mismatched closing tag
// "<h3>Project Delta</h2>" nested inside an <article>, plus a distinctive
// far-tail marker that must NEVER appear in a targeted prompt.
func largeMismatchedIndexHTML() string {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n")
	b.WriteString("<html lang=\"en\">\n")
	b.WriteString("<head>\n")
	b.WriteString("  <title>Large Landing</title>\n")
	b.WriteString("</head>\n")
	b.WriteString("<body>\n")
	b.WriteString("  <main>\n")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "    <section class=\"sect-%02d\">\n      <h3>Section %02d</h3>\n      <p>Section %02d body</p>\n    </section>\n", i, i, i)
	}
	b.WriteString("    <article class=\"project\">\n")
	b.WriteString("      <h3>Project Delta</h2>\n")
	b.WriteString("      <p>Stable content</p>\n")
	b.WriteString("    </article>\n")
	b.WriteString("    <div>unique-tail-marker</div>\n")
	b.WriteString("  </main>\n")
	b.WriteString("</body>\n")
	b.WriteString("</html>\n")
	return b.String()
}

// TestHotfixContractSelectsReplaceBlockBeforeInvocation is regression test 1:
// a large-file $hot selects the ReplaceBlock artifact contract BEFORE any model
// invocation — the decision is made by Izen from the on-disk state, not by the
// model's output. Phase 8 ownership fix: ANY existing file with real content —
// however small — also selects ReplaceBlock, so a tiny mutation never forces a
// full-file re-emission. Whole-file rewrite survives only for new / empty /
// whitespace-only files.
func TestHotfixContractSelectsReplaceBlockBeforeInvocation(t *testing.T) {
	if got := hotfixContractFor(largeMismatchedIndexHTML()); got != contractReplaceBlock {
		t.Errorf("hotfixContractFor(large) = %v, want contractReplaceBlock", got)
	}
	if got := hotfixContractFor(largeHotfixIndexHTML()); got != contractReplaceBlock {
		t.Errorf("hotfixContractFor(large) = %v, want contractReplaceBlock", got)
	}
	if got := hotfixContractFor(hotfixIndexHTML); got != contractReplaceBlock {
		t.Errorf("hotfixContractFor(small real file) = %v, want contractReplaceBlock", got)
	}
	if got := hotfixContractFor(""); got != contractReplaceFile {
		t.Errorf("hotfixContractFor(new) = %v, want contractReplaceFile", got)
	}
	if got := hotfixContractFor("   \n\t "); got != contractReplaceFile {
		t.Errorf("hotfixContractFor(whitespace-only) = %v, want contractReplaceFile", got)
	}
}

// TestHotfixLargeFileTargetedHandoff is regression tests 2-4: the FINAL model
// request for a large-file $hot on an HTML target carries (a) the bounded
// mutation contract, (b) the deterministic target scope (the resolved block and
// the exact mutation), and (c) never the entire file — and the primary
// generation is a single call (regression test 7: no retry loop).
func TestHotfixLargeFileTargetedHandoff(t *testing.T) {
	large := largeMismatchedIndexHTML()
	if execution.LineCount(large) < 100 {
		t.Fatalf("fixture must exceed the small-file boundary, got %d lines", execution.LineCount(large))
	}
	indexPath := writeHotfixFixture(t, "index.html", large)

	corrected := "```html\n    <article class=\"project\">\n      <h3>Project Delta</h3>\n      <p>Stable content</p>\n    </article>\n```"
	mock := &mockProvider{
		responses: []*ai.Response{
			{Content: corrected, TokenOutput: 80},
		},
	}
	m := newTestModel()
	m.provider = mock

	task := &plan.Task{
		StepNum:     1,
		Type:        "FILE_MUTATE",
		Target:      indexPath,
		Description: "Fix an HTML syntax error in @index.html",
	}

	msg := m.proposeHotfixPatch(task)()
	result, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("expected hotfixProposalMsg, got %T", msg)
	}
	// Regression test 7: exactly one generation call — no retry/heuristic loop.
	if mock.callCount != 1 {
		t.Fatalf("provider calls = %d, want 1", mock.callCount)
	}
	if result.Err != nil {
		t.Fatalf("targeted hotfix failed: %v", result.Err)
	}
	if len(mock.requests) != 1 {
		t.Fatalf("captured requests = %d, want 1", len(mock.requests))
	}
	system := mock.requests[0].System
	user := ""
	if len(mock.requests[0].Messages) > 0 {
		user = mock.requests[0].Messages[0].Content
	}

	// Regression test 2: the request carries the bounded mutation contract.
	if !strings.Contains(system, "corrected snippet") {
		t.Errorf("system prompt missing the bounded snippet contract:\n%s", system)
	}
	if !strings.Contains(user, "OUTPUT CONTRACT") {
		t.Errorf("user prompt missing the OUTPUT CONTRACT section:\n%s", user)
	}

	// Regression test 3: the request carries the deterministic target scope.
	if !strings.Contains(user, "TARGET BLOCK") {
		t.Errorf("user prompt missing the TARGET BLOCK scope:\n%s", user)
	}
	if !strings.Contains(user, "TARGET MUTATION") || !strings.Contains(user, "</h2>") {
		t.Errorf("user prompt missing the resolved mutation target:\n%s", user)
	}
	if !strings.Contains(user, "Project Delta") {
		t.Errorf("user prompt missing the target block content:\n%s", user)
	}

	// Regression test 4: the model is not instructed to reproduce the full file
	// AND the entire file is not present in the request.
	if strings.Contains(user, "COMPLETE corrected file content") {
		t.Errorf("user prompt must not invite a complete-file rewrite:\n%s", user)
	}
	if strings.Contains(user, "unique-tail-marker") {
		t.Errorf("user prompt leaked lines outside the target block (full file leaked):\n%s", user)
	}
	if !strings.Contains(user, "Do NOT output the entire file") {
		t.Errorf("user prompt missing the whole-file prohibition:\n%s", user)
	}

	// The bounded response compiled to a REPLACE_BLOCK artifact, not a rewrite.
	if result.Patch == nil || !strings.Contains(result.Patch.Modified, "@@") {
		t.Fatalf("expected a compiled unified diff, got:\n%s", func() string {
			if result.Patch == nil {
				return "<nil patch>"
			}
			return result.Patch.Modified
		}())
	}
	if result.Patch.IsFullRewrite {
		t.Fatal("targeted hotfix must NOT be flagged as a full rewrite")
	}
	if !strings.Contains(result.Diff, "<h3>Project Delta</h3>") {
		t.Errorf("approval diff missing the corrected block:\n%s", result.Diff)
	}
}

// TestHotfixLargeFileTargetedFullFileDumpRejected is regression test 6: even on
// the targeted path, a model that (wrongly) dumps the entire document is
// rejected with the bounded-contract pause — never applied.
func TestHotfixLargeFileTargetedFullFileDumpRejected(t *testing.T) {
	large := largeMismatchedIndexHTML()
	indexPath := writeHotfixFixture(t, "index.html", large)

	mock := &mockProvider{
		responses: []*ai.Response{
			{Content: "```html\n" + large + "\n```", TokenOutput: 1200},
		},
	}
	m := newTestModel()
	m.provider = mock

	task := &plan.Task{
		StepNum:     1,
		Type:        "FILE_MUTATE",
		Target:      indexPath,
		Description: "Fix an HTML syntax error in @index.html",
	}

	msg := m.proposeHotfixPatch(task)()
	result, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("expected hotfixProposalMsg, got %T", msg)
	}
	if mock.callCount != 1 {
		t.Fatalf("provider calls = %d, want 1 (no retry loop)", mock.callCount)
	}
	if result.Patch != nil {
		t.Fatalf("full-file dump must not produce a patch, got: %+v", result.Patch)
	}
	if result.Err == nil || !strings.Contains(result.Err.Error(), "bounded hotfix contract") {
		t.Fatalf("expected the bounded-contract rejection, got: %v", result.Err)
	}
}

// TestIsStructuralHotfixIntent pins the deterministic structural-intent
// classifier that gates both the HTML target resolver and the ambiguity pause.
func TestIsStructuralHotfixIntent(t *testing.T) {
	structural := []string{
		"Fix an HTML syntax error in @index.html",
		"fix the unclosed <section> tag @index.html",
		"repair the mismatched closing tag in @index.html",
		"index.html has broken markup",
	}
	for _, desc := range structural {
		if !isStructuralHotfixIntent(desc) {
			t.Errorf("isStructuralHotfixIntent(%q) = false, want true", desc)
		}
	}
	content := []string{
		"Remove extra text from @index.html",
		"change the title of @index.html",
		"update the footer in @index.html",
	}
	for _, desc := range content {
		if isStructuralHotfixIntent(desc) {
			t.Errorf("isStructuralHotfixIntent(%q) = true, want false", desc)
		}
	}
}

// TestHotfixAmbiguousContentMutationPauses is the regression test for the exact
// reported request: "$hot Remove extra text from @index.html" names a file but
// no explicit or uniquely-inferable mutation target. The system MUST pause and
// request clarification — it must NEVER invoke the model, never produce a
// patch, and never invent a target merely because an HTML structural anomaly
// exists in the file.
func TestHotfixAmbiguousContentMutationPauses(t *testing.T) {
	large := largeMismatchedIndexHTML()
	indexPath := writeHotfixFixture(t, "index.html", large)

	mock := &mockProvider{
		responses: []*ai.Response{{Content: "ignored", TokenOutput: 60}},
	}
	m := newTestModel()
	m.provider = mock

	task := &plan.Task{
		StepNum:     1,
		Type:        "FILE_MUTATE",
		Target:      indexPath,
		Description: "Remove extra text from @index.html",
	}

	msg := m.proposeHotfixPatch(task)()
	amb, ok := msg.(hotfixAmbiguousMsg)
	if !ok {
		t.Fatalf("expected hotfixAmbiguousMsg, got %T", msg)
	}
	if mock.callCount != 0 {
		t.Fatalf("ambiguous hotfix must NOT invoke the model (got %d calls)", mock.callCount)
	}
	if len(mock.requests) != 0 {
		t.Fatalf("ambiguous hotfix must not send a model request (got %d)", len(mock.requests))
	}
	if !strings.Contains(amb.Reason, "ambiguous") {
		t.Errorf("ambiguity reason missing the ambiguity signal: %q", amb.Reason)
	}
	if amb.Task == nil || amb.Task.Target != indexPath {
		t.Errorf("ambiguous message must carry the original target, got: %+v", amb.Task)
	}
	if len(amb.Candidates) == 0 {
		t.Errorf("ambiguous message for an HTML file with an anomaly must offer inspectable candidates")
	}

	// The structural anomaly in the file must NOT have become a mutation target:
	// the on-disk file is untouched.
	onDisk, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != large {
		t.Fatal("ambiguous hotfix mutated the file")
	}
}

// TestHotfixStructuralIntentStillExecutes is the companion regression test:
// the SAME large HTML file with a structural-intent request ("Fix an HTML
// syntax error") is uniquely inferable and DOES execute — the ambiguity pause
// must not over-block legitimate defect-fix requests.
func TestHotfixStructuralIntentStillExecutes(t *testing.T) {
	large := largeMismatchedIndexHTML()
	indexPath := writeHotfixFixture(t, "index.html", large)

	corrected := "```html\n    <article class=\"project\">\n      <h3>Project Delta</h3>\n      <p>Stable content</p>\n    </article>\n```"
	mock := &mockProvider{
		responses: []*ai.Response{{Content: corrected, TokenOutput: 80}},
	}
	m := newTestModel()
	m.provider = mock

	task := &plan.Task{
		StepNum:     1,
		Type:        "FILE_MUTATE",
		Target:      indexPath,
		Description: "Fix an HTML syntax error in @index.html",
	}

	msg := m.proposeHotfixPatch(task)()
	result, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("expected hotfixProposalMsg, got %T", msg)
	}
	if mock.callCount != 1 {
		t.Fatalf("structural hotfix must reach the model, got %d calls", mock.callCount)
	}
	if result.Err != nil {
		t.Fatalf("structural hotfix failed: %v", result.Err)
	}
	if result.Patch == nil || !strings.Contains(result.Patch.Modified, "@@") {
		t.Fatalf("expected a compiled ReplaceBlock diff, got: %+v", result.Patch)
	}
}
