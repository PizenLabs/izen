package layer2

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

// Sentinel errors returned by the ContextGovernor.
var (
	ErrInvalidPolicy  = errors.New("layer2: invalid context policy")
	ErrInvalidRequest = errors.New("layer2: invalid context request")
	ErrTargetNotFound = errors.New("layer2: target not found")
	ErrBudgetTooSmall = errors.New("layer2: token budget too small for context")
)

// minCompressTokens is the file size below which body stripping is not worth
// the structural loss.
const minCompressTokens = 64

// ContextPolicy governs how much execution context Layer 2 may materialize.
type ContextPolicy struct {
	// MaxTokenBudget is the hard token ceiling for the assembled context.
	MaxTokenBudget int
	// MaxFiles caps the number of files included.
	MaxFiles int
	// MaxSymbols caps the number of ranked symbols returned.
	MaxSymbols int
	// AllowBinary permits files whose source fails UTF-8 validation.
	AllowBinary bool
	// ExpandDependencies includes dependent files and in-repo dependency
	// files in the context.
	ExpandDependencies bool
	// CompressionRatio is the fraction of a file's ranked symbols that keep
	// full bodies; the remainder are stripped by the AST compressor. 1.0
	// disables compression, 0.0 strips everything but the top symbol.
	CompressionRatio float64
}

// DefaultPolicy returns a conservative default context policy.
func DefaultPolicy() ContextPolicy {
	return ContextPolicy{
		MaxTokenBudget:     16000,
		MaxFiles:           16,
		MaxSymbols:         512,
		AllowBinary:        false,
		ExpandDependencies: true,
		CompressionRatio:   0.4,
	}
}

// Valid reports whether the policy satisfies its invariants.
func (p ContextPolicy) Valid() bool {
	if p.MaxTokenBudget <= 0 || p.MaxFiles <= 0 || p.MaxSymbols <= 0 {
		return false
	}
	return p.CompressionRatio >= 0 && p.CompressionRatio <= 1
}

func (p ContextPolicy) validate() error {
	switch {
	case p.MaxTokenBudget <= 0:
		return fmt.Errorf("%w: max token budget must be positive", ErrInvalidPolicy)
	case p.MaxFiles <= 0:
		return fmt.Errorf("%w: max files must be positive", ErrInvalidPolicy)
	case p.MaxSymbols <= 0:
		return fmt.Errorf("%w: max symbols must be positive", ErrInvalidPolicy)
	case p.CompressionRatio < 0 || p.CompressionRatio > 1:
		return fmt.Errorf("%w: compression ratio must be within [0, 1]", ErrInvalidPolicy)
	}
	return nil
}

// ContextRequest describes the focus of the requested execution context. At
// least one of TargetFile or TargetSymbol must be set.
type ContextRequest struct {
	TargetFile   string
	TargetSymbol string
}

// FileContext is one file's contribution to the execution context.
type FileContext struct {
	Path           string
	Language       string
	Package        string
	Symbols        []SymbolInfo
	Imports        []string
	Source         string
	Compressed     bool
	FullyStripped  bool
	StrippedBodies int
	TokenEstimate  int
	Relevance      float64
	Rank           int
}

// ContextStats summarizes the assembled execution context.
type ContextStats struct {
	Files             int
	Symbols           int
	Tokens            int
	BudgetTokens      int
	BudgetMet         bool
	CompressedFiles   int
	StrippedBodies    int
	RankingIterations int
}

// ExecutionContext is the immutable output of Layer 2: the policy-governed,
// ranked and compressed context for a request target. It is constructed with
// deep copies that never alias the SoR, and its collections must be treated
// as read-only.
type ExecutionContext struct {
	Target  *SymbolInfo
	Files   []FileContext
	Symbols []SymbolInfo
	Imports map[string][]string
	Stats   ContextStats
	Policy  ContextPolicy
	BuiltAt time.Time
}

// ContextGovernor is the policy enforcement engine of Layer 2. It combines the
// SoR ranker with the AST compressor and strictly enforces the token budget
// before returning an ExecutionContext. A governor is safe for concurrent
// Build calls once constructed.
type ContextGovernor struct {
	sor        *Sor
	ranker     *Ranker
	compressor *Compressor
}

// NewContextGovernor returns a governor wired to the given SoR.
func NewContextGovernor(sor *Sor) *ContextGovernor {
	return &ContextGovernor{
		sor:        sor,
		ranker:     NewRanker(sor),
		compressor: NewCompressor(),
	}
}

// Build assembles the policy-governed execution context for req. It ranks the
// SoR neighborhood, selects and optionally compresses files, then enforces the
// policy token budget strictly: the returned context never exceeds
// MaxTokenBudget, otherwise an error is returned.
func (g *ContextGovernor) Build(req ContextRequest, policy ContextPolicy) (*ExecutionContext, error) {
	if err := policy.validate(); err != nil {
		return nil, err
	}
	if g.sor == nil {
		return nil, fmt.Errorf("%w: nil sor", ErrInvalidRequest)
	}

	target, err := g.resolveTarget(req)
	if err != nil {
		return nil, err
	}

	rankRes, err := g.ranker.Rank(req, policy)
	if err != nil {
		return nil, err
	}

	// Aggregate per-file relevance from the ranked symbols.
	fileRel := make(map[string]float64)
	for _, sc := range rankRes.Symbols {
		f := sc.Symbol.File
		if f == "" {
			continue
		}
		if cur, ok := fileRel[f]; !ok || sc.Score > cur {
			fileRel[f] = sc.Score
		}
	}
	if req.TargetFile != "" {
		if cur, ok := fileRel[req.TargetFile]; !ok || cur < 1.0 {
			fileRel[req.TargetFile] = 1.0
		}
	}
	if policy.ExpandDependencies {
		seeds := make([]string, 0, len(fileRel))
		for f := range fileRel {
			seeds = append(seeds, f)
		}
		sort.Strings(seeds)
		for _, f := range seeds {
			base := fileRel[f] * 0.5
			for _, n := range g.sor.Neighborhood(f) {
				if n == f {
					continue
				}
				if cur, ok := fileRel[n]; !ok || base > cur {
					fileRel[n] = base
				}
			}
		}
	}

	paths := make([]string, 0, len(fileRel))
	for f := range fileRel {
		paths = append(paths, f)
	}
	sort.Slice(paths, func(i, j int) bool {
		if fileRel[paths[i]] != fileRel[paths[j]] {
			return fileRel[paths[i]] > fileRel[paths[j]]
		}
		return paths[i] < paths[j]
	})
	if len(paths) > policy.MaxFiles {
		paths = paths[:policy.MaxFiles]
	}

	files := make([]FileContext, 0, len(paths))
	for rank, path := range paths {
		fc, ok := g.buildFileContext(path, fileRel[path], rank, rankRes.Symbols, policy)
		if !ok {
			continue
		}
		files = append(files, fc)
	}

	ctxFiles, err := g.enforceBudget(files, policy)
	if err != nil {
		return nil, err
	}

	symbols := filterScoredToFiles(rankRes.Symbols, ctxFiles)
	if len(symbols) > policy.MaxSymbols {
		symbols = symbols[:policy.MaxSymbols]
	}

	imports := make(map[string][]string, len(ctxFiles))
	for _, f := range ctxFiles {
		if len(f.Imports) > 0 {
			imports[f.Path] = append([]string(nil), f.Imports...)
		}
	}

	tokens := totalTokens(ctxFiles)
	return &ExecutionContext{
		Target:  target,
		Files:   ctxFiles,
		Symbols: symbols,
		Imports: imports,
		Stats: ContextStats{
			Files:             len(ctxFiles),
			Symbols:           len(symbols),
			Tokens:            tokens,
			BudgetTokens:      policy.MaxTokenBudget,
			BudgetMet:         tokens <= policy.MaxTokenBudget,
			CompressedFiles:   countCompressed(ctxFiles),
			StrippedBodies:    sumStripped(ctxFiles),
			RankingIterations: rankRes.Iterations,
		},
		Policy:  policy,
		BuiltAt: time.Now(),
	}, nil
}

// resolveTarget resolves the request target to its representative symbol.
func (g *ContextGovernor) resolveTarget(req ContextRequest) (*SymbolInfo, error) {
	switch {
	case req.TargetSymbol != "":
		si, ok := g.sor.Symbol(req.TargetSymbol)
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrTargetNotFound, req.TargetSymbol)
		}
		return &si, nil
	case req.TargetFile != "":
		if !g.sor.HasFile(req.TargetFile) {
			return nil, fmt.Errorf("%w: file %q not indexed", ErrTargetNotFound, req.TargetFile)
		}
		syms := g.sor.SymbolsOfFile(req.TargetFile)
		if len(syms) > 0 {
			si := syms[0]
			return &si, nil
		}
		return &SymbolInfo{File: req.TargetFile}, nil
	default:
		return nil, fmt.Errorf("%w: request requires target file or symbol", ErrInvalidRequest)
	}
}

// buildFileContext loads and optionally compresses one file. ok=false when the
// file must be excluded (unreadable or binary without AllowBinary).
func (g *ContextGovernor) buildFileContext(path string, rel float64, rank int, scored []ScoredSymbol, policy ContextPolicy) (FileContext, bool) {
	src, err := g.sor.Source(path)
	if err != nil {
		return FileContext{}, false
	}
	if isBinary(src) && !policy.AllowBinary {
		return FileContext{}, false
	}

	lang := g.sor.Language(path)
	syms := g.sor.SymbolsOfFile(path)
	orig := EstimateTokens(string(src))
	fc := FileContext{
		Path:          path,
		Language:      lang,
		Package:       g.sor.Package(path),
		Symbols:       syms,
		Imports:       g.sor.ImportsOf(path),
		Source:        string(src),
		TokenEstimate: orig,
		Relevance:     rel,
		Rank:          rank,
	}

	if policy.CompressionRatio >= 1 || orig <= minCompressTokens || lang == "" || len(syms) == 0 {
		return fc, true
	}

	keepCount := int(math.Ceil(float64(len(syms)) * policy.CompressionRatio))
	if keepCount < 1 {
		keepCount = 1
	}
	keep := keepSetForFile(scored, path, keepCount)
	res, cerr := g.compressor.Compress(lang, src, syms, func(si SymbolInfo) bool {
		return si.QualName != "" && !keep[si.QualName]
	})
	if cerr == nil && res.Tokens < orig {
		fc.Source = res.Content
		fc.TokenEstimate = res.Tokens
		fc.Compressed = true
		fc.StrippedBodies = res.Stripped
	}
	return fc, true
}

// keepSetForFile returns the qualified names of the top keepCount ranked
// symbols declared in path; these retain full bodies during compression.
func keepSetForFile(scored []ScoredSymbol, path string, keepCount int) map[string]bool {
	keep := make(map[string]bool)
	kept := 0
	for _, sc := range scored {
		if kept >= keepCount {
			break
		}
		if sc.Symbol.File != path || sc.Symbol.QualName == "" {
			continue
		}
		keep[sc.Symbol.QualName] = true
		kept++
	}
	return keep
}

// enforceBudget strictly fits the selected files under the policy budget.
// Files are dropped from the lowest-relevance end first; a remaining
// oversized file is fully body-stripped as a last resort. If the budget still
// cannot be met, ErrBudgetTooSmall is returned.
func (g *ContextGovernor) enforceBudget(files []FileContext, policy ContextPolicy) ([]FileContext, error) {
	files = append([]FileContext(nil), files...)
	for {
		tok := totalTokens(files)
		if tok <= policy.MaxTokenBudget {
			return files, nil
		}
		if len(files) > 1 {
			files = files[:len(files)-1]
			continue
		}
		if files[0].Compressed && files[0].FullyStripped {
			return nil, ErrBudgetTooSmall
		}
		fc := g.compressFull(files[0])
		if fc.TokenEstimate >= files[0].TokenEstimate {
			return nil, ErrBudgetTooSmall
		}
		files[0] = fc
	}
}

// compressFull strips every function/method body of a file, keeping only
// signatures, types, interfaces and doc comments.
func (g *ContextGovernor) compressFull(fc FileContext) FileContext {
	if fc.FullyStripped {
		return fc
	}
	src, err := g.sor.Source(fc.Path)
	if err != nil {
		return fc
	}
	syms := g.sor.SymbolsOfFile(fc.Path)
	res, cerr := g.compressor.Compress(fc.Language, src, syms, func(SymbolInfo) bool { return true })
	if cerr != nil || res.Tokens >= fc.TokenEstimate {
		return fc
	}
	fc.Source = res.Content
	fc.TokenEstimate = res.Tokens
	fc.Compressed = true
	fc.FullyStripped = true
	fc.StrippedBodies = res.Stripped
	return fc
}

// filterScoredToFiles returns the scored symbols declared in the selected
// files, preserving rank order.
func filterScoredToFiles(scored []ScoredSymbol, files []FileContext) []SymbolInfo {
	in := make(map[string]bool, len(files))
	for _, f := range files {
		in[f.Path] = true
	}
	out := make([]SymbolInfo, 0, len(scored))
	for _, sc := range scored {
		if in[sc.Symbol.File] {
			out = append(out, sc.Symbol)
		}
	}
	return out
}

func totalTokens(files []FileContext) int {
	total := 0
	for _, f := range files {
		total += f.TokenEstimate
	}
	return total
}

func countCompressed(files []FileContext) int {
	n := 0
	for _, f := range files {
		if f.Compressed {
			n++
		}
	}
	return n
}

func sumStripped(files []FileContext) int {
	n := 0
	for _, f := range files {
		n += f.StrippedBodies
	}
	return n
}

// isBinary reports whether src contains NUL bytes, i.e. it is not a
// well-formed UTF-8 source file.
func isBinary(src []byte) bool {
	for _, b := range src {
		if b == 0 {
			return true
		}
	}
	return false
}
