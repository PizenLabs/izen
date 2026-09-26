package ui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	goldmarkext "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"

	"github.com/PizenLabs/izen/internal/ui/markdown"
)

// ListIndentQuantum is the indent step one Markdown list level occupies. It is
// the markdown package's constant, aliased here so the AST renderer and the
// fence renderer step by exactly the same number and a fence's gutter always
// lands on the same column as the item text above it.
const ListIndentQuantum = markdown.ListIndentPerLevel

// fenceLevelWithinListItem is the list level a fence is rendered at when it is
// reached from inside renderListItem.
//
// It is always exactly one quantum, regardless of how deep the fence actually
// sits, because the enclosing levels have already indented the line by the time
// the fence is drawn. A fence at absolute depth 2 is reached through a nested
// list whose items were each prefixed with one quantum, so it needs one more —
// not two. Passing the absolute depth charges it for the same prefix twice and
// lands it 6 cells in rather than 4.
const fenceLevelWithinListItem = 1

// MarkdownRenderer converts Markdown to semantic UI components
type MarkdownRenderer struct {
	Width int
}

// NewMarkdownRenderer creates a new MarkdownRenderer
func NewMarkdownRenderer(width int) *MarkdownRenderer {
	return &MarkdownRenderer{
		Width: width,
	}
}

// Render converts Markdown to rendered UI components.
//
// The raw chunk is normalised BEFORE parsing (see markdown.SanitizeRawMarkdown):
// HTML line breaks and CRLF are folded to real newlines first, so goldmark sees
// a soft break where the model wrote `<br>` and the AST walk never has to print
// literal markup. Fenced code is left untouched.
func (r *MarkdownRenderer) Render(src string) string {
	if src == "" {
		return ""
	}

	// Pre-AST normalisation: `<br>`/`<br/>`/`<br />` and CRLF -> newline.
	src = markdown.SanitizeRawMarkdown(src)
	sourceBytes := []byte(src)

	// Configure Goldmark with extensions
	md := goldmark.New(
		goldmark.WithExtensions(
			extension.Table,
			extension.TaskList,
			extension.Strikethrough,
		),
	)

	// Parse the markdown
	p := md.Parser()
	doc := p.Parse(text.NewReader(sourceBytes))

	// Process the AST and convert to semantic UI
	return renderAST(doc, r.Width, sourceBytes)
}

// renderAST converts the goldmark AST to UI components.
// NOTE on goldmark types:
//   - *ast.Emphasis handles both *italic* (Level=1) and **bold** (Level=2)
//   - *ast.ThematicBreak is "---" (not ast.HorizontalRule)
//   - Table types live in goldmark/extension/ast, not goldmark/ast
func renderAST(node ast.Node, width int, source []byte) string {
	var result strings.Builder

	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		switch n := child.(type) {
		case *ast.Heading:
			result.WriteString(renderHeading(n, width, source))
			result.WriteString("\n")
		case *ast.Paragraph:
			result.WriteString(renderParagraph(n, width, source))
			result.WriteString("\n")
		case *ast.Text:
			result.WriteString(renderText(n, source))
		case *ast.List:
			result.WriteString(renderList(n, width, source))
			result.WriteString("\n")
		case *ast.ListItem:
			result.WriteString(renderListItem(n, width, source, false, 0))
		case *ast.Blockquote:
			result.WriteString(renderBlockquote(n, width, source))
			result.WriteString("\n")
		case *ast.FencedCodeBlock:
			result.WriteString(renderFencedCodeBlock(n, width, source))
			result.WriteString("\n")
		case *ast.CodeSpan:
			result.WriteString(renderCodeSpan(n, source))
		case *ast.Link:
			result.WriteString(renderLink(n, source))
		case *ast.Image:
			result.WriteString(renderImage(n, source))
		case *ast.Emphasis:
			// Level 1 = *italic*, Level 2 = **bold**
			if n.Level == 2 {
				result.WriteString(renderStrong(n, source))
			} else {
				result.WriteString(renderEmphasis(n, source))
			}
		case *ast.ThematicBreak:
			// "---" horizontal rule
			result.WriteString(renderMHorizontalRule(width))
			result.WriteString("\n")
		case *goldmarkext.Table:
			result.WriteString(renderASTTable(n, width, source))
			result.WriteString("\n")
		default:
			// Recursively process child nodes for unhandled types
			if child.FirstChild() != nil {
				result.WriteString(renderAST(child, width, source))
			}
		}
	}

	return result.String()
}

// renderText extracts text from a text node. A CellBreakSentinel left by the
// pre-AST sanitizer is materialised into a real newline here, so a `<br>` that
// survived parsing as inline text becomes the break the model meant.
func renderText(node *ast.Text, source []byte) string {
	return materializeCellBreaks(string(node.Value(source)))
}

// materializeCellBreaks turns the pre-AST CellBreakSentinel into a newline.
func materializeCellBreaks(s string) string {
	if strings.Contains(s, markdown.CellBreakSentinel) {
		return strings.ReplaceAll(s, markdown.CellBreakSentinel, "\n")
	}
	return s
}

// renderEmphasis renders *italic* (Emphasis.Level == 1)
func renderEmphasis(node *ast.Emphasis, source []byte) string {
	textContent := strings.TrimSpace(renderInlineContent(node, source))
	if textContent == "" {
		return ""
	}

	return mdEmphasisStyle.Render(textContent)
}

// renderStrong renders **bold** (Emphasis.Level == 2)
func renderStrong(node *ast.Emphasis, source []byte) string {
	textContent := strings.TrimSpace(renderInlineContent(node, source))
	if textContent == "" {
		return ""
	}

	return mdStrongStyle.Render(textContent)
}

// renderHeading renders a heading per ASK_RENDERING.md specification
func renderHeading(node *ast.Heading, width int, source []byte) string {
	headingText := strings.TrimSpace(renderInlineContent(node, source))
	if headingText == "" {
		return ""
	}

	var styledText string
	switch node.Level {
	case 1:
		styledText = mdH1Style.Render(headingText)
	case 2:
		styledText = mdH2Style.Render(headingText)
	case 3:
		styledText = mdH3Style.Render(headingText)
	default:
		styledText = mdH4Style.Render(headingText)
	}

	separator := strings.Repeat("─", utf8.RuneCountInString(headingText))
	styledSeparator := mdMutedStyle.Render(separator)

	return "\n" + styledText + "\n" + styledSeparator
}

// renderInlineContent renders all inline children: text, emphasis, strong, code, links.
// This is the canonical way to extract content from block nodes that contain inline spans.
func renderInlineContent(node ast.Node, source []byte) string {
	var result strings.Builder
	for c := node.FirstChild(); c != nil; c = c.NextSibling() {
		switch c := c.(type) {
		case *ast.Text:
			result.WriteString(materializeCellBreaks(string(c.Value(source))))
			if c.HardLineBreak() || c.SoftLineBreak() {
				result.WriteString("\n")
			}
		case *ast.Emphasis:
			if c.Level == 2 {
				result.WriteString(renderStrong(c, source))
			} else {
				result.WriteString(renderEmphasis(c, source))
			}
		case *ast.CodeSpan:
			result.WriteString(renderCodeSpan(c, source))
		case *ast.Link:
			result.WriteString(renderLink(c, source))
		case *ast.RawHTML:
			// A raw-HTML inline node in prose is markup the renderer has no
			// meaning for, so it is dropped. A LINE BREAK is the exception,
			// because models reach for `<br>` inside table cells as the only
			// break GFM allows one, and dropping it fuses the two halves of a
			// sentence into a single unbreakable token. Rewriting it to "\n"
			// is what keeps the cell wrapping inside its column.
			if markdown.IsCellBreak(renderRawHTML(c, source)) {
				result.WriteString("\n")
			}
		default:
			// Fallback: recurse into unknown inline containers
			if c.FirstChild() != nil {
				result.WriteString(renderInlineContent(c, source))
			}
		}
	}
	return result.String()
}

// renderRawHTML returns the verbatim source text of a raw-HTML inline node.
//
// goldmark stores the node's bytes as segments rather than a decoded string, so
// this is the one place that decodes them. It is only ever called to decide
// whether the node is a line break (see the *ast.RawHTML arm above), never to
// emit markup, so the returned value is never rendered verbatim.
func renderRawHTML(node *ast.RawHTML, source []byte) string {
	if node.Segments == nil {
		return ""
	}
	var b strings.Builder
	for i := 0; i < node.Segments.Len(); i++ {
		seg := node.Segments.At(i)
		b.Write(seg.Value(source))
	}
	return b.String()
}

// renderParagraph renders a paragraph with full inline element support
func renderParagraph(node *ast.Paragraph, width int, source []byte) string {
	paragraphText := strings.TrimSpace(renderInlineContent(node, source))
	if paragraphText == "" {
		return ""
	}

	wrappedLines := wrapMText(paragraphText, width)
	return strings.Join(wrappedLines, "\n")
}

// renderList renders an ordered or unordered list
func renderList(node *ast.List, width int, source []byte) string {
	var result strings.Builder
	ordered := node.IsOrdered()
	counter := node.Start

	for item := node.FirstChild(); item != nil; item = item.NextSibling() {
		if listItem, ok := item.(*ast.ListItem); ok {
			result.WriteString(renderListItem(listItem, width, source, ordered, counter))
			result.WriteString("\n")
			if ordered {
				counter++
			}
		}
	}

	return strings.TrimRight(result.String(), "\n")
}

// renderListItem renders a single list item, supporting ordered lists and
// nested lists.
//
// A list item is a CONTAINER, not a leaf: its children may be prose, a nested
// list, a blockquote, a table, or a fenced code block. Every child type that
// can legally appear is dispatched here, because the default arm of a type
// switch SILENTLY DISCARDS its node — a fence that reaches it is deleted from
// the document, and the numbered step that referenced it becomes a lie. There
// is no "unknown" fallback that preserves content here on purpose: an
// unrecognised child is indented verbatim rather than dropped, because a
// renderer that loses the model's answer is strictly worse than one that draws
// it plainly.
func renderListItem(node *ast.ListItem, width int, source []byte, ordered bool, index int) string {
	var result strings.Builder

	for c := node.FirstChild(); c != nil; c = c.NextSibling() {
		switch c := c.(type) {
		case *ast.TextBlock, *ast.Paragraph:
			itemText := strings.TrimSpace(renderInlineContent(c, source))
			if itemText == "" {
				continue
			}
			wrapped := wrapMText(itemText, width-4)

			var bullet string
			if ordered {
				bullet = mdBulletStyle.Render(mdIntToStr(index) + ". ")
			} else {
				bullet = mdBulletStyle.Render(Icon.Bullet + " ")
			}

			for i, line := range wrapped {
				if i == 0 {
					result.WriteString(bullet + line)
				} else {
					result.WriteString("  " + line)
				}
				if i < len(wrapped)-1 {
					result.WriteString("\n")
				}
			}

		case *ast.FencedCodeBlock:
			// A fence inside a list item. renderFencedCodeBlock reads its own
			// list depth off the AST, so it applies the margin and suppresses
			// the language badge with no depth argument to keep in sync here.
			//
			// The level passed is the item's OWN quantum, not the fence's
			// absolute depth: by the time control reaches this arm, every
			// enclosing level has already prefixed its lines with an indent (see
			// the *ast.List arm below). Passing ListDepth here would charge the
			// fence for those same levels twice, dropping a depth-2 fence 6
			// cells in instead of 4 — visibly right of the item that owns it.
			fence := renderFencedCodeBlockAtLevel(c, width, source, fenceLevelWithinListItem)
			if fence == "" {
				continue
			}
			if result.Len() > 0 {
				result.WriteString("\n")
			}
			result.WriteString(fence)

		case *goldmarkext.Table:
			if result.Len() > 0 {
				result.WriteString("\n")
			}
			result.WriteString(renderASTTable(c, width, source))

		case *ast.Blockquote:
			if result.Len() > 0 {
				result.WriteString("\n")
			}
			if quoted := renderBlockquote(c, width, source); quoted != "" {
				result.WriteString(quoted)
			}

		case *ast.List:
			// Nested list: indent by one indent quantum.
			nestedRendered := renderList(c, width-ListIndentQuantum, source)
			for _, line := range strings.Split(nestedRendered, "\n") {
				result.WriteString("\n  " + line)
			}

		default:
			// Unknown block child: keep its text, indented, rather than
			// dropping it. See the doc comment above.
			if c.FirstChild() == nil {
				continue
			}
			inner := renderAST(c, width-ListIndentQuantum, source)
			if strings.TrimSpace(inner) == "" {
				continue
			}
			for _, line := range strings.Split(strings.TrimRight(inner, "\n"), "\n") {
				result.WriteString("\n  " + line)
			}
		}
	}

	return result.String()
}

// mdIntToStr converts an integer to a string (avoids strconv import dependency)
func mdIntToStr(n int) string {
	if n == 0 {
		return "0"
	}
	result := ""
	for n > 0 {
		result = string(rune('0'+n%10)) + result
		n /= 10
	}
	return result
}

// calloutMeta holds display metadata for a recognized callout keyword
type calloutMeta struct {
	icon  string
	color string
}

// calloutKeywords maps recognized callout keywords to their semantic display metadata.
// Icons are quiet monochrome glyphs (no emoji) from the shared Icon tokens.
var calloutKeywords = map[string]calloutMeta{
	"IMPORTANT": {Icon.Risk, "#f38ba8"},
	"NOTE":      {Icon.Info, "#89b4fa"},
	"TIP":       {Icon.Spark, "#a6e3a1"},
	"WARNING":   {Icon.Warning, "#f9e2af"},
	"CAUTION":   {"■", "#fab387"},
}

// renderBlockquote renders a blockquote with callout detection.
// GitHub-style [!NOTE] prefixes and bare-keyword callouts are recognized.
func renderBlockquote(node *ast.Blockquote, width int, source []byte) string {
	// Collect all child content
	var rawParts []string
	for c := node.FirstChild(); c != nil; c = c.NextSibling() {
		switch c := c.(type) {
		case *ast.Text:
			rawParts = append(rawParts, materializeCellBreaks(string(c.Value(source))))
		case *ast.Paragraph:
			rawParts = append(rawParts, renderInlineContent(c, source))
		default:
			if c.FirstChild() != nil {
				rawParts = append(rawParts, renderInlineContent(c, source))
			}
		}
	}

	quoteText := strings.TrimSpace(strings.Join(rawParts, "\n"))
	if quoteText == "" {
		return ""
	}

	// Extract first word for callout detection
	firstLine := quoteText
	if idx := strings.Index(quoteText, "\n"); idx >= 0 {
		firstLine = quoteText[:idx]
	}
	fields := strings.Fields(firstLine)
	if len(fields) == 0 {
		return ""
	}
	firstWord := strings.ToUpper(fields[0])

	// Strip GitHub-style [!NOTE] callout prefix
	if strings.HasPrefix(firstWord, "[!") && strings.HasSuffix(firstWord, "]") {
		firstWord = firstWord[2 : len(firstWord)-1]
	}

	if meta, ok := calloutKeywords[firstWord]; ok {
		rest := strings.TrimSpace(strings.TrimPrefix(quoteText, firstLine))
		labelStyle := mdCalloutStyles[firstWord]
		label := labelStyle.Render(meta.icon + " " + firstWord)

		var result strings.Builder
		result.WriteString(label)
		if rest != "" {
			result.WriteString("\n")
			wrapped := wrapMText(rest, width-2)
			for i, line := range wrapped {
				if i > 0 {
					result.WriteString("\n")
				}
				result.WriteString("  " + line)
			}
		}
		return result.String()
	}

	// Standard blockquote: vertical accent line per ASK_RENDERING.md
	var result strings.Builder
	wrapped := wrapMText(quoteText, width-2)
	for i, line := range wrapped {
		if i > 0 {
			result.WriteString("\n")
		}
		result.WriteString(mdAccentStyle.Render("┃") + " " + line)
	}

	return result.String()
}

// renderFencedCodeBlock renders a fenced code block with Chroma syntax
// highlighting using the Catppuccin Mocha palette. Lines are hard-wrapped to fit
// the available width without splitting ANSI escape sequences or multi-byte
// runes.
//
// LIST-EMBEDDED FENCES: the list depth is read off the AST itself
// (markdown.ListDepth) rather than threaded down as a parameter, so the
// indentation a fence receives can never disagree with the indentation the list
// that owns it already applied. See internal/ui/markdown/code_block.go for why
// the three list-embedded behaviours — margin, header suppression, wrap width —
// are derived from that one number.
func renderFencedCodeBlock(node *ast.FencedCodeBlock, width int, source []byte) string {
	return renderFencedCodeBlockAtLevel(node, width, source, markdown.ListDepth(node))
}

// renderFencedCodeBlockAtLevel is renderFencedCodeBlock with the list depth
// supplied by the caller.
//
// It exists for the one case the AST walk cannot express: a fence reached from
// inside renderListItem, where the enclosing levels' indentation is already on
// the line (see fenceLevelWithinListItem).
func renderFencedCodeBlockAtLevel(node *ast.FencedCodeBlock, width int, source []byte, level int) string {
	lang := markdown.FenceLanguage(node, source)
	codeLines := markdown.FenceLines(node, source)

	// Remove trailing empty lines: a fence's last segment always carries a
	// newline, and rendering it produces a blank row between the code and
	// whatever follows — a gap that reads as a dropped line.
	for len(codeLines) > 0 && codeLines[len(codeLines)-1] == "" {
		codeLines = codeLines[:len(codeLines)-1]
	}

	if len(codeLines) == 0 {
		return ""
	}

	block := markdown.NewCodeBlock(lang, codeLines, level)
	contentWidth := block.ContentWidth(width)

	// out accumulates the fence's physical rows. A slice of rows — rather than
	// a string built by appending separators as we go — is what keeps the
	// optional header row, the wrapped continuation rows, and the blank code
	// line (a genuinely empty line in the source) from being conflated: an
	// empty row is a row, and only the join decides where rows begin.
	out := make([]string, 0, len(codeLines)+1)

	// Language label — drawn ONLY for a top-level fence. A fence inside a list
	// item has already been introduced by its parent item's prose, so the badge
	// would be a second title for one block. This is the single place the
	// decision is made, so no renderer can emit a duplicate.
	if header := block.HeaderLine(); header != "" {
		out = append(out, block.Indent(mdMutedStyle.Render(header)))
	}

	// wrap re-flows one logical code line to the content column and applies the
	// fence's margin. The margin is applied AFTER wrapping, never before, so it
	// can never be double-counted inside a wrap width.
	wrap := func(s string) string {
		parts := hardSplit(s, contentWidth)
		for i, p := range parts {
			parts[i] = block.Indent(p)
		}
		return strings.Join(parts, "\n")
	}
	appendPlain := func() {
		for _, line := range codeLines {
			out = append(out, strings.Split(wrap(line), "\n")...)
		}
	}

	lexer := lexers.Get(lang)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)
	iterator, err := lexer.Tokenise(nil, strings.Join(codeLines, "\n"))
	if err != nil {
		// A lexer failure must never lose the code: fall back to the plain
		// wrapped rendering rather than dropping the block.
		appendPlain()
		return strings.Join(out, "\n")
	}

	// The token walk rebuilds the fence row by row. Two invariants drive it:
	//
	//   - A row ends when the SOURCE says so (a literal newline), or when the
	//     next token would not fit. It does NOT end at a token boundary: a
	//     lexer emits `func`, ` `, `main`, `(`, `)` as five separate tokens,
	//     and treating each as its own row shatters one line of code into five.
	//   - A blank line in the source is a ROW, not an absence. Dropping it
	//     renumbers every line after it, so a reader copying code out of the
	//     fence gets the wrong thing.
	var row strings.Builder
	rowCells := 0
	rowStyled := false
	// drewCodeRow records that a code row reached `out` at all, so a body of
	// nothing but blank lines is detectable without pattern-matching the header.
	drewCodeRow := false

	flush := func() {
		if rowStyled {
			// Close the SGR run at the row boundary. Leaving it open lets the
			// colour bleed into the margin and the next row's first token.
			row.WriteString(ansiReset)
		}
		out = append(out, strings.Split(wrap(strings.TrimRight(row.String(), " \t")), "\n")...)
		row.Reset()
		rowCells = 0
		rowStyled = false
		drewCodeRow = true
	}
	newline := func() {
		drewCodeRow = true
		if row.Len() == 0 {
			// Already at a row boundary: this newline was a blank code line.
			out = append(out, block.Indent(""))
			return
		}
		flush()
	}

	for _, token := range iterator.Tokens() {
		if token.Value == "" {
			continue
		}
		ansiStart := sgrForToken(token.Type)
		for fi, frag := range strings.Split(token.Value, "\n") {
			if fi > 0 {
				newline()
			}
			if frag == "" {
				continue
			}
			// Hard-wrap a fragment wider than the content column, re-opening
			// the ANSI run on every continuation row.
			for _, chunk := range hardSplit(frag, contentWidth) {
				chunkCells := ansi.StringWidth(chunk)
				if rowCells > 0 && rowCells+chunkCells > contentWidth {
					flush()
				}
				row.WriteString(ansiStart)
				row.WriteString(chunk)
				rowStyled = true
				rowCells += chunkCells
			}
		}
	}
	if row.Len() > 0 {
		flush()
	} else if !drewCodeRow {
		// The fence body was entirely blank lines. Those are content: fall back
		// to the plain path so the rows are actually drawn.
		appendPlain()
	}

	return strings.Join(out, "\n")
}

// hardSplit splits s into cell-width-bounded rows: literal newlines first, then
// a hard cut at the cell boundary, so an unbreakable token (a 4000-cell base64
// blob, a long path) is cut rather than allowed to blow past the frame. It is
// ANSI-aware — escape sequences are copied through and never counted as cells.
func hardSplit(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	if s == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		out = append(out, strings.Split(ansi.Hardwrap(line, width, true), "\n")...)
	}
	return out
}

// renderCodeSpan renders inline code with subtle emphasis
func renderCodeSpan(node *ast.CodeSpan, source []byte) string {
	return mdCodeSpanStyle.Render(renderInlineContent(node, source))
}

// renderLink renders a link showing only link text (URL hidden per ASK_RENDERING.md)
func renderLink(node *ast.Link, source []byte) string {
	textContent := strings.TrimSpace(renderInlineContent(node, source))
	if textContent == "" {
		return ""
	}

	return mdLinkStyle.Render(textContent)
}

// renderImage renders an image as a placeholder (no binary rendering in terminal)
func renderImage(node *ast.Image, source []byte) string {
	altText := strings.TrimSpace(renderInlineContent(node, source))
	if altText == "" {
		altText = "Image"
	}

	filename := "image"
	if len(node.Destination) > 0 {
		dest := string(node.Destination)
		if lastSlash := strings.LastIndex(dest, "/"); lastSlash >= 0 {
			filename = dest[lastSlash+1:]
		} else {
			filename = dest
		}
	}

	return mdImageMutedStyle.Render(altText) + "\n" + mdImageMutedStyle.Render(filename)
}

// renderMHorizontalRule renders "---" as a full-width horizontal separator
func renderMHorizontalRule(width int) string {
	return mdMutedStyle.Render(strings.Repeat("─", width))
}

// astTableRow is one header or data row of a GFM table, resolved out of the
// goldmark extension AST into plain cell slices so the renderers below can work
// with strings and never touch AST nodes again.
type astTableRow struct {
	cells    []*goldmarkext.TableCell
	aligns   []goldmarkext.Alignment
	isHeader bool
}

// renderASTTable renders a GFM table from the goldmark extension AST node.
// Named renderASTTable to avoid collision with view.go's string-based renderTable.
//
// Column widths come from markdown.BudgetTable rather than from a local
// max-width scan, so this grid and the streaming grid in view.go are bounded by
// the same rule at the same widths and can never disagree about whether a table
// fits.
//
// The frame is CLOSED: a top rule, the header, the header separator, the data
// rows, and a bottom rule. An open-ended grid is not a narrower grid, it is a
// corrupt one — the reader cannot see where the table ends, and the streaming
// renderer that shares this budget draws a closed frame at the same widths, so
// the same markdown would look like two different tables depending on whether it
// arrived during a stream or after it completed.
func renderASTTable(node *goldmarkext.Table, width int, source []byte) string {
	var result strings.Builder

	// cellText extracts plain text from a TableCell.
	//
	// SanitizeCellBreaks is not cosmetic here: the inline walk already turns a
	// `<br>` node into a newline, and this pass catches the forms that arrive as
	// literal text (a tag inside a code span, a cell the stream renderer never
	// saw). Running it unconditionally also makes the two renderers agree, which
	// is the property the shared budget is supposed to imply.
	cellText := func(cell *goldmarkext.TableCell) string {
		return strings.TrimSpace(markdown.SanitizeCellBreaks(renderInlineContent(cell, source)))
	}

	// Collect all rows: first check for a TableHeader child, then TableRow children
	var rows []astTableRow

	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		switch row := child.(type) {
		case *goldmarkext.TableHeader:
			var cells []*goldmarkext.TableCell
			var aligns []goldmarkext.Alignment
			for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
				if tc, ok := cell.(*goldmarkext.TableCell); ok {
					cells = append(cells, tc)
					aligns = append(aligns, tc.Alignment)
				}
			}
			if len(cells) > 0 {
				rows = append(rows, astTableRow{cells: cells, aligns: aligns, isHeader: true})
			}
		case *goldmarkext.TableRow:
			var cells []*goldmarkext.TableCell
			var aligns []goldmarkext.Alignment
			for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
				if tc, ok := cell.(*goldmarkext.TableCell); ok {
					cells = append(cells, tc)
					aligns = append(aligns, tc.Alignment)
				}
			}
			if len(cells) > 0 {
				rows = append(rows, astTableRow{cells: cells, aligns: aligns, isHeader: false})
			}
		}
	}

	if len(rows) == 0 {
		return ""
	}

	numCols := max(len(rows[0].cells), 1)

	// Intrinsic demand: the widest cell LINE in each column across the header
	// AND every data row. Cell width is measured in terminal cells, never bytes
	// or runes, so a CJK header and an ASCII one are budgeted for what they
	// actually occupy on screen — and per LINE, so a `<br>`-broken cell demands
	// the width of its widest line rather than the sum of all of them.
	raw := make([]int, numCols)
	for _, row := range rows {
		for colIdx, cell := range row.cells {
			if colIdx >= numCols {
				continue
			}
			if cw := markdown.CellIntrinsicWidth(cellText(cell)); cw > raw[colIdx] {
				raw[colIdx] = cw
			}
		}
	}

	budget := markdown.BudgetTable(raw, width)
	if !budget.Fits(width) {
		// More columns than the pane can frame at any readable width. A torn
		// frame is not an option, so degrade to a stacked key/value listing.
		return renderTableFallback(rows, cellText, numCols, width)
	}
	colWidths := budget.Widths

	// Top rule, then one logical block per row. A cell that wraps to several
	// physical lines grows its own row instead of desynchronising every column
	// below it — renderTableRow owns that arithmetic exactly once.
	result.WriteString(renderTableRule(colWidths, true))
	result.WriteString("\n")
	for rowIdx, row := range rows {
		// Header separator after header row
		if rowIdx > 0 && rows[rowIdx-1].isHeader {
			result.WriteString(renderTableRule(colWidths, false))
			result.WriteString("\n")
		}

		texts := make([]string, numCols)
		for c := 0; c < numCols; c++ {
			if c < len(row.cells) {
				texts[c] = cellText(row.cells[c])
			}
		}
		result.WriteString(renderTableRow(texts, row.aligns, row.isHeader, colWidths))

		if rowIdx < len(rows)-1 {
			result.WriteString("\n")
		}
	}
	// A newline before the closing rule, ALWAYS — including for a single-row
	// table, where the row loop's own separator never fires. Without it the
	// bottom rule lands on the same line as the last row and the frame reads as
	// corrupted.
	result.WriteString("\n")
	result.WriteString(renderTableBottomRule(colWidths))

	return result.String()
}

// renderTableRule draws a horizontal rule across a bordered grid, aligned to the
// same columns as the rows it separates.
//
// top selects the OPENING rule (corners) rather than the internal one (tees).
//
// top selects the OPENING rule (corners) rather than the internal one (tees): the
// two are the same line of dashes with different terminators, and drawing the
// wrong terminator is how a grid ends up with a bottom edge made of `┼`.
func renderTableRule(colWidths []int, top bool) string {
	var b strings.Builder
	if top {
		b.WriteString("┌")
	} else {
		b.WriteString("├")
	}
	for i, w := range colWidths {
		if i > 0 {
			if top {
				b.WriteString("┬")
			} else {
				b.WriteString("┼")
			}
		}
		b.WriteString(mdSepStyle.Render(strings.Repeat("─", w+2)))
	}
	if top {
		b.WriteString("┐")
	} else {
		b.WriteString("┤")
	}
	return b.String()
}

// renderTableBottomRule draws the CLOSING rule of a bordered grid: a dash line
// terminated by bottom corners. It is separate from renderTableRule because the
// bottom of a frame is not the same glyph row as its top — a grid closed with `┤`
// reads as truncated rather than finished.
func renderTableBottomRule(colWidths []int) string {
	var b strings.Builder
	b.WriteString("└")
	for i, w := range colWidths {
		if i > 0 {
			b.WriteString("┴")
		}
		b.WriteString(mdSepStyle.Render(strings.Repeat("─", w+2)))
	}
	b.WriteString("┘")
	return b.String()
}

// renderTableRow draws one bordered table row of any height: every cell is
// wrapped to its column width, and the row is as tall as its tallest cell. The
// `│` boundaries are re-emitted on every physical line, which is what keeps a
// wrapped cell from tearing the border off the rows beneath it.
func renderTableRow(cells []string, aligns []goldmarkext.Alignment, header bool, colWidths []int) string {
	numCols := len(colWidths)
	heights := make([]int, numCols)
	wrapped := make([][]string, numCols)

	cellStyle := mdCellStyle
	if header {
		cellStyle = mdHeaderBoldCell
	}

	for c := 0; c < numCols; c++ {
		text := ""
		align := goldmarkext.AlignLeft
		if c < len(cells) {
			text = cells[c]
		}
		if c < len(aligns) {
			align = aligns[c]
		}
		// .Width(colWidth).Wrap(true): the width is the wrap budget AND the pad
		// target, so a wrapped cell and an unwrapped one are padded by the same
		// rule and the column edges stay in one vertical line.
		lines := strings.Split(markdown.Cell{Style: cellStyle}.Width(colWidths[c]).Wrap(true).Render(text), "\n")
		if align == goldmarkext.AlignRight {
			lines = alignRight(lines, colWidths[c])
		}
		wrapped[c] = lines
		heights[c] = len(lines)
	}

	height := 1
	for _, h := range heights {
		if h > height {
			height = h
		}
	}

	var b strings.Builder
	for r := 0; r < height; r++ {
		b.WriteString("│")
		for c := 0; c < numCols; c++ {
			cell := ""
			if r < len(wrapped[c]) {
				cell = wrapped[c][r]
			}
			b.WriteString(" " + padMRight(cell, colWidths[c]) + " ")
			b.WriteString("│")
		}
		if r < height-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// alignRight left-pads every already-wrapped line so a right-aligned column
// reads as right-aligned on EVERY physical line, not just the last. Padding only
// the final line is the classic bug here: the column's right edge is then ragged
// and the grid reads as broken.
func alignRight(lines []string, width int) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		pad := width - ansi.StringWidth(l)
		if pad < 0 {
			pad = 0
		}
		out[i] = strings.Repeat(" ", pad) + l
	}
	return out
}

// renderTableFallback re-emits a table as a stacked key/value listing for panes
// too narrow to frame it. It is the honest degradation: the column RELATIONSHIP
// is preserved (every value is labelled with its header) even though the
// alignment cue is not.
func renderTableFallback(rows []astTableRow, cellText func(*goldmarkext.TableCell) string, numCols, width int) string {
	var b strings.Builder
	headers := make([]string, numCols)
	for c := 0; c < numCols; c++ {
		headers[c] = fmt.Sprintf("Col %d", c+1)
	}
	if len(rows) > 0 {
		for c, cell := range rows[0].cells {
			if c < numCols {
				headers[c] = cellText(cell)
			}
		}
	}
	rule := mdSepStyle.Render(strings.Repeat("─", max(width, 1)))
	for rowIdx := 1; rowIdx < len(rows); rowIdx++ {
		if rowIdx > 1 {
			b.WriteString(rule)
			b.WriteString("\n")
		}
		for c := 0; c < numCols; c++ {
			value := ""
			if c < len(rows[rowIdx].cells) {
				value = cellText(rows[rowIdx].cells[c])
			}
			b.WriteString(mdMutedStyle.Render(Icon.Bullet + " " + headers[c] + ": "))
			b.WriteString(markdown.Cell{Style: mdCellStyle}.Width(max(width-2, 1)).Wrap(true).Render(value))
			b.WriteString("\n")
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// ── Helper functions ──────────────────────────────────────────────────────

// wrapMText wraps text to fit within the given width, word-by-word.
// It delegates to the shared ANSI-aware, cell-accurate wrapper so paragraph
// and list-item text is measured by visual cell width, never byte length.
func wrapMText(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}
	return wrapText(text, width)
}

// padMRight pads string s on the right with spaces to the given width
func padMRight(s string, width int) string {
	rw := utf8.RuneCountInString(s)
	if rw >= width {
		return s
	}
	return s + strings.Repeat(" ", width-rw)
}
