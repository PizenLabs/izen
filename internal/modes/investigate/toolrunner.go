package investigate

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/providers"
)

// ToolResult is the structured output of a single forensic tool run. The engine
// digests it into session.ContextLedger packets ([PKT-N]) for /plan.
type ToolResult struct {
	Tool     Tool
	Target   string
	Content  string // raw diagnostic text to package
	Ok       bool   // false => run failed, trigger strict fallback
	Evidence []Evidence
}

// ToolRunner executes a single forensic tool. It is the programmatic analogue
// of the native UI commands ($env/$trace/$diagnose) and the LX retriever.
type ToolRunner struct {
	root        string
	provider    ai.Provider
	model       string
	retriever   Retriever
	diagnostics string // raw $test failure log for the diagnose fallback
}

func NewToolRunner(root string, provider ai.Provider, model string, retriever Retriever, diagnostics string) *ToolRunner {
	return &ToolRunner{
		root:        root,
		provider:    provider,
		model:       model,
		retriever:   retriever,
		diagnostics: diagnostics,
	}
}

// Run executes the given tool once and returns its structured result. A failed
// run (Ok=false) signals the engine to abort this path and fall back.
func (r *ToolRunner) Run(ctx context.Context, tool Tool, target string) ToolResult {
	switch tool {
	case ToolEnv:
		return r.runEnv(ctx, target)
	case ToolTrace:
		return r.runTrace(ctx, target)
	case ToolDiagnose:
		return r.runDiagnose(ctx, target)
	case ToolLX:
		return r.runLX(ctx, target)
	default:
		return ToolResult{Tool: tool, Target: target, Ok: false}
	}
}

// runEnv reproduces the $env pipeline: Go toolchain version, git state, and the
// relevant environment variables. It is pure shell — no network, sub-second.
func (r *ToolRunner) runEnv(ctx context.Context, target string) ToolResult {
	ectx, cancel := context.WithTimeout(ctx, traceTimeout)
	defer cancel()
	var b strings.Builder
	b.WriteString("\n═══════════════════════════════════════════\n")
	b.WriteString("  [SYSTEM ENVIRONMENT DIAGNOSTICS]\n")
	b.WriteString("═══════════════════════════════════════════\n")

	if v, err := shell(ectx, r.root, "go version"); err == nil {
		b.WriteString("  Go Version : " + strings.TrimSpace(v) + "\n")
	}
	if v, err := shell(ectx, r.root, "git rev-parse --abbrev-ref HEAD"); err == nil {
		b.WriteString("  Git Branch : " + strings.TrimSpace(v) + "\n")
	}
	if v, err := shell(ectx, r.root, "git rev-parse HEAD"); err == nil {
		b.WriteString("  Git Commit : " + strings.TrimSpace(v) + "\n")
	}
	if v, err := shell(ectx, r.root, "git status --short"); err == nil && strings.TrimSpace(v) != "" {
		b.WriteString("  Git Dirt   :\n")
		for _, line := range strings.Split(strings.TrimRight(v, "\n"), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				fmt.Fprintf(&b, "    %s\n", line)
			}
		}
	}
	b.WriteString("  Environment :\n")
	for _, name := range []string{"GOPATH", "GO111MODULE", "GOFLAGS", "GOROOT", "PATH", "SHELL", "TERM", "HOME"} {
		if val, ok := os.LookupEnv(name); ok {
			fmt.Fprintf(&b, "    %s=%s\n", name, val)
		}
	}
	b.WriteString("═══════════════════════════════════════════\n")

	content := b.String()
	return ToolResult{
		Tool:     ToolEnv,
		Target:   target,
		Content:  content,
		Ok:       true,
		Evidence: []Evidence{{Source: EvSourceExecution, Content: content, File: "", Line: 0, Confidence: 0.6}},
	}
}

// runTrace reproduces $trace: a live `go test -run=<target> -v -race` capturing
// panics and data races. Target may be empty (runs the whole suite).
// traceTimeout bounds a single $trace shell invocation so a hung test
// binary can never exceed the 2-5s investigation budget.
const traceTimeout = 20 * time.Second

func (r *ToolRunner) runTrace(ctx context.Context, target string) ToolResult {
	run := target
	if run == "" {
		run = "."
	}
	tctx, cancel := context.WithTimeout(ctx, traceTimeout)
	defer cancel()
	cmd := fmt.Sprintf("go test -run=%s -v -race 2>&1", run)
	out, err := shell(tctx, r.root, cmd)

	failed := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "--- FAIL:") {
			failed++
		}
	}
	passed := err == nil && failed == 0
	if out == "" && err != nil {
		out = err.Error()
	}

	content := fmt.Sprintf("## TRACE (passed=%v): go test -run=%s\n%s", passed, run, out)
	return ToolResult{
		Tool:     ToolTrace,
		Target:   target,
		Content:  content,
		Ok:       true,
		Evidence: []Evidence{{Source: EvSourceTest, Content: content, File: "", Line: 0, Confidence: 0.6}},
	}
}

// runDiagnose reproduces $diagnose: feeds the raw failure log through the
// unified AI provider for a one-sentence root cause. Always terminates the
// fallback chain, so it never "fails" in the abort sense.
func (r *ToolRunner) runDiagnose(ctx context.Context, target string) ToolResult {
	if r.provider == nil {
		return ToolResult{
			Tool:     ToolDiagnose,
			Target:   target,
			Content:  "[diagnose] no AI provider configured; raw diagnostics preserved",
			Ok:       true,
			Evidence: []Evidence{{Source: EvSourceExecution, Content: r.diagnostics, File: "", Line: 0, Confidence: 0.4}},
		}
	}
	resp, err := r.provider.Execute(ctx, ai.Request{
		Model: r.model,
		Messages: []ai.Message{
			{Role: "user", Content: r.diagnostics},
		},
		Stream: false,
		System: providers.DiagnoseSystemPrompt,
	})
	if err != nil || resp == nil {
		// Provider unreachable — still preserve the raw log so the chain ends
		// with usable evidence rather than an empty ledger.
		return ToolResult{
			Tool:     ToolDiagnose,
			Target:   target,
			Content:  fmt.Sprintf("[diagnose] provider error %v; raw log preserved", err),
			Ok:       true,
			Evidence: []Evidence{{Source: EvSourceExecution, Content: r.diagnostics, File: "", Line: 0, Confidence: 0.4}},
		}
	}
	diagnosis := strings.TrimSpace(resp.Content)
	return ToolResult{
		Tool:    ToolDiagnose,
		Target:  target,
		Content: "[diagnosis] " + diagnosis,
		Ok:      true,
		Evidence: []Evidence{
			{Source: EvSourceExecution, Content: diagnosis, File: "", Line: 0, Confidence: 0.8},
		},
	}
}

// runLX routes a targeted symbol/file/package lookup through the LX retriever.
// Unlike env/trace, a hard error here (e.g. RPC -32603) sets Ok=false so the
// engine aborts this path and falls back to the next tool.
func (r *ToolRunner) runLX(ctx context.Context, target string) ToolResult {
	if r.retriever == nil || target == "" {
		return ToolResult{Tool: ToolLX, Target: target, Ok: false}
	}

	var evidence []Evidence
	var collected []SearchResult
	var runErr error

	switch {
	case isFilePathTarget(target):
		{
			path := normalizePathTarget(target)
			results, err := r.retriever.SearchFile(path)
			if err != nil {
				runErr = err
				break
			}
			collected = append(collected, results...)
			readResults, rerr := r.retriever.ReadTarget(path, 30)
			if rerr == nil {
				collected = append(collected, readResults...)
			}
		}
	default:
		results, err := r.retriever.SearchSymbol(target)
		if err != nil {
			runErr = err
			break
		}
		collected = append(collected, results...)
	}

	if runErr != nil {
		// Strict fallback trigger: do NOT retry variants of a broken token.
		return ToolResult{Tool: ToolLX, Target: target, Ok: false}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## LX lookup: %s\n", target)
	for _, res := range collected {
		evidence = append(evidence, Evidence{
			Source:     EvSourceGraph,
			Content:    res.Content,
			File:       res.File,
			Line:       res.Line,
			Confidence: res.Confidence,
		})
		fmt.Fprintf(&b, "  %s:%d %s\n", res.File, res.Line, res.Content)
	}
	if len(collected) == 0 {
		return ToolResult{Tool: ToolLX, Target: target, Ok: false}
	}
	return ToolResult{Tool: ToolLX, Target: target, Content: b.String(), Ok: true, Evidence: evidence}
}

// shell runs a command in the runner root and returns combined stdout.
func shell(ctx context.Context, root, command string) (string, error) {
	c := exec.CommandContext(ctx, "bash", "-c", command)
	c.Dir = root
	var out bytes.Buffer
	c.Stdout = &out
	c.Stderr = &out
	err := c.Run()
	return out.String(), err
}

// isFilePathTarget reports whether a token denotes a directory or file path
// (e.g. cmd/api/main.go, internal/database, ./pkg/foo). Such tokens must be
// targeted as files, never opened as raw string symbol tokens.
func isFilePathTarget(tok string) bool {
	if strings.Contains(tok, "/") || strings.Contains(tok, "\\") {
		return true
	}
	if strings.HasPrefix(tok, "./") || strings.HasPrefix(tok, "../") {
		return true
	}
	return false
}

// normalizePathTarget strips shell/CLI prefixes so a path maps cleanly onto
// the repository layout consumed by the retriever.
func normalizePathTarget(tok string) string {
	tok = strings.Trim(tok, ".,;:()[]{}")
	tok = strings.TrimPrefix(tok, "./")
	tok = strings.TrimPrefix(tok, "../")
	return tok
}
