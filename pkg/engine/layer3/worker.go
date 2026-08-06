package layer3

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/izen/internal/patch"
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

// LinePatchParser parses a completion into proposed file patches. Two output
// forms are accepted — both never require diff markers, because a new file has
// no old content to diff against:
//
//  1. The deterministic file-block protocol:
//
//     === FILE: <relative path>
//     <full replacement content>
//     === END
//
//     Whitespace inside a block is preserved verbatim; the content between the
//     header and the terminator replaces the target file entirely. Blocks may
//     appear in any order and may target different paths.
//
//  2. Markdown code fences carrying an inline path header (```lang:path,
//     ```lang path, ```file=path). Small models frequently emit path-tagged
//     fences instead of === FILE: blocks; both are interpreted as complete
//     replacement content for the tagged path. Parsing is delegated to
//     internal/patch (the single owner of patch extraction).
type LinePatchParser struct{}

// Parse implements PatchParser.
func (LinePatchParser) Parse(text string) ([]FilePatch, error) {
	// Pass 1: the deterministic === FILE: protocol.
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

	// Pass 2: markdown code fences with path headers. Fence-only parsing so a
	// === FILE: block handled in pass 1 is never duplicated.
	for _, cf := range patch.ParseMarkdownFences(text) {
		if cf.Path == "" {
			continue
		}
		newContent := strings.TrimSuffix(cf.Content, "\n")
		out = append(out, FilePatch{
			Path:       cf.Path,
			Language:   cf.Lang,
			New:        newContent,
			LinesAdded: strings.Count(newContent, "\n"),
			Changed:    true,
		})
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

// targetFileOnDisk stats the target file and returns its current content. It
// is the single owner of the new-vs-existing decision for the worker prompt
// ("One Question, One Owner"): whether the target happens to be listed in the
// Layer 2 execution context is NOT authoritative — the actual filesystem state
// is. A missing path, a directory, or a 0-byte file is treated as new.
func targetFileOnDisk(target string) (string, bool) {
	if target == "" {
		return "", false
	}
	fi, err := os.Stat(target)
	if err != nil || fi.IsDir() || fi.Size() == 0 {
		return "", false
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return "", false
	}
	return string(data), true
}

// smallFileLineThreshold is the "Explicit Over Implicit" stub boundary. A
// target that does not exist, is 0 bytes, or has fewer than this many lines is
// treated as a stub and forced through the whole-file overwrite protocol: a
// SEARCH/REPLACE diff against a stub has no reliable "old content" anchor and
// makes weak models fail with "ambiguous snippet without SEARCH/REPLACE
// markers" or loop until timeout.
const smallFileLineThreshold = 100

// isStubContent reports whether the on-disk content represents a stub
// (empty/whitespace only or under smallFileLineThreshold lines) that must be
// whole-file overwritten, never diffed.
func isStubContent(content string) bool {
	return strings.TrimSpace(content) == "" || fileLineCount(content) < smallFileLineThreshold
}

// fileLineCount returns the number of newline-terminated lines in content
// (wc -l semantics), matching the execution layer's stub boundary.
func fileLineCount(content string) int {
	return strings.Count(content, "\n")
}

// BuildPrompt deterministically renders a Layer 2 execution context plus a
// request into a stateless worker prompt, bounded to maxChars characters.
//
// The OUTPUT PROTOCOL (full-creation vs replacement) is chosen by an
// authoritative on-disk stat of the target file, never by whether the file is
// listed in the Layer 2 context. A new/0-byte target must be created from
// scratch: forcing a diff against a file with no old content makes weak models
// emit invalid diffs and loop until the request times out.
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

	// Authoritative on-disk state of the target, decided before any prompt
	// section is rendered.
	targetContent, targetExists := targetFileOnDisk(req.TargetFile)
	// Stub classification drives the whole-file overwrite protocol: missing,
	// 0-byte, or under-smallFileLineThreshold content is always overwritten in
	// full and never forced through a diff against incomplete "old content".
	targetStub := !targetExists || isStubContent(targetContent)

	files := 0
	if exec != nil {
		for _, f := range exec.Files {
			// The target's on-disk content is rendered below; skip a stale
			// context copy so the model never sees two versions of one file.
			if targetExists && filepath.Clean(f.Path) == filepath.Clean(req.TargetFile) {
				continue
			}
			fmt.Fprintf(&b, "\n### %s\n%s\n", f.Path, f.Source)
			files++
		}
	}
	if targetExists {
		fmt.Fprintf(&b, "\n### %s\n%s\n", req.TargetFile, targetContent)
		if targetStub {
			fmt.Fprintf(&b, "The current file is an incomplete skeleton. Fully implement and expand all functions, styles, and markup. Do NOT repeat incomplete stubs.\n")
		}
		files++
	}
	fmt.Fprintf(&b, "Files: %d\n", files)

	// ── OUTPUT PROTOCOL ─────────────────────────────────────────────────
	// The format is driven by the stat above. Stubs (new/0-byte/under
	// smallFileLineThreshold lines) get an explicit whole-file overwrite
	// contract with diff formats forbidden; large existing files keep the
	// complete-replacement contract. Both use the deterministic FILE block
	// protocol (never fragments, never a raw unified diff).
	b.WriteString("\nOUTPUT PROTOCOL\n")
	if targetStub {
		if targetExists {
			fmt.Fprintf(&b, "- The target file %q is a stub (%d bytes, %d lines). Output its COMPLETE, FULLY IMPLEMENTED content.\n",
				req.TargetFile, len(targetContent), fileLineCount(targetContent))
		} else {
			fmt.Fprintf(&b, "- The target file %q does NOT exist on disk (new file). CREATE it with the COMPLETE new file content.\n",
				req.TargetFile)
		}
		b.WriteString("- Do NOT output a unified diff (--- a/ ... +++ b/) or SEARCH/REPLACE blocks (<<<<<<< SEARCH) — a stub has no complete old content to diff against.\n")
	} else {
		fmt.Fprintf(&b, "- The target file %q EXISTS on disk (%d bytes, %d lines). Output its COMPLETE replacement content.\n",
			req.TargetFile, len(targetContent), fileLineCount(targetContent))
	}
	b.WriteString("- Output the full file content in a single block:\n")
	b.WriteString("  === FILE: <relative path>\n  <complete file content>\n  === END\n")
	b.WriteString("- Do NOT output any text outside FILE blocks.\n")

	out := b.String()
	if maxChars > 0 && len(out) > maxChars {
		out = out[:maxChars]
	}
	return out
}
