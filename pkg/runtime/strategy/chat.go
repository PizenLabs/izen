package strategy

import (
	"context"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/pkg/runtime/registry"
)

// DirectChatStrategy is the built-in strategy for conversational / non-coding
// prompts (IntentChat). It performs a single-pass conversation completion
// through the capability registry's ProviderRouter and returns the model's
// textual response verbatim. It never reads the workspace, never loads AST
// and never stages code edit plans or tool commands.
type DirectChatStrategy struct {
	generator Generator
	system    string
	maxTokens int
}

// NewDirectChatStrategy returns a direct chat strategy over the given
// generator. The system prompt and token ceiling are optional and default to
// sensible values.
func NewDirectChatStrategy(gen Generator, opts ...Option) *DirectChatStrategy {
	s := &DirectChatStrategy{
		generator: gen,
		system:    "You are Izen, a human-centered coding intelligence assistant. Answer the user's message directly and conversationally, in plain text.",
		maxTokens: 2048,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Name implements registry.Strategy.
func (s *DirectChatStrategy) Name() string { return StrategyChat }

func (s *DirectChatStrategy) setSystem(system string) { s.system = system }
func (s *DirectChatStrategy) setMaxTokens(n int)      { s.maxTokens = n }

// Execute implements registry.Strategy. A single generation pass is issued
// with the raw user input as the prompt; the response text is returned on the
// Result. No targets, tools or files are involved.
func (s *DirectChatStrategy) Execute(ctx context.Context, task registry.Task) (*registry.Result, error) {
	if s.generator == nil {
		return nil, ErrNoGenerator
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	gen, err := s.generator.Complete(ctx, GenerationRequest{
		System:    s.system,
		Prompt:    task.Input,
		MaxTokens: s.maxTokens,
	})
	if err != nil {
		return nil, fmt.Errorf("direct chat: %w", err)
	}
	return &registry.Result{
		Status: registry.StatusOK,
		Text:   strings.TrimSpace(gen.Text),
		Tokens: gen.Tokens,
	}, nil
}
