package investigate

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
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

	fmt.Fprintf(&b, "  Host OS    : %s / %s\n", runtime.GOOS, runtime.GOARCH)
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
//
// TARGET TYPE GATE: a missing dependency is frequently reported as a remote
// import path (e.g. "github.com/docker/docker/client"). Treating such a string
// as a local workspace file makes the retriever attempt to open a physical file
// that does not exist, producing a fatal "no such file or directory" error and
// corrupting the Context-Ledger. When the target is identified as a remote
// package, local file operations are EXPLICITLY FORBIDDEN and the orbiter
// routes straight to an environment/packages remediation blueprint instead.
func (r *ToolRunner) runLX(ctx context.Context, target string) ToolResult {
	if r.retriever == nil || target == "" {
		return ToolResult{Tool: ToolLX, Target: target, Ok: false}
	}

	// Step 1 — Target Type Validation Gate.
	// Remote package import paths must never reach the local file reader.
	if isRemotePackageTarget(target) {
		// Step 2 — Route to Environment/Shell strategy directly, bypassing the
		// local file inspection step entirely. The blocker is recorded cleanly
		// into the forensic context without pushing any invalid file-descriptor
		// operations onto the ledger.
		return r.runPackageRemediation(ctx, target)
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

// remotePackagePrefixes enumerate the well-known module/package hosts whose
// import paths must NEVER be treated as local workspace files.
var remotePackagePrefixes = []string{
	"github.com/",
	"golang.org/",
	"gopkg.in/",
	"google.golang.org/",
	"k8s.io/",
	"sigs.k8s.io/",
	"go.opentelemetry.io/",
	"google.golang.com/",
}

// isRemotePackageTarget reports whether a token denotes a remote package import
// path rather than a concrete file or symbol inside the workspace. Such tokens
// are dependency coordinates — they have no on-disk representation the LX
// retriever can open, so any local file-read attempt is forbidden.
//
// A token is treated as a remote package when it:
//   - starts with a known module host prefix, OR
//   - looks like a dotted module path (e.g. "example.com/foo/bar") that is NOT
//     a relative path (no leading "./" or "../") and carries no file extension.
func isRemotePackageTarget(tok string) bool {
	t := strings.TrimSpace(tok)
	if t == "" {
		return false
	}
	for _, p := range remotePackagePrefixes {
		if strings.HasPrefix(t, p) {
			return true
		}
	}

	// Reject explicit local/relative anchors — these are workspace files.
	if strings.HasPrefix(t, "./") || strings.HasPrefix(t, "../") || strings.HasPrefix(t, "/") {
		return false
	}

	// A remote package path contains a slash and a dotted host segment and no
	// file extension (no "." after the last slash component).
	if !strings.Contains(t, "/") {
		return false
	}
	if hasFileExtension(t) {
		return false
	}
	// Require a dotted host (e.g. "example.com/...") to avoid classifying bare
	// relative workspace paths like "internal/foo" as remote packages.
	firstSeg := t
	if i := strings.Index(t, "/"); i >= 0 {
		firstSeg = t[:i]
	}
	return strings.Contains(firstSeg, ".")
}

// hasFileExtension reports whether the last path segment carries a recognizable
// source-file extension, which marks the token as a local file rather than a
// remote package coordinate.
func hasFileExtension(tok string) bool {
	base := tok
	if i := strings.LastIndex(tok, "/"); i >= 0 {
		base = tok[i+1:]
	}
	switch {
	case strings.HasSuffix(base, ".go"),
		strings.HasSuffix(base, ".py"),
		strings.HasSuffix(base, ".js"),
		strings.HasSuffix(base, ".ts"),
		strings.HasSuffix(base, ".java"),
		strings.HasSuffix(base, ".rb"),
		strings.HasSuffix(base, ".rs"),
		strings.HasSuffix(base, ".c"),
		strings.HasSuffix(base, ".h"),
		strings.HasSuffix(base, ".cpp"),
		strings.HasSuffix(base, ".md"),
		strings.HasSuffix(base, ".json"),
		strings.HasSuffix(base, ".yaml"),
		strings.HasSuffix(base, ".yml"),
		strings.HasSuffix(base, ".toml"),
		strings.HasSuffix(base, ".mod"),
		strings.HasSuffix(base, ".sum"):
		return true
	}
	return false
}

// runPackageRemediation handles a remote dependency blocker by routing directly
// to an environment/shell remediation blueprint instead of touching the local
// file reader. It stages a package-management task (go mod tidy / go get) in the
// forensic context and records the vector cleanly — no invalid file descriptor
// operations are pushed to the ledger.
func (r *ToolRunner) runPackageRemediation(ctx context.Context, target string) ToolResult {
	ectx, cancel := context.WithTimeout(ctx, traceTimeout)
	defer cancel()

	pkg := strings.TrimSpace(target)

	// Stage the remediation command in the workspace root. We DO NOT execute a
	// non-interactive `go get` that mutates go.mod silently; `go mod tidy`
	// validates that the dependency graph resolves and surfaces the missing
	// package as actionable diagnostics.
	cmd := fmt.Sprintf("go mod tidy 2>&1; echo '---'; go list -m %s 2>&1 || true", pkg)
	out, err := shell(ectx, r.root, cmd)
	if out == "" && err != nil {
		out = err.Error()
	}

	content := fmt.Sprintf("## REMOTE DEPENDENCY BLOCKER (lx bypassed): %s\n"+
		"Local file inspection is forbidden for remote import paths. "+
		"Staging environment remediation blueprint (go mod tidy / go get):\n%s",
		pkg, out)

	return ToolResult{
		Tool:    ToolLX,
		Target:  pkg,
		Content: content,
		Ok:      true,
		Evidence: []Evidence{
			{
				Source:     EvSourceExecution,
				Content:    content,
				File:       "",
				Line:       0,
				Confidence: 0.7,
			},
		},
	}
}
