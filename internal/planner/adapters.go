package planner

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/internal/graph"
	"github.com/PizenLabs/izen/internal/lea"
	leagraph "github.com/PizenLabs/izen/internal/lea/graph"
	"github.com/PizenLabs/izen/internal/retrieval"
	"github.com/PizenLabs/izen/internal/runtime/output"
)

// ── Lea graph adapter ─────────────────────────────────────────────────────────

// LeaAdapter backs the planner's GraphSource with the Lea structural engine.
// It is the primary source for architecture summaries, route maps, symbol
// resolution and call-chain reconstruction.
type LeaAdapter struct {
	engine *lea.Engine
}

// NewLeaAdapter wraps a Lea engine as a planner GraphSource.
func NewLeaAdapter(e *lea.Engine) *LeaAdapter {
	return &LeaAdapter{engine: e}
}

// ResolveSymbol resolves a symbol to its structural definitions.
func (a *LeaAdapter) ResolveSymbol(ctx context.Context, symbol string) ([]SymbolRef, error) {
	if a == nil || a.engine == nil {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	g := a.engine.Graph()
	var nodes []leagraph.Node
	if q := g.LookupQual(symbol); len(q) > 0 {
		nodes = q
	} else {
		nodes = g.Lookup(symbol)
	}
	out := make([]SymbolRef, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, SymbolRef{
			Name:      n.Name,
			Kind:      string(n.Kind),
			QualName:  n.QualName,
			Package:   n.Package,
			File:      n.File,
			Line:      n.Line,
			Exported:  n.Exported,
			Signature: n.Signature,
		})
	}
	return out, nil
}

// CallChain reconstructs the inbound call tree of a symbol.
func (a *LeaAdapter) CallChain(ctx context.Context, symbol string, depth int) (string, error) {
	if a == nil || a.engine == nil {
		return "", nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if depth <= 0 {
		depth = 2
	}
	tree := a.engine.TraceCallChain(symbol, lea.Inbound, depth)
	if tree.Node.Name == "" {
		return "", nil
	}
	var b strings.Builder
	formatCallTree(&b, tree, 0)
	return strings.TrimSpace(b.String()), nil
}

// ArchitectureSummary returns the compact package/layer overview.
func (a *LeaAdapter) ArchitectureSummary(ctx context.Context) (string, error) {
	if a == nil || a.engine == nil {
		return "", nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	summary := a.engine.GetArchitectureSummary()
	var b strings.Builder
	b.WriteString("Root: " + summary.Root + "\n")
	fmt.Fprintf(&b, "Stats: %d files, %d functions, %d methods, %d types, %d routes\n",
		summary.Stats.FileCount, summary.Stats.FunctionCount, summary.Stats.MethodCount,
		summary.Stats.TypeCount, summary.Stats.RouteCount)
	b.WriteString("\nPackages:\n")
	for _, p := range summary.Packages {
		fmt.Fprintf(&b, "  - %s (%s): %d files, %d symbols, imports %d\n",
			p.Name, p.Dir, p.FileCount, p.SymbolCount, p.ImportCount)
		if len(p.DependsOn) > 0 {
			sort.Strings(p.DependsOn)
			b.WriteString("      depends on: " + strings.Join(p.DependsOn, ", ") + "\n")
		}
	}
	if len(summary.LayerDirection) > 0 {
		b.WriteString("\nLayer directions:\n")
		for _, d := range summary.LayerDirection {
			fmt.Fprintf(&b, "  - %s -> %s (%d edges)\n", d.From, d.To, d.EdgeCount)
		}
	}
	if len(summary.EntryPoints) > 0 {
		b.WriteString("\nEntry points:\n")
		for _, e := range summary.EntryPoints {
			fmt.Fprintf(&b, "  - %s (%s:%d)\n", e.Name, e.File, e.Line)
		}
	}
	return strings.TrimSpace(b.String()), nil
}

// Routes returns the HTTP route → handler map.
func (a *LeaAdapter) Routes(ctx context.Context) (string, error) {
	if a == nil || a.engine == nil {
		return "", nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	routes := a.engine.FindRoutes()
	if len(routes) == 0 {
		return "", nil
	}
	var b strings.Builder
	b.WriteString("HTTP routes:\n")
	for _, r := range routes {
		handler := r.Handler
		if handler == "" {
			handler = r.File
		}
		fmt.Fprintf(&b, "  %s %s -> %s (%s:%d)\n", r.Method, r.Path, handler, r.HandlerFile, r.HandlerLine)
	}
	return strings.TrimSpace(b.String()), nil
}

// formatCallTree renders a lea CallTree indented by depth.
func formatCallTree(b *strings.Builder, tree lea.CallTree, depth int) {
	indent := strings.Repeat("  ", depth)
	fmt.Fprintf(b, "%s%s %s (%s:%d)\n", indent, tree.Node.Kind, tree.Node.QualName, tree.Node.File, tree.Node.Line)
	for _, child := range tree.Children {
		formatCallTree(b, child, depth+1)
	}
}

// ── Native graph adapter (integration seam) ───────────────────────────────────

// GraphAdapter backs the planner's GraphSource with the native in-memory
// graph (internal/graph). It supports symbol resolution and a lightweight
// package overview; call-chain and route data are not available in the native
// graph and degrade to empty strings.
type GraphAdapter struct {
	g *graph.Graph
}

// NewGraphAdapter wraps a native graph as a planner GraphSource.
func NewGraphAdapter(g *graph.Graph) *GraphAdapter {
	return &GraphAdapter{g: g}
}

// ResolveSymbol resolves a symbol to its definitions in the native graph.
func (a *GraphAdapter) ResolveSymbol(ctx context.Context, symbol string) ([]SymbolRef, error) {
	if a == nil || a.g == nil {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	syms := a.g.LookupSymbol(symbol)
	if len(syms) == 0 {
		return nil, nil
	}
	out := make([]SymbolRef, 0, len(syms))
	for _, s := range syms {
		out = append(out, SymbolRef{
			Name:      s.Name,
			Kind:      s.Kind.String(),
			Package:   s.Parent,
			File:      s.File,
			Line:      s.Line,
			Exported:  s.Exported,
			Signature: s.Signature,
		})
	}
	return out, nil
}

// CallChain is unavailable in the native graph.
func (a *GraphAdapter) CallChain(ctx context.Context, symbol string, depth int) (string, error) {
	return "", nil
}

// ArchitectureSummary renders a compact package overview from the native graph.
func (a *GraphAdapter) ArchitectureSummary(ctx context.Context) (string, error) {
	if a == nil || a.g == nil {
		return "", nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	stats := a.g.Stats()
	var b strings.Builder
	fmt.Fprintf(&b, "Stats: %d files, %d functions, %d methods, %d types\n",
		stats.FileCount, stats.FunctionCount, stats.MethodCount, stats.TypeCount)

	files := a.g.Files
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	limit := 40
	if len(files) < limit {
		limit = len(files)
	}
	if limit > 0 {
		b.WriteString("\nFiles:\n")
		for _, f := range files[:limit] {
			fmt.Fprintf(&b, "  - %s (%d symbols)\n", f.Path, len(f.Symbols))
		}
		if len(files) > limit {
			fmt.Fprintf(&b, "  ... and %d more\n", len(files)-limit)
		}
	}
	return strings.TrimSpace(b.String()), nil
}

// Routes is unavailable in the native graph.
func (a *GraphAdapter) Routes(ctx context.Context) (string, error) {
	return "", nil
}

// ── Tee log adapter ───────────────────────────────────────────────────────────

// TeeLogAdapter backs the planner's LogSource with the Phase 1 persistent
// tool logs (`.logs/`). The newest logs are read first.
type TeeLogAdapter struct {
	root string
}

// NewTeeLogAdapter wires the workspace `.logs/` directory as the log source.
func NewTeeLogAdapter(root string) *TeeLogAdapter {
	return &TeeLogAdapter{root: root}
}

// LatestLogs returns up to limit newest tee log bodies.
func (a *TeeLogAdapter) LatestLogs(ctx context.Context, limit int) ([]string, error) {
	if a == nil || a.root == "" {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 3
	}
	tee := output.NewTee(a.root)
	paths := tee.Logs()
	if len(paths) > limit {
		paths = paths[:limit]
	}
	var bodies []string
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		bodies = append(bodies, string(data))
	}
	return bodies, nil
}

// ── Retrieval file source adapter ─────────────────────────────────────────────

// RetrievalFileAdapter backs the planner's FileSource with the retrieval
// search engine (Lynx or the native Go engine).
type RetrievalFileAdapter struct {
	engine retrieval.SearchEngine
}

// NewRetrievalFileAdapter wraps a retrieval engine as a planner FileSource.
func NewRetrievalFileAdapter(e retrieval.SearchEngine) *RetrievalFileAdapter {
	return &RetrievalFileAdapter{engine: e}
}

// Search returns ranked file-level matches for the query.
func (a *RetrievalFileAdapter) Search(ctx context.Context, query string) ([]SearchHit, error) {
	if a == nil || a.engine == nil {
		return nil, nil
	}
	chunks, err := a.engine.SearchContext(ctx, query)
	if err != nil {
		return nil, err
	}
	out := make([]SearchHit, 0, len(chunks))
	for _, c := range chunks {
		out = append(out, SearchHit{
			File:    c.File,
			Line:    c.StartLine,
			Content: c.Content,
			Score:   c.Score,
			Symbol:  c.SymbolName,
			Kind:    kindLabel(c.SymbolName, c.File),
		})
	}
	return out, nil
}

// FocusedContext returns the targeted region of a file.
func (a *RetrievalFileAdapter) FocusedContext(ctx context.Context, file string, startLine, endLine int) (string, error) {
	if a == nil || a.engine == nil {
		return "", nil
	}
	return a.engine.GetFocusedContext(ctx, file, startLine, endLine)
}

// kindLabel is a best-effort symbol-kind label for search hits.
func kindLabel(symbol, file string) string {
	if symbol != "" {
		return "symbol"
	}
	_ = file
	return "match"
}
