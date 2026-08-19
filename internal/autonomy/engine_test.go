package autonomy

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/events"
)

// countSub subscribes to the bus BEFORE any event is published and counts
// deliveries atomically, mirroring the subscription-before-emission contract
// used across the engine test suites.
func countSub(t *testing.T, bus *events.Bus, typ string) (*int32, *events.Subscription) {
	t.Helper()
	var n int32
	sub := bus.Subscribe(typ, func(events.DomainEvent) { atomic.AddInt32(&n, 1) })
	return &n, sub
}

func waitDelivered(t *testing.T, n *int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(n) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("expected event delivery timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func newTestEngine() *Engine {
	return NewEngine(WithScope("repository"))
}

// ── VALIDATION CASE 1 ───────────────────────────────────────────────────────
func TestValidationCase1ConversationDirectResponse(t *testing.T) {
	eng := newTestEngine()
	trace := eng.Decide("hi")

	if trace.Intent.Intent != IntentConversation {
		t.Fatalf("intent = %s, want conversation", trace.Intent.Intent)
	}
	if trace.Decision.Decision != DecisionDirectResponse {
		t.Fatalf("decision = %s, want direct_response", trace.Decision.Decision)
	}
	if trace.Route.Workspace != WorkspaceAsk {
		t.Errorf("workspace = %s, want ask (no execution workspace)", trace.Route.Workspace)
	}
	// Conversation must never require a grant.
	if trace.Grant.Required != nil {
		t.Errorf("conversation must not produce a grant request: %+v", trace.Grant)
	}
}

// ── VALIDATION CASE 2 ───────────────────────────────────────────────────────
func TestValidationCase2InspectRoutesToInvestigateReadOnly(t *testing.T) {
	eng := newTestEngine()
	trace := eng.Decide("$prompt inspect @file.go")

	if trace.Intent.Intent != IntentInvestigation {
		t.Fatalf("intent = %s, want investigation", trace.Intent.Intent)
	}
	if trace.Route.Workspace != WorkspaceInvestigate {
		t.Fatalf("workspace = %s, want investigate", trace.Route.Workspace)
	}
	if trace.Decision.Decision != DecisionAutoContinue {
		t.Fatalf("decision = %s, want auto_continue (read-only evidence collection)", trace.Decision.Decision)
	}
	if trace.Intent.Target() != "file.go" {
		t.Errorf("target = %q, want file.go", trace.Intent.Target())
	}
	// Read-only: no grant request.
	if trace.Grant.Required != nil {
		t.Errorf("read-only inspect must not request a grant: %+v", trace.Grant)
	}
	// Workspace contract must be read-only: no mutation.
	if ContractFor(trace.Route.Workspace).Allows(CapMutate) {
		t.Error("investigate workspace contract must forbid mutation")
	}
}

// ── VALIDATION CASE 3 ───────────────────────────────────────────────────────
func TestValidationCase3MutationRequiresGrantThenAutoContinues(t *testing.T) {
	eng := newTestEngine()

	// Phase 1: mutation intent detected, routes to PLAN -> BUILD, asks for the
	// BUILD capability grant exactly once.
	trace := eng.Decide("$prompt remove unused content from @index.html")
	if trace.Intent.Intent != IntentModification {
		t.Fatalf("intent = %s, want modification", trace.Intent.Intent)
	}
	if trace.Route.Workspace != WorkspaceBuild {
		t.Fatalf("workspace = %s, want build", trace.Route.Workspace)
	}
	if trace.Decision.Decision != DecisionAskUser {
		t.Fatalf("pre-grant decision = %s, want ask_user", trace.Decision.Decision)
	}
	if !trace.Decision.Missing.Has(CapMutate) {
		t.Fatalf("missing = %v, want mutate", trace.Decision.Missing)
	}
	if trace.Grant.Scope != "repository" || !trace.Grant.Required.Has(CapMutate) {
		t.Errorf("grant request = %+v", trace.Grant)
	}

	// Phase 2: user grants BUILD access for the workspace.
	g := eng.Grant("repository", CapRead, CapAnalyze, CapPropose, CapMutate, CapVerify)
	if g.ID == "" {
		t.Fatal("grant must carry an ID")
	}
	if !eng.Authority(RequiredCapabilities(IntentModification)) {
		t.Fatal("authority must hold after grant")
	}

	// Phase 3: the same request now auto-continues — no repeated approval.
	trace2 := eng.Decide("$prompt remove unused content from @index.html")
	if trace2.Decision.Decision != DecisionAutoContinue {
		t.Fatalf("post-grant decision = %s, want auto_continue", trace2.Decision.Decision)
	}
}

// ── Event observability ─────────────────────────────────────────────────────

func TestEnginePublishesAutonomyDecision(t *testing.T) {
	bus := events.NewBus(32)
	eng := NewEngine(WithEventBus(bus), WithScope("repository"))
	n, sub := countSub(t, bus, events.EventAutonomyDecision)
	defer sub.Cancel()

	eng.Decide("remove @index.html unused parts")
	waitDelivered(t, n)
}

func TestEnginePublishesCapabilityGranted(t *testing.T) {
	bus := events.NewBus(32)
	eng := NewEngine(WithEventBus(bus), WithScope("repository"))
	n, sub := countSub(t, bus, events.EventCapabilityGranted)
	defer sub.Cancel()

	eng.Grant("repository", CapMutate)
	waitDelivered(t, n)
}

func TestEnginePublishesContextCompiled(t *testing.T) {
	bus := events.NewBus(32)
	eng := NewEngine(WithEventBus(bus), WithScope("repository"))
	ctxN, ctxSub := countSub(t, bus, events.EventContextCompiled)
	defer ctxSub.Cancel()

	eng.CompileContext("index.html", "<html><body><div>hi</div>stray</body></html>")

	waitDelivered(t, ctxN)
}

func TestEngineRiskFuncInformsDecision(t *testing.T) {
	eng := NewEngine(
		WithScope("repository"),
		WithRiskFunc(func(target string) MutationRiskInput {
			return MutationRiskInput{Level: RiskCritical, Indicators: []string{"system path"}}
		}),
	)
	eng.Grant("repository", CapRead, CapAnalyze, CapPropose, CapMutate, CapVerify)

	trace := eng.Decide("remove @/etc/passwd content")
	if trace.Decision.Decision != DecisionAskUser {
		t.Fatalf("critical-risk granted mutation = %s, want ask_user", trace.Decision.Decision)
	}
	if trace.Risk != RiskCritical {
		t.Errorf("trace risk = %s, want critical", trace.Risk)
	}
}

func TestEngineRollbackFuncInformsDecision(t *testing.T) {
	eng := NewEngine(
		WithScope("repository"),
		WithRollbackFunc(func() bool { return false }),
	)
	eng.Grant("repository", CapRead, CapAnalyze, CapPropose, CapMutate, CapVerify)

	trace := eng.Decide("remove unused content from @index.html")
	if trace.Decision.Decision != DecisionAskUser {
		t.Fatalf("no-rollback granted mutation = %s, want ask_user", trace.Decision.Decision)
	}
}

func TestEngineWithAffectedScopeOption(t *testing.T) {
	eng := newTestEngine()
	eng.Grant("repository", CapRead, CapAnalyze, CapPropose, CapMutate, CapVerify)
	trace := eng.Decide("remove unused content from @index.html", WithAffectedScope(5))
	if trace.Decision.Decision != DecisionAskUser {
		t.Fatalf("scope-5 mutation = %s, want ask_user", trace.Decision.Decision)
	}
	if trace.ScopeSize != 5 {
		t.Errorf("scope size = %d, want 5", trace.ScopeSize)
	}
}
