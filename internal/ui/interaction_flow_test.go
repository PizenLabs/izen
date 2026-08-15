package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/hotfix"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/plan"
)

// ── PHASE 6: DIRECTIVE → ACTION → EXECUTION UX REGRESSION SUITE ───────────
// These tests pin the continuous, intent-driven interaction model:
//  1. $hot inside /build executes immediately.
//  2. $hot outside /build enters the required context and continues.
//  3. No second /build is ever required.
//  4. $prompt does not unnecessarily stop at a mode transition.
//  5. Printable text cannot be hijacked by hotkeys.
//  6. Keyword/keybinding conflicts are impossible while typing.
//  7. A resolved target executes without unnecessary confirmation.
//  8. An ambiguous target stops with useful action chips.
//  9. A confirmation-required action stops correctly.
// 10. Action chips disappear after execution.
// 11. Stale chips cannot execute an obsolete operation.
// 12. A cancelled operation cannot be resumed through stale UI.
// 13. The next command is accepted immediately after a terminal state.
// 14. No duplicate dispatch.
// 15. One user action → one execution ownership.

func readyChatModel(m *model) *model {
	m.state = StateChat
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	m.streaming = false
	m.agentRunning = false
	return m
}

// initializedChatModel builds a chat-ready model backed by an on-disk
// workspace so the full Update() key pipeline (including the init-stage
// routing guard) accepts keyboard input like the real app.
func initializedChatModel(t *testing.T) *model {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".izen"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".izen", "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := readyChatModel(newTestModel())
	m.workspaceRoot = dir
	m.initStage = initComplete
	return m
}

// ── 1. $hot inside /build executes immediately ────────────────────────────

func TestPhase6HotInBuildExecutesImmediately(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	m := readyChatModel(newTestModel())
	m.resolver.Set(modes.ModeBuild)

	cmd := m.handleInput("$hot add a README file @README.md")
	if cmd == nil {
		t.Fatal("$hot inside /build returned nil cmd — the directive must dispatch immediately")
	}
	if got := m.resolver.Current(); got != modes.ModeBuild {
		t.Fatalf("mode = /%s, want /build (unchanged — /build is already the execution context)", got)
	}
	if m.activeOp == nil || m.activeOp.Kind != OpHotfix {
		t.Fatalf("expected an active hotfix operation, got %+v", m.activeOp)
	}
	if !strings.Contains(recordsText(m), "[HOTFIX] Urgent hotfix:") {
		t.Error("expected the hotfix pipeline to have started")
	}
}

// ── 2 + 3. $hot outside /build auto-enters /build and continues; no repeat ─

func TestPhase6HotOutsideBuildTransitionsAndContinues(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	for _, start := range []modes.Mode{modes.ModeAsk, modes.ModePlan, modes.ModeReview} {
		m := readyChatModel(newTestModel())
		m.resolver.Set(start)

		cmd := m.handleInput("$hot add a README file @README.md")
		if cmd == nil {
			t.Fatalf("from /%s: $hot returned nil cmd — continuous execution must dispatch", start)
		}
		// The transition happens internally; the directive continues in /build.
		if got := m.resolver.Current(); got != modes.ModeBuild {
			t.Fatalf("from /%s: mode = /%s, want /build after internal transition", start, got)
		}
		if m.activeOp == nil || m.activeOp.Kind != OpHotfix {
			t.Fatalf("from /%s: expected an active hotfix operation", start)
		}
		// Test 3: no second /build — the pipeline ran from the single input.
		if n := strings.Count(recordsText(m), "[HOTFIX] Urgent hotfix:"); n != 1 {
			t.Fatalf("from /%s: hotfix dispatched %d times, want exactly 1 (single input → single execution)", start, n)
		}
	}
}

// TestPhase6HotFromAskSingleDispatch guards test 14 (no duplicate dispatch):
// the auto-transition + directive dispatch must yield exactly one execution
// start, never a re-parse of the raw input through a second routing path.
func TestPhase6HotFromAskSingleDispatch(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	m := readyChatModel(newTestModel())
	m.resolver.Set(modes.ModeAsk)

	m.handleInput("$hot add a LICENSE file @LICENSE")

	if n := strings.Count(recordsText(m), "[HOTFIX] Urgent hotfix:"); n != 1 {
		t.Fatalf("hotfix dispatched %d times, want exactly 1", n)
	}
	if m.activeOp == nil {
		t.Fatal("expected one active hotfix operation")
	}
}

// ── 4. $prompt does not unnecessarily stop at a mode transition ───────────

func TestPhase6PromptTransitionsAndContinues(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.resolver.Set(modes.ModeBuild)

	cmd := m.handleInput("$prompt design a plugin architecture")
	if cmd == nil {
		t.Fatal("$prompt returned nil cmd — the /ask handoff must fire, not stop at the transition")
	}
	if got := m.resolver.Current(); got != modes.ModeAsk {
		t.Fatalf("mode = /%s, want /ask after $prompt router", got)
	}
	// The transition is NOT a dead end: the ask-handoff command continues the
	// pipeline in the same turn.
	if !strings.Contains(recordsText(m), "transitioning to /ask for structured analysis") {
		t.Error("expected the mode transition notice alongside continued execution")
	}
}

// ── 5 + 6. Printable text is never hijacked by hotkeys/keybindings ────────

func TestPhase6PrintableCharInStateChatIsText(t *testing.T) {
	m := initializedChatModel(t)
	m.resolver.Set(modes.ModePlan)

	// A plan view exposes an "approve-plan" chip with shortcut alt+p; a plain
	// 'p' must type, never activate the chip. (The returned cmd is the
	// textinput's cursor-blink tick, never an execution command.)
	m.ti.Focus()
	m.ti.SetValue("")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if got := m.ti.Value(); got != "p" {
		t.Fatalf("input value = %q, want %q", got, "p")
	}
	if m.resolver.Current() != modes.ModePlan {
		t.Fatalf("mode changed to /%s — typing must never switch modes", m.resolver.Current())
	}
}

func TestPhase6QuestionMarkTypesInsteadOfHelp(t *testing.T) {
	m := initializedChatModel(t)
	m.ti.Focus()
	m.ti.SetValue("")

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if m.showHelpOverlay {
		t.Fatal("'?' must be text, not a help-toggle hotkey")
	}
	if got := m.ti.Value(); got != "?" {
		t.Fatalf("input value = %q, want %q", got, "?")
	}
}

func TestPhase6TypingDuringAmbiguityCardIsText(t *testing.T) {
	m := readyChatModel(newTestModel())
	res, _ := m.Update(ambiguousMsgFor("Remove extra text from @index.html", "index.html", nil))
	m2 := res.(*model)
	if m2.state != StateHotfixAmbiguous {
		t.Fatalf("precondition: state = %v, want StateHotfixAmbiguous", m2.state)
	}
	if !m2.ti.Focused() {
		t.Fatal("precondition: input must be focused while the ambiguity card renders")
	}

	// Typing "check" while the card is up must produce text in the input — a
	// 'c' must NOT trigger Clarify, an 'x' must NOT trigger Cancel.
	for _, r := range "check" {
		res, _ = m2.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m2 = res.(*model)
	}
	if got := m2.ti.Value(); got != "check" {
		t.Fatalf("input value = %q, want %q — printable chars were hijacked by the card", got, "check")
	}
	if m2.pendingHotfixAmbiguous == nil {
		t.Fatal("typing must not dismiss the ambiguity card")
	}
	if m2.state != StateHotfixAmbiguous {
		t.Fatalf("state = %v, want StateHotfixAmbiguous while typing", m2.state)
	}
}

// TestPhase6TestInBuildTransitionsToReview pins the general continuity rule:
// a directive typed in a mode that is NOT one of its native contexts performs
// the internal transition and continues. $test executes in /review or
// /investigate; typed inside /build it aligns to /review automatically rather
// than erroring "unknown build action".
func TestPhase6TestInBuildTransitionsToReview(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.resolver.Set(modes.ModeBuild)

	cmd := m.handleInput("$test ./...")
	if cmd == nil {
		t.Fatal("$test inside /build returned nil cmd — directive must continue in its execution context")
	}
	if got := m.resolver.Current(); got != modes.ModeReview {
		t.Fatalf("mode = /%s, want /review after internal alignment", got)
	}
	if !m.reviewRunning {
		t.Error("$test must dispatch the test runner after the internal transition")
	}
}

// ── 7. A resolved target executes without unnecessary confirmation ────────

func TestPhase6ResolvedTargetExecutesWithoutAmbiguityStop(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("LICENSE", []byte("Copyright 2023\nAll rights reserved.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mock := &mockProvider{responses: []*ai.Response{{Content: "Copyright 2026\nAll rights reserved.\n", TokenOutput: 20}}}
	m := readyChatModel(newTestModel())
	m.provider = mock
	m.resolver.Set(modes.ModeBuild)

	cmd := m.handleInput("$hot update the year to 2026 @LICENSE")
	if cmd == nil {
		t.Fatal("$hot with a resolved target returned nil cmd")
	}
	// No ambiguity stop: the operation began executing immediately.
	if m.state == StateHotfixAmbiguous {
		t.Fatal("a resolved target must not enter the ambiguity card")
	}
	if m.activeOp == nil || m.activeOp.Kind != OpHotfix {
		t.Fatalf("expected an active hotfix operation for a resolved target")
	}
}

// ── 8. An ambiguous target stops with useful action chips ─────────────────

func TestPhase6AmbiguousTargetStopsWithActionableChips(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "index.html")
	if err := os.WriteFile(target, []byte(largeMismatchedIndexHTML()), 0o644); err != nil {
		t.Fatal(err)
	}
	cands := hotfix.ResolveHTMLCandidates(largeMismatchedIndexHTML())
	if len(cands) == 0 {
		t.Fatal("fixture must yield deterministic candidates")
	}

	m := readyChatModel(newTestModel())
	res, _ := m.Update(ambiguousMsgFor("Remove extra text from @index.html", target, cands))
	m2 := res.(*model)
	if m2.state != StateHotfixAmbiguous {
		t.Fatalf("state = %v, want StateHotfixAmbiguous", m2.state)
	}
	view := m2.renderHotfixAmbiguousBlock(100)
	// The card offers genuine next actions (inspect / cancel), not decoration.
	for _, want := range []string{"Inspect candidates", "Cancel", "Clarify target"} {
		if !strings.Contains(view, want) {
			t.Errorf("ambiguity card missing actionable chip %q:\n%s", want, view)
		}
	}
}

// ── 9. A confirmation-required action stops correctly ─────────────────────

func TestPhase6ConfirmationRequiredStopsAtApproval(t *testing.T) {
	m := newHotfixBusyModel(t)

	res, _ := m.Update(hotfixProposalMsg{
		Task: &plan.Task{StepNum: 1, Type: "FILE_MUTATE", Target: "LICENSE"},
		Patch: &execution.Patch{
			ID:       "hotfix-1",
			File:     "LICENSE",
			Original: "old",
			Modified: "new",
		},
		Diff: "--- a/LICENSE\n+++ b/LICENSE\n@@ -1 +1 @@\n-old\n+new\n",
	})
	m2 := res.(*model)
	if m2.state != StateAwaitingApproval {
		t.Fatalf("state = %v, want StateAwaitingApproval — the confirmation gate must hold", m2.state)
	}
	if len(m2.pendingProposals) == 0 {
		t.Fatal("confirmation-required stop must stage the reviewable proposal")
	}
	// The stop is a clean pause, not a leaked worker: no transient execution
	// flag remains spinning.
	if m2.agentRunning || m2.reviewRunning || m2.streaming || m2.pipelineRunning {
		t.Errorf("transient execution flags still set at the confirmation stop: %v/%v/%v/%v",
			m2.agentRunning, m2.reviewRunning, m2.streaming, m2.pipelineRunning)
	}
}

// ── 10 + 11. Action chips disappear after execution / new input ───────────

func TestPhase6ChipsDisappearAfterActivation(t *testing.T) {
	m := readyChatModel(newTestModel())
	m.resolver.Set(modes.ModePlan)
	m.currentResult = planApprovalActions()
	if len(m.currentResultActions()) == 0 {
		t.Fatal("precondition: chips must be present")
	}

	_ = m.handleChipActivation(m.currentResultActions()[0])
	// Consuming a chip ends its relevance: no stale chip remains on screen.
	if m.currentResult != nil {
		t.Fatal("action chips must disappear after activation/execution")
	}
	if !m.planApproved {
		t.Fatal("approve-plan activation must record the approval before execution")
	}
}

func TestPhase6StaleChipsClearedOnNewInput(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	m := readyChatModel(newTestModel())
	m.resolver.Set(modes.ModeBuild)
	m.currentResult = buildVerifyResult(true)

	m.handleInput("$hot add a README file @README.md")

	if m.currentResult != nil {
		t.Fatal("a stale chip from a previous operation must be cleared by new user input")
	}
}

// ── 12. A cancelled operation cannot be resumed through stale UI ──────────

func TestPhase6CancelledOperationNotResumable(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "index.html")
	if err := os.WriteFile(target, []byte(largeMismatchedIndexHTML()), 0o644); err != nil {
		t.Fatal(err)
	}
	mock := &mockProvider{responses: []*ai.Response{{Content: "x", TokenOutput: 10}}}
	m := newTestModel()
	m.provider = mock
	res, _ := m.Update(ambiguousMsgFor("Remove extra text from @index.html", target, nil))
	m2 := res.(*model)

	// Cancel the ambiguous operation explicitly ([⌥X]).
	res, _ = m2.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}, Alt: true})
	m3 := res.(*model)
	if m3.pendingHotfixAmbiguous != nil {
		t.Fatal("cancel must dismiss the ambiguity card")
	}
	if m3.activeOp != nil {
		t.Fatal("cancel must release operation ownership")
	}

	// A subsequent idle keystroke (Enter on empty input) must NOT resume the
	// cancelled operation.
	res, _ = m3.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	m4 := res.(*model)
	if m4.activeOp != nil {
		t.Fatal("idle Enter resumed the cancelled operation")
	}
	if mock.callCount != 0 {
		t.Fatalf("provider invoked after cancel: %d calls", mock.callCount)
	}
	if m4.pendingHotfixAmbiguous != nil {
		t.Fatal("idle Enter resurrected the cancelled ambiguity card")
	}
}

// ── 13. The next command is accepted immediately after a terminal state ───

func TestPhase6NextCommandAcceptedAfterTerminalState(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	// Terminal state: a hotfix apply completed → StateChat, input focused.
	m := readyChatModel(newTestModel())
	m.resolver.Set(modes.ModeBuild)
	m.Update(buildResultMsg{output: "applied", exitCode: 0})

	if m.state != StateChat {
		t.Fatalf("state = %v, want StateChat after terminal result", m.state)
	}
	m.ti.Focus()
	m.ti.SetValue("$hot add a LICENSE file @LICENSE")
	res, cmd := m.submitEnter()
	m2 := res.(*model)
	if cmd == nil {
		t.Fatal("the next command after a terminal state must dispatch immediately")
	}
	if !strings.Contains(recordsText(m2), "[HOTFIX] Urgent hotfix:") {
		t.Error("the next command did not execute after the terminal state")
	}
}

// ── 14 + 15. No duplicate dispatch; one action → one ownership ────────────

func TestPhase6OneActionOneOwnership(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	m := readyChatModel(newTestModel())
	m.resolver.Set(modes.ModeAsk)

	m.handleInput("$hot add a LICENSE file @LICENSE")

	// Exactly one active operation, of exactly one kind — a single user action
	// owns a single execution.
	if m.activeOp == nil {
		t.Fatal("expected exactly one active operation")
	}
	if m.activeOp.Kind != OpHotfix {
		t.Fatalf("operation kind = %s, want hotfix", m.activeOp.Kind)
	}
	// The ownership is exclusive: beginning a new operation supersedes any
	// prior one (never accumulates workers).
	before := m.activeOp.ID
	m.beginOperation(OpHotfix)
	if m.activeOp == nil || m.activeOp.ID == before {
		t.Fatal("a new user action must own a fresh operation, not reuse the previous one")
	}
}
