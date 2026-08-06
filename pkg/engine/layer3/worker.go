package layer3

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/pkg/engine/layer2"
)

// Provider identifies an LLM backend. Providers are labels only: the actual
// model call happens inside the injected WorkerClient.
type Provider string

const (
	// ProviderOpenAI is the OpenAI provider.
	ProviderOpenAI Provider = "openai"
	// ProviderOpenRouter is the OpenRouter provider.
	ProviderOpenRouter Provider = "openrouter"
	// ProviderClaude is the Anthropic Claude provider.
	ProviderClaude Provider = "claude"
	// ProviderLocal is a local model provider (Ollama, LM Studio, ...).
	ProviderLocal Provider = "local"
)

// String returns the machine-readable provider label.
func (p Provider) String() string { return string(p) }

// Worker is a stateless LLM execution worker. It receives an immutable
// ExecutionContext from Layer 2 and returns proposed patches. A worker never
// owns or mutates system state: every Execute call is independent and state
// transitions stay exclusively inside the pipeline.
type Worker interface {
	// Name identifies the worker/provider for telemetry.
	Name() string
	// Execute proposes patches for req given the Layer 2 execution context.
	Execute(ctx context.Context, exec *layer2.ExecutionContext, req Request) (*WorkerResult, error)
}

// WorkerResult is the proposed outcome of one worker invocation.
type WorkerResult struct {
	// Patches are the proposed file mutations.
	Patches []FilePatch
	// Reason summarizes why the worker produced these patches.
	Reason string
	// Tokens reports model token accounting.
	Tokens TokenUsage
	// Raw is the unparsed completion text, for audit and debugging.
	Raw string
}

// TokenUsage reports model token accounting for one invocation.
type TokenUsage struct {
	Input  int
	Output int
}

// Total returns the combined token usage.
func (u TokenUsage) Total() int { return u.Input + u.Output }

// WorkerClient performs the underlying model call for a provider.
type WorkerClient interface {
	// Complete performs one stateless model call.
	Complete(ctx context.Context, req *CompletionRequest) (*CompletionResponse, error)
}

// CompletionRequest is a single stateless model call.
type CompletionRequest struct {
	Provider Provider
	Model    string
	Prompt   string
}

// CompletionResponse is the model's reply to a CompletionRequest.
type CompletionResponse struct {
	Text   string
	Tokens TokenUsage
}

// PatchParser interprets a worker's raw completion into proposed patches.
type PatchParser interface {
	Parse(text string) ([]FilePatch, error)
}

// ErrInvalidPatch is returned when a completion cannot be parsed into patches.
var ErrInvalidPatch = errors.New("layer3: invalid patch")

// LinePatchParser parses the deterministic file-block protocol:
//
//	=== FILE: <relative path>
//	<full replacement content>
//	=== END
//
// Whitespace inside a block is preserved verbatim; the content between the
// header and the terminator replaces the target file entirely. Blocks may
// appear in any order and may target different paths.
type LinePatchParser struct{}

// Parse implements PatchParser.
func (LinePatchParser) Parse(text string) ([]FilePatch, error) {
	var out []FilePatch
	lines := strings.Split(text, "\n")
	var cur *FilePatch
	var content []string
	flush := func() error {
		if cur != nil {
			cur.New = strings.Join(content, "\n")
			cur.Changed = true
			cur.LinesAdded = strings.Count(cur.New, "\n")
			out = append(out, *cur)
		}
		cur = nil
		content = nil
		return nil
	}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "=== FILE:"):
			if err := flush(); err != nil {
				return nil, err
			}
			path := strings.TrimSpace(strings.TrimPrefix(trimmed, "=== FILE:"))
			if path == "" {
				return nil, fmt.Errorf("%w: empty file header at line %d", ErrInvalidPatch, i+1)
			}
			cur = &FilePatch{Path: path}
		case trimmed == "=== END":
			if err := flush(); err != nil {
				return nil, err
			}
		default:
			if cur != nil {
				content = append(content, line)
			}
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return out, nil
}

// StatelessWorker is the reference Worker implementation. It is immutable
// after construction: provider, model, client and parser are fixed, and a
// single instance may be reused concurrently. It carries zero state between
// invocations; the pipeline is the only place execution state lives.
type StatelessWorker struct {
	provider  Provider
	model     string
	client    WorkerClient
	parser    PatchParser
	maxPrompt int
}

// WorkerOption configures a StatelessWorker.
type WorkerOption func(*StatelessWorker)

// WithPatchParser installs the patch parser used to interpret completions.
func WithPatchParser(p PatchParser) WorkerOption {
	return func(w *StatelessWorker) { w.parser = p }
}

// WithMaxPromptChars caps the rendered prompt size in characters.
func WithMaxPromptChars(n int) WorkerOption {
	return func(w *StatelessWorker) {
		if n > 0 {
			w.maxPrompt = n
		}
	}
}

// NewStatelessWorker returns a stateless worker wired to client.
func NewStatelessWorker(provider Provider, model string, client WorkerClient, opts ...WorkerOption) *StatelessWorker {
	w := &StatelessWorker{
		provider:  provider,
		model:     model,
		client:    client,
		parser:    LinePatchParser{},
		maxPrompt: 64000,
	}
	for _, o := range opts {
		o(w)
	}
	return w
}

// Name returns the worker's provider label.
func (w *StatelessWorker) Name() string { return string(w.provider) }

// Execute renders a deterministic prompt from the Layer 2 execution context
// and the request, calls the client, parses the completion into proposed
// patches and returns them. The worker never mutates any system state.
func (w *StatelessWorker) Execute(ctx context.Context, exec *layer2.ExecutionContext, req Request) (*WorkerResult, error) {
	if w.client == nil {
		return nil, fmt.Errorf("%w: worker has no client", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	prompt := BuildPrompt(exec, req, w.maxPrompt)
	resp, err := w.client.Complete(ctx, &CompletionRequest{
		Provider: w.provider,
		Model:    w.model,
		Prompt:   prompt,
	})
	if err != nil {
		return nil, fmt.Errorf("layer3: %s completion: %w", w.Name(), err)
	}
	patches, err := w.parser.Parse(resp.Text)
	if err != nil {
		return nil, err
	}
	return &WorkerResult{
		Patches: patches,
		Reason:  req.Description,
		Tokens:  resp.Tokens,
		Raw:     resp.Text,
	}, nil
}

// FuncWorker adapts a plain function to the Worker interface. It is primarily
// a test seam and a way to compose deterministic generators.
type FuncWorker func(ctx context.Context, exec *layer2.ExecutionContext, req Request) (*WorkerResult, error)

// Name returns the adapter's label.
func (f FuncWorker) Name() string { return "func-worker" }

// Execute implements Worker by delegating to the wrapped function.
func (f FuncWorker) Execute(ctx context.Context, exec *layer2.ExecutionContext, req Request) (*WorkerResult, error) {
	if f == nil {
		return nil, fmt.Errorf("%w: nil worker function", ErrInvalidRequest)
	}
	return f(ctx, exec, req)
}

// BuildPrompt deterministically renders a Layer 2 execution context plus a
// request into a stateless worker prompt, bounded to maxChars characters.
func BuildPrompt(exec *layer2.ExecutionContext, req Request, maxChars int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Intent: %s\n", req.Intent)
	if req.TargetFile != "" {
		fmt.Fprintf(&b, "Target file: %s\n", req.TargetFile)
	}
	if req.TargetSymbol != "" {
		fmt.Fprintf(&b, "Target symbol: %s\n", req.TargetSymbol)
	}
	if req.Description != "" {
		fmt.Fprintf(&b, "Description: %s\n", req.Description)
	}
	files := 0
	if exec != nil {
		files = len(exec.Files)
		for _, f := range exec.Files {
			fmt.Fprintf(&b, "\n### %s\n%s\n", f.Path, f.Source)
		}
	}
	fmt.Fprintf(&b, "Files: %d\n", files)

	// ── OUTPUT PROTOCOL ─────────────────────────────────────────────────
	// The target file may or may not exist. When it is not listed above
	// (or is empty), it must be created from scratch — a SEARCH/REPLACE
	// patch against a non-existent file has no "old content" to match and
	// causes reasoning loops on small models.
	b.WriteString("\nOUTPUT PROTOCOL\n")
	fmt.Fprintf(&b, "- The target file %q is listed above with its full current content only if it exists on disk.\n", req.TargetFile)
	b.WriteString("- If the target file is NOT listed above (it does not exist or is 0 bytes), produce the COMPLETE new file content in a single block:\n")
	b.WriteString("  === FILE: <relative path>\n  <complete replacement content>\n  === END\n")
	b.WriteString("- If the target file IS listed above, output a full replacement block for it too (the complete updated content between === FILE: and === END) — never a fragment.\n")
	b.WriteString("- Do NOT output any text outside FILE blocks.\n")

	out := b.String()
	if maxChars > 0 && len(out) > maxChars {
		out = out[:maxChars]
	}
	return out
}
