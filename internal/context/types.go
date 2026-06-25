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
	Path    string     `json:"path"`
	Package string     `json:"package,omitempty"`
	Symbols []SymbolRef `json:"symbols,omitempty"`
	Imports []string   `json:"imports,omitempty"`
	Lines   int        `json:"lines"`
	Size    int64      `json:"size"`
}

type Context struct {
	Objective string      `json:"objective"`
	Mode      string      `json:"mode"`
	Files     []FileSlice `json:"files"`
	Diff      string      `json:"diff,omitempty"`
	Status    []string    `json:"status,omitempty"`
	Errors    []string    `json:"errors,omitempty"`
	Query     string      `json:"query,omitempty"`
}

type Stats struct {
	FileCount   int    `json:"file_count"`
	SymbolCount int    `json:"symbol_count"`
	DiffLines   int    `json:"diff_lines"`
	PromptChars int    `json:"prompt_chars"`
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
	return s
}