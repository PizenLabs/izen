package retrieval

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/izen/internal/retrieval/symbol"
)

// PolyglotEngine implements SearchEngine using the polyglot symbol extraction
// registry. For Go projects it delegates to NativeGoEngine, and for other
// languages it uses the ExtractorRegistry to resolve symbols and search context.
type PolyglotEngine struct {
	root         string
	registry     *symbol.ExtractorRegistry
	nativeEngine *NativeGoEngine
}

// NewPolyglotEngine creates a polyglot search engine rooted at the project directory.
func NewPolyglotEngine(root string, registry *symbol.ExtractorRegistry) *PolyglotEngine {
	return &PolyglotEngine{
		root:         root,
		registry:     registry,
		nativeEngine: NewNativeGoEngine(root),
	}
}

// ResolveSymbol resolves a symbol name to its definition coordinates.
// It first tries the native Go engine, then falls back to polyglot extraction.
func (e *PolyglotEngine) ResolveSymbol(ctx context.Context, symbolName string) ([]CodeCoord, error) {
	if symbolName == "" {
		return nil, nil
	}

	// Try native Go engine first for Go symbols.
	coords, err := e.nativeEngine.ResolveSymbol(ctx, symbolName)
	if err == nil && len(coords) > 0 {
		return coords, nil
	}

	// Use polyglot extraction for non-Go symbols.
	return e.resolvePolyglot(symbolName)
}

// SearchContext performs search across all supported languages using
// the polyglot extraction registry.
func (e *PolyglotEngine) SearchContext(ctx context.Context, query string) ([]CodeChunk, error) {
	// Try native Go engine first for Go context search.
	chunks, err := e.nativeEngine.SearchContext(ctx, query)
	if err == nil && len(chunks) > 0 {
		return chunks, nil
	}

	// Fall back to polyglot regex search across all extracted source files.
	return e.searchPolyglot(query)
}

// GetFocusedContext delegates to the native Go engine for file reading.
func (e *PolyglotEngine) GetFocusedContext(ctx context.Context, file string, startLine, endLine int) (string, error) {
	return e.nativeEngine.GetFocusedContext(ctx, file, startLine, endLine)
}

// ExtractAllSymbols runs polyglot extraction across all relevant files
// and returns the combined FileASTInfo results.
func (e *PolyglotEngine) ExtractAllSymbols() ([]symbol.FileASTInfo, error) {
	lang, extractor, ok := e.registry.DetectLanguage(e.root)
	if !ok {
		return nil, nil
	}

	var results []symbol.FileASTInfo
	_ = filepath.Walk(e.root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil //nolint:nilerr
		}
		if info.IsDir() {
			return nil
		}
		if strings.Contains(path, "vendor/") || strings.Contains(path, ".izen/") {
			return nil
		}
		if info.Size() > 1<<20 {
			return nil
		}
		if !isRelevantFile(path, lang) {
			return nil
		}

		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil //nolint:nilerr
		}

		fileInfo, extractErr := extractor.ExtractSymbols(path, src)
		if extractErr != nil || fileInfo == nil {
			return nil //nolint:nilerr
		}
		results = append(results, *fileInfo)
		return nil
	})

	return results, nil
}

func (e *PolyglotEngine) resolvePolyglot(symbolName string) ([]CodeCoord, error) {
	results, err := e.ExtractAllSymbols()
	if err != nil {
		return nil, err
	}

	var coords []CodeCoord
	for _, fn := range results {
		for _, sym := range fn.Symbols {
			if sym.Name == symbolName {
				coords = append(coords, CodeCoord{
					File:       fn.FilePath,
					StartLine:  sym.StartLine,
					StartCol:   1,
					EndLine:    sym.EndLine,
					EndCol:     sym.EndLine,
					SymbolName: sym.Name,
					SymbolKind: string(sym.Kind),
					Content:    sym.Signature,
					Score:      1.0,
				})
			}
		}
	}

	return coords, nil
}

func (e *PolyglotEngine) searchPolyglot(query string) ([]CodeChunk, error) {
	results, err := e.ExtractAllSymbols()
	if err != nil {
		return nil, err
	}

	pattern := strings.ToLower(query)
	var chunks []CodeChunk

	for _, fn := range results {
		for _, sym := range fn.Symbols {
			if strings.Contains(strings.ToLower(sym.Name), pattern) ||
				strings.Contains(strings.ToLower(sym.Signature), pattern) {
				chunks = append(chunks, CodeChunk{
					File:       fn.FilePath,
					StartLine:  sym.StartLine,
					EndLine:    sym.EndLine,
					Content:    sym.Signature,
					SymbolName: sym.Name,
					Score:      0.8,
				})
			}
		}
	}

	return chunks, nil
}

func isRelevantFile(path string, lang symbol.LanguageID) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch lang {
	case symbol.LangGo:
		return ext == ".go"
	case symbol.LangJava:
		return ext == ".java"
	case symbol.LangTypeScript:
		return ext == ".ts" || ext == ".tsx"
	case symbol.LangJavaScript:
		return ext == ".js" || ext == ".jsx"
	case symbol.LangPython:
		return ext == ".py"
	case symbol.LangRust:
		return ext == ".rs"
	case symbol.LangCC, symbol.LangC:
		return ext == ".c" || ext == ".h" || ext == ".cpp" || ext == ".cc" || ext == ".cxx" || ext == ".hpp" || ext == ".hh"
	default:
		return false
	}
}
