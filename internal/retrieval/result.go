package retrieval

type Result struct {
	Content    string  `json:"content"`
	File       string  `json:"file"`
	Line       int     `json:"line"`
	Column     int     `json:"column"`
	Confidence float64 `json:"confidence"`
	Strategy   string  `json:"strategy"`
	SymbolName string  `json:"symbol_name,omitempty"`
	SymbolKind string  `json:"symbol_kind,omitempty"`
}

type ResultSet struct {
	Results    []Result `json:"results"`
	Strategy   string   `json:"strategy"`
	Confidence float64  `json:"confidence"`
	Duration   string   `json:"duration,omitempty"`
	Error      string   `json:"error,omitempty"`
}

func (r *ResultSet) Add(result Result) {
	r.Results = append(r.Results, result)
}

func (r *ResultSet) Merge(other *ResultSet) {
	r.Results = append(r.Results, other.Results...)
	if other.Confidence > r.Confidence {
		r.Confidence = other.Confidence
	}
	if r.Strategy == "" {
		r.Strategy = other.Strategy
	}
}

func (r *ResultSet) Best() *Result {
	if len(r.Results) == 0 {
		return nil
	}
	best := r.Results[0]
	for _, res := range r.Results[1:] {
		if res.Confidence > best.Confidence {
			best = res
		}
	}
	return &best
}

func (r *ResultSet) Empty() bool {
	return len(r.Results) == 0
}

func (r *ResultSet) Count() int {
	return len(r.Results)
}

func (r *ResultSet) ByFile() map[string][]Result {
	m := make(map[string][]Result)
	for _, res := range r.Results {
		m[res.File] = append(m[res.File], res)
	}
	return m
}

func (r *ResultSet) Files() []string {
	seen := make(map[string]bool)
	var files []string
	for _, res := range r.Results {
		if !seen[res.File] {
			seen[res.File] = true
			files = append(files, res.File)
		}
	}
	return files
}
