package retrieval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// LynxAdapter implements SearchEngine by delegating to an externally
// installed `lx` CLI binary. It uses os/exec to invoke the binary with
// appropriate subcommands (lx resolve, lx search).
//
// This adapter is only created when lx is found in PATH at startup.
// It NEVER attempts to download, build, or copy lx if not present.
type LynxAdapter struct {
	binaryPath string
	root       string
}

// NewLynxAdapter creates an adapter for the given lx binary path.
func NewLynxAdapter(binaryPath, root string) *LynxAdapter {
	return &LynxAdapter{
		binaryPath: binaryPath,
		root:       root,
	}
}

// ResolveSymbol calls `lx resolve <symbol>` and parses the JSON output.
func (a *LynxAdapter) ResolveSymbol(ctx context.Context, symbol string) ([]CodeCoord, error) {
	if symbol == "" {
		return nil, nil
	}
	out, err := a.runLX(ctx, "resolve", symbol)
	if err != nil {
		return nil, fmt.Errorf("lx resolve: %w", err)
	}
	return a.parseResolveOutput(out)
}

// SearchContext calls `lx search <query>` and parses the JSON output.
func (a *LynxAdapter) SearchContext(ctx context.Context, query string) ([]CodeChunk, error) {
	if query == "" {
		return nil, nil
	}
	out, err := a.runLX(ctx, "search", query)
	if err != nil {
		return nil, fmt.Errorf("lx search: %w", err)
	}
	return a.parseSearchOutput(out)
}

// GetFocusedContext calls `lx context <file> <start> <end>` and returns
// the focused snippet.
func (a *LynxAdapter) GetFocusedContext(ctx context.Context, file string, startLine, endLine int) (string, error) {
	if file == "" {
		return "", nil
	}
	lineArg := fmt.Sprintf("%d", startLine)
	if endLine > 0 {
		lineArg = fmt.Sprintf("%d:%d", startLine, endLine)
	}
	out, err := a.runLX(ctx, "context", file, lineArg)
	if err != nil {
		// Fallback: native file read
		return "", nil //nolint:nilerr
	}
	return strings.TrimSpace(string(out)), nil
}

// runLX executes lx with the given subcommand and args.
func (a *LynxAdapter) runLX(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, a.binaryPath, args...)
	cmd.Dir = a.root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w\nstderr: %s", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// lxResolveResult mirrors the JSON output of `lx resolve`.
type lxResolveResult struct {
	FilePath   string  `json:"file_path"`
	StartLine  int     `json:"start_line"`
	EndLine    int     `json:"end_line"`
	StartCol   int     `json:"start_col"`
	EndCol     int     `json:"end_col"`
	SymbolName string  `json:"symbol_name"`
	SymbolKind string  `json:"symbol_kind"`
	Content    string  `json:"content"`
	Score      float64 `json:"score"`
}

func (a *LynxAdapter) parseResolveOutput(data []byte) ([]CodeCoord, error) {
	var results []lxResolveResult
	if err := json.Unmarshal(data, &results); err != nil {
		return nil, fmt.Errorf("parse resolve output: %w", err)
	}
	coords := make([]CodeCoord, 0, len(results))
	for _, r := range results {
		score := r.Score
		if score <= 0 {
			score = 0.9
		}
		coords = append(coords, CodeCoord{
			File:       r.FilePath,
			StartLine:  r.StartLine,
			StartCol:   r.StartCol,
			EndLine:    r.EndLine,
			EndCol:     r.EndCol,
			SymbolName: r.SymbolName,
			SymbolKind: r.SymbolKind,
			Content:    r.Content,
			Score:      score,
		})
	}
	return coords, nil
}

type lxSearchResult struct {
	FilePath   string  `json:"file_path"`
	StartLine  int     `json:"start_line"`
	EndLine    int     `json:"end_line"`
	SymbolName string  `json:"symbol_name"`
	Content    string  `json:"content"`
	Score      float64 `json:"score"`
}

func (a *LynxAdapter) parseSearchOutput(data []byte) ([]CodeChunk, error) {
	var results []lxSearchResult
	if err := json.Unmarshal(data, &results); err != nil {
		return nil, fmt.Errorf("parse search output: %w", err)
	}
	chunks := make([]CodeChunk, 0, len(results))
	for _, r := range results {
		chunks = append(chunks, CodeChunk{
			File:       r.FilePath,
			StartLine:  r.StartLine,
			EndLine:    r.EndLine,
			SymbolName: r.SymbolName,
			Content:    r.Content,
			Score:      r.Score,
		})
	}
	return chunks, nil
}

// IsLXAvailable checks whether lx is available in PATH.
func IsLXAvailable() bool {
	_, err := exec.LookPath("lx")
	return err == nil
}

// FindLXPath returns the path to the lx binary, or empty string if not found.
func FindLXPath() string {
	path, err := exec.LookPath("lx")
	if err != nil {
		return ""
	}
	return path
}
