package stream

import "testing"

func TestParseDeltaContentAndReasoning(t *testing.T) {
	d := ParseDelta(`{"choices":[{"delta":{"content":"hello"}}]}`)
	if d.Content != "hello" || d.ReasoningContent != "" {
		t.Fatalf("content delta = %+v", d)
	}
	d = ParseDelta(`{"choices":[{"delta":{"reasoning_content":"think step"}}]}`)
	if d.ReasoningContent != "think step" || d.Content != "" {
		t.Fatalf("reasoning delta = %+v", d)
	}
	d = ParseDelta(`{"choices":[{"delta":{"reasoning":"deep thought"}}]}`)
	if d.ReasoningContent != "deep thought" {
		t.Fatalf("reasoning alias = %+v", d)
	}
	d = ParseDelta(`{"choices":[{"delta":{"thinking":"plan"}}]}`)
	if d.ReasoningContent != "plan" {
		t.Fatalf("thinking alias = %+v", d)
	}
}

func TestParseDeltaUsageFromFinalChunk(t *testing.T) {
	d := ParseDelta(`{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":34,"total_tokens":46}}`)
	if d.Usage == nil {
		t.Fatal("usage should be parsed")
	}
	if d.Usage.PromptTokens != 12 || d.Usage.CompletionTokens != 34 {
		t.Fatalf("usage = %+v", d.Usage)
	}
	d = ParseDelta(`[DONE]`)
	if !d.Done {
		t.Fatal("DONE should parse")
	}
}
