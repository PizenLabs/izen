package layer2

import (
	"fmt"
	"sort"
)

const (
	kindFunction = "function"
	kindMethod   = "method"
	kindStruct   = "struct"

	rankAlpha     = 0.85
	rankTolerance = 1e-8
	rankMaxIter   = 60
	seedFileBoost = 0.5
)

// ScoredSymbol pairs a symbol with its computed structural relevance.
type ScoredSymbol struct {
	Symbol SymbolInfo
	Score  float64
	Depth  int
}

// RankResult is the deterministic output of a ranking pass.
type RankResult struct {
	Symbols    []ScoredSymbol
	Iterations int
}

// Ranker ranks the symbols surrounding a request target by structural
// relevance. It runs a personalized PageRank over the SoR call graph and
// blends the result with a call-graph-depth bias so that symbols nearer the
// target outrank distant ones. Types and interfaces declared in the target
// file receive a floor score so the target's structural context always
// survives.
type Ranker struct {
	sor *Sor
}

// NewRanker returns a ranker over the given SoR.
func NewRanker(sor *Sor) *Ranker {
	return &Ranker{sor: sor}
}

// Rank returns the symbols most relevant to req, ordered by relevance
// descending, capped at policy.MaxSymbols.
func (r *Ranker) Rank(req ContextRequest, policy ContextPolicy) (RankResult, error) {
	if r.sor == nil {
		return RankResult{}, fmt.Errorf("%w: nil sor", ErrInvalidRequest)
	}
	seeds := r.seedSymbols(req)
	if len(seeds) == 0 {
		if req.TargetSymbol != "" {
			return RankResult{}, fmt.Errorf("%w: %q", ErrTargetNotFound, req.TargetSymbol)
		}
		return RankResult{}, nil
	}

	seedFile := req.TargetFile
	if seedFile == "" {
		seedFile = seeds[0].File
	}

	depthLimit := 2
	if policy.ExpandDependencies {
		depthLimit = 4
	}

	seedQuals := callableQuals(seeds)
	if len(seedQuals) == 0 {
		for _, sym := range r.sor.SymbolsOfFile(seedFile) {
			if isCallable(sym) {
				seedQuals = append(seedQuals, sym.QualName)
			}
		}
		seedQuals = dedupe(seedQuals)
	}
	if len(seedQuals) == 0 {
		return RankResult{}, nil
	}

	adj := r.buildRankGraph(seedQuals, depthLimit)
	if len(adj) == 0 {
		return RankResult{}, nil
	}

	pr, iterations := pageRank(adj, seedQuals, rankAlpha, rankMaxIter, rankTolerance)
	depth := bfsDepth(adj, seedQuals)

	scores := make(map[string]float64, len(adj))
	for qual := range adj {
		dw := 1.0 / (1.0 + float64(depth[qual]))
		scores[qual] = 0.7*pr[qual] + 0.3*dw
	}

	// Structural symbols (types, interfaces) declared in the seed file always
	// belong in context: give them a floor score.
	for _, sym := range r.sor.SymbolsOfFile(seedFile) {
		if isCallable(sym) || sym.QualName == "" {
			continue
		}
		if cur, ok := scores[sym.QualName]; !ok || cur < seedFileBoost {
			scores[sym.QualName] = seedFileBoost
		}
	}

	out := make([]ScoredSymbol, 0, len(scores))
	for qual, score := range scores {
		si, ok := r.sor.LookupQual(qual)
		if !ok {
			continue
		}
		out = append(out, ScoredSymbol{Symbol: si, Score: score, Depth: depth[qual]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Symbol.QualName != out[j].Symbol.QualName {
			return out[i].Symbol.QualName < out[j].Symbol.QualName
		}
		return out[i].Symbol.File < out[j].Symbol.File
	})
	if len(out) > policy.MaxSymbols {
		out = out[:policy.MaxSymbols]
	}
	return RankResult{Symbols: out, Iterations: iterations}, nil
}

// seedSymbols resolves the request target to its seed symbol set.
func (r *Ranker) seedSymbols(req ContextRequest) []SymbolInfo {
	if req.TargetSymbol != "" {
		if si, ok := r.sor.Symbol(req.TargetSymbol); ok {
			return []SymbolInfo{si}
		}
		return nil
	}
	if req.TargetFile != "" {
		syms := r.sor.SymbolsOfFile(req.TargetFile)
		var callables []SymbolInfo
		for _, sym := range syms {
			if isCallable(sym) {
				callables = append(callables, sym)
			}
		}
		if len(callables) > 0 {
			return callables
		}
		return syms
	}
	return nil
}

// buildRankGraph collects the undirected call-graph neighborhood of the seeds
// up to depthLimit hops.
func (r *Ranker) buildRankGraph(seeds []string, depthLimit int) map[string][]string {
	adj := make(map[string][]string)
	seen := make(map[string]int)
	queue := make([]string, 0, len(seeds))
	for _, seed := range seeds {
		if _, ok := seen[seed]; !ok {
			seen[seed] = 0
			queue = append(queue, seed)
		}
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		d := seen[cur]
		if d >= depthLimit {
			adj[cur] = nil
			continue
		}
		neighbors := make(map[string]bool)
		for _, c := range r.sor.Callers(cur) {
			if c.QualName != "" {
				neighbors[c.QualName] = true
			}
		}
		for _, c := range r.sor.Callees(cur) {
			if c.QualName != "" {
				neighbors[c.QualName] = true
			}
		}
		adj[cur] = sortedKeys(neighbors)
		for nb := range neighbors {
			if _, ok := seen[nb]; !ok {
				seen[nb] = d + 1
				queue = append(queue, nb)
			}
		}
	}
	return adj
}

// pageRank runs personalized PageRank over the adjacency graph, seeding mass
// on the target quals. It returns the steady-state scores and the iteration
// count used.
func pageRank(adj map[string][]string, seeds []string, alpha float64, maxIter int, tol float64) (map[string]float64, int) {
	nodes := make([]string, 0, len(adj))
	for q := range adj {
		nodes = append(nodes, q)
	}
	sort.Strings(nodes)
	n := len(nodes)
	iterations := 0
	if n == 0 {
		return map[string]float64{}, iterations
	}

	idx := make(map[string]int, n)
	for i, q := range nodes {
		idx[q] = i
	}

	adjIdx := make([][]int, n)
	outDeg := make([]int, n)
	for i, q := range nodes {
		list := make([]int, 0, len(adj[q]))
		seen := make(map[int]bool, len(adj[q]))
		for _, nb := range adj[q] {
			if j, ok := idx[nb]; ok && !seen[j] {
				seen[j] = true
				list = append(list, j)
			}
		}
		sort.Ints(list)
		adjIdx[i] = list
		outDeg[i] = len(list)
	}

	pers := make([]float64, n)
	for _, seed := range seeds {
		if i, ok := idx[seed]; ok {
			pers[i] = 1
		}
	}
	persSum := 0.0
	for i := range pers {
		persSum += pers[i]
	}
	if persSum == 0 {
		for i := range pers {
			pers[i] = 1 / float64(n)
		}
	} else {
		for i := range pers {
			pers[i] /= persSum
		}
	}

	pr := make([]float64, n)
	copy(pr, pers)
	for it := 0; it < maxIter; it++ {
		iterations = it + 1
		next := make([]float64, n)
		for i := range next {
			next[i] = (1 - alpha) * pers[i]
		}
		for i := 0; i < n; i++ {
			if outDeg[i] == 0 {
				for j := 0; j < n; j++ {
					next[j] += alpha * pr[i] * pers[j]
				}
				continue
			}
			share := alpha * pr[i] / float64(outDeg[i])
			for _, j := range adjIdx[i] {
				next[j] += share
			}
		}
		diff := 0.0
		for i := range pr {
			d := next[i] - pr[i]
			if d < 0 {
				d = -d
			}
			diff += d
		}
		copy(pr, next)
		if diff < tol {
			break
		}
	}

	out := make(map[string]float64, n)
	for i, q := range nodes {
		out[q] = pr[i]
	}
	return out, iterations
}

// bfsDepth computes the shortest-path distance of every node from the seeds.
func bfsDepth(adj map[string][]string, seeds []string) map[string]int {
	depth := make(map[string]int)
	queue := make([]string, 0, len(seeds))
	for _, seed := range seeds {
		if _, ok := adj[seed]; ok && depth[seed] == 0 {
			depth[seed] = 0
			queue = append(queue, seed)
		}
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, nb := range adj[cur] {
			if _, ok := depth[nb]; !ok {
				depth[nb] = depth[cur] + 1
				queue = append(queue, nb)
			}
		}
	}
	return depth
}

func isCallable(s SymbolInfo) bool {
	return s.Kind == kindFunction || s.Kind == kindMethod
}

func callableQuals(syms []SymbolInfo) []string {
	var quals []string
	for _, sym := range syms {
		if isCallable(sym) && sym.QualName != "" {
			quals = append(quals, sym.QualName)
		}
	}
	return dedupe(quals)
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
