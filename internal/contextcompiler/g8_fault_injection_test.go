package contextcompiler

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
)

type g8CountingProvider struct {
	executeCalls atomic.Int32
	streamCalls  atomic.Int32
}

func (p *g8CountingProvider) Name() string { return "g8-fault-provider" }
func (p *g8CountingProvider) Execute(context.Context, ai.Request) (*ai.Response, error) {
	p.executeCalls.Add(1)
	return &ai.Response{}, nil
}
func (p *g8CountingProvider) ExecuteStream(context.Context, ai.Request) (io.ReadCloser, error) {
	p.streamCalls.Add(1)
	return io.NopCloser(strings.NewReader("")), nil
}

func TestG8PromptCompilerBudgetExhaustionFailsBeforeProvider(t *testing.T) {
	compiler := New(WithMaxTokens(32))
	inner := &g8CountingProvider{}
	prepared := compiler.WrapProvider(inner)
	tooLarge := ai.Request{
		Model:               "g8-model",
		InteractionContract: "direct_completion",
		System:              strings.Repeat("critical system contract ", 64),
		Messages:            []ai.Message{{Role: "user", Content: "explain"}},
		MaxTokens:           16,
	}

	if _, err := prepared.Execute(t.Context(), tooLarge); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("Execute error = %v, want ErrBudgetExceeded", err)
	}
	if inner.executeCalls.Load() != 0 {
		t.Fatalf("provider execute calls = %d, want zero after budget exhaustion", inner.executeCalls.Load())
	}

	if _, err := prepared.ExecuteStream(t.Context(), tooLarge); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("ExecuteStream error = %v, want ErrBudgetExceeded", err)
	}
	if inner.streamCalls.Load() != 0 {
		t.Fatalf("provider stream calls = %d, want zero after budget exhaustion", inner.streamCalls.Load())
	}
}

func TestG8PromptCompilerOptionalContextIsBoundedOnBudgetExhaustion(t *testing.T) {
	compiler := New(WithMaxTokens(64))
	compiled, err := compiler.Compile(t.Context(), Input{
		Phase:              PhaseExecute,
		SystemInstructions: "system",
		UserRequest:        "inspect the workspace",
		Artifacts: []ArtifactRef{{
			Path:    "large.txt",
			Content: strings.Repeat("x", 16_000),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if compiled.UsedTokens > compiled.Budget.Total {
		t.Fatalf("compiled tokens = %d, budget = %d", compiled.UsedTokens, compiled.Budget.Total)
	}
	if !compiled.Truncated && compiled.Dropped == 0 {
		t.Fatal("oversized optional context was neither truncated nor dropped")
	}
}

func TestG8PromptCompilerHonorsCancellationBeforeCompilation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New().Compile(ctx, Input{UserRequest: "cancelled"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Compile error = %v, want context.Canceled", err)
	}
}
