package ui

import (
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	goldmarkext "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

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

// Render converts Markdown to rendered UI components
func (r *MarkdownRenderer) Render(markdown string) string {
	if markdown == "" {
		return ""
	}

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
	doc := p.Parse(text.NewReader([]byte(markdown)))

	// Process the AST and convert to semantic UI
	return renderAST(doc, r.Width, []byte(markdown))
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

// renderText extracts text from a text node
func renderText(node *ast.Text, source []byte) string {
	return string(node.Value(source))
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

	return styledText + "\n" + styledSeparator
}

// renderInlineContent renders all inline children: text, emphasis, strong, code, links.
// This is the canonical way to extract content from block nodes that contain inline spans.
func renderInlineContent(node ast.Node, source []byte) string {
	var result strings.Builder
	for c := node.FirstChild(); c != nil; c = c.NextSibling() {
		switch c := c.(type) {
		case *ast.Text:
			result.Write(c.Value(source))
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
		default:
			// Fallback: recurse into unknown inline containers
			if c.FirstChild() != nil {
				result.WriteString(renderInlineContent(c, source))
			}
		}
	}
	return result.String()
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

// renderListItem renders a single list item, supporting ordered lists and nested lists
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
				bullet = mdBulletStyle.Render("• ")
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

		case *ast.List:
			// Nested list: indent by 2 spaces
			nestedRendered := renderList(c, width-2, source)
			for _, line := range strings.Split(nestedRendered, "\n") {
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
			rawParts = append(rawParts, string(c.Value(source)))
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

// renderFencedCodeBlock renders a fenced code block with optional language label.
// Uses the node's raw Lines() to correctly extract multi-line code content.
func renderFencedCodeBlock(node *ast.FencedCodeBlock, width int, source []byte) string {
	// Extract language from the info string
	lang := ""
	if node.Info != nil {
		info := strings.TrimSpace(string(node.Info.Segment.Value(source)))
		if spaceIdx := strings.IndexByte(info, ' '); spaceIdx >= 0 {
			lang = info[:spaceIdx]
		} else {
			lang = info
		}
	}

	// Extract code lines from the node's raw line segments
	var codeLines []string
	lines := node.Lines()
	for i := 0; i < lines.Len(); i++ {
		line := lines.At(i)
		codeLines = append(codeLines, strings.TrimRight(string(line.Value(source)), "\n"))
	}

	// Remove trailing empty line if present
	for len(codeLines) > 0 && codeLines[len(codeLines)-1] == "" {
		codeLines = codeLines[:len(codeLines)-1]
	}

	if len(codeLines) == 0 {
		return ""
	}

	var builder strings.Builder

	// Optional language label (muted, no heavy border per ASK_RENDERING.md)
	if lang != "" {
		builder.WriteString(mdMutedStyle.Render(lang))
		builder.WriteString("\n")
	}

	for i, line := range codeLines {
		if i > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString(mdCodeContStyle.Render(line))
	}

	return builder.String()
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

// renderASTTable renders a GFM table from the goldmark extension AST node.
// Named renderASTTable to avoid collision with view.go's string-based renderTable.
func renderASTTable(node *goldmarkext.Table, width int, source []byte) string {
	var result strings.Builder

	// cellText extracts plain text from a TableCell
	cellText := func(cell *goldmarkext.TableCell) string {
		return strings.TrimSpace(renderInlineContent(cell, source))
	}

	// Collect all rows: first check for a TableHeader child, then TableRow children
	type tableRow struct {
		cells    []*goldmarkext.TableCell
		aligns   []goldmarkext.Alignment
		isHeader bool
	}
	var rows []tableRow

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
				rows = append(rows, tableRow{cells: cells, aligns: aligns, isHeader: true})
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
				rows = append(rows, tableRow{cells: cells, aligns: aligns, isHeader: false})
			}
		}
	}

	if len(rows) == 0 {
		return ""
	}

	numCols := len(rows[0].cells)
	colWidths := make([]int, numCols)

	// Calculate column widths
	for _, row := range rows {
		for colIdx, cell := range row.cells {
			if colIdx >= numCols {
				continue
			}
			content := cellText(cell)
			cw := utf8.RuneCountInString(content)
			if cw > colWidths[colIdx] {
				colWidths[colIdx] = cw
			}
		}
	}

	// Enforce minimum column width
	for i := range colWidths {
		if colWidths[i] < 3 {
			colWidths[i] = 3
		}
	}

	for rowIdx, row := range rows {
		// Header separator after header row
		if rowIdx > 0 && rows[rowIdx-1].isHeader {
			for colIdx := 0; colIdx < numCols; colIdx++ {
				if colIdx > 0 {
					result.WriteString("  ")
				}
				result.WriteString(mdSepStyle.Render(strings.Repeat("─", colWidths[colIdx])))
			}
			result.WriteString("\n")
		}

		for colIdx := 0; colIdx < numCols; colIdx++ {
			if colIdx > 0 {
				result.WriteString("  ")
			}

			content := ""
			align := goldmarkext.AlignLeft
			if colIdx < len(row.cells) {
				content = cellText(row.cells[colIdx])
				if colIdx < len(row.aligns) {
					align = row.aligns[colIdx]
				}
			}

			var padded string
			switch align {
			case goldmarkext.AlignRight:
				extra := colWidths[colIdx] - utf8.RuneCountInString(content)
				if extra < 0 {
					extra = 0
				}
				padded = strings.Repeat(" ", extra) + content
			default:
				padded = padMRight(content, colWidths[colIdx])
			}

			if row.isHeader {
				result.WriteString(mdHeaderBoldCell.Render(padded))
			} else {
				result.WriteString(mdCellStyle.Render(padded))
			}
		}

		if rowIdx < len(rows)-1 {
			result.WriteString("\n")
		}
	}

	return result.String()
}

// ── Helper functions ──────────────────────────────────────────────────────

// wrapMText wraps text to fit within the given width, word-by-word
func wrapMText(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}

	inputLines := strings.Split(text, "\n")
	var result []string

	for _, line := range inputLines {
		if line == "" {
			result = append(result, "")
			continue
		}

		words := strings.Fields(line)
		if len(words) == 0 {
			result = append(result, line)
			continue
		}

		var currentLine strings.Builder
		for _, word := range words {
			switch {
			case currentLine.Len() == 0:
				currentLine.WriteString(word)
			case currentLine.Len()+1+utf8.RuneCountInString(word) <= width:
				currentLine.WriteString(" ")
				currentLine.WriteString(word)
			default:
				result = append(result, currentLine.String())
				currentLine.Reset()
				currentLine.WriteString(word)
			}
		}
		if currentLine.Len() > 0 {
			result = append(result, currentLine.String())
		}
	}

	return result
}

// padMRight pads string s on the right with spaces to the given width
func padMRight(s string, width int) string {
	rw := utf8.RuneCountInString(s)
	if rw >= width {
		return s
	}
	return s + strings.Repeat(" ", width-rw)
}
