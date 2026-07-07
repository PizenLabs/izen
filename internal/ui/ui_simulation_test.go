package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/modes"
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
	// But the tick chain must continue (for when state changes)
	if cmd == nil {
		t.Fatal("tickMsg in StateChat returned nil cmd — chain broken")
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

	newModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m2 := newModel.(*model)

	if m2.state != StateProcessing {
		t.Errorf("after 'a': state = %v, want StateProcessing", m2.state)
	}
	if cmd == nil {
		t.Fatal("after 'a': cmd is nil — applySingleProposal not triggered")
	}
}

// ── Test 4: Key P toggle (BUG — broken without fix) ──────────────────────

func TestKeyPToggleProposal(t *testing.T) {
	m := newTestModel()
	m.pendingProposals[0].Expanded = false

	// Press 'p' in StateAwaitingApproval
	newModel, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m2 := newModel.(*model)

	if !m2.pendingProposals[0].Expanded {
		t.Error("'p' did NOT toggle Expanded — handleKey StateAwaitingApproval block missing p/P case")
	}
	_ = cmd
}

// ── Test 5: Key R → StateChat, proposals cleared ─────────────────────────

func TestKeyRRejectsProposal(t *testing.T) {
	m := newTestModel()

	newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m2 := newModel.(*model)

	if m2.state != StateChat {
		t.Errorf("after 'r': state = %v, want StateChat", m2.state)
	}
	if len(m2.pendingProposals) != 0 {
		t.Errorf("after 'r': %d pending proposals remain, want 0", len(m2.pendingProposals))
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
	// height=40 - inputHeight(3) - statusLineHeight(1) - dockHeight(13) = 23
	vpHeight := m.computeVpHeight()
	expectVp := 23
	if vpHeight != expectVp {
		t.Errorf("computeVpHeight = %d, want %d", vpHeight, expectVp)
	}

	// Render the proposal block and count lines
	proposalBlock := m.renderProposalBlock()
	proposalLines := len(strings.Split(proposalBlock, "\n"))
	t.Logf("Proposal block rendered %d lines (vpHeight=%d)", proposalLines, vpHeight)

	// The View() output total should not exceed m.height
	fullView := m.View()
	viewLines := len(strings.Split(fullView, "\n"))
	if viewLines > m.height {
		t.Errorf("View() output = %d lines, exceeds terminal height of %d — content clipped past boundary",
			viewLines, m.height)
	}
}
