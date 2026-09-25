package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/providers"
	appruntime "github.com/PizenLabs/izen/internal/runtime"
	"github.com/PizenLabs/izen/internal/runtime/authority"
)

// newEligibilityTestModel builds a chat-ready model whose active binding is the
// agentic-harness OpenRouter model (thinkingmachines/inkling-small:free) with a
// different workspace default configured. Adaptive Contract Promotion executes
// the selected model natively, so the active binding must survive every
// session-boot and turn-lifecycle event.
func newEligibilityTestModel(t *testing.T) *model {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	stubPersistBinding(t)
	cfg := config.Default()
	cfg.Bindings.Active = config.ActiveBindingConfig{
		Provider: "openrouter",
		Model:    "thinkingmachines/inkling-small:free",
	}
	cfg.AI.Providers["openrouter"] = config.AIProviderConfig{
		DefaultModel: "anthropic/claude-3.5-sonnet",
	}
	m := readyChatModel(newTestModel())
	m.cfg = cfg
	m.provider = stubBindingProvider{name: "openrouter"}
	m.modelAuthority = appruntime.NewRuntimeAuthority()
	m.modelAuthority.Activate(authority.ModelBinding{
		ProviderID: authority.ProviderID("openrouter"),
		ModelID:    authority.ModelID("thinkingmachines/inkling-small:free"),
	})
	return m
}

// assertAgenticBindingIntact pins the Adaptive Runtime contract: the active
// binding is still the agentic-harness model after the event.
func assertAgenticBindingIntact(t *testing.T, m *model) {
	t.Helper()
	if got := m.getActiveModelName(); got != "thinkingmachines/inkling-small:free" {
		t.Fatalf("active model = %q, want the selected agentic model retained", got)
	}
	m.ensureModelAuthority()
	auth := m.modelAuthority.ActiveBinding()
	if string(auth.ProviderID) != "openrouter" || string(auth.ModelID) != "thinkingmachines/inkling-small:free" {
		t.Fatalf("authority binding = %s/%s, want openrouter/thinkingmachines/inkling-small:free", auth.ProviderID, auth.ModelID)
	}
}

// assertNoReversionNotice pins the removal of the legacy pre-flight reversion:
// no surface may emit a model-reversion notice or point at /model as a remedy.
func assertNoReversionNotice(t *testing.T, m *model) {
	t.Helper()
	text := recordsText(m)
	for _, banned := range []string{
		"is unavailable for Izen's current execution path",
		"Reverted to workspace default",
		"no eligible default model is configured",
	} {
		if strings.Contains(text, banned) {
			t.Fatalf("legacy reversion notice %q must be gone, got:\n%s", banned, text)
		}
	}
}

// TestBootRetainsAgenticBinding: session boot never reconciles, reverts, or
// rejects the restored active binding — an agentic-harness model is executed
// natively via Dynamic Contract Promotion.
func TestBootRetainsAgenticBinding(t *testing.T) {
	m := newEligibilityTestModel(t)
	m.modelAuthority = nil // simulate the lazy authority not yet seeded
	um, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = um.(*model)

	assertAgenticBindingIntact(t, m)
	assertNoReversionNotice(t, m)
}

// TestStreamErrDoesNotRevertActiveModel: a wire-level compatibility refusal is
// reported as a provider error; it never mutates the active binding and never
// renders the legacy "reverted to workspace default" notice.
func TestStreamErrDoesNotRevertActiveModel(t *testing.T) {
	m := newEligibilityTestModel(t)
	um, _ := m.Update(streamErrMsg{err: providers.ErrOpenRouterModelIncompatible})
	m = um.(*model)

	assertAgenticBindingIntact(t, m)
	assertNoReversionNotice(t, m)
}

// TestGatedExecutionDoesNotRevertActiveModel: the executor/gated path surfaces
// the real error and leaves the selected model active.
func TestGatedExecutionDoesNotRevertActiveModel(t *testing.T) {
	m := newEligibilityTestModel(t)
	um, _ := m.handleGatedExecution(gatedExecutionMsg{err: providers.ErrOpenRouterModelIncompatible})
	m = um.(*model)

	assertAgenticBindingIntact(t, m)
	assertNoReversionNotice(t, m)
	if m.cfg.Bindings.Active.Model != "thinkingmachines/inkling-small:free" {
		t.Fatalf("session config binding = %q, want the selected model retained", m.cfg.Bindings.Active.Model)
	}
}

// TestSwitchModelDirectAcceptsAgenticModel: /model <agentic model> activates the
// binding with no local eligibility rejection.
func TestSwitchModelDirectAcceptsAgenticModel(t *testing.T) {
	m := newEligibilityTestModel(t)
	m.switchModelDirect("thinkingmachines/inkling:free")

	if got := m.getActiveModelName(); got != "thinkingmachines/inkling:free" {
		t.Fatalf("active model = %q, want the requested agentic model", got)
	}
	text := recordsText(m)
	if strings.Contains(text, "unavailable for Izen") {
		t.Fatalf("/model must not reject a discovered model locally, got:\n%s", text)
	}
}

// TestAgenticGateIsActionableAndNonReverting: OpenRouter's "Gate Free Endpoints
// by Agentic Harness" refusal is a provider access policy. It must render the
// three real remedies, keep the user's binding, and never read as a reversion.
func TestAgenticGateIsActionableAndNonReverting(t *testing.T) {
	m := newEligibilityTestModel(t)
	um, _ := m.Update(streamErrMsg{err: fmt.Errorf("%w: model %q is served only to agentic harnesses OpenRouter has registered",
		providers.ErrOpenRouterAgenticGate, "thinkingmachines/inkling-small:free")})
	m = um.(*model)

	text := recordsText(m)
	for _, want := range []string{
		"served only to registered agentic harnesses",
		"Your active model is unchanged",
		"openrouter/thinkingmachines/inkling-small",
		"roles.<role>.fallback",
		"https://openrouter.ai/apps",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("gate notice must mention %q, got:\n%s", want, text)
		}
	}
	if strings.Contains(text, "stream error") {
		t.Fatalf("the gate must render an actionable notice, not a raw stream error:\n%s", text)
	}
	assertAgenticBindingIntact(t, m)
	assertNoReversionNotice(t, m)
}

// TestLoadRecentBindingsRetainsAgentic pins the Adaptive Runtime contract: the
// RECENTLY USED list never hides a discovered model, so agentic-harness models
// remain visible and selectable.
func TestLoadRecentBindingsRetainsAgentic(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := config.SaveRecentModels([]config.RecentModelEntry{
		{Provider: "openrouter", ModelID: "thinkingmachines/inkling-small:free"},
		{Provider: "openrouter", ModelID: "thinkingmachines/inkling:free"},
		{Provider: "openrouter", ModelID: "anthropic/claude-3.5-sonnet"},
	}); err != nil {
		t.Fatalf("SaveRecentModels: %v", err)
	}

	got := loadRecentBindings()
	if len(got) != 3 {
		t.Fatalf("loadRecentBindings = %+v, want all 3 remembered models (no hiding)", got)
	}
}
