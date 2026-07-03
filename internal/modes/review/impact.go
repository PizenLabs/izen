package review

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/internal/graph"
)

type ImpactAnalyzer struct {
	root  string
	graph *graph.Graph
	fset  *token.FileSet
}

func NewImpactAnalyzer(root string, g *graph.Graph) *ImpactAnalyzer {
	return &ImpactAnalyzer{
		root:  root,
		graph: g,
		fset:  token.NewFileSet(),
	}
}

func (ia *ImpactAnalyzer) Analyze(files []DiffFile) (*ImpactRadius, error) {
	radius := &ImpactRadius{}

	for _, df := range files {
		if df.Status == "deleted" || df.Status == "untracked" {
			continue
		}
		radius.DirectFiles = append(radius.DirectFiles, df.Path)
	}

	radius.DirectFiles = unique(radius.DirectFiles)

	indirectFiles := ia.findIndirectFiles(radius.DirectFiles)
	radius.IndirectFiles = indirectFiles

	allFiles := append(radius.DirectFiles, radius.IndirectFiles...)
	radius.AffectedPkgs = ia.extractPackages(allFiles)

	symbols := ia.extractAffectedSymbols(radius.DirectFiles)
	radius.AffectedSyms = symbols

	importChains := ia.traceImportChains(radius.DirectFiles, radius.IndirectFiles)
	radius.ImportChains = importChains

	callChains := ia.TraceDownstreamCalls(radius.DirectFiles)
	radius.CallChains = callChains

	radius.Complexity = ia.estimateComplexity(radius.DirectFiles)

	return radius, nil
}

func (ia *ImpactAnalyzer) findIndirectFiles(directFiles []string) []string {
	indirect := make(map[string]bool)

	if ia.graph == nil {
		importGraph := ia.buildImportGraph(directFiles)
		for _, df := range directFiles {
			for file, deps := range importGraph {
				for _, dep := range deps {
					if dep == df && !ia.contains(directFiles, file) {
						indirect[file] = true
					}
				}
			}
		}
	} else {
		for _, df := range directFiles {
			deps := ia.graph.Dependents[df]
			for _, dep := range deps {
				if !ia.contains(directFiles, dep) {
					indirect[dep] = true
				}
			}

			imports := ia.graph.Imports[df]
			for _, imp := range imports {
				for _, fn := range ia.graph.Files {
					if fn.Package == imp && !ia.contains(directFiles, fn.Path) {
						indirect[fn.Path] = true
					}
				}
			}
		}
	}

	var result []string
	for f := range indirect {
		result = append(result, f)
	}
	sort.Strings(result)
	return result
}

// TraceDownstreamCalls computes the full downstream call chain for each directly
// modified file by walking the graph's Dependents map. Each chain lists the
// sequence of callers that depend on the modified file, forming an explicit
// regression-risk trace.
func (ia *ImpactAnalyzer) TraceDownstreamCalls(directFiles []string) []CallChain {
	if ia.graph == nil {
		return nil
	}

	var chains []CallChain
	seen := make(map[string]bool)

	for _, df := range directFiles {
		chain := CallChain{
			Source:  df,
			Callers: ia.walkDependents(df, make(map[string]bool), 0),
		}
		if len(chain.Callers) > 0 {
			key := df + ":" + strings.Join(chain.Callers, ",")
			if !seen[key] {
				seen[key] = true
				chains = append(chains, chain)
			}
		}
	}

	sort.Slice(chains, func(i, j int) bool {
		return len(chains[j].Callers) < len(chains[i].Callers)
	})

	if len(chains) > 20 {
		chains = chains[:20]
	}

	return chains
}

func (ia *ImpactAnalyzer) walkDependents(file string, visited map[string]bool, depth int) []string {
	if depth > 10 || visited[file] {
		return nil
	}
	visited[file] = true

	var callers []string
	deps := ia.graph.Dependents[file]
	for _, dep := range deps {
		if !visited[dep] {
			callers = append(callers, dep)
		}
	}
	for _, dep := range deps {
		transitive := ia.walkDependents(dep, visited, depth+1)
		callers = append(callers, transitive...)
	}
	return callers
}

func (ia *ImpactAnalyzer) buildImportGraph(directFiles []string) map[string][]string {
	graph := make(map[string][]string)

	filepath.Walk(ia.root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.Contains(path, "vendor/") || strings.Contains(path, ".izen/") {
			return nil
		}

		rel, err := filepath.Rel(ia.root, path)
		if err != nil {
			return nil
		}

		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return nil
		}

		var imports []string
		for _, imp := range f.Imports {
			imports = append(imports, imp.Path.Value)
		}
		graph[rel] = imports

		return nil
	})

	return graph
}

func (ia *ImpactAnalyzer) extractPackages(files []string) []string {
	pkgs := make(map[string]bool)

	for _, f := range files {
		pkg := filepath.Dir(f)
		if pkg == "." || pkg == "" {
			base := filepath.Base(ia.root)
			pkg = base
		}
		pkgs[pkg] = true
	}

	var result []string
	for p := range pkgs {
		result = append(result, p)
	}
	sort.Strings(result)
	return result
}

func (ia *ImpactAnalyzer) extractAffectedSymbols(files []string) []AffectedSymbol {
	var symbols []AffectedSymbol

	if ia.graph != nil {
		for _, f := range files {
			fn := ia.graph.LookupFile(f)
			if fn == nil {
				continue
			}
			for _, sym := range fn.Symbols {
				impact := "direct"
				if !sym.Exported {
					impact = "indirect"
				}
				symbols = append(symbols, AffectedSymbol{
					Name:     sym.Name,
					Kind:     sym.Kind.String(),
					File:     sym.File,
					Line:     sym.Line,
					Impact:   impact,
					Exported: sym.Exported,
				})
			}
		}
	}

	sort.Slice(symbols, func(i, j int) bool {
		if symbols[i].File != symbols[j].File {
			return symbols[i].File < symbols[j].File
		}
		return symbols[i].Line < symbols[j].Line
	})

	return symbols
}

func (ia *ImpactAnalyzer) traceImportChains(directFiles, indirectFiles []string) []ImportChain {
	var chains []ImportChain

	if ia.graph == nil {
		return chains
	}

	for _, ind := range indirectFiles {
		for _, dir := range directFiles {
			chain := ia.buildChain(ind, dir, make(map[string]bool), 5)
			if len(chain) > 0 {
				chains = append(chains, ImportChain{
					Source: dir,
					Chain:  chain,
				})
			}
		}
	}

	sort.Slice(chains, func(i, j int) bool {
		return len(chains[i].Chain) < len(chains[j].Chain)
	})

	if len(chains) > 10 {
		chains = chains[:10]
	}

	return chains
}

func (ia *ImpactAnalyzer) buildChain(from, to string, visited map[string]bool, depth int) []string {
	if depth <= 0 {
		return nil
	}

	visited[from] = true
	deps := ia.graph.Imports[from]

	for _, dep := range deps {
		if dep == to {
			return []string{from, to}
		}
	}

	for _, dep := range deps {
		if visited[dep] {
			continue
		}
		chain := ia.buildChain(dep, to, visited, depth-1)
		if len(chain) > 0 {
			return append([]string{from}, chain...)
		}
	}

	return nil
}

func (ia *ImpactAnalyzer) estimateComplexity(files []string) int {
	complexity := 0

	for _, f := range files {
		fset := token.NewFileSet()
		path := filepath.Join(ia.root, f)

		src, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		parsed, err := parser.ParseFile(fset, f, src, parser.ParseComments)
		if err != nil {
			complexity += 5
			continue
		}

		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			complexity += ia.funcComplexity(fn)
		}
	}

	return complexity
}

func (ia *ImpactAnalyzer) funcComplexity(fn *ast.FuncDecl) int {
	c := 1

	if fn.Body == nil {
		return c
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt:
			c++
		case *ast.CaseClause:
			c++
		case *ast.BinaryExpr:
			c++
		}
		return true
	})

	return c
}

func (ia *ImpactAnalyzer) contains(files []string, target string) bool {
	for _, f := range files {
		if f == target {
			return true
		}
	}
	return false
}

func unique(items []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, item := range items {
		if !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}
	return result
}

func prettyPrint(result *ReviewResult) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("Branch: %s → %s\n", result.BaseBranch, result.Branch))
	b.WriteString(fmt.Sprintf("Commit: %s\n", result.CommitHash))
	b.WriteString("═══════════════════════════════════════\n\n")

	b.WriteString(fmt.Sprintf("Review Score: %d/100\n", result.Score))
	b.WriteString(fmt.Sprintf("Risk Score: %d/100\n", result.ImpactRadius.RiskScore))
	b.WriteString(fmt.Sprintf("Duration: %s\n\n", result.Duration))

	b.WriteString("Changed Files:\n")
	for _, f := range result.FilesChanged {
		statusSym := "~"
		switch f.Status {
		case "added":
			statusSym = "+"
		case "deleted":
			statusSym = "-"
		case "renamed":
			statusSym = "→"
		}
		b.WriteString(fmt.Sprintf("  %s %s (+%d/-%d)\n", statusSym, f.Path, f.Additions, f.Deletions))
	}
	b.WriteString("\n")

	if len(result.ImpactRadius.IndirectFiles) > 0 {
		b.WriteString("Impact Radius:\n")
		b.WriteString(fmt.Sprintf("  Direct: %d files\n", len(result.ImpactRadius.DirectFiles)))
		b.WriteString(fmt.Sprintf("  Indirect: %d files\n", len(result.ImpactRadius.IndirectFiles)))
		b.WriteString(fmt.Sprintf("  Packages: %s\n\n", strings.Join(result.ImpactRadius.AffectedPkgs, ", ")))
	}

	severityOrder := []RiskSeverity{RiskCritical, RiskHigh, RiskMedium, RiskLow, RiskInfo}
	for _, sev := range severityOrder {
		var sevFindings []RiskFinding
		for _, f := range result.RiskFindings {
			if f.Severity == sev {
				sevFindings = append(sevFindings, f)
			}
		}
		if len(sevFindings) > 0 {
			b.WriteString(fmt.Sprintf("  [%s] (%d)\n", strings.ToUpper(string(sev)), len(sevFindings)))
			for _, f := range sevFindings {
				b.WriteString(fmt.Sprintf("    %s:%d — %s (%s)\n", f.File, f.Line, f.Description, f.RuleID))
			}
		}
	}
	b.WriteString("\n")

	if len(result.Recommendations) > 0 {
		b.WriteString("Recommendations:\n")
		for i, rec := range result.Recommendations {
			b.WriteString(fmt.Sprintf("  %d. %s\n", i+1, rec))
		}
		b.WriteString("\n")
	}

	b.WriteString(fmt.Sprintf("Summary: %s\n", result.Summary))

	return b.String()
}

func init() {
	_ = prettyPrint
}
