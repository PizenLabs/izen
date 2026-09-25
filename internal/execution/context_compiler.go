package execution

import (
	"context"
	"strings"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/contextcompiler"
	"github.com/PizenLabs/izen/internal/execution/strategy"
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

func contextLineage(req ExecuteRequest) string {
	if req.Context == nil {
		return ""
	}
	return req.Context.ID
}

func (x *RuntimeExecutor) workspaceFiles(targets []string, critical bool) []contextcompiler.FileContext {
	if x == nil {
		return nil
	}
	files := make([]contextcompiler.FileContext, 0, len(targets))
	for i, target := range targets {
		data, ok := x.getSnapshotContent(target)
		if !ok {
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
