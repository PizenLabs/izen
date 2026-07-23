package llm

import (
	"context"
)

type PromptRequest struct {
	Model       string
	System      string
	Messages    []Message
	Stream      bool
	MaxTokens   int
	Temperature float64

	CacheSystem   bool
	CacheMessages []int
}

type Message struct {
	Role    string
	Content string
}

type LLMResponse struct {
	Content     string
	TokenInput  int
	TokenOutput int
}

type StreamHandler func(chunk string) error

type LLMProvider interface {
	Name() string
	GenerateResponse(ctx context.Context, req PromptRequest) (LLMResponse, error)
	StreamResponse(ctx context.Context, req PromptRequest, handler StreamHandler) (LLMResponse, error)
}

var _ LLMProvider = (*ProviderAdapter)(nil)

type ProviderAdapter struct {
	name    string
	execute func(ctx context.Context, model string, system string, messages []Message, maxTokens int, temperature float64) (string, int, int, error)
	stream  func(ctx context.Context, model string, system string, messages []Message, maxTokens int, temperature float64, handler StreamHandler) (int, int, error)
}

func NewProviderAdapter(name string, execute func(ctx context.Context, model string, system string, messages []Message, maxTokens int, temperature float64) (string, int, int, error), stream func(ctx context.Context, model string, system string, messages []Message, maxTokens int, temperature float64, handler StreamHandler) (int, int, error)) *ProviderAdapter {
	return &ProviderAdapter{name: name, execute: execute, stream: stream}
}

func (a *ProviderAdapter) Name() string { return a.name }

func (a *ProviderAdapter) GenerateResponse(ctx context.Context, req PromptRequest) (LLMResponse, error) {
	if a.execute == nil {
		return LLMResponse{}, nil
	}
	content, tokenIn, tokenOut, err := a.execute(ctx, req.Model, req.System, req.Messages, req.MaxTokens, req.Temperature)
	if err != nil {
		return LLMResponse{}, err
	}
	return LLMResponse{Content: content, TokenInput: tokenIn, TokenOutput: tokenOut}, nil
}

func (a *ProviderAdapter) StreamResponse(ctx context.Context, req PromptRequest, handler StreamHandler) (LLMResponse, error) {
	if a.stream == nil {
		return LLMResponse{}, nil
	}
	tokenIn, tokenOut, err := a.stream(ctx, req.Model, req.System, req.Messages, req.MaxTokens, req.Temperature, handler)
	if err != nil {
		return LLMResponse{}, err
	}
	return LLMResponse{TokenInput: tokenIn, TokenOutput: tokenOut}, nil
}
