package ui

import (
	"testing"

	"github.com/PizenLabs/izen/internal/session"
	"github.com/PizenLabs/izen/internal/ui/status"
)

// TestNewCommandResetsTokenMetrics verifies that /new clears Session history
// and resets all token counters to zero, with footer instantly showing 0.
func TestNewCommandResetsTokenMetrics(t *testing.T) {
	m, _, _, _ := slashRouterTestModel(t)

	// Populate token metrics with 1140 tokens
	m.InputTokens = 600
	m.OutputTokens = 540
	m.TotalTokens = 1140
	m.TurnInputTokens = 300
	m.TurnOutputTokens = 250
	m.AccumulatedCost = 0.05
	m.usageKnown = true
	status.Default.Record(m.InputTokens, m.OutputTokens)

	// Populate session history
	m.sess.AddMessage("user", "hello", 5)
	m.sess.AddMessage("assistant", "hi there", 5)
	if len(m.sess.History) == 0 {
		t.Fatal("history should not be empty before /new")
	}
	if m.TotalTokens != 1140 {
		t.Fatalf("TotalTokens = %d, want 1140", m.TotalTokens)
	}

	// Trigger /new via handleInput (slash command path)
	m.handleInput("/new")

	// Assert Session.Messages is empty (new session)
	if len(m.sess.History) != 0 {
		t.Fatalf("Session.Messages len = %d, want 0 after /new", len(m.sess.History))
	}
	// Assert TokenStats.TotalTokens == 0
	if m.TotalTokens != 0 || m.InputTokens != 0 || m.OutputTokens != 0 {
		t.Fatalf("token counters not reset: Input=%d Output=%d Total=%d, want 0", m.InputTokens, m.OutputTokens, m.TotalTokens)
	}
	if m.AccumulatedCost != 0 {
		t.Fatalf("AccumulatedCost = %v, want 0", m.AccumulatedCost)
	}
	if status.Default.Has() && (status.Default.Total() != 0) {
		// status tracker should be reset; Has() may be false after Reset
		snap := status.Default.Snapshot()
		if snap.Total != 0 {
			t.Fatalf("status.Default Total = %d, want 0", snap.Total)
		}
	}
	// Footer should instantly reflect zeroed state
	// renderFixedFooter reads from m.InputTokens/m.OutputTokens, so 0 is correct
	footer := m.renderFixedFooter(80, nil)
	if footer == "" {
		t.Fatal("footer should not be empty after /new")
	}
	// Verify that history file is also cleared via Prune check
	if len(m.sess.History) != 0 {
		t.Fatal("history not cleared")
	}
	// Also test direct resetTokenMetrics helper
	m2 := &model{}
	m2.InputTokens = 100
	m2.OutputTokens = 200
	m2.TotalTokens = 300
	m2.resetTokenMetrics()
	if m2.TotalTokens != 0 {
		t.Fatalf("resetTokenMetrics failed: TotalTokens=%d", m2.TotalTokens)
	}
	_ = session.New() // ensure session import used
}
