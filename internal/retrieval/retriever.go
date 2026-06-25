package retrieval

import (
	"fmt"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/graph"
)

type Tier string

const (
	TierGraph    Tier = "graph"
	TierLynx     Tier = "lynx"
	TierGlob     Tier = "glob"
	TierRipgrep  Tier = "rg"
	TierGrep     Tier = "grep"
	TierRead     Tier = "read"
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
	root     string
	graph    *GraphLookup
	fallback *FallbackChain
	tiers    []Tier
}

type RetrieverOption func(*Retriever)

func WithTiers(tiers ...Tier) RetrieverOption {
	return func(r *Retriever) {
		r.tiers = tiers
	}
}

func NewRetriever(root string, g *graph.Graph, opts ...RetrieverOption) *Retriever {
	r := &Retriever{
		root:     root,
		graph:    NewGraphLookup(g, root),
		fallback: NewFallbackChain(root),
		tiers: []Tier{
			TierGraph,
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

func (r *Retriever) Retrieve(query Query) *ResultSet {
	start := time.Now()

	result := &ResultSet{Strategy: "none"}
	usedTiers := make([]string, 0)

	for _, tier := range r.tiers {
		rs := r.executeTier(tier, query)
		if rs == nil || rs.Empty() {
			continue
		}

		result.Merge(rs)
		usedTiers = append(usedTiers, string(tier))
		result.Strategy = strings.Join(usedTiers, " → ")

		if rs.Confidence >= ConfExact.Float64() {
			break
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
		return r.fallback.Glob(pattern)

	case TierRipgrep:
		if r.fallback == nil || query.Text == "" {
			return nil
		}
		return r.fallback.Ripgrep(query.Text, query.FilePattern)

	case TierGrep:
		if r.fallback == nil || query.Text == "" {
			return nil
		}
		return r.fallback.Grep(query.Text)

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

	case TierLynx:
		return nil

	default:
		return nil
	}
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