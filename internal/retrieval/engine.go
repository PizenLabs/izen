package retrieval

import "context"

// CodeCoord represents a resolved symbol coordinate in the codebase.
type CodeCoord struct {
	File       string
	StartLine  int
	StartCol   int
	EndLine    int
	EndCol     int
	SymbolName string
	SymbolKind string
	Content    string
	Score      float64
}

// CodeChunk represents a chunk of context for a search result.
type CodeChunk struct {
	File       string
	StartLine  int
	EndLine    int
	Content    string
	SymbolName string
	Score      float64
}

// SearchEngine is the unified interface for code discovery.
// Implementations must be safe for concurrent use.
type SearchEngine interface {
	ResolveSymbol(ctx context.Context, symbol string) ([]CodeCoord, error)
	SearchContext(ctx context.Context, query string) ([]CodeChunk, error)
	GetFocusedContext(ctx context.Context, file string, startLine, endLine int) (string, error)
}
