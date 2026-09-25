package ui

import (
	"context"
	"io"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	domainrole "github.com/PizenLabs/izen/internal/domain/role"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/providers"
)

// Role fallback execution (explicit, user-configured).
//
// Izen has NO implicit model reversion. The single model-switch mechanism is
// the role fallback chain the user declares in ~/.izen/config.yml:
//
//	roles:
//	  plan:
//	    model:    "openrouter/thinkingmachines/inkling-small:free"
//	    fallback: "openrouter/anthropic/claude-3.5-sonnet"
//
// Semantics pinned by this file:
//   - The switch fires ONLY on network-transient failures (timeout, HTTP 429,
//     HTTP 5xx). See providers.ClassifyFallbackError.
//   - It NEVER fires for a wire-policy / agentic-harness requirement: those are
//     satisfied transparently by Dynamic Contract Promotion inside the provider
//     adapter (contract promoted to ToolEnabledCompletion + read-only tools
//     bound before dispatch).
//   - It NEVER mutates the active binding. The chain is a per-turn retry: the
//     user's selected model stays selected, and the switch is reported to the
//     trace view as an explicit event.
//   - The fallback is attempted exactly once. A failing fallback surfaces the
//     real error; there is no fallback loop.

// roleFallbackPlan is the resolved per-turn retry target: which model to switch
// to and which provider executes it.
type roleFallbackPlan struct {
	chain    config.RoleChain
	provider ai.Provider
	// request is the primary request with only the model swapped, so prompts,
	// history, budget and tool promotion decisions stay identical.
	request ai.Request
}

// roleFallbackMsg reports a role-chain switch to the trace view.
type roleFallbackMsg struct {
	// Primary is the model that failed.
	Primary string
	// Fallback is the "provider/model" label of the model now executing.
	Fallback string
	// Reason is the classified cause (e.g. "rate limit (HTTP 429)").
	Reason string
	// Role is the config key whose chain fired.
	Role string
}

// currentTurnRole maps the active mode onto a semantic role key. Plan mode runs
// the "plan" role; every other surface (ask, build, review, investigate) runs
// the "default" role.
func (m *model) currentTurnRole() string {
	if m == nil || m.resolver == nil {
		return domainrole.RoleDefault
	}
	if m.resolver.Current() == modes.ModePlan {
		return domainrole.RolePlan
	}
	return domainrole.RoleDefault
}

// planRoleFallback resolves the fallback chain for this turn. It returns nil
// when no chain is configured, when the chain's fallback is the model already
// executing, or when the fallback's provider cannot be resolved.
//
// Resolution happens on the Bubble Tea goroutine (never inside the stream
// producer): the provider registry lookup and config read are both
// goroutine-affine to the UI model.
func (m *model) planRoleFallback(req ai.Request) *roleFallbackPlan {
	if m == nil || m.cfg == nil || len(m.cfg.Roles) == 0 {
		return nil
	}
	chain, ok := m.cfg.RoleChainFor(m.currentTurnRole(), req.Model, m.getActiveProviderName())
	if !ok {
		return nil
	}
	fallbackReq := req
	fallbackReq.Model = chain.Model
	// The fallback may live on another provider (e.g. primary openrouter,
	// fallback anthropic). Resolve it through the registry; when the provider
	// is unregistered or unavailable the chain is skipped and the primary error
	// surfaces unchanged (never a silent half-switch).
	target := m.provider
	if chain.Provider != "" && (m.provider == nil || m.provider.Name() != chain.Provider) {
		if m.mgr == nil {
			return nil
		}
		p, ok := m.mgr.Get(chain.Provider)
		if !ok || p == nil {
			return nil
		}
		target = m.contextCompiler.WrapProvider(p)
		// A cross-provider fallback must be able to execute agentic-harness
		// models natively too: inject the same read-only tool runner the
		// execution pipeline gives its bound provider, so Dynamic Contract
		// Promotion works on the fallback path as well.
		if setter, ok := target.(interface{ SetToolRunner(ai.ToolRunner) }); ok {
			setter.SetToolRunner(execution.NewReadOnlyToolRunner(m.workspaceRoot))
		}
	}
	if target == nil {
		return nil
	}
	return &roleFallbackPlan{chain: chain, provider: target, request: fallbackReq}
}

// executeStreamWithRoleFallback dispatches one streaming invocation and applies
// the role chain when (and only when) the primary failed for a
// network-transient reason.
//
// onSwitch is invoked exactly once, BEFORE the fallback is dispatched, so the
// switch is visible in the trace even if the fallback itself then fails. Any
// other primary error is returned verbatim: no chain, no reversion, no
// fabricated model change.
func executeStreamWithRoleFallback(
	ctx context.Context,
	primary ai.Provider,
	req ai.Request,
	plan *roleFallbackPlan,
	onSwitch func(msg roleFallbackMsg),
) (io.ReadCloser, error) {
	raw, err := primary.ExecuteStream(ctx, req)
	if err == nil || plan == nil {
		return raw, err
	}
	// Wire-policy refusals and non-transient client errors never switch models.
	if !providers.IsFallbackEligible(err) {
		if raw != nil {
			_ = raw.Close()
		}
		return nil, err
	}
	if onSwitch != nil {
		onSwitch(roleFallbackMsg{
			Primary:  plan.chain.Primary,
			Fallback: plan.chain.LabelOrEmpty(),
			Reason:   providers.FallbackReason(err),
			Role:     plan.chain.Role,
		})
	}
	return plan.provider.ExecuteStream(ctx, plan.request)
}

// fallbackNotice renders the single trace line for a role-chain switch:
//
//	[fallback] Primary model failed (rate limit (HTTP 429)). Switched to openrouter/anthropic/claude-3.5-sonnet.
func fallbackNotice(msg roleFallbackMsg) string {
	return providers.FormatFallbackEvent(msg.Fallback, msg.Reason)
}
