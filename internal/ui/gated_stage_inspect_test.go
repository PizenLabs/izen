package ui

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/modes"
)

// TestGatedExecutionDrivesTruthfulStage pins that the authoritative execution
// stage on the gated RuntimeExecutor path is driven EXCLUSIVELY by canonical
// runtime events — never a fabricated label. Every real boundary (target read,
// provider waiting, provider streaming, token usage) transitions the stage.
func TestGatedExecutionDrivesTruthfulStage(t *testing.T) {
	m := newTestModel()
	if lbl := m.stageSnapshot().Label; lbl != "" {
		t.Fatalf("stage must carry no label before any runtime boundary, got %q", lbl)
	}

	m.handleDomainEvent(events.NewTargetResolved("r", "index.html", true, "strategy"))
	st := m.stageSnapshot()
	if st.Label != "target" || st.Target != "index.html" {
		t.Fatalf("stage after target.resolved = %+v, want target/index.html", st)
	}

	m.handleDomainEvent(events.NewProviderWaiting("r", "mock"))
	st = m.stageSnapshot()
	if st.State != stageWaiting || st.Target != "mock" {
		t.Fatalf("stage after provider.waiting = %+v, want waiting/mock", st)
	}

	m.handleDomainEvent(events.NewProviderFirstToken("r", "mock", 0))
	st = m.stageSnapshot()
	if st.State != stageStreaming {
		t.Fatalf("stage after provider.first_token = %+v, want streaming", st)
	}

	// Token counts reach the indicator ONLY from authoritative provider usage.
	m.handleDomainEvent(events.NewProviderUsageUpdate("r", "mock", 12, 6, 0))
	st = m.stageSnapshot()
	if st.Tokens != 6 {
		t.Fatalf("stage tokens after usage update = %d, want 6 (provider-reported)", st.Tokens)
	}

	// An empty fresh stage (no boundary reached) must render NO claim.
	m.beginOperation(OpHotfix)
	if line := m.renderStageLine(); line != "" {
		t.Fatalf("empty fresh stage rendered a claim %q — empty is better than fake", line)
	}
}

// TestInspectRetainsRuntimeGraph pins P1 #6: a gated RuntimeExecutor result
// retains the runtime-owned ExecutionProof + RuntimeGraph so $inspect renders
// the authoritative execution timeline — never reconstructed from UI state.
func TestInspectRetainsRuntimeGraph(t *testing.T) {
	mock := &mockProvider{responses: []*ai.Response{{
		Content: "```\nfixed\n```",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 12, CompletionTokens: 6},
	}}}
	m := gatedDispatchModel(t, mock, map[string]string{"index.html": "<p>old</p>"})
	m.resolver.Set(modes.ModeBuild)

	cmd := m.runGatedLine("$hot fix index.html")
	if cmd == nil {
		t.Fatal("nil command")
	}
	msg := extractGatedExecutionMsg(t, cmd)
	if msg.err != nil {
		t.Fatalf("execution err: %v", msg.err)
	}
	res, _ := m.executionResultUpdate(executionResultMsg{res: msg.res})
	m2 := res.(*model)

	if len(m2.lastRuntimeGraph) == 0 {
		t.Fatal("runtime graph not retained for $inspect")
	}
	if m2.lastExecutionProof.Strategy == "" {
		t.Fatal("runtime proof strategy not retained")
	}
	if m2.lastExecutionProof.ProviderInvocations != 1 {
		t.Fatalf("proof invocations = %d, want 1", m2.lastExecutionProof.ProviderInvocations)
	}
	if m2.lastExecutionProof.OutputUsage != 6 {
		t.Fatalf("proof output usage = %d, want 6 (provider-reported)", m2.lastExecutionProof.OutputUsage)
	}

	rendered := renderRuntimeGraph(m2.lastRuntimeGraph)
	for _, want := range []string{"strategy_selected", "context_prepared", "model_invoked"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("runtime graph rendering missing %q: %q", want, rendered)
		}
	}
}
