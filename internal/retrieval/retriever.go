package retrieval

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/core/artifact"
	"github.com/PizenLabs/izen/internal/graph"
)

var globalRouter *Router
var globalCompressor *ContextCompressor

func SetGlobalRouter(r *Router) {
	globalRouter = r
}

func GetGlobalRouter() *Router {
	return globalRouter
}

func SetGlobalCompressor(cc *ContextCompressor) {
	globalCompressor = cc
}

func GetGlobalCompressor() *ContextCompressor {
	return globalCompressor
}

func BuildGlobalCompressor(g *graph.Graph, objective string) {
	if g != nil {
		globalCompressor = NewContextCompressorFromGraph(g, objective)
	}
}

type Tier string

const (
	TierGraph   Tier = "graph"
	TierLynx    Tier = "lynx"
	TierGlob    Tier = "glob"
	TierRipgrep Tier = "rg"
	TierGrep    Tier = "grep"
	TierRead    Tier = "read"
)

func (t Tier) Order() int {
	switch t {
	case TierGraph:
		return 0
	case TierLynx:
		return 1
	case TierGlob:
		return 2
	case TierRipgrep:
		return 3
	case TierGrep:
		return 4
	case TierRead:
		return 5
	default:
		return 99
	}
}

type Retriever struct {
	root           string
	graph          *GraphLookup
	fallback       *FallbackChain
	tiers          []Tier
	store          *artifact.Store
	tokenEstimator *TokenWeightEstimator
}

type RetrieverOption func(*Retriever)

func WithTiers(tiers ...Tier) RetrieverOption {
	return func(r *Retriever) {
		r.tiers = tiers
	}
}

func WithEvidenceStore(s *artifact.Store) RetrieverOption {
	return func(r *Retriever) {
		r.store = s
	}
}

func isFallbackTier(t Tier) bool {
	return t == TierGlob || t == TierRipgrep || t == TierGrep || t == TierRead
}

func NewRetriever(root string, g *graph.Graph, opts ...RetrieverOption) *Retriever {
	r := &Retriever{
		root:           root,
		graph:          NewGraphLookup(g, root),
		fallback:       NewFallbackChain(root),
		tokenEstimator: NewTokenWeightEstimator(),
		tiers: []Tier{
			TierGraph,
			TierLynx,
			TierGlob,
			TierRipgrep,
			TierGrep,
			TierRead,
		},
	}

	for _, opt := range opts {
		opt(r)
	}

	return r
}

type Query struct {
	Text        string
	File        string
	Symbol      string
	Package     string
	FilePattern string
	Lines       int
}

func (q Query) String() string {
	var parts []string
	if q.Text != "" {
		parts = append(parts, fmt.Sprintf("text=%q", q.Text))
	}
	if q.File != "" {
		parts = append(parts, fmt.Sprintf("file=%q", q.File))
	}
	if q.Symbol != "" {
		parts = append(parts, fmt.Sprintf("symbol=%q", q.Symbol))
	}
	if q.Package != "" {
		parts = append(parts, fmt.Sprintf("pkg=%q", q.Package))
	}
	return strings.Join(parts, " ")
}

// confidenceThresholdReached returns true when the result set's BM25 relevance
// is sufficient to stop tier progression. This provides proper score gating:
//
//   - Graph exact (Confidence >= 1.0): always trust, stop tiers
//   - BM25 Score >= 0.7: high confidence, stop tiers
//   - BM25 Score < 0.3 (or zero): continue to fallback tiers
func confidenceThresholdReached(rs *ResultSet) bool {
	if rs == nil || rs.Empty() {
		return false
	}
	if rs.Confidence >= ConfExact.Float64() {
		return true
	}
	best := rs.Best()
	if best == nil {
		return false
	}
	effScore := best.Score
	if effScore == 0 {
		effScore = best.Confidence
	}
	return effScore >= 0.7
}

func (r *Retriever) Retrieve(query Query) *ResultSet {
	start := time.Now()

	result := &ResultSet{Strategy: "none"}
	usedTiers := make([]string, 0)

	for _, tier := range r.tiers {
		rs := r.executeTier(tier, query)
		if rs == nil || rs.Empty() {
			if rs != nil && rs.Error != "" && globalActivityLog != nil {
				globalActivityLog("[search] tier %s error: %s", tier, rs.Error)
			}
			continue
		}

		result.Merge(rs)
		usedTiers = append(usedTiers, string(tier))
		result.Strategy = strings.Join(usedTiers, " → ")

		if confidenceThresholdReached(rs) {
			best := rs.Best()
			if best != nil && globalActivityLog != nil {
				effScore := best.Score
				if effScore == 0 {
					effScore = best.Confidence
				}
				globalActivityLog("[retrieval] tier %s confidence %.3f >= 0.7 — stopping tier progression", tier, effScore)
			}
			break
		}
		if globalActivityLog != nil {
			globalActivityLog("[retrieval] tier %s confidence below 0.7 — continuing to next tier", tier)
		}
	}

	result.Duration = time.Since(start).Round(time.Millisecond).String()
	return result
}

func (r *Retriever) executeTier(tier Tier, query Query) *ResultSet {
	switch tier {
	case TierGraph:
		if r.graph == nil {
			return nil
		}
		switch {
		case query.Symbol != "":
			return r.graph.SearchAll(query.Symbol)
		case query.File != "":
			return r.graph.SearchFile(query.File)
		case query.Package != "":
			return r.graph.SearchPackage(query.Package)
		case query.Text != "":
			symResult := r.graph.SearchAll(query.Text)
			if !symResult.Empty() {
				return symResult
			}
			return r.graph.SearchImports(query.Text)
		default:
			return nil
		}

	case TierLynx:
		router := GetGlobalRouter()
		if router == nil {
			return nil
		}
		if query.Symbol != "" {
			return r.executeSearchResolve(router, query)
		}
		if query.Text != "" && len(query.Text) >= 5 {
			return r.executeSearchContext(router, query)
		}
		return nil

	case TierGlob:
		if r.fallback == nil {
			return nil
		}
		pattern := query.Text
		if query.FilePattern != "" {
			pattern = query.FilePattern
		}
		if pattern == "" {
			return nil
		}
		rs := r.fallback.Glob(pattern)
		if r.store != nil && !rs.Empty() {
			_, _ = RecordFallbackEvidence(r.store, "glob", pattern, rs)
		}
		return rs

	case TierRipgrep:
		if r.fallback == nil || query.Text == "" {
			return nil
		}
		rs := r.fallback.Ripgrep(query.Text, query.FilePattern)
		if r.store != nil && !rs.Empty() {
			_, _ = RecordFallbackEvidence(r.store, "rg", query.Text, rs)
		}
		return rs

	case TierGrep:
		if r.fallback == nil || query.Text == "" {
			return nil
		}
		rs := r.fallback.Grep(query.Text)
		if r.store != nil && !rs.Empty() {
			_, _ = RecordFallbackEvidence(r.store, "grep", query.Text, rs)
		}
		return rs

	case TierRead:
		if r.fallback == nil {
			return nil
		}
		target := query.File
		if target == "" && query.Symbol != "" {
			target = query.Symbol
		}
		if target == "" {
			return nil
		}
		if query.Lines > 0 {
			return r.fallback.ReadLines(target, 1, query.Lines)
		}
		return r.fallback.ReadFile(target)

	default:
		return nil
	}
}

func (r *Retriever) executeSearchResolve(router *Router, query Query) *ResultSet {
	if globalActivityLog != nil {
		globalActivityLog("[system] resolving symbol: %s", query.Symbol)
	}

	coords, err := router.ResolveSymbol(context.Background(), query.Symbol)
	if err != nil {
		if globalActivityLog != nil {
			globalActivityLog("[FAIL] resolve %q: %v", query.Symbol, err)
		}
		return &ResultSet{
			Strategy:   "search.resolve",
			Confidence: 0,
			Error:      fmt.Sprintf("resolve: %v", err),
		}
	}

	rs := SearchResultAdapter(coords, "search.resolve")

	if globalActivityLog != nil && !rs.Empty() {
		globalActivityLog("[ OK ] resolve %q: %d results", query.Symbol, len(rs.Results))
	}
	if rs.Empty() {
		globalActivityLog("[search] no results for resolve %q", query.Symbol)
	}

	return rs
}

func (r *Retriever) executeSearchContext(router *Router, query Query) *ResultSet {
	if globalActivityLog != nil {
		globalActivityLog("[system] searching context: %s", query.Text)
	}

	chunks, err := router.SearchContext(context.Background(), query.Text)
	if err != nil {
		if globalActivityLog != nil {
			globalActivityLog("[FAIL] search %q: %v", query.Text, err)
		}
		return &ResultSet{
			Strategy:   "search.context",
			Confidence: 0,
			Error:      fmt.Sprintf("search: %v", err),
		}
	}

	// Compress if available.
	if globalCompressor != nil && len(chunks) > 0 {
		chunks = globalCompressor.CompressChunks(chunks)
	}

	rs := SearchChunkAdapter(chunks, "search.context")

	if globalActivityLog != nil && !rs.Empty() {
		globalActivityLog("[ OK ] search %q: %d results", query.Text, len(rs.Results))
	}

	return rs
}

func (r *Retriever) SearchSymbol(name string) *ResultSet {
	return r.Retrieve(Query{Symbol: name})
}

func (r *Retriever) SearchText(text string) *ResultSet {
	return r.Retrieve(Query{Text: text})
}

func (r *Retriever) SearchFile(path string) *ResultSet {
	return r.Retrieve(Query{File: path})
}

func (r *Retriever) SearchPackage(pkg string) *ResultSet {
	return r.Retrieve(Query{Package: pkg})
}

func (r *Retriever) ReadTarget(path string, lines int) *ResultSet {
	return r.Retrieve(Query{File: path, Lines: lines})
}
