package analyzer

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const defaultMaxFiles = 10000

// maxReadBytes caps how much of a single file is read during analysis. Files
// larger than this are read partially; the token estimate is still derived
// from the truncated byte count so a pathological file can never exhaust
// memory.
const maxReadBytes = 4 << 20 // 4 MiB

// skippedDirs are directory basenames never walked during a workspace scan.
var skippedDirs = map[string]struct{}{
	".git": {}, ".izen": {}, ".idea": {}, ".vscode": {}, ".venv": {},
	"node_modules": {}, "vendor": {}, "bin": {}, "dist": {}, "build": {},
	"target": {}, "venv": {}, ".terraform": {},
}

// sourceExts are the file extensions treated as source for file counting and
// token estimation.
var sourceExts = map[string]struct{}{
	".go": {}, ".ts": {}, ".tsx": {}, ".js": {}, ".jsx": {}, ".py": {},
	".rs": {}, ".java": {}, ".c": {}, ".cpp": {}, ".h": {}, ".sh": {},
	".swift": {}, ".rb": {}, ".php": {}, ".cs": {}, ".mod": {},
}

// importRe catches inline `from 'pkg'`, `require('pkg')` and `import 'pkg'`
// statements in non-Go sources.
var importRe = regexp.MustCompile(`(?:from|import|require)\s*\(?\s*[` + "`" + `'"]([^` + "`" + `'"]+)[` + "`" + `'"]`)

// Option configures an Analyzer.
type Option func(*Analyzer)

// WithMaxFiles caps the number of files scanned by one analysis pass. The
// default is 10000.
func WithMaxFiles(n int) Option {
	return func(a *Analyzer) {
		if n > 0 {
			a.maxFiles = n
		}
	}
}

// WithWalkDir overrides the directory walker used to discover source files
// (test seam).
func WithWalkDir(fn func(string, fs.WalkDirFunc) error) Option {
	return func(a *Analyzer) {
		if fn != nil {
			a.walk = fn
		}
	}
}

// Analyzer produces workspace Facts. It is immutable after construction and
// therefore safe for concurrent use.
type Analyzer struct {
	root     string
	maxFiles int
	walk     func(string, fs.WalkDirFunc) error
}

// New returns an Analyzer rooted at the given workspace directory.
func New(root string, opts ...Option) *Analyzer {
	a := &Analyzer{
		root:     root,
		maxFiles: defaultMaxFiles,
		walk:     filepath.WalkDir,
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Request describes the user input and the files it targets.
type Request struct {
	Input   string
	Targets []string
}

// Analyze extracts workspace facts for the request. When no targets are given
// the whole workspace is scanned; otherwise only the target files are
// analyzed. Conversational prompts (IntentChat) with no explicit targets skip
// the scan entirely: they never touch files, AST symbols or code operations,
// so there are no workspace facts to gather. The returned Facts are immutable
// and deterministically ordered.
func (a *Analyzer) Analyze(ctx context.Context, req Request) (*Facts, error) {
	start := time.Now()
	intent, confidence, reason := ParseIntentConfidence(req.Input)
	targets, err := normalizeTargets(a.root, req.Targets)
	if err != nil {
		return nil, err
	}
	facts := &Facts{
		Root:             a.root,
		Input:            req.Input,
		Intent:           intent,
		IntentConfidence: confidence,
		IntentReason:     reason,
		TargetFiles:      targets,
		DependencyFanout: map[string][]string{},
		GeneratedAt:      start,
	}
	if intent == IntentChat && len(targets) == 0 {
		facts.Duration = time.Since(start)
		return facts, nil
	}
	if err := a.scan(ctx, facts); err != nil {
		return nil, err
	}
	facts.Duration = time.Since(start)
	return facts, nil
}

// scan collects the file set, token estimate, dependency fanout and AST
// scopes into facts.
func (a *Analyzer) scan(ctx context.Context, facts *Facts) error {
	files := facts.TargetFiles
	if len(files) == 0 {
		all, err := a.walkSource()
		if err != nil {
			return err
		}
		files = all
	}
	if len(files) > a.maxFiles {
		files = files[:a.maxFiles]
	}
	facts.Files = len(files)
	for _, path := range files {
		if err := ctx.Err(); err != nil {
			return ctx.Err()
		}
		if err := a.analyzeFile(facts, path); err != nil {
			return fmt.Errorf("analyzer: analyze %s: %w", path, err)
		}
	}
	sortScopes(facts.ModifiedScopes)
	return nil
}

// analyzeFile reads one source file and records its token estimate,
// dependency fanout and (for Go files) AST scopes.
func (a *Analyzer) analyzeFile(facts *Facts, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() {
		_ = f.Close()
	}()
	data, err := io.ReadAll(io.LimitReader(f, maxReadBytes))
	if err != nil {
		return err
	}
	facts.TokenEstimate += len(data) / 4
	if !strings.HasSuffix(path, ".go") {
		facts.DependencyFanout[path] = importsFor(path, string(data))
		facts.updateMaxFanout()
		return nil
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		facts.DependencyFanout[path] = importsFor(path, string(data))
		facts.updateMaxFanout()
		return nil
	}
	facts.DependencyFanout[path] = importsOf(file)
	facts.updateMaxFanout()
	for _, decl := range file.Decls {
		facts.ModifiedScopes = append(facts.ModifiedScopes, declScopes(path, decl, fset)...)
	}
	return nil
}

// updateMaxFanout recomputes the maximum per-file dependency count.
func (f *Facts) updateMaxFanout() {
	for _, deps := range f.DependencyFanout {
		if len(deps) > f.MaxFanout {
			f.MaxFanout = len(deps)
		}
	}
}

// walkSource enumerates all source files under the workspace root, skipping
// version-control, dependency and build directories.
func (a *Analyzer) walkSource() ([]string, error) {
	var files []string
	err := a.walk(a.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != a.root && isSkippedDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if isSourceFile(d.Name()) {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("analyzer: walk workspace: %w", err)
	}
	sort.Strings(files)
	return files, nil
}

// normalizeTargets cleans, deduplicates and sorts the requested target files
// relative to the workspace root, failing on missing paths.
func normalizeTargets(root string, targets []string) ([]string, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		clean := filepath.Clean(t)
		if !filepath.IsAbs(clean) {
			clean = filepath.Join(root, clean)
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		if _, err := os.Stat(clean); err != nil {
			return nil, fmt.Errorf("analyzer: target %s: %w", t, err)
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	sort.Strings(out)
	return out, nil
}

func isSkippedDir(name string) bool {
	_, ok := skippedDirs[name]
	return ok
}

func isSourceFile(name string) bool {
	_, ok := sourceExts[strings.ToLower(filepath.Ext(name))]
	return ok
}

// importsOf returns the sorted, deduplicated import paths of a parsed Go
// file.
func importsOf(f *ast.File) []string {
	set := map[string]struct{}{}
	for _, imp := range f.Imports {
		if imp.Path != nil {
			set[strings.Trim(imp.Path.Value, `"`)] = struct{}{}
		}
	}
	return sortedKeys(set)
}

// importsFor returns the sorted, deduplicated import-like tokens of a
// non-Go source, using lightweight line heuristics.
func importsFor(path, content string) []string {
	set := map[string]struct{}{}
	for _, line := range strings.Split(content, "\n") {
		trim := strings.TrimSpace(line)
		lower := strings.ToLower(trim)
		switch {
		case strings.HasPrefix(lower, "import "):
			if tok := firstWord(strings.TrimPrefix(trim, "import ")); tok != "" {
				set[tok] = struct{}{}
			}
		case strings.HasPrefix(lower, "from "):
			if tok := firstWord(strings.TrimPrefix(trim, "from ")); tok != "" {
				set[tok] = struct{}{}
			}
		}
		for _, m := range importRe.FindAllStringSubmatch(trim, -1) {
			if len(m) > 1 && m[1] != "" {
				set[m[1]] = struct{}{}
			}
		}
	}
	return sortedKeys(set)
}

// declScopes projects one top-level Go declaration into modified AST scopes.
func declScopes(path string, decl ast.Decl, fset *token.FileSet) []Scope {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		kind := "func"
		if d.Recv != nil {
			kind = "method"
		}
		return []Scope{{
			Path: path, Kind: kind, Name: d.Name.Name,
			LineStart: fset.Position(d.Pos()).Line,
			LineEnd:   fset.Position(d.End()).Line,
		}}
	case *ast.GenDecl:
		if d.Tok != token.TYPE {
			return nil
		}
		scopes := make([]Scope, 0, len(d.Specs))
		for _, spec := range d.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			scopes = append(scopes, Scope{
				Path: path, Kind: "type", Name: ts.Name.Name,
				LineStart: fset.Position(d.Pos()).Line,
				LineEnd:   fset.Position(d.End()).Line,
			})
		}
		return scopes
	default:
		return nil
	}
}

func sortScopes(scopes []Scope) {
	sort.Slice(scopes, func(i, j int) bool {
		if scopes[i].Path != scopes[j].Path {
			return scopes[i].Path < scopes[j].Path
		}
		if scopes[i].LineStart != scopes[j].LineStart {
			return scopes[i].LineStart < scopes[j].LineStart
		}
		return scopes[i].Name < scopes[j].Name
	})
}

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func firstWord(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	fields := strings.Fields(s)
	return fields[0]
}
