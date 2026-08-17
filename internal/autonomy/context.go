package autonomy

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

// ArtifactKind is the structural category of a compiled artifact.
type ArtifactKind string

const (
	KindHTML ArtifactKind = "html"
	KindCode ArtifactKind = "code"
	KindText ArtifactKind = "text"
)

// FindingSeverity ranks the evidence strength of a finding.
type FindingSeverity string

const (
	SeverityInfo  FindingSeverity = "info"
	SeverityWarn  FindingSeverity = "warning"
	SeverityError FindingSeverity = "error"
)

// Finding is one piece of structural evidence about an artifact. The context
// intelligence layer produces findings — it does not produce raw text.
type Finding struct {
	Type     string          `json:"type"`
	Severity FindingSeverity `json:"severity"`
	Line     int             `json:"line,omitempty"`
	Detail   string          `json:"detail"`
}

// SemanticBlock is a structural region of an HTML document with a purpose.
type SemanticBlock struct {
	Tag      string `json:"tag"`
	ID       string `json:"id,omitempty"`
	Class    string `json:"class,omitempty"`
	Line     int    `json:"line"`
	TextHint string `json:"text_hint,omitempty"`
}

// HTMLUnderstanding is the structural reading of an HTML artifact: its
// semantic blocks, orphan content, and invalid regions.
type HTMLUnderstanding struct {
	Blocks        []SemanticBlock `json:"blocks"`
	OrphanContent []Finding       `json:"orphan_content,omitempty"`
	InvalidRegion []Finding       `json:"invalid_regions,omitempty"`
	ElementCounts map[string]int  `json:"element_counts"`
}

// CodeSymbol is a declaration extracted from a code artifact.
type CodeSymbol struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Line     int    `json:"line"`
	Language string `json:"language"`
}

// CodeUnderstanding is the structural reading of a code artifact: its symbols,
// dependencies, and affected scope.
type CodeUnderstanding struct {
	Symbols       []CodeSymbol `json:"symbols"`
	Dependencies  []string     `json:"dependencies"`
	AffectedScope []string     `json:"affected_scope"`
	Findings      []Finding    `json:"findings,omitempty"`
}

// ArtifactContext is the compiled understanding of a single artifact. It is the
// "structural understanding -> semantic context" tier of the context
// intelligence layer: consumers reason over findings, not byte slices.
type ArtifactContext struct {
	Path     string             `json:"path"`
	Kind     ArtifactKind       `json:"kind"`
	Size     int                `json:"size"`
	HTML     *HTMLUnderstanding `json:"html,omitempty"`
	Code     *CodeUnderstanding `json:"code,omitempty"`
	Findings []Finding          `json:"findings,omitempty"`
}

// Evidence returns the aggregate findings: artifact-level findings plus any
// kind-specific findings. Consumers reason over this evidence vector, never
// over raw text.
func (a ArtifactContext) Evidence() []Finding {
	if a.HTML != nil {
		all := append([]Finding(nil), a.Findings...)
		all = append(all, a.HTML.OrphanContent...)
		all = append(all, a.HTML.InvalidRegion...)
		return all
	}
	if a.Code != nil {
		all := append([]Finding(nil), a.Findings...)
		all = append(all, a.Code.Findings...)
		return all
	}
	return a.Findings
}

// FormatEvidenceLedger renders the compiled structural understanding as a
// compact Context Evidence Ledger — the deterministic evidence the runtime
// hands the model BEFORE it is asked to interpret, diagnose or propose. The
// model never discovers structural facts on its own; it reasons over this
// ledger. The output stays under ~100 tokens so it fits any model budget.
func (a ArtifactContext) FormatEvidenceLedger() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Context Evidence Ledger\nTarget: %s\n", a.Path)
	ev := a.Evidence()
	if len(ev) == 0 {
		b.WriteString("Structural findings: none\n")
		return strings.TrimSpace(b.String())
	}
	b.WriteString("Structural findings:\n")
	limit := 8
	for i, f := range ev {
		if i >= limit {
			b.WriteString("* ... more findings omitted\n")
			break
		}
		line := ""
		if f.Line > 0 {
			line = fmt.Sprintf(" at line %d", f.Line)
		}
		fmt.Fprintf(&b, "* %s%s — %s\n", f.Type, line, truncateForContext(f.Detail, 80))
	}
	return strings.TrimSpace(b.String())
}

// KindOf infers the artifact kind from its file extension.
func KindOf(path string) ArtifactKind {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".html", ".htm":
		return KindHTML
	case ".go", ".py", ".rs", ".java", ".js", ".jsx", ".ts", ".tsx",
		".c", ".cc", ".cpp", ".h", ".hpp", ".rb", ".php", ".sh", ".sql":
		return KindCode
	default:
		return KindText
	}
}

// CompileContext builds the structural understanding of an artifact from its
// raw content. It never reads disk: the caller owns file access. The compiler
// degrades gracefully — a parse failure yields findings, never an error.
func CompileContext(path, content string) ArtifactContext {
	kind := KindOf(path)
	ctx := ArtifactContext{
		Path: path,
		Kind: kind,
		Size: len(content),
	}
	switch kind {
	case KindHTML:
		ctx.HTML = compileHTML(content)
	case KindCode:
		ctx.Code = compileCode(path, content)
	default:
		ctx.Findings = textFindings(content)
	}
	return ctx
}

// ── HTML ────────────────────────────────────────────────────────────────────

var semanticTags = map[string]bool{
	"header": true, "nav": true, "main": true, "section": true,
	"article": true, "aside": true, "footer": true, "div": true,
	"form": true, "table": true, "ul": true, "ol": true, "p": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"figure": true, "figcaption": true, "blockquote": true,
}

// compileHTML walks the parsed document and produces structural evidence.
func compileHTML(content string) *HTMLUnderstanding {
	u := &HTMLUnderstanding{ElementCounts: make(map[string]int)}
	doc, err := html.Parse(strings.NewReader(content))
	if err != nil {
		u.InvalidRegion = append(u.InvalidRegion, Finding{
			Type: "html.parse_error", Severity: SeverityError, Detail: err.Error(),
		})
		return u
	}

	var walk func(n *html.Node, depth int)
	walk = func(n *html.Node, depth int) {
		switch n.Type {
		case html.ElementNode:
			tag := n.Data
			u.ElementCounts[tag]++
			if semanticTags[tag] {
				blk := SemanticBlock{Tag: tag, Line: nodeLine(n, content)}
				for _, a := range n.Attr {
					switch a.Key {
					case "id":
						blk.ID = a.Val
					case "class":
						blk.Class = a.Val
					}
				}
				blk.TextHint = textHint(n)
				u.Blocks = append(u.Blocks, blk)
			}
		case html.TextNode:
			// Orphan content: meaningful text not nested inside a semantic
			// block container. "Meaningful" means more than whitespace.
			text := strings.TrimSpace(n.Data)
			if text == "" {
				break
			}
			// Depth <= 3 means the text sits directly under <html>/<body>
			// (document(0) -> html(1) -> body(2) -> text(3)).
			if depth <= 3 && !isInsideSemanticBlock(n) {
				u.OrphanContent = append(u.OrphanContent, Finding{
					Type:     "html.orphan_text",
					Severity: SeverityWarn,
					Line:     nodeLine(n, content),
					Detail:   fmt.Sprintf("orphan text node: %q", truncateForContext(text, 60)),
				})
			}
		}
		// Descend into children for EVERY node type (the document root is a
		// DocumentNode, not an ElementNode).
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, depth+1)
		}
	}
	walk(doc, 0)

	// Invalid regions: mismatched tag counts. Void elements are excluded.
	u.InvalidRegion = append(u.InvalidRegion, unbalancedTags(content)...)
	return u
}

var voidTags = map[string]bool{
	"br": true, "hr": true, "img": true, "input": true, "link": true,
	"meta": true, "area": true, "base": true, "col": true, "embed": true,
	"param": true, "source": true, "track": true, "wbr": true,
}

var tagPattern = regexp.MustCompile(`</?\s*([a-zA-Z][a-zA-Z0-9]*)`)

// unbalancedTags finds tags whose open/close occurrences disagree.
func unbalancedTags(content string) []Finding {
	opens := make(map[string]int)
	closes := make(map[string]int)
	for _, m := range tagPattern.FindAllStringSubmatch(content, -1) {
		if len(m) < 2 {
			continue
		}
		tag := strings.ToLower(m[1])
		if voidTags[tag] {
			continue
		}
		// The matched token includes the leading "</" or "<".
		tok := m[0]
		if strings.HasPrefix(tok, "</") {
			closes[tag]++
		} else {
			opens[tag]++
		}
	}
	var findings []Finding
	for tag, n := range opens {
		if closes[tag] < n {
			findings = append(findings, Finding{
				Type:     "html.unclosed_tag",
				Severity: SeverityError,
				Detail:   fmt.Sprintf("<%s> opened %d time(s) but closed %d time(s)", tag, n, closes[tag]),
			})
		}
	}
	return findings
}

// isInsideSemanticBlock reports whether any ancestor is a semantic block tag.
func isInsideSemanticBlock(n *html.Node) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && semanticTags[p.Data] {
			return true
		}
	}
	return false
}

// textHint captures the first meaningful text inside a block.
func textHint(n *html.Node) string {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			if t := strings.TrimSpace(c.Data); t != "" {
				return truncateForContext(t, 48)
			}
		}
	}
	return ""
}

// nodeLine locates the source line of an element node by searching the content
// for its opening tag. The html.Node does not carry byte positions, so this is
// a best-effort scan that degrades to line 1.
func nodeLine(n *html.Node, content string) int {
	if n == nil || n.Type != html.ElementNode || len(content) == 0 {
		return 1
	}
	needle := "<" + n.Data
	idx := strings.Index(content, needle)
	if idx < 0 {
		return 1
	}
	return strings.Count(content[:idx], "\n") + 1
}

func truncateForContext(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ── Code ────────────────────────────────────────────────────────────────────

var (
	goFuncRe   = regexp.MustCompile(`(?m)^\s*func\s+(\([^)]*\)\s*)?([A-Za-z_][A-Za-z0-9_]*)`)
	goTypeRe   = regexp.MustCompile(`(?m)^\s*type\s+([A-Za-z_][A-Za-z0-9_]*)\s+(struct|interface)`)
	pyDefRe    = regexp.MustCompile(`(?m)^\s*def\s+([A-Za-z_][A-Za-z0-9_]*)`)
	pyClassRe  = regexp.MustCompile(`(?m)^\s*class\s+([A-Za-z_][A-Za-z0-9_]*)`)
	jsFuncRe   = regexp.MustCompile(`(?m)^\s*(?:export\s+)?(?:function|const|let|var)\s+([A-Za-z_$][A-Za-z0-9_$]*)`)
	jsClassRe  = regexp.MustCompile(`(?m)^\s*(?:export\s+)?class\s+([A-Za-z_$][A-Za-z0-9_$]*)`)
	importGoRe = regexp.MustCompile(`(?m)"([^"]+)"`)
	importPyRe = regexp.MustCompile(`(?m)^\s*(?:from\s+([\w.]+)\s+import|import\s+([\w.]+))`)
	importJsRe = regexp.MustCompile(`(?m)^\s*(?:import|from)\s+['"]([^'"]+)['"]|import\s+(\w+)\s+from\s+['"]([^'"]+)['"]`)
)

// compileCode extracts symbols, dependencies and affected scope from a code
// artifact using lightweight structural extraction. It is deliberately
// conservative: it prefers precision over recall so the findings never
// fabricate symbols.
func compileCode(path, content string) *CodeUnderstanding {
	lang := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	u := &CodeUnderstanding{}
	lines := strings.Split(content, "\n")

	var funcRe, typeRe *regexp.Regexp
	switch lang {
	case "go":
		funcRe, typeRe = goFuncRe, goTypeRe
	case "py":
		funcRe, typeRe = pyDefRe, pyClassRe
	case "js", "jsx", "ts", "tsx":
		funcRe, typeRe = jsFuncRe, jsClassRe
	default:
		funcRe, typeRe = nil, nil
	}

	for i, line := range lines {
		if funcRe != nil {
			if m := funcRe.FindStringSubmatch(line); m != nil {
				name := lastNonEmpty(m[1:])
				if name != "" {
					u.Symbols = append(u.Symbols, CodeSymbol{Name: name, Kind: "function", Line: i + 1, Language: lang})
				}
			}
		}
		if typeRe != nil {
			if m := typeRe.FindStringSubmatch(line); m != nil {
				if name := m[1]; name != "" {
					kind := "type"
					if lang == "js" || lang == "jsx" || lang == "ts" || lang == "tsx" {
						kind = "class"
					}
					u.Symbols = append(u.Symbols, CodeSymbol{Name: name, Kind: kind, Line: i + 1, Language: lang})
				}
			}
		}
	}

	u.Dependencies = extractImports(lang, content, lines)
	u.AffectedScope = affectedScope(u.Symbols, lines)
	return u
}

func lastNonEmpty(parts []string) string {
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return ""
}

func extractImports(lang, content string, lines []string) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	switch lang {
	case "go":
		for _, l := range lines {
			t := strings.TrimSpace(l)
			if strings.HasPrefix(t, "import") || strings.Contains(t, `"`) {
				for _, m := range importGoRe.FindAllStringSubmatch(l, -1) {
					if len(m) > 1 {
						add(m[1])
					}
				}
			}
		}
	case "py":
		for _, l := range lines {
			for _, m := range importPyRe.FindAllStringSubmatch(l, -1) {
				for _, part := range m[1:] {
					add(part)
				}
			}
		}
	case "js", "jsx", "ts", "tsx":
		for _, l := range lines {
			for _, m := range importJsRe.FindAllStringSubmatch(l, -1) {
				for _, part := range m[1:] {
					add(part)
				}
			}
		}
	}
	return out
}

// affectedScope maps each top-level symbol to its approximate source range
// (next symbol start minus one). This gives the planner a "what would this
// mutation touch" answer without a full parser.
func affectedScope(symbols []CodeSymbol, lines []string) []string {
	if len(symbols) == 0 {
		return nil
	}
	total := len(lines)
	var out []string
	for i, s := range symbols {
		end := total
		if i+1 < len(symbols) {
			end = symbols[i+1].Line - 1
		}
		out = append(out, fmt.Sprintf("%s:%d-%d", s.Name, s.Line, end))
	}
	return out
}

// ── Text ────────────────────────────────────────────────────────────────────

// textFindings produces lightweight evidence for non-HTML/non-code artifacts.
func textFindings(content string) []Finding {
	lineCount := len(strings.Split(content, "\n"))
	var findings []Finding
	if lineCount > 0 {
		findings = append(findings, Finding{
			Type: "text.lines", Severity: SeverityInfo, Detail: fmt.Sprintf("%d lines", lineCount),
		})
	}
	for _, c := range content {
		if !unicode.IsPrint(c) && !unicode.IsSpace(c) && c != '\n' && c != '\r' && c != '\t' {
			findings = append(findings, Finding{
				Type: "text.non_printable", Severity: SeverityWarn,
				Detail: "artifact contains non-printable characters",
			})
			break
		}
	}
	return findings
}
