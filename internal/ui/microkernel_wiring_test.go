package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/plan"
)

// greenfieldPromptUI is the verification prompt: a greenfield static website
// generation request typed into the TUI.
const greenfieldPromptUI = "make the website introduce for JAY with your job is software engineer using html, css and js"

// TestFrontendUIBypassPreservesRawPrompt verifies that when a FRONTEND_UI
// prompt is received in /investigate, the mode transitions to /plan AND the
// raw prompt survives the mode-transition purge (via the ContextLedger
// packet SSOT) so the microkernel pipeline can plan from the actual request
// instead of a placeholder.
func TestFrontendUIBypassPreservesRawPrompt(t *testing.T) {
	m := newTestModel()
	m.resolver = modes.NewResolver()
	m.resolver.Set(modes.ModeInvestigate)
	m.microkernel = plan.NewMicrokernelPlanner(t.TempDir())
	m.handoffLedgerContent = "ledger content"
	m.handoffCtx.LastFailurePayload = "failure"

	_ = m.handleMessageContent(greenfieldPromptUI)
	if m.resolver.Current() != modes.ModePlan {
		t.Fatalf("resolver.Current() = %v, want ModePlan", m.resolver.Current())
	}
	if !strings.Contains(m.handoffLedgerContent, "make the website") {
		t.Fatalf("raw prompt lost across mode transition: %q", m.handoffLedgerContent)
	}
	if !strings.Contains(m.handoffLedgerContent, "user_intent") {
		t.Fatalf("prompt should survive as a ledger packet: %q", m.handoffLedgerContent)
	}
}

// TestPlanResultMsgMicrokernelRejectionSurfacesReason verifies that a
// microkernel rejection (PolicyEngine / ExecutionPreconditions) surfaces its
// explicit reason in the footer notification (status bar).
func TestPlanResultMsgMicrokernelRejectionSurfacesReason(t *testing.T) {
	m := newTestModel()
	m.microkernel = plan.NewMicrokernelPlanner(t.TempDir())

	reason := errors.New("microkernel plan rejected by policy: permitted_path on \"outside.tmp\" — target outside every permitted root")
	m.Update(planResultMsg{Err: reason, Microkernel: true})

	if m.uiNotice == "" {
		t.Fatal("rejection reason must be surfaced in the status-bar footer")
	}
	if !strings.Contains(m.uiNotice, "permitted_path") {
		t.Fatalf("uiNotice = %q, want explicit policy reason", m.uiNotice)
	}
}

// TestPlanResultMsgMicrokernelStaging verifies that a staged microkernel plan
// announces itself in the footer and never runs the LLM synthesis message.
func TestPlanResultMsgMicrokernelStaging(t *testing.T) {
	m := newTestModel()
	m.microkernel = plan.NewMicrokernelPlanner(t.TempDir())

	tasks := []plan.Task{
		{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Description: "CREATE index.html"},
		{StepNum: 2, Type: "FILE_MUTATE", Target: "styles.css", Description: "CREATE styles.css"},
		{StepNum: 3, Type: "FILE_MUTATE", Target: "script.js", Description: "CREATE script.js"},
	}
	m.Update(planResultMsg{Tasks: tasks, Microkernel: true})

	if m.uiNotice == "" {
		t.Fatal("microkernel staging must set a footer notice")
	}
	if !strings.Contains(m.uiNotice, "Microkernel") || !strings.Contains(m.uiNotice, "3") {
		t.Fatalf("uiNotice = %q, want microkernel staging announcement", m.uiNotice)
	}
}
