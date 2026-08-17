package ui

import (
	"os"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes"
)

func autonomyTestModel() *model {
	m := readyChatModel(newTestModel())
	m.resolver.Set(modes.ModeAsk)
	m.autonomy = autonomy.NewEngine(autonomy.WithScope("repository"))
	m.execEng = execution.NewEngine(".", m.cfg, m.sess)
	m.provider = &mockProvider{responses: []*ai.Response{{Content: "ok", TokenOutput: 5}}}
	return m
}

// TestAutonomyValidationCase1ConversationDirectResponse pins Case 1: "hi" is a
// conversation that is answered directly — no workspace switch, no autonomous
// loop, no timeline.
func TestAutonomyValidationCase1ConversationDirectResponse(t *testing.T) {
	m := autonomyTestModel()
	m.resolver.Set(modes.ModeBuild)

	cmd := m.runAutonomyRoutedCmd("hi")
	// A greeting is answered locally by interceptLocalIntent (nil cmd) or via
	// the chat stream — either way the workspace must NOT switch and no
	// execution engine may start.
	if m.investigateRunning || m.reviewRunning || m.agentRunning {
		t.Fatal("conversation must not start any execution engine")
	}
	// The workspace must NOT switch away from the current mode for a greeting.
	if got := m.resolver.Current(); got != modes.ModeBuild {
		t.Fatalf("workspace = /%s, want /build (no switch for conversation)", got)
	}
	_ = cmd
}

// TestAutonomyValidationCase2InspectRoutesToInvestigate pins Case 2:
// "$prompt inspect @index.html" selects the INVESTIGATE workspace and executes
// the evidence pipeline (no mutation capability).
func TestAutonomyValidationCase2InspectRoutesToInvestigate(t *testing.T) {
	m := autonomyTestModel()

	cmd := m.runAutonomyRoutedCmd("inspect @index.html")
	if cmd == nil {
		t.Fatal("inspect objective must dispatch the investigate engine")
	}
	if got := m.resolver.Current(); got != modes.ModeInvestigate {
		t.Fatalf("workspace = /%s, want /investigate", got)
	}
	if !m.investigateRunning {
		t.Error("investigate engine must have started")
	}
}

// TestAutonomyValidationCase3MutationGatesThenExecutes pins Case 3:
// "$prompt read @index.html and remove extra contents" first asks for the
// mutation capability grant; after /grant it executes in BUILD without a
// repeated approval.
func TestAutonomyValidationCase3MutationGatesThenExecutes(t *testing.T) {
	m := autonomyTestModel()

	cmd := m.runAutonomyRoutedCmd("read @index.html and remove extra contents")
	if cmd != nil {
		t.Fatal("pre-grant mutation must return nil cmd and await approval")
	}
	if m.pendingAutonomyGrant == nil {
		t.Fatal("expected a pending grant request before mutation executes")
	}
	if !m.pendingAutonomyGrant.Decision.Missing.Has(autonomy.CapMutate) {
		t.Fatalf("pending grant must require mutate, got %v", m.pendingAutonomyGrant.Decision.Missing)
	}

	// The grant request must be rendered to the viewport.
	if !strings.Contains(recordsText(m), "AUTONOMY GRANT REQUEST") {
		t.Error("expected the grant request to be rendered")
	}

	// Grant via /grant: the runtime issues the missing capability, re-runs the
	// decision and executes the BUILD workspace — no repeated approval.
	grantCmd := m.handleAutonomyGrant("")
	if grantCmd == nil {
		t.Fatal("/grant must continue the pending autonomy decision")
	}
	if m.pendingAutonomyGrant != nil {
		t.Error("pending grant must be consumed by /grant")
	}
	if got := m.resolver.Current(); got != modes.ModeBuild {
		t.Fatalf("post-grant workspace = /%s, want /build", got)
	}
	if !m.autonomy.Authority(autonomy.RequiredCapabilities(autonomy.IntentModification)) {
		t.Error("authority must hold after the grant")
	}
}

// TestAutonomyGrantNoRepeatedApproval pins the "no repeated approval for the
// granted scope" guarantee: after one grant, the same objective auto-continues.
func TestAutonomyGrantNoRepeatedApproval(t *testing.T) {
	m := autonomyTestModel()

	// First request: ask_user (mutation not granted).
	m.runAutonomyRoutedCmd("remove unused content from @index.html")
	if m.pendingAutonomyGrant == nil {
		t.Fatal("expected pending grant on first mutation request")
	}
	m.handleAutonomyGrant("")

	// Second request with the same objective: now auto-continues directly.
	m.pendingAutonomyGrant = nil
	m.resolver.Set(modes.ModeAsk)
	cmd := m.runAutonomyRoutedCmd("remove unused content from @index.html")
	if cmd == nil {
		t.Fatal("post-grant mutation must execute without a repeated grant request")
	}
	if m.pendingAutonomyGrant != nil {
		t.Error("granted scope must not re-ask for approval")
	}
}

// TestAutonomyFreeFormConversationStaysAsk pins the free-form boundary: bare
// conversation input never enters a workspace or execution loop.
func TestAutonomyFreeFormConversationStaysAsk(t *testing.T) {
	m := autonomyTestModel()
	m.resolver.Set(modes.ModeAsk)

	cmd := m.handleInput("explain what a goroutine is")
	if cmd == nil {
		t.Fatal("conversation must dispatch a chat response")
	}
	if got := m.resolver.Current(); got != modes.ModeAsk {
		t.Fatalf("workspace = /%s, want /ask for conversation", got)
	}
}

// TestAutonomyPromptDirectiveRoutesThroughRuntime pins that the $prompt
// directive flows through the autonomy runtime rather than the legacy /ask
// handoff when the decision runtime is wired.
func TestAutonomyPromptDirectiveRoutesThroughRuntime(t *testing.T) {
	m := autonomyTestModel()

	cmd := m.routePromptDirective("inspect @index.html")
	if cmd == nil {
		t.Fatal("$prompt inspect must dispatch")
	}
	if got := m.resolver.Current(); got != modes.ModeInvestigate {
		t.Fatalf("$prompt inspect → workspace /%s, want /investigate", got)
	}
	if !m.investigateRunning {
		t.Error("investigate engine must start after $prompt routing")
	}
}

// TestAutonomyPromptDirectiveMutationGates pins that $prompt mutation requests
// ask for the capability grant before execution.
func TestAutonomyPromptDirectiveMutationGates(t *testing.T) {
	m := autonomyTestModel()

	cmd := m.routePromptDirective("read @index.html and remove extra contents")
	if cmd != nil {
		t.Fatal("pre-grant $prompt mutation must await approval")
	}
	if m.pendingAutonomyGrant == nil || !m.pendingAutonomyGrant.Decision.Missing.Has(autonomy.CapMutate) {
		t.Fatal("$prompt mutation must stage a mutation grant request")
	}
}

// TestAutonomyContextEvidenceLedger pins requirement §6: before the build
// engine asks the model, the runtime compiles structural evidence for the
// resolved target and hands the model a Context Evidence Ledger.
func TestAutonomyContextEvidenceLedger(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	indexHTML := "<html><body><main><p>keep</p></main>stray text</body></html>\n"
	if err := os.WriteFile("index.html", []byte(indexHTML), 0o644); err != nil {
		t.Fatal(err)
	}

	m := autonomyTestModel()
	m.autonomy.GrantDefault(autonomy.CapRead, autonomy.CapAnalyze, autonomy.CapPropose, autonomy.CapMutate, autonomy.CapVerify)
	m.resolver.Set(modes.ModeAsk)

	cmd := m.runAutonomyRoutedCmd("read @index.html and remove extra contents")
	if cmd == nil {
		t.Fatal("post-grant mutation must execute")
	}
	// The runtime must have compiled the target's structural evidence: orphan
	// text (stray text) is a deterministic finding the model never has to
	// rediscover.
	msg := cmd()
	prm, ok := msg.(planResultMsg)
	if !ok {
		t.Fatalf("expected planResultMsg, got %T", msg)
	}
	if len(prm.Tasks) == 0 {
		t.Fatal("expected staged build tasks")
	}
	if !strings.Contains(prm.Tasks[0].Evidence, "Context Evidence Ledger") {
		t.Errorf("task evidence missing ledger, got: %q", prm.Tasks[0].Evidence)
	}
	if !strings.Contains(prm.Tasks[0].Evidence, "html.orphan_text") {
		t.Errorf("task evidence missing orphan-text finding, got: %q", prm.Tasks[0].Evidence)
	}
	if prm.Tasks[0].Target != "index.html" {
		t.Errorf("build target = %q, want index.html", prm.Tasks[0].Target)
	}
}
