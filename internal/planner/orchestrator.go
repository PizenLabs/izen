package planner

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Planner is the Context Planner: it sits between User Input Intent
// Recognition and the Retrieval/Prompt Pipeline. For a given input it
// classifies the intent, computes a dynamic token budget, queries only the
// context sources the intent prioritizes, ranks the retrieved chunks by
// relevance, and enforces the budget before the context reaches prompt
// assembly.
type Planner struct {
	graph     GraphSource
	logs      LogSource
	files     FileSource
	tokens    *TokenEstimator
	maxTokens int
	logFn     func(string, ...interface{})
}

// Option configures a Planner.
type Option func(*Planner)

// WithGraphSource wires the structural graph engine (default: none).
func WithGraphSource(g GraphSource) Option {
	return func(p *Planner) { p.graph = g }
}

// WithLogSource wires the Phase 1 Tee log source (default: none).
func WithLogSource(l LogSource) Option {
	return func(p *Planner) { p.logs = l }
}

// WithFileSource wires the focused-file/search source (default: none).
func WithFileSource(f FileSource) Option {
	return func(p *Planner) { p.files = f }
}

// WithTokenEstimator overrides the token estimator (default: 0.25/char).
func WithTokenEstimator(t *TokenEstimator) Option {
	return func(p *Planner) {
		if t != nil {
			p.tokens = t
		}
	}
}

// WithMaxTokens caps the total assembled context budget. Values <= 0 fall
// back to DefaultMaxContextTokens.
func WithMaxTokens(n int) Option {
	return func(p *Planner) {
		if n > 0 {
			p.maxTokens = n
		}
	}
}

// WithLogFn wires an activity sink (default: no-op).
func WithLogFn(fn func(string, ...interface{})) Option {
	return func(p *Planner) {
		if fn != nil {
			p.logFn = fn
		}
	}
}

// New constructs a Planner. The context sources are optional; an intent whose
// sources are all missing degrades gracefully to whatever is wired.
func New(opts ...Option) *Planner {
	p := &Planner{
		tokens:    NewTokenEstimator(),
		maxTokens: DefaultMaxContextTokens,
		logFn:     func(string, ...interface{}) {},
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// MaxTokens returns the configured total budget cap.
func (p *Planner) MaxTokens() int {
	if p == nil || p.maxTokens <= 0 {
		return DefaultMaxContextTokens
	}
	return p.maxTokens
}

// PlanAssembled classifies the input, plans the context, and returns the
// assembled, budget-fitted context string ready for prompt injection. It is
// the convenience entry point for consumers (e.g. the investigate engine)
// that only need the rendered context block.
func (p *Planner) PlanAssembled(ctx context.Context, input string) (string, error) {
	plan, err := p.Plan(ctx, input)
	if err != nil {
		return "", err
	}
	if plan == nil || len(plan.Chunks) == 0 {
		return "", nil
	}
	return plan.Assemble(), nil
}

// Plan classifies the input, computes the budget, queries the prioritized
// context engines, ranks the chunks, and enforces the token budget. The
// returned ContextPlan is ready for prompt assembly via Assemble.
func (p *Planner) Plan(ctx context.Context, input string) (*ContextPlan, error) {
	if p == nil {
		return nil, fmt.Errorf("planner: nil receiver")
	}
	intent := ClassifyIntent(input)
	budget := Allocate(intent, p.MaxTokens())
	alloc := allocationFor(intent)

	p.log("[planner] intent=%s budget=%d tokens", intent, budget.Total)

	chunks, err := p.gather(ctx, intent, alloc, budget, input)
	if err != nil {
		return nil, err
	}

	// Rank: ascending priority (source truncation order), then descending
	// relevance score within the same priority.
	sort.SliceStable(chunks, func(i, j int) bool {
		if chunks[i].Priority != chunks[j].Priority {
			return chunks[i].Priority < chunks[j].Priority
		}
		return chunks[i].Score > chunks[j].Score
	})

	fitted, total, dropped, truncated := p.fitBudget(chunks, budget)

	return &ContextPlan{
		Intent:     intent,
		Budget:     budget,
		Chunks:     fitted,
		TokenTotal: total,
		Truncated:  truncated,
		Dropped:    dropped,
	}, nil
}

// gather queries the context engines enabled for the intent, tagging every
// chunk with its source priority so the budget enforcer can drop the least
// valuable context first.
func (p *Planner) gather(ctx context.Context, intent Intent, alloc Allocation, budget Budget, input string) ([]Chunk, error) {
	var chunks []Chunk
	priority := make(map[SourceType]int, len(alloc.Priority))
	for i, src := range alloc.Priority {
		priority[src] = i
	}

	switch intent {
	case IntentBugFix:
		chunks = append(chunks, p.gatherLogs(ctx, priority, budget.Source(SourceLog))...)
		chunks = append(chunks, p.gatherCallChains(ctx, priority, input)...)
		chunks = append(chunks, p.gatherFileHits(ctx, priority, input)...)

	case IntentArchitecture:
		chunks = append(chunks, p.gatherArchitecture(ctx, priority)...)
		// Explicitly NO file reads: architecture questions consume the
		// structural overview and the route map only.

	case IntentRefactor:
		chunks = append(chunks, p.gatherSymbols(ctx, priority, input)...)
		chunks = append(chunks, p.gatherCallChains(ctx, priority, input)...)
		chunks = append(chunks, p.gatherFileHits(ctx, priority, input)...)

	default: // EXPLANATION, GENERAL
		chunks = append(chunks, p.gatherSymbols(ctx, priority, input)...)
		chunks = append(chunks, p.gatherFileHits(ctx, priority, input)...)
	}
	return chunks, nil
}

// fitBudget enforces the token budget: each source is capped to its allocated
// share and the whole assembly is capped to the global total. Chunks are
// processed in rank order (ascending priority, then relevance), so low-
// priority items — raw file reads — are the first to be dropped when the
// budget is exceeded, preserving graph symbols and logs.
func (p *Planner) fitBudget(chunks []Chunk, budget Budget) ([]Chunk, int, int, bool) {
	kept := make([]Chunk, 0, len(chunks))
	usedBySource := make(map[SourceType]int)
	total := 0
	dropped := 0

	for _, c := range chunks {
		if c.Tokens == 0 {
			c.Tokens = p.tokens.Estimate(c.Content)
		}
		// Per-source cap: a single source may never exceed its allocated share.
		if srcCap := budget.Source(c.Source); srcCap > 0 && usedBySource[c.Source]+c.Tokens > srcCap {
			dropped++
			continue
		}
		// Global cap: the assembled context may never exceed the total budget.
		if total+c.Tokens > budget.Total {
			dropped++
			continue
		}
		usedBySource[c.Source] += c.Tokens
		total += c.Tokens
		kept = append(kept, c)
	}
	return kept, total, dropped, dropped > 0
}

// gatherLogs reads the most recent tee logs. An oversized single log is
// head-truncated to the source's allocated budget so its failure signature is
// never dropped wholesale.
func (p *Planner) gatherLogs(ctx context.Context, priority map[SourceType]int, sourceBudget int) []Chunk {
	if p.logs == nil {
		return nil
	}
	bodies, err := p.logs.LatestLogs(ctx, 3)
	if err != nil {
		p.log("[planner] log source error: %v", err)
		return nil
	}
	var chunks []Chunk
	for i, body := range bodies {
		if body == "" {
			continue
		}
		toks := p.tokens.Estimate(body)
		if sourceBudget > 0 && toks > sourceBudget {
			// Keep only the leading region of an oversized log so the source
			// never blows its allocation on a single file.
			body = p.truncateToBudget(body, sourceBudget)
			toks = p.tokens.Estimate(body)
		}
		chunks = append(chunks, Chunk{
			Source:   SourceLog,
			Content:  body,
			Score:    1.0 - float64(i)*0.1, // newest logs rank first
			Priority: priority[SourceLog],
			Tokens:   toks,
		})
	}
	return chunks
}

// gatherCallChains traces inbound callers of the symbols named in the input.
func (p *Planner) gatherCallChains(ctx context.Context, priority map[SourceType]int, input string) []Chunk {
	if p.graph == nil {
		return nil
	}
	symbols := p.symbolTokens(input, 3)
	var chunks []Chunk
	for _, sym := range symbols {
		tree, err := p.graph.CallChain(ctx, sym, 2)
		if err != nil || tree == "" {
			continue
		}
		chunks = append(chunks, Chunk{
			Source:   SourceCallTree,
			Content:  tree,
			Score:    0.9,
			Priority: priority[SourceCallTree],
			Tokens:   p.tokens.Estimate(tree),
		})
	}
	return chunks
}

// gatherSymbols resolves the input's symbol tokens to their graph definitions.
func (p *Planner) gatherSymbols(ctx context.Context, priority map[SourceType]int, input string) []Chunk {
	if p.graph == nil {
		return nil
	}
	symbols := p.symbolTokens(input, 4)
	var chunks []Chunk
	for _, sym := range symbols {
		refs, err := p.graph.ResolveSymbol(ctx, sym)
		if err != nil || len(refs) == 0 {
			continue
		}
		var b strings.Builder
		for _, r := range refs {
			fmt.Fprintf(&b, "%s %s (%s:%d)\n", r.Kind, r.QualName, r.File, r.Line)
			if r.Signature != "" {
				b.WriteString("  " + r.Signature + "\n")
			}
		}
		body := strings.TrimSpace(b.String())
		if body == "" {
			continue
		}
		chunks = append(chunks, Chunk{
			Source:   SourceGraph,
			Content:  body,
			Score:    0.8,
			Priority: priority[SourceGraph],
			Tokens:   p.tokens.Estimate(body),
		})
	}
	return chunks
}

// gatherArchitecture assembles the architecture summary + route map.
func (p *Planner) gatherArchitecture(ctx context.Context, priority map[SourceType]int) []Chunk {
	if p.graph == nil {
		return nil
	}
	summary, err := p.graph.ArchitectureSummary(ctx)
	if err != nil {
		p.log("[planner] architecture summary error: %v", err)
		return nil
	}
	chunks := make([]Chunk, 0, 2)
	if summary != "" {
		chunks = append(chunks, Chunk{
			Source:   SourceArch,
			Content:  summary,
			Score:    1.0,
			Priority: priority[SourceArch],
			Tokens:   p.tokens.Estimate(summary),
		})
	}
	if routes := p.routesOrEmpty(ctx); routes != "" {
		chunks = append(chunks, Chunk{
			Source:   SourceArch,
			Content:  routes,
			Score:    0.9,
			Priority: priority[SourceArch],
			Tokens:   p.tokens.Estimate(routes),
		})
	}
	return chunks
}

// routesOrEmpty fetches the route map, logging failures quietly.
func (p *Planner) routesOrEmpty(ctx context.Context) string {
	routes, err := p.graph.Routes(ctx)
	if err != nil {
		p.log("[planner] routes error: %v", err)
		return ""
	}
	return routes
}

// gatherFileHits performs a focused search and pulls localized snippets for
// the best matches. File chunks carry the LOWEST priority so budget
// enforcement drops raw file reads in favor of graph symbols.
func (p *Planner) gatherFileHits(ctx context.Context, priority map[SourceType]int, input string) []Chunk {
	if p.files == nil {
		return nil
	}
	hits, err := p.files.Search(ctx, input)
	if err != nil {
		p.log("[planner] file source error: %v", err)
		return nil
	}
	var chunks []Chunk
	seen := make(map[string]bool)
	for _, h := range hits {
		key := fmt.Sprintf("%s:%d", h.File, h.Line)
		if seen[key] {
			continue
		}
		seen[key] = true
		body := h.Content
		if body == "" {
			body = fmt.Sprintf("%s:%d", h.File, h.Line)
			if h.Symbol != "" {
				body += " " + h.Symbol
			}
		}
		chunks = append(chunks, Chunk{
			Source:   SourceFile,
			Content:  body,
			Score:    h.Score,
			Priority: priority[SourceFile],
			Tokens:   p.tokens.Estimate(body),
		})
	}
	return chunks
}

// symbolTokens extracts plausible symbol tokens from the input: words that
// look like identifiers (camelCase, snake_case, dotted paths, trailing "()"),
// capped at max.
func (p *Planner) symbolTokens(input string, max int) []string {
	var out []string
	seen := make(map[string]bool)
	for _, tok := range strings.Fields(input) {
		tok = strings.Trim(tok, ".,:;!?()[]{}\"'`@#")
		if tok == "" || len(tok) < 2 {
			continue
		}
		lower := strings.ToLower(tok)
		// Skip function words and common verbs.
		if isFunctionWord(lower) {
			continue
		}
		if seen[tok] {
			continue
		}
		seen[tok] = true
		out = append(out, tok)
		if len(out) >= max {
			break
		}
	}
	return out
}

// isFunctionWord filters high-frequency tokens that are never symbols.
func isFunctionWord(lower string) bool {
	switch lower {
	case "the", "and", "for", "with", "why", "how", "what", "when", "does",
		"is", "are", "this", "that", "from", "into", "about", "will", "can",
		"explain", "describe", "find", "show", "fix", "debug", "log", "panic",
		"error", "crash", "test", "tests", "failure", "failing", "refactor",
		"architecture", "overview", "route", "routes", "function", "func":
		return true
	default:
		return false
	}
}

// truncateToBudget trims text to fit within the given token budget while
// preserving the head (which carries the failure signature for logs). The
// truncation marker's own weight is reserved so the result stays within
// budget.
func (p *Planner) truncateToBudget(text string, budget int) string {
	if p.tokens.Estimate(text) <= budget {
		return text
	}
	const marker = "\n[truncated]"
	// Reserve room for the marker so the trimmed result plus marker stays
	// within the budget.
	markerBudget := p.tokens.Estimate(marker)
	lines := strings.Split(text, "\n")
	var b strings.Builder
	used := 0
	for _, line := range lines {
		toks := p.tokens.Estimate(line)
		if used+toks+markerBudget > budget {
			break
		}
		used += toks
		b.WriteString(line)
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		// Even a single line exceeds the budget: hard-slice by characters.
		maxChars := int(float64(budget-markerBudget) / p.tokens.TokensPerChar)
		if maxChars > 0 && maxChars < len(text) {
			return text[:maxChars] + marker
		}
	}
	trimmed := strings.TrimRight(b.String(), "\n")
	if trimmed == "" {
		return ""
	}
	return trimmed + marker
}

// log writes to the activity sink.
func (p *Planner) log(format string, args ...interface{}) {
	if p != nil && p.logFn != nil {
		p.logFn(format, args...)
	}
}
