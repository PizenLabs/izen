package pipeline

import (
	"fmt"
	"sync"

	"github.com/PizenLabs/izen/pkg/engine/layer2"
)

// RouteProfile is the immutable result of intent-based model routing: the
// model tier, the optional provider label and the Layer 2 context policy the
// engine must execute the request under.
type RouteProfile struct {
	// Intent the profile was resolved for.
	Intent Intent
	// Model is the concrete model name to call.
	Model string
	// Provider is the provider label for the model, when known.
	Provider string
	// Policy is the Layer 2 context budget the intent is allowed.
	Policy layer2.ContextPolicy
	// Reason is a short human-readable justification for the route.
	Reason string
}

// Valid reports whether the profile carries a resolvable model and a valid
// policy.
func (p RouteProfile) Valid() bool {
	return p.Model != "" && p.Policy.Valid()
}

// DefaultPolicies are the per-intent Layer 2 context budgets enforced by the
// engine. The budget numbers are deliberately conservative: reasoning holds
// the largest window, execution a balanced one, informational the smallest.
var DefaultPolicies = map[Intent]layer2.ContextPolicy{
	IntentReasoning: {
		MaxTokenBudget:     24000,
		MaxFiles:           20,
		MaxSymbols:         1024,
		AllowBinary:        false,
		ExpandDependencies: true,
		CompressionRatio:   0.4,
	},
	IntentExecution: {
		MaxTokenBudget:     16000,
		MaxFiles:           16,
		MaxSymbols:         512,
		AllowBinary:        false,
		ExpandDependencies: true,
		CompressionRatio:   0.4,
	},
	IntentInformational: {
		MaxTokenBudget:     8000,
		MaxFiles:           8,
		MaxSymbols:         256,
		AllowBinary:        false,
		ExpandDependencies: true,
		CompressionRatio:   0.6,
	},
}

// Router resolves a RouteProfile for a request. It is immutable after
// construction (models and policies are set once) and safe for concurrent use.
type Router struct {
	mu        sync.RWMutex
	models    map[Intent]string
	providers map[Intent]string
	policies  map[Intent]layer2.ContextPolicy
	fallback  string
}

// RouterOption configures a Router at construction time.
type RouterOption func(*Router)

// WithModel pins the model for an intent. An empty model leaves the router
// fallback (or default) in place.
func WithModel(i Intent, model string) RouterOption {
	return func(r *Router) {
		if model != "" {
			r.models[i] = model
		}
	}
}

// WithProvider pins the provider label for an intent.
func WithProvider(i Intent, provider string) RouterOption {
	return func(r *Router) {
		if provider != "" {
			r.providers[i] = provider
		}
	}
}

// WithPolicy overrides the default Layer 2 context budget for an intent. An
// invalid policy is ignored so the router always stays safe.
func WithPolicy(i Intent, p layer2.ContextPolicy) RouterOption {
	return func(r *Router) {
		if p.Valid() {
			r.policies[i] = p
		}
	}
}

// WithFallbackModel sets the model used for intents with no explicit pin.
func WithFallbackModel(model string) RouterOption {
	return func(r *Router) {
		if model != "" {
			r.fallback = model
		}
	}
}

// NewRouter returns a router with the default per-intent context policies.
// The fallback model defaults to "qwen2.5-coder:7b"; callers that resolve
// models from configuration must inject them via WithModel.
func NewRouter(opts ...RouterOption) *Router {
	r := &Router{
		models:    make(map[Intent]string, len(allIntents)),
		providers: make(map[Intent]string, len(allIntents)),
		policies:  make(map[Intent]layer2.ContextPolicy, len(allIntents)),
		fallback:  "qwen2.5-coder:7b",
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// FallbackModel returns the fallback model name.
func (r *Router) FallbackModel() string {
	if r == nil {
		return ""
	}
	return r.fallback
}

// SyncTiers re-pins the per-intent model and provider selections from a
// resolver. It is the runtime refresh seam: callers that switch the active
// provider or model tier after construction (e.g. /model or a config reload)
// invoke it so the router never serves a model that was pinned to a
// no-longer-active provider (an Ollama model leaking into an OpenRouter
// request fails with HTTP 400 "not a valid model ID"). Empty models returned
// by the resolver leave the current pin in place; empty providers leave the
// current provider pin in place.
func (r *Router) SyncTiers(resolve func(Intent) (model, provider string)) {
	if r == nil || resolve == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, i := range allIntents {
		model, provider := resolve(i)
		if model != "" {
			r.models[i] = model
			if i == IntentExecution {
				r.fallback = model
			}
		}
		if provider != "" {
			r.providers[i] = provider
		}
	}
}

// Policy returns the context policy the router uses for an intent. It never
// returns an invalid policy; unknown intents fall back to the execution
// policy.
func (r *Router) Policy(i Intent) layer2.ContextPolicy {
	if r == nil {
		return DefaultPolicies[IntentExecution]
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if p, ok := r.policies[i]; ok && p.Valid() {
		return p
	}
	if !i.Valid() {
		i = IntentExecution
	}
	if p, ok := r.policies[i]; ok {
		return p
	}
	return DefaultPolicies[i]
}

// RouteFor resolves the profile for an explicit intent.
func (r *Router) RouteFor(i Intent) RouteProfile {
	if !i.Valid() {
		i = IntentExecution
	}
	r.mu.RLock()
	model := r.models[i]
	provider := r.providers[i]
	policy := r.policies[i]
	fallback := r.fallback
	r.mu.RUnlock()

	if model == "" {
		model = fallback
	}
	if !policy.Valid() {
		policy = DefaultPolicies[i]
	}
	return RouteProfile{
		Intent:   i,
		Model:    model,
		Provider: provider,
		Policy:   policy,
		Reason:   reasonFor(i),
	}
}

// RouteForMode resolves the profile for a mode name via IntentForMode. It is
// the routing entry point used by the orchestrator and the presentation layer.
func (r *Router) RouteForMode(mode string) RouteProfile {
	return r.RouteFor(IntentForMode(mode))
}

// reasonFor renders the human-readable routing justification.
func reasonFor(i Intent) string {
	switch i {
	case IntentReasoning:
		return "heavy-reasoning intent: high-capability model, strict context budget"
	case IntentExecution:
		return "execution intent: fast coding model, balanced context budget"
	case IntentInformational:
		return "informational intent: read-only, minimal context policy"
	default:
		return fmt.Sprintf("unknown intent %q routed as execution", i)
	}
}
