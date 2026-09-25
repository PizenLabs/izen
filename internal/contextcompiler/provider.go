package contextcompiler

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/PizenLabs/izen/internal/ai"
)

// PreparedProvider is the provider-bound facade for the Context Compilation
// authority. It is intentionally thin: it enforces the budget on the request
// immediately before transport and delegates the actual model call unchanged
// to the wrapped provider. It does not resolve workspace files, select tools,
// or grant execution authority.
type PreparedProvider struct {
	inner    ai.Provider
	compiler *Compiler
}

// WrapProvider returns a provider facade that compiles every not-yet-prepared
// request through this compiler. Explicit phase labels are preferred; legacy
// unlabelled requests use the conservative execute phase. A nil provider is
// returned unchanged.
func (c *Compiler) WrapProvider(inner ai.Provider) ai.Provider {
	if c == nil || inner == nil {
		return inner
	}
	if existing, ok := inner.(*PreparedProvider); ok && existing.compiler == c {
		return existing
	}
	return &PreparedProvider{inner: inner, compiler: c}
}

func (p *PreparedProvider) Name() string {
	if p == nil || p.inner == nil {
		return ""
	}
	return p.inner.Name()
}

func (p *PreparedProvider) prepare(ctx context.Context, req ai.Request) (ai.Request, error) {
	if p == nil || p.inner == nil {
		return ai.Request{}, fmt.Errorf("contextcompiler: nil prepared provider")
	}
	if req.ContextPrepared || p.compiler == nil {
		return req, nil
	}
	rawPhase := req.ContextPhase
	phase := Phase(rawPhase).Normalize()
	if strings.TrimSpace(rawPhase) != "" && phase == "" {
		return ai.Request{}, fmt.Errorf("contextcompiler: invalid request phase %q", rawPhase)
	}
	if phase == "" {
		if req.Contract != nil {
			phase = PhaseForContract(req.Contract.Contract)
		} else {
			phase = PhaseForContract(req.InteractionContract)
		}
		// A composed provider is the final enforcement seam. Even a legacy
		// caller that forgot a phase label receives the conservative execute
		// budget rather than bypassing the compiler entirely.
		if phase == "" {
			phase = PhaseExecute
		}
	}
	if !phase.Valid() {
		return ai.Request{}, fmt.Errorf("contextcompiler: invalid request phase %q", req.ContextPhase)
	}
	prepared, _, err := p.compiler.CompileRequest(ctx, req, RequestCompileOptions{
		Phase:                 phase,
		Provider:              p.inner.Name(),
		WorkflowState:         string(phase),
		ContextPolicy:         contextPolicy(req.ContextPolicy),
		RequestedOutputTokens: req.MaxTokens,
		ModelMetadata:         req.ModelMetadata,
	})
	if err != nil {
		return ai.Request{}, err
	}
	prepared.ContextPrepared = true
	return prepared, nil
}

func contextPolicy(value string) string {
	return normalizeContextPolicy(value)
}

func (p *PreparedProvider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	prepared, err := p.prepare(ctx, req)
	if err != nil {
		return nil, err
	}
	return p.inner.Execute(ctx, prepared)
}

func (p *PreparedProvider) ExecuteStream(ctx context.Context, req ai.Request) (io.ReadCloser, error) {
	prepared, err := p.prepare(ctx, req)
	if err != nil {
		return nil, err
	}
	return p.inner.ExecuteStream(ctx, prepared)
}
