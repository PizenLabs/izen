package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/providers"
	appruntime "github.com/PizenLabs/izen/internal/runtime"
	"github.com/PizenLabs/izen/internal/runtime/authority"
)

// TestAskStreamsAgenticModelWithoutReversion is the end-to-end DoD for the
// adaptive runtime: /ask against the agentic-harness model
// (thinkingmachines/inkling-small:free) streams a real textual answer and the
// trace view carries ZERO model-reversion warnings.
func TestAskStreamsAgenticModelWithoutReversion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubPersistBinding(t)

	m := readyChatModel(newTestModel())
	m.cfg = config.Default()
	m.cfg.Bindings.Active = config.ActiveBindingConfig{
		Provider: "openrouter",
		Model:    "thinkingmachines/inkling-small:free",
	}
	m.modelAuthority = appruntime.NewRuntimeAuthority()
	m.modelAuthority.Activate(authority.ModelBinding{
		ProviderID: authority.ProviderID("openrouter"),
		ModelID:    authority.ModelID("thinkingmachines/inkling-small:free"),
	})
	provider := &recordingProvider{
		name:     "openrouter",
		outcomes: []providerOutcome{{content: "The answer is 42."}},
	}
	m.provider = provider

	if cmd := m.streamCmd("what is the answer?"); cmd == nil {
		t.Fatalf("streamCmd must dispatch, got nil (records:\n%s)", recordsText(m))
	}

	// The producer goroutine feeds m.streamCh; drain it into the Update loop
	// exactly like the Bubble Tea runtime does.
	deadline := time.After(5 * time.Second)
	var sawDone bool
	var gotText string
	for !sawDone {
		select {
		case msg := <-m.streamCh:
			switch typed := msg.(type) {
			case streamDoneMsg:
				gotText = typed.content
				sawDone = true
			case streamErrMsg:
				t.Fatalf("stream error: %v", typed.err)
			default:
				um, _ := m.Update(msg)
				m = um.(*model)
			}
		case <-deadline:
			t.Fatalf("stream did not complete; records:\n%s", recordsText(m))
		}
	}

	if !strings.Contains(gotText, "The answer is 42.") {
		t.Fatalf("streamed content = %q, want the provider answer", gotText)
	}
	if got := provider.dispatchedModels(); len(got) != 1 || got[0] != "thinkingmachines/inkling-small:free" {
		t.Fatalf("dispatched models = %v, want the selected agentic model exactly once", got)
	}
	assertNoReversionNotice(t, m)
	if got := m.getActiveModelName(); got != "thinkingmachines/inkling-small:free" {
		t.Fatalf("active model = %q, want the selected model retained", got)
	}
}

// TestAskWithRoleFallbackStreamsFallbackAnswer is the end-to-end DoD for the
// explicit role chain: the primary model answers 429 on the wire, the turn is
// retried on the configured fallback, the answer streams, and the trace shows
// the explicit [fallback] event (never a reversion notice).
func TestAskWithRoleFallbackStreamsFallbackAnswer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubPersistBinding(t)

	m := readyChatModel(newTestModel())
	m.resolver.Set(modes.ModePlan)
	m.cfg = config.Default()
	m.cfg.Bindings.Active = config.ActiveBindingConfig{
		Provider: "openrouter",
		Model:    "thinkingmachines/inkling-small:free",
	}
	m.cfg.Roles = map[string]config.RoleFallbackConfig{
		"plan": {
			Model:    "openrouter/thinkingmachines/inkling-small:free",
			Fallback: "openrouter/anthropic/claude-3.5-sonnet",
		},
	}
	m.modelAuthority = appruntime.NewRuntimeAuthority()
	m.modelAuthority.Activate(authority.ModelBinding{
		ProviderID: authority.ProviderID("openrouter"),
		ModelID:    authority.ModelID("thinkingmachines/inkling-small:free"),
	})
	provider := &recordingProvider{
		name: "openrouter",
		outcomes: []providerOutcome{
			{err: rateLimitErr()},
			{content: "Recovered on the fallback model."},
		},
	}
	m.provider = provider

	if cmd := m.streamCmd("what is the answer?"); cmd == nil {
		t.Fatalf("streamCmd must dispatch, got nil (records:\n%s)", recordsText(m))
	}

	deadline := time.After(5 * time.Second)
	var gotText string
	var sawDone, sawNotice bool
	for !sawDone {
		select {
		case msg := <-m.streamCh:
			switch typed := msg.(type) {
			case streamDoneMsg:
				gotText = typed.content
				sawDone = true
			case streamErrMsg:
				t.Fatalf("stream error: %v", typed.err)
			case roleFallbackNoticeMsg:
				sawNotice = true
				um, _ := m.Update(msg)
				m = um.(*model)
			default:
				um, _ := m.Update(msg)
				m = um.(*model)
			}
		case <-deadline:
			t.Fatalf("stream did not complete; records:\n%s", recordsText(m))
		}
	}

	if !strings.Contains(gotText, "Recovered on the fallback model.") {
		t.Fatalf("streamed content = %q, want the fallback answer", gotText)
	}
	if !sawNotice {
		t.Fatal("the role chain switch must emit an explicit fallback event")
	}
	if got := provider.dispatchedModels(); len(got) != 2 ||
		got[0] != "thinkingmachines/inkling-small:free" || got[1] != "anthropic/claude-3.5-sonnet" {
		t.Fatalf("dispatched models = %v, want [primary fallback]", got)
	}
	text := recordsText(m)
	if !strings.Contains(text, "[fallback] Primary model failed (rate limit (HTTP 429)). Switched to openrouter/anthropic/claude-3.5-sonnet.") {
		t.Fatalf("trace view must show the explicit fallback event, got:\n%s", text)
	}
	assertNoReversionNotice(t, m)
	if got := m.getActiveModelName(); got != "thinkingmachines/inkling-small:free" {
		t.Fatalf("active model = %q, want the user's selection untouched by the chain", got)
	}
}

// TestAskWirePolicyRefusalNeverFallsBack: a wire-policy refusal surfaces as the
// provider error with no chain switch and no reversion.
func TestAskWirePolicyRefusalNeverFallsBack(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubPersistBinding(t)

	m := readyChatModel(newTestModel())
	m.resolver.Set(modes.ModePlan)
	m.cfg = config.Default()
	m.cfg.Bindings.Active = config.ActiveBindingConfig{
		Provider: "openrouter",
		Model:    "thinkingmachines/inkling-small:free",
	}
	m.cfg.Roles = map[string]config.RoleFallbackConfig{
		"plan": {Model: "openrouter/thinkingmachines/inkling-small:free", Fallback: "openrouter/anthropic/claude-3.5-sonnet"},
	}
	provider := &recordingProvider{
		name: "openrouter",
		outcomes: []providerOutcome{{
			err: fmt.Errorf("%w: only available on agentic harnesses", providers.ErrOpenRouterModelIncompatible),
		}},
	}
	m.provider = provider

	if cmd := m.streamCmd("what is the answer?"); cmd == nil {
		t.Fatalf("streamCmd must dispatch, got nil (records:\n%s)", recordsText(m))
	}

	deadline := time.After(5 * time.Second)
	var sawErr bool
	for !sawErr {
		select {
		case msg := <-m.streamCh:
			if _, ok := msg.(streamErrMsg); ok {
				sawErr = true
				continue
			}
			um, _ := m.Update(msg)
			m = um.(*model)
		case <-deadline:
			t.Fatalf("stream did not terminate; records:\n%s", recordsText(m))
		}
	}

	if got := provider.dispatchedModels(); len(got) != 1 {
		t.Fatalf("dispatched models = %v, want a single attempt (no chain switch)", got)
	}
	assertNoReversionNotice(t, m)
	if got := m.getActiveModelName(); got != "thinkingmachines/inkling-small:free" {
		t.Fatalf("active model = %q, want the selected model retained", got)
	}
}

// recordingProvider must satisfy ai.Provider; the compile-time assertion keeps
// the test honest if the interface changes.
var _ ai.Provider = (*recordingProvider)(nil)
