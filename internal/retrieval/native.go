package retrieval

import (
	"bufio"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	sympkg "github.com/PizenLabs/izen/internal/retrieval/symbol"
)

// NativeGoEngine implements SearchEngine using Go standard libraries.
// Layer 1: go/ast + go/parser for precise symbol resolution.
// Layer 4: focused file reads for context budget enforcement.
//
// Runs entirely in-memory with zero extra process overhead.
// Target RAM footprint: < 30MB total application memory.
type NativeGoEngine struct {
	root  string
	mu    sync.RWMutex
	fset  *token.FileSet
	files map[string]*ast.File
	cache map[string][]CacheEntry
}

type CacheEntry struct {
	Kind      string
	Name      string
	Line      int
	Column    int
	EndLine   int
	EndColumn int
	Signature string
}

// NewNativeGoEngine creates an engine rooted at the project directory.
func NewNativeGoEngine(root string) *NativeGoEngine {
	return &NativeGoEngine{
		root:  root,
		fset:  token.NewFileSet(),
		files: make(map[string]*ast.File),
		cache: make(map[string][]CacheEntry),
	}
}

// ResolveSymbol resolves a symbol name to its definition coordinates using
// go/ast parsing. Returns all definitions matching the symbol name.
// O(1) average lookup via AST cache.
func (e *NativeGoEngine) ResolveSymbol(ctx context.Context, symbol string) ([]CodeCoord, error) {
	if symbol == "" {
		return nil, nil
	}

	entries, err := e.loadCache()
	if err != nil {
		return nil, fmt.Errorf("native resolve: %w", err)
	}

	var coords []CodeCoord
	for _, entry := range entries {
		if entry.Name == symbol {
			rel := e.relativePath(entry.Name)
			coords = append(coords, CodeCoord{
				File:       rel,
				StartLine:  entry.Line,
				StartCol:   entry.Column,
				EndLine:    entry.EndLine,
				EndCol:     entry.EndColumn,
				SymbolName: entry.Name,
				SymbolKind: entry.Kind,
				Content:    entry.Signature,
				Score:      1.0,
			})
		}
	}

	// Fallback: regex line scan for quick symbol discovery.
	if len(coords) == 0 {
		coords = e.regexResolve(symbol)
	}

	return coords, nil
}

// SearchContext performs full-text search over file content using regex.
// Since NativeGoEngine has no index, this scans files sequentially.
// For large workspaces, consider using ripgrep or an external index.
func (e *NativeGoEngine) SearchContext(ctx context.Context, query string) ([]CodeChunk, error) {
	if query == "" {
		return nil, nil
	}

	pattern, err := regexp.Compile("(?i)" + regexp.QuoteMeta(query))
	if err != nil {
		pattern = regexp.MustCompile("(?i).*" + regexp.QuoteMeta(query) + ".*")
	}

	var chunks []CodeChunk
	walkErr := filepath.Walk(e.root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if sympkg.ShouldIgnorePath(path, e.root) {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		f, openErr := os.Open(path)
		if openErr != nil {
			return nil //nolint:nilerr
		}

		scanner := bufio.NewScanner(f)
		lineNum := 0
		var matchLines []int
		for scanner.Scan() {
			lineNum++
			if pattern.MatchString(scanner.Text()) {
				matchLines = append(matchLines, lineNum)
			}
		}
		_ = f.Close()

		for _, ln := range matchLines {
			rel, _ := filepath.Rel(e.root, path)
			content, _ := e.GetFocusedContext(ctx, rel, ln, ln)
			chunks = append(chunks, CodeChunk{
				File:      rel,
				StartLine: ln,
				EndLine:   ln,
				Content:   content,
				Score:     0.5,
			})
		}
		return nil
	})
	if walkErr != nil {
		return chunks, walkErr
	}

	return chunks, nil
}

// GetFocusedContext reads a specific line range from a file.
// Enforces context budget by returning only the requested range.
func (e *NativeGoEngine) GetFocusedContext(ctx context.Context, file string, startLine, endLine int) (string, error) {
	fullPath := filepath.Join(e.root, file)
	f, err := os.Open(fullPath)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", file, err)
	}
	defer func() { _ = f.Close() }()

	if startLine < 1 {
		startLine = 1
	}
	if endLine > 0 && endLine < startLine {
		endLine = startLine
	}

	var content strings.Builder
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if lineNum < startLine {
			continue
		}
		if endLine > 0 && lineNum > endLine {
			break
		}
		content.WriteString(scanner.Text())
		content.WriteString("\n")
	}

	return content.String(), nil
}

// ── AST Cache ────────────────────────────────────────────────────────────

func (e *NativeGoEngine) loadCache() ([]CacheEntry, error) {
	e.mu.RLock()
	if len(e.cache) > 0 {
		defer e.mu.RUnlock()
		return e.flattenCache(), nil
	}
	e.mu.RUnlock()

	e.mu.Lock()
	defer e.mu.Unlock()

	// Double-check after acquiring write lock.
	if len(e.cache) > 0 {
		return e.flattenCache(), nil
	}

	_ = filepath.Walk(e.root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if sympkg.ShouldIgnorePath(path, e.root) {
			return nil
		}
		e.parseFile(path)
		return nil
	})

	return e.flattenCache(), nil
}

func (e *NativeGoEngine) parseFile(path string) {
	src, err := os.ReadFile(path)
	if err != nil {
		return
	}

	f, err := parser.ParseFile(e.fset, path, src, parser.ParseComments)
	if err != nil {
		return
	}
	e.files[path] = f

	var entries []CacheEntry
	ast.Inspect(f, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncDecl:
			pos := e.fset.Position(node.Pos())
			end := e.fset.Position(node.End())
			sig := node.Name.Name
			if node.Recv != nil && len(node.Recv.List) > 0 {
				recv := e.exprString(node.Recv.List[0].Type)
				sig = "(" + recv + ") " + sig
			}
			sig += signatureFromFieldList(node.Type.Params, node.Type.Results)
			entries = append(entries, CacheEntry{
				Kind: "func", Name: node.Name.Name,
				Line: pos.Line, Column: pos.Column,
				EndLine: end.Line, EndColumn: end.Column,
				Signature: sig,
			})
		case *ast.GenDecl:
			for _, spec := range node.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					pos := e.fset.Position(s.Pos())
					end := e.fset.Position(s.End())
					kind := "type"
					switch s.Type.(type) {
					case *ast.StructType:
						kind = "struct"
					case *ast.InterfaceType:
						kind = "interface"
					}
					entries = append(entries, CacheEntry{
						Kind: kind, Name: s.Name.Name,
						Line: pos.Line, Column: pos.Column,
						EndLine: end.Line, EndColumn: end.Column,
						Signature: kind,
					})
				case *ast.ValueSpec:
					if len(s.Names) > 0 {
						pos := e.fset.Position(s.Pos())
						end := e.fset.Position(s.End())
						entries = append(entries, CacheEntry{
							Kind: "var", Name: s.Names[0].Name,
							Line: pos.Line, Column: pos.Column,
							EndLine: end.Line, EndColumn: end.Column,
						})
					}
				}
			}
		}
		return true
	})

	e.cache[path] = entries
}

func (e *NativeGoEngine) flattenCache() []CacheEntry {
	var all []CacheEntry
	for _, entries := range e.cache {
		all = append(all, entries...)
	}
	return all
}

// ── Regex-based fallback resolution ──────────────────────────────────────

func (e *NativeGoEngine) regexResolve(symbol string) []CodeCoord {
	funPat := regexp.MustCompile(`(?:^|\n)\s*(?:func\s+)?` + regexp.QuoteMeta(symbol) + `\s*\(`)
	typePat := regexp.MustCompile(`(?:^|\n)\s*type\s+` + regexp.QuoteMeta(symbol) + `\s+(?:struct|interface)`)
	structPat := regexp.MustCompile(`(?:^|\n)\s*type\s+` + regexp.QuoteMeta(symbol) + `\s+struct`)

	var coords []CodeCoord
	_ = filepath.Walk(e.root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if sympkg.ShouldIgnorePath(path, e.root) {
			return nil
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil //nolint:nilerr
		}
		text := string(data)

		var match funMatch
		if m := structPat.FindStringIndex(text); m != nil {
			match = findLine(text, m[0], "struct")
		} else if m := typePat.FindStringIndex(text); m != nil {
			match = findLine(text, m[0], "type")
		} else if m := funPat.FindStringIndex(text); m != nil {
			match = findLine(text, m[0], "func")
		} else {
			return nil
		}

		rel, _ := filepath.Rel(e.root, path)
		coords = append(coords, CodeCoord{
			File:       rel,
			StartLine:  match.line,
			SymbolName: symbol,
			SymbolKind: match.kind,
			Content:    match.content,
			Score:      0.9,
		})
		return nil
	})

	return coords
}

type funMatch struct {
	line    int
	kind    string
	content string
}

func findLine(text string, offset int, kind string) funMatch {
	lineNum := strings.Count(text[:offset], "\n") + 1
	end := strings.IndexByte(text[offset:], '\n')
	if end < 0 {
		end = len(text) - offset
	}
	content := strings.TrimSpace(text[offset : offset+end])
	return funMatch{line: lineNum, kind: kind, content: content}
}

// ── Expression helpers ───────────────────────────────────────────────────

func (e *NativeGoEngine) exprString(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.StarExpr:
		return "*" + e.exprString(x.X)
	case *ast.SelectorExpr:
		return e.exprString(x.X) + "." + x.Sel.Name
	case *ast.ArrayType:
		return "[]" + e.exprString(x.Elt)
	default:
		return "?"
	}
}

func (e *NativeGoEngine) relativePath(absPath string) string {
	rel, err := filepath.Rel(e.root, absPath)
	if err != nil {
		return absPath
	}
	return rel
}

func signatureFromFieldList(params, results *ast.FieldList) string {
	var b strings.Builder
	b.WriteString("(")
	if params != nil {
		for i, f := range params.List {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(typeName(f.Type))
		}
	}
	b.WriteString(")")
	if results != nil && len(results.List) > 0 {
		b.WriteString(" ")
		if len(results.List) == 1 && results.List[0].Names == nil {
			b.WriteString(typeName(results.List[0].Type))
		} else {
			b.WriteString("(")
			for i, f := range results.List {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(typeName(f.Type))
			}
			b.WriteString(")")
		}
	}
	return b.String()
}

func typeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + typeName(t.X)
	case *ast.SelectorExpr:
		return typeName(t.X) + "." + t.Sel.Name
	case *ast.ArrayType:
		return "[]" + typeName(t.Elt)
	default:
		return "?"
	}
}

// MutationTracer provides AST-based symbol assignment tracing.
// It uses go/ast for precise point-of-assignment discovery.
type MutationTracer struct {
	root  string
	fset  *token.FileSet
	files map[string]*ast.File
}

type MutationPoint struct {
	File       string
	Line       int
	Column     int
	VarName    string
	Kind       string
	TypeName   string
	Expr       string
	SymbolName string
}

func NewMutationTracer(root string) *MutationTracer {
	return &MutationTracer{
		root:  root,
		fset:  token.NewFileSet(),
		files: make(map[string]*ast.File),
	}
}

func (mt *MutationTracer) loadFile(path string) (*ast.File, error) {
	if f, ok := mt.files[path]; ok {
		return f, nil
	}
	fullPath := filepath.Join(mt.root, path)
	src, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, err
	}
	f, err := parser.ParseFile(mt.fset, path, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	mt.files[path] = f
	return f, nil
}

func (mt *MutationTracer) TraceAssignments(symbolName string) ([]MutationPoint, error) {
	return mt.traceAssignments(symbolName)
}

func (mt *MutationTracer) traceAssignments(symbolName string) ([]MutationPoint, error) {
	var points []MutationPoint
	_ = filepath.Walk(mt.root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.Contains(path, "vendor/") || strings.Contains(path, ".izen/") {
			return nil
		}
		f, loadErr := mt.loadFile(path)
		if loadErr != nil {
			return nil //nolint:nilerr
		}
		ast.Inspect(f, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, lhs := range assign.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok || ident.Name != symbolName {
					continue
				}
				pos := mt.fset.Position(ident.Pos())
				var rhsStr string
				if i < len(assign.Rhs) {
					rhsStr = exprString(assign.Rhs[i])
				}
				kind := "assign"
				if assign.Tok == token.DEFINE {
					kind = "define"
				}
				rel, _ := filepath.Rel(mt.root, path)
				points = append(points, MutationPoint{
					File: rel, Line: pos.Line, Column: pos.Column,
					VarName: symbolName, Kind: kind, Expr: rhsStr, SymbolName: symbolName,
				})
			}
			return true
		})
		return nil
	})
	return points, nil
}

func exprString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.BasicLit:
		return e.Value
	case *ast.CallExpr:
		if sel, ok := e.Fun.(*ast.SelectorExpr); ok {
			return exprString(sel.X) + "." + sel.Sel.Name + "(...)"
		}
		if ident, ok := e.Fun.(*ast.Ident); ok {
			return ident.Name + "(...)"
		}
		return "fn(...)"
	case *ast.SelectorExpr:
		return exprString(e.X) + "." + e.Sel.Name
	case *ast.StarExpr:
		return "*" + exprString(e.X)
	default:
		return "?"
	}
}
