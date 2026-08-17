package ui

// ── PHASE 2 — UI RESULT PROJECTION TRUTH ────────────────────────────────────
//
// The UI must project canonical runtime truth. These tests pin that a
// committed no-change, a rejected proposal, and unknown provider usage are
// rendered from the ExecutionResult — never inferred from model/proposal state.

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/execution"
)

// uiRunExecution drives a gated $prompt execution to the approval gate and
// returns the staged model.
func uiRunExecution(t *testing.T, m *model, input string) *model {
	t.Helper()
	gem := runGate(m, input)
	if gem.res == nil || gem.res.PendingPatchID == "" {
		t.Fatalf("execution must stop at the approval gate: %+v", gem.res)
	}
	res, _ := m.executionResultUpdate(executionResultMsg{res: gem.res})
	m2 := res.(*model)
	if m2.state != StateAwaitingApproval {
		t.Fatalf("state = %v, want StateAwaitingApproval", m2.state)
	}
	return m2
}

// uiApprove runs the executor approve through the model and returns the
// updated model + final result.
func uiApprove(t *testing.T, m *model) (*model, *execution.ExecutionResult) {
	t.Helper()
	applyCmd := m.runExecutorApproveCmd(m.executorPendingPatchID)
	if applyCmd == nil {
		t.Fatal("approve command is nil")
	}
	applyMsg := applyCmd()
	erm, ok := applyMsg.(executionResultMsg)
	if !ok {
		t.Fatalf("approve produced %T, want executionResultMsg", applyMsg)
	}
	if erm.err != nil {
		t.Fatalf("approve failed: %v", erm.err)
	}
	res2, cmd2 := m.executionResultUpdate(erm)
	m3 := res2.(*model)
	for _, msg := range drainCmds(t, cmd2) {
		var r3 tea.Model
		r3, _ = m3.Update(msg)
		m3 = r3.(*model)
	}
	return m3, erm.res
}

// TestUITruth_NoChangeIsProjectedDistinctly pins that a committed no-change
// approve renders its own terminal projection — never the generic "Completed —
// nothing produced." fallback and never a claimed mutation.
func TestUITruth_NoChangeIsProjectedDistinctly(t *testing.T) {
	orig := "line1\nline2\nline3\n"
	m, _ := gatedHarness(t, map[string]string{"note.txt": orig}, &mockProvider{
		responses: []*ai.Response{{Content: orig}},
	})
	m2 := uiRunExecution(t, m, "$prompt change bar to qux in @note.txt")
	m3, res := uiApprove(t, m2)

	if res.Proof.Outcome != execution.OutcomeNoChange {
		t.Fatalf("proof outcome = %s, want nochange", res.Proof.Outcome)
	}
	if m3.state == StateAwaitingApproval {
		t.Fatal("approval gate still held after a no-change apply")
	}
	joined := recordsText(m3)
	if !strings.Contains(joined, "no content changed") {
		t.Fatalf("no-change must project its own terminal line, got: %q", joined)
	}
	if strings.Contains(joined, "nothing produced") {
		t.Fatalf("no-change must not fall through to the generic completion: %q", joined)
	}
	if strings.Contains(joined, "Mutation applied and verified") {
		t.Fatalf("no-change must not claim an applied-and-verified mutation: %q", joined)
	}
	// The filesystem is byte-identical.
	if onDisk, err := os.ReadFile("note.txt"); err != nil || string(onDisk) != orig {
		t.Fatalf("no-change apply mutated the file: %q err=%v", onDisk, err)
	}
}

// TestUITruth_RejectedIsProjectedDistinctly pins that an explicit rejection at
// the approval gate renders as a rejection, distinct from a cancellation.
func TestUITruth_RejectedIsProjectedDistinctly(t *testing.T) {
	orig := "foo\nbar\nbaz\n"
	m, _ := gatedHarness(t, map[string]string{"note.txt": orig}, &mockProvider{
		responses: []*ai.Response{{
			Content: "<<<<<<< SEARCH\nbar\n=======\nqux\n>>>>>>>",
			Usage:   ai.ProviderUsage{Known: true, PromptTokens: 5, CompletionTokens: 3},
		}},
	})
	m2 := uiRunExecution(t, m, "$prompt change bar to qux in @note.txt")

	rejectCmd := m2.runExecutorRejectCmd(m2.executorPendingPatchID, "no")
	rejectMsg := rejectCmd()
	erm, ok := rejectMsg.(executionResultMsg)
	if !ok {
		t.Fatalf("reject produced %T, want executionResultMsg", rejectMsg)
	}
	if erm.res.Proof.Outcome != execution.OutcomeRejected {
		t.Fatalf("proof outcome = %s, want rejected", erm.res.Proof.Outcome)
	}
	res2, _ := m2.executionResultUpdate(erm)
	m3 := res2.(*model)
	if m3.state == StateAwaitingApproval {
		t.Fatal("approval gate still held after a rejection")
	}
	joined := recordsText(m3)
	if !strings.Contains(joined, "Rejected") {
		t.Fatalf("rejection must project as a rejection: %q", joined)
	}
	if onDisk, err := os.ReadFile("note.txt"); err != nil || string(onDisk) != orig {
		t.Fatalf("rejected proposal mutated the file: %q err=%v", onDisk, err)
	}
}

// TestUITruth_UsageUnknownIsNotZero pins that when the provider reports NO
// usage, the footer stays in the "usage unknown" state (usageKnown=false) —
// the UI must not infer a genuine zero from the runtime's unknown account.
func TestUITruth_UsageUnknownIsNotZero(t *testing.T) {
	m, _ := gatedHarness(t, map[string]string{"note.txt": "foo\nbar\nbaz\n"}, &mockProvider{
		responses: []*ai.Response{{Content: "<<<<<<< SEARCH\nbar\n=======\nqux\n>>>>>>>"}},
	})
	m2 := uiRunExecution(t, m, "$prompt change bar to qux in @note.txt")
	if m2.usageKnown {
		t.Fatal("footer must start in the usage-unknown state")
	}
	m3, res := uiApprove(t, m2)
	if res.Completed.Known {
		t.Fatal("runtime usage account must be unknown when the provider reported nothing")
	}
	if m3.usageKnown {
		t.Fatal("footer must stay usage-unknown (not a fabricated zero) when the runtime reports unknown usage")
	}
	if m3.InputTokens != 0 || m3.OutputTokens != 0 {
		t.Fatalf("footer tokens = %d/%d, want 0/0 (unknown)", m3.InputTokens, m3.OutputTokens)
	}
}

// TestUITruth_UsageKnownReachesFooter pins that provider-reported usage on the
// approve path travels to the footer through the runtime's authoritative
// Completed account (Known=true), not a UI hardcode.
func TestUITruth_UsageKnownReachesFooter(t *testing.T) {
	m, _ := gatedHarness(t, map[string]string{"note.txt": "foo\nbar\nbaz\n"}, &mockProvider{
		responses: []*ai.Response{{
			Content: "<<<<<<< SEARCH\nbar\n=======\nqux\n>>>>>>>",
			Usage:   ai.ProviderUsage{Known: true, PromptTokens: 12, CompletionTokens: 6},
		}},
	})
	m2 := uiRunExecution(t, m, "$prompt change bar to qux in @note.txt")
	m3, res := uiApprove(t, m2)
	if !res.Completed.Known {
		t.Fatal("runtime usage account must be known")
	}
	if m3.usageKnown {
		t.Log("footer marked usage known (session-wide) as expected")
	}
	if m3.InputTokens != 12 || m3.OutputTokens != 6 {
		t.Fatalf("footer tokens = %d/%d, want 12/6", m3.InputTokens, m3.OutputTokens)
	}
}
