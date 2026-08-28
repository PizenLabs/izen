package execution

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/events"
)

// ── PASS 1 MANIFEST COMPACTNESS + REJECTION (free-tier model hardening) ─────

// manifestPassProvider records the wire contract of every manifest invocation
// and returns the configured response.
type manifestPassProvider struct {
	mu       sync.Mutex
	response *ai.Response
	requests []ai.Request
}

func (m *manifestPassProvider) Name() string { return "manifest-mock" }

func (m *manifestPassProvider) Execute(_ context.Context, req ai.Request) (*ai.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, req)
	return m.response, nil
}

func (m *manifestPassProvider) ExecuteStream(_ context.Context, _ ai.Request) (io.ReadCloser, error) {
	return nil, errors.New("stream not supported in manifest mock")
}

func (m *manifestPassProvider) recorded() []ai.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]ai.Request(nil), m.requests...)
}

// TestInvokeManifestPass_CompactDirectivePinned proves the Pass 1 manifest
// wire contract that prevents OUTPUT_EXHAUSTED on free-tier models: the
// injected compact system prompt demands a minified JSON array with ZERO prose
// and MAX 200 TOKENS, and the request enforces that ceiling.
func TestInvokeManifestPass_CompactDirectivePinned(t *testing.T) {
	p := &manifestPassProvider{response: &ai.Response{
		Content: `{"targetFile":"index.html","intent":"remove redundant content","mutations":[{"selector":"#hero","action":"delete","estimatedLines":5}]}`,
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 100, CompletionTokens: 40, FinishReason: "stop"},
	}}
	root := t.TempDir()
	x := NewRuntimeExecutor(root, config.Default(), p, events.NewBus(events.DefaultBufferSize), "")

	compactPrompt := "OUTPUT ONLY VALID MINIFIED JSON ARRAY OF MUTATION TARGETS. DO NOT WRITE CODE, DO NOT EXPLAIN, DO NOT INCLUDE MARKDOWN FENCES. MAX 200 TOKENS."
	x.SetManifestSystemPrompt(compactPrompt + " — (wrapped by the autonomy layer)")

	raw, err := x.InvokeManifestPass(context.Background(), "check this file @index.html and remove redundant content", []byte("<html></html>"))
	if err != nil {
		t.Fatalf("InvokeManifestPass: %v", err)
	}
	if !strings.Contains(raw, "#hero") {
		t.Fatalf("raw manifest lost the mutation: %q", raw)
	}
	reqs := p.recorded()
	if len(reqs) != 1 {
		t.Fatalf("invocations = %d, want 1", len(reqs))
	}
	a := reqs[0]
	for _, want := range []string{
		"OUTPUT ONLY VALID MINIFIED JSON ARRAY OF MUTATION TARGETS",
		"DO NOT WRITE CODE",
		"DO NOT EXPLAIN",
		"DO NOT INCLUDE MARKDOWN FENCES",
		"MAX 200 TOKENS",
	} {
		if !strings.Contains(a.System, want) {
			t.Fatalf("manifest request missing compact directive %q:\n%s", want, a.System)
		}
	}
	if a.MaxTokens != 200 {
		t.Fatalf("max_tokens = %d, want 200 (the MAX 200 TOKENS ceiling)", a.MaxTokens)
	}
	if a.Reasoning == nil || !a.Reasoning.Disabled {
		t.Fatal("manifest pass must disable hidden reasoning (it would crowd the tiny JSON budget)")
	}
}

// TestInvokeManifestPass_VerboseRejectedAsInvalidJSON proves a manifest output
// that still exceeds the 512-token rejection ceiling (a provider ignoring
// max_tokens) is surfaced as an invalid-manifest failure — never an
// OUTPUT_EXHAUSTED gate signal.
func TestInvokeManifestPass_VerboseRejectedAsInvalidJSON(t *testing.T) {
	verbose := `{"targetFile":"index.html","intent":"x","mutations":[{"selector":"#a","action":"modify","estimatedLines":1},` +
		strings.Repeat(`{"selector":"#pad","action":"modify","estimatedLines":1},`, 5000) + `{"selector":"#z","action":"delete","estimatedLines":1}]}`
	if len(verbose)/4 <= manifestPassRejectTokens {
		t.Fatalf("verbose fixture is ~%d tokens, want > %d", len(verbose)/4, manifestPassRejectTokens)
	}
	p := &manifestPassProvider{response: &ai.Response{
		Content: verbose,
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 100, CompletionTokens: len(verbose) / 4, FinishReason: "stop"},
	}}
	x := NewRuntimeExecutor(t.TempDir(), config.Default(), p, nil, "")
	raw, err := x.InvokeManifestPass(context.Background(), "obj", []byte("<html></html>"))
	if err == nil {
		t.Fatalf("InvokeManifestPass accepted a %d-token manifest (raw=%d bytes)", len(verbose)/4, len(raw))
	}
	if errors.Is(err, ErrOutputExhausted) {
		t.Fatalf("verbose manifest must NOT surface OUTPUT_EXHAUSTED — it is rejected as invalid JSON: %v", err)
	}
	if !strings.Contains(err.Error(), "rejected as invalid manifest") {
		t.Fatalf("rejection must name the invalid-manifest cause: %v", err)
	}
}

// TestInvokeManifestPass_TruncatedLengthIsPlainJSONFailure proves a
// finish_reason="length" manifest generation crosses as raw bytes (the caller
// rejects the truncated JSON) instead of triggering the OUTPUT_EXHAUSTED gate
// — a verbose free-tier manifest falls back silently.
func TestInvokeManifestPass_TruncatedLengthIsPlainJSONFailure(t *testing.T) {
	truncated := `{"targetFile":"index.html","intent":"x","mutations":[{"selector":"#a","act`
	p := &manifestPassProvider{response: &ai.Response{
		Content: truncated,
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 100, CompletionTokens: 200, FinishReason: "length"},
	}}
	x := NewRuntimeExecutor(t.TempDir(), config.Default(), p, nil, "")
	raw, err := x.InvokeManifestPass(context.Background(), "obj", []byte("<html></html>"))
	if err != nil {
		t.Fatalf("InvokeManifestPass surfaced the output gate on truncation: %v", err)
	}
	if raw != truncated {
		t.Fatalf("raw = %q, want the verbatim truncated content", raw)
	}
	// The truncated JSON crosses to the caller, whose manifest parser rejects it
	// (a silent fallback) — the gate never surfaces OUTPUT_EXHAUSTED.
}
