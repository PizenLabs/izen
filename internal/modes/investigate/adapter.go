package investigate

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/retrieval"
)

// ForensicsRetriever is the subset of retrieval.Retriever that /investigate
// needs. It is an interface (not the concrete *retrieval.Retriever) so the
// adapter is testable and decoupled from the global LX controller lifecycle.
type ForensicsRetriever interface {
	SearchSymbol(name string) *retrieval.ResultSet
	SearchText(text string) *retrieval.ResultSet
	SearchFile(path string) *retrieval.ResultSet
	SearchPackage(pkg string) *retrieval.ResultSet
	ReadTarget(path string, lines int) *retrieval.ResultSet
}

// RetrieverAdapter bridges the graph-based retrieval.Retriever (LX) into the
// investigate engine's Retriever interface. It maps retrieval.ResultSet
// entries into investigate.SearchResult so /investigate can perform genuine
// Language-Server-grade forensic lookups instead of short-circuiting.
type RetrieverAdapter struct {
	inner ForensicsRetriever
}

// NewRetrieverAdapter wraps a graph-backed retrieval.Retriever for use by the
// investigate engine. A nil inner retriever yields an adapter that reports no
// results, so callers must supply a real graph lookup to enable forensics.
func NewRetrieverAdapter(inner ForensicsRetriever) *RetrieverAdapter {
	return &RetrieverAdapter{inner: inner}
}

func adaptResultSet(rs *retrieval.ResultSet) []SearchResult {
	if rs == nil {
		return nil
	}
	out := make([]SearchResult, 0, len(rs.Results))
	for _, r := range rs.Results {
		out = append(out, SearchResult{
			File:       r.File,
			Line:       r.Line,
			Column:     r.Column,
			Content:    r.Content,
			Confidence: r.Confidence,
			Strategy:   r.Strategy,
			SymbolName: r.SymbolName,
			SymbolKind: r.SymbolKind,
			Score:      r.Score,
		})
	}
	return out
}

func (a *RetrieverAdapter) SearchSymbol(name string) ([]SearchResult, error) {
	if a.inner == nil {
		return nil, nil
	}
	return adaptResultSet(a.inner.SearchSymbol(name)), nil
}

func (a *RetrieverAdapter) SearchText(text string) ([]SearchResult, error) {
	if a.inner == nil {
		return nil, nil
	}
	return adaptResultSet(a.inner.SearchText(text)), nil
}

func (a *RetrieverAdapter) SearchFile(path string) ([]SearchResult, error) {
	if a.inner == nil {
		return nil, nil
	}
	return adaptResultSet(a.inner.SearchFile(path)), nil
}

func (a *RetrieverAdapter) SearchPackage(pkg string) ([]SearchResult, error) {
	if a.inner == nil {
		return nil, nil
	}
	return adaptResultSet(a.inner.SearchPackage(pkg)), nil
}

func (a *RetrieverAdapter) ReadTarget(path string, lines int) ([]SearchResult, error) {
	if a.inner == nil {
		return nil, nil
	}
	return adaptResultSet(a.inner.ReadTarget(path, lines)), nil
}

// ShellTestExecutor runs the project's test suite via `go test` to feed real
// diagnostic output (compiler errors, stack traces, failures) into /investigate.
// It honors the investigate capability contract: read-only diagnostic shell
// execution only — no writes, no patches.
type ShellTestExecutor struct {
	root    string
	timeout time.Duration
}

// NewShellTestExecutor constructs an executor rooted at the given directory.
func NewShellTestExecutor(root string) *ShellTestExecutor {
	return &ShellTestExecutor{root: root, timeout: 5 * time.Minute}
}

func (e *ShellTestExecutor) run(ctx context.Context, args ...string) (*TestResultSummary, error) {
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", args...)
	if e.root != "" {
		cmd.Dir = e.root
	}
	raw, err := cmd.CombinedOutput()

	summary := &TestResultSummary{Output: string(raw)}

	passed := ctx.Err() == nil && err == nil
	summary.Passed = passed

	// Parse a coarse pass/fail line count so downstream evidence is populated
	// even when the test tooling returns a non-zero exit (test failures are
	// still valid diagnostic signal, not an execution error).
	seenFail := false
	for _, line := range strings.Split(summary.Output, "\n") {
		l := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(l, "ok "):
			summary.PassedN++
			summary.Total++
		case strings.HasPrefix(l, "--- FAIL"):
			summary.FailedN++
			summary.Total++
			seenFail = true
			name := strings.TrimSpace(strings.TrimPrefix(l, "--- FAIL"))
			summary.Failed = append(summary.Failed, name)
		case strings.HasPrefix(l, "FAIL"):
			summary.FailedN++
			seenFail = true
		}
	}
	if seenFail {
		summary.Passed = false
	}

	// A non-zero exit that is NOT a context error still carries diagnostic
	// value; return it as a summary rather than a hard error so the engine
	// continues to process real evidence.
	if err != nil && ctx.Err() == nil {
		return summary, nil
	}
	if ctx.Err() != nil {
		return summary, ctx.Err()
	}
	return summary, nil
}

// RunAllTestsContext runs the full test suite, threading the inherited context
// from the caller (used by the engine's mandatory forensic probe so the run
// deadline is derived from the investigation's parent context).
func (e *ShellTestExecutor) RunAllTestsContext(ctx context.Context) (*TestResultSummary, error) {
	return e.run(ctx, "test", "./...")
}

func (e *ShellTestExecutor) RunAllTests() (*TestResultSummary, error) {
	ctx, cancel := context.WithTimeout(context.Background(), e.timeout)
	defer cancel()
	return e.run(ctx, "test", "./...")
}

func (e *ShellTestExecutor) RunPackageTests(pkg string) (*TestResultSummary, error) {
	ctx, cancel := context.WithTimeout(context.Background(), e.timeout)
	defer cancel()
	return e.run(ctx, "test", pkg)
}

func (e *ShellTestExecutor) RunSpecificTest(pkg, test string) (*TestResultSummary, error) {
	ctx, cancel := context.WithTimeout(context.Background(), e.timeout)
	defer cancel()
	return e.run(ctx, "test", "-run", test, pkg)
}
