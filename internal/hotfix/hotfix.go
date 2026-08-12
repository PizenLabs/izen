// Package hotfix implements the deterministic TARGET RESOLUTION stage of the
// $hot pipeline: it analyzes a target file for the first structural mutation
// target BEFORE any model is invoked, and extracts the bounded block that must
// change. The model never receives the entire file — only the resolved target
// block plus the exact mutation instruction, making an unbounded full-document
// response structurally impossible at generation time.
package hotfix

import (
	"fmt"
	"regexp"
	"strings"
)

// MismatchKind classifies the structural error located by ResolveHTMLTarget.
type MismatchKind string

const (
	// KindUnmatchedClosing: a closing tag with no matching open tag anywhere
	// in the current element stack (e.g. "<h3>Project Delta</h2>").
	KindUnmatchedClosing MismatchKind = "unmatched-closing"
	// KindUnclosed: a required element left open at end of document.
	KindUnclosed MismatchKind = "unclosed"
)

// Mismatch is a deterministic structural error located in an HTML document.
type Mismatch struct {
	Line     int          // 1-based line of the offending tag
	Tag      string       // the offending tag name (lowercased)
	Kind     MismatchKind // how the document is structurally wrong
	Expected string       // KindUnmatchedClosing: the open tag that should have been closed
	OpenLine int          // line where the relevant element was opened
}

// Describe renders a human/LLM-readable mutation instruction for the mismatch.
func (m Mismatch) Describe() string {
	switch m.Kind {
	case KindUnmatchedClosing:
		if m.Expected != "" {
			return fmt.Sprintf("line %d: closing tag </%s> does not match the open tag <%s> (opened at line %d)",
				m.Line, m.Tag, m.Expected, m.OpenLine)
		}
		return fmt.Sprintf("line %d: closing tag </%s> has no matching open tag", m.Line, m.Tag)
	case KindUnclosed:
		return fmt.Sprintf("line %d: <%s> is never closed", m.OpenLine, m.Tag)
	default:
		return ""
	}
}

// Target is the deterministic, bounded mutation target for a hotfix.
type Target struct {
	StartLine int      // 1-based first line of the target block
	EndLine   int      // 1-based last line of the target block (inclusive)
	Block     string   // the exact on-disk text of lines [StartLine..EndLine]
	Mismatch  Mismatch // the structural error inside the block
}

// maxHotfixCandidates bounds the deterministic candidate list so candidate
// inspection stays readable.
const maxHotfixCandidates = 8

// ResolveHTMLTarget analyzes HTML content for the first deterministic
// structural mismatch and extracts the bounded target block that must change.
// ok=false when no structural mismatch can be located (a balanced document, or
// content that is not recognizable HTML).
func ResolveHTMLTarget(content string) (Target, bool) {
	candidates := ResolveHTMLCandidates(content)
	if len(candidates) == 0 {
		return Target{}, false
	}
	return candidates[0], true
}

// ResolveHTMLCandidates is the deterministic candidate-discovery stage: it
// returns EVERY structural anomaly in the HTML content, each with its bounded
// target block. It is used ONLY for human inspection — selecting a candidate is
// an explicit user act, never automatic. The list is capped at
// maxHotfixCandidates and ordered by scan position.
func ResolveHTMLCandidates(content string) []Target {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	anoms := scanAllMismatches(content)
	if len(anoms) > maxHotfixCandidates {
		anoms = anoms[:maxHotfixCandidates]
	}
	out := make([]Target, 0, len(anoms))
	for _, a := range anoms {
		start, end := targetSpan(lines, a.stack, a.mm)
		block := strings.Join(lines[start-1:end], "\n")
		out = append(out, Target{StartLine: start, EndLine: end, Block: block, Mismatch: a.mm})
	}
	return out
}

// anomaly is one located structural mismatch plus the element-stack snapshot at
// the point it was found (used to scope the target block).
type anomaly struct {
	stack []openTag
	mm    Mismatch
}

// openTag is one element currently open in the element-stack scan.
type openTag struct {
	tag  string
	line int
}

// tagScanner walks HTML content character by character, tracking exact 1-based
// line numbers, and maintains the element stack.
type tagScanner struct {
	content string
	i       int
	line    int
}

// advance consumes n bytes, accounting for any newlines crossed.
func (s *tagScanner) advance(n int) {
	if n <= 0 {
		return
	}
	if n > len(s.content)-s.i {
		n = len(s.content) - s.i
	}
	s.line += strings.Count(s.content[s.i:s.i+n], "\n")
	s.i += n
}

// scanAllMismatches runs the line-numbered element-stack scan over the whole
// document, collecting every structural mismatch (with a per-mismatch stack
// snapshot). The scan RECOVERS after each mismatch (implicit-close semantics)
// so one defect never cascades into duplicate candidates.
func scanAllMismatches(content string) []anomaly {
	s := &tagScanner{content: content, i: 0, line: 1}
	var stack []openTag
	var out []anomaly
	for s.i < len(s.content) {
		if s.content[s.i] != '<' {
			s.advance(1)
			continue
		}
		mm, err := s.handleTag(&stack)
		if err != nil {
			break
		}
		if mm != nil {
			out = append(out, anomaly{stack: append([]openTag{}, stack...), mm: *mm})
		}
	}
	// End of document: report every strong element still open (excluding
	// html/head/body and optional-close elements, which real documents omit).
	for i := len(stack) - 1; i >= 0; i-- {
		if strongClose[stack[i].tag] {
			out = append(out, anomaly{
				stack: append([]openTag{}, stack...),
				mm: Mismatch{
					Line:     stack[i].line,
					Tag:      stack[i].tag,
					Kind:     KindUnclosed,
					OpenLine: stack[i].line,
				},
			})
		}
	}
	return out
}

// handleTag processes one tag beginning at s.content[s.i] ('<') and returns the
// first mismatch found, if any.
func (s *tagScanner) handleTag(stack *[]openTag) (*Mismatch, error) {
	if s.i+1 >= len(s.content) {
		s.advance(1) // dangling '<' at EOF — plain text
		return nil, nil
	}
	switch nxt := s.content[s.i+1]; {
	case nxt == '!':
		s.skipDeclaration()
		return nil, nil
	case nxt == '?':
		s.skipTo("?>")
		return nil, nil
	case nxt == '/':
		return s.handleClose(stack)
	case isNameStart(nxt):
		return s.handleOpen(stack)
	default:
		s.advance(1) // '<' followed by non-name: plain text
		return nil, nil
	}
}

// handleOpen processes an opening tag "<name ...>" and returns the first
// mismatch found, if any.
func (s *tagScanner) handleOpen(stack *[]openTag) (*Mismatch, error) {
	openLine := s.line
	start := s.i + 1
	end := start
	for end < len(s.content) && isNameChar(s.content[end]) {
		end++
	}
	name := strings.ToLower(s.content[start:end])
	if name == "" {
		s.advance(1)
		return nil, nil
	}

	// Scan to '>' respecting quoted attribute values; detect self-closing.
	selfClosing := false
	j := end
	var quote byte
	for j < len(s.content) {
		switch c := s.content[j]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '>':
			if j > end && s.content[j-1] == '/' {
				selfClosing = true
			}
			s.advance(j - s.i + 1)
			j = -1
		}
		if j == -1 {
			break
		}
		j++
	}
	if j != -1 {
		s.advance(len(s.content) - s.i) // unterminated tag — consume rest
	}

	if selfClosing || voidElements[name] {
		return nil, nil
	}
	if rawTextElements[name] {
		return nil, s.skipRawText(name)
	}
	if optionalClose[name] {
		popThrough(stack, name)
	}
	*stack = append(*stack, openTag{tag: name, line: openLine})
	return nil, nil
}

// handleClose processes a closing tag "</name>" and returns the first mismatch
// found, if any. It always RECOVERS the element stack (HTML implicit-close
// semantics) so the scan can continue collecting every anomaly in the document.
func (s *tagScanner) handleClose(stack *[]openTag) (*Mismatch, error) {
	closeLine := s.line
	start := s.i + 2
	end := start
	for end < len(s.content) && isNameChar(s.content[end]) {
		end++
	}
	name := strings.ToLower(s.content[start:end])
	// Advance past the closing tag.
	if idx := strings.IndexByte(s.content[end:], '>'); idx >= 0 {
		s.advance(end - s.i + idx + 1)
	} else {
		s.advance(len(s.content) - s.i)
	}
	if name == "" || len(*stack) == 0 {
		return nil, nil
	}

	top := (*stack)[len(*stack)-1]
	if top.tag == name {
		*stack = (*stack)[:len(*stack)-1]
		return nil, nil
	}
	if idx := indexOfInStack(*stack, name); idx >= 0 {
		// Closing a tag that is open deeper in the stack: HTML implicitly
		// closes the intervening elements. This is legitimate ONLY when every
		// intervening element is implicitly closable (optional-close elements
		// plus html/head/body, which real documents omit). A strong element
		// still open at the close is the structural error being hunted.
		chainOK := true
		for _, e := range (*stack)[idx+1:] {
			if !implicitlyClosable[e.tag] {
				chainOK = false
				break
			}
		}
		if chainOK {
			popThrough(stack, name)
			return nil, nil
		}
		for _, e := range (*stack)[idx+1:] {
			if !implicitlyClosable[e.tag] {
				mm := &Mismatch{
					Line:     closeLine,
					Tag:      e.tag,
					Kind:     KindUnclosed,
					OpenLine: e.line,
				}
				popThrough(stack, name) // recover: the close consumes the chain
				return mm, nil
			}
		}
	}
	if optionalClose[name] || voidElements[name] {
		return nil, nil // tolerated stray close
	}
	// Stray close of a strong element: record it, then implicitly close the top
	// of stack (as the browser would) so the scan stays clean.
	mm := &Mismatch{
		Line:     closeLine,
		Tag:      name,
		Kind:     KindUnmatchedClosing,
		Expected: top.tag,
		OpenLine: top.line,
	}
	*stack = (*stack)[:len(*stack)-1]
	return mm, nil
}

// skipDeclaration skips <!-- comments -->, <!DOCTYPE>, <![CDATA[ ... ]]> and
// other "<!" markup declarations, which may span lines.
func (s *tagScanner) skipDeclaration() {
	rest := s.content[s.i:]
	switch {
	case strings.HasPrefix(rest, "<!--"):
		if idx := strings.Index(rest[4:], "-->"); idx >= 0 {
			s.advance(4 + idx + 3)
			return
		}
		s.advance(len(rest)) // unterminated comment
	case strings.HasPrefix(rest, "<![CDATA["):
		if idx := strings.Index(rest[9:], "]]>"); idx >= 0 {
			s.advance(9 + idx + 3)
			return
		}
		s.advance(len(rest))
	default:
		if idx := strings.IndexByte(rest, '>'); idx >= 0 {
			s.advance(idx + 1)
			return
		}
		s.advance(len(rest))
	}
}

// skipTo advances to the first occurrence of marker (e.g. "?>").
func (s *tagScanner) skipTo(marker string) {
	rest := s.content[s.i:]
	if idx := strings.Index(rest, marker); idx >= 0 {
		s.advance(idx + len(marker))
		return
	}
	s.advance(len(rest))
}

// skipRawText skips the raw-text/RCDATA body of an element (script, style,
// title, ...) up to and including its closing tag.
func (s *tagScanner) skipRawText(name string) error {
	re := regexp.MustCompile(`(?i)</` + regexp.QuoteMeta(name) + `(?:\s|/|>)`)
	rest := s.content[s.i:]
	if loc := re.FindStringIndex(rest); loc != nil {
		tail := rest[loc[0]:]
		if idx := strings.IndexByte(tail, '>'); idx >= 0 {
			s.advance(loc[0] + idx + 1)
		} else {
			s.advance(loc[0] + len(tail))
		}
		return nil
	}
	s.advance(len(rest)) // element never closed — consume to EOF
	return nil
}

// targetSpan returns the 1-based inclusive line span of the bounded block the
// model is allowed to mutate. For an unclosed element the block runs from the
// element's opening line toward EOF (where the missing close belongs); for an
// unmatched closing tag it is the enclosing sectioning element's span, falling
// back to a small window around the mismatch.
func targetSpan(lines []string, stack []openTag, mm Mismatch) (start, end int) {
	if mm.Kind == KindUnclosed {
		start = mm.OpenLine
		end = len(lines)
		if end-start+1 > maxTargetBlockLines {
			end = start + maxTargetBlockLines - 1
		}
		return start, end
	}

	n := len(lines)
	var inner *openTag
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].line <= mm.Line && sectioningElements[stack[i].tag] {
			inner = &stack[i]
			break
		}
	}
	if inner != nil {
		if s, e, ok := elementSpan(lines, inner.line, inner.tag); ok {
			return s, e
		}
	}
	if mm.Line < 1 {
		mm.Line = 1
	}
	start = max(1, mm.Line-6)
	end = min(n, mm.Line+8)
	if end < start {
		end = start
	}
	return start, end
}

// maxTargetBlockLines bounds the target block so it can never swallow the whole
// document.
const maxTargetBlockLines = 80

// sectioningElements are element boundaries the target block prefers to align
// with (a mismatch nested inside an <article>/<section> is scoped to that
// element, not the whole document).
var sectioningElements = map[string]bool{
	"html": true, "body": true, "div": true, "section": true, "article": true,
	"main": true, "header": true, "footer": true, "nav": true, "aside": true,
	"form": true, "table": true, "ul": true, "ol": true, "figure": true,
	"li": true, "tr": true, "td": true, "th": true,
}

// strongClose are elements that MUST be explicitly closed for the document to
// be structurally well-formed. html/head/body are deliberately excluded — real
// documents omit their closing tags, so reporting them as anomalies would be
// noise. Optional-close elements (p, li, td, ...) and void elements are also
// excluded.
var strongClose = map[string]bool{
	"title": true, "div": true, "section": true, "article": true,
	"main": true, "header": true, "footer": true, "nav": true, "aside": true,
	"form": true, "table": true, "ul": true, "ol": true, "dl": true,
	"figure": true, "script": true, "style": true, "textarea": true,
	"select": true, "h1": true, "h2": true, "h3": true, "h4": true,
	"h5": true, "h6": true,
}

// optionalClose are elements HTML implicitly closes when re-opened (or by a
// sibling opener). Tracked so legitimate documents do not produce false
// mismatches.
var optionalClose = map[string]bool{
	"li": true, "p": true, "dt": true, "dd": true, "option": true,
	"tr": true, "td": true, "th": true, "thead": true, "tbody": true, "tfoot": true,
}

// implicitlyClosable are elements a later closing tag (or EOF) may implicitly
// close without being a structural error: optional-close elements plus the
// html/head/body scaffolding real documents omit.
var implicitlyClosable = map[string]bool{
	"html": true, "head": true, "body": true,
	"li": true, "p": true, "dt": true, "dd": true, "option": true,
	"tr": true, "td": true, "th": true, "thead": true, "tbody": true, "tfoot": true,
}

// voidElements never require a closing tag and are not pushed onto the stack.
var voidElements = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true,
	"hr": true, "img": true, "input": true, "link": true, "meta": true,
	"param": true, "source": true, "track": true, "wbr": true,
}

// rawTextElements are elements whose content is parsed as raw text or RCDATA:
// the body is skipped verbatim until the closing tag.
var rawTextElements = map[string]bool{
	"script": true, "style": true, "textarea": true, "title": true,
	"xmp": true, "iframe": true, "noembed": true, "noframes": true,
	"noscript": true, "template": true, "plaintext": true,
}

// popThrough pops the stack up to and including the deepest open instance of
// tag (the HTML optional-close recovery used by <li>, <p>, <td>, ...).
func popThrough(stack *[]openTag, tag string) {
	for i := len(*stack) - 1; i >= 0; i-- {
		if (*stack)[i].tag == tag {
			*stack = (*stack)[:i]
			return
		}
	}
}

// indexOfInStack returns the index of the deepest open instance of tag in the
// stack, or -1 when the tag is not open.
func indexOfInStack(stack []openTag, tag string) int {
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].tag == tag {
			return i
		}
	}
	return -1
}

// isNameStart reports whether b can begin a tag name.
func isNameStart(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// isNameChar reports whether b can continue a tag name.
func isNameChar(b byte) bool {
	return isNameStart(b) || b >= '0' && b <= '9' || b == '-' || b == '_' || b == ':'
}

// tagCountRE matches opening/closing tags for elementSpan's depth tracking.
var tagCountRE = regexp.MustCompile(`</?\s*([a-zA-Z][a-zA-Z0-9-]*)\b`)

// elementSpan returns the 1-based inclusive span of the element opened at
// openLine. ok=false when the element does not close within maxTargetBlockLines
// (e.g. it is itself the unclosed element).
func elementSpan(lines []string, openLine int, tag string) (int, int, bool) {
	depth := 0
	for ln := openLine; ln <= len(lines); ln++ {
		if ln-openLine > maxTargetBlockLines {
			return 0, 0, false
		}
		for _, m := range tagCountRE.FindAllStringSubmatch(lines[ln-1], -1) {
			if m[1] != tag {
				continue
			}
			if strings.HasPrefix(m[0], "</") {
				depth--
			} else {
				depth++
			}
		}
		if ln > openLine && depth <= 0 {
			return openLine, ln, true
		}
	}
	return 0, 0, false
}
