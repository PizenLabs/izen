package providers

// ── A SYNTHESIZED STREAM MUST CARRY THE RESPONSE'S CAPABILITIES ─────────────
//
// The reported defect: real OpenRouter calls reported thousands of tokens while
// IZEN displayed `↑0 · ↓0`.
//
// The cause was not the renderer. The $prompt mutation lane runs the bounded
// read-only capability tool loop and then presents the completed answer as a
// stream. That wrapper carried ONLY the content bytes, so the provider's
// usage, finish_reason and metadata were discarded at the seam. The executor —
// correctly — found no usage capability, recorded "unknown", and the footer
// faithfully rendered a fabricated zero.
//
// These tests pin the capability contract at that seam: whatever the provider
// billed must survive the wrapping.

import (
	"io"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
)

// TestSynthesizedStream_PreservesProviderUsage is the direct regression: a
// response carrying real usage must arrive as a stream that reports it.
func TestSynthesizedStream_PreservesProviderUsage(t *testing.T) {
	resp := &ai.Response{
		Content: "<!DOCTYPE html>\n<html><body><h1>Hello</h1></body></html>",
		Usage: ai.ProviderUsage{
			Known:            true,
			PromptTokens:     2181,
			CompletionTokens: 5883,
			ReasoningTokens:  5000,
			CachedTokens:     64,
			FinishReason:     "stop",
		},
	}

	stream := newSynthesizedStream(resp)

	// The bytes are still the answer.
	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("reading the synthesized stream: %v", err)
	}
	if string(got) != resp.Content {
		t.Fatalf("stream content = %q, want %q", string(got), resp.Content)
	}

	// And the provider's billing evidence survived the wrapping.
	up, ok := stream.(ai.UsageProvider)
	if !ok {
		t.Fatal("a synthesized stream must expose the ai.UsageProvider seam; without it the executor cannot see the provider's usage")
	}
	u := up.Usage()
	if !u.Known {
		t.Fatal("the provider reported usage and the synthesized stream reports it unknown — this is what renders as a fabricated zero")
	}
	if u.PromptTokens != 2181 || u.CompletionTokens != 5883 || u.ReasoningTokens != 5000 {
		t.Errorf("stream usage = %d/%d (+%d reasoning), want the provider's 2181/5883 (+5000)",
			u.PromptTokens, u.CompletionTokens, u.ReasoningTokens)
	}
	if u.TotalTokens == 0 {
		t.Error("the total token count was not derived from the reported halves")
	}
}

// TestSynthesizedStream_PreservesFinishReason pins the truncation signal: an
// output-ceiling cut must be observable on this path exactly as it is on the SSE
// one. A wrapper that erased finish_reason would turn an exhausted generation
// into an apparently clean one.
func TestSynthesizedStream_PreservesFinishReason(t *testing.T) {
	stream := newSynthesizedStream(&ai.Response{
		Content:      "partial",
		FinishReason: "length",
		Truncated:    true,
		Usage:        ai.ProviderUsage{Known: true, PromptTokens: 900, CompletionTokens: 3072, FinishReason: "length"},
	})
	fr, ok := stream.(ai.FinishReasonProvider)
	if !ok {
		t.Fatal("a synthesized stream must expose the finish_reason seam")
	}
	if got := fr.FinishReason(); !isOutputLength(got) {
		t.Errorf("finish_reason = %q, want an output-length truncation", got)
	}
}

// TestSynthesizedStream_PreservesMetadata pins the contract provenance: the
// interaction contract travels on the response metadata and must not be lost when
// the answer is re-presented as a stream.
func TestSynthesizedStream_PreservesMetadata(t *testing.T) {
	resp := &ai.Response{
		Content:      "answer",
		ContractID:   "ct-abc123",
		Mode:         "build",
		FinishReason: "stop",
		Usage:        ai.ProviderUsage{Known: true, PromptTokens: 10, CompletionTokens: 5},
	}
	StampResponseMetadata(resp, "openrouter", "test/model", ContractSerialization{}, "stop")

	stream := newSynthesizedStream(resp)
	md, ok := stream.(ai.ResponseMetadataProvider)
	if !ok {
		t.Fatal("a synthesized stream must expose the response-metadata seam")
	}
	got := md.ResponseMetadata()
	if got.Usage.Known != resp.Usage.Known {
		t.Errorf("metadata usage known = %v, want %v", got.Usage.Known, resp.Usage.Known)
	}
	if got.Usage.PromptTokens != 10 || got.Usage.CompletionTokens != 5 {
		t.Errorf("metadata usage = %d/%d, want the response's 10/5",
			got.Usage.PromptTokens, got.Usage.CompletionTokens)
	}
}

// TestSynthesizedStream_LegacyUsageTransportIsRecovered covers the adapters that
// report usage on the response fields rather than the ProviderUsage record: the
// wrapper must read them, or the bill disappears on exactly the path that reports
// it the old way.
func TestSynthesizedStream_LegacyUsageTransportIsRecovered(t *testing.T) {
	stream := newSynthesizedStream(&ai.Response{
		Content:      "answer",
		TokenInput:   800,
		TokenOutput:  300,
		FinishReason: "stop",
	})
	u := stream.(ai.UsageProvider).Usage()
	if !u.Known || u.PromptTokens != 800 || u.CompletionTokens != 300 {
		t.Errorf("legacy usage transport was lost: known=%v %d/%d", u.Known, u.PromptTokens, u.CompletionTokens)
	}
}

// TestSynthesizedStream_ReportsUnknownRatherThanZero pins the honesty rule: a
// response with no usage at all must stay UNKNOWN. The wrapper must never
// manufacture a zero that renders as a measurement.
func TestSynthesizedStream_ReportsUnknownRatherThanZero(t *testing.T) {
	stream := newSynthesizedStream(&ai.Response{Content: "answer", FinishReason: "stop"})
	u := stream.(ai.UsageProvider).Usage()
	if u.Known {
		t.Fatalf("a response with no usage was reported as known (in=%d out=%d); unknown must stay unknown",
			u.PromptTokens, u.CompletionTokens)
	}
}

// TestSynthesizedStream_NilResponseIsSafeAndUnknown keeps the fail-safe path
// honest: no response means no bytes AND no usage.
func TestSynthesizedStream_NilResponseIsSafeAndUnknown(t *testing.T) {
	stream := newSynthesizedStream(nil)
	body, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("a nil response must still yield a readable stream: %v", err)
	}
	if len(body) != 0 {
		t.Errorf("a nil response produced %d bytes: %q", len(body), string(body))
	}
	if u := stream.(ai.UsageProvider).Usage(); u.Known {
		t.Error("a nil response reported known usage")
	}
	// And the finish_reason seam must not panic.
	_ = stream.(ai.FinishReasonProvider).FinishReason()
	_ = stream.(ai.ResponseMetadataProvider).ResponseMetadata()
}

var _ = strings.NewReader
