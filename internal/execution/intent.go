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
//
// ── WHY THE READ-ONLY CONSTRAINT IS READ HERE AND NOT IN Select ─────────────
//
// An explicit "read-only" / "do not change anything" in the request is a
// statement by the human, and it closes the mutation path exactly as an
// unauthorized scope does. It is evaluated HERE, and not inside
// strategy.Select, for one decisive reason: Select is also called on text the
// runtime itself composed.
//
// Decomposition re-classifies every sub-task by running Select over the fully
// compiled provider prompt — target context, the assigned change window, the
// artifact contract and the scoping instructions. Those instructions include
// phrases like "do not modify any other region". Scanned as if they were a
// human request, the runtime read its OWN prompt back as the user revoking
// mutation authority and downgraded a live mutation to read-only. The
// subtask's patch was then never authorized, the DAG applied no bytes, and the
// objective failed as UNSUBSTANTIATED with "no durable delta observed".
//
// The general rule this file now enforces: the semantic boundary may read a
// human REQUEST; it may never read runtime-composed TEXT as if it were one.
func (g *IntentGateway) selectScopedStrategy(prompt string, scope intentdomain.ScopeProvenance) strategy.ExecutionStrategyProfile {
	profile := g.SelectStrategy(prompt)

	// ── UNDETERMINED fails closed HERE, and only here ─────────────────
	// A request that states a goal but no workspace act — "make this project
	// better" — has no basis for a file, an operation, or a change. Choosing any
	// of them would be the runtime writing the objective instead of executing
	// it, so the run parks at the clarification boundary instead: no provider
	// call, no workspace scan, no grant.
	//
	// It is enforced at this boundary and NOT inside strategy.Select for the
	// same reason the read-only constraint is: Select is called a second time by
	// the RuntimeExecutor, AFTER admission, purely to choose budgets and an
	// artifact shape for work that is already authorized. Stopping there would
	// refuse authorized work on a classification made before anyone was
	// authorized — the authority gate and the execution planner are different
	// questions, and only the gate may refuse.
	if strategy.ClassifySemantic(prompt).IsUndetermined() && !strategy.IsCasualPrompt(prompt) {
		profile.Strategy = strategy.HumanClarification
		profile.Deterministic = true
		profile.ModelRequired = false
		profile.StrategyReason = "the request names a goal but states no workspace act; " +
			strategy.ClassifySemantic(prompt).Reason
		profile.ContextKinds = []strategy.ContextKind{strategy.ContextUserIntent}
		profile.ContextPolicy = strategy.ContextPolicyNone
		profile.Escalation = true
		profile.EscalationReason = "human clarification required before execution"
		return profile
	}
	readOnlyRequested := strategy.StatesReadOnlyConstraint(prompt)
	if scope.AllowsMutation() && !readOnlyRequested {
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
		// The two causes are reported separately. They close the same path but
		// they mean opposite things to a human reading the transcript: one is
		// the system withholding authority, the other is the user declining it.
		if readOnlyRequested {
			profile.StrategyReason = "read-only intent: the request itself states an explicit read-only constraint"
		} else {
			profile.StrategyReason = "read-only intent: mutation scope was not authorized"
		}
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
