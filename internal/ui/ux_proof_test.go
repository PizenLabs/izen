package ui

import (
	"os"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/presentation"
)

// TestUXProofLog logs the rendered surface for the three acceptance scenarios
// (evidence capture — run with -v).
func TestUXProofLog(t *testing.T) {
	m1 := gatedDispatchModel(t, &mockProvider{responses: []*ai.Response{{Content: "Hi! How can I help?"}}}, nil)
	m1.runGatedLine("hi")
	t.Logf("CASE 1 'hi' — dock-text=%q panel=%q dock-surface=%q",
		m1.composeDockText(), m1.renderExecutionLayered(), stripANSITest(m1.renderLoadingDock()))

	m2 := gatedDispatchModel(t, &mockProvider{}, map[string]string{"index.html": "<p>hi</p>"})
	m2.execView = presentation.NewExecutionProjection()
	m2.execView.Begin("p")
	m2.executionResolving = true
	m2.execVisibility = presentation.VisibilityNormal
	m2.handleDomainEvent(events.NewExecutionStarted("p", "build", "inspect index.html"))
	m2.handleDomainEvent(events.NewStrategySelected("p", "targeted_mutation", true, "x"))
	m2.handleDomainEvent(events.NewTargetResolved("p", "index.html", true, "strategy"))
	m2.handleDomainEvent(events.NewContextPrepared("p", []string{"user_intent", "target_content"}, 42))
	m2.handleDomainEvent(events.NewModelInvoked("p", "mock", 0, 0))
	t.Logf("CASE 2 NORMAL panel:\n%s", m2.renderExecutionLayered())
	m2.execVisibility = presentation.VisibilityExpanded
	t.Logf("CASE 2 EXPANDED panel:\n%s", m2.renderExecutionLayered())
	m2.execVisibility = presentation.VisibilityDebug
	t.Logf("CASE 2 DEBUG panel:\n%s", m2.renderExecutionLayered())
}

// TestUXProofCase3 logs the Case 3 approval surface.
func TestUXProofCase3(t *testing.T) {
	orig := "<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n  <meta charset=\"utf-8\">\n  <meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n  <title>Demo Page</title>\n  <link rel=\"stylesheet\" href=\"styles.css\">\n</head>\n<body>\n  <header>\n    <h1>Welcome to the Demo</h1>\n  </header>\n  <main>\n    <p>This is the primary content of the demo page.</p>\n    <p>extra</p>\n  </main>\n  <footer>\n    <p>Copyright 2026</p>\n  </footer>\n</body>\n</html>\n"
	block := "<<<<<<< SEARCH\n    <p>This is the primary content of the demo page.</p>\n    <p>extra</p>\n=======\n    <p>This is the primary content of the demo page.</p>\n>>>>>>>"
	mock := &mockProvider{responses: []*ai.Response{{
		Content: block, TokenInput: 2860, TokenOutput: 2048,
		Usage: ai.ProviderUsage{PromptTokens: 2860, CompletionTokens: 2048, TotalTokens: 4908, Known: true},
	}}}
	m := gatedDispatchModel(t, mock, map[string]string{"index.html": orig})
	m.state = StateChat
	cmd := m.runGatedLine("$prompt remove extra content @index.html")
	gem := extractGatedExecutionMsg(t, cmd)
	res, _ := m.executionResultUpdate(executionResultMsg{res: gem.res})
	m2 := res.(*model)
	before, _ := os.ReadFile("index.html")
	beforeChanged := string(before) != orig
	t.Logf("CASE 3 state=%s proposals=%d file-changed-before-approval=%v completed.tokens=%d/%d artifact=%s",
		m2.state, len(m2.pendingProposals), beforeChanged, gem.res.Completed.InputTokens, gem.res.Completed.OutputTokens, gem.res.Completed.Artifact)
	for _, r := range m2.records {
		t.Logf("CASE 3 record: %s", r.text)
	}
}
