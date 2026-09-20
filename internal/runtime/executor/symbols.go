package executor

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"text/scanner"

	"github.com/PizenLabs/izen/internal/retrieval/symbol"
	"github.com/PizenLabs/izen/internal/retrieval/symbol/extractors"
)

// RedundantSymbolError requests a bounded replan, not a terminal failure.
type RedundantSymbolError struct {
	Target         string
	NewSymbol      string
	ExistingSymbol string
	ExistingFile   string
	Similarity     float64
}

func (e *RedundantSymbolError) Error() string {
	return fmt.Sprintf("REDUNDANT_SYMBOL: new private helper %s duplicates %s in %s (%.0f%% similarity). Reuse %s instead of declaring a new helper; replan a bounded patch against the unchanged baseline.", e.NewSymbol, e.ExistingSymbol, e.ExistingFile, e.Similarity*100, e.ExistingSymbol)
}

type indexedSymbol struct {
	symbol.SymbolNode
	language  symbol.LanguageID
	pkg       string
	signature []string
	body      []string
}

// SymbolBaseline is an immutable, runtime-captured scope table. Sources must
// come from authorized workspace snapshots before invoking the worker, never
// from model output or model-provided symbol names.
type SymbolBaseline struct{ symbols []indexedSymbol }

func NewSymbolBaseline(sources map[string]string) (*SymbolBaseline, error) {
	b := &SymbolBaseline{}
	paths := make([]string, 0, len(sources))
	for path := range sources {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if sources[path] == "" {
			continue
		}
		syms, err := indexSymbols(path, sources[path])
		if err != nil {
			return nil, fmt.Errorf("symbol baseline %s: %w", path, err)
		}
		b.symbols = append(b.symbols, syms...)
	}
	return b, nil
}

// Names returns the trusted baseline symbol names in deterministic order.
func (b *SymbolBaseline) Names() []string {
	if b == nil {
		return nil
	}
	names := make([]string, 0, len(b.symbols))
	for _, s := range b.symbols {
		names = append(names, s.Name)
	}
	return names
}

func (b *SymbolBaseline) Context() string {
	if b == nil || len(b.symbols) == 0 {
		return ""
	}
	var out strings.Builder
	out.WriteString("AVAILABLE SYMBOLS (trusted target scope; reuse existing utilities before adding helpers):\n")
	for _, s := range b.symbols {
		visibility := "private"
		if s.Exported {
			visibility = "public"
		}
		fmt.Fprintf(&out, "%s:%d %s %s %s %s\n", s.FilePath, s.StartLine, visibility, s.Kind, s.Name, s.Signature)
	}
	return out.String()
}

func (b *SymbolBaseline) Check(target, content string) *RedundantSymbolError {
	if b == nil {
		return nil
	}
	proposed, _ := indexSymbols(target, content)
	for _, candidate := range proposed {
		if candidate.Exported || candidate.Kind != symbol.SymbolFunction || len(candidate.body) == 0 {
			continue
		}
		existing := false
		for _, prior := range b.symbols {
			if prior.FilePath == target && prior.Name == candidate.Name && prior.Parent == candidate.Parent && prior.Kind == candidate.Kind {
				existing = true
				break
			}
		}
		if existing {
			continue
		}
		for _, prior := range b.symbols {
			if prior.Kind != symbol.SymbolFunction || len(prior.body) == 0 || prior.language != candidate.language {
				continue
			}
			// A private declaration is only reusable in its lexical module/package.
			if prior.FilePath != target && (candidate.language != symbol.LangGo || prior.pkg != candidate.pkg || filepath.Dir(prior.FilePath) != filepath.Dir(target)) {
				continue
			}
			score := 0.3*tokenSimilarity(candidate.signature, prior.signature) + 0.7*tokenSimilarity(candidate.body, prior.body)
			if score > 0.70 {
				return &RedundantSymbolError{Target: target, NewSymbol: candidate.Name, ExistingSymbol: prior.Name, ExistingFile: prior.FilePath, Similarity: score}
			}
		}
	}
	return nil
}

func languageExtractor(path string) symbol.LanguageExtractor {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return extractors.NewGoExtractor()
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
		return extractors.NewTSExtractor()
	case ".py":
		return extractors.NewPythonExtractor()
	case ".rs":
		return extractors.NewRustExtractor()
	case ".java":
		return extractors.NewJavaExtractor()
	case ".c", ".h", ".cc", ".cpp", ".hpp", ".cxx":
		return extractors.NewCCExtractor()
	default:
		return nil
	}
}

func indexSymbols(path, source string) ([]indexedSymbol, error) {
	ext := languageExtractor(path)
	if ext == nil {
		return nil, nil
	}
	info, err := ext.ExtractSymbols(path, []byte(source))
	if err != nil {
		return nil, err
	}
	result := make([]indexedSymbol, 0, len(info.Symbols))
	for _, node := range info.Symbols {
		result = append(result, indexedSymbol{SymbolNode: node, language: info.Language, pkg: info.Package})
	}
	if info.Language == symbol.LangGo {
		file, err := parser.ParseFile(token.NewFileSet(), path, source, 0)
		if err != nil {
			return nil, err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Body == nil {
				continue
			}
			// Canonicalize local bindings through AST object identity. Global
			// calls, member names, types, operators and literals retain meaning.
			//nolint:staticcheck // SA1019: ast.Object identity is used deliberately for lexical binding grouping
			names := map[*ast.Object]string{}
			ast.Inspect(fn, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && id.Obj != nil && id.Obj.Kind == ast.Var {
					if _, found := names[id.Obj]; !found {
						names[id.Obj] = fmt.Sprintf("local%d", len(names))
					}
				}
				return true
			})
			ast.Inspect(fn, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok {
					if name, found := names[id.Obj]; found {
						id.Name = name
					}
				}
				return true
			})
			var signature, body bytes.Buffer
			if err := format.Node(&signature, token.NewFileSet(), fn.Type); err != nil {
				return nil, err
			}
			if err := format.Node(&body, token.NewFileSet(), fn.Body); err != nil {
				return nil, err
			}
			for i := range result {
				if result[i].Name == fn.Name.Name && result[i].Kind == symbol.SymbolFunction {
					result[i].signature = sourceTokens(signature.String())
					result[i].body = sourceTokens(body.String())
					break
				}
			}
		}
		return result, nil
	}
	// The existing non-Go extractors are structural scanners. Their symbol
	// identities/visibility own discovery; tokenize a balanced declaration body
	// rather than inventing model-provided signatures or matching whole files.
	lines := strings.Split(source, "\n")
	for i := range result {
		s := &result[i]
		if s.Kind != symbol.SymbolFunction || s.StartLine <= 0 || s.StartLine > len(lines) {
			continue
		}
		start := s.StartLine - 1
		if info.Language == symbol.LangPython {
			end := start + 1
			indent := len(lines[start]) - len(strings.TrimLeft(lines[start], " \t"))
			for end < len(lines) {
				line := lines[end]
				if strings.TrimSpace(line) != "" && len(line)-len(strings.TrimLeft(line, " \t")) <= indent {
					break
				}
				end++
			}
			s.signature = sourceTokens(strings.Replace(lines[start], s.Name, "helper", 1))
			s.body = sourceTokens(strings.Join(lines[start+1:end], "\n"))
			continue
		}
		tokens := sourceTokens(strings.Join(lines[start:], "\n"))
		open := -1
		for j, tok := range tokens {
			if tok == "{" {
				open = j
				break
			}
			if tok == s.Name {
				tokens[j] = "helper"
			}
		}
		if open < 0 {
			continue
		}
		depth, end := 0, open
		for ; end < len(tokens); end++ {
			if tokens[end] == "{" {
				depth++
			}
			if tokens[end] == "}" {
				depth--
				if depth == 0 {
					end++
					break
				}
			}
		}
		if depth != 0 {
			continue
		}
		s.signature, s.body = tokens[:open], tokens[open:end]
	}
	return result, nil
}

func sourceTokens(source string) []string {
	var scan scanner.Scanner
	scan.Init(strings.NewReader(source))
	scan.Error = func(*scanner.Scanner, string) {}
	var tokens []string
	for tok := scan.Scan(); tok != scanner.EOF; tok = scan.Scan() {
		if text := scan.TokenText(); !syntaxOnly(text) {
			tokens = append(tokens, text)
		}
	}
	return tokens
}

// syntaxOnly reports pure punctuation: it carries structure, not
// similarity-relevant semantics, and would otherwise let identical skeletal
// shapes (func ( ) { }) push unrelated helpers over the redundancy threshold.
func syntaxOnly(tokenText string) bool {
	switch tokenText {
	case "(", ")", "{", "}", "[", "]", ";", ",", ".", "::":
		return true
	}
	return false
}

// Ordered token similarity uses a linear-space LCS; unlike a set overlap it
// preserves multiplicity and statement order. Bound work on large helpers.
func tokenSimilarity(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	if len(a) > 2048 || len(b) > 2048 {
		if strings.Join(a, "\x00") == strings.Join(b, "\x00") {
			return 1
		}
		return 0
	}
	row := make([]int, len(b)+1)
	for _, x := range a {
		prev := 0
		for j, y := range b {
			old := row[j+1]
			if x == y {
				row[j+1] = prev + 1
			} else {
				row[j+1] = max(row[j+1], row[j])
			}
			prev = old
		}
	}
	return 2 * float64(row[len(b)]) / float64(len(a)+len(b))
}
