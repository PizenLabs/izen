package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/session"
)

// newTestModel builds a minimal model in StateAwaitingApproval.
// All fields that could panic on nil are zeroed safely; callers that
// need cfg/sess/execEng must set them before the relevant message flow.
func newTestModel() *model {
	resolver := modes.NewResolver()
	resolver.Set(modes.ModeBuild)

	ti := textinput.New()
	ti.Prompt = ""
	ti.CharLimit = 0

	vp := viewport.New(100, 20)

	return &model{
		width:    100,
		height:   40,
		resolver: resolver,
		ledger:   NewContextLedger(),
		ti:       ti,
		sess:     &session.Session{},
		cfg: &config.Config{
			AI: config.AIConfig{
				DefaultProvider: "ollama",
				Providers: map[string]config.AIProviderConfig{
					"ollama": {DefaultModel: "qwen2.5-coder:7b"},
				},
			},
		},
		showBanner: false,
		state:      StateAwaitingApproval,
		Ready:      true,
		Viewport:   vp,
		pendingProposals: []SemanticProposal{
			{
				ID:       "test-1",
				Target:   SemanticTarget{QualifiedName: "test.go"},
				Diff:     "--- a/test.go\n+++ b/test.go\n@@ -1 +1 @@\n-old\n+new\n",
				Expanded: true,
			},
		},
		awaitingConfirmation: true,
		spinnerFrame:         0,
		PreRenderedHistory:   "previous content\n",
		viewRegistry: func() *Registry {
			r := NewRegistry()
			r.Register(modes.ModeAsk, askView{})
			r.Register(modes.ModePlan, planView{})
			r.Register(modes.ModeBuild, buildView{})
			r.Register(modes.ModeInvestigate, investigateView{})
			r.Register(modes.ModeReview, reviewView{})
			return r
		}(),
	}
}

// ── Test 1: tickMsg keeps the spinner chain alive ────────────────────────

func TestSpinnerTickInStateAwaitingApproval(t *testing.T) {
	m := newTestModel()
	initialFrame := m.spinnerFrame

	newModel, cmd := m.Update(tickMsg(time.Now()))
	m2 := newModel.(*model)

	expectFrame := (initialFrame + 1) % len(ProposalSpinnerFrames)
	if m2.spinnerFrame != expectFrame {
		t.Errorf("spinnerFrame = %d, want %d", m2.spinnerFrame, expectFrame)
	}
	if cmd == nil {
		t.Fatal("tickMsg returned nil cmd — spinner tick chain broken")
	}
}

func TestSpinnerTickInStateChat(t *testing.T) {
	m := newTestModel()
	m.state = StateChat
	m.awaitingConfirmation = false
	m.pendingProposals = nil
	initialFrame := m.spinnerFrame

	newModel, cmd := m.Update(tickMsg(time.Now()))
	m2 := newModel.(*model)

	// In StateChat the frame should NOT advance (no animation needed)
	if m2.spinnerFrame != initialFrame {
		t.Errorf("spinnerFrame advanced in StateChat: %d → %d", initialFrame, m2.spinnerFrame)
	}
	// The tick chain should stop when idle to prevent CPU spinning.
	// State changes are driven by other messages, not tickMsg.
	if cmd != nil {
		t.Fatal("tickMsg in idle StateChat should return nil cmd — tick loop stops when idle")
	}
}

// ── Test 2: streamDoneMsg → StateAwaitingApproval ────────────────────────

func TestStreamDoneMsgTransitionsToStateAwaitingApproval(t *testing.T) {
	m := newTestModel()
	m.state = StateChat
	m.awaitingConfirmation = false
	m.currentPrompt = "" // skip session persist path
	m.currentStreamContent = "FILE: fix.go\n```go\npackage main\nfunc main() {}\n```"

	newModel, cmd := m.Update(streamDoneMsg{
		content:     m.currentStreamContent,
		tokenInput:  10,
		tokenOutput: 20,
	})
	m2 := newModel.(*model)

	if len(m2.pendingProposals) == 0 {
		t.Fatal("streamDoneMsg produced 0 proposals — extractBuildProposals may be failing")
	}
	if m2.state != StateAwaitingApproval {
		t.Errorf("state = %v, want StateAwaitingApproval", m2.state)
	}
	if m2.awaitingConfirmation != true {
		t.Error("awaitingConfirmation should be true after streamDoneMsg with proposals")
	}
	if m2.ti.Focused() {
		t.Error("textinput should be blurred in StateAwaitingApproval")
	}
	_ = cmd // streamDoneMsg returns nil cmd by design
}

// ── Test 3: Key A → StateProcessing + apply command ──────────────────────

func TestKeyAToAcceptProposal(t *testing.T) {
	m := newTestModel()

	newModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}, Alt: true})
	m2 := newModel.(*model)

	if m2.state != StateProcessing {
		t.Errorf("after 'alt+a': state = %v, want StateProcessing", m2.state)
	}
	if cmd == nil {
		t.Fatal("after 'alt+a': cmd is nil — applySingleProposal not triggered")
	}
}

// ── Test 4: Key P toggle (BUG — broken without fix) ──────────────────────

func TestKeyPToggleProposal(t *testing.T) {
	m := newTestModel()
	m.pendingProposals[0].Expanded = false

	// Press Alt+P in StateAwaitingApproval
	newModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}, Alt: true})
	m2 := newModel.(*model)

	if !m2.pendingProposals[0].Expanded {
		t.Error("'alt+p' did NOT toggle Expanded — handleKey StateAwaitingApproval block missing alt+p case")
	}
	_ = cmd
}

// ── Test 5: Key R → StateChat, proposals cleared ─────────────────────────

func TestKeyRRejectsProposal(t *testing.T) {
	m := newTestModel()

	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}, Alt: true})
	m2 := newModel.(*model)

	if m2.state != StateChat {
		t.Errorf("after 'alt+r': state = %v, want StateChat", m2.state)
	}
	if len(m2.pendingProposals) != 0 {
		t.Errorf("after 'alt+r': %d pending proposals remain, want 0", len(m2.pendingProposals))
	}
	if !m2.ti.Focused() {
		t.Error("textinput should be focused after rejection back to Chat")
	}
}

// ── Test 6: smoothStreamTickMsg stops when buffer drains (spinner freeze) ─

func TestSmoothStreamTickKeepsStreamingAlive(t *testing.T) {
	m := newTestModel()
	m.state = StateChat
	m.streaming = true
	m.streamBuffer = "" // empty buffer — LLM thinking pause
	m.streamTickActive = false

	// When streaming is active, smooth tick must keep refreshing the
	// viewport even if the buffer is empty, so the spinner animates.
	newModel, cmd := m.Update(smoothStreamTickMsg(time.Now()))
	m2 := newModel.(*model)

	if cmd == nil {
		t.Error("smoothStreamTickMsg returned nil while streaming=true — spinner freezes during LLM pauses")
	}
	if !m2.streamTickActive {
		t.Error("streamTickActive should be true while streaming=true")
	}
}

// ── Test 7: Viewport height overflow detection ──────────────────────────

func TestComputeVpHeightWithProposalBlock(t *testing.T) {
	m := newTestModel()

	// computeVpHeight uses the new zero-gap formula:
	// height=40 - inputHeight(2) - statusLineHeight(1) - dockHeight(10) - bottomSep(1) = 26
	vpHeight := m.computeVpHeight()
	expectVp := 26
	if vpHeight != expectVp {
		t.Errorf("computeVpHeight = %d, want %d", vpHeight, expectVp)
	}

	// Render the proposal block and count lines
	proposalBlock := m.renderProposalBlock()
	proposalLines := len(strings.Split(strings.TrimRight(proposalBlock, "\n"), "\n"))
	t.Logf("Proposal block rendered %d lines (vpHeight=%d)", proposalLines, vpHeight)

	// The View() output total should not exceed m.height
	fullView := m.View()
	viewLines := len(strings.Split(strings.TrimRight(fullView, "\n"), "\n"))
	if viewLines > m.height {
		t.Errorf("View() output = %d lines, exceeds terminal height of %d — content clipped past boundary",
			viewLines, m.height)
	}
}

// ── Test 8-11: Alt-modifier enforcement — raw y/n/a must be NO-OP ──────
//
// These tests verify the unified Alt-modifier-only keybinding policy:
// bare y/Y/a/A/n/N key presses MUST be silently ignored (return nil cmd,
// no state transitions) across all approval gates.
//
// Rationale: single-character y/n/a clashed with Alt+ shortcuts in Tmux/
// Ghostty and could accidentally catch buffer-leaked stdin during shell
// execution, bypassing safety guardrails.
// See https://github.com/PizenLabs/izen/issues/... for design discussion.

func TestKeyYIsNoOpInProposalMode(t *testing.T) {
	m := newTestModel()
	m.state = StateAwaitingApproval
	m.awaitingConfirmation = true
	m.pendingProposals[0].Expanded = true

	// Press raw 'y' (no Alt modifier) while proposals are pending
	newModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m2 := newModel.(*model)

	if m2.state != StateAwaitingApproval {
		t.Errorf("raw 'y' changed state to %v, want StateAwaitingApproval (NO-OP)", m2.state)
	}
	if len(m2.pendingProposals) == 0 {
		t.Error("raw 'y' cleared pendingProposals — should be NO-OP")
	}
	if cmd != nil {
		t.Errorf("raw 'y' returned non-nil cmd — must be NO-OP (got %T)", cmd)
	}
}

func TestKeyNIsNoOpInProposalMode(t *testing.T) {
	m := newTestModel()
	m.state = StateAwaitingApproval
	m.awaitingConfirmation = true
	m.pendingProposals[0].Expanded = true

	// Press raw 'n' (no Alt modifier) while proposals are pending
	newModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m2 := newModel.(*model)

	if m2.state != StateAwaitingApproval {
		t.Errorf("raw 'n' changed state to %v, want StateAwaitingApproval (NO-OP)", m2.state)
	}
	if len(m2.pendingProposals) == 0 {
		t.Error("raw 'n' cleared pendingProposals — should be NO-OP")
	}
	if cmd != nil {
		t.Errorf("raw 'n' returned non-nil cmd — must be NO-OP (got %T)", cmd)
	}
}

func TestKeyYIsNoOpInBuildApproval(t *testing.T) {
	m := newTestModel()
	m.pendingProposals = nil
	m.state = StateAwaitingApproval
	m.pendingBuildApproval = true
	m.pendingBuildTask = &plan.Task{StepNum: 1, Type: "SHELL_EXEC", Target: "go vet ./..."}

	// Press raw 'y' (no Alt modifier) during SHELL_EXEC approval
	newModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m2 := newModel.(*model)

	if !m2.pendingBuildApproval {
		t.Error("raw 'y' cleared pendingBuildApproval — should be NO-OP")
	}
	if m2.state != StateAwaitingApproval {
		t.Errorf("raw 'y' changed state to %v, want StateAwaitingApproval (NO-OP)", m2.state)
	}
	if cmd != nil {
		t.Errorf("raw 'y' returned non-nil cmd — must be NO-OP (got %T)", cmd)
	}
}

func TestKeyNIsNoOpInBuildApproval(t *testing.T) {
	m := newTestModel()
	m.pendingProposals = nil
	m.state = StateAwaitingApproval
	m.pendingBuildApproval = true
	m.pendingBuildTask = &plan.Task{StepNum: 1, Type: "SHELL_EXEC", Target: "go vet ./..."}

	// Press raw 'n' (no Alt modifier) during SHELL_EXEC approval
	newModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m2 := newModel.(*model)

	if !m2.pendingBuildApproval {
		t.Error("raw 'n' cleared pendingBuildApproval — should be NO-OP")
	}
	if m2.state != StateAwaitingApproval {
		t.Errorf("raw 'n' changed state to %v, want StateAwaitingApproval (NO-OP)", m2.state)
	}
	if cmd != nil {
		t.Errorf("raw 'n' returned non-nil cmd — must be NO-OP (got %T)", cmd)
	}
}

func TestKeyAIsNoOpInBuildApproval(t *testing.T) {
	m := newTestModel()
	m.pendingProposals = nil
	m.state = StateAwaitingApproval
	m.pendingBuildApproval = true
	m.pendingBuildTask = &plan.Task{StepNum: 1, Type: "SHELL_EXEC", Target: "go vet ./..."}

	// Press raw 'a' (no Alt modifier, used to mean "Allow Always") during SHELL_EXEC approval
	newModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m2 := newModel.(*model)

	if m2.pendingBuildAllowAlways {
		t.Error("raw 'a' set pendingBuildAllowAlways — should be NO-OP")
	}
	if !m2.pendingBuildApproval {
		t.Error("raw 'a' cleared pendingBuildApproval — should be NO-OP")
	}
	if cmd != nil {
		t.Errorf("raw 'a' returned non-nil cmd — must be NO-OP (got %T)", cmd)
	}
}

func TestKeyAIsNoOpInHotfixApproval(t *testing.T) {
	m := newTestModel()
	m.pendingProposals = nil
	m.state = StateAwaitingApproval
	m.pendingHotfixTask = &plan.Task{StepNum: 1, Type: "FILE", Target: "fix.go"}
	m.pendingHotfixPatch = &execution.Patch{File: "fix.go"}

	// Press raw 'a' (no Alt modifier) during hotfix approval
	newModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m2 := newModel.(*model)

	if m2.pendingHotfixTask == nil {
		t.Error("raw 'a' cleared pendingHotfixTask — should be NO-OP")
	}
	if m2.state != StateAwaitingApproval {
		t.Errorf("raw 'a' changed state to %v, want StateAwaitingApproval (NO-OP)", m2.state)
	}
	if cmd != nil {
		t.Errorf("raw 'a' returned non-nil cmd — must be NO-OP (got %T)", cmd)
	}
}

func TestAltAStillWorksInProposalMode(t *testing.T) {
	m := newTestModel()

	// Press Alt+A — must still accept and transition to StateProcessing
	newModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}, Alt: true})
	m2 := newModel.(*model)

	if m2.state != StateProcessing {
		t.Errorf("Alt+A: state = %v, want StateProcessing", m2.state)
	}
	if cmd == nil {
		t.Fatal("Alt+A: cmd is nil — applySingleProposal not triggered")
	}
}

func TestAltRStillWorksInProposalMode(t *testing.T) {
	m := newTestModel()

	// Press Alt+R — must still reject and return to chat
	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}, Alt: true})
	m2 := newModel.(*model)

	if m2.state != StateChat {
		t.Errorf("Alt+R: state = %v, want StateChat", m2.state)
	}
	if len(m2.pendingProposals) != 0 {
		t.Errorf("Alt+R: %d pending proposals remain, want 0", len(m2.pendingProposals))
	}
	if !m2.ti.Focused() {
		t.Error("Alt+R: textinput should be focused after rejection")
	}
}

// TestSanitizeFileOutputFences is the regression guard for the $hot markdown
// fence bug: the file content cleanse must strip a wrapping code block (with or
// without a language identifier) so literal triple backticks never reach disk.
func TestSanitizeFileOutputFences(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "mit fence",
			input: "```mit\nMIT License\n\nCopyright (c) 2024\n```",
			want:  "MIT License\n\nCopyright (c) 2024",
		},
		{
			name:  "go fence",
			input: "```go\npackage main\n\nfunc main() {}\n```",
			want:  "package main\n\nfunc main() {}",
		},
		{
			name:  "bare fence",
			input: "```\nhello\n```",
			want:  "hello",
		},
		{
			name:  "no fence passthrough",
			input: "MIT License\n\nCopyright (c) 2024",
			want:  "MIT License\n\nCopyright (c) 2024",
		},
		{
			name:  "surrounding whitespace trimmed",
			input: "\n\n```text\npayload\n```\n\n",
			want:  "payload",
		},
	}
	for _, c := range cases {
		got := sanitizeFileOutput(c.input)
		if got != c.want {
			t.Errorf("%s: sanitizeFileOutput =\n %q\nwant\n %q", c.name, got, c.want)
		}
	}
}
