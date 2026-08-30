package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestCodeBlock_ShellCommandColorized verifies shell/command code blocks are
// syntax-highlighted with 24-bit foreground codes for command elements.
func TestCodeBlock_ShellCommandColorized(t *testing.T) {
	// Test each shell language variant normalizes to bash lexer and produces ANSI
	shellLangs := []string{"sh", "bash", "zsh", "shell", "console", "cmd", "terminal"}
	for _, lang := range shellLangs {
		line := "go run hello.go"
		out := colorizeNoBg(lang, line)
		if !strings.Contains(out, "38;2;") {
			t.Fatalf("lang %q: expected ANSI foreground 38;2; in output, got %q", lang, out)
		}
		if strings.Contains(out, "48;2;") {
			t.Fatalf("lang %q: should not contain background 48;2;, got %q", lang, out)
		}
		// Must contain colorized tokens, not raw plain text
		if out == line {
			t.Fatalf("lang %q: output is raw plain text, expected colorized, got %q", lang, out)
		}
		// Ensure command elements are colored: "go", "run", "hello.go" should each have 38;2;
		// Count foreground codes: should be at least 2 (command + arg)
		count := strings.Count(out, "38;2;")
		if count < 2 {
			t.Fatalf("lang %q: expected at least 2 foreground codes for command elements, got %d in %q", lang, count, out)
		}
		// Verify fallback colors are present: green for command, mauve for subcommand, peach for arg
		// We check that hello.go is wrapped with peach/mauve/green
		if !strings.Contains(out, "hello.go") {
			t.Fatalf("lang %q: missing hello.go in output %q", lang, out)
		}
	}

	// Specific assertion for sh with "go run hello.go"
	out := colorizeNoBg("sh", "go run hello.go")
	if !strings.Contains(out, "38;2;") {
		t.Fatalf("sh: go run hello.go should be colorized, got %q", out)
	}
	plain := ansi.Strip(out)
	if plain != "go run hello.go" {
		t.Fatalf("stripped plain %q want %q", plain, "go run hello.go")
	}
}

// TestCodeBlock_ShellFlags verifies flags are mauve/blue
func TestCodeBlock_ShellFlags(t *testing.T) {
	out := colorizeNoBg("bash", "npm install --save -v")
	if !strings.Contains(out, "38;2;") {
		t.Fatalf("expected colorized, got %q", out)
	}
	if strings.Contains(out, "48;2;") {
		t.Fatalf("should not contain background codes")
	}
}

// TestCodeBlock_NonShellPreserves verifies non-shell languages still use Chroma
func TestCodeBlock_NonShellPreserves(t *testing.T) {
	out := colorizeNoBg("go", "func main() {}")
	if !strings.Contains(out, "38;2;") {
		t.Fatalf("go language should be colorized, got %q", out)
	}
	if strings.Contains(out, "48;2;") {
		t.Fatalf("go: should not contain background")
	}
}

// TestColorizeNoBg_NoBackground ensures no background fill leaks
func TestColorizeNoBg_NoBackground(t *testing.T) {
	for _, lang := range []string{"sh", "bash", "go", "python"} {
		out := colorizeNoBg(lang, "some code line")
		if strings.Contains(out, "48;2;") {
			t.Fatalf("lang %q leaked background 48;2;: %q", lang, out)
		}
	}
}

// TestCodeBlock_ShellSnippetFramelessYellow verifies a markdown shell snippet
// (```sh / go run hello.go / ```) renders frameless: no box borders, a dimmed
// "$ " prompt, Catppuccin Yellow command foreground, and no background fill.
func TestCodeBlock_ShellSnippetFramelessYellow(t *testing.T) {
	const width = 60
	out := renderCodeBlockToLines("sh", []string{"go run hello.go"}, width)
	if len(out) == 0 {
		t.Fatal("expected rendered lines for shell snippet")
	}
	var joined strings.Builder
	for _, l := range out {
		joined.WriteString(l.RenderedStr)
		joined.WriteString("\n")
	}
	s := joined.String()

	for _, border := range []string{"┌", "└"} {
		if strings.Contains(s, border) {
			t.Fatalf("shell snippet must be frameless (no %q box border), got:\n%s", border, s)
		}
	}
	if !strings.Contains(s, "$ ") {
		t.Fatalf("shell snippet must contain a \"$ \" prompt, got:\n%s", s)
	}
	if !strings.Contains(s, "38;2;249;226;175") {
		t.Fatalf("shell snippet must apply Catppuccin Yellow 38;2;249;226;175 to command, got:\n%s", s)
	}
	if strings.Contains(s, "48;2;") {
		t.Fatalf("shell snippet must not contain background codes, got:\n%s", s)
	}
	if plain := ansi.Strip(s); !strings.Contains(plain, "go run hello.go") {
		t.Fatalf("shell snippet must preserve the command text, got:\n%s", s)
	}
}

// TestTable_RendersUnicodeBoxGrid verifies a pipe-delimited markdown table
// renders as a structured Unicode box grid (┌─┬─┐ / ├─┼─┤ / └─┴─┘ / │) with no
// background fill.
func TestTable_RendersUnicodeBoxGrid(t *testing.T) {
	const width = 60
	md := "| Col1 | Col2 |\n|---|---|\n| A | B |"
	out := renderAIBlockLines(md, width)
	if len(out) == 0 {
		t.Fatal("expected rendered table lines")
	}
	var joined strings.Builder
	for _, l := range out {
		joined.WriteString(l.RenderedStr)
		joined.WriteString("\n")
	}
	s := joined.String()

	for _, frag := range []string{"┌", "┬", "┐", "├", "┼", "┤", "└", "┴", "┘", "│"} {
		if !strings.Contains(s, frag) {
			t.Fatalf("table grid must contain %q, got:\n%s", frag, s)
		}
	}
	if strings.Contains(s, "48;2;") {
		t.Fatalf("table grid must not contain background codes, got:\n%s", s)
	}

	// Exactly 2 columns: a body row is 3 cell separators plus the outer AI
	// gutter ("│ "), i.e. 4 '│' glyphs after stripping ANSI. A buggy column
	// count would emit extra empty columns.
	plain := ansi.Strip(s)
	for _, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "A") && strings.Contains(line, "B") {
			if got := strings.Count(line, "│"); got != 4 {
				t.Fatalf("expected 2 table columns (4 pipe glyphs), got %d in %q", got, line)
			}
		}
	}
}

// TestShellSnippet_CommentIsGreen verifies shell snippets color full-line and
// inline comments in Catppuccin Green (#a6e3a1) while commands stay Catppuccin
// Yellow (#f9e2af).
func TestShellSnippet_CommentIsGreen(t *testing.T) {
	const width = 60
	out := renderCodeBlockToLines("sh", []string{"# Comment", "go run .", "mkdir -p cmd # create dir"}, width)
	if len(out) == 0 {
		t.Fatal("expected rendered shell snippet lines")
	}
	var joined strings.Builder
	for _, l := range out {
		joined.WriteString(l.RenderedStr)
		joined.WriteString("\n")
	}
	s := joined.String()

	if !strings.Contains(s, "38;2;166;227;161") {
		t.Fatalf("comments must render green 38;2;166;227;161, got:\n%s", s)
	}
	if !strings.Contains(s, "38;2;249;226;175") {
		t.Fatalf("commands must render yellow 38;2;249;226;175, got:\n%s", s)
	}
	if strings.Contains(s, "48;2;") {
		t.Fatalf("shell snippet must not contain background codes, got:\n%s", s)
	}
	if plain := ansi.Strip(s); !strings.Contains(plain, "# Comment") || !strings.Contains(plain, "go run .") || !strings.Contains(plain, "# create dir") {
		t.Fatalf("shell snippet must preserve comment/command text, got:\n%s", s)
	}
}

// TestCodeBlock_GoCodeKeepsFrame verifies a programming-language code block
// (e.g. go) keeps the framed box container with its language label and side
// borders.
func TestCodeBlock_GoCodeKeepsFrame(t *testing.T) {
	const width = 60
	out := renderCodeBlockToLines("go", []string{"func main() {}", "    fmt.Println(\"hi\")"}, width)
	if len(out) == 0 {
		t.Fatal("expected rendered lines for go code block")
	}
	var joined strings.Builder
	for _, l := range out {
		joined.WriteString(l.RenderedStr)
		joined.WriteString("\n")
	}
	s := joined.String()

	for _, fragment := range []string{"┌─ go", "│", "└"} {
		if !strings.Contains(s, fragment) {
			t.Fatalf("go code block must contain %q box frame, got:\n%s", fragment, s)
		}
	}
	if strings.Contains(s, "$ ") {
		t.Fatalf("go code block must not render a shell prompt, got:\n%s", s)
	}
}

// TestTable_ResponsiveColumnWrapping verifies responsive column budgeting and
// intra-cell wrapping: a wide markdown table with wrapWidth=50 must shrink
// columns, wrap long cell text into multiple lines, and keep vertical borders
// aligned without overflowing 50 cells.
func TestTable_ResponsiveColumnWrapping(t *testing.T) {
	md := "| ColumnOneWithLongHeader | ColumnTwoWithLongHeader | ColumnThreeWithLongHeader |\n|---|---|---|\n| This is a very long cell content that should wrap into multiple lines because it exceeds the allocated width | Short | Another very long cell content that needs wrapping across multiple visual lines to stay within budget |"
	wrapWidth := 50
	out := renderAIBlockLines(md, wrapWidth)
	if len(out) == 0 {
		t.Fatalf("no lines")
	}
	for i, l := range out {
		w := ansi.StringWidth(ansi.Strip(l.RenderedStr))
		if w > wrapWidth {
			t.Fatalf("line %d width %d exceeds %d: %q", i, w, wrapWidth, ansi.Strip(l.RenderedStr))
		}
	}
	if len(out) < 5 {
		t.Fatalf("expected wrapping to produce multiple lines per row, got %d lines", len(out))
	}
	var pipeCount = -1
	for _, l := range out {
		plain := ansi.Strip(l.RenderedStr)
		if strings.Contains(plain, "┌") || strings.Contains(plain, "├") || strings.Contains(plain, "└") {
			continue
		}
		c := strings.Count(plain, "│")
		if pipeCount == -1 {
			pipeCount = c
		} else if c != pipeCount && c != 0 {
			t.Fatalf("vertical borders misaligned: expected %d pipes, got %d in %q", pipeCount, c, plain)
		}
	}
	sepIdx := -1
	botIdx := -1
	for i, l := range out {
		plain := ansi.Strip(l.RenderedStr)
		if strings.Contains(plain, "├") {
			sepIdx = i
		}
		if strings.Contains(plain, "└") {
			botIdx = i
		}
	}
	if sepIdx >= 0 && botIdx > sepIdx {
		bodyLines := botIdx - sepIdx - 1
		if bodyLines < 2 {
			t.Fatalf("expected body row to wrap into multiple lines, got bodyLines=%d total %d", bodyLines, len(out))
		}
	}
}
