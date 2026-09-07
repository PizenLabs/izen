package stream

import "encoding/json"

// Usage captures streamed token usage metadata verbatim from the provider's
// final stream chunk (usage.prompt_tokens, usage.completion_tokens, ...).
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// StreamDelta is the provider-native delta view parsed seamlessly from a
// single SSE chunk: standard content deltas (delta.content) alongside
// reasoning deltas (delta.reasoning_content / reasoning / thinking).
type StreamDelta struct {
	Content          string
	ReasoningContent string
	Usage            *Usage
	FinishReason     string
	Done             bool
}

// rawChunk mirrors the OpenAI-compatible SSE envelope shared by OpenRouter,
// OpenAI, Groq, Ollama and compatible gateways.
type rawChunk struct {
	Choices []struct {
		Delta *struct {
			Content          *string `json:"content"`
			ReasoningContent *string `json:"reasoning_content"`
			Reasoning        *string `json:"reasoning"`
			Thinking         *string `json:"thinking"`
		} `json:"delta"`
		Message *struct {
			Content          *string `json:"content"`
			ReasoningContent *string `json:"reasoning_content"`
			Reasoning        *string `json:"reasoning"`
			Thinking         *string `json:"thinking"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
}

// ParseDelta parses one SSE data payload into its content/reasoning/usage
// components. It handles content deltas and reasoning deltas
// (reasoning_content, reasoning, thinking) seamlessly, plus terminal usage
// metadata and finish reasons. "[DONE]" yields Done=true. Unknown payloads
// yield a zero delta and no error so the stream buffer never fails on
// provider extensions.
func ParseDelta(data string) StreamDelta {
	if data == "[DONE]" {
		return StreamDelta{Done: true}
	}
	var c rawChunk
	if err := json.Unmarshal([]byte(data), &c); err != nil {
		return StreamDelta{}
	}
	var out StreamDelta
	if c.Usage != nil {
		u := *c.Usage
		out.Usage = &u
	}
	if len(c.Choices) > 0 {
		ch := c.Choices[0]
		if ch.FinishReason != "" {
			out.FinishReason = ch.FinishReason
		}
		if ch.Delta != nil {
			if ch.Delta.Content != nil {
				out.Content = *ch.Delta.Content
			}
			switch {
			case ch.Delta.ReasoningContent != nil && *ch.Delta.ReasoningContent != "":
				out.ReasoningContent = *ch.Delta.ReasoningContent
			case ch.Delta.Reasoning != nil && *ch.Delta.Reasoning != "":
				out.ReasoningContent = *ch.Delta.Reasoning
			case ch.Delta.Thinking != nil && *ch.Delta.Thinking != "":
				out.ReasoningContent = *ch.Delta.Thinking
			}
		}
		if ch.Message != nil {
			if out.Content == "" && ch.Message.Content != nil {
				out.Content = *ch.Message.Content
			}
			if out.ReasoningContent == "" {
				switch {
				case ch.Message.ReasoningContent != nil && *ch.Message.ReasoningContent != "":
					out.ReasoningContent = *ch.Message.ReasoningContent
				case ch.Message.Reasoning != nil && *ch.Message.Reasoning != "":
					out.ReasoningContent = *ch.Message.Reasoning
				case ch.Message.Thinking != nil && *ch.Message.Thinking != "":
					out.ReasoningContent = *ch.Message.Thinking
				}
			}
		}
	}
	return out
}
