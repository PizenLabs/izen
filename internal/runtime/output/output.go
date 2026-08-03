// Package output is the Tool Output Intelligence Engine (Phase 1) of Izen.
//
// It transforms raw command execution output into structured, classification-
// driven context ready for LLM consumption:
//
//	Exec Output -> Normalizer -> Classifier (Tool Type) -> Semantic Compressor -> LLM Context
//
// The Normalizer strips ANSI escape codes, unifies carriage returns, and
// normalizes UTF-8. The Classifier inspects the command invocation and tags the
// execution context (GO_TEST, RUST_TEST, GIT_STATUS, LINTER_GO, GENERIC). The
// Semantic Compressor then applies tool-specific compression:
//
//   - GO_TEST / RUST_TEST: drops passing test blocks (=== RUN / PASS); keeps
//     failed assertions, panic traces, and the final execution summary.
//   - LINTER_GO: flattens diagnostics to a uniform
//     <file>:<line>:<col>: [<rule>] <message> line without the repetitive
//     source-code previews.
//   - GENERIC outputs longer than 500 lines: preserves the Head, the
//     Error/Panic region (located by pattern search), and the Tail instead of
//     blindly truncating the middle.
//
// An optional persistent Tee writes the uncompressed, normalized output to
// .logs/ (rotated via a last.log symlink and a 7-day retention window) so raw
// forensic detail is never lost while the LLM only ever sees the compressed
// context. The Pipeline orchestrates the whole flow; a nil Tee keeps the
// pipeline a pure transformation for headless callers.
package output

import (
	"context"
	"errors"
	"os/exec"
	"time"
)

// ToolType tags the execution context of a command so the Semantic Compressor
// can select the appropriate strategy.
type ToolType string

// Canonical execution contexts understood by the classifier.
const (
	ToolGeneric   ToolType = "GENERIC"
	ToolGoTest    ToolType = "GO_TEST"
	ToolRustTest  ToolType = "RUST_TEST"
	ToolGitStatus ToolType = "GIT_STATUS"
	ToolLinterGo  ToolType = "LINTER_GO"
)

// Metrics reports what the pipeline changed about the raw output. Counts are
// best-effort signals: they never gate correctness of the compressed text.
type Metrics struct {
	OriginalLines       int
	OriginalChars       int
	CompressedLines     int
	CompressedChars     int
	DroppedPassingTests int
	FailedTests         int
	Panics              int
	LintIssues          int
	Truncated           bool
	HeadLines           int
	TailLines           int
	RegionLines         int
	ErrorRegionFound    bool
}

// Result is the outcome of processing one command execution through the
// pipeline. Normalized holds the ANSI-stripped, line-ending-unified raw
// output; Compressed holds the semantic compression destined for the LLM
// context; LogPath is the persistent tee log written with the uncompressed
// output (empty when tee logging is disabled).
type Result struct {
	Tool       ToolType
	Normalized string
	Compressed string
	LogPath    string
	ExitCode   int
	Err        error
	Metrics    Metrics
}

// Pipeline is the orchestration entry point. Construct with New and optionally
// attach a persistent Tee (or a workspace root) to enable .logs/ writing.
// A pipeline with no Tee is a pure transformation: it normalizes, classifies,
// and compresses without touching the filesystem.
type Pipeline struct {
	tee *Tee
}

// New returns a Pipeline with tee logging disabled. Call WithTee or
// WithWorkspace to enable persistent .logs/ recording.
func New() *Pipeline {
	return &Pipeline{}
}

// WithTee attaches the persistent tee logger so every Processed execution is
// recorded uncompressed under the tee's log directory. A nil tee disables
// logging.
func (p *Pipeline) WithTee(tee *Tee) *Pipeline {
	p.tee = tee
	return p
}

// WithWorkspace enables tee logging rooted at <root>/.logs/. It is a
// convenience over WithTee(NewTee(root)).
func (p *Pipeline) WithWorkspace(root string) *Pipeline {
	p.tee = NewTee(root)
	return p
}

// Process runs the full pipeline over raw command output: it classifies the
// tool type from the command string, normalizes the raw bytes, applies the
// semantic compression for that type, and — when a Tee is attached — records
// the uncompressed output to the persistent log (updating last.log and
// pruning expired logs).
func (p *Pipeline) Process(command string, raw []byte) Result {
	typ := ClassifyCommand(command)
	norm := Normalize(raw)
	compressed, metrics := Compress(typ, norm)

	res := Result{
		Tool:       typ,
		Normalized: norm,
		Compressed: compressed,
		Metrics:    metrics,
	}
	if p != nil && p.tee != nil {
		if path, err := p.tee.Write(typ, []byte(norm)); err == nil {
			res.LogPath = path
		}
	}
	return res
}

// ExecuteOptions configures a single Execute run.
type ExecuteOptions struct {
	// Dir is the working directory for the command. Empty runs in the
	// current process working directory.
	Dir string
	// Timeout bounds the run. Zero uses the caller's context deadline (or
	// none when the context has none).
	Timeout time.Duration
}

// Execute runs command via the shell and feeds its combined output through the
// pipeline. It is the Exec Output stage of the architecture: the raw
// stdout+stderr bytes are normalized, classified, compressed, and tee-logged
// in one step. A non-zero exit is not an error — it is ordinary signal (a
// failed test run) — so the Result carries the exit code and the caller
// inspects Compressed for the diagnostic context.
func (p *Pipeline) Execute(ctx context.Context, command string, opts ExecuteOptions) Result {
	out, code, err := runCommand(ctx, command, opts)
	res := p.Process(command, out)
	res.ExitCode = code
	res.Err = err
	return res
}

// runCommand executes command in a shell and returns its combined output, the
// process exit code, and any non-exit error (e.g. context deadline, missing
// binary). Exit errors are folded into the exit code so a failed build or test
// run is reported as signal rather than failure.
func runCommand(ctx context.Context, command string, opts ExecuteOptions) ([]byte, int, error) {
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	out, err := cmd.CombinedOutput()
	if err == nil {
		return out, 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return out, exitErr.ExitCode(), nil
	}
	return out, -1, err
}
