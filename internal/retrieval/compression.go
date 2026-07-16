package retrieval

import (
	"bufio"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/PizenLabs/izen/internal/graph"
	"github.com/PizenLabs/izen/internal/lynx"
)

// ── Log Deduplication & Topological Grouping ──────────────────────────────────

// ErrorNode represents a single unique compiler/test error with its dependency
// ordering metadata. It is used by the topological error sorter to surface
// root-cause errors before cascading failures.
type ErrorNode struct {
	Signature string // Deduplication key: file:line:message-hash
	File      string
	Line      int
	Message   string
	Package   string
	// IsRootCause is true when this error likely originates in this package
	// (syntax/declaration errors) vs being a cascading failure in dependents.
	IsRootCause bool
	// Order is the topological sort order (lower = earlier in dependency chain).
	Order int
}

// compilerErrorRe matches Go compiler errors of the form:
//
//	file.go:line:col: message
//	file.go:line: message
var compilerErrorRe = regexp.MustCompile(`^([^:]+\.go):(\d+)(?::(\d+))?:(.+)$`)

// packageDeclRe matches Go package declaration and import errors, which
// are root causes that block compilation of dependent packages.
var packageDeclRe = regexp.MustCompile(`(?i)(package\s+\w+|undefined|expected\s+ declaration|expected\s+'package'|expected\s+'}'|expected\s+')`)

// LogDeduplicator implements a deterministic log error pre-processor that
// deduplicates repetitive compiler errors down to single distinct error nodes,
// then sorts them topologically so root-cause errors appear before cascading
// failures.
type LogDeduplicator struct {
	errors          []ErrorNode
	pkgIndex        map[string]int
	dependencyOrder []string
}

// NewLogDeduplicator creates an empty deduplicator.
func NewLogDeduplicator() *LogDeduplicator {
	return &LogDeduplicator{
		pkgIndex: make(map[string]int),
	}
}

// Feed processes a raw log/output line, extracting compiler errors.
func (ld *LogDeduplicator) Feed(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}

	m := compilerErrorRe.FindStringSubmatch(line)
	if m == nil {
		// Not a structured compiler error — try to extract plain errors.
		if strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "  ") {
			// Indented continuation lines from test output.
			ld.addIfDistinct(line, "", 0, line)
		}
		return
	}

	file := m[1]
	lineNum, _ := strconv.Atoi(m[2])
	message := strings.TrimSpace(m[4])

	pkg := extractPackagePath(file)

	// Check if this is a root-cause error (package declaration/fatal syntax).
	isRoot := packageDeclRe.MatchString(message)

	ld.addIfDistinct(file+":"+m[2]+":"+hashMessage(message), file, lineNum, message)

	if pkg != "" && isRoot {
		if _, exists := ld.pkgIndex[pkg]; !exists {
			ld.pkgIndex[pkg] = len(ld.dependencyOrder)
			ld.dependencyOrder = append(ld.dependencyOrder, pkg)
		}
	}
}

// addIfDistinct inserts an ErrorNode only if its dedup key is new.
func (ld *LogDeduplicator) addIfDistinct(key, file string, line int, message string) {
	for _, e := range ld.errors {
		if e.Signature == key {
			return
		}
	}
	pkg := extractPackagePath(file)
	isRoot := packageDeclRe.MatchString(message)
	ld.errors = append(ld.errors, ErrorNode{
		Signature:   key,
		File:        file,
		Line:        line,
		Message:     message,
		Package:     pkg,
		IsRootCause: isRoot,
	})
}

// Deduplicate returns the deduplicated error nodes, sorted topologically:
// package declaration / root syntax errors first, then cascading failures.
func (ld *LogDeduplicator) Deduplicate() []ErrorNode {
	if len(ld.errors) == 0 {
		return nil
	}

	// Mark dependency order: root causes tied to their package get order 0.
	for i, e := range ld.errors {
		ld.errors[i].Order = i + 1
		if e.IsRootCause {
			if idx, ok := ld.pkgIndex[e.Package]; ok {
				ld.errors[i].Order = idx
			} else {
				ld.errors[i].Order = 0
			}
		}
	}

	// Sort by order (topological), then by file+line for determinism.
	sort.SliceStable(ld.errors, func(i, j int) bool {
		if ld.errors[i].Order != ld.errors[j].Order {
			return ld.errors[i].Order < ld.errors[j].Order
		}
		if ld.errors[i].File != ld.errors[j].File {
			return ld.errors[i].File < ld.errors[j].File
		}
		return ld.errors[i].Line < ld.errors[j].Line
	})

	return ld.errors
}

// TopologicalErrorFormatter formats deduplicated error nodes as a compact
// context string suitable for LLM injection.
type TopologicalErrorFormatter struct{}

func NewTopologicalErrorFormatter() *TopologicalErrorFormatter {
	return &TopologicalErrorFormatter{}
}

// Format renders the error list in topological order with root causes first.
// It strips all but the absolute source positions for direct injection into
// the lynx resolve pipeline.
func (f *TopologicalErrorFormatter) Format(errors []ErrorNode) string {
	if len(errors) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("### TOPOLOGICALLY SORTED COMPILATION ERRORS\n")
	b.WriteString("(Root cause errors first, cascading failures below)\n\n")

	rootCauses := 0
	for _, e := range errors {
		if e.IsRootCause {
			rootCauses++
		}
	}

	if rootCauses > 0 {
		b.WriteString("--- ROOT CAUSE ERRORS ---\n")
		for _, e := range errors {
			if !e.IsRootCause {
				continue
			}
			fmt.Fprintf(&b, "%s:%d %s\n", e.File, e.Line, e.Message)
		}
		b.WriteString("\n")
	}

	if rootCauses < len(errors) {
		b.WriteString("--- CASCADING ERRORS ---\n")
		for _, e := range errors {
			if e.IsRootCause {
				continue
			}
			fmt.Fprintf(&b, "%s:%d %s\n", e.File, e.Line, e.Message)
		}
	}

	return b.String()
}

// ExtractSourcePositions returns only the file:line positions for root cause
// errors, for direct piping into the lynx resolve pipeline.
func ExtractSourcePositions(errors []ErrorNode) []string {
	var positions []string
	seen := make(map[string]bool)
	for _, e := range errors {
		key := fmt.Sprintf("%s:%d", e.File, e.Line)
		if seen[key] {
			continue
		}
		seen[key] = true
		positions = append(positions, key)
	}
	return positions
}

// hashMessage produces a short deterministic hash of an error message for
// deduplication purposes. It strips line numbers and memory addresses so that
// structurally identical errors map to the same key.
func hashMessage(msg string) string {
	// Normalise: strip quoted paths and hex addresses.
	normalised := msg
	normalised = regexp.MustCompile(`0x[0-9a-fA-F]+`).ReplaceAllString(normalised, "")
	normalised = regexp.MustCompile(`"[^"]*"`).ReplaceAllString(normalised, "")
	if len(normalised) > 120 {
		normalised = normalised[:120]
	}
	return normalised
}

// extractPackagePath returns the Go package path from a file path by scanning
// for well-known directory prefixes.
func extractPackagePath(file string) string {
	parts := strings.Split(file, "/")
	for i, part := range parts {
		if part == "internal" || part == "pkg" || part == "cmd" {
			if i+1 < len(parts) {
				return strings.Join(parts[:i+2], "/")
			}
		}
	}
	// Fallback: use the directory of the first two path components.
	if len(parts) >= 2 {
		return parts[0]
	}
	return ""
}

// ── End Log Deduplication ─────────────────────────────────────────────────────

type acNode struct {
	children map[rune]int
	fail     int
	output   []int
}

type AhoCorasick struct {
	trie     []acNode
	patterns []string
}

func NewAhoCorasick(patterns []string) *AhoCorasick {
	ac := &AhoCorasick{}
	ac.patterns = patterns

	ac.trie = make([]acNode, 1)
	ac.trie[0].children = make(map[rune]int)

	for id, pat := range patterns {
		node := 0
		for _, ch := range pat {
			if next, ok := ac.trie[node].children[ch]; ok {
				node = next
			} else {
				ac.trie = append(ac.trie, acNode{children: make(map[rune]int)})
				next = len(ac.trie) - 1
				ac.trie[node].children[ch] = next
				node = next
			}
		}
		ac.trie[node].output = append(ac.trie[node].output, id)
	}

	queue := make([]int, 0)
	for _, next := range ac.trie[0].children {
		ac.trie[next].fail = 0
		queue = append(queue, next)
	}

	for len(queue) > 0 {
		r := queue[0]
		queue = queue[1:]
		for ch, u := range ac.trie[r].children {
			queue = append(queue, u)
			v := ac.trie[r].fail
			for v != 0 {
				if next, ok := ac.trie[v].children[ch]; ok {
					v = next
					goto link
				}
				v = ac.trie[v].fail
			}
			if next, ok := ac.trie[0].children[ch]; ok {
				v = next
			}
		link:
			ac.trie[u].fail = v
			ac.trie[u].output = append(ac.trie[u].output, ac.trie[v].output...)
		}
	}

	return ac
}

func (ac *AhoCorasick) MatchAny(text string) bool {
	node := 0
	for _, ch := range text {
		for node != 0 {
			if _, ok := ac.trie[node].children[ch]; ok {
				break
			}
			node = ac.trie[node].fail
		}
		if next, ok := ac.trie[node].children[ch]; ok {
			node = next
		}
		if len(ac.trie[node].output) > 0 {
			return true
		}
	}
	return false
}

type ContextCompressor struct {
	matcher *AhoCorasick
}

func NewContextCompressorFromGraph(g *graph.Graph, objective string) *ContextCompressor {
	patterns := extractPatterns(g, objective)
	return &ContextCompressor{
		matcher: NewAhoCorasick(patterns),
	}
}

func extractPatterns(g *graph.Graph, objective string) []string {
	seen := make(map[string]bool)
	var patterns []string

	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] || len(s) < 2 {
			return
		}
		seen[s] = true
		patterns = append(patterns, s)
	}

	if g != nil {
		for _, fn := range g.Files {
			add(fn.Package)
			for _, sym := range fn.Symbols {
				add(sym.Name)
				if sym.Signature != "" {
					add(sym.Signature)
				}
			}
			for _, imp := range fn.Imports {
				parts := strings.Split(imp, "/")
				add(parts[len(parts)-1])
			}
		}
	}

	if objective != "" {
		for _, tok := range strings.Fields(objective) {
			tok = strings.Trim(tok, ".,:;!?()[]{}")
			if len(tok) >= 3 {
				add(tok)
			}
		}
	}

	goKeywords := []string{
		"func ", "type ", "struct", "interface", "const ", "var ",
		"package ", "import ", "return ", "nil", "defer ", "go ",
		"chan ", "map[", "[]", "...", "error", "bool", "string",
		"int", "int64", "float64", "byte", "rune", "uintptr",
		"make(", "new(", "append(", "len(", "cap(", "copy(",
		"func (", ") error", "error)", "interface {", "struct {",
	}
	for _, kw := range goKeywords {
		add(kw)
	}

	rustKeywords := []string{
		"fn ", "pub ", "impl ", "trait ", "enum ", "struct ",
		"let ", "mut ", "async ", "await ", "unsafe ", "match ",
		"use ", "mod ", "where ", "dyn ", "self", "&self",
		"Result<", "Option<", "Box<", "Arc<", "Rc<",
		"fn (", "-> ", "=> ", "::", "_ =>",
	}
	for _, kw := range rustKeywords {
		add(kw)
	}

	pythonKeywords := []string{
		"def ", "class ", "import ", "from ", "return ",
		"async def", "await ", "self", "-> ",
		"__init__", "__call__", "__str__", "__repr__",
		"if __name__", "except ", "raise ", "yield ",
		"with ", "as ", "lambda ", "pass", "None",
		"True", "False", "TypeVar", "Generic[", "Optional[",
	}
	for _, kw := range pythonKeywords {
		add(kw)
	}

	sort.Strings(patterns)
	return patterns
}

func (cc *ContextCompressor) CompressLines(text string) string {
	if text == "" || cc.matcher == nil {
		return text
	}

	var out strings.Builder
	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := scanner.Text()
		if cc.matcher.MatchAny(line) {
			if out.Len() > 0 {
				out.WriteByte('\n')
			}
			out.WriteString(line)
		}
	}
	return out.String()
}

func (cc *ContextCompressor) CompressResults(results []lynx.SearchResult) []lynx.SearchResult {
	if cc.matcher == nil || len(results) == 0 {
		return results
	}
	compressed := make([]lynx.SearchResult, len(results))
	for i, r := range results {
		compressed[i] = r
		compressed[i].Content = cc.CompressLines(r.Content)
	}
	return compressed
}

const HighDensityFrame = "### HIGH-DENSITY COMPRESSED CONTEXT MATRIX"

func FormatCompressedFrame(content string) string {
	if content == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString(HighDensityFrame)
	b.WriteString("\n")
	b.WriteString(content)
	b.WriteString("\n")
	return b.String()
}

const PlanInstructions = `You are the Execution Planning Engine for Izen. Your job is to output absolute, concrete step-by-step tasks based ONLY on the provided repository structural skeleton.

CRITICAL INSTRUCTIONS:
1. **Zero Speculation:** Identify the primary programming language from the provided skeleton. If the project contains .go files or Go packages, the entire plan MUST use Go tooling (go get, go build). Absolutely FORBIDDEN to mention Node.js, npm, Python, or any external stack not visible in the skeleton.
2. **Absolute File Grounding:** Every FILE_MUTATE task description must reference exact file targets or structural layouts present in the Repository Skeleton. Do not invent boilerplate directory trees (like src/auth.js for a Go repo).
3. **Action-Oriented Output:** Tasks must be immediately actionable via the /build engine. Keep descriptions concise, factual, and bound to the active workspace.`

const PlanSkeletonFrame = "### REPOSITORY STRUCTURAL SKELETON (LYNX + AHO-CORASICK COMPRESSED)"

func FormatPlanFrame(content string) string {
	if content == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString(PlanInstructions)
	b.WriteString("\n\n")
	b.WriteString(PlanSkeletonFrame)
	b.WriteString("\n")
	b.WriteString(content)
	return b.String()
}

func FormatResultsAsSkeleton(results []lynx.SearchResult) string {
	var b strings.Builder
	for _, r := range results {
		if r.Content == "" {
			continue
		}
		fmt.Fprintf(&b, "%s:%d\n", r.FilePath, r.StartLine)
		if r.SymbolName != "" {
			fmt.Fprintf(&b, "  %s\n", r.SymbolName)
		}
		for _, line := range strings.Split(r.Content, "\n") {
			if line != "" {
				fmt.Fprintf(&b, "  %s\n", line)
			}
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
