package ui

import (
	"testing"

	statuscommand "github.com/PizenLabs/izen/internal/ui/commands"
)

func TestStatusCommandRendersWithoutResettingInteractionState(t *testing.T) {
	m := newTestModel()
	m.workspaceRoot = t.TempDir()
	m.currentResult = &Result{Actions: []Action{{ID: "keep-this-result"}}}
	m.lastApplyError = "keep this error"
	m.InputTokens = 11
	m.OutputTokens = 7
	m.sess.History = nil
	m.sess.Objective = "inspect workspace"

	cmd := m.handleInput("/status")
	if cmd == nil {
		t.Fatal("/status returned nil command")
	}
	msg := cmd()
	result, ok := msg.(statuscommand.ResultMsg)
	if !ok {
		t.Fatalf("/status command returned %T, want statuscommand.ResultMsg", msg)
	}
	updated, _ := m.Update(result)
	m2 := updated.(*model)
	if !m2.showStatus {
		t.Fatal("status result closed the modal")
	}
	if m2.currentResult == nil || len(m2.currentResult.Actions) != 1 || m2.currentResult.Actions[0].ID != "keep-this-result" {
		t.Fatalf("status cleared current result: %+v", m2.currentResult)
	}
	if m2.lastApplyError != "keep this error" {
		t.Fatalf("status cleared error state: %q", m2.lastApplyError)
	}
	if m2.InputTokens != 11 || m2.OutputTokens != 7 {
		t.Fatalf("status changed token counters: %d/%d", m2.InputTokens, m2.OutputTokens)
	}
	if len(m2.records) != 0 {
		t.Fatalf("status polluted viewport records: %+v", m2.records)
	}
	if m2.statusView.Snapshot.Session.Tokens.InputTokens != 11 || m2.statusView.Snapshot.Session.Tokens.OutputTokens != 7 {
		t.Fatalf("status snapshot tokens = %+v", m2.statusView.Snapshot.Session.Tokens)
	}
}
