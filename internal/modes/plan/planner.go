package plan

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/internal/git"
	"github.com/PizenLabs/izen/internal/graph"
)

// Task represents a single tactical operation in the markdown-based task system.
// Structure: - [ ] TYPE: Target | Description
// Where TYPE is: "FILE_MUTATE", "SHELL_EXEC", "GIT_ACTION"
type Task struct {
	StepNum     int    `json:"step_num"`
	IsDone      bool   `json:"is_done"`
	Status      string `json:"status"`      // "idle", "processing", "done"
	Type        string `json:"type"`        // "FILE_MUTATE", "SHELL_EXEC", "GIT_ACTION"
	Target      string `json:"target"`      // File path or exact CLI command
	Description string `json:"description"` // Explanation of why this step exists
	Rationale   string `json:"rationale,omitempty"`
	Solution    string `json:"solution,omitempty"`
}

// ParseMarkdownToTasks converts markdown content into structured Task objects.
// It finds lines starting with - [ ] or - [x] and parses them into structured Task objects.
// It accepts the syntax: - [ ] TYPE: Target | Description
func ParseMarkdownToTasks(mdContent string) []Task {
	var tasks []Task
	lines := strings.Split(mdContent, "\n")
	taskCount := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- [ ]") || strings.HasPrefix(line, "- [x]") {
			taskCount++
			prefix := "- [x]"
			if strings.HasPrefix(line, "- [ ]") {
				prefix = "- [ ]"
			}
			content := strings.TrimPrefix(line, prefix)
			content = strings.TrimSpace(content)

			parts := strings.SplitN(content, ":", 2)
			if len(parts) < 2 {
				continue
			}
			typeStr := strings.TrimSpace(parts[0])
			rest := strings.TrimSpace(parts[1])

			if !strings.Contains(rest, "|") {
				continue
			}
			targetParts := strings.SplitN(rest, "|", 2)
			target := strings.TrimSpace(targetParts[0])
			desc := strings.TrimSpace(targetParts[1])

			// Anti-escape guard: IZEN MUST NOT mutate documentation
			// (README.md, etc.) to work around compilation or dependency
			// failures. The /plan engine must target go.mod or emit a
			// SHELL_EXEC task instead. Drop any task whose target resolves to
			// a documentation file so the model cannot silently fall back to
			// doc edits under context pressure (this is the failure mode the
			// local 7B model exhibits on compile/dep blockers).
			if IsDocumentationTarget(target, typeStr) {
				continue
			}

			isDone := false
			status := "idle"
			if strings.HasPrefix(line, "- [x]") {
				isDone = true
				status = "done"
			}
			task := Task{
				StepNum:     taskCount,
				IsDone:      isDone,
				Status:      status,
				Type:        typeStr,
				Target:      target,
				Description: desc,
			}
			tasks = append(tasks, task)
		}
	}
	return tasks
}

// ── Deterministic Context Assembly ─────────────────────────────────────────────

// Planner handles deterministic plan context assembly by querying the local AST
// graph and git working tree BEFORE dispatching the LLM payload.
type Planner struct {
	graph  *graph.Graph
	gitEng *git.Engine
	root   string
}

// NewPlanner creates a Planner wired to the project's AST graph and git engine.
func NewPlanner(root string, g *graph.Graph, ge *git.Engine) *Planner {
	return &Planner{
		root:   root,
		graph:  g,
		gitEng: ge,
	}
}

// AssemblyRequest carries the user's intent and any explicitly attached files.
type AssemblyRequest struct {
	Objective     string
	Keywords      []string
	AttachedFiles []string
}

// AssemblyResult holds the pre-filtered, token-budgeted context.
type AssemblyResult struct {
	DirtyFiles     []git.StatusEntry
	SymbolFiles    []SymbolFileRef
	DirectoryMap   string
	RawContext     string
	EstimateTokens int
}

// SymbolFileRef is a compact reference to a file's symbols.
type SymbolFileRef struct {
	Path    string
	Package string
	Symbols []SymRef
}

// SymRef is a lightweight symbol reference for context assembly.
type SymRef struct {
	Name      string
	Kind      string
	Signature string
	Exported  bool
}

// AssemblePlanContext builds a deterministic, token-optimised context.
// It follows the hierarchy: Graph AST Query -> Symbol Definitions -> Call Chains.
func (p *Planner) AssemblePlanContext(req AssemblyRequest) *AssemblyResult {
	result := &AssemblyResult{}

	// Stage 1: Collect dirty files from git working tree.
	result.DirtyFiles = p.collectDirtyFiles()

	// Stage 2: Resolve intent keywords against the graph AST.
	seen := make(map[string]bool)
	for _, kw := range dedupeKeywords(req.Keywords, req.Objective) {
		if p.graph == nil {
			break
		}
		symbols := p.graph.LookupSymbol(kw)
		for _, sym := range symbols {
			if seen[sym.File] {
				continue
			}
			seen[sym.File] = true
			sref := SymbolFileRef{
				Path:    sym.File,
				Package: p.filePackage(sym.File),
			}
			// Inject the matched symbol plus its immediate file neighbours.
			fn := p.graph.LookupFile(sym.File)
			if fn != nil {
				for _, s := range fn.Symbols {
					sref.Symbols = append(sref.Symbols, SymRef{
						Name:      s.Name,
						Kind:      s.Kind.String(),
						Signature: s.Signature,
						Exported:  s.Exported,
					})
				}
			}
			result.SymbolFiles = append(result.SymbolFiles, sref)
		}

		// Dependency slice: add callers of the matched file.
		if p.graph != nil {
			for _, sym := range p.graph.LookupSymbol(kw) {
				for _, dep := range p.graph.Dependents[sym.File] {
					if seen[dep] {
						continue
					}
					seen[dep] = true
					fn := p.graph.LookupFile(dep)
					if fn != nil {
						sref := SymbolFileRef{Path: fn.Path, Package: fn.Package}
						for _, s := range fn.Symbols {
							if s.Exported || len(fn.Symbols) <= 5 {
								sref.Symbols = append(sref.Symbols, SymRef{
									Name:      s.Name,
									Kind:      s.Kind.String(),
									Signature: s.Signature,
									Exported:  s.Exported,
								})
							}
						}
						result.SymbolFiles = append(result.SymbolFiles, sref)
					}
				}
			}
		}
	}

	// Stage 3: Build the directory boundary map.
	result.DirectoryMap = p.buildDirectoryMap(seen)

	// Stage 4: Render the full context string and estimate tokens.
	result.RawContext = p.renderAssembly(result)
	result.EstimateTokens = EstimateTokens(result.RawContext)

	return result
}

func (p *Planner) collectDirtyFiles() []git.StatusEntry {
	if p.gitEng == nil || !p.gitEng.IsRepo() {
		return nil
	}
	entries, err := p.gitEng.Status()
	if err != nil {
		return nil
	}
	return entries
}

func (p *Planner) filePackage(path string) string {
	if p.graph == nil {
		return ""
	}
	fn := p.graph.LookupFile(path)
	if fn == nil {
		return ""
	}
	return fn.Package
}

func dedupeKeywords(keywords []string, objective string) []string {
	seen := make(map[string]bool)
	var out []string

	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] || len(s) < 2 {
			return
		}
		seen[s] = true
		out = append(out, s)
	}

	for _, kw := range keywords {
		add(kw)
	}
	for _, tok := range strings.Fields(objective) {
		tok = strings.Trim(tok, ".,:;!?()[]{}")
		add(tok)
	}

	// Prioritise CamelCase tokens (likely symbol names).
	sort.SliceStable(out, func(i, j int) bool {
		ii, jj := 0, 0
		for _, c := range out[i] {
			if c >= 'A' && c <= 'Z' {
				ii++
			}
		}
		for _, c := range out[j] {
			if c >= 'A' && c <= 'Z' {
				jj++
			}
		}
		return ii > jj
	})

	// Cap keywords to prevent token explosion.
	if len(out) > 5 {
		out = out[:5]
	}
	return out
}

func (p *Planner) buildDirectoryMap(seen map[string]bool) string {
	if p.graph == nil || len(p.graph.Files) == 0 {
		return ""
	}

	// Collect directories from seen files + all graph files (compressed).
	dirs := make(map[string][]string)
	for _, f := range p.graph.Files {
		dir := filepath.Dir(f.Path)
		if dir == "." {
			dir = "/"
		}
		dirs[dir] = append(dirs[dir], filepath.Base(f.Path))
	}

	// Sort directories for deterministic output.
	var dirList []string
	for d := range dirs {
		dirList = append(dirList, d)
	}
	sort.Strings(dirList)

	var b strings.Builder
	b.WriteString("### DIRECTORY BOUNDARY MAP\n")
	fmt.Fprintf(&b, "PROJECT ROOT: %s\n", p.root)
	b.WriteString("ABSOLUTE BOUNDARY: All paths are relative to project root.\n")
	b.WriteString("No paths outside this tree may be referenced.\n\n")

	for _, d := range dirList {
		files := dirs[d]
		if len(files) > 8 {
			files = files[:8]
			files = append(files, "...")
		}
		fmt.Fprintf(&b, "  %s/  (%d file(s))\n", d, len(dirs[d]))
		if d == "/" {
			continue
		}
		for _, f := range files {
			fmt.Fprintf(&b, "    %s\n", f)
		}
	}
	return b.String()
}

func (p *Planner) renderAssembly(result *AssemblyResult) string {
	var b strings.Builder

	// Section 1: Working tree status.
	if len(result.DirtyFiles) > 0 {
		b.WriteString("### MODIFIED FILES\n")
		for _, e := range result.DirtyFiles {
			label := "modified"
			switch e.Staging {
			case "?":
				label = "untracked"
			case "M":
				label = "staged"
			}
			fmt.Fprintf(&b, "  %s: %s\n", label, e.Path)
		}
		b.WriteString("\n")
	}

	// Section 2: Symbol definitions from graph.
	if len(result.SymbolFiles) > 0 {
		b.WriteString("### RELEVANT SYMBOLS\n")
		for _, sf := range result.SymbolFiles {
			fmt.Fprintf(&b, "  %s", sf.Path)
			if sf.Package != "" {
				fmt.Fprintf(&b, " (pkg: %s)", sf.Package)
			}
			b.WriteString("\n")
			for _, sym := range sf.Symbols {
				fmt.Fprintf(&b, "    %s %s", sym.Kind, sym.Name)
				if sym.Signature != "" {
					fmt.Fprintf(&b, " %s", sym.Signature)
				}
				if sym.Exported {
					b.WriteString(" (exported)")
				}
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
	}

	// Section 3: Directory boundary map.
	if result.DirectoryMap != "" {
		b.WriteString(result.DirectoryMap)
		b.WriteString("\n")
	}

	// Section 4: User objective.
	b.WriteString("### USER OBJECTIVE\n")
	fmt.Fprintf(&b, "%s\n", result.getObjective())

	return b.String()
}

func (r *AssemblyResult) getObjective() string {
	// The objective is embedded through AssemblyRequest; this is a proxy
	// for rendering. In practice the caller sets it directly.
	return ""
}

// AttachObjective sets the objective after assembly for rendering.
func (r *AssemblyResult) AttachObjective(objective string) {
	r.RawContext = strings.ReplaceAll(r.RawContext, "### USER OBJECTIVE\n\n", "### USER OBJECTIVE\n"+objective+"\n")
	r.EstimateTokens = EstimateTokens(r.RawContext)
}
