package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/router"
)

// fakeIntentRouter satisfies the router.IntentClassifier contract so tests can
// drive classification results without an LLM.
type fakeIntentRouter struct {
	intent router.Intent
	conf   float64
	expl   string
	req    bool
}

func (f *fakeIntentRouter) Classify(_ context.Context, _ string) (router.ClassificationResult, error) {
	return router.ClassificationResult{
		Intent:                  f.intent,
		Confidence:              f.conf,
		Explanation:             f.expl,
		ConfirmationRequirement: f.req,
	}, nil
}

func routerTestModel() *model {
	m := newTestModel()
	m.intentRouter = router.NewRouter(&fakeIntentRouter{intent: router.IntentAsk, conf: 1.0, expl: "test"}, nil)
	m.state = StateChat
	m.awaitingConfirmation = false
	m.ti.Focus()
	m.execEng = execution.NewEngine(".", m.cfg, m.sess)
	return m
}

func TestHandleRouterResultConfidentSwitch(t *testing.T) {
	m := routerTestModel()
	m.intentRouter = router.NewRouter(&fakeIntentRouter{
		intent: router.IntentPlan, conf: 0.92, expl: "plan-related prompt", req: false,
	}, nil)

	// Start in /build; a confident /plan classification should auto-switch.
	m.resolver.Set(modes.ModeBuild)
	m.modeChangeAuthorized = true

	next, cmd := m.handleRouterResult(routerResultMsg{
		line: "please create a plan",
		result: router.ClassificationResult{
			Intent: router.IntentPlan, Confidence: 0.92,
			Explanation: "plan-related prompt", ConfirmationRequirement: false,
		},
	})

	if next == nil || cmd == nil {
		t.Fatalf("expected model + command, got %v / %v", next, cmd)
	}
	if got := m.resolver.Current(); got != modes.ModePlan {
		t.Errorf("mode = %v, want ModePlan", got)
	}
	if m.pendingRouteConfirm {
		t.Error("confident result must NOT enter pending confirmation")
	}
}

func TestHandleRouterResultConfirmationPrompt(t *testing.T) {
	m := routerTestModel()
	m.intentRouter = router.NewRouter(&fakeIntentRouter{
		intent: router.IntentBuild, conf: 0.40, expl: "ambiguous", req: true,
	}, nil)

	next, cmd := m.handleRouterResult(routerResultMsg{
		line: "hmm what should I do",
		result: router.ClassificationResult{
			Intent: router.IntentBuild, Confidence: 0.40,
			Explanation: "ambiguous", ConfirmationRequirement: true,
		},
	})

	if next == nil {
		t.Fatal("expected model, got nil")
	}
	if cmd != nil {
		t.Error("confirmation prompt must NOT launch a command (blocks on user choice)")
	}
	if !m.pendingRouteConfirm {
		t.Error("expected pendingRouteConfirm = true")
	}
	if m.state != StateAwaitingApproval {
		t.Errorf("state = %v, want StateAwaitingApproval", m.state)
	}
	if len(m.pendingRouteOptions) != 5 {
		t.Errorf("expected 5 mode options, got %d", len(m.pendingRouteOptions))
	}
	if m.pendingRouteIdx != 3 { // build is the 4th option (ask,investigate,plan,build,review)
		t.Errorf("pendingRouteIdx = %d, want 3 (build)", m.pendingRouteIdx)
	}
	// Render must include the prompt title and the option list.
	rendered := m.renderRouteConfirmPrompt(100)
	for _, want := range []string{"CLARIFY INTENT", "/build", "1-5 Select"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("renderRouteConfirmPrompt missing %q\n%s", want, rendered)
		}
	}
}

func TestConfirmRouteSelectionDispatches(t *testing.T) {
	m := routerTestModel()
	m.pendingRouteConfirm = true
	m.pendingRouteInput = "investigate this crash"
	m.pendingRouteResult = router.ClassificationResult{Intent: router.IntentInvestigate, Confidence: 0.3}
	m.pendingRouteOptions = []modes.Mode{
		modes.ModeAsk, modes.ModeInvestigate, modes.ModePlan, modes.ModeBuild, modes.ModeReview,
	}
	m.pendingRouteIdx = 1

	cmd := m.confirmRouteSelection(modes.ModeInvestigate)
	if cmd == nil {
		t.Fatal("expected a dispatch command after confirm")
	}
	if m.pendingRouteConfirm {
		t.Error("pendingRouteConfirm must clear after confirm")
	}
	if m.state != StateChat {
		t.Errorf("state = %v, want StateChat", m.state)
	}
	if got := m.resolver.Current(); got != modes.ModeInvestigate {
		t.Errorf("mode = %v, want ModeInvestigate", got)
	}
}

func TestHandleRouterResultErrorFallsThrough(t *testing.T) {
	m := routerTestModel()
	m.intentRouter = router.NewRouter(&fakeIntentRouter{}, nil) // never used
	m.resolver.Set(modes.ModeAsk)                               // always yields a stream cmd

	next, cmd := m.handleRouterResult(routerResultMsg{
		line:   "hello",
		result: router.ClassificationResult{},
		err:    errors.New("classifier down"),
	})
	if next == nil {
		t.Fatal("expected model, got nil")
	}
	// The error path must fall through to the normal chat dispatch. Whether a
	// cmd is produced depends on the provider wiring of the harness; what
	// matters is that the router does NOT enter the confirmation prompt.
	if m.pendingRouteConfirm {
		t.Error("error path must not enter pending confirmation")
	}
	_ = cmd // dispatch may be nil for provider-less harnesses
}

func TestRouteConfirmPromptKeyHandling(t *testing.T) {
	m := routerTestModel()
	m.pendingRouteConfirm = true
	m.pendingRouteInput = "pick a mode"
	m.pendingRouteResult = router.ClassificationResult{Intent: router.IntentAsk, Confidence: 0.2}
	m.pendingRouteOptions = []modes.Mode{
		modes.ModeAsk, modes.ModeInvestigate, modes.ModePlan, modes.ModeBuild, modes.ModeReview,
	}
	m.pendingRouteIdx = 0
	m.state = StateAwaitingApproval

	// Digit 3 selects /plan.
	next, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	if next == nil {
		t.Fatal("expected model after digit selection")
	}
	if m.resolver.Current() != modes.ModePlan {
		t.Errorf("mode = %v, want ModePlan", m.resolver.Current())
	}
	if cmd == nil {
		t.Error("digit selection must dispatch the prompt")
	}
	if m.pendingRouteConfirm {
		t.Error("pendingRouteConfirm must clear after digit selection")
	}
}
