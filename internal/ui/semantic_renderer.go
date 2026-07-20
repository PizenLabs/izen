package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/lipgloss"
)

// SymbolRenderer renders a presentation-ready symbol card.
type SymbolRenderer struct {
	Width int
}

func (r *SymbolRenderer) Render(v SymbolCardViewModel) string {
	var b strings.Builder
	kind := v.Kind
	if kind == "" {
		kind = "Symbol"
	}
	fmt.Fprintf(&b, "  Type:   %s\n", kind)
	fmt.Fprintf(&b, "  Symbol: %s\n", v.Name)
	if v.Module != "" {
		fmt.Fprintf(&b, "  Module: %s\n", v.Module)
	}
	if v.Language != "" {
		fmt.Fprintf(&b, "  Lang:   %s\n", v.Language)
	}
	return b.String()
}

// RiskRenderer renders a presentation-ready risk card.
type RiskRenderer struct {
	Width int
}

func (r *RiskRenderer) Render(v RiskCardViewModel) string {
	level := v.Level
	if level == "" {
		level = "UNKNOWN"
	}
	reason := v.Reason
	if reason == "" {
		reason = "No computed risks detected"
	}
	return fmt.Sprintf("  Level:  %s\n  Reason: %s\n", level, reason)
}

// ImpactRenderer renders a presentation-ready impact card.
type ImpactRenderer struct {
	Width int
}

func (r *ImpactRenderer) Render(v ImpactCardViewModel) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  Direct:   %d file(s) modified\n", v.DirectCount)
	fmt.Fprintf(&b, "  Indirect: %d downstream caller(s) affected\n", v.IndirectCount)
	if v.RiskScore > 0 {
		fmt.Fprintf(&b, "  Score:    %d/100\n", v.RiskScore)
	}
	if v.HasAPIChanges {
		b.WriteString("  Scope:    PUBLIC API (Breaking Risk)\n")
	} else {
		b.WriteString("  Scope:    Internal implementation\n")
	}
	return b.String()
}

// DiffRenderer renders standard semantic/syntax-highlighted diff regions.
// The new implementation produces a symmetrical single-line-number gutter
// with full-width colored background tracks for additions and deletions.
type DiffRenderer struct {
	Width     int
	IsNewFile bool   // When true, render single-column line numbers (new file creation)
	Language  string // Optional: language hint for Chroma syntax highlighting (e.g. "go", "python")
}

// diffSyntaxHighlight applies Chroma token-based coloring to a single line of
// source code using the Catppuccin Mocha ANSI palette. It returns a styled
// lipgloss string with keywords, strings, comments, and function names colored
// distinctly so the developer can verify syntax correctness at a glance.
// Falls back to the default style when the lexer or tokeniser fails.
func (r *DiffRenderer) diffSyntaxHighlight(line string) string {
	if r.Language == "" {
		return line
	}
	lexer := lexers.Get(r.Language)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)
	it, err := lexer.Tokenise(nil, line)
	if err != nil || it == nil {
		return line
	}
	var b strings.Builder
	for _, tok := range it.Tokens() {
		switch {
		case tok.Type >= chroma.Keyword && tok.Type <= chroma.KeywordType:
			b.WriteString(ansiKeyword)
			b.WriteString(tok.Value)
			b.WriteString(ansiReset)
		case tok.Type >= chroma.NameFunction && tok.Type <= chroma.NameFunctionMagic:
			b.WriteString(ansiFunction)
			b.WriteString(tok.Value)
			b.WriteString(ansiReset)
		case tok.Type >= chroma.String && tok.Type <= chroma.StringSymbol:
			b.WriteString(ansiString)
			b.WriteString(tok.Value)
			b.WriteString(ansiReset)
		case tok.Type >= chroma.Comment && tok.Type <= chroma.CommentPreprocFile:
			b.WriteString(ansiComment)
			b.WriteString(tok.Value)
			b.WriteString(ansiReset)
		case tok.Type >= chroma.LiteralNumber && tok.Type <= chroma.LiteralNumberOct:
			b.WriteString(ansiNumber)
			b.WriteString(tok.Value)
			b.WriteString(ansiReset)
		default:
			b.WriteString(tok.Value)
		}
	}
	return b.String()
}

// langFromPath returns the Chroma-compatible language label for a file path
// by mapping the file extension (e.g. ".go" → "go", ".py" → "python"). Returns
// "" when the extension is unknown, which disables Chroma-based highlighting.
func langFromPath(path string) string {
	switch {
	case strings.HasSuffix(path, ".go"):
		return "go"
	case strings.HasSuffix(path, ".py"):
		return "python"
	case strings.HasSuffix(path, ".rs"):
		return "rust"
	case strings.HasSuffix(path, ".ts") || strings.HasSuffix(path, ".tsx"):
		return "typescript"
	case strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".jsx"):
		return "javascript"
	case strings.HasSuffix(path, ".java"):
		return "java"
	case strings.HasSuffix(path, ".kt") || strings.HasSuffix(path, ".kts"):
		return "kotlin"
	case strings.HasSuffix(path, ".swift"):
		return "swift"
	case strings.HasSuffix(path, ".rb"):
		return "ruby"
	case strings.HasSuffix(path, ".php"):
		return "php"
	case strings.HasSuffix(path, ".cs"):
		return "csharp"
	case strings.HasSuffix(path, ".c") || strings.HasSuffix(path, ".h"):
		return "c"
	case strings.HasSuffix(path, ".cpp") || strings.HasSuffix(path, ".hpp") || strings.HasSuffix(path, ".cc"):
		return "cpp"
	case strings.HasSuffix(path, ".css"):
		return "css"
	case strings.HasSuffix(path, ".html") || strings.HasSuffix(path, ".htm"):
		return "html"
	case strings.HasSuffix(path, ".json"):
		return "json"
	case strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml"):
		return "yaml"
	case strings.HasSuffix(path, ".toml"):
		return "toml"
	case strings.HasSuffix(path, ".sql"):
		return "sql"
	case strings.HasSuffix(path, ".sh") || strings.HasSuffix(path, ".bash"):
		return "bash"
	case strings.HasSuffix(path, ".md"):
		return "markdown"
	default:
		return ""
	}
}

// padToWidth pads s to exactly n visual cells with trailing spaces.
func padToWidth(s string, n int) string {
	w := lipgloss.Width(s)
	if w >= n {
		return s
	}
	return s + strings.Repeat(" ", n-w)
}

func (r *DiffRenderer) Render(v DiffCardViewModel) string {
	if v.Content == "" {
		return ""
	}

	lines := strings.Split(v.Content, "\n")
	var renderedLines []string

	contentWidth := r.Width
	if contentWidth < 20 {
		contentWidth = 20
	}

	// Hunk line tracking
	newLineNum := 1

	for _, line := range lines {
		if strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++") {
			continue
		}

		if strings.HasPrefix(line, "@@") {
			// Parse hunk header for new-file starting line number
			parts := strings.Split(line, "@@")
			if len(parts) >= 2 {
				header := strings.TrimSpace(parts[1])
				fields := strings.Fields(header)
				if len(fields) >= 2 {
					newRange := strings.TrimPrefix(fields[1], "+")
					newParts := strings.Split(newRange, ",")
					if len(newParts) >= 1 {
						if start, err := strconv.Atoi(newParts[0]); err == nil {
							newLineNum = start
						}
					}
				}
			}

			// Render hunk header as metadata line
			hunk := diffHunkStyle.Render(padToWidth(line, contentWidth))
			renderedLines = append(renderedLines, hunk)

			if len(parts) >= 3 {
				sym := strings.TrimSpace(parts[2])
				if sym != "" {
					symLine := diffHunkStyle.Render(padToWidth("  ─── "+sym+" ───", contentWidth))
					renderedLines = append(renderedLines, symLine)
				}
			}
			continue
		}

		if r.IsNewFile {
			switch {
			case strings.HasPrefix(line, "+"):
				clean := strings.TrimPrefix(line, "+")
				gutter := diffLineNumHLSty.Render(fmt.Sprintf("%4d │ ", newLineNum))
				newLineNum++
				highlighted := r.diffSyntaxHighlight(clean)
				body := "+ " + highlighted
				bodyW := contentWidth - lipgloss.Width(gutter)
				if bodyW < 0 {
					bodyW = 0
				}
				row := gutter + semanticAddStyle.Render(padToWidth(body, bodyW))
				renderedLines = append(renderedLines, row)
			case strings.HasPrefix(line, "-"):
				continue
			default:
				gutter := diffLineNumSty.Render(fmt.Sprintf("%4d │ ", newLineNum))
				newLineNum++
				highlighted := r.diffSyntaxHighlight(line)
				bodyW := contentWidth - lipgloss.Width(gutter)
				if bodyW < 0 {
					bodyW = 0
				}
				row := gutter + semanticNormalStyle.Render(padToWidth("  "+highlighted, bodyW))
				renderedLines = append(renderedLines, row)
			}
		} else {
			// STANDARD DIFF — symmetrical single-line-number gutter with syntax highlighting
			switch {
			case strings.HasPrefix(line, "-"):
				clean := strings.TrimPrefix(line, "-")
				highlighted := r.diffSyntaxHighlight(clean)
				gutter := diffLineNumSty.Render(fmt.Sprintf("%4d │ ", newLineNum))
				body := "- " + highlighted
				bodyW := contentWidth - lipgloss.Width(gutter)
				if bodyW < 0 {
					bodyW = 0
				}
				row := gutter + diffDelBgStyle.Render(padToWidth(body, bodyW))
				renderedLines = append(renderedLines, row)

			case strings.HasPrefix(line, "+"):
				clean := strings.TrimPrefix(line, "+")
				highlighted := r.diffSyntaxHighlight(clean)
				gutter := diffLineNumHLSty.Render(fmt.Sprintf("%4d │ ", newLineNum))
				newLineNum++
				body := "+ " + highlighted
				bodyW := contentWidth - lipgloss.Width(gutter)
				if bodyW < 0 {
					bodyW = 0
				}
				row := gutter + diffAddBgStyle.Render(padToWidth(body, bodyW))
				renderedLines = append(renderedLines, row)

			default:
				// Context line with Chroma syntax highlighting
				highlighted := r.diffSyntaxHighlight(line)
				gutter := diffLineNumSty.Render(fmt.Sprintf("%4d │ ", newLineNum))
				newLineNum++
				bodyW := contentWidth - lipgloss.Width(gutter)
				if bodyW < 0 {
					bodyW = 0
				}
				row := gutter + diffCtxStyle.Render(padToWidth("  "+highlighted, bodyW))
				renderedLines = append(renderedLines, row)
			}
		}
	}
	return strings.Join(renderedLines, "\n")
}

// MutationRenderer is the orchestrator that composes sub-renderers to output the final Mutation Card.
type MutationRenderer struct {
	Width        int
	ScrollOffset int
}

func (r *MutationRenderer) Render(v MutationCardViewModel) string {
	contentWidth := r.Width
	if contentWidth < 20 {
		contentWidth = 20
	}

	toggleLabel := dimmedStyle.Render("[▼ Expand]")
	if v.Expanded {
		toggleLabel = dimmedStyle.Render("[▲ Collapse]")
	}
	actionLine := renderHotkeyPromptWithToggle(contentWidth)

	header := "Edit"
	if v.Target.Name != "" {
		symbolName := v.Target.Name
		if dotIdx := strings.LastIndex(symbolName, "."); dotIdx >= 0 {
			symbolName = symbolName[dotIdx+1:]
		}
		if slashIdx := strings.LastIndex(symbolName, "/"); slashIdx >= 0 {
			symbolName = symbolName[slashIdx+1:]
		}
		if symbolName != "" {
			header += ": " + symbolName
		} else {
			header += ": " + v.Target.Name
		}
	} else {
		header += ": Unknown"
	}
	headerLine := boldTextStyle.Render(Icon.Edit+" "+header) + " " + toggleLabel

	scope := "Internal"
	if v.Impact.HasAPIChanges {
		scope = "Public"
	}
	riskLevel := v.Risk.Level
	if riskLevel == "" {
		riskLevel = "UNKNOWN"
	}
	metadataLine := dimmedStyle.Render("Scope: " + scope + " · Risk: " + riskLevel)

	lines := make([]string, 0, 20)
	lines = append(lines, "")
	lines = append(lines, "  "+headerLine)
	lines = append(lines, "  "+metadataLine)
	lines = append(lines, "")

	if v.Expanded {
		if v.Diff.Content != "" {
			dr := &DiffRenderer{Width: contentWidth - 4, IsNewFile: v.IsNewFile, Language: langFromPath(v.Target.Name)}
			diffRendered := dr.Render(v.Diff)
			diffLines := strings.Split(diffRendered, "\n")

			total := len(diffLines)
			start := r.ScrollOffset
			if start >= total {
				start = 0
			}
			end := start + maxProposalDiffHeight
			if end > total {
				end = total
			}

			for _, line := range diffLines[start:end] {
				if len(line) > 0 {
					lines = append(lines, "  "+line)
				}
			}

			if end < total || start > 0 {
				scrollHint := "    " + dimmedStyle.Render("(scroll ↑↓)")
				lines = append(lines, scrollHint)
			}
			lines = append(lines, "")
		}
	}

	lines = append(lines, "  "+actionLine)
	lines = append(lines, "")
	return strings.Join(lines, "\n")
}

// SemanticRenderer legacy wrapper to maintain compatibility while migrating.
type SemanticRenderer struct {
	Width        int
	ScrollOffset int
}

func NewSemanticRenderer(width int) *SemanticRenderer {
	return &SemanticRenderer{Width: width}
}

func (r *SemanticRenderer) RenderMutationCard(m SemanticMutation) string {
	vm := ToMutationCardViewModel(m)
	mr := &MutationRenderer{Width: r.Width, ScrollOffset: r.ScrollOffset}
	return mr.Render(vm)
}
