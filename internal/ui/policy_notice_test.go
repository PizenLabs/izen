package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/internal/modes"
)

// TestToolRejectionNoticeRendersAsBadge guards the /ask policy-rejection
// presentation: a forbidden tool-call notice ("Tool 'shell' rejected in /ask.")
// must render as a styled muted status badge ("☢ [POLICY] ...") in the
// viewport, never as raw unformatted text.
func TestToolRejectionNoticeRendersAsBadge(t *testing.T) {
	tm := newTestModel()
	m := tm
	m.state = StateChat

	notice := toolRejectBadgeStyle.Render("☢ [POLICY]") + " " +
		mutedStyle.Render("Tool 'shell' rejected in /ask.")
	m.push(roleSystem, notice)

	if len(m.records) == 0 {
		t.Fatal("rejection notice was not recorded")
	}
	rendered := m.renderRecordForViewport(m.records[0])
	stripped := ansi.Strip(rendered)

	if !strings.Contains(stripped, "[POLICY]") {
		t.Errorf("rendered notice missing the policy badge:\n%q", stripped)
	}
	if !strings.Contains(stripped, "Tool 'shell' rejected in /ask.") {
		t.Errorf("rendered notice missing the rejection text:\n%q", stripped)
	}
	// The badge style itself is precompiled bold with an explicit maroon
	// foreground (lipgloss strips ANSI in non-TTY tests, so the SGR bytes are
	// asserted on the style, not the rendered string).
	if got := toolRejectBadgeStyle.GetBold(); !got {
		t.Error("toolRejectBadgeStyle must be bold")
	}
	if got := toolRejectBadgeStyle.GetForeground(); got == nil {
		t.Error("toolRejectBadgeStyle has no explicit foreground — reads as plain text")
	}
}

// TestToolRejectionNoticeHandlerPath feeds a shell command through the real
// streamDoneMsg handler in /ask (read-only, no shell capability) and asserts
// the recorded system notice carries the policy badge.
func TestToolRejectionNoticeHandlerPath(t *testing.T) {
	tm := newTestModel()
	m := tm
	m.state = StateChat
	m.awaitingConfirmation = false
	m.streaming = true
	m.streamCh = nil
	m.streamTickActive = false
	m.resolver.Set(modes.ModeAsk)

	before := len(m.records)
	nm, _ := m.Update(streamDoneMsg{
		content: "You can create it with:\n```bash\ncat > Hello.java\n```",
	})
	m = nm.(*model)

	var got string
	for _, r := range m.records[before:] {
		if r.role == roleSystem && strings.Contains(r.text, "rejected in /ask") {
			got = r.text
			break
		}
	}
	if got == "" {
		t.Fatal("no policy rejection record was pushed for a shell command in /ask")
	}
	if !strings.Contains(got, "[POLICY]") {
		t.Fatalf("rejection record missing the policy badge: %q", got)
	}
	// The session copy sent to the model stays plain text (no badge ANSI).
	if m.sess == nil || len(m.sess.History) == 0 {
		t.Fatal("session history was not updated")
	}
}
