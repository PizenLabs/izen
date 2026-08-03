package planner

import "context"

// SymbolRef is a minimal structural symbol reference resolved from the graph.
// It is the planner's own projection so the graph source can be backed by any
// structural engine without leaking concrete graph types into the planner.
type SymbolRef struct {
	Name      string
	Kind      string
	QualName  string
	Package   string
	File      string
	Line      int
	Exported  bool
	Signature string
}

// SearchHit is a ranked file-level match returned by the file source.
type SearchHit struct {
	File    string
	Line    int
	Column  int
	Content string
	Score   float64
	Symbol  string
	Kind    string
}

// GraphSource provides structural context. The default implementation is
// backed by the Lea engine (TraceCallChain, GetArchitectureSummary,
// FindRoutes); tests inject fakes.
type GraphSource interface {
	// ResolveSymbol resolves a symbol name to its definitions.
	ResolveSymbol(ctx context.Context, symbol string) ([]SymbolRef, error)
	// CallChain reconstructs the inbound call tree of a symbol up to depth.
	CallChain(ctx context.Context, symbol string, depth int) (string, error)
	// ArchitectureSummary returns a compact structural overview of the repo.
	ArchitectureSummary(ctx context.Context) (string, error)
	// Routes returns the HTTP route → handler map, or "" when unavailable.
	Routes(ctx context.Context) (string, error)
}

// LogSource provides the most recent Phase 1 Tee tool logs. The default
// implementation reads from the workspace `.logs/` directory.
type LogSource interface {
	// LatestLogs returns up to limit most recent tee log bodies, newest first.
	LatestLogs(ctx context.Context, limit int) ([]string, error)
}

// FileSource provides focused file snippets and ranked search hits.
type FileSource interface {
	// Search returns ranked matches for the query.
	Search(ctx context.Context, query string) ([]SearchHit, error)
	// FocusedContext returns the targeted region of a file.
	FocusedContext(ctx context.Context, file string, startLine, endLine int) (string, error)
}

// TokenEstimator converts text to a token weight using the ~4 chars/token
// heuristic (0.25 tokens per char), conservative for source code.
type TokenEstimator struct {
	TokensPerChar float64
}

// NewTokenEstimator returns an estimator using the default 0.25 tokens/char.
func NewTokenEstimator() *TokenEstimator {
	return &TokenEstimator{TokensPerChar: 0.25}
}

// Estimate returns the estimated token count of text.
func (t *TokenEstimator) Estimate(text string) int {
	if t == nil || text == "" {
		return 0
	}
	return int(float64(len(text)) * t.TokensPerChar)
}

// ContextReady reports whether the planner has at least one usable source.
func (p *Planner) ContextReady() bool {
	if p == nil {
		return false
	}
	return p.graph != nil || p.logs != nil || p.files != nil
}
