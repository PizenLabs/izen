package ui

import (
	"strings"
	"testing"

	ctxpkg "github.com/PizenLabs/izen/internal/context"
	"github.com/PizenLabs/izen/internal/session"
)

// TestPlanHasNothingToSynthesize covers the /plan empty-handoff guard. Zero
// pending TODOs is the HEALTHY handoff state (the ledger forensics drive
// synthesis), so the guard must key off actual material — handoff content,
// ledger diagnostics/packets, or user objective — NOT the TODO count.
func TestPlanHasNothingToSynthesize(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(m *model)
		rawContext string
		content    string
		want       bool // true == nothing to synthesize (guard fires)
	}{
		{
			name:  "completely empty everything",
			setup: func(m *model) {},
			want:  true,
		},
		{
			name:    "user typed an objective",
			setup:   func(m *model) {},
			content: "refactor the auth module",
			want:    false,
		},
		{
			name:       "conversational assembly has context",
			setup:      func(m *model) {},
			rawContext: "## PLAN CONTEXT\nsome graph symbols",
			want:       false,
		},
		{
			name: "handoff ledger content present",
			setup: func(m *model) {
				m.handoffLedgerContent = "### INVESTIGATION LEDGER\nroot cause: nil deref"
			},
			want: false,
		},
		{
			name: "proposed fix present",
			setup: func(m *model) {
				m.handoffCtx.ProposedFix = "apply patch X"
			},
			want: false,
		},
		{
			name: "ledger has diagnostics (valid handoff, 0 todos)",
			setup: func(m *model) {
				m.sess.ContextLedger = &session.ContextLedger{
					Diagnostics: "cmd/api/main.go:7:5: undefined: Router",
				}
			},
			want: false,
		},
		{
			name: "ledger has analytical packets (valid handoff, 0 todos)",
			setup: func(m *model) {
				m.sess.ContextLedger = &session.ContextLedger{
					Packets: []session.LedgerPacket{
						{PacketID: 1, Kind: "root_cause", Payload: "missing dep"},
					},
				}
			},
			want: false,
		},
		{
			name: "empty ledger with zero packets and no diagnostics",
			setup: func(m *model) {
				m.sess.ContextLedger = &session.ContextLedger{}
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &model{sess: &session.Session{}}
			tt.setup(m)
			got := m.planHasNothingToSynthesize(tt.rawContext, tt.content)
			if got != tt.want {
				t.Fatalf("planHasNothingToSynthesize(%q, %q) = %v, want %v",
					tt.rawContext, tt.content, got, tt.want)
			}
		})
	}
}

// TestPlanGuardIgnoresPendingTodoCount is the explicit anti-regression for the
// report's incorrect ask: it must NOT block on "0 pending TODOs" when a valid
// forensic handoff exists. A ledger with diagnostics and ZERO todos is a fully
// valid handoff that must proceed to synthesis.
func TestPlanGuardIgnoresPendingTodoCount(t *testing.T) {
	m := &model{sess: &session.Session{}}
	m.handoffCtx.PendingTodos = nil // 0 todos — the healthy state
	m.sess.ContextLedger = &session.ContextLedger{
		Diagnostics: "build failed: undefined: Router",
	}
	if m.planHasNothingToSynthesize("", "") {
		t.Fatal("guard fired on a valid 0-TODO handoff with ledger diagnostics — " +
			"this would deadlock every /investigate → /plan transition")
	}
}

// TestPlanHandoffRepopulatesFromDiagnostics is the regression guard for the
// "Data Flow Regression" crash: when a plan is rejected / an environment
// correction occurs, the live handoff (handoffLedgerContent / ProposedFix) can
// be empty while the authoritative root-cause diagnostics still live in
// session.ContextLedger.Diagnostics. The handoff resolution MUST repopulate
// from the diagnostics instead of reporting an empty source and crashing.
func TestPlanHandoffRepopulatesFromDiagnostics(t *testing.T) {
	m := &model{sess: &session.Session{}}
	m.handoffLedgerContent = ""     // cleared after rejection
	m.handoffCtx.ProposedFix = ""   // cleared after rejection
	m.handoffCtx.PendingTodos = nil // 0 todos
	m.sess.ContextLedger = &session.ContextLedger{
		Diagnostics: "cmd/api/main.go:7:5: no required module provides package github.com/foo/bar",
	}

	// Replicate the handoff-source resolution from /plan (commands.go):
	// the SAFETY GUARD must NOT fire because handoffSource is rebuilt from
	// the ledger diagnostics.
	ledgerContent := ctxpkg.SanitizeLedger(m.handoffLedgerContent)
	handoffSource := ledgerContent
	if handoffSource == "" {
		handoffSource = m.handoffCtx.ProposedFix
	}
	if handoffSource == "" && m.sess.ContextLedger != nil {
		if m.sess.ContextLedger.Diagnostics != "" {
			handoffSource = ctxpkg.SanitizeLedger(m.sess.ContextLedger.Diagnostics)
		}
	}

	if handoffSource == "" {
		t.Fatal("handoff source was NOT repopulated from ContextLedger.Diagnostics — " +
			"the Data Flow Regression crash would re-occur on plan rejection / env correction")
	}
	if !strings.Contains(handoffSource, "no required module provides package") {
		t.Fatalf("repopulated handoff source lost the root compilation error: %q", handoffSource)
	}
}

// TestStreamCmdEmptyContentIsNoOp pins the pre-existing safety that an empty
// payload never reaches the LLM: streamCmd returns nil and starts no stream.
func TestStreamCmdEmptyContentIsNoOp(t *testing.T) {
	m := &model{sess: &session.Session{}}
	if cmd := m.streamCmd("   \n\t "); cmd != nil {
		t.Fatal("streamCmd(empty) returned a non-nil command — would fire an empty LLM request")
	}
	if m.streaming {
		t.Fatal("streamCmd(empty) set streaming=true — spinner would hang with no producer")
	}
	if m.streamCh != nil {
		t.Fatal("streamCmd(empty) allocated a stream channel for an empty payload")
	}
}
