package llm

import (
	"context"
	"errors"
)

// ErrPayloadTruncated is returned when finish_reason == "length" is observed.
// It matches the directive's required message and must be returned BEFORE any
// envelope/JSON parsing, without attempting a FULL_REWRITE->BOUNDED_PATCH
// transition.
var ErrPayloadTruncated = errors.New("model output exceeded max_tokens limit: ErrPayloadTruncated")

type PromptRequest struct {
	Model       string
	System      string
	Messages    []Message
	Stream      bool
	MaxTokens   int
	Temperature float64

	CacheSystem   bool
	CacheMessages []int

	// ReasoningHandler receives reasoning/thinking content as it streams in,
	// separated from the main response. It is called with verbatim chunks (the
	// same text that reasoning_content / thinking_delta frames carry) so
	// consumers can publish a reasoning stream without ever mixing it into the
	// response pipeline. When nil, reasoning content is silently discarded.
	ReasoningHandler func(chunk string) error
}

type Message struct {
	Role    string
	Content string
}

type LLMResponse struct {
	Content          string
	TokenInput       int
	TokenOutput      int
	CacheWriteTokens int
	CacheReadTokens  int
	TotalCostUSD     float64
	DurationMs       int64
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
	execute func(ctx context.Context, model string, system string, messages []Message, maxTokens int, temperature float64) (string, int, int, int, int, error)
	stream  func(ctx context.Context, model string, system string, messages []Message, maxTokens int, temperature float64, handler StreamHandler) (int, int, int, int, error)
}

func NewProviderAdapter(name string, execute func(ctx context.Context, model string, system string, messages []Message, maxTokens int, temperature float64) (string, int, int, int, int, error), stream func(ctx context.Context, model string, system string, messages []Message, maxTokens int, temperature float64, handler StreamHandler) (int, int, int, int, error)) *ProviderAdapter {
	return &ProviderAdapter{name: name, execute: execute, stream: stream}
}

func (a *ProviderAdapter) Name() string { return a.name }

func (a *ProviderAdapter) GenerateResponse(ctx context.Context, req PromptRequest) (LLMResponse, error) {
	if a.execute == nil {
		return LLMResponse{}, nil
	}
	content, tokenIn, tokenOut, cacheWrite, cacheRead, err := a.execute(ctx, req.Model, req.System, req.Messages, req.MaxTokens, req.Temperature)
	if err != nil {
		return LLMResponse{}, err
	}
	return LLMResponse{
		Content:          content,
		TokenInput:       tokenIn,
		TokenOutput:      tokenOut,
		CacheWriteTokens: cacheWrite,
		CacheReadTokens:  cacheRead,
	}, nil
}

func (a *ProviderAdapter) StreamResponse(ctx context.Context, req PromptRequest, handler StreamHandler) (LLMResponse, error) {
	if a.stream == nil {
		return LLMResponse{}, nil
	}
	tokenIn, tokenOut, cacheWrite, cacheRead, err := a.stream(ctx, req.Model, req.System, req.Messages, req.MaxTokens, req.Temperature, handler)
	if err != nil {
		return LLMResponse{}, err
	}
	return LLMResponse{
		TokenInput:       tokenIn,
		TokenOutput:      tokenOut,
		CacheWriteTokens: cacheWrite,
		CacheReadTokens:  cacheRead,
	}, nil
}
