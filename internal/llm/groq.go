package llm

import (
	"context"
	"strings"
)

type GroqClient struct {
	inner *OpenAIClient
}

func NewGroqClient(apiKey, model, baseURL string) *GroqClient {
	if baseURL == "" {
		baseURL = "https://api.groq.com/openai/v1"
	}
	return &GroqClient{
		inner: NewOpenAIClient(apiKey, model, strings.TrimRight(baseURL, "/")),
	}
}

func (c *GroqClient) Name() string {
	return "groq"
}

func (c *GroqClient) GenerateResponse(ctx context.Context, req PromptRequest) (LLMResponse, error) {
	return c.inner.GenerateResponse(ctx, req)
}

func (c *GroqClient) StreamResponse(ctx context.Context, req PromptRequest, handler StreamHandler) (LLMResponse, error) {
	return c.inner.StreamResponse(ctx, req, handler)
}
