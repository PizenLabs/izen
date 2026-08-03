package ui

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/session"
)

// TestPrimeHandoffFromLedger_DoesNotDuplicateRawIntent is the regression guard
// for the /investigate → /plan payload bloat: when the raw intent text is
// already embedded in the handoff payload (it heads the formatted investigation
// diagnostics AND the problem packet), it MUST NOT be appended a third time as
// "### USER RAW INTENT".
func TestPrimeHandoffFromLedger_DoesNotDuplicateRawIntent(t *testing.T) {
	const rawIntent = "rewrite a personal profile website to use semantic HTML5"

	m := &model{
		sess: &session.Session{},
	}
	l := session.NewContextLedger(modes.ModeInvestigate)
	l.UserRawIntent = rawIntent
	l.Diagnostics = rawIntent + "\nROOT: profile.html uses div soup\nDONE: confirmed duplicate DOM nodes"
	l.InjectPacket(session.LedgerPacket{Kind: "problem", Payload: rawIntent})
	l.InjectPacket(session.LedgerPacket{Kind: "target", File: "index.html", Line: 12})
	l.InjectPacket(session.LedgerPacket{Kind: "conclusion", Payload: "confirmed duplicate DOM nodes"})
	m.sess.SetContextLedger(l)

	m.primeHandoffFromLedger(modes.ModePlan)

	// The intent legitimately heads the formatted diagnostics AND the problem
	// packet (two structural roles), so the guard is: no THIRD copy via the
	// USER RAW INTENT header, and the LLM-boundary compressor collapses the
	// identical lines to a single occurrence.
	if strings.Contains(m.handoffLedgerContent, "### USER RAW INTENT") {
		t.Fatalf("USER RAW INTENT header appended despite intent already present:\n%s", m.handoffLedgerContent)
	}
	compressed := compressHandoffSource(m.handoffLedgerContent, 10_000)
	if got := strings.Count(compressed, rawIntent); got != 1 {
		t.Fatalf("raw intent appears %d times in LLM-bound payload, want 1:\n%s", got, compressed)
	}
}

// TestPrimeHandoffFromLedger_InjectsRawIntentWhenAbsent verifies the dedup gate
// still appends the raw intent when it is genuinely missing from the payload.
func TestPrimeHandoffFromLedger_InjectsRawIntentWhenAbsent(t *testing.T) {
	m := &model{
		sess: &session.Session{},
	}
	l := session.NewContextLedger(modes.ModeInvestigate)
	l.UserRawIntent = "migrate the api server to fastify"
	l.Diagnostics = "ROOT: express app in server.js"
	l.InjectPacket(session.LedgerPacket{Kind: "conclusion", Payload: "migrate server.js to fastify"})
	m.sess.SetContextLedger(l)

	m.primeHandoffFromLedger(modes.ModePlan)

	if !strings.Contains(m.handoffLedgerContent, "### USER RAW INTENT") {
		t.Fatalf("USER RAW INTENT header missing:\n%s", m.handoffLedgerContent)
	}
	if !strings.Contains(m.handoffLedgerContent, "migrate the api server to fastify") {
		t.Fatalf("raw intent not injected:\n%s", m.handoffLedgerContent)
	}
}

// TestThinkingStyleIsDimGray asserts the reasoning style is a genuine dim/muted
// gray (not reliant on the Faint attribute alone, which some terminals ignore).
func TestThinkingStyleIsDimGray(t *testing.T) {
	if !thinkingStyle.GetFaint() || !thinkingStyle.GetItalic() {
		t.Fatalf("thinkingStyle = faint=%v italic=%v, want both true",
			thinkingStyle.GetFaint(), thinkingStyle.GetItalic())
	}
	if got := thinkingStyle.GetForeground(); got == nil {
		t.Error("thinkingStyle has no explicit foreground color — dim look depends on Faint support")
	}
}

// TestThinkingBufferCompactThoughtUsesThinkingStyle guards the
// "▸ Thought for Xs (N tokens)" compact summary line: it must render through
// the dimmed reasoning style, never the normal answer style.
func TestThinkingBufferCompactThoughtUsesThinkingStyle(t *testing.T) {
	tb := NewThinkingBuffer()
	tb.Append("analyze failure")
	tb.MarkComplete()

	renderFn := func() string {
		return tb.Render(80, true, "✦")
	}
	out := renderFn()
	if !strings.Contains(out, "Thought for") {
		t.Fatalf("compact line missing Thought for: %q", out)
	}
	if !strings.Contains(out, "tokens)") {
		t.Fatalf("compact line missing token count: %q", out)
	}
	if !thinkingStyle.GetFaint() {
		t.Error("compact thought line must carry the dim reasoning style")
	}
}
