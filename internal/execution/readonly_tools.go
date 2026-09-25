package execution

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/internal/ai"
)

// ReadOnlyToolRunner is the execution-pipeline implementation of
// ai.ToolRunner. It executes IZEN's authentic read-only inspection tools
// against the live workspace. It never mutates the filesystem.
type ReadOnlyToolRunner struct {
	root string
}

// NewReadOnlyToolRunner builds a runner rooted at the workspace. An empty root
// resolves to the current working directory.
func NewReadOnlyToolRunner(root string) *ReadOnlyToolRunner {
	if strings.TrimSpace(root) == "" {
		if cwd, err := os.Getwd(); err == nil {
			root = cwd
		}
	}
	return &ReadOnlyToolRunner{root: root}
}

// Run dispatches one read-only tool call. Unknown or mutating tools are
// rejected with an explicit error so the loop can return a bounded tool result
// instead of executing anything outside the read-only contract.
func (r *ReadOnlyToolRunner) Run(_ context.Context, call ai.ToolCall) (string, error) {
	if r == nil {
		return "", fmt.Errorf("read-only tools: nil runner")
	}
	switch call.Function.Name {
	case ai.ToolReadFile:
		return r.readFile(call.Function.Arguments)
	case ai.ToolListDirectory:
		return r.listDirectory(call.Function.Arguments)
	case ai.ToolSearchCodebase:
		return r.searchCodebase(call.Function.Arguments)
	case ai.ToolSymbolLookup:
		return r.symbolLookup(call.Function.Arguments)
	default:
		return "", fmt.Errorf("read-only tools: unsupported tool %q", call.Function.Name)
	}
}

const (
	// readFileMaxBytes bounds a single read_file result.
	readFileMaxBytes = 256 * 1024
	// listDirectoryMaxEntries bounds one directory listing.
	listDirectoryMaxEntries = 500
	// searchDefaultMaxResults bounds a search_codebase result set.
	searchDefaultMaxResults = 50
	// searchScanMaxFiles bounds the number of files scanned per search.
	searchScanMaxFiles = 5000
)

// resolveWithinRoot resolves target relative to the runner root and guarantees
// the result stays inside the workspace (defends against ../ traversal).
func (r *ReadOnlyToolRunner) resolveWithinRoot(target string) (string, error) {
	target = strings.TrimSpace(target)
	root := r.root
	abs := target
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, target)
	}
	abs = filepath.Clean(abs)
	rootClean := filepath.Clean(root)
	if rootClean != "" && rootClean != "." {
		rel, err := filepath.Rel(rootClean, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return "", fmt.Errorf("path %q escapes the workspace root", target)
		}
	}
	return abs, nil
}

func (r *ReadOnlyToolRunner) readFile(args string) (string, error) {
	var params ai.ReadFileParams
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("read_file: parse arguments: %w", err)
	}
	if strings.TrimSpace(params.Path) == "" {
		return "", fmt.Errorf("read_file: path is required")
	}
	abs, err := r.resolveWithinRoot(params.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("read_file %s: %w", params.Path, err)
	}
	if len(data) > readFileMaxBytes {
		data = data[:readFileMaxBytes]
	}
	content := string(data)
	if params.StartLine > 0 || params.EndLine > 0 {
		content = sliceLines(content, params.StartLine, params.EndLine)
	}
	return content, nil
}

func (r *ReadOnlyToolRunner) listDirectory(args string) (string, error) {
	var params ai.ListDirectoryParams
	if strings.TrimSpace(args) != "" {
		if err := json.Unmarshal([]byte(args), &params); err != nil {
			return "", fmt.Errorf("list_directory: parse arguments: %w", err)
		}
	}
	abs, err := r.resolveWithinRoot(params.Path)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return "", fmt.Errorf("list_directory %s: %w", params.Path, err)
	}
	names := make([]string, 0, len(entries))
	for i, e := range entries {
		if i >= listDirectoryMaxEntries {
			names = append(names, "… (truncated)")
			break
		}
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, "\n"), nil
}

func (r *ReadOnlyToolRunner) searchCodebase(args string) (string, error) {
	var params ai.SearchCodebaseParams
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("search_codebase: parse arguments: %w", err)
	}
	query := strings.TrimSpace(params.Query)
	if query == "" {
		return "", fmt.Errorf("search_codebase: query is required")
	}
	max := params.MaxResults
	if max <= 0 {
		max = searchDefaultMaxResults
	}
	base, err := r.resolveWithinRoot(params.Path)
	if err != nil {
		return "", err
	}
	return r.walkSearch(base, func(line string) bool {
		return strings.Contains(line, query)
	}, max)
}

func (r *ReadOnlyToolRunner) symbolLookup(args string) (string, error) {
	var params ai.SymbolLookupParams
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("symbol_lookup: parse arguments: %w", err)
	}
	symbol := strings.TrimSpace(params.Symbol)
	if symbol == "" {
		return "", fmt.Errorf("symbol_lookup: symbol is required")
	}
	base, err := r.resolveWithinRoot(params.Path)
	if err != nil {
		return "", err
	}
	needles := []string{
		"func " + symbol, "type " + symbol, "struct " + symbol, "interface " + symbol,
		"class " + symbol, "def " + symbol, "function " + symbol, "const " + symbol,
		"var " + symbol, symbol + "(",
	}
	return r.walkSearch(base, func(line string) bool {
		for _, needle := range needles {
			if strings.Contains(line, needle) {
				return true
			}
		}
		return false
	}, searchDefaultMaxResults)
}

// walkSearch scans text files under base, returning "path:line: text" records
// for every line the predicate matches.
func (r *ReadOnlyToolRunner) walkSearch(base string, match func(string) bool, max int) (string, error) {
	info, err := os.Stat(base)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return r.searchFile(base, match, max)
	}
	var out []string
	scanned := 0
	walkErr := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			//nolint:nilerr // best-effort scan: an unreadable entry is skipped, never fatal
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" || name == ".izen" {
				return filepath.SkipDir
			}
			return nil
		}
		if scanned >= searchScanMaxFiles || len(out) >= max {
			return filepath.SkipAll
		}
		scanned++
		_, ferr := r.scanFile(path, func(lineNo int, line string) bool {
			if len(out) >= max {
				return false
			}
			if match(line) {
				rel, rerr := filepath.Rel(r.root, path)
				if rerr != nil {
					rel = path
				}
				out = append(out, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(rel), lineNo, strings.TrimRight(line, "\r\n")))
				return true
			}
			return false
		})
		if ferr != nil {
			//nolint:nilerr // best-effort scan: an unreadable/binary file is skipped
			return nil
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, filepath.SkipAll) {
		return "", walkErr
	}
	if len(out) == 0 {
		return "no matches", nil
	}
	return strings.Join(out, "\n"), nil
}

func (r *ReadOnlyToolRunner) searchFile(path string, match func(string) bool, max int) (string, error) {
	var out []string
	_, err := r.scanFile(path, func(_ int, line string) bool {
		if len(out) >= max {
			return false
		}
		if match(line) {
			out = append(out, strings.TrimRight(line, "\r\n"))
			return true
		}
		return false
	})
	if err != nil {
		return "", err
	}
	if len(out) == 0 {
		return "no matches", nil
	}
	return strings.Join(out, "\n"), nil
}

func (r *ReadOnlyToolRunner) scanFile(path string, fn func(lineNo int, line string) bool) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	// Skip binary files: any NUL byte in the first block marks it non-text.
	head := make([]byte, 512)
	n, _ := f.Read(head)
	if bytesContainsZero(head[:n]) {
		return 0, fmt.Errorf("binary file")
	}
	if _, err := f.Seek(0, 0); err != nil {
		return 0, err
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		// The predicate may signal a match, but scanning continues until the
		// predicate's own result bound is reached; the bool is informational.
		_ = fn(lineNo, scanner.Text())
	}
	return lineNo, scanner.Err()
}

func bytesContainsZero(b []byte) bool {
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}

// sliceLines returns a 1-based inclusive line range of content. Non-positive
// bounds mean "from the start" / "to the end".
func sliceLines(content string, start, end int) string {
	lines := strings.Split(content, "\n")
	if start <= 0 {
		start = 1
	}
	if end <= 0 || end > len(lines) {
		end = len(lines)
	}
	if start > len(lines) || start > end {
		return ""
	}
	return strings.Join(lines[start-1:end], "\n")
}

// compile-time assertion: the runner satisfies the provider contract.
var _ ai.ToolRunner = (*ReadOnlyToolRunner)(nil)
