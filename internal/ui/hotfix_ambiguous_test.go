package ui

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/hotfix"
	"github.com/PizenLabs/izen/internal/modes/plan"
)

// recordsText joins every record the model has pushed so tests can assert what
// was (and was not) rendered/logged.
func recordsText(m *model) string {
	var b strings.Builder
	for _, r := range m.records {
		b.WriteString(r.text)
		b.WriteString("\n")
	}
	return b.String()
}

// ambiguousMsgFor builds a hotfixAmbiguousMsg for the given request/candidates.
func ambiguousMsgFor(desc, target string, cands []hotfix.Target) hotfixAmbiguousMsg {
	return hotfixAmbiguousMsg{
		Task:       &plan.Task{StepNum: 0, Type: "FILE_MUTATE", Target: target, Description: desc},
		Reason:     "ambiguous: " + desc + " does not name a mutation target",
		Candidates: cands,
	}
}

// TestHotfixTargetResolutionStatusDistinction pins requirement 5: the runtime
// distinguishes target_not_found (no deterministic candidates) from
// target_ambiguous (multiple candidates) from target_resolved — they are never
// collapsed into a generic hotfix failure.
func TestHotfixTargetResolutionStatusDistinction(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	// target_ambiguous: a large HTML file with structural candidates.
	large := largeMismatchedIndexHTML()
	if err := os.WriteFile("a.html", []byte(large), 0o644); err != nil {
		t.Fatal(err)
	}
	ambiguous := classifyHotfixAmbiguity("Remove extra text from @a.html", "a.html", large)
	if !ambiguous.ambiguous {
		t.Fatal("structural-content request on a large file must be ambiguous")
	}
	if ambiguous.status != targetAmbiguous || len(ambiguous.candidates) == 0 {
		t.Errorf("status = %s candidates=%d, want target_ambiguous with candidates", ambiguous.status, len(ambiguous.candidates))
	}

	// target_not_found: a large HTML file with no structural candidates.
	wellFormed := "<!DOCTYPE html>\n<html><head><title>T</title></head><body><main><p>ok</p></main></body></html>\n"
	// Pad beyond the small-file threshold so the ambiguity boundary runs.
	for i := 0; i < 120; i++ {
		wellFormed += "<p>padding " + numStr(i) + "</p>\n"
	}
	if err := os.WriteFile("b.html", []byte(wellFormed), 0o644); err != nil {
		t.Fatal(err)
	}
	notFound := classifyHotfixAmbiguity("Remove extra text from @b.html", "b.html", wellFormed)
	if !notFound.ambiguous {
		t.Fatal("content mutation on a large well-formed file must pause")
	}
	if notFound.status != targetNotFound {
		t.Errorf("status = %s, want target_not_found", notFound.status)
	}

	// target_resolved: a redundancy-removal request with deterministic evidence.
	if err := os.WriteFile("c.html", []byte(largeRedundantIndexHTML()), 0o644); err != nil {
		t.Fatal(err)
	}
	resolved := classifyHotfixAmbiguity("check @c.html and remove redundant content", "c.html", largeRedundantIndexHTML())
	if resolved.ambiguous {
		t.Fatal("redundancy-resolved request must not pause")
	}
	if resolved.status != targetResolved || len(resolved.redundant) == 0 {
		t.Errorf("status = %s redundant=%d, want target_resolved with evidence", resolved.status, len(resolved.redundant))
	}
}

// TestHotfixAmbiguousRendersActionableState is regression test 1: an ambiguous
// $hot request renders an actionable ambiguity state with Clarify / Cancel
// actions — not a bare "Patch generation failed" line.
func TestHotfixAmbiguousRendersActionableState(t *testing.T) {
	m := newTestModel()
	res, _ := m.Update(ambiguousMsgFor("Remove extra text from @index.html", "index.html", nil))
	m2 := res.(*model)

	if m2.pendingHotfixAmbiguous == nil {
		t.Fatal("ambiguous request must enter the pending ambiguity state")
	}
	if m2.state != StateHotfixAmbiguous {
		t.Fatalf("state = %v, want StateHotfixAmbiguous", m2.state)
	}
	// AMBIGUOUS is a result/state of the hotfix operation, NOT a workflow
	// phase: the workflow state machine must not transition.
	if m2.workflowSM != nil && m2.workflowSM.State() != workflow.StateIdle {
		t.Fatalf("ambiguity must not transition the workflow phase, got %s", m2.workflowSM.State())
	}
	view := m2.renderProposalBlock()
	if !strings.Contains(view, "HOTFIX TARGET AMBIGUOUS") {
		t.Errorf("card missing the ambiguity title:\n%s", view)
	}
	if !strings.Contains(view, "[⌥C] Clarify target") {
		t.Errorf("card missing the Clarify action:\n%s", view)
	}
	if !strings.Contains(view, "[⌥X] Cancel") {
		t.Errorf("card missing the Cancel action:\n%s", view)
	}
}

// TestHotfixAmbiguousNoAcceptReject is regression test 2: the ambiguous card
// never renders Accept/Reject because there is no patch (Patch == nil).
func TestHotfixAmbiguousNoAcceptReject(t *testing.T) {
	m := newTestModel()
	res, _ := m.Update(ambiguousMsgFor("Remove extra text from @index.html", "index.html", nil))
	m2 := res.(*model)

	if len(m2.pendingProposals) != 0 {
		t.Fatal("ambiguous request must not stage any patch proposal")
	}
	view := m2.renderProposalBlock()
	for _, forbidden := range []string{"Accept", "Reject", "Alt+A", "Alt+R"} {
		if strings.Contains(view, forbidden) {
			t.Errorf("ambiguous card must not render %q (no patch exists):\n%s", forbidden, view)
		}
	}
}

// TestHotfixAmbiguousClarifyReturnsFocusToInput is regression test 3: selecting
// Clarify returns focus to the build input and dismisses the ambiguous card.
func TestHotfixAmbiguousClarifyReturnsFocusToInput(t *testing.T) {
	m := newTestModel()
	res, _ := m.Update(ambiguousMsgFor("Remove extra text from @index.html", "index.html", nil))
	m2 := res.(*model)
	m2.ti.Blur()

	// Clarify uses the explicit alt+c keybinding (a plain 'c' is always text).
	m3, cmd := m2.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}, Alt: true})
	if cmd != nil {
		t.Fatalf("clarify returned a cmd: %v", cmd)
	}
	after := m3.(*model)
	if after.pendingHotfixAmbiguous != nil {
		t.Fatal("clarify must dismiss the ambiguous card")
	}
	if after.state != StateChat {
		t.Fatalf("state = %v, want StateChat after clarify", after.state)
	}
	if !after.ti.Focused() {
		t.Fatal("clarify must return focus to the build input")
	}
}

// TestHotfixAmbiguousInspectCandidatesNoMutation is regression test 4: Inspect
// candidates is strictly read-only — it never mutates the file and never
// auto-selects a candidate.
func TestHotfixAmbiguousInspectCandidatesNoMutation(t *testing.T) {
	dir := t.TempDir()
	target := dir + "/index.html"
	large := largeMismatchedIndexHTML()
	if err := os.WriteFile(target, []byte(large), 0o644); err != nil {
		t.Fatal(err)
	}
	cands := hotfix.ResolveHTMLCandidates(large)
	if len(cands) == 0 {
		t.Fatal("fixture must yield deterministic candidates")
	}

	m := newTestModel()
	res, _ := m.Update(ambiguousMsgFor("Remove extra text from @index.html", target, cands))
	m2 := res.(*model)

	m3, _ := m2.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}, Alt: true})
	after := m3.(*model)
	if !after.hotfixCandidatesMode {
		t.Fatal("alt+i must toggle candidate-inspection mode")
	}
	view := after.renderProposalBlock()
	if !strings.Contains(view, cands[0].Mismatch.Describe()) {
		t.Errorf("candidate view missing the anomaly description:\n%s", view)
	}
	// Strictly read-only: the on-disk file is untouched.
	onDisk, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != large {
		t.Fatal("candidate inspection mutated the file")
	}
	if after.pendingHotfixAmbiguous == nil {
		t.Fatal("candidate inspection must not dismiss the ambiguity state")
	}
}

// TestHotfixAmbiguousNoProviderBeforeClarify is regression test 5: no provider
// call occurs before the target is clarified.
func TestHotfixAmbiguousNoProviderBeforeClarify(t *testing.T) {
	dir := t.TempDir()
	target := dir + "/index.html"
	if err := os.WriteFile(target, []byte(largeMismatchedIndexHTML()), 0o644); err != nil {
		t.Fatal(err)
	}

	mock := &mockProvider{responses: []*ai.Response{{Content: "x", TokenOutput: 10}}}
	m := newTestModel()
	m.provider = mock

	task := &plan.Task{StepNum: 0, Type: "FILE_MUTATE", Target: target, Description: "Remove extra text from @index.html"}
	msg := m.proposeHotfixPatch(task)()
	if _, ok := msg.(hotfixAmbiguousMsg); !ok {
		t.Fatalf("expected hotfixAmbiguousMsg before clarification, got %T", msg)
	}
	if mock.callCount != 0 {
		t.Fatalf("provider called %d times before clarification, want 0", mock.callCount)
	}
}

// TestHotfixAmbiguousNoProviderProgress is regression test 6: when the
// ambiguity gate prevents provider invocation (callCount == 0), the UI must not
// claim "Invoking provider" — the provider-invocation progress now lives only
// after the gate has passed.
func TestHotfixAmbiguousNoProviderProgress(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("index.html", []byte(largeMismatchedIndexHTML()), 0o644); err != nil {
		t.Fatal(err)
	}

	mock := &mockProvider{responses: []*ai.Response{{Content: "x", TokenOutput: 10}}}
	m := newTestModel()
	m.provider = mock

	cmd := m.handleHotfixCmd("Remove extra text from @index.html")
	if cmd == nil {
		t.Fatal("handleHotfixCmd returned nil for an ambiguous request")
	}
	var msg tea.Msg
	for _, em := range drainCmds(t, cmd) {
		if am, ok := em.(hotfixAmbiguousMsg); ok {
			msg = am
			break
		}
	}
	if msg == nil {
		t.Fatalf("expected hotfixAmbiguousMsg, got no ambiguous message from batch")
	}
	if _, ok := msg.(hotfixAmbiguousMsg); !ok {
		t.Fatalf("expected hotfixAmbiguousMsg, got %T", msg)
	}
	if mock.callCount != 0 {
		t.Fatalf("provider called %d times for an ambiguous request, want 0", mock.callCount)
	}
	logged := recordsText(m)
	if strings.Contains(logged, "Invoking") {
		t.Errorf("provider-invocation progress claimed with callCount==0:\n%s", logged)
	}
	if !strings.Contains(logged, "Target is ambiguous") {
		t.Errorf("ambiguous status line missing:\n%s", logged)
	}
}

// TestHotfixStructuralBehaviorUnchanged is regression test 7: a structural-intent
// request still executes the normal bounded mutation flow — never the ambiguity
// card.
func TestHotfixStructuralBehaviorUnchanged(t *testing.T) {
	dir := t.TempDir()
	target := dir + "/index.html"
	large := largeMismatchedIndexHTML()
	if err := os.WriteFile(target, []byte(large), 0o644); err != nil {
		t.Fatal(err)
	}
	corrected := "```html\n    <article class=\"project\">\n      <h3>Project Delta</h3>\n      <p>Stable content</p>\n    </article>\n```"
	mock := &mockProvider{responses: []*ai.Response{{Content: corrected, TokenOutput: 80}}}
	m := newTestModel()
	m.provider = mock

	task := &plan.Task{StepNum: 0, Type: "FILE_MUTATE", Target: target, Description: "Fix an HTML syntax error in @index.html"}
	msg := m.proposeHotfixPatch(task)()
	prop, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("structural request must yield a proposal, got %T", msg)
	}
	if prop.Err != nil {
		t.Fatalf("structural hotfix failed: %v", prop.Err)
	}
	if prop.Patch == nil || !strings.Contains(prop.Patch.Modified, "@@") {
		t.Fatalf("expected a compiled ReplaceBlock diff, got: %+v", prop.Patch)
	}
	if mock.callCount != 1 {
		t.Fatalf("provider calls = %d, want 1", mock.callCount)
	}
}

// TestHotfixReplaceBlockSafetyUnchanged is regression test 8: the ReplaceBlock
// safety invariant is unchanged — a full-file response is still rejected with
// the bounded-contract pause.
func TestHotfixReplaceBlockSafetyUnchanged(t *testing.T) {
	dir := t.TempDir()
	target := dir + "/index.html"
	large := largeMismatchedIndexHTML()
	if err := os.WriteFile(target, []byte(large), 0o644); err != nil {
		t.Fatal(err)
	}
	mock := &mockProvider{responses: []*ai.Response{{Content: "```html\n" + large + "\n```", TokenOutput: 1200}}}
	m := newTestModel()
	m.provider = mock

	task := &plan.Task{StepNum: 0, Type: "FILE_MUTATE", Target: target, Description: "Fix an HTML syntax error in @index.html"}
	msg := m.proposeHotfixPatch(task)()
	result, ok := msg.(hotfixProposalMsg)
	if !ok {
		t.Fatalf("expected hotfixProposalMsg, got %T", msg)
	}
	if result.Patch != nil {
		t.Fatalf("full-file dump must not produce a patch, got: %+v", result.Patch)
	}
	if result.Err == nil || !strings.Contains(result.Err.Error(), "bounded hotfix contract") {
		t.Fatalf("expected the bounded-contract rejection, got: %v", result.Err)
	}
}

// TestHotfixBudgetAuthorizationUnchanged is regression test 9: the budget /
// authorization gate is unchanged — an exhausted mutation budget still blocks
// the apply and never touches the file.
func TestHotfixBudgetAuthorizationUnchanged(t *testing.T) {
	dir := t.TempDir()
	target := dir + "/index.html"
	original := "<html><body><h1>Hi</h1></body></html>\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	m := wiredAuthModel(exhaustedMutationBudget())
	m.workflowSM = nil

	task := &plan.Task{StepNum: 0, Type: "FILE_MUTATE", Target: target}
	patch := &execution.Patch{File: target, Modified: "--- a\n+++ b\n@@ -1 +1 @@\n-old\n+new\n"}

	msg := m.applyHotfixPatch(task, patch)()
	im, ok := msg.(buildResultMsg)
	if !ok {
		t.Fatalf("expected buildResultMsg, got %T", msg)
	}
	if im.err == nil || !strings.Contains(im.err.Error(), "mutation budget already exhausted") {
		t.Fatalf("expected the budget gate to block the apply, got: %v", im.err)
	}
	onDisk, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != original {
		t.Fatalf("file was mutated despite the budget gate:\n%s", onDisk)
	}
}

// TestHotfixCandidateSelectionRunsTargetedPipeline proves the candidate flow:
// human-selected candidate → target becomes explicit → normal bounded mutation
// pipeline (targeted handoff, model once, ReplaceBlock diff). It also proves a
// candidate is NEVER auto-selected (callCount == 0 until the human acts).
func TestHotfixCandidateSelectionRunsTargetedPipeline(t *testing.T) {
	dir := t.TempDir()
	target := dir + "/index.html"
	large := largeMismatchedIndexHTML()
	if err := os.WriteFile(target, []byte(large), 0o644); err != nil {
		t.Fatal(err)
	}
	cands := hotfix.ResolveHTMLCandidates(large)
	if len(cands) == 0 {
		t.Fatal("fixture must yield deterministic candidates")
	}

	corrected := "```html\n    <article class=\"project\">\n      <h3>Project Delta</h3>\n      <p>Stable content</p>\n    </article>\n```"
	mock := &mockProvider{responses: []*ai.Response{{Content: corrected, TokenOutput: 80}}}
	m := newTestModel()
	m.provider = mock

	// No provider call before the human acts.
	res, _ := m.Update(ambiguousMsgFor("Remove extra text from @index.html", target, cands))
	m2 := res.(*model)
	if mock.callCount != 0 {
		t.Fatalf("provider called before candidate selection: %d", mock.callCount)
	}

	// The human enters candidate-inspection mode (alt+i — a plain 'i' is
	// always text), then explicitly selects candidate #1.
	m3, _ := m2.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}, Alt: true})
	m4 := m3.(*model)
	if !m4.hotfixCandidatesMode {
		t.Fatal("alt+i must enter candidate-inspection mode")
	}
	// Candidate selection owns the number keys ONLY while the inspection
	// sub-view blurs the input (explicit modal interaction).
	if m4.ti.Focused() {
		t.Fatal("candidate-inspection mode must blur the input so [1-9] are modal")
	}
	sel, cmds := m4.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	after := sel.(*model)
	if after.pendingHotfixAmbiguous != nil {
		t.Fatal("candidate selection must dismiss the ambiguity card")
	}
	msgs := drainCmds(t, cmds)

	var prop *hotfixProposalMsg
	for _, em := range msgs {
		if p, ok := em.(hotfixProposalMsg); ok {
			prop = &p
			break
		}
	}
	if prop == nil {
		t.Fatalf("candidate-selected hotfix must produce a proposal (messages: %d)", len(msgs))
	}
	if prop.Err != nil {
		t.Fatalf("candidate-selected hotfix failed: %v", prop.Err)
	}
	if prop.Patch == nil || !strings.Contains(prop.Patch.Modified, "@@") {
		t.Fatalf("expected a compiled ReplaceBlock diff, got: %+v", prop.Patch)
	}
	if prop.Patch.IsFullRewrite {
		t.Fatal("candidate-selected hotfix must not be a full rewrite")
	}
	if mock.callCount != 1 {
		t.Fatalf("provider calls = %d, want exactly 1 after explicit candidate selection", mock.callCount)
	}
	// The request carried the candidate's deterministic target scope.
	user := ""
	if len(mock.requests) == 1 && len(mock.requests[0].Messages) > 0 {
		user = mock.requests[0].Messages[0].Content
	}
	if !strings.Contains(user, "TARGET BLOCK") || !strings.Contains(user, cands[0].Mismatch.Describe()) {
		t.Errorf("candidate-selected request must carry the selected candidate's scope:\n%s", user)
	}
}
