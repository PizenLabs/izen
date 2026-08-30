package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes"
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

// ── 1. $hot inside /build executes immediately through the gateway ─────────

func TestPhase6HotInBuildExecutesImmediately(t *testing.T) {
	m := gatedDispatchModel(t, &mockProvider{responses: []*ai.Response{{Content: "x"}}}, nil)
	m.resolver.Set(modes.ModeBuild)

	cmd := m.handleInput("$hot add a README file @README.md")
	if cmd == nil {
		t.Fatal("$hot inside /build returned nil cmd — the execution request must dispatch immediately")
	}
	if got := m.resolver.Current(); got != modes.ModeBuild {
		t.Fatalf("mode = /%s, want /build (unchanged)", got)
	}
	if !hasDispatchRecord(m, "Resolving your request...") {
		t.Error("expected $hot to dispatch through the unified gateway")
	}
}

// ── 2 + 3. $hot outside /build does NOT transition modes; single dispatch ──

func TestPhase6HotOutsideBuildNoTransition(t *testing.T) {
	for _, start := range []modes.Mode{modes.ModeAsk, modes.ModePlan, modes.ModeReview} {
		m := gatedDispatchModel(t, &mockProvider{responses: []*ai.Response{{Content: "x"}}}, nil)
		m.resolver.Set(start)

		cmd := m.handleInput("$hot add a README file @README.md")
		if cmd == nil {
			t.Fatalf("from /%s: $hot returned nil cmd — execution request must dispatch", start)
		}
		// Modes are presentation contexts: $hot must NOT auto-transition.
		if got := m.resolver.Current(); got != start {
			t.Fatalf("from /%s: mode = /%s, want /%s (no mode transition — the gateway decides the path)", start, got, start)
		}
		// Single dispatch: one input → one gateway execution.
		if n := strings.Count(recordsText(m), "Resolving your request..."); n != 1 {
			t.Fatalf("from /%s: $hot dispatched %d times, want exactly 1 (single input → single execution)", start, n)
		}
	}
}

// TestPhase6HotFromAskSingleDispatch guards test 14 (no duplicate dispatch):
// a $hot action yields exactly one gateway execution, never a re-parse of the
// raw input through a second routing path.
func TestPhase6HotFromAskSingleDispatch(t *testing.T) {
	m := gatedDispatchModel(t, &mockProvider{responses: []*ai.Response{{Content: "x"}}}, nil)
	m.resolver.Set(modes.ModeAsk)

	m.handleInput("$hot add a LICENSE file @LICENSE")

	if n := strings.Count(recordsText(m), "Resolving your request..."); n != 1 {
		t.Fatalf("hotfix dispatched %d times, want exactly 1", n)
	}
}

// ── 4. $prompt is an execution request, not a message command ──────────────

func TestPhase6PromptNoModeTransition(t *testing.T) {
	m := gatedDispatchModel(t, &mockProvider{responses: []*ai.Response{{Content: "plan"}}}, nil)
	m.resolver.Set(modes.ModeBuild)

	cmd := m.handleInput("$prompt design a plugin architecture")
	if cmd == nil {
		t.Fatal("$prompt returned nil cmd — the execution request must fire")
	}
	// $prompt is an execution request: it must NOT transition to /ask.
	if got := m.resolver.Current(); got != modes.ModeBuild {
		t.Fatalf("mode = /%s, want /build (no transition — $prompt is an execution request)", got)
	}
	if !hasDispatchRecord(m, "Resolving your request...") {
		t.Error("expected $prompt to dispatch through the unified gateway")
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

func TestPhase6QuestionMarkEmptyInputOpensHelp(t *testing.T) {
	m := initializedChatModel(t)
	m.ti.Focus()
	m.ti.SetValue("")

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if !m.showHelpOverlay {
		t.Fatal("'?' on an empty input must open the help overlay, not type text")
	}
	if got := m.ti.Value(); got != "" {
		t.Fatalf("input value = %q, want empty (the '?' must not be inserted)", got)
	}

	// A second '?' on the still-empty input closes the overlay.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if m.showHelpOverlay {
		t.Fatal("second '?' should close the help overlay")
	}
}

func TestPhase6QuestionMarkWithTextStillTypes(t *testing.T) {
	m := initializedChatModel(t)
	m.ti.Focus()
	m.ti.SetValue("hello")

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if m.showHelpOverlay {
		t.Fatal("'?' with non-empty input must NOT open help")
	}
	if got := m.ti.Value(); got != "hello?" {
		t.Fatalf("input value = %q, want %q", got, "hello?")
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
	m := gatedDispatchModel(t, &mockProvider{responses: []*ai.Response{{Content: "Copyright 2026\nAll rights reserved.\n"}}}, map[string]string{
		"LICENSE": "Copyright 2023\nAll rights reserved.\n",
	})
	m.resolver.Set(modes.ModeBuild)

	cmd := m.handleInput("$hot update the year to 2026 @LICENSE")
	if cmd == nil {
		t.Fatal("$hot with a resolved target returned nil cmd")
	}
	// No ambiguity stop: the execution request dispatched immediately.
	if m.state == StateHotfixAmbiguous {
		t.Fatal("a resolved target must not enter the ambiguity card")
	}
	if !hasDispatchRecord(m, "Resolving your request...") {
		t.Error("expected $hot to dispatch through the unified gateway")
	}
}

// ── 9. A confirmation-required action stops correctly ─────────────────────
// (the executor approval gate holds the confirmation; the proposal dock stages
// the reviewable diff — see runtime_cutover_test.go and gateway.go)

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
	if err := os.WriteFile(target, []byte("<html><body><p>old</p></body></html>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mock := &mockProvider{responses: []*ai.Response{{
		Content: "<<<<<<< SEARCH\n<p>old</p>\n=======\n<p>new</p>\n>>>>>>>",
		Usage:   ai.ProviderUsage{Known: true},
	}}}
	m := newTestModel()
	m.provider = mock
	m.gateway = execution.NewIntentGateway(".")
	m.executor = execution.NewRuntimeExecutor(".", m.cfg, mock, nil, "")
	m.resolver.Set(modes.ModeBuild)

	// Dispatch the mutation through the executor and stage it at the approval
	// gate.
	cmd := m.handleInput("$hot remove redundant content from @index.html")
	if cmd == nil {
		t.Fatal("granted hotfix must dispatch")
	}
	gem := extractGatedExecutionMsg(t, cmd)
	if gem.err != nil {
		t.Fatalf("executor failed: %v", gem.err)
	}
	res, _ := m.Update(gem)
	m2 := res.(*model)

	// Reject the held proposal (Esc) — the operation terminates and releases
	// ownership; no mutation is applied.
	res, _ = m2.handleKey(tea.KeyMsg{Type: tea.KeyEscape})
	m3 := res.(*model)
	if m3.executorPendingPatchID != "" {
		t.Fatal("reject must clear the held patch")
	}
	if m3.activeOp != nil {
		t.Fatal("reject must release operation ownership")
	}
	onDisk, rerr := os.ReadFile(target)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if !strings.Contains(string(onDisk), "<p>old</p>") {
		t.Fatal("a rejected proposal must not mutate the file")
	}
}

// ── 13. The next command is accepted immediately after a terminal state ───

func TestPhase6NextCommandAcceptedAfterTerminalState(t *testing.T) {
	m := gatedDispatchModel(t, &mockProvider{responses: []*ai.Response{{Content: "x"}}}, nil)
	m.resolver.Set(modes.ModeBuild)

	// Terminal state: a hotfix apply completed → StateChat, input focused.
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
	if !hasDispatchRecord(m2, "Resolving your request...") {
		t.Error("the next command did not execute after the terminal state")
	}
}

// ── 14 + 15. No duplicate dispatch; one action → one ownership ────────────

func TestPhase6OneActionOneOwnership(t *testing.T) {
	m := gatedDispatchModel(t, &mockProvider{responses: []*ai.Response{{Content: "x"}}}, map[string]string{
		"LICENSE": "Copyright 2023\nAll rights reserved.\n",
	})
	m.resolver.Set(modes.ModeBuild)

	// Dispatch the execution request, run it through the runtime, and project
	// the approval-gate result — the operation begins at the proposal terminal,
	// owned by exactly one OpHotfix.
	cmd := m.handleInput("$hot add a LICENSE file @LICENSE")
	if cmd == nil {
		t.Fatal("$hot dispatch returned nil cmd")
	}
	gem := extractGatedExecutionMsg(t, cmd)
	if gem.err != nil {
		t.Fatalf("gate err: %v", gem.err)
	}
	if gem.res == nil {
		t.Fatal("nil gate result")
	}
	res, _ := m.executionResultUpdate(executionResultMsg{res: gem.res})
	m2 := res.(*model)
	if m2.activeOp == nil || m2.activeOp.Kind != OpHotfix {
		t.Fatalf("expected exactly one hotfix operation at the proposal terminal, got %+v", m2.activeOp)
	}
	// The ownership is exclusive: beginning a new operation supersedes any
	// prior one (never accumulates workers).
	before := m2.activeOp.ID
	m2.beginOperation(OpHotfix)
	if m2.activeOp == nil || m2.activeOp.ID == before {
		t.Fatal("a new user action must own a fresh operation, not reuse the previous one")
	}
}
