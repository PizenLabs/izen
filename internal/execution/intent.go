package execution

import (
	"context"
	"strings"

	"github.com/PizenLabs/izen/internal/execution/strategy"
)

// ── IntentGateway (unified intent resolution) ──────────────────────────────
//
// The IntentGateway is the single entry point every user action crosses:
// bare text, $prompt, $hot — all produce an ExecutionRequest. It performs the
// deterministic resolution BEFORE any execution:
//
//	User Input
//	   |
//	   v
//	IntentGateway.Gate()      (directive stripping + Strategy.Select, unconditional)
//	   |
//	   v
//	ExecutionRequest { Strategy profile, Targets, Prompt }
//	   |
//	   v
//	RuntimeExecutor.Execute() (owns provider, context, mutation, verification)
//
// The gateway never selects a mode, never triggers a hidden /build, and never
// invokes a provider. The strategy profile it attaches is the single source of
// the execution path decision; modes are presentation context labels only.

// IntentResolution is the gateway's deterministic interpretation of one user
// action. It is observable ($inspect) and carries the reasoning for every
// decision before execution begins.
type IntentResolution struct {
	// Raw is the exact line the user submitted.
	Raw string
	// Prompt is the strategy input (directive prefix stripped).
	Prompt string
	// Directive is the recognized execution directive: "prompt", "hot", or ""
	// for bare text.
	Directive string
	// Profile is the unconditionally selected execution strategy.
	Profile strategy.ExecutionStrategyProfile
	// Targets is the resolved workspace-relative target set.
	Targets []string
	// Context is the integrity-sealed snapshot of this intent's execution
	// context payload, frozen at creation time (Phase 1 P1). It is carried on
	// the ExecuteRequest and verified at the RuntimeExecutor admission
	// boundary; any mid-flight modification fails closed there.
	Context *ContextSnapshot
}

// IntentGateway is the unified intent resolver. It is stateless beyond its
// workspace root and safe for concurrent use.
type IntentGateway struct {
	root string
}

// NewIntentGateway wires an IntentGateway over a workspace root.
func NewIntentGateway(root string) *IntentGateway {
	return &IntentGateway{root: root}
}

// SelectStrategy runs Strategy.Select UNCONDITIONALLY on the given input. It
// is the single strategy decision point of the runtime; no caller may skip it
// or replace it with a mode.
func (g *IntentGateway) SelectStrategy(prompt string) strategy.ExecutionStrategyProfile {
	deps := strategy.Deps{Root: g.root, Workspace: executorWorkspace{root: g.root}}
	return strategy.Select(prompt, deps)
}

// Gate resolves one user action into an ExecutionRequest. It never decides the
// execution path beyond what Strategy.Select decided deterministically. The
// intent's execution context payload is FROZEN here — at the point of intent
// creation — into an integrity-sealed ContextSnapshot carried on the request;
// the RuntimeExecutor admission boundary verifies it fail-closed before
// anything executes.
func (g *IntentGateway) Gate(_ context.Context, line string) (ExecuteRequest, IntentResolution, error) {
	raw := strings.TrimSpace(line)
	res := IntentResolution{Raw: raw}

	prompt := raw
	lower := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(lower, "$prompt"):
		res.Directive = "prompt"
		prompt = strings.TrimSpace(raw[len("$prompt"):])
	case strings.HasPrefix(lower, "$hot"):
		res.Directive = "hot"
		prompt = strings.TrimSpace(raw[len("$hot"):])
	}
	if prompt == "" {
		// No executable content beyond the directive marker: surface a
		// clarification rather than executing an empty request.
		profile := g.SelectStrategy(raw)
		res.Prompt = raw
		res.Profile = profile
		req := ExecuteRequest{Prompt: raw, Strategy: &profile}
		freezeGatewayContext(&req, &res, profile, g.root)
		return req, res, nil
	}
	res.Prompt = prompt

	// Strategy selection is UNCONDITIONAL: the gateway always classifies the
	// operation before any execution decides anything.
	profile := g.SelectStrategy(prompt)
	res.Profile = profile

	for _, t := range profile.Targets {
		if t.Resolved != "" {
			res.Targets = append(res.Targets, t.Resolved)
		}
	}

	req := ExecuteRequest{
		Prompt:          prompt,
		Targets:         res.Targets,
		MaxOutputTokens: profile.MaxOutputTokens,
		Strategy:        &profile,
	}
	freezeGatewayContext(&req, &res, profile, g.root)
	return req, res, nil
}

// freezeGatewayContext seals the intent context payload onto both the request
// and its observable resolution. It is the ONLY place intent contexts are born.
func freezeGatewayContext(req *ExecuteRequest, res *IntentResolution, profile strategy.ExecutionStrategyProfile, root string) {
	snapshot := freezeIntentContext("", *req, string(profile.Strategy), root)
	req.Context = snapshot
	res.Context = snapshot
}
