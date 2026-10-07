package execution

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/contextcompiler"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution/strategy"
	"github.com/PizenLabs/izen/internal/protocol"
)

// AgentContext is the execution-facing name for the compiler-owned bounded
// projection. It is an alias so embedders can use the protocol vocabulary
// without duplicating a second context type.
type AgentContext = contextcompiler.AgentContext

// contextCompilerInstance returns the single compiler wired to this executor.
// A zero-value compiler is a supported test/headless fallback; production
// composition replaces it with Application.Compiler at the composition root.
func (x *RuntimeExecutor) contextCompilerInstance() *contextcompiler.Compiler {
	if x == nil {
		return nil
	}
	x.mu.Lock()
	compiler := x.contextCompiler
	x.mu.Unlock()
	if compiler == nil {
		return &contextcompiler.Compiler{}
	}
	return compiler
}

// SetContextCompiler wires the application-owned Context Compilation authority.
// Passing nil restores the zero-value compatibility compiler; it does not
// create a second application authority.
func (x *RuntimeExecutor) SetContextCompiler(compiler *contextcompiler.Compiler) {
	if x == nil {
		return
	}
	if compiler == nil {
		compiler = &contextcompiler.Compiler{}
	}
	x.mu.Lock()
	x.contextCompiler = compiler
	x.mu.Unlock()
}

// ContextCompiler exposes the wired compiler for observability and embedders.
func (x *RuntimeExecutor) ContextCompiler() *contextcompiler.Compiler {
	return x.contextCompilerInstance()
}

// SetTelemetrySink attaches the optional per-operation execution telemetry
// record used by the inspect/audit projection. Passing nil disables the sink.
func (x *RuntimeExecutor) SetTelemetrySink(sink *Telemetry) {
	if x == nil {
		return
	}
	x.mu.Lock()
	x.telemetrySink = sink
	x.mu.Unlock()
}

func (x *RuntimeExecutor) telemetry() *Telemetry {
	if x == nil {
		return nil
	}
	x.mu.Lock()
	sink := x.telemetrySink
	x.mu.Unlock()
	return sink
}

func contextPhaseForStrategy(value strategy.ExecutionStrategy) contextcompiler.Phase {
	switch value {
	case strategy.MultiFilePlanning:
		return contextcompiler.PhasePlan
	case strategy.TargetedMutation, strategy.DirectDeterministic:
		return contextcompiler.PhaseExecute
	case strategy.RepositoryInvestigation, strategy.TargetedReasoning, strategy.DirectResponse, strategy.HumanClarification:
		return contextcompiler.PhaseInvestigate
	default:
		return contextcompiler.PhaseExecute
	}
}

func (x *RuntimeExecutor) contextModelLimits(req ExecuteRequest, model string, requestedOutput int) contextcompiler.ModelLimits {
	provider := ""
	if x != nil {
		provider = x.providerName()
	}
	limits := contextcompiler.ModelLimitsFor(provider, model, requestedOutput)
	metadata := req.ModelMetadata
	if metadata != nil && metadata.ID != "" && !strings.EqualFold(strings.TrimSpace(metadata.ID), strings.TrimSpace(model)) {
		metadata = nil
	}
	if metadata != nil {
		if metadata.ContextWindow > 0 {
			limits.ContextWindow = metadata.ContextWindow
		}
		if metadata.MaxOutputTokens > 0 {
			limits.MaxOutputTokens = metadata.MaxOutputTokens
		}
	}
	return limits
}

func (x *RuntimeExecutor) compileRequest(
	ctx context.Context,
	req ExecuteRequest,
	profile strategy.ExecutionStrategyProfile,
	model string,
	system string,
	user string,
	files []contextcompiler.FileContext,
	requestedOutput int,
) (ai.Request, *contextcompiler.AgentContext, error) {
	metadata := req.ModelMetadata
	limits := x.contextModelLimits(req, model, requestedOutput)
	policy := "repository"
	contextBudget := profile.ContextBudget.Tokens
	switch profile.Policy() {
	case strategy.ContextPolicyNone:
		policy = "none"
		contextBudget = 0
	case strategy.ContextPolicyTargetFileOnly:
		policy = "target_file_only"
	}
	if contextBudget <= 0 && policy != "none" {
		switch profile.Policy() {
		case strategy.ContextPolicyTargetFileOnly:
			contextBudget = 4000
		case strategy.ContextPolicyRepository:
			contextBudget = 16000
		}
	}
	scope := strings.TrimSpace(req.Scope)
	if scope == "" {
		scope = strings.Join(append([]string(nil), req.Targets...), ",")
	}
	compiled, agent, err := x.contextCompilerInstance().CompileRequest(ctx, ai.Request{
		Model:               model,
		System:              system,
		Messages:            []ai.Message{{Role: "user", Content: user}},
		MaxTokens:           requestedOutput,
		InteractionContract: req.InteractionContract,
		Contract:            req.Contract,
		ContractID:          req.ContractID,
		RequestID:           req.RequestID,
		Mode:                req.Mode,
		AuthorityLevel:      contractAuthority(req.Contract),
	}, contextcompiler.RequestCompileOptions{
		Phase:                 contextPhaseForStrategy(profile.Strategy),
		Provider:              limits.Provider,
		WorkflowState:         string(profile.Strategy),
		ContextPolicy:         policy,
		Scope:                 scope,
		Lineage:               contextLineage(req),
		Files:                 files,
		ContextWindow:         limits.ContextWindow,
		MaxOutputTokens:       limits.MaxOutputTokens,
		RequestedOutputTokens: requestedOutput,
		ContextBudget:         contextBudget,
		ModelMetadata:         metadata,
	})
	if err != nil {
		return ai.Request{}, nil, err
	}
	compiled.ContextPrepared = true
	compiled.ContextPhase = string(contextPhaseForStrategy(profile.Strategy))
	return compiled, agent, nil
}

func contextTelemetry(req ExecuteRequest, agent *contextcompiler.AgentContext) (events.ContextPreparedPayload, events.ContextCompilationPayload) {
	result := agent.CompileResult()
	binding := protocol.NewObservabilityBinding(req.InteractionContract, req.Contract, req.ContractID, req.Mode, string(ai.SchemaModeAuto))
	prepared := events.ContextPreparedPayload{
		RequestID:          req.RequestID,
		Channels:           append([]string(nil), agent.Compiled.ContextChannels()...),
		Tokens:             result.UsedTokens,
		Phase:              string(result.Phase),
		Policy:             result.Policy,
		Scope:              result.Scope,
		FittedContextScope: result.FittedContextScope,
		Lineage:            result.Lineage,
		BudgetTokens:       result.BudgetTotal,
		ReservedTokens:     result.ReservedTokens,
		AvailableTokens:    result.AvailableTokens,
		ContextTokens:      result.ContextTokens,
		Truncated:          result.Truncated,
		TruncatedFiles:     append([]string(nil), result.TruncatedFiles...),
		TruncatedFileCount: result.TruncatedFileCount,
		DropCount:          result.DropCount,
		PromptChars:        result.PromptChars,
		PromptFingerprint:  result.PromptFingerprint,
		CacheHit:           result.CacheHit,
		ProtocolTelemetry:  binding,
	}
	metrics := events.ContextCompilationPayload{
		RequestID:          req.RequestID,
		Phase:              string(result.Phase),
		Policy:             result.Policy,
		Scope:              result.Scope,
		FittedContextScope: result.FittedContextScope,
		Lineage:            result.Lineage,
		BudgetTokens:       result.BudgetTotal,
		ReservedTokens:     result.ReservedTokens,
		AvailableTokens:    result.AvailableTokens,
		UsedTokens:         result.UsedTokens,
		ContextTokens:      result.ContextTokens,
		SystemTokens:       result.SystemTokens,
		SchemaTokens:       result.SchemaTokens,
		ToolTokens:         result.ToolTokens,
		Truncated:          result.Truncated,
		TruncatedFiles:     append([]string(nil), result.TruncatedFiles...),
		TruncatedFileCount: result.TruncatedFileCount,
		DropCount:          result.DropCount,
		SectionCount:       result.SectionCount,
		Sources:            make([]string, 0, len(result.Sources)),
		PromptChars:        result.PromptChars,
		PromptFingerprint:  result.PromptFingerprint,
		CacheHit:           result.CacheHit,
		ProtocolTelemetry:  binding,
	}
	for _, source := range result.Sources {
		metrics.Sources = append(metrics.Sources, string(source))
	}
	return prepared, metrics
}

func contractAuthority(descriptor *protocol.ContractDescriptor) protocol.AuthorityCeiling {
	if descriptor == nil {
		return ""
	}
	return descriptor.AuthorityCeiling
}

func (x *RuntimeExecutor) recordContextTelemetry(req ExecuteRequest, agent *contextcompiler.AgentContext) {
	if x == nil || agent == nil {
		return
	}
	if sink := x.telemetry(); sink != nil {
		sink.BindProtocol(protocol.NewObservabilityBinding(req.InteractionContract, req.Contract, req.ContractID, req.Mode, string(ai.SchemaModeAuto)))
		result := agent.CompileResult()
		sink.RecordCompileResult(&result)
	}
}

func contextLineage(req ExecuteRequest) string {
	if req.Context == nil {
		return ""
	}
	return req.Context.ID
}

// ErrIntentContextProvenance is returned when a re-compiled workspace context
// does not satisfy the ACTIVE canonical intent's provenance contract: a
// requested target is missing, the payload carries no workspace material, or
// the compilation was bound to a different intent.
//
// It fails CLOSED. A mutation intent dispatched over a read-only context is
// exactly the split-brain state the intent revision exists to prevent, and
// running it would mean the model judged a workspace it was never shown.
var ErrIntentContextProvenance = errors.New("execution: compiled context does not satisfy the active intent's provenance contract")

// ErrIntentContextStarvation is returned when a re-compilation is semantically
// VALID but carries no usable workspace material for the declared targets: the
// payload admits every requested scope entry and yet the prompt crosses the
// provider boundary carrying effectively nothing to judge.
//
// It is a separate sentinel from ErrIntentContextProvenance because the two
// demand different responses. Provenance failure means the contract was not
// satisfied and the run must park. Starvation means the contract WAS satisfied
// on paper while the workspace was still invisible — the exact state produced by
// freezing the compiled context before capability authorization completed. The
// acceptance criterion is deliberately the strongest one available: a granted
// workspace capability must be backed by non-zero target context bytes before
// any model is asked to judge the workspace.
var ErrIntentContextStarvation = errors.New("execution: granted workspace capabilities produced an empty target context")

// RecompileIntentContext re-compiles the workspace context for the given
// targets under an intent's contract and returns the SEMANTIC provenance
// verdict.
//
// It is the second half of the blocking intent revision: the payload compiled
// for the previous intent is never reused, because a read-only projection is
// not a valid mutation context no matter how completely it filled its budget.
// The verdict is a scope + provenance + intent check — never a token threshold.
// `required` is one of the contextcompiler.IntentContext* vocabularies.
func (x *RuntimeExecutor) RecompileIntentContext(ctx context.Context, targets []string, intentLabel, required string) (contextcompiler.ContextProvenance, error) {
	return x.recompileIntentContext(ctx, targets, intentLabel, required)
}

// RecompileGrantedIntentContext is the GRANT-GATED half of the intent revision
// (Phase 15): the re-compilation that must happen once a workspace capability
// grant is in force, and before the execution driver lowers its preflight
// barrier.
//
// It is `RecompileIntentContext` plus one additional, stronger acceptance
// condition: the compiled payload must be a NON-EMPTY projection of the workspace.
// The semantic provenance gate already refuses a payload that carries no
// workspace material, so this is not a second copy of that check — it is the
// condition provenance cannot express, namely that a payload which satisfies
// every clause must still have content to show. A compiler that reports a valid,
// scope-matched, workspace-bound context of zero tokens is not a valid context;
// it is an empty one wearing a valid label, and it is precisely the state the
// pre-grant freeze produced.
//
// WHAT THE PAYLOAD IS AND IS NOT FOR. This call does not hand a prompt to the
// model and is not a substitute for the dispatch-time compilation. Its two
// effects are (a) it drops the previous intent's cached projection, so the
// compilation the executor performs when it builds the dispatched request cannot
// be a cache hit on a read-only payload, and (b) it proves — before a single
// provider request is admitted — that a context satisfying the active intent
// actually exists and carries bytes. Both effects must happen before the barrier
// lowers; neither replaces the other.
//
// The refusal is ErrIntentContextStarvation so a caller can tell "the contract
// was refused" (ErrIntentContextProvenance) from "the contract was met by
// nothing" — a compiler defect rather than a legitimate refusal.
func (x *RuntimeExecutor) RecompileGrantedIntentContext(ctx context.Context, targets []string, intentLabel, required string) (contextcompiler.ContextProvenance, error) {
	provenance, err := x.recompileIntentContext(ctx, targets, intentLabel, required)
	if err != nil {
		return provenance, err
	}
	return provenance, GrantedContextStarvation(provenance, targets, required)
}

// GrantedContextStarvation is the pure acceptance check a grant-gated
// re-compilation must pass, separated from the executor so it can be reasoned
// about — and tested — without a workspace, a compiler or a provider.
//
// It returns nil for every contract that does not demand workspace material. That
// exclusion is load-bearing: a zero-workspace or self-contained turn is CORRECT
// with an empty payload, and refusing it would reintroduce the token-threshold
// rule this gate was written alongside, just with a different threshold.
func GrantedContextStarvation(provenance contextcompiler.ContextProvenance, targets []string, required string) error {
	if required != contextcompiler.IntentContextWorkspace {
		return nil
	}
	// `Valid` is checked first so this never re-diagnoses a refusal: the
	// provenance gate's reason is the better answer and the caller must not have
	// two competing explanations for one failure.
	if !provenance.Valid {
		return nil
	}
	if !provenance.WorkspaceMaterialPresent || provenance.ObservedTokens <= 0 {
		return fmt.Errorf("%w: %d target(s) compiled to a valid but empty context (%d token(s), workspace_material=%t)",
			ErrIntentContextStarvation, len(uniqueTargets(targets)), provenance.ObservedTokens,
			provenance.WorkspaceMaterialPresent)
	}
	return nil
}

// uniqueTargets de-duplicates non-empty targets, preserving first-appearance
// order. It exists so a diagnostic counts declared targets rather than
// re-listing the same path twice.
func uniqueTargets(targets []string) []string {
	if len(targets) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(targets))
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		trimmed := strings.TrimSpace(t)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	return out
}

// recompileIntentContext is the shared body of the two revision entry points.
// The invalidation step, the compile call and the provenance evaluation live
// here exactly once so the blocking revision and the grant-gated one can never
// drift apart in what they consider a valid re-compilation.
func (x *RuntimeExecutor) recompileIntentContext(ctx context.Context, targets []string, intentLabel, required string) (contextcompiler.ContextProvenance, error) {
	if x == nil {
		return contextcompiler.ContextProvenance{}, fmt.Errorf("execution: nil executor")
	}
	if ctx == nil {
		// Fail closed rather than substituting a fresh context: the caller's
		// cancellation authority is the run's, and silently detaching from it
		// would let a re-compilation outlive an aborted run.
		return contextcompiler.ContextProvenance{}, errors.New("execution: intent context re-compilation requires a context")
	}
	// The MUTATION context contract always projects the declared targets as
	// required (critical) file context: a read-only projection of them is not a
	// mutation context. A DECLARED CREATION contract is the exception: the
	// targets are expected absent, so they are admitted with an explicit
	// creation representation instead of being required to carry bytes.
	critical := required == contextcompiler.IntentContextWorkspace
	creation := required == contextcompiler.IntentContextCreation
	compiler := x.contextCompilerInstance()
	// PHASE 14 — step 1 of the blocking intent revision: drop every payload
	// compiled under the PREVIOUS intent before compiling under this one. The
	// read-only projection of the previous intent is not a mutation context, and
	// a cache hit on it would be indistinguishable from a correct compilation.
	compiler.InvalidateCache()
	compiled, err := compiler.Compile(ctx, contextcompiler.Input{
		UserRequest:   intentLabel,
		WorkflowState: string(strategy.TargetedMutation),
		Phase:         contextcompiler.PhaseExecute,
		Files:         x.workspaceFiles(targets, critical, creation),
		ContextPolicy: "target_file_only",
		Scope:         strings.Join(targets, ","),
	})
	if err != nil {
		return contextcompiler.ContextProvenance{}, err
	}
	provenance := compiled.ValidateContextProvenance(contextcompiler.IntentBinding{
		Active:   intentLabel,
		Required: required,
	}, targets)
	if !provenance.Valid {
		return provenance, fmt.Errorf("%w: %s", ErrIntentContextProvenance, provenance.Reason)
	}
	return provenance, nil
}

func (x *RuntimeExecutor) workspaceFiles(targets []string, critical, creation bool) []contextcompiler.FileContext {
	if x == nil {
		return nil
	}
	files := make([]contextcompiler.FileContext, 0, len(targets))
	for i, target := range targets {
		data, ok := x.getSnapshotContent(target)
		if !ok {
			// A DECLARED CREATION target is expected to be absent. Its absence
			// is part of the contract, not a dropped read: it is carried into
			// the payload with an explicit creation representation, which is
			// what lets scope provenance treat the target as NAMED without
			// fabricating bytes it does not have.
			if creation {
				files = append(files, contextcompiler.FileContext{
					Path:     target,
					Size:     0,
					Content:  "",
					Critical: false,
					Priority: len(targets) - i,
					Creation: true,
				})
			}
			continue
		}
		// Optional supporting context may be capped at the executor's legacy
		// per-target read ceiling. Required mutation bytes are passed intact so
		// the compiler can fail closed rather than silently truncating a file
		// the model is being asked to modify.
		originalSize := len(data)
		truncated := false
		if !critical && originalSize > maxExecutorContextBytes {
			data = data[:maxExecutorContextBytes]
			truncated = true
		}
		files = append(files, contextcompiler.FileContext{
			Path:      target,
			Size:      originalSize,
			Content:   string(data),
			Critical:  critical,
			Priority:  len(targets) - i,
			Truncated: truncated,
		})
	}
	return files
}

func lastUserMessage(req ai.Request) (string, bool) {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if !strings.EqualFold(req.Messages[i].Role, "system") {
			return req.Messages[i].Content, true
		}
	}
	return "", false
}
