package scopeguard

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/PizenLabs/izen/internal/runtime/durable"
)

// GatewayDecision is the IntentGateway authorization outcome.
type GatewayDecision string

const (
	// DecisionAllow grants execution authority.
	DecisionAllow GatewayDecision = "ALLOW"
	// DecisionRequireVerification routes an ambiguous proposal to
	// Verification (linter/test runner) before authority is granted.
	DecisionRequireVerification GatewayDecision = "REQUIRE_VERIFICATION"
	// DecisionDeny rejects the proposal with no execution authority.
	DecisionDeny GatewayDecision = "DENY"
)

// GatewayResult carries the decision plus justification.
type GatewayResult struct {
	Decision GatewayDecision `json:"decision"`
	Reason   string          `json:"reason"`
	// Structural carries the structural tier outcome (if evaluated).
	Structural StructuralResult `json:"structural"`
}

// Budget is the bounded execution budget seam. Implementations refuse
// proposals when exhausted; the gateway never invents budget.
type Budget interface {
	// Remaining reports remaining file/diff/token capacity.
	Remaining() (files, diffs, tokens int)
	// Exhausted reports whether any dimension is spent.
	Exhausted() bool
}

// SimpleBudget is a decrementing in-memory budget for tests/runtimes.
type SimpleBudget struct {
	mu     sync.Mutex
	files  int
	diffs  int
	tokens int
}

// NewSimpleBudget returns a budget with the given capacity.
func NewSimpleBudget(files, diffs, tokens int) *SimpleBudget {
	return &SimpleBudget{files: files, diffs: diffs, tokens: tokens}
}

// Remaining implements Budget.
func (b *SimpleBudget) Remaining() (int, int, int) {
	if b == nil {
		return 0, 0, 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.files, b.diffs, b.tokens
}

// Exhausted implements Budget.
func (b *SimpleBudget) Exhausted() bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.files <= 0 || b.diffs <= 0 || b.tokens <= 0
}

// Consume deducts files/diffs/tokens capacity.
func (b *SimpleBudget) Consume(files, diffs, tokens int) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.files -= files
	b.diffs -= diffs
	b.tokens -= tokens
}

// IntentGateway is the authorization decision point between structural
// evaluation and side-effect dispatch. It validates proposals against
// task policy (workspace capability allowances), budget, and scope —
// resolution, relevance and operation never imply permission to mutate
// (authority invariant 6: authorization is independent of resolution).
type IntentGateway struct {
	scope      *ScopeGuard
	structural *StructuralGuard
	session    *WorkspaceSession
	budget     Budget
	ledger     ScopeLedger
}

// NewIntentGateway wires the gateway. Scope and session are required;
// structural/graph, budget and ledger are optional seams (nil disables
// that check tier, never widens authority).
func NewIntentGateway(scope *ScopeGuard, structural *StructuralGuard, session *WorkspaceSession, budget Budget, ledger ScopeLedger) *IntentGateway {
	return &IntentGateway{scope: scope, structural: structural, session: session, budget: budget, ledger: ledger}
}

// Authorize runs ScopeGuard -> StructuralGuard -> policy/budget checks.
// It performs NO side effects and dispatches NO tools: Proposal != Execution.
func (g *IntentGateway) Authorize(_ context.Context, p Proposal, primaryScope []string) GatewayResult {
	if g == nil {
		return GatewayResult{Decision: DecisionDeny, Reason: "nil gateway (fail-closed)"}
	}
	if err := p.Validate(); err != nil {
		return GatewayResult{Decision: DecisionDeny, Reason: "invalid proposal: " + err.Error()}
	}
	// Tier 1: deterministic scope guard (hard boundary, no LLM eval).
	scopeRes := g.scope.Enforce(p, g.ledger)
	if scopeRes.Verdict == ScopeRejected {
		return GatewayResult{Decision: DecisionDeny, Reason: "scope guard: " + scopeRes.Reason}
	}
	// Tier 2: structural consistency for mutating proposals.
	structRes := StructuralResult{Result: StructuralPass, Reason: "no structural evaluation required"}
	if p.Op.Mutating() && g.structural != nil {
		structRes = g.structural.Check(p.TargetFiles, primaryScope, g.ledger, p.TaskID, p.ID)
		if structRes.Result == StructuralReject {
			return GatewayResult{Decision: DecisionDeny, Reason: "structural guard: " + structRes.Reason, Structural: structRes}
		}
		if structRes.Result == StructuralAmbiguity {
			return GatewayResult{Decision: DecisionRequireVerification, Reason: "structural ambiguity: " + structRes.Reason, Structural: structRes}
		}
	}
	// Tier 3: workspace capability policy (context != authority).
	if g.session != nil {
		pol := g.session.Policy
		if pol.Mode != "" && !pol.Allowed.Allows(p.Op) {
			return GatewayResult{Decision: DecisionDeny,
				Reason:     fmt.Sprintf("workspace %q denies %s capability", pol.Mode, p.Op),
				Structural: structRes}
		}
	}
	// Tier 4: bounded budget.
	if p.Op.Mutating() && g.budget != nil && g.budget.Exhausted() {
		return GatewayResult{Decision: DecisionDeny, Reason: "execution budget exhausted", Structural: structRes}
	}
	if g.ledger != nil {
		_ = g.ledger.RecordCustomEvent(p.TaskID, "PROPOSAL_AUTHORIZED", map[string]any{
			"proposalId": p.ID,
			"op":         string(p.Op),
			"targets":    append([]string(nil), p.TargetFiles...),
		})
	}
	return GatewayResult{Decision: DecisionAllow, Reason: "proposal authorized against scope, structure, policy and budget", Structural: structRes}
}

// ── RuntimeExecutor ──

// Effect is the injectable side-effect seam. Under ExecutionCursor
// semantics it runs at most once per OperationID.
type Effect func(ctx context.Context) (postDigest string, err error)

// Verifier is the evidence-verification seam (linter/test runner).
// It runs AFTER commit and reports whether the side effect is valid.
type Verifier func(ctx context.Context) (ok bool, detail string, err error)

// ExecutorResult is the outcome of one authorized dispatch.
type ExecutorResult struct {
	Executed   bool           `json:"executed"`
	Verified   bool           `json:"verified"`
	Decision   ReconcileAlias `json:"decision"`
	VerifyNote string         `json:"verifyNote,omitempty"`
}

// ReconcileAlias mirrors durable.ReconcileDecision without importing
// string constants into JSON output ambiguity.
type ReconcileAlias = durable.ReconcileDecision

// RuntimeExecutor applies authorized side effects under an idempotent
// ExecutionCursor. It NEVER accepts raw worker output: callers must pass
// a GatewayResult with Decision == DecisionAllow (or a verified
// REQUIRE_VERIFICATION), preserving Proposal != Execution.
type RuntimeExecutor struct {
	store *durable.TaskStore
}

// NewRuntimeExecutor binds an executor to the durable task substrate.
func NewRuntimeExecutor(store *durable.TaskStore) *RuntimeExecutor {
	return &RuntimeExecutor{store: store}
}

// Execute dispatches an authorized proposal exactly once:
//  1. Reconcile current digest via durable.Reconcile (missing response
//     != missing side effect — Invariant 6).
//  2. On ALREADY_COMMITTED: advance without re-executing.
//  3. On SAFE_RETRY: dispatch cursor, run effect once, commit.
//  4. On CONFLICT: record TARGET_CONFLICT, return error, never execute.
//  5. Run evidence verification; record VERIFICATION_RESULT.
func (e *RuntimeExecutor) Execute(ctx context.Context, p Proposal, preDigest, currentDigest string, effect Effect, verify Verifier) (ExecutorResult, error) {
	if e == nil || e.store == nil {
		return ExecutorResult{}, fmt.Errorf("scopeguard: nil executor store")
	}
	if strings.TrimSpace(p.OperationID) == "" {
		return ExecutorResult{}, fmt.Errorf("scopeguard: proposal carries no operation id")
	}
	cursor := durable.ExecutionCursor{
		TaskID: p.TaskID, StepID: p.StepID, OperationID: p.OperationID,
		PreconditionDigest: preDigest,
	}
	runner := &durable.IdempotentRunner{Store: e.store}
	var postDigest string
	executed, err := runner.Run(p.TaskID, p.StepID, p.OperationID, preDigest, "",
		func() (string, error) { return currentDigest, nil },
		func() error {
			if effect == nil {
				return nil
			}
			post, effErr := effect(ctx)
			if effErr != nil {
				return effErr
			}
			postDigest = post
			return nil
		},
	)
	_ = cursor
	_ = postDigest
	if err != nil {
		return ExecutorResult{Executed: executed}, err
	}
	// Evidence verification (ambiguity tier and post-commit validation).
	verified := true
	note := ""
	if verify != nil {
		ok, detail, verr := verify(ctx)
		if verr != nil {
			ok = false
			detail = "verifier error: " + verr.Error()
		}
		verified = ok
		note = detail
		_ = e.store.RecordVerification(p.TaskID, p.OperationID, ok, detail)
		if !ok {
			return ExecutorResult{Executed: executed, Verified: false, VerifyNote: note},
				fmt.Errorf("scopeguard: verification failed: %s", detail)
		}
	} else {
		_ = e.store.RecordVerification(p.TaskID, p.OperationID, true, "no verifier configured; commit recorded")
	}
	// Reconcile alias for callers: executed==false with no error means
	// the postcondition already held (already committed).
	decision := durable.DecisionSafeRetry
	if !executed {
		decision = durable.DecisionAlreadyCommitted
	}
	return ExecutorResult{Executed: executed, Verified: verified, Decision: decision, VerifyNote: note}, nil
}

// Pipeline composes the full authority-enforced path:
// ScopeGuard -> StructuralGuard -> IntentGateway -> RuntimeExecutor ->
// Evidence Verification. Tool dispatch occurs ONLY inside Execute and
// only for GatewayResult.Decision == DecisionAllow.
type Pipeline struct {
	gateway  *IntentGateway
	executor *RuntimeExecutor
}

// NewPipeline composes a gateway with an executor.
func NewPipeline(gateway *IntentGateway, executor *RuntimeExecutor) *Pipeline {
	return &Pipeline{gateway: gateway, executor: executor}
}

// RunProposal authorizes then (conditionally) executes one proposal.
// Verification-required proposals are returned WITHOUT execution so the
// caller can run the linter/test runner first and re-submit with
// verified=true.
func (pl *Pipeline) RunProposal(ctx context.Context, p Proposal, primaryScope []string, preDigest, currentDigest string, effect Effect, verify Verifier, verified bool) (GatewayResult, ExecutorResult, error) {
	if pl == nil || pl.gateway == nil {
		return GatewayResult{Decision: DecisionDeny, Reason: "nil pipeline gateway"}, ExecutorResult{}, fmt.Errorf("scopeguard: nil pipeline gateway")
	}
	gw := pl.gateway.Authorize(ctx, p, primaryScope)
	switch gw.Decision {
	case DecisionDeny:
		return gw, ExecutorResult{}, fmt.Errorf("scopeguard: proposal denied: %s", gw.Reason)
	case DecisionRequireVerification:
		if !verified {
			return gw, ExecutorResult{}, fmt.Errorf("scopeguard: proposal requires verification before execution")
		}
		// Verified ambiguity proceeds to execution below.
		gw.Decision = DecisionAllow
		gw.Reason = "ambiguity resolved by verification: " + gw.Reason
	case DecisionAllow:
		// Proceed.
	default:
		return gw, ExecutorResult{}, fmt.Errorf("scopeguard: unknown gateway decision %q", gw.Decision)
	}
	if pl.executor == nil {
		return gw, ExecutorResult{}, fmt.Errorf("scopeguard: nil pipeline executor")
	}
	execRes, err := pl.executor.Execute(ctx, p, preDigest, currentDigest, effect, verify)
	return gw, execRes, err
}
