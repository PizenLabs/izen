package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/PizenLabs/izen/internal/session"
)

// TestFirstTurnTitleIsHumanReadable verifies Stage 1 derives a deterministic
// title from the first prompt and persists it to the active slot so the Session
// Manager never surfaces a raw timestamp id.
func TestFirstTurnTitleIsHumanReadable(t *testing.T) {
	m, sm, _ := sessionCLITestModel(t)
	prompt := "Implement dual stage session titling pipeline"

	m.applyFirstTurnTitle(prompt)
	m.persistSession("test")

	want := session.SanitizeTitle(prompt)
	if m.sess.Title != want {
		t.Fatalf("Title = %q, want %q", m.sess.Title, want)
	}
	if m.sess.Title == m.sess.SessionID {
		t.Fatalf("Title must not equal the raw session id %q", m.sess.SessionID)
	}

	active := activeSlotInfo(t, sm)
	if active.Title != want {
		t.Fatalf("persisted SlotInfo.Title = %q, want %q", active.Title, want)
	}
}

// TestHandleInputSetsFirstTurnTitle verifies the production prompt path wires
// Stage 1 through handleInput's synchronous turn commit.
func TestHandleInputSetsFirstTurnTitle(t *testing.T) {
	m, sm, _ := sessionCLITestModel(t)
	prompt := "Refactor the title pipeline deterministically"
	_ = m.handleInput(prompt)

	want := session.SanitizeTitle(prompt)
	if m.sess.Title != want {
		t.Fatalf("Title after handleInput = %q, want %q", m.sess.Title, want)
	}
	if active := activeSlotInfo(t, sm); active.Title != want {
		t.Fatalf("persisted title = %q, want %q", active.Title, want)
	}
	if m.sess.Model == "" {
		t.Fatal("session model metadata was not recorded on the first turn")
	}
}

// TestFirstTurnTitleNeverClobbersManualRename verifies an explicit title wins.
func TestFirstTurnTitleNeverClobbersManualRename(t *testing.T) {
	m, _, _ := sessionCLITestModel(t)
	m.sess.Title = "Manual Title"
	m.applyFirstTurnTitle("some other prompt")
	if m.sess.Title != "Manual Title" {
		t.Fatalf("manual title clobbered: %q", m.sess.Title)
	}
}

// TestRefineTitleAppliesStageTwo verifies the async refinement result is
// normalized and persisted, and that a stale result is rejected.
func TestRefineTitleAppliesStageTwo(t *testing.T) {
	m, sm, _ := sessionCLITestModel(t)
	m.applyFirstTurnTitle("fix the flaky session picker test")
	m.titleRefiner = func(ctx context.Context, model, prompt string) (string, error) {
		return "\"Fix Flaky Picker Test\"", nil
	}

	cmd := m.refineTitleCmd()
	if cmd == nil {
		t.Fatal("refineTitleCmd returned nil with a wired refiner")
	}
	var refined sessionTitleRefinedMsg
	for _, msg := range drainCmds(t, cmd) {
		if r, ok := msg.(sessionTitleRefinedMsg); ok {
			refined = r
		}
	}
	if refined.title != "Fix Flaky Picker Test" {
		t.Fatalf("refined title = %q, want normalized", refined.title)
	}
	m.handleSessionTitleRefined(refined)
	if m.sess.Title != "Fix Flaky Picker Test" {
		t.Fatalf("Title after refinement = %q", m.sess.Title)
	}
	if active := activeSlotInfo(t, sm); active.Title != "Fix Flaky Picker Test" {
		t.Fatalf("persisted refined title = %q", active.Title)
	}

	// A stale result (different session id) must be ignored.
	m.sess.Title = m.titleHeuristic
	m.handleSessionTitleRefined(sessionTitleRefinedMsg{sessionID: "other", title: "Should Not Apply"})
	if m.sess.Title == "Should Not Apply" {
		t.Fatal("stale refinement must not retitle the session")
	}
}

// TestCleanRefinedTitleNormalizes covers the model output normalization.
func TestCleanRefinedTitleNormalizes(t *testing.T) {
	cases := map[string]string{
		"  Add auth guard  ":             "Add auth guard",
		"- Fix broken footer\nmore text": "Fix broken footer",
		"`Handle empty sessions`":        "Handle empty sessions",
	}
	for in, want := range cases {
		if got := cleanRefinedTitle(in); got != want {
			t.Errorf("cleanRefinedTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestResumeBriefingRendersMetrics verifies the resume briefing carries the
// slot, full goal, metrics, and active model, and is cleared for a new turn.
func TestResumeBriefingRendersMetrics(t *testing.T) {
	m, _, _ := sessionCLITestModel(t)
	m.width = 100

	dormant := session.New()
	dormant.SessionID = "sess-dormant-1"
	dormant.Title = "Keep the session picker responsive"
	dormant.Model = "gpt-4o"
	dormant.History = []session.Message{
		{Role: "user", Content: "Keep the session picker responsive"},
		{Role: "assistant", Content: "ok"},
	}
	m.setResumeBriefing(session.SlotA, dormant)

	if m.resumeBriefing == nil {
		t.Fatal("resume briefing not staged")
	}
	banner := m.renderResumeBriefing()
	if banner == "" {
		t.Fatal("resume briefing rendered empty")
	}
	for _, want := range []string{"resumed", "slot A", "Keep the session picker responsive", "tokens", "turn(s)", "gpt-4o"} {
		if !strings.Contains(banner, want) {
			t.Errorf("banner missing %q:\n%s", want, banner)
		}
	}
	m.clearResumeBriefing()
	if m.resumeBriefing != nil {
		t.Fatal("clearResumeBriefing did not drop the briefing")
	}
}

// TestSessionPickerDetailsPanelUpdatesOnNavigation verifies the static DETAILS
// preview re-derives from the highlighted row with no timer/ticker loop.
func TestSessionPickerDetailsPanelUpdatesOnNavigation(t *testing.T) {
	infos := []session.SlotInfo{
		{Slot: session.SlotA, Active: true, Title: "First session goal", Tokens: 14200, Turns: 4, Model: "gpt-4o", ContextWindow: 128000, LastPrompt: "do the thing"},
		{Slot: session.SlotB, Title: "Second session goal", Tokens: 500, Turns: 1, Model: "claude-sonnet", ContextWindow: 200000, LastPrompt: "another thing"},
	}
	sp := NewSessionPickerModal(infos)
	sp.SetSize(88, 18)
	sp.cursor = 0

	first := sp.renderDetailsPanel()
	for _, want := range []string{"DETAILS", "First session goal", "14,200", "128,000", "11%", "4 turns", "gpt-4o", "do the thing"} {
		if !strings.Contains(first, want) {
			t.Errorf("details missing %q:\n%s", want, first)
		}
	}
	for _, line := range strings.Split(first, "\n") {
		if lipgloss.Width(line) > sp.contentWidth() {
			t.Errorf("details line exceeds content width: %q", line)
		}
	}

	sp.cursor = 1
	second := sp.renderDetailsPanel()
	if strings.Contains(second, "First session goal") {
		t.Fatal("details panel did not update after navigation")
	}
	if !strings.Contains(second, "Second session goal") || !strings.Contains(second, "500") {
		t.Fatalf("details panel missing second row data:\n%s", second)
	}

	// The panel must be embedded in the full modal view at the standard size.
	view := sp.View()
	if !strings.Contains(view, "DETAILS") || !strings.Contains(view, "Second session goal") {
		t.Fatalf("modal view missing DETAILS preview:\n%s", view)
	}
}

// TestRunSessionResumeStagesBriefing verifies the resume handshake stages the
// orientation briefing from the dormant session's persisted metadata and never
// re-renders its legacy chat history.
func TestRunSessionResumeStagesBriefing(t *testing.T) {
	m, sm, _ := sessionCLITestModel(t)
	sm.Session().Title = "Resume the dormant session"
	sm.Session().Model = "qwen2.5-coder:7b"
	sm.Session().AddMessage("user", "resume the dormant session", 5)
	sm.Session().AddMessage("assistant", "legacy answer that must not be re-rendered", 5)
	if err := sm.Persist(context.Background()); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if _, err := sm.NewSession(context.Background()); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	m.sess = sm.Session()
	m.state = StateChat
	m.Ready = true
	m.width = 100

	if cmd := m.runSessionResumeCmd(session.SlotA); cmd != nil {
		_ = cmd
	}
	if sm.Active() != session.SlotA {
		t.Fatalf("active = %s, want A", sm.Active())
	}
	if m.resumeBriefing == nil {
		t.Fatal("resume briefing not staged")
	}
	if m.resumeBriefing.Title != "Resume the dormant session" {
		t.Fatalf("briefing title = %q", m.resumeBriefing.Title)
	}
	if m.resumeBriefing.Turns != 1 {
		t.Fatalf("briefing turns = %d, want 1", m.resumeBriefing.Turns)
	}
	// The briefing must be injected into the viewport chrome.
	prefix := m.viewportContentPrefixHeight()
	if prefix <= 0 {
		t.Fatal("resume briefing not counted in the viewport chrome prefix")
	}
	// Legacy chat history must not have been rendered.
	if strings.Contains(m.PreRenderedHistory, "legacy answer") {
		t.Fatal("legacy chat history was re-rendered into the viewport buffer")
	}
}

func activeSlotInfo(t *testing.T, sm *session.Manager) session.SlotInfo {
	t.Helper()
	for _, info := range sm.List(context.Background()) {
		if info.Active {
			return info
		}
	}
	t.Fatal("no active slot found")
	return session.SlotInfo{}
}
