package execution

import (
	"context"
	"errors"
	"strings"

	intentdomain "github.com/PizenLabs/izen/internal/core/domain"
	"github.com/PizenLabs/izen/internal/domain/command"
	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/parser"
	"github.com/PizenLabs/izen/internal/protocol"
)

// ── IntentGateway (unified intent resolution) ──────────────────────────────
//
// The IntentGateway is the single entry point every user action crosses:
// bare text, $prompt, $hot, /build — all produce an ExecutionRequest. It performs the
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
	ScopeProvenance intentdomain.ScopeProvenance
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
	// InteractionContract and Contract are the normalized semantic descriptor
	// selected at the intent boundary and carried unchanged to execution
	// admission. They constrain the request; they never grant authority.
	InteractionContract protocol.InteractionContract
	Contract            *protocol.ContractDescriptor
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
	ast, err := parser.ParseInWorkspace(raw, nil, command.WorkspaceBuild)
	if err != nil {
		return ExecuteRequest{}, res, err
	}
	res.ScopeProvenance = ast.ScopeProvenance
	for _, d := range ast.Directives {
		if d.Name == "prompt" || d.Name == "hot" {
			res.Directive = d.Name
		}
	}
	if ast.Workspace == command.WorkspaceBuild && strings.HasPrefix(raw, "/build") && !res.ScopeProvenance.AllowsMutation() {
		return ExecuteRequest{}, res, errors.New(intentdomain.ScopeAuthorizationError)
	}
	if res.Directive != "" {
		var stripped strings.Builder
		start := 0
		for _, token := range parser.Tokenize(raw) {
			if token.Kind != parser.TokenCommand || token.Marker == command.MarkerAt {
				continue
			}
			stripped.WriteString(raw[start:token.Pos.Offset])
			start = token.Pos.Offset + 1 + len(token.Name)
		}
		stripped.WriteString(raw[start:])
		prompt = strings.TrimSpace(stripped.String())
	}
	if prompt == "" {
		// No executable content beyond the directive marker: surface a
		// clarification rather than executing an empty request.
		profile := g.selectScopedStrategy(raw, res.ScopeProvenance)
		res.Prompt = raw
		res.Profile = profile
		req := ExecuteRequest{Prompt: raw, Strategy: &profile, ScopeProvenance: res.ScopeProvenance}
		bindGatewayContract(&req, &res, profile)
		freezeGatewayContext(&req, &res, profile, g.root)
		return req, res, nil
	}
	res.Prompt = prompt

	// Strategy selection is UNCONDITIONAL: the gateway always classifies the
	// operation before any execution decides anything.
	profile := g.selectScopedStrategy(prompt, res.ScopeProvenance)
	res.Profile = profile

	for _, t := range profile.Targets {
		if t.Resolved != "" {
			res.Targets = append(res.Targets, t.Resolved)
		}
	}

	req := ExecuteRequest{
		ScopeProvenance: res.ScopeProvenance,
		Prompt:          prompt,
		Targets:         res.Targets,
		MaxOutputTokens: profile.MaxOutputTokens,
		Strategy:        &profile,
	}
	bindGatewayContract(&req, &res, profile)
	freezeGatewayContext(&req, &res, profile, g.root)
	return req, res, nil
}

// selectScopedStrategy compiles an unauthorized goal to an observational graph,
// even when its natural-language operation asks for file creation or mutation.
func (g *IntentGateway) selectScopedStrategy(prompt string, scope intentdomain.ScopeProvenance) strategy.ExecutionStrategyProfile {
	profile := g.SelectStrategy(prompt)
	if scope.AllowsMutation() {
		return profile
	}
	switch profile.Strategy {
	case strategy.DirectDeterministic, strategy.TargetedMutation, strategy.MultiFilePlanning:
		profile.Strategy = strategy.RepositoryInvestigation
		profile.ContextPolicy = strategy.ContextPolicyRepository
		profile.ContextKinds = []strategy.ContextKind{strategy.ContextUserIntent, strategy.ContextRepositoryConstraints, strategy.ContextDependencyEvidence}
		if profile.FileCount() > 0 {
			profile.Strategy = strategy.TargetedReasoning
			profile.ContextPolicy = strategy.ContextPolicyTargetFileOnly
			profile.ContextKinds = []strategy.ContextKind{strategy.ContextUserIntent, strategy.ContextExplicitTargets, strategy.ContextTargetContent}
		}
		profile.StrategyReason = "read-only intent: mutation scope was not authorized"
		profile.ModelRequired, profile.Deterministic = true, false
		profile.ModelDecision = "investigate the request and explain a plan without producing or applying file mutations"
		profile.Artifact = strategy.ArtifactContract{Kind: "explanation", Bounded: true, Description: "read-only investigation or plan"}
	}
	return profile
}

// bindGatewayContract attaches the semantic descriptor selected at the intent
// boundary. A scope-authorized mutation receives the bounded agentic contract;
// an unauthorized mutation remains read-only, and planning receives the
// structured proposal contract. The descriptor is copied onto the request and
// resolution so admission never has to reconstruct an untyped operation.
func bindGatewayContract(req *ExecuteRequest, res *IntentResolution, profile strategy.ExecutionStrategyProfile) {
	if req == nil {
		return
	}
	contract := protocol.DirectCompletion
	switch {
	case profile.Strategy == strategy.MultiFilePlanning:
		contract = protocol.StructuredCompletion
	case req.ScopeProvenance.AllowsMutation() &&
		(profile.Strategy == strategy.TargetedMutation || profile.Strategy == strategy.DirectDeterministic):
		contract = protocol.AgenticLoop
	case strings.Contains(strings.ToLower(req.Prompt), "json") || strings.Contains(strings.ToLower(req.Prompt), "schema"):
		contract = protocol.StructuredCompletion
	}
	descriptor := protocol.Describe(contract)
	req.InteractionContract = contract
	req.Contract = &descriptor
	if res != nil {
		copy := descriptor.Clone()
		res.InteractionContract = contract
		res.Contract = &copy
	}
}

// freezeGatewayContext seals the intent context payload onto both the request
// and its observable resolution. It is the ONLY place intent contexts are born.
func freezeGatewayContext(req *ExecuteRequest, res *IntentResolution, profile strategy.ExecutionStrategyProfile, root string) {
	snapshot := freezeIntentContext("", *req, string(profile.Strategy), root)
	req.Context = snapshot
	res.Context = snapshot
}
