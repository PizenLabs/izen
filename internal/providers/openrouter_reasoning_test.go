package providers

import (
	"io"
	"strings"
	"testing"
)

// openrouterReasoningBody builds an SSE body from raw delta JSON fragments so
// each test controls the exact chunking (reasoning fields, inline think tags).
func openrouterReasoningBody(deltas ...string) string {
	var b strings.Builder
	for _, d := range deltas {
		b.WriteString("data: {\"choices\":[{\"delta\":" + d + "}]}")
		b.WriteString("\n\n")
	}
	b.WriteString("data: [DONE]")
	b.WriteString("\n\n")
	return b.String()
}

// stripSentinelMarkers removes reasoning sentinels for plain-text comparison.
func stripSentinelMarkers(s string) string {
	return strings.ReplaceAll(s, ReasoningSentinel, "")
}

// TestOpenRouterStreamResult_ReasoningDeltaExtraction verifies the SSE event
// loop routes delta.reasoning AND delta.reasoning_content chunks onto the
// sentinel-wrapped reasoning channel (the Ctrl+O thought-trace source), even
// when both fields arrive in one combined delta alongside content.
func TestOpenRouterStreamResult_ReasoningDeltaExtraction(t *testing.T) {
	t.Run("delta_reasoning_field", func(t *testing.T) {
		body := openrouterReasoningBody(`{"reasoning":"thinking step "}`, `{"content":"visible"}`)
		sr := &openrouterSSEReader{body: io.NopCloser(strings.NewReader(body))}
		got := drainStream(t, sr)
		if want := ReasoningSentinel + "thinking step " + ReasoningSentinel + "visible"; got != want {
			t.Errorf("stream = %q, want %q", got, want)
		}
	})

	t.Run("delta_reasoning_content_field", func(t *testing.T) {
		body := openrouterReasoningBody(`{"reasoning_content":"cot fragment"}`)
		sr := &openrouterSSEReader{body: io.NopCloser(strings.NewReader(body))}
		got := drainStream(t, sr)
		if want := ReasoningSentinel + "cot fragment" + ReasoningSentinel; got != want {
			t.Errorf("stream = %q, want %q", got, want)
		}
	})

	t.Run("combined_reasoning_and_content_not_dropped", func(t *testing.T) {
		body := openrouterReasoningBody(`{"reasoning":"why","content":"what"}`)
		sr := &openrouterSSEReader{body: io.NopCloser(strings.NewReader(body))}
		got := drainStream(t, sr)
		if !strings.Contains(got, ReasoningSentinel+"why"+ReasoningSentinel) {
			t.Errorf("reasoning channel lost: %q", got)
		}
		if !strings.Contains(got, "what") {
			t.Errorf("content dropped when a delta carried both fields: %q", got)
		}
	})
}

// TestOpenRouterStreamResult_InlineThinkTagExtraction verifies models that
// return thinking blocks INSIDE delta.content get them rewritten into
// sentinel-wrapped reasoning runs — including markers split across SSE chunk
// boundaries.
func TestOpenRouterStreamResult_InlineThinkTagExtraction(t *testing.T) {
	t.Run("inline_think_block", func(t *testing.T) {
		body := openrouterReasoningBody(`{"content":"<think>pondering</think>answer text"}`)
		sr := &openrouterSSEReader{body: io.NopCloser(strings.NewReader(body))}
		got := drainStream(t, sr)
		if want := ReasoningSentinel + "pondering" + ReasoningSentinel + "answer text"; got != want {
			t.Errorf("stream = %q, want %q", got, want)
		}
	})

	t.Run("marker_split_across_chunks", func(t *testing.T) {
		body := openrouterReasoningBody(
			`{"content":"<thi"}`,
			`{"content":"nk>deep thought</th"}`,
			`{"content":"ink>final"}`,
		)
		sr := &openrouterSSEReader{body: io.NopCloser(strings.NewReader(body))}
		got := drainStream(t, sr)
		if want := ReasoningSentinel + "deep thought" + ReasoningSentinel + "final"; got != want {
			t.Errorf("stream = %q, want %q", got, want)
		}
		if strings.Contains(got, "<think>") || strings.Contains(got, "</think>") {
			t.Errorf("literal think markers leaked to consumers: %q", got)
		}
	})

	t.Run("plain_content_passthrough", func(t *testing.T) {
		body := openrouterReasoningBody(`{"content":"no markers here < > fine"}`)
		sr := &openrouterSSEReader{body: io.NopCloser(strings.NewReader(body))}
		if got := drainStream(t, sr); got != "no markers here < > fine" {
			t.Errorf("plain content mutated: %q", got)
		}
	})

	t.Run("residue_at_stream_end_delivered", func(t *testing.T) {
		body := openrouterReasoningBody(`{"content":"text <thi"}`)
		sr := &openrouterSSEReader{body: io.NopCloser(strings.NewReader(body))}
		got := drainStream(t, sr)
		if strip := stripSentinelMarkers(got); strip != "text <thi" {
			t.Errorf("trailing partial marker lost at DONE: %q", got)
		}
	})
}
