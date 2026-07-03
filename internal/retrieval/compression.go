package retrieval

import (
	"bufio"
	"fmt"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/internal/graph"
	"github.com/PizenLabs/izen/internal/lynx"
)

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
		b.WriteString(fmt.Sprintf("%s:%d\n", r.FilePath, r.StartLine))
		if r.SymbolName != "" {
			b.WriteString(fmt.Sprintf("  %s\n", r.SymbolName))
		}
		for _, line := range strings.Split(r.Content, "\n") {
			if line != "" {
				b.WriteString(fmt.Sprintf("  %s\n", line))
			}
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
