package pipeline

import (
	"context"
	"fmt"

	"github.com/PizenLabs/izen/pkg/engine/layer3"
)

// CompleteFunc is the stateless model-call seam the pipeline engine executes
// against. The presentation layer adapts its concrete provider (e.g. an
// internal/ai.Provider) onto this signature so the engine never depends on a
// concrete LLM client.
type CompleteFunc func(ctx context.Context, provider, model, prompt string) (string, layer3.TokenUsage, error)

// FuncClient adapts a CompleteFunc to the Layer 3 WorkerClient contract. It is
// immutable after construction and safe for concurrent use.
type FuncClient struct {
	complete CompleteFunc
}

// NewFuncClient returns a WorkerClient delegating completion to fn. A nil fn
// yields a client whose Complete always fails.
func NewFuncClient(fn CompleteFunc) *FuncClient {
	return &FuncClient{complete: fn}
}

// Complete implements layer3.WorkerClient.
func (c *FuncClient) Complete(ctx context.Context, req *layer3.CompletionRequest) (*layer3.CompletionResponse, error) {
	if c == nil || c.complete == nil {
		return nil, fmt.Errorf("pipeline: no completion function configured")
	}
	if req == nil {
		return nil, fmt.Errorf("pipeline: nil completion request")
	}
	text, usage, err := c.complete(ctx, string(req.Provider), req.Model, req.Prompt)
	if err != nil {
		return nil, err
	}
	return &layer3.CompletionResponse{Text: text, Tokens: usage}, nil
}
