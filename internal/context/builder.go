package context

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/internal/git"
	"github.com/PizenLabs/izen/internal/graph"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/session"
)

type Builder struct {
	graph   *graph.Graph
	git     *git.Engine
	session *session.Session
	root    string
}

func NewBuilder(root string, g *graph.Graph, ge *git.Engine, sess *session.Session) *Builder {
	return &Builder{
		root:    root,
		graph:   g,
		git:     ge,
		session: sess,
	}
}

type BuildRequest struct {
	Query       string
	Files       []string
	Symbols     []string
	IncludeDiff bool
	IncludeAll  bool
	MaxFiles    int
	MaxSymbols  int
}

func (b *Builder) Build(req BuildRequest) *Context {
	ctx := &Context{
		Objective: b.session.Objective,
		Mode:      b.session.Mode.String(),
		Query:     req.Query,
	}

	if req.MaxFiles == 0 {
		req.MaxFiles = 10
	}
	if req.MaxSymbols == 0 {
		req.MaxSymbols = 30
	}

	if b.graph != nil {
		if req.IncludeAll {
			b.collectAllFiles(ctx, req)
		} else {
			b.collectRelevantFiles(ctx, req)
		}
	}

	if b.git != nil && req.IncludeDiff && b.git.IsRepo() {
		b.collectDiff(ctx)
		b.collectStatus(ctx)
	}

	ctx.BuildTrace()

	return ctx
}

func (b *Builder) collectRelevantFiles(ctx *Context, req BuildRequest) {
	matched := make(map[string]bool)

	for _, name := range req.Symbols {
		if b.graph == nil {
			continue
		}
		symbols := b.graph.LookupSymbol(name)
		for _, sym := range symbols {
			matched[sym.File] = true
		}
	}

	for _, f := range req.Files {
		matched[f] = true
	}

	if req.Query != "" && b.graph != nil {
		lower := strings.ToLower(req.Query)
		for _, f := range b.graph.Files {
			path := strings.ToLower(f.Path)
			if strings.Contains(path, lower) {
				matched[f.Path] = true
			}
			for _, sym := range f.Symbols {
				if strings.Contains(strings.ToLower(sym.Name), lower) {
					matched[f.Path] = true
					break
				}
			}
		}
	}

	for path := range matched {
		fn := b.graph.LookupFile(path)
		if fn == nil {
			continue
		}
		ctx.Files = append(ctx.Files, CompressFile(fn, req.MaxSymbols))
	}

	sort.Slice(ctx.Files, func(i, j int) bool {
		return ctx.Files[i].Path < ctx.Files[j].Path
	})

	if len(ctx.Files) > req.MaxFiles {
		ctx.Files = ctx.Files[:req.MaxFiles]
	}
}

func (b *Builder) collectAllFiles(ctx *Context, req BuildRequest) {
	if b.graph == nil {
		return
	}
	for _, fn := range b.graph.Files {
		ctx.Files = append(ctx.Files, CompressFile(&fn, req.MaxSymbols))
		if req.MaxFiles > 0 && len(ctx.Files) >= req.MaxFiles {
			break
		}
	}
}

func (b *Builder) collectDiff(ctx *Context) {
	diff, err := b.git.Diff()
	if err == nil && diff != "" {
		ctx.Diff = diff
	}
}

func (b *Builder) collectStatus(ctx *Context) {
	entries, err := b.git.Status()
	if err != nil {
		return
	}
	for _, e := range entries {
		label := statusLabel(e.Staging, e.Worktree)
		ctx.Status = append(ctx.Status, label+" "+e.Path)
	}
}

func (b *Builder) AddError(ctx *Context, err error) {
	if err != nil {
		ctx.Errors = append(ctx.Errors, err.Error())
	}
}

func CompressFile(fn *graph.FileNode, maxSymbols int) FileSlice {
	fs := FileSlice{
		Path:    fn.Path,
		Package: fn.Package,
		Imports: fn.Imports,
		Lines:   fn.Lines,
		Size:    fn.Size,
	}

	for _, sym := range fn.Symbols {
		ref := SymbolRef{
			Name:      sym.Name,
			Kind:      sym.Kind.String(),
			File:      sym.File,
			Line:      sym.Line,
			Signature: sym.Signature,
			Exported:  sym.Exported,
		}
		fs.Symbols = append(fs.Symbols, ref)
	}

	if maxSymbols > 0 && len(fs.Symbols) > maxSymbols {
		exported := make([]graph.Symbol, 0)
		for _, s := range fn.Symbols {
			if s.Exported {
				exported = append(exported, s)
			}
		}
		sort.Slice(exported, func(i, j int) bool {
			return exported[i].Name < exported[j].Name
		})
		if len(exported) > 0 {
			fs.Symbols = fs.Symbols[:0]
			for _, s := range exported {
				if len(fs.Symbols) >= maxSymbols {
					break
				}
				fs.Symbols = append(fs.Symbols, SymbolRef{
					Name:      s.Name,
					Kind:      s.Kind.String(),
					File:      s.File,
					Line:      s.Line,
					Signature: s.Signature,
					Exported:  s.Exported,
				})
			}
		} else {
			fs.Symbols = fs.Symbols[:maxSymbols]
		}
	}

	return fs
}

func statusLabel(staging, worktree string) string {
	switch {
	case staging == "?" && worktree == "?":
		return "untracked"
	case staging == "M":
		return "staged"
	case worktree == "M":
		return "modified"
	case staging == "A":
		return "added"
	case staging == "D":
		return "deleted"
	case staging == "R":
		return "renamed"
	default:
		return "changed"
	}
}

// BuildPlanAssembly delegates to the deterministic Plan mode context assembler.
// It returns a pre-filtered context with only the symbols matching the intent
// keywords, plus the directory boundary map and dirty file status.
func (b *Builder) BuildPlanAssembly(objective string, attachedFiles []string) *plan.AssemblyResult {
	p := plan.NewPlanner(b.root, b.graph, b.git)
	result := p.AssemblePlanContext(plan.AssemblyRequest{
		Objective:     objective,
		Keywords:      append(buildKeywords(objective), attachedFiles...),
		AttachedFiles: attachedFiles,
	})
	result.AttachObjective(objective)
	return result
}

// buildKeywords extracts intent-bearing tokens from a query string.
func buildKeywords(query string) []string {
	var kws []string
	for _, tok := range strings.Fields(query) {
		tok = strings.Trim(tok, ".,:;!?()[]{}\"'`")
		if len(tok) < 3 {
			continue
		}
		kws = append(kws, tok)
	}
	return kws
}

func (b *Builder) ImportGraph() string {
	if b.graph == nil {
		return ""
	}
	var bld strings.Builder
	for path, imports := range b.graph.Imports {
		bld.WriteString(filepath.Base(path))
		bld.WriteString(" imports: ")
		bld.WriteString(strings.Join(imports, ", "))
		bld.WriteString("\n")
	}
	return bld.String()
}

func (b *Builder) DependentsOf(target string) []FileSlice {
	if b.graph == nil {
		return nil
	}
	deps := b.graph.Dependents[target]
	var slices []FileSlice
	for _, dep := range deps {
		fn := b.graph.LookupFile(dep)
		if fn != nil {
			slices = append(slices, CompressFile(fn, 5))
		}
	}
	return slices
}

// BuildDependencySlice assembles a compact, token-efficient context for a given
// symbol by retrieving its file's symbol table plus files that depend on it
// (import callers). This avoids dumping raw file contents.
func (b *Builder) BuildDependencySlice(symbol string) *Context {
	ctx := &Context{
		Objective: b.session.Objective,
		Mode:      b.session.Mode.String(),
		Query:     symbol,
	}

	if b.graph == nil {
		return ctx
	}

	symbols := b.graph.LookupSymbol(symbol)
	if len(symbols) == 0 {
		return ctx
	}

	seen := make(map[string]bool)

	// File containing the symbol.
	sym := symbols[0]
	if fn := b.graph.LookupFile(sym.File); fn != nil {
		ctx.Files = append(ctx.Files, CompressFile(fn, 30))
		seen[sym.File] = true

		// This file's import callers (dependents).
		for _, dep := range b.graph.Dependents[sym.File] {
			if !seen[dep] {
				if fn := b.graph.LookupFile(dep); fn != nil {
					ctx.Files = append(ctx.Files, CompressFile(fn, 10))
					seen[dep] = true
				}
			}
		}

		// This file's own imports (for call chain context).
		for _, imp := range fn.Imports {
			if !seen[imp] {
				impFile := b.graph.LookupFile(imp)
				if impFile != nil {
					ctx.Files = append(ctx.Files, CompressFile(impFile, 5))
					seen[imp] = true
				}
			}
		}
	}

	ctx.BuildTrace()
	return ctx
}
