package planner

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ── Lea structural scan (read-only AST/DOM analysis) ────────────────────────
//
// Syntactic splitting cuts documents at LINE boundaries: a top-level tag,
// declaration or heading. That is blind partitioning — a sub-task scoped to
// "lines 38–76" carries no semantic identity, so its model reasons over an
// arbitrary byte window and, lacking any document topology, claims
// NO_CHANGES_REQUIRED against content it never understood.
//
// LeaStructuralScan is the semantic alternative: a READ-ONLY parse of the
// structured formats (HTML, JSX/TSX components, Go templates) that produces
//
//   - the document TOPOLOGY as a node tree (tag identity, id/class, line
//     region, parent/child relations) — the skeleton the runtime's context
//     compressor later renders into sub-task prompts;
//   - SEMANTIC UNITS: candidate modification regions that are structural
//     nodes ("<head> metadata", "<section#hero> hero"), never raw line ranges;
//   - targeted FINDINGS: unused elements, duplicate selectors and dead code
//     paths — deterministic candidate modification regions.
//
// The scan is heuristic and fail-open BY DESIGN: malformed input yields a
// best-effort report flagged LowConfidence=true so the caller falls back to
// the syntactic splitters instead of partitioning along broken structure.

// FindingKind classifies one Lea structural finding.
type FindingKind string

const (
	// FindingUnusedElement: an element carrying an id that nothing else in
	// the document references (no style rule, anchor or script hook).
	FindingUnusedElement FindingKind = "unused_element"
	// FindingDuplicateSelector: the same id is bound to multiple elements,
	// or an identical CSS selector rule is declared more than once.
	FindingDuplicateSelector FindingKind = "duplicate_selector"
	// FindingDeadCodePath: commented-out markup or a constant-false template
	// branch — content reachable by no render path.
	FindingDeadCodePath FindingKind = "dead_code_path"
)

// String returns the canonical finding label.
func (k FindingKind) String() string { return string(k) }

// StructuralFinding is one deterministic candidate modification region
// produced by LeaStructuralScan. Findings are evidence, not verdicts: they
// travel to sub-task models as bounded pointers, never as mutations.
type StructuralFinding struct {
	// Kind classifies the finding.
	Kind FindingKind
	// Label is the bounded identity of the finding ("#sidebar").
	Label string
	// Detail is the one-line deterministic rationale.
	Detail string
	// Region is the inclusive 1-indexed line window the finding occupies.
	Region Region
}

// String renders the compact evidence line for one finding.
func (f StructuralFinding) String() string {
	return fmt.Sprintf("%s %s [%s]: %s", f.Kind, f.Label, f.Region, f.Detail)
}

// DOMNode is one node of the scanned document topology. Nodes form a forest:
// Parent indexes the containing node (-1 at document root level); Children
// list child positions in document order.
type DOMNode struct {
	// Tag is the lower-cased element name ("section", "head") or the
	// component/template unit name for non-HTML scans.
	Tag string
	// ID is the element's id attribute ("" when absent).
	ID string
	// Classes are the element's class tokens in declaration order.
	Classes []string
	// StartLine / EndLine bound the node inclusively (1-indexed).
	StartLine int
	EndLine   int
	// Parent is the index of the parent node in LeaScanReport.Nodes
	// (-1 when the node sits at the document root).
	Parent int
	// Children lists child node indexes in document order.
	Children []int
	// Depth is 0 for root-level nodes.
	Depth int
}

// CSSSelector renders the node identity in CSS-selector shorthand
// ("section.hero", "div#nav").
func (n DOMNode) CSSSelector() string {
	sel := n.Tag
	if n.ID != "" {
		sel += "#" + n.ID
	}
	if len(n.Classes) > 0 {
		sel += "." + strings.Join(n.Classes, ".")
	}
	return sel
}

// ActiveReference records where an id/class defined in the topology is USED
// elsewhere in the document (style rule, href anchor, script lookup). It is
// the dependency edge set of the compressed structural context.
type ActiveReference struct {
	// Name is the referenced token (without "#"/"." prefix).
	Name string
	// Kind is "id" or "class".
	Kind string
	// UsedAt lists the 1-indexed lines of referencing sites.
	UsedAt []int
}

// LeaScanReport is the read-only structural analysis of ONE structured file.
// A nil report means the format has no Lea scanner; LowConfidence marks a
// parse that recovered from malformed structure and must not drive splitting
// (low_semantic_confidence ⇒ syntactic fallback).
type LeaScanReport struct {
	// Format is the canonical format label ("html", "jsx", "go_template").
	Format string
	// LowConfidence reports that parsing hit malformed structure; the caller
	// must fall back to syntactic splitting.
	LowConfidence bool
	// Units are the semantic decomposition candidates covering the document.
	Units []Section
	// Nodes is the flattened topology forest (depth-first, document order).
	Nodes []DOMNode
	// Findings are the deterministic candidate modification regions.
	Findings []StructuralFinding
	// References are the active id/class usage edges.
	References []ActiveReference
	// TotalLines is the document line count.
	TotalLines int
}

// leaScanFormat maps a structured extension onto its canonical format label.
func leaScanFormat(target string) string {
	switch strings.ToLower(filepath.Ext(target)) {
	case ".html", ".htm", ".xhtml":
		return "html"
	case ".jsx", ".tsx":
		return "jsx"
	case ".gohtml", ".tmpl", ".gotmpl", ".gotemplate":
		return "go_template"
	default:
		return ""
	}
}

// LeaScannable reports whether the target has a Lea structural scanner.
func LeaScannable(target string) bool {
	return leaScanFormat(target) != ""
}

// LeaStructuralScan performs the read-only AST/DOM parse of one structured
// target. It never mutates anything and never reads the workspace: source
// bytes are provided by the caller. Unsupported formats return nil; malformed
// content returns a best-effort report with LowConfidence=true.
func LeaStructuralScan(target string, source []byte) *LeaScanReport {
	format := leaScanFormat(target)
	if format == "" || len(strings.TrimSpace(string(source))) == 0 {
		return nil
	}
	lines := splitKeepNewline(source)
	rep := &LeaScanReport{Format: format, TotalLines: len(lines)}
	switch format {
	case "html":
		rep.Nodes = parseHTMLTopology(lines, rep)
	case "jsx":
		rep.Nodes = parseComponentTopology(lines)
	case "go_template":
		rep.Nodes = parseTemplateTopology(lines, rep)
	}
	rep.Units = semanticUnits(rep, lines)
	rep.References = collectReferences(lines, rep.Nodes)
	rep.Findings = detectFindings(rep, lines)
	return rep
}

// ── HTML DOM topology ───────────────────────────────────────────────────────

// rawTextElements hold opaque character data until their own close tag:
// everything inside them (including "<div>" look-alikes) is text.
var rawTextElements = map[string]bool{
	"script": true, "style": true, "textarea": true, "title": true,
}

// parseHTMLTopology walks the tag stream and builds the element forest.
// Structural wrappers (html/head/body) become ordinary nodes so the skeleton
// can show them; void/self-closing elements and raw-text elements are handled
// without nesting. Mismatched close tags and unclosed elements flag
// LowConfidence on the report — the topology still covers what was salvaged.
func parseHTMLTopology(lines [][]byte, rep *LeaScanReport) []DOMNode {
	type frame struct {
		idx     int    // node index of the open element
		tag     string // lower-cased element name
		rawText string // non-empty while inside a raw-text element
	}
	var (
		nodes     []DOMNode
		stack     []frame
		inComment bool
		malformed bool
	)
	parentIdx := func() int {
		if len(stack) == 0 {
			return -1
		}
		return stack[len(stack)-1].idx
	}
	pushNode := func(n DOMNode) int {
		n.Parent = parentIdx()
		n.Depth = 0
		if p := n.Parent; p >= 0 {
			n.Depth = nodes[p].Depth + 1
			nodes[p].Children = append(nodes[p].Children, len(nodes))
		}
		nodes = append(nodes, n)
		return len(nodes) - 1
	}
	// closeAbove closes every open element above (and including) the frame at
	// stack position k as of lineNo. Closing through inner frames is the
	// malformed-structure recovery path and flags low confidence.
	closeAbove := func(k, lineNo int) {
		for len(stack) > k {
			top := stack[len(stack)-1]
			nodes[top.idx].EndLine = lineNo
			stack = stack[:len(stack)-1]
			if len(stack) > k {
				malformed = true // implicit close: inner element never closed
			}
		}
	}
	for i, line := range lines {
		lineNo := i + 1
		s := string(line)
		for x := 0; x < len(s); {
			switch {
			case inComment:
				end := strings.Index(s[x:], "-->")
				if end < 0 {
					x = len(s)
					continue
				}
				inComment = false
				x += end + 3
			case len(stack) > 0 && stack[len(stack)-1].rawText != "":
				rw := stack[len(stack)-1].rawText
				j := indexFold(s, "</"+rw, x)
				if j < 0 {
					x = len(s) // whole remaining line is raw text content
					continue
				}
				// Consume the ENTIRE close tag here: the raw-text element
				// leaves the stack exactly once and the scanner resumes
				// AFTER ">", so the default branch never sees a stray close.
				top := stack[len(stack)-1]
				gt := strings.IndexByte(s[j:], '>')
				stack = stack[:len(stack)-1]
				nodes[top.idx].EndLine = lineNo
				if gt < 0 {
					x = len(s) // tag name spills to the next line: resume there
					continue
				}
				x = j + gt + 1
			default:
				lt := strings.IndexByte(s[x:], '<')
				if lt < 0 {
					x = len(s)
					continue
				}
				x += lt
				rest := s[x:]
				switch {
				case strings.HasPrefix(rest, "<!--"):
					if end := strings.Index(rest, "-->"); end >= 0 {
						x += end + 3
					} else {
						inComment = true
						x = len(s)
					}
				case strings.HasPrefix(strings.ToLower(rest), "<!doctype"):
					x = len(s)
				case strings.HasPrefix(rest, "<?"):
					if end := strings.Index(rest, "?>"); end >= 0 {
						x += end + 2
					} else {
						x = len(s)
					}
				default:
					gt := strings.IndexByte(rest, '>')
					if gt < 0 {
						x = len(s) // multi-line attribute list: resume next line
						continue
					}
					interior := strings.TrimSpace(rest[1:gt])
					selfClosing := strings.HasSuffix(interior, "/")
					interior = strings.TrimSpace(strings.TrimRight(interior, "/"))
					closing := strings.HasPrefix(interior, "/")
					name := strings.ToLower(tagName(strings.TrimPrefix(interior, "/")))
					switch {
					case closing:
						found := -1
						for k := len(stack) - 1; k >= 0; k-- {
							if stack[k].tag == name {
								found = k
								break
							}
						}
						if found < 0 {
							malformed = true // stray close tag
						} else {
							closeAbove(found, lineNo)
						}
					case selfClosing || voidElements[name]:
						idx := pushNode(DOMNode{Tag: name, StartLine: lineNo, EndLine: lineNo})
						applyAttrs(&nodes[idx], interior)
					default:
						idx := pushNode(DOMNode{Tag: name, StartLine: lineNo, EndLine: lineNo})
						applyAttrs(&nodes[idx], interior)
						fr := frame{idx: idx, tag: name}
						if rawTextElements[name] {
							fr.rawText = name
						}
						stack = append(stack, fr)
					}
					x += gt + 1
				}
			}
		}
	}
	// EOF recovery: anything still open was never closed.
	if len(stack) > 0 {
		malformed = true
		for _, fr := range stack {
			nodes[fr.idx].EndLine = rep.TotalLines
		}
	}
	rep.LowConfidence = malformed
	return nodes
}

// applyAttrs extracts id/class attributes from a tag's interior text.
func applyAttrs(n *DOMNode, interior string) {
	lower := strings.ToLower(interior)
	if v := tagAttrValue(interior, lower, "id"); v != "" {
		n.ID = v
	}
	if v := tagAttrValue(interior, lower, "class"); v != "" {
		n.Classes = strings.Fields(v)
	}
}

// tagAttrValue pulls one attribute's value out of a tag interior
// (quote-tolerant; unquoted values terminate at whitespace or slash-gt).
func tagAttrValue(interior, lower, name string) string {
	i := strings.Index(lower, name+"=")
	if i < 0 {
		return ""
	}
	rest := strings.TrimSpace(interior[i+len(name)+1:])
	if rest == "" {
		return ""
	}
	if q := rest[0]; q == '"' || q == '\'' {
		if end := strings.IndexByte(rest[1:], q); end >= 0 {
			return rest[1 : end+1]
		}
		return strings.TrimSpace(rest[1:])
	}
	field := strings.Fields(rest)
	if len(field) == 0 {
		return ""
	}
	return strings.TrimSuffix(field[0], "/>")
}

// ── JSX / TSX component topology ────────────────────────────────────────────

// parseComponentTopology reuses the column-zero declaration scanner and lifts
// each function/class/type declaration into a component node. Components are
// the semantic units of a JSX tree: each is a self-contained render path.
func parseComponentTopology(lines [][]byte) []DOMNode {
	starts := astDeclStarts(lines, tsDeclPrefixes)
	nodes := make([]DOMNode, 0, len(starts))
	for i, ds := range starts {
		end := len(lines)
		if i+1 < len(starts) {
			end = starts[i+1].start
		}
		label := declLabel(lines[ds.decl])
		nodes = append(nodes, DOMNode{
			Tag:       componentName(label),
			StartLine: ds.start + 1,
			EndLine:   end,
			Parent:    -1,
		})
	}
	return nodes
}

// componentName extracts the identifier of a TS/JSX declaration line.
func componentName(label string) string {
	fields := strings.Fields(strings.TrimRight(label, "{( \t\r\n"))
	for i, tok := range fields {
		switch tok {
		case "export", "default", "declare", "abstract", "async",
			"function", "class", "interface", "const", "let", "var", "type":
			continue
		}
		if i > 0 || len(fields) == 1 {
			return tok
		}
	}
	if len(fields) > 0 {
		return fields[len(fields)-1]
	}
	return "(unnamed)"
}

// ── Go template topology ────────────────────────────────────────────────────

// parseTemplateTopology lifts {{define "name"}} blocks into template nodes and
// records constant-false {{if}} branches as dead code paths — including
// branches nested inside define blocks. Unclosed constructs flag low
// confidence; everything outside define blocks stays in the header/tail zones.
func parseTemplateTopology(lines [][]byte, rep *LeaScanReport) []DOMNode {
	type frame struct {
		define  bool
		ifFalse bool
		node    int // node index of the open define block (-1 otherwise)
		start   int // opening line of an if-frame
	}
	var (
		nodes []DOMNode
		stack []frame
	)
	pushNode := func(tag string, lineNo int) int {
		nodes = append(nodes, DOMNode{Tag: tag, StartLine: lineNo, EndLine: len(lines), Parent: -1})
		return len(nodes) - 1
	}
	for i, line := range lines {
		lineNo := i + 1
		for _, act := range templateActionsOn(line) {
			switch act.kind {
			case tplDefine:
				stack = append(stack, frame{define: true, node: pushNode("define:"+act.name, lineNo), start: lineNo})
			case tplIfFalse:
				stack = append(stack, frame{ifFalse: true, node: -1, start: lineNo})
			case tplIfTrue:
				stack = append(stack, frame{node: -1, start: lineNo})
			case tplEnd:
				if len(stack) == 0 {
					break // stray end: tolerated, structure already broken
				}
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				switch {
				case top.define:
					nodes[top.node].EndLine = lineNo
				case top.ifFalse:
					// Only the OUTERMOST false branch is reported: any inner
					// dead branch is subsumed by its dead parent.
					subsumed := false
					for _, f := range stack {
						if !f.define && f.ifFalse {
							subsumed = true
						}
					}
					if !subsumed {
						rep.Findings = append(rep.Findings, deadTemplateBranch(top.start, lineNo))
					}
				}
			}
		}
	}
	if len(stack) > 0 {
		rep.LowConfidence = true // unclosed define or conditional
	}
	return nodes
}

// deadTemplateBranch builds the dead-code finding for one false-guarded span.
func deadTemplateBranch(start, end int) StructuralFinding {
	return StructuralFinding{
		Kind:   FindingDeadCodePath,
		Label:  `{{if false}} branch`,
		Detail: fmt.Sprintf("template branch guarded by a constant false condition (%s) is unreachable", Region{start, end}),
		Region: Region{StartLine: start, EndLine: end},
	}
}

type tplActionKind int

const (
	tplOther tplActionKind = iota
	tplDefine
	tplEnd
	tplIfFalse
	tplIfTrue
)

type tplAction struct {
	kind tplActionKind
	name string
}

// templateActionsOn extracts the control actions on one line, in order.
func templateActionsOn(line []byte) []tplAction {
	s := string(line)
	var out []tplAction
	for x := 0; x < len(s); {
		open := strings.Index(s[x:], "{{")
		if open < 0 {
			break
		}
		x += open + 2
		end := strings.Index(s[x:], "}}")
		if end < 0 {
			break
		}
		body := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s[x:x+end]), "-"))
		x += end + 2
		switch {
		case strings.HasPrefix(body, "define"):
			out = append(out, tplAction{kind: tplDefine, name: quotedName(body)})
		case body == "end":
			out = append(out, tplAction{kind: tplEnd})
		case strings.HasPrefix(body, "if"):
			cond := strings.TrimSpace(strings.TrimPrefix(body, "if"))
			if cond == "false" {
				out = append(out, tplAction{kind: tplIfFalse})
			} else {
				out = append(out, tplAction{kind: tplIfTrue})
			}
		}
	}
	return out
}

// quotedName extracts the first quoted string of an action body.
func quotedName(body string) string {
	for _, q := range []byte{'"', '`'} {
		i := strings.IndexByte(body, q)
		if i < 0 {
			continue
		}
		if j := strings.IndexByte(body[i+1:], q); j >= 0 {
			return body[i+1 : i+1+j]
		}
	}
	return ""
}

// ── semantic units ──────────────────────────────────────────────────────────

// minSemanticUnits is the smallest unit count a semantic split must produce
// before it beats the syntactic splitters. Fewer partitions than that carry
// no decomposition value, so the caller keeps the syntactic path.
const minSemanticUnits = 2

// semanticUnits derives the candidate modification regions from the scanned
// topology: a leading header zone, one unit per meaningful top-level node
// (structural wrappers html/body are descended through; <head> collapses to a
// single metadata unit), then a trailing zone. Units cover every document
// line contiguously — gaps between siblings attach forward to the preceding
// unit so downstream DAG coverage validation holds by construction.
func semanticUnits(rep *LeaScanReport, lines [][]byte) []Section {
	return buildSemanticUnits(rep, lines)
}

// unitRoots collects the node indexes that seed one unit each: root nodes,
// descending through the structural wrappers html and body.
func unitRoots(rep *LeaScanReport, out []int) []int {
	var walk func(i int)
	walk = func(i int) {
		n := &rep.Nodes[i]
		if n.Tag == "html" || n.Tag == "body" {
			for _, c := range n.Children {
				walk(c)
			}
			return
		}
		out = append(out, i)
	}
	for i := range rep.Nodes {
		if rep.Nodes[i].Parent == -1 {
			walk(i)
		}
	}
	return out
}

// buildSemanticUnits tiles [1..TotalLines] with one section per unit root.
func buildSemanticUnits(rep *LeaScanReport, lines [][]byte) []Section {
	roots := unitRoots(rep, nil)
	if len(roots) == 0 {
		return nil
	}
	type span struct {
		start, end int
		label      string
	}
	spans := make([]span, 0, len(roots)+2)
	first := rep.Nodes[roots[0]].StartLine
	if first > 1 {
		spans = append(spans, span{1, first - 1, "(document header)"})
	}
	for _, r := range roots {
		n := rep.Nodes[r]
		if n.EndLine < n.StartLine {
			n.EndLine = rep.TotalLines
		}
		spans = append(spans, span{n.StartLine, n.EndLine, nodeUnitLabel(n)})
	}
	lastEnd := rep.Nodes[roots[len(roots)-1]].EndLine
	if lastEnd < len(lines) {
		spans = append(spans, span{lastEnd + 1, len(lines), "(document footer)"})
	}
	// Tile: absorb inter-span gaps into the preceding span and clamp overlaps
	// so the union provably covers 1..TotalLines without holes.
	units := make([]Section, 0, len(spans))
	prevEnd := 0
	for _, sp := range spans {
		start := sp.start
		if start <= prevEnd {
			start = prevEnd + 1
		}
		if start > sp.end {
			continue // fully absorbed by the preceding unit
		}
		if len(units) > 0 && start > prevEnd+1 {
			units[len(units)-1].Region.EndLine = start - 1 // gap attaches forward
		}
		units = append(units, Section{
			Region: Region{StartLine: start, EndLine: sp.end},
			Label:  sp.label,
		})
		prevEnd = sp.end
	}
	if len(units) == 0 {
		return nil
	}
	if final := &units[len(units)-1]; final.Region.EndLine < len(lines) {
		final.Region.EndLine = len(lines)
	}
	return units
}

// nodeUnitLabel renders the bounded human identity of one topology node
// ("​<head> metadata", "<section#hero> hero").
func nodeUnitLabel(n DOMNode) string {
	switch {
	case n.Tag == "head":
		return "<head> metadata"
	case n.ID != "":
		return "<" + n.Tag + "#" + n.ID + "> " + readableRole(n)
	case len(n.Classes) > 0:
		return "<" + n.Tag + "." + n.Classes[0] + "> " + readableRole(n)
	default:
		return "<" + n.Tag + ">"
	}
}

// readableRole derives a role hint from the node identity ("hero", "main nav")
// so sub-task descriptions read semantically instead of numerically.
func readableRole(n DOMNode) string {
	token := n.ID
	if token == "" && len(n.Classes) > 0 {
		token = n.Classes[0]
	}
	if token == "" {
		role := kebabToWords(n.Tag)
		if role == "" {
			return n.Tag
		}
		return role
	}
	role := kebabToWords(token)
	switch {
	case role == "":
		return n.Tag
	case structuralRoleWord(role):
		return role
	default:
		return role + " " + n.Tag
	}
}

// structuralRoleWord reports whether the role hint already names a layout
// role (section/nav/hero/…), in which case no tag suffix is needed.
func structuralRoleWord(role string) bool {
	for _, w := range strings.Fields(role) {
		switch w {
		case "section", "nav", "navigation", "header", "footer", "panel",
			"card", "form", "modal", "hero", "table", "sidebar", "main",
			"banner", "content", "aside", "article", "menu", "toolbar":
			return true
		}
	}
	return false
}

// kebabToWords splits an id/class token on -, _, : and camelCase boundaries.
func kebabToWords(token string) string {
	var b strings.Builder
	prevLower := false
	for _, r := range token {
		switch {
		case r == '-' || r == '_' || r == ':' || r == '.':
			b.WriteByte(' ')
			prevLower = false
		case prevLower && r >= 'A' && r <= 'Z':
			b.WriteByte(' ')
			b.WriteRune(r)
			prevLower = false
		default:
			b.WriteRune(r)
			prevLower = r >= 'a' && r <= 'z'
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// ── reference collection + findings ─────────────────────────────────────────

// collectReferences finds where every id/class defined in the topology is
// used elsewhere: CSS selectors (#id / .cls), href anchors (#id),
// url(#id) fragments and quoted tokens inside scripts. Definition sites are
// excluded so an unreferenced element cannot cite itself.
func collectReferences(lines [][]byte, nodes []DOMNode) []ActiveReference {
	type defKey struct {
		token string
		kind  string
	}
	defined := map[defKey]bool{}
	defLines := map[string]map[int]bool{} // bare token -> defining lines
	addDef := func(token, kind string, line int) {
		defined[defKey{token, kind}] = true
		if defLines[token] == nil {
			defLines[token] = map[int]bool{}
		}
		defLines[token][line] = true
	}
	for _, n := range nodes {
		if n.ID != "" {
			addDef(n.ID, "id", n.StartLine)
		}
		for _, c := range n.Classes {
			addDef(c, "class", n.StartLine)
		}
	}
	if len(defined) == 0 {
		return nil
	}
	uses := map[defKey][]int{}
	for i, line := range lines {
		lineNo := i + 1
		s := string(line)
		for key := range defined {
			if defLines[key.token][lineNo] {
				continue // never cite the definition itself
			}
			if referencedInLine(s, key.token, key.kind) {
				uses[key] = append(uses[key], lineNo)
			}
		}
	}
	keys := make([]defKey, 0, len(uses))
	for k := range uses {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].token != keys[j].token {
			return keys[i].token < keys[j].token
		}
		return keys[i].kind < keys[j].kind
	})
	out := make([]ActiveReference, 0, len(keys))
	for _, k := range keys {
		out = append(out, ActiveReference{Name: k.token, Kind: k.kind, UsedAt: dedupSorted(uses[k])})
	}
	return out
}

// referencedInLine reports whether the token is referenced by a selector,
// anchor or lookup on this line.
func referencedInLine(s, token, kind string) bool {
	if !strings.Contains(s, token) {
		return false
	}
	switch kind {
	case "id":
		// Anchor/SVG-fragment forms: href="#top", url(#top).
		if strings.Contains(s, "\"#"+token) || strings.Contains(s, "'#"+token) ||
			strings.Contains(s, "url(#"+token+")") {
			return true
		}
	case "class":
		// Class selectors may chain (.card.active): require a '.' boundary
		// that is not the opening bracket of the defining tag.
		from := 0
		for {
			idx := strings.Index(s[from:], "."+token)
			if idx < 0 {
				break
			}
			at := from + idx
			if at > 0 && !isTagNameChar(rune(s[at-1])) && s[at-1] != '<' &&
				s[at-1] != '"' && s[at-1] != '\'' {
				return true
			}
			from = at + 1
			if from >= len(s) {
				break
			}
		}
	}
	// Script/template lookups: getElementById("x"), addEventListener on
	// quoted hooks, template data bindings.
	for _, q := range []byte{'"', '\'', '`'} {
		if strings.Contains(s, string(q)+token+string(q)) {
			return true
		}
	}
	return false
}

// isTagNameChar reports whether r can appear inside a tag or attribute name
// (letters, digits, dash, underscore, colon) — used to reject '.' boundaries
// inside compound identifiers rather than selectors.
func isTagNameChar(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '-' || r == '_' || r == ':':
		return true
	default:
		return false
	}
}

// detectFindings runs the deterministic candidate-region detectors and
// appends them to any findings the format scanners already recorded (the
// template scanner reports its own dead branches during the walk):
// duplicate selectors (repeated ids, repeated CSS rules), unused elements
// (unreferenced generic ids) and dead code paths (commented-out markup).
func detectFindings(rep *LeaScanReport, lines [][]byte) []StructuralFinding {
	rep.Findings = append(rep.Findings, duplicateIDFindings(rep.Nodes)...)
	rep.Findings = append(rep.Findings, duplicateCSSRuleFindings(lines, rep.Nodes)...)
	rep.Findings = append(rep.Findings, unusedElementFindings(rep)...)
	rep.Findings = append(rep.Findings, deadMarkupFindings(lines)...)
	return rep.Findings
}

// duplicateIDFindings flags id values bound to more than one element.
func duplicateIDFindings(nodes []DOMNode) []StructuralFinding {
	at := map[string][]int{}
	for _, n := range nodes {
		if n.ID != "" {
			at[n.ID] = append(at[n.ID], n.StartLine)
		}
	}
	ids := make([]string, 0, len(at))
	for id := range at {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []StructuralFinding
	for _, id := range ids {
		locs := at[id]
		if len(locs) < 2 {
			continue
		}
		out = append(out, StructuralFinding{
			Kind:   FindingDuplicateSelector,
			Label:  "#" + id,
			Detail: fmt.Sprintf("id %q is bound to %d elements (lines %s) — selectors resolve to only the first", id, len(locs), joinInts(locs)),
			Region: Region{StartLine: locs[0], EndLine: locs[len(locs)-1]},
		})
	}
	return out
}

// cssRule is one extracted style-block selector rule.
type cssRule struct {
	selector string
	line     int
}

// extractCSSRules pulls `selector { ... }` heads out of <style> raw-text
// zones using the topology's style nodes as trusted bounds.
func extractCSSRules(lines [][]byte, nodes []DOMNode) []cssRule {
	var rules []cssRule
	for _, n := range nodes {
		if n.Tag != "style" {
			continue
		}
		for i := n.StartLine; i <= n.EndLine && i <= len(lines); i++ {
			t := strings.TrimSpace(string(lines[i-1]))
			if t == "" || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "*") {
				continue
			}
			if brace := strings.Index(t, "{"); brace > 0 {
				sel := normalizeCSSSelector(t[:brace])
				if sel != "" && !strings.Contains(sel, ";") {
					rules = append(rules, cssRule{selector: sel, line: i})
				}
			}
		}
	}
	return rules
}

// normalizeCSSSelector canonicalizes whitespace inside a selector head.
func normalizeCSSSelector(sel string) string {
	return strings.Join(strings.Fields(sel), " ")
}

// duplicateCSSRuleFindings flags identical selector heads declared twice —
// the second rule silently shadows the first, a classic dead-style defect.
func duplicateCSSRuleFindings(lines [][]byte, nodes []DOMNode) []StructuralFinding {
	rules := extractCSSRules(lines, nodes)
	if len(rules) < 2 {
		return nil
	}
	at := map[string][]int{}
	order := make([]string, 0, len(rules))
	for _, r := range rules {
		if _, seen := at[r.selector]; !seen {
			order = append(order, r.selector)
		}
		at[r.selector] = append(at[r.selector], r.line)
	}
	var out []StructuralFinding
	for _, sel := range order {
		locs := at[sel]
		if len(locs) < 2 {
			continue
		}
		out = append(out, StructuralFinding{
			Kind:   FindingDuplicateSelector,
			Label:  sel,
			Detail: fmt.Sprintf("style rule %q is declared %d times (lines %s) — later declarations shadow earlier ones", sel, len(locs), joinInts(locs)),
			Region: Region{StartLine: locs[0], EndLine: locs[len(locs)-1]},
		})
	}
	return out
}

// landmarkTags are structural landmarks whose unreferenced ids are normal
// page anatomy — a footer needs no style hook or anchor to be legitimate.
// Unused-element findings therefore target only generic containers.
var landmarkTags = map[string]bool{
	"html": true, "head": true, "body": true,
	"header": true, "footer": true, "main": true, "nav": true,
	"section": true, "article": true, "aside": true, "form": true,
}

// unusedElementFindings flags GENERIC elements whose id no other line uses.
// Landmark tags are exempt: an unanchored <header> is anatomy, not a defect.
func unusedElementFindings(rep *LeaScanReport) []StructuralFinding {
	used := make(map[string]bool, len(rep.References))
	for _, r := range rep.References {
		used[r.Kind+":"+r.Name] = true
	}
	var out []StructuralFinding
	for _, n := range rep.Nodes {
		if n.ID == "" || used["id:"+n.ID] || landmarkTags[n.Tag] {
			continue
		}
		out = append(out, StructuralFinding{
			Kind:  FindingUnusedElement,
			Label: "#" + n.ID,
			Detail: fmt.Sprintf("<%s id=%q> at line %d is referenced nowhere (no style rule, anchor or script hook)",
				n.Tag, n.ID, n.StartLine),
			Region: Region{n.StartLine, n.EndLine},
		})
	}
	return out
}

// deadMarkupFindings flags commented-out markup blocks: an HTML comment whose
// body contains a start tag is dead code a previous edit left behind.
func deadMarkupFindings(lines [][]byte) []StructuralFinding {
	var (
		out       []StructuralFinding
		start     = -1 // 1-indexed line an unterminated dead comment opened on
		hasMarkup bool
	)
	flush := func(end int) {
		if start > 0 && hasMarkup {
			out = append(out, StructuralFinding{
				Kind:   FindingDeadCodePath,
				Label:  "commented-out markup",
				Detail: fmt.Sprintf("markup block commented out at %s is unreachable dead code", Region{start, end}),
				Region: Region{StartLine: start, EndLine: end},
			})
		}
		start, hasMarkup = -1, false
	}
	for i, line := range lines {
		s := string(line)
		for x := 0; x < len(s); {
			if start > 0 { // inside a multi-line dead-comment candidate
				end := strings.Index(s[x:], "-->")
				if end < 0 {
					if !hasMarkup && containsTagOpen(s[x:]) {
						hasMarkup = true
					}
					x = len(s)
					continue
				}
				body := s[x : x+end]
				if !hasMarkup && containsTagOpen(body) {
					hasMarkup = true
				}
				x += end + 3
				flush(i + 1)
				continue
			}
			openC := strings.Index(s[x:], "<!--")
			if openC < 0 {
				break
			}
			from := x + openC + 4
			endC := strings.Index(s[from:], "-->")
			if endC < 0 {
				start, hasMarkup = i+1, containsTagOpen(s[from:])
				x = len(s)
				continue
			}
			if body := s[from : from+endC]; containsTagOpen(body) {
				out = append(out, StructuralFinding{
					Kind:   FindingDeadCodePath,
					Label:  "commented-out markup",
					Detail: fmt.Sprintf("markup block commented out at %s is unreachable dead code", Region{i + 1, i + 1}),
					Region: Region{StartLine: i + 1, EndLine: i + 1},
				})
			}
			x = from + endC + 3
		}
	}
	flush(len(lines))
	return out
}

// containsTagOpen reports whether s contains something that looks like a
// markup tag (<name or </name).
func containsTagOpen(s string) bool {
	for x := 0; x+1 < len(s); x++ {
		if s[x] != '<' {
			continue
		}
		c := s[x+1]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '/' {
			return true
		}
	}
	return false
}

// NodeIdentity renders the bounded structural identity of a topology node
// for cross-package consumers (compressed sub-task contexts): the
// CSS-selector shorthand plus its human role hint.
func NodeIdentity(n DOMNode) string {
	if n.Tag == "" {
		return "(unnamed node)"
	}
	sel := n.CSSSelector()
	role := readableRole(n)
	if role != "" && !strings.EqualFold(role, n.Tag) {
		return sel + " " + role
	}
	return sel
}

// ── small deterministic formatting helpers ──────────────────────────────────

// joinInts renders line numbers compactly ("3, 7, 12").
func joinInts(xs []int) string {
	parts := make([]string, 0, len(xs))
	for _, x := range xs {
		parts = append(parts, strconv.Itoa(x))
	}
	return strings.Join(parts, ", ")
}
