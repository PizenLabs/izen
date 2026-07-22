package retrieval

// LxQuery is the standardized query structure sent to the LX Rust daemon.
// Every LX invocation in the codebase MUST use this struct.
type LxQuery struct {
	Text   string // free-text search (BM25 semantic)
	Symbol string // exact symbol name resolution
	File   string // file path for context/related queries
	Line   int    // line number for context/related queries
	Lines  int    // number of context lines to read
}

// LxResult is the standardized response from the LX Rust daemon.
// It carries the BM25 relevance score, exact file coordinates,
// and content snippets. All fields are populated by the daemon;
// no field is ever fabricated by the Go runtime.
//
// BM25 Score Contract:
//   - Score is the raw BM25 relevance score returned by the daemon (0.0–1.0)
//   - Confidence is a derived classification label (exact/high/medium/low/fallback)
//   - Consumers MUST use Score for ranking decisions
//   - Confidence is used for tier-based routing decisions only
type LxResult struct {
	File       string  // file path relative to project root
	Line       int     // start line (1-indexed)
	Column     int     // start column (1-indexed, 0 if unavailable)
	EndLine    int     // end line for multi-line spans
	EndColumn  int     // end column
	Content    string  // code snippet or line content
	Score      float64 // raw BM25 relevance score from LX daemon (0.0–1.0)
	Confidence float64 // derived confidence label (0.0–1.0)
	Strategy   string  // retrieval strategy used (e.g., "lynx.semantic", "graph.exact")
	SymbolName string  // resolved symbol name, if applicable
	SymbolKind string  // kind of symbol (function, type, variable, etc.)
}

// LxResultSet is the collection returned by an LX query.
type LxResultSet struct {
	Results    []LxResult
	Strategy   string
	Confidence float64
	Duration   string
	Error      string
}

// Contract invariants enforced by the codebase:
//
// 1. DIRECT WRAPPER RULE: LX tool execution is a DIRECT, TRUTHFUL wrapper
//    around the Rust daemon's JSON-RPC response. The Go runtime MUST NOT:
//    - Fabricate SearchResult objects without a daemon round-trip
//    - Replace BM25 scores with static confidence values
//    - Log success when the daemon returned an error
//
// 2. LOW-CONFIDENCE LOGGING: If LX fails or returns Score < 0.3, the
//    orchestrator MUST log "[lx] low relevance score (0.x)" instead of
//    masking with fabricated rationales like "missing Go module dependency".
//
// 3. NO GUESSED RATIONALES: Rationale strings in the orchestrator log
//    MUST describe actual tool outcomes, not pre-decision classifications.

func (r LxResult) IsLowConfidence() bool {
	return r.Score < 0.3
}

func (r LxResult) IsHighConfidence() bool {
	return r.Score >= 0.7
}

// ToLegacyResult converts a LxResult to the legacy Result type.
// This is a compatibility bridge for existing consumers.
func (r LxResult) ToLegacyResult() Result {
	confidence := r.Score
	if confidence <= 0 {
		confidence = r.Confidence
	}
	return Result{
		File:       r.File,
		Line:       r.Line,
		Column:     r.Column,
		Content:    r.Content,
		Confidence: confidence,
		Strategy:   r.Strategy,
		SymbolName: r.SymbolName,
		SymbolKind: r.SymbolKind,
	}
}
