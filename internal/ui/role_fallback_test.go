package ui

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/providers"
)

// recordingProvider is a scripted ai.Provider: each queued outcome is either a
// stream body or an error. It records every dispatched request so tests can
// prove which model actually reached the wire.
type recordingProvider struct {
	name     string
	outcomes []providerOutcome
	mu       sync.Mutex
	requests []ai.Request
}

type providerOutcome struct {
	content string
	err     error
}

func (p *recordingProvider) Name() string { return p.name }

func (p *recordingProvider) record(req ai.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, req)
}

func (p *recordingProvider) dispatchedModels() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.requests))
	for _, r := range p.requests {
		out = append(out, r.Model)
	}
	return out
}

func (p *recordingProvider) next(req ai.Request) (io.ReadCloser, error) {
	p.record(req)
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.outcomes) == 0 {
		return nil, fmt.Errorf("%s: no outcome queued", p.name)
	}
	out := p.outcomes[0]
	p.outcomes = p.outcomes[1:]
	if out.err != nil {
		return nil, out.err
	}
	return io.NopCloser(strings.NewReader(out.content)), nil
}

func (p *recordingProvider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	rc, err := p.next(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	body, _ := io.ReadAll(rc)
	return &ai.Response{Content: string(body)}, nil
}

func (p *recordingProvider) ExecuteStream(_ context.Context, req ai.Request) (io.ReadCloser, error) {
	return p.next(req)
}

func rateLimitErr() error {
	return providers.NewProviderError("openrouter", http.StatusTooManyRequests, []byte(`{"error":{"message":"rate limit exceeded"}}`))
}

func serverErr() error {
	return providers.NewProviderError("openrouter", http.StatusBadGateway, []byte(`{"error":{"message":"upstream unavailable"}}`))
}

// newRoleFallbackModel builds a chat model with an explicit role chain:
// plan.primary = the agentic model, plan.fallback = the stable model, both on
// the openrouter provider. default.fallback is intentionally unconfigured so a
// non-plan turn can never silently switch models.
func newRoleFallbackModel(t *testing.T) (*model, *recordingProvider) {
	t.Helper()
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
	primary := &recordingProvider{name: "openrouter"}
	m.provider = primary
	return m, primary
}

// TestRoleFallbackPlanResolves: the configured plan chain resolves to the
// fallback model on the declared provider.
func TestRoleFallbackPlanResolves(t *testing.T) {
	m, _ := newRoleFallbackModel(t)
	plan := m.planRoleFallback(ai.Request{Model: "thinkingmachines/inkling-small:free"})
	if plan == nil {
		t.Fatal("planRoleFallback must resolve the configured plan chain")
	}
	if plan.request.Model != "anthropic/claude-3.5-sonnet" {
		t.Fatalf("fallback request model = %q, want anthropic/claude-3.5-sonnet", plan.request.Model)
	}
	if plan.chain.Primary != "thinkingmachines/inkling-small:free" || plan.chain.Role != "plan" {
		t.Fatalf("chain provenance = %+v", plan.chain)
	}
}

// TestRoleFallbackSkippedWhenNotConfigured: with no roles configured there is
// no chain and therefore no model switch of any kind.
func TestRoleFallbackSkippedWhenNotConfigured(t *testing.T) {
	m, _ := newRoleFallbackModel(t)
	m.cfg.Roles = nil
	if plan := m.planRoleFallback(ai.Request{Model: "thinkingmachines/inkling-small:free"}); plan != nil {
		t.Fatalf("no roles configured must yield no plan, got %+v", plan)
	}
}

// TestRoleFallbackFiresOnRateLimit: a 429 on the primary retries the turn on
// the configured fallback and reports the explicit switch.
func TestRoleFallbackFiresOnRateLimit(t *testing.T) {
	m, primary := newRoleFallbackModel(t)
	primary.outcomes = []providerOutcome{{err: rateLimitErr()}, {content: "fallback answer"}}

	var switches []roleFallbackMsg
	raw, err := executeStreamWithRoleFallback(context.Background(), primary,
		ai.Request{Model: "thinkingmachines/inkling-small:free"},
		m.planRoleFallback(ai.Request{Model: "thinkingmachines/inkling-small:free"}),
		func(msg roleFallbackMsg) { switches = append(switches, msg) })
	if err != nil {
		t.Fatalf("chain dispatch error = %v, want the fallback answer", err)
	}
	body, _ := io.ReadAll(raw)
	_ = raw.Close()
	if string(body) != "fallback answer" {
		t.Fatalf("stream body = %q, want the fallback model answer", string(body))
	}
	if got := primary.dispatchedModels(); len(got) != 2 || got[1] != "anthropic/claude-3.5-sonnet" {
		t.Fatalf("dispatched models = %v, want [primary fallback]", got)
	}
	if len(switches) != 1 {
		t.Fatalf("switches = %d, want exactly 1", len(switches))
	}
	sw := switches[0]
	if sw.Primary != "thinkingmachines/inkling-small:free" || sw.Fallback != "openrouter/anthropic/claude-3.5-sonnet" {
		t.Fatalf("switch = %+v", sw)
	}
	if !strings.Contains(sw.Reason, "429") {
		t.Fatalf("reason = %q, want the 429 rate-limit cause", sw.Reason)
	}
	want := "[fallback] Primary model failed (rate limit (HTTP 429)). Switched to openrouter/anthropic/claude-3.5-sonnet."
	if got := fallbackNotice(sw); got != want {
		t.Fatalf("notice = %q, want %q", got, want)
	}
	// The user's active binding is never rewritten by a fallback.
	if got := m.getActiveModelName(); got != "thinkingmachines/inkling-small:free" {
		t.Fatalf("active model = %q, want the selected primary retained", got)
	}
}

// TestRoleFallbackFiresOnServerError: HTTP 5xx is a fallback trigger.
func TestRoleFallbackFiresOnServerError(t *testing.T) {
	m, primary := newRoleFallbackModel(t)
	primary.outcomes = []providerOutcome{{err: serverErr()}, {content: "recovered"}}

	var switches []roleFallbackMsg
	raw, err := executeStreamWithRoleFallback(context.Background(), primary,
		ai.Request{Model: "thinkingmachines/inkling-small:free"},
		m.planRoleFallback(ai.Request{Model: "thinkingmachines/inkling-small:free"}),
		func(msg roleFallbackMsg) { switches = append(switches, msg) })
	if err != nil {
		t.Fatalf("chain dispatch error = %v", err)
	}
	defer func() { _ = raw.Close() }()
	if len(switches) != 1 || !strings.Contains(switches[0].Reason, "server error") {
		t.Fatalf("switches = %+v, want one server-error switch", switches)
	}
}

// TestRoleFallbackFiresOnTimeout: a context/network timeout is a fallback
// trigger.
func TestRoleFallbackFiresOnTimeout(t *testing.T) {
	m, primary := newRoleFallbackModel(t)
	primary.outcomes = []providerOutcome{{err: context.DeadlineExceeded}, {content: "late answer"}}

	var switches []roleFallbackMsg
	raw, err := executeStreamWithRoleFallback(context.Background(), primary,
		ai.Request{Model: "thinkingmachines/inkling-small:free"},
		m.planRoleFallback(ai.Request{Model: "thinkingmachines/inkling-small:free"}),
		func(msg roleFallbackMsg) { switches = append(switches, msg) })
	if err != nil {
		t.Fatalf("chain dispatch error = %v", err)
	}
	defer func() { _ = raw.Close() }()
	if len(switches) != 1 || !strings.Contains(switches[0].Reason, "timeout") {
		t.Fatalf("switches = %+v, want one timeout switch", switches)
	}
}

// TestNoFallbackOnWirePolicyRefusal: an agentic-harness refusal is Dynamic
// Contract Promotion territory — the chain must NOT fire and the error must
// surface verbatim.
func TestNoFallbackOnWirePolicyRefusal(t *testing.T) {
	m, primary := newRoleFallbackModel(t)
	primary.outcomes = []providerOutcome{
		{err: fmt.Errorf("%w: agentic harness required", providers.ErrOpenRouterModelIncompatible)},
		{content: "must never be reached"},
	}
	var switches []roleFallbackMsg
	_, err := executeStreamWithRoleFallback(context.Background(), primary,
		ai.Request{Model: "thinkingmachines/inkling-small:free"},
		m.planRoleFallback(ai.Request{Model: "thinkingmachines/inkling-small:free"}),
		func(msg roleFallbackMsg) { switches = append(switches, msg) })
	if err == nil || !strings.Contains(err.Error(), "agentic harness required") {
		t.Fatalf("err = %v, want the wire-policy refusal verbatim", err)
	}
	if len(switches) != 0 {
		t.Fatalf("a wire-policy refusal must not switch models, got %+v", switches)
	}
	if got := primary.dispatchedModels(); len(got) != 1 {
		t.Fatalf("dispatched models = %v, want only the primary attempt", got)
	}
}

// TestNoFallbackOnClientError: 4xx client errors are request bugs, not
// transient infrastructure — no switch.
func TestNoFallbackOnClientError(t *testing.T) {
	m, primary := newRoleFallbackModel(t)
	primary.outcomes = []providerOutcome{
		{err: providers.NewProviderError("openrouter", http.StatusBadRequest, []byte(`{"error":{"message":"max_tokens too low"}}`))},
		{content: "must never be reached"},
	}
	var switches []roleFallbackMsg
	_, err := executeStreamWithRoleFallback(context.Background(), primary,
		ai.Request{Model: "thinkingmachines/inkling-small:free"},
		m.planRoleFallback(ai.Request{Model: "thinkingmachines/inkling-small:free"}),
		func(msg roleFallbackMsg) { switches = append(switches, msg) })
	if err == nil {
		t.Fatal("a 400 must surface as an error")
	}
	if len(switches) != 0 {
		t.Fatalf("a 4xx must not switch models, got %+v", switches)
	}
}

// TestNoFallbackLoop: a failing fallback is NOT retried again — the chain is
// one hop, and the real error surfaces.
func TestNoFallbackLoop(t *testing.T) {
	m, primary := newRoleFallbackModel(t)
	primary.outcomes = []providerOutcome{{err: rateLimitErr()}, {err: serverErr()}}
	var switches []roleFallbackMsg
	_, err := executeStreamWithRoleFallback(context.Background(), primary,
		ai.Request{Model: "thinkingmachines/inkling-small:free"},
		m.planRoleFallback(ai.Request{Model: "thinkingmachines/inkling-small:free"}),
		func(msg roleFallbackMsg) { switches = append(switches, msg) })
	if err == nil {
		t.Fatal("the fallback failure must surface")
	}
	if got := primary.dispatchedModels(); len(got) != 2 {
		t.Fatalf("dispatched models = %v, want exactly 2 attempts (no loop)", got)
	}
	if len(switches) != 1 {
		t.Fatalf("switches = %d, want exactly 1", len(switches))
	}
}

// TestFallbackNoticeRendersToTrace: the explicit switch event lands in the
// trace view as a system record.
func TestFallbackNoticeRendersToTrace(t *testing.T) {
	m := newTestModel()
	um, _ := m.Update(roleFallbackNoticeMsg{
		notice:   "[fallback] Primary model failed (rate limit (HTTP 429)). Switched to openrouter/anthropic/claude-3.5-sonnet.",
		primary:  "thinkingmachines/inkling-small:free",
		fallback: "openrouter/anthropic/claude-3.5-sonnet",
		reason:   "rate limit (HTTP 429)",
		role:     "plan",
	})
	m = um.(*model)
	text := recordsText(m)
	if !strings.Contains(text, "[fallback] Primary model failed (rate limit (HTTP 429)). Switched to openrouter/anthropic/claude-3.5-sonnet.") {
		t.Fatalf("trace view must show the explicit fallback event, got:\n%s", text)
	}
}
