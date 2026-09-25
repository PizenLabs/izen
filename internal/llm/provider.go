package llm

import (
	"context"
	"errors"

	"github.com/PizenLabs/izen/internal/protocol"
)

// ErrPayloadTruncated is returned when finish_reason == "length" is observed.
// It matches the directive's required message and must be returned BEFORE any
// envelope/JSON parsing, without attempting a FULL_REWRITE->BOUNDED_PATCH
// transition.
//
// Universal Stream Outcome Invariant: truncation maps to PARTIAL
// (EvidenceState.PARTIAL) across ALL provider tiers. Truncated streams MUST
// preserve canonical token buffers without synthetic content mutation: the
// LLMResponse accompanying this error carries the verbatim partial Content
// plus token counts, and callers MUST surface it with a UI boundary badge
// instead of clearing/swallowing the buffer.
var ErrPayloadTruncated = protocol.ErrOutputTruncated

// ErrOutputTruncated is the protocol-level alias used by newer callers.
var ErrOutputTruncated = protocol.ErrOutputTruncated

type OutputTruncatedError = protocol.OutputTruncatedError

// IsOutputTruncated reports whether err carries the canonical output-ceiling
// signal across the legacy and protocol provider stacks.
func IsOutputTruncated(err error) bool { return errors.Is(err, ErrOutputTruncated) }

// NewOutputTruncated constructs the protocol-level typed truncation error.
func NewOutputTruncated(provider, reason string) error {
	return protocol.NewOutputTruncated(provider, reason)
}

// IsPayloadTruncated reports whether err wraps ErrPayloadTruncated.
func IsPayloadTruncated(err error) bool {
	return errors.Is(err, ErrPayloadTruncated)
}

type PromptRequest struct {
	Model       string
	System      string
	Messages    []Message
	Stream      bool
	MaxTokens   int
	Temperature float64

	InteractionContract protocol.InteractionContract
	Contract            *protocol.ContractDescriptor
	// SchemaMode optionally forces native schema or compact prompt fallback.
	// Empty preserves the provider's automatic choice.
	SchemaMode    string
	ModelMetadata *protocol.ModelMetadata

	CacheSystem   bool
	CacheMessages []int

	// ReasoningHandler receives reasoning/thinking content as it streams in,
	// separated from the main response. It is called with verbatim chunks (the
	// same text that reasoning_content / thinking_delta frames carry) so
	// consumers can publish a reasoning stream without ever mixing it into the
	// response pipeline. When nil, reasoning content is silently discarded.
	ReasoningHandler func(chunk string) error

	// ExtraParams carries arbitrary provider-native JSON fields merged
	// directly into the HTTP POST body (generic passthrough).
	ExtraParams map[string]any
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
	// Provider/Model and the contract fields are the legacy stack's
	// standardized metadata wrapper. They are descriptive only.
	Provider            string
	Model               string
	InteractionContract protocol.InteractionContract
	Contract            *protocol.ContractDescriptor
	NativeSchema        bool
	Schema              string
	InlineConstraint    string
	// FinishReason is the provider-native terminal reason ("stop", "length",
	// "tool_calls", ...). "length" maps to the universal PARTIAL outcome.
	FinishReason string
	// Truncated is true when finish_reason == "length" was observed. Content
	// still carries the verbatim partial buffer; callers must not clear it.
	Truncated bool
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
	return stampAdapterResponse(LLMResponse{
		Content:          content,
		TokenInput:       tokenIn,
		TokenOutput:      tokenOut,
		CacheWriteTokens: cacheWrite,
		CacheReadTokens:  cacheRead,
	}, a.name, req), nil
}

func stampAdapterResponse(response LLMResponse, provider string, req PromptRequest) LLMResponse {
	response.Provider = provider
	response.Model = req.Model
	response.InteractionContract = req.InteractionContract
	if req.Contract != nil {
		copy := req.Contract.Clone()
		response.Contract = &copy
	}
	return response
}

func (a *ProviderAdapter) StreamResponse(ctx context.Context, req PromptRequest, handler StreamHandler) (LLMResponse, error) {
	if a.stream == nil {
		return LLMResponse{}, nil
	}
	tokenIn, tokenOut, cacheWrite, cacheRead, err := a.stream(ctx, req.Model, req.System, req.Messages, req.MaxTokens, req.Temperature, handler)
	if err != nil {
		return LLMResponse{}, err
	}
	return stampAdapterResponse(LLMResponse{
		TokenInput:       tokenIn,
		TokenOutput:      tokenOut,
		CacheWriteTokens: cacheWrite,
		CacheReadTokens:  cacheRead,
	}, a.name, req), nil
}
