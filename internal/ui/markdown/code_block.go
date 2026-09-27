// code_block.go — NORMALIZATION FOR CODE FENCES NESTED INSIDE MARKDOWN LISTS.
//
// # THE PROBLEM THIS SOLVES
//
// A fenced code block inside a list item is a first-class Markdown construct
// that every renderer handles differently and most handle badly:
//
//  1. INSTANT DISAPPEARANCE. List-item walkers in this codebase dispatch on the
//     children of an `ast.ListItem`. When `*ast.FencedCodeBlock` is not one of
//     the dispatched cases it is not merely unstyled — it is DROPPED. The
//     commands vanish, the numbered step that referenced them becomes a lie,
//     and the list silently shrinks. This is not a cosmetic bug: the model
//     answered a question and the terminal is showing a different answer.
//
//  2. THE DUPLICATED TITLE. A code fence renders with a language header badge
//     ("bash", "Bash / POSIX shell") so a bare top-level fence is identifiable.
//     Inside a list item the badge is not merely redundant, it is WRONG: the
//     parent item's prose has already introduced the language ("Install deps:"),
//     and Glamour/Chroma both re-emit their own header. The user sees the title
//     twice, stacked, in two different styles — the visual signature of two
//     renderers having both claimed the same block.
//
//  3. THE RAGGED MARGIN. A list nests by INDENT. Indent is a function of DEPTH
//     (2 cells per level), so a code fence drawn at the full document width
//     either overhangs its parent's right edge or, worse, is re-wrapped at the
//     un-indented width and pushes its own `│` gutter one column right of every
//     sibling row. The vertical border stops lining up, and a border that stops
//     lining up is a border the reader cannot trust.
//
// # THE CONTRACT
//
// A CodeBlock carries the ONE piece of context a renderer cannot recover on its
// own: the list depth it was found at. Everything else is derived from that, so
// the three bugs above cannot reappear independently:
//
//   - ShowLanguage() is false for every list-embedded fence. One title, drawn
//     by exactly one renderer, or none.
//   - LeftMargin() is ListIndentPerLevel × depth, computed rather than assumed,
//     so a fence at depth 3 sits at 6 cells and a fence at depth 1 sits at 2.
//   - ContentWidth() subtracts that margin from the available width BEFORE the
//     code is wrapped, so the wrap and the indent are derived from one number
//     and can never disagree.
//
// The zero value is a top-level fence: no margin, language header shown. A nil
// *CodeBlock is not required anywhere — this type is a value, not a resource.
package markdown

import (
	"strings"

	"github.com/yuin/goldmark/ast"
)

// ListIndentPerLevel is the number of terminal cells one Markdown list nesting
// level occupies. It is the project's single indent quantum: list items,
// nested lists, and now list-embedded code fences all step by this much, which
// is what keeps a fence's left edge on the same column as the text of the item
// that owns it.
const ListIndentPerLevel = 2

// minCodeContentWidth is the narrowest code content column a fence may be
// wrapped to. Below this a fence is a column of single characters, which
// conveys nothing; the fence degrades to the minimum rather than to noise.
//
// It is a FLOOR, not a target. A caller that also spends cells on other chrome —
// the streaming renderer spends two on its "│ " gutter — may impose a higher
// floor of its own, but never a lower one: two different floors for the same
// concept is how a fence ends up wrapped to 8 cells on one path and 10 on
// another.
const minCodeContentWidth = 8

// CodeBlock is one fenced code block together with the list context it was
// found in. Level is the number of `ast.ListItem` ancestors: 0 for a top-level
// fence, 1 for a fence directly inside a list item, and so on.
type CodeBlock struct {
	// Language is the fence's language tag, already normalised (see FenceInfo)
	// so info-string metadata is not carried along. It is a LEXER SELECTION
	// hint; whether it is also DISPLAYED is a separate question with a separate
	// answer — see ShowLanguage.
	Language string

	// Lines are the fence's code lines with the opening/closing fence markers
	// already stripped and the trailing newline removed.
	Lines []string

	// Level is the list nesting depth of the fence (0 = top level).
	Level int
}

// NewCodeBlock returns a CodeBlock for the given language and lines at list
// depth level. It is the single constructor, so Level can never be left
// uninitialised by a caller that forgot it.
func NewCodeBlock(language string, lines []string, level int) CodeBlock {
	if level < 0 {
		level = 0
	}
	return CodeBlock{Language: language, Lines: lines, Level: level}
}

// InListItem reports whether the fence is nested inside at least one list item.
// This is the single predicate the other two questions key off, so a renderer
// can never decide "show the header" by a rule that disagrees with "indent me".
func (c CodeBlock) InListItem() bool { return c.Level > 0 }

// ShowLanguage reports whether the language header badge should be drawn.
//
// It is false for every list-embedded fence and true for a top-level one. The
// rule is deliberately binary rather than conditional on payload size or
// language: the duplicate title is a property of NESTING, not of the language,
// so any payload-dependent exception would let the duplicate back in for
// exactly the common case (a short "bash" fence under a bullet).
func (c CodeBlock) ShowLanguage() bool { return !c.InListItem() }

// LeftMargin is the dynamic left-margin offset for this fence: two cells per
// list level, computed from the measured depth rather than hardcoded per call
// site. A top-level fence is 0, so this returns 0 for the common case and
// costs the layout nothing.
func (c CodeBlock) LeftMargin() int {
	if c.Level <= 0 {
		return 0
	}
	return ListIndentPerLevel * c.Level
}

// ContentWidth is the number of cells available to the code text itself: the
// width the caller was given, minus this fence's own left margin.
//
// The margin is subtracted HERE, at the single point where the width enters the
// layout, rather than at each render site. A caller that pre-wraps to the full
// width and then indents has already lost — the wrapped line is one wrap too
// wide by the time the margin is applied, and the right border is the cell that
// goes missing.
func (c CodeBlock) ContentWidth(available int) int {
	w := available - c.LeftMargin()
	if w < minCodeContentWidth {
		return minCodeContentWidth
	}
	return w
}

// HeaderLine returns the language badge to draw above the fence, or "" when the
// badge is suppressed. The caller renders the returned text; this function owns
// only the DECISION, so a caller cannot accidentally draw a title it was not
// given.
func (c CodeBlock) HeaderLine() string {
	if !c.ShowLanguage() {
		return ""
	}
	return c.Language
}

// Indent prefixes every line with the fence's left margin. It is applied after
// wrapping, never before, so the margin can never be double-counted inside a
// wrap width.
func (c CodeBlock) Indent(line string) string {
	margin := c.LeftMargin()
	if margin == 0 || line == "" {
		return line
	}
	return strings.Repeat(" ", margin) + line
}

// ListDepth counts how many `ast.ListItem` ancestors node has. It walks
// parents rather than children because the question is about CONTAINMENT: a
// fence knows its depth from where it sits, not from what it contains.
//
// The walk is bounded by the document itself and cannot loop: goldmark's parent
// chain is acyclic and terminates at the Document root. A nil node is not a
// depth question whose answer is "unknown" but a question that was never asked,
// so it returns 0 and an optional subtree can be passed through unguarded.
func ListDepth(node ast.Node) int {
	if node == nil {
		return 0
	}
	depth := 0
	for p := node.Parent(); p != nil; p = p.Parent() {
		if p.Kind() == ast.KindListItem {
			depth++
		}
	}
	return depth
}

// FenceInfo normalises a fence's info string to a bare language tag. Goldmark
// allows "```go title=main.go"; the language is the first space-delimited
// field, and the rest is metadata the terminal has nowhere to show.
func FenceInfo(info string) string {
	info = strings.TrimSpace(info)
	if i := strings.IndexAny(info, " \t"); i >= 0 {
		info = info[:i]
	}
	return info
}

// SplitFence reports whether a line is a Markdown code fence MARKER — an
// optional indent, then ``` or ~~~, then an optional language — and if so
// returns the marker's indent width and its language.
//
// This exists because the streaming path is line-oriented and cannot ask an AST
// where a fence sits. Its previous test was `strings.HasPrefix(line, "```")`,
// which is FALSE for every fence nested in a list item, because a nested fence
// is indented:
//
//   - Install deps:
//     ```bash        ← two spaces, so HasPrefix("```") is false
//     npm install
//     ```
//
// The consequence was not a cosmetic slip: the fence markers were never
// recognised as markers, so they were rendered as literal text and the code
// between them lost its highlighting, its width budget, and its block identity.
// The document showed a stray backtick, then a run of unstyled text, then
// another stray backtick. Accepting the indent is what makes the nested case
// parse as the nested case.
func SplitFence(line string) (indent int, language string, ok bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if len(trimmed) < 3 {
		return 0, "", false
	}
	marker := trimmed[:3]
	if marker != "```" && marker != "~~~" {
		return 0, "", false
	}
	return len(line) - len(trimmed), FenceInfo(trimmed[3:]), true
}

// IndentLevel converts a leading-indent width into a list nesting depth using
// ListIndentPerLevel as the step. An indent that is not a whole multiple of the
// step still yields a depth — the floor — because under-indenting a fence by one
// cell is a cosmetic imperfection while over-indenting it pushes the right
// border off-screen.
func IndentLevel(indent int) int {
	if indent <= 0 {
		return 0
	}
	return indent / ListIndentPerLevel
}

// Dedent removes the longest common leading-whitespace prefix from a block's
// lines. A list-embedded fence's body is indented in the source to line up with
// its marker, and that source indent is STRUCTURAL, not content: leaving it in
// renders every code line one level deeper than the fence's own margin, so the
// block sits visibly to the right of the item that owns it. Dedent is applied
// before the margin, never after, so exactly one indent survives.
//
// Blank lines are skipped when measuring the common prefix: a whitespace-only
// line would otherwise force the common prefix to zero and defeat the whole
// operation.
func Dedent(lines []string) []string {
	common := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := 0
		for n < len(l) && (l[n] == ' ' || l[n] == '\t') {
			n++
		}
		if common < 0 || n < common {
			common = n
		}
	}
	if common <= 0 {
		return lines
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		if len(l) >= common {
			out[i] = l[common:]
		} else {
			out[i] = strings.TrimLeft(l, " \t")
		}
	}
	return out
}

// VisitFencedCodeBlocks calls fn once for every fenced code block in the
// subtree rooted at node, in document order, with the block's language and
// list depth already resolved. source must be the byte slice the document was
// parsed from — goldmark stores segment contents as offsets into it, so the
// info string and the code lines are only recoverable against it.
//
// It is a read-only walk: it mutates nothing, and a nil node or nil callback
// returns immediately rather than panicking, so a caller can hand it an
// optional subtree without a guard.
func VisitFencedCodeBlocks(node ast.Node, source []byte, fn func(CodeBlock)) {
	if node == nil || fn == nil {
		return
	}
	for c := node.FirstChild(); c != nil; c = c.NextSibling() {
		if fence, ok := c.(*ast.FencedCodeBlock); ok {
			fn(NewCodeBlock(FenceLanguage(fence, source), FenceLines(fence, source), ListDepth(fence)))
		}
		VisitFencedCodeBlocks(c, source, fn)
	}
}

// FenceLanguage returns a fenced code block's normalised language tag, or ""
// when the fence carries no info string. It reads the info segment against
// source, which is why the source must travel with the AST.
func FenceLanguage(fence *ast.FencedCodeBlock, source []byte) string {
	if fence == nil || fence.Info == nil {
		return ""
	}
	return FenceInfo(string(fence.Info.Segment.Value(source)))
}

// FenceLines returns a fence's code lines with the per-line trailing newline
// stripped and the fence markers themselves already excluded — goldmark's
// Lines() covers only the content between the markers.
//
// Stripping the trailing newline on the LAST line too is what makes the row
// count equal the number of code lines a reader would count; leaving it renders
// one phantom blank row, which inside a list item is a visible gap between the
// code and the next bullet.
func FenceLines(fence *ast.FencedCodeBlock, source []byte) []string {
	if fence == nil {
		return nil
	}
	seg := fence.Lines()
	if seg == nil || seg.Len() == 0 {
		return nil
	}
	out := make([]string, 0, seg.Len())
	for i := 0; i < seg.Len(); i++ {
		line := seg.At(i)
		out = append(out, strings.TrimRight(string(line.Value(source)), "\r\n"))
	}
	return out
}
