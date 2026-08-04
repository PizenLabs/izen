package layer2

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
)

// bodyElided replaces a stripped function body.
const bodyElided = "{ /* body elided */ }"

// CompressResult describes one compressed file.
type CompressResult struct {
	Content  string
	Tokens   int
	Stripped int
}

// Compressor is an AST-aware code compressor. It strips the bodies of
// low-relevance functions and methods while preserving function signatures,
// struct/type definitions, interfaces and doc comments, so a file's full
// structural context survives with a fraction of the token footprint.
//
// Go sources are processed with the Go AST; TypeScript/JavaScript bodies are
// located with a brace-matching scanner driven by the lea-extracted symbol
// spans. It is stateless and safe for concurrent use.
type Compressor struct{}

// NewCompressor returns a compressor.
func NewCompressor() *Compressor { return &Compressor{} }

// Compress strips the bodies of symbols for which strip returns true.
// Unsupported languages and files with nothing to strip are returned
// unchanged.
func (c *Compressor) Compress(language string, src []byte, symbols []SymbolInfo, strip func(SymbolInfo) bool) (CompressResult, error) {
	orig := EstimateTokens(string(src))
	noop := CompressResult{Content: string(src), Tokens: orig}
	if strip == nil || len(symbols) == 0 || len(src) == 0 {
		return noop, nil
	}
	switch language {
	case "go":
		return c.compressGo(src, symbols, strip)
	case "typescript", "javascript":
		return c.compressBraces(src, symbols, strip)
	default:
		return noop, nil
	}
}

// byteRange is a half-open [start, end) byte interval of a source file.
type byteRange struct {
	start int
	end   int
}

// compressGo strips function/method bodies using the Go AST. Doc comments,
// signatures, structs, interfaces and all non-function declarations are
// preserved because only the exact body byte ranges are removed.
func (c *Compressor) compressGo(src []byte, symbols []SymbolInfo, strip func(SymbolInfo) bool) (CompressResult, error) {
	noop := CompressResult{Content: string(src), Tokens: EstimateTokens(string(src))}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return noop, err
	}

	symByQual := make(map[string]SymbolInfo, len(symbols))
	symByName := make(map[string][]SymbolInfo, len(symbols))
	for _, si := range symbols {
		if si.QualName != "" {
			symByQual[si.QualName] = si
		}
		symByName[si.Name] = append(symByName[si.Name], si)
	}

	var ranges []byteRange
	stripped := 0
	ast.Inspect(f, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			return true
		}
		si, found := resolveDeclSymbol(fd, fset, symByQual, symByName)
		if !found || !strip(si) {
			return true
		}
		start := int(fd.Body.Pos()) - 1
		end := int(fd.Body.End()) - 1
		if end <= start {
			return true
		}
		ranges = append(ranges, byteRange{start: start, end: end})
		stripped++
		return true
	})

	out := applyRanges(src, ranges)
	return CompressResult{Content: string(out), Tokens: EstimateTokens(string(out)), Stripped: stripped}, nil
}

// resolveDeclSymbol maps a function declaration to its extracted symbol,
// preferring the qualified-name match and falling back to name + declaration
// line.
func resolveDeclSymbol(fd *ast.FuncDecl, fset *token.FileSet, byQual map[string]SymbolInfo, byName map[string][]SymbolInfo) (SymbolInfo, bool) {
	if si, ok := byQual[qualOfDecl(fd)]; ok {
		return si, true
	}
	line := fset.Position(fd.Pos()).Line
	for _, si := range byName[fd.Name.Name] {
		if si.Line == line {
			return si, true
		}
	}
	return SymbolInfo{}, false
}

// qualOfDecl renders the qualified name of a function declaration.
func qualOfDecl(fd *ast.FuncDecl) string {
	if fd.Recv != nil && len(fd.Recv.List) > 0 {
		recv := strings.TrimPrefix(recvTypeString(fd.Recv.List[0].Type), "*")
		if recv != "" {
			return recv + "." + fd.Name.Name
		}
	}
	return fd.Name.Name
}

func recvTypeString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return recvTypeString(t.X)
	case *ast.IndexExpr:
		return recvTypeString(t.X)
	case *ast.IndexListExpr:
		return recvTypeString(t.X)
	default:
		return ""
	}
}

// compressBraces strips function bodies in TypeScript/JavaScript sources by
// locating each declared function's body brace and matching it, skipping
// string literals and comments.
func (c *Compressor) compressBraces(src []byte, symbols []SymbolInfo, strip func(SymbolInfo) bool) (CompressResult, error) {
	noop := CompressResult{Content: string(src), Tokens: EstimateTokens(string(src))}
	stripLines := make(map[int]bool)
	for _, si := range symbols {
		if si.Kind != kindFunction {
			continue
		}
		if strip(si) {
			stripLines[si.Line] = true
		}
	}
	if len(stripLines) == 0 {
		return noop, nil
	}

	starts := lineStarts(src)
	var ranges []byteRange
	for line := range stripLines {
		if line < 1 || line > len(starts) {
			continue
		}
		open := findBodyOpen(src, starts[line-1])
		if open < 0 {
			continue
		}
		close := matchBrace(src, open)
		if close < 0 {
			continue
		}
		ranges = append(ranges, byteRange{start: open, end: close + 1})
	}

	out := applyRanges(src, ranges)
	return CompressResult{Content: string(out), Tokens: EstimateTokens(string(out)), Stripped: len(ranges)}, nil
}

// lineStarts returns the byte offset of every line in src.
func lineStarts(src []byte) []int {
	starts := []int{0}
	for i, c := range src {
		if c == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// findBodyOpen returns the byte index of the first '{' at paren-depth zero
// after start, skipping string literals and comments. It is used to locate a
// function body following its declaration.
func findBodyOpen(src []byte, start int) int {
	i := start
	n := len(src)
	var str byte
	comment := 0
	paren := 0
	for i < n {
		c := src[i]
		switch {
		case comment == 1:
			if c == '\n' {
				comment = 0
			}
		case comment == 2:
			if c == '*' && i+1 < n && src[i+1] == '/' {
				comment = 0
				i++
			}
		case str != 0:
			switch c {
			case '\\':
				i++
			case str:
				str = 0
			}
		case c == '/' && i+1 < n && src[i+1] == '/':
			comment = 1
			i++
		case c == '/' && i+1 < n && src[i+1] == '*':
			comment = 2
			i++
		case c == '\'' || c == '"' || c == '`':
			str = c
		case c == '(':
			paren++
		case c == ')':
			if paren > 0 {
				paren--
			}
		case c == '{' && paren == 0:
			return i
		}
		i++
	}
	return -1
}

// matchBrace returns the byte index of the closing brace matching the '{' at
// open, skipping string literals and comments.
func matchBrace(src []byte, open int) int {
	i := open
	n := len(src)
	depth := 0
	var str byte
	comment := 0
	for i < n {
		c := src[i]
		switch {
		case comment == 1:
			if c == '\n' {
				comment = 0
			}
		case comment == 2:
			if c == '*' && i+1 < n && src[i+1] == '/' {
				comment = 0
				i++
			}
		case str != 0:
			switch c {
			case '\\':
				i++
			case str:
				str = 0
			}
		case c == '/' && i+1 < n && src[i+1] == '/':
			comment = 1
			i++
		case c == '/' && i+1 < n && src[i+1] == '*':
			comment = 2
			i++
		case c == '\'' || c == '"' || c == '`':
			str = c
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return i
			}
		}
		i++
	}
	return -1
}

// applyRanges removes the given body ranges, replacing each with the elided
// marker. Ranges must be sorted ascending; overlapping ranges are skipped.
func applyRanges(src []byte, ranges []byteRange) []byte {
	if len(ranges) == 0 {
		return src
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].start < ranges[j].start })
	out := make([]byte, 0, len(src))
	prev := 0
	for _, r := range ranges {
		if r.start < prev || r.start >= len(src) || r.end > len(src) {
			continue
		}
		out = append(out, src[prev:r.start]...)
		out = append(out, bodyElided...)
		prev = r.end
	}
	out = append(out, src[prev:]...)
	return out
}
