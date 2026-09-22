package autonomy

import (
	"strings"
	"testing"
)

// Test A — ASK cannot become PLAN: mode ASK + classifier PLAN @ 0.95 must
// reconcile to ASK with zero execution authority.
func TestReconcileAskCannotBecomePlan(t *testing.T) {
	classified := IntentResult{Intent: IntentPlanning, Confidence: 0.95}
	r := ReconcileRoute("ask", classified, false)
	if r.Route != "ask" {
		t.Fatalf("route = %q, want ask", r.Route)
	}
	if r.ExecutionAuthorized {
		t.Fatal("execution_authorized must be false for /ask")
	}
	if r.LoweringKind != "read_only" {
		t.Fatalf("lowering_kind = %q, want read_only", r.LoweringKind)
	}
	if r.ClassifiedIntent != IntentPlanning || r.Confidence != 0.95 {
		t.Fatal("reconciliation must preserve the advisory candidate for telemetry")
	}
}

// Test B — ASK cannot become BUILD @ 0.99.
func TestReconcileAskCannotBecomeBuild(t *testing.T) {
	classified := IntentResult{Intent: IntentModification, Confidence: 0.99}
	r := ReconcileRoute("ask", classified, false)
	if r.Route != "ask" {
		t.Fatalf("route = %q, want ask", r.Route)
	}
	if r.ExecutionAuthorized {
		t.Fatal("read-only conversational route must never authorize execution")
	}
}

// Test C — ASK cannot become implicit $prompt: BUILD intent at high
// confidence without an execution marker is NOT equivalent to /build $prompt.
func TestReconcileAskCannotBecomeImplicitPrompt(t *testing.T) {
	classified := IntentResult{Intent: IntentModification, Confidence: 0.99}
	bare := ReconcileRoute("ask", classified, false)
	explicit := ReconcileRoute("ask", classified, true)
	if bare.Route == explicit.Route && bare.ExecutionAuthorized == explicit.ExecutionAuthorized && explicit.ExecutionAuthorized {
		t.Fatal("bare /ask input must not equal explicit $prompt authority")
	}
	if bare.ExecutionAuthorized {
		t.Fatal("bare /ask must not be execution-authorized")
	}
	if !explicit.ExecutionAuthorized {
		t.Fatal("explicit $prompt BUILD must remain execution-authorized")
	}
}

// Confidence cannot raise the ceiling: sweep every intent at 0.99 in ASK.
func TestReconcileConfidenceNeverRaisesAskCeiling(t *testing.T) {
	for _, intent := range []Intent{
		IntentConversation, IntentExplanation, IntentInvestigation,
		IntentPlanning, IntentModification, IntentVerification,
		IntentDebugging, IntentRefactoring,
	} {
		r := ReconcileRoute("ask", IntentResult{Intent: intent, Confidence: 0.999}, false)
		if r.Route != "ask" || r.ExecutionAuthorized {
			t.Errorf("intent %s @ 99.9%% in /ask reconciled to %s authorized=%t — ceiling violated",
				intent, r.Route, r.ExecutionAuthorized)
		}
	}
}

// Conversational auto-return: simple questions in execution modes return to ASK.
func TestReconcileConversationalFallback(t *testing.T) {
	cases := []struct {
		mode  string
		input string
	}{
		{"investigate", "What does this function do?"},
		{"review", "Why does this function return nil?"},
		{"plan", "What is the purpose of this package?"},
		{"investigate", "hi"},
		{"plan", "explain what a goroutine is"},
	}
	for _, c := range cases {
		classified := Classify(c.input, nil)
		r := ReconcileRouteFor(c.mode, classified, c.input, false)
		if r.Route != "ask" {
			t.Errorf("mode=%s input=%q classified=%s reconciled to %s, want ask",
				c.mode, c.input, classified.Intent, r.Route)
		}
		if r.ExecutionAuthorized {
			t.Errorf("mode=%s input=%q fallback must not authorize execution", c.mode, c.input)
		}
	}
}

// Genuine workflow requests preserve their explicit mode.
func TestReconcileGenuineWorkflowPreserved(t *testing.T) {
	cases := []struct {
		mode  string
		input string
		want  string
	}{
		{"plan", "design the migration architecture for the billing service", "plan"},
		{"investigate", "investigate the crash on startup with stack trace", "investigate"},
		{"review", "audit the risk in @index.html", "review"},
		{"build", "implement the retry handler", "build"},
	}
	for _, c := range cases {
		classified := Classify(c.input, nil)
		r := ReconcileRouteFor(c.mode, classified, c.input, false)
		if r.Route != c.want {
			t.Errorf("mode=%s input=%q reconciled to %s, want %s (classified=%s)",
				c.mode, c.input, r.Route, c.want, classified.Intent)
		}
	}
}

// Explicit execution authority bypasses the ceiling.
func TestReconcileExplicitExecutionBypassesCeiling(t *testing.T) {
	classified := IntentResult{Intent: IntentModification, Confidence: 0.9}
	r := ReconcileRoute("ask", classified, true)
	if r.Route != "build" {
		t.Fatalf("explicit BUILD route = %q, want build", r.Route)
	}
	if !r.ExecutionAuthorized {
		t.Fatal("explicit execution must remain authorized")
	}
}

// Telemetry line carries every required field with real values.
func TestReconciliationTelemetryFields(t *testing.T) {
	r := ReconcileRoute("ask", IntentResult{Intent: IntentPlanning, Confidence: 0.95}, false)
	line := r.FormatReconciliation()
	for _, want := range []string{
		"classified_intent=planning", "confidence=0.95", "current_mode=ask",
		"reconciled_route=ask", "execution_authorized=false",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("telemetry line missing %q: %s", want, line)
		}
	}
}
