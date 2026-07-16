package context

type SymbolRef struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	File      string `json:"file"`
	Line      int    `json:"line"`
	Signature string `json:"signature,omitempty"`
	Exported  bool   `json:"exported"`
}

type FileSlice struct {
	Path    string      `json:"path"`
	Package string      `json:"package,omitempty"`
	Symbols []SymbolRef `json:"symbols,omitempty"`
	Imports []string    `json:"imports,omitempty"`
	Lines   int         `json:"lines"`
	Size    int64       `json:"size"`
}

// CodebaseTrace captures the AST/Graph resolution metadata produced during
// context assembly so the TUI can render the AI's "thought route".
type CodebaseTrace struct {
	MatchedFiles     []string `json:"matched_files"`
	ResolvedSymbols  []string `json:"resolved_symbols"`
	TotalTokensSaved int      `json:"total_tokens_saved"`
	CompressionRatio float64  `json:"compression_ratio"`
}

type Context struct {
	Objective          string         `json:"objective"`
	Mode               string         `json:"mode"`
	ContextID          string         `json:"context_id,omitempty"`
	RunNumber          int            `json:"run_number"`
	CheckpointID       string         `json:"checkpoint_id,omitempty"`
	CompilerPayload    string         `json:"compiler_payload,omitempty"`
	SymbolAST          string         `json:"symbol_ast,omitempty"`
	DiagnosticsSummary string         `json:"diagnostics_summary,omitempty"`
	Files              []FileSlice    `json:"files"`
	Diff               string         `json:"diff,omitempty"`
	Status             []string       `json:"status,omitempty"`
	Errors             []string       `json:"errors,omitempty"`
	Query              string         `json:"query,omitempty"`
	Trace              *CodebaseTrace `json:"trace,omitempty"`
	// TaskStatusSnapshot is a windowed view of task states injected by the
	// sliding-window renderer. It is set externally by the build loop and
	// carries only the active (non-terminal) task metadata.
	TaskStatusSnapshot map[int]string `json:"task_status_snapshot,omitempty"`
}

type Stats struct {
	FileCount       int `json:"file_count"`
	SymbolCount     int `json:"symbol_count"`
	DiffLines       int `json:"diff_lines"`
	PromptChars     int `json:"prompt_chars"`
	TokensSaved     int `json:"tokens_saved"`
	MatchedFiles    int `json:"matched_files"`
	ResolvedSymbols int `json:"resolved_symbols"`
}

func (c *Context) Stats() Stats {
	s := Stats{
		FileCount:   len(c.Files),
		PromptChars: 0,
	}
	for _, f := range c.Files {
		s.SymbolCount += len(f.Symbols)
	}
	for _, line := range c.Diff {
		if line == '\n' {
			s.DiffLines++
		}
	}
	if c.Trace != nil {
		s.TokensSaved = c.Trace.TotalTokensSaved
		s.MatchedFiles = len(c.Trace.MatchedFiles)
		s.ResolvedSymbols = len(c.Trace.ResolvedSymbols)
	}
	return s
}

// BuildTrace populates the CodebaseTrace from the files already collected in
// the context. It extracts matched file paths and resolved symbol names, then
// estimates the token savings from compression (symbols only vs full source).
func (c *Context) BuildTrace() {
	if len(c.Files) == 0 {
		return
	}
	trace := &CodebaseTrace{}
	seen := make(map[string]bool)

	var totalSymbolTokens int
	var estimatedFullTokens int

	for _, f := range c.Files {
		if !seen[f.Path] {
			seen[f.Path] = true
			trace.MatchedFiles = append(trace.MatchedFiles, f.Path)
		}
		for _, sym := range f.Symbols {
			key := sym.Name + ":" + sym.File
			if !seen[key] {
				seen[key] = true
				trace.ResolvedSymbols = append(trace.ResolvedSymbols, sym.Name)
			}
		}
		// Each symbol ref ≈ 4 tokens (name + kind + line). Full source ≈ lines/4 tokens.
		totalSymbolTokens += len(f.Symbols) * 4
		estimatedFullTokens += f.Lines / 4
	}

	if estimatedFullTokens > 0 {
		trace.TotalTokensSaved = estimatedFullTokens - totalSymbolTokens
		if trace.TotalTokensSaved < 0 {
			trace.TotalTokensSaved = 0
		}
		trace.CompressionRatio = float64(totalSymbolTokens) / float64(estimatedFullTokens)
	}

	c.Trace = trace
}
