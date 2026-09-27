// code_in_list_test.go — END-TO-END CONTRACTS FOR CODE FENCES IN LIST ITEMS.
//
// The unit tests in internal/ui/markdown pin the decisions (how deep is this
// fence, is the badge shown, what is the margin). These pin the OUTCOME at the
// two surfaces a user actually reads: the completed-response AST renderer and
// the live streaming pipeline. A decision can be correct and still not reach the
// screen, and the failure that motivated this work was exactly that — the AST
// walker's type switch had no *ast.FencedCodeBlock arm, so every nested fence
// was silently deleted from the document.
package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// renderASTText renders markdown through the AST pipeline and strips styling, so
// assertions are about layout and content rather than escape sequences.
func renderASTText(md string, width int) string {
	return ansi.Strip(NewMarkdownRenderer(width).Render(md))
}

// TestCodeInListItemIsNotDropped is the primary regression. `renderListItem`
// dispatched on the children of an *ast.ListItem and had no case for
// *ast.FencedCodeBlock; the default arm of a type switch does not render, it
// DISCARDS, so the code vanished and the numbered step that referenced it became
// a lie. Every one of these documents must come back with its code intact.
func TestCodeInListItemIsNotDropped(t *testing.T) {
	cases := map[string]struct {
		md   string
		want []string
	}{
		"unordered bullet": {
			md:   "- Install deps:\n  ```bash\n  npm install\n  npm run build\n  ```\n- Run it.\n",
			want: []string{"npm install", "npm run build"},
		},
		"ordered list": {
			md:   "1. Install deps:\n\n   ```bash\n   npm install\n   ```\n\n2. Run it.\n",
			want: []string{"npm install"},
		},
		"loose fence with blank lines": {
			md:   "Steps:\n\n- Build:\n\n  ```bash\n  make build\n  ```\n\n- Done.\n",
			want: []string{"make build"},
		},
		"nested two levels": {
			md:   "Steps:\n- outer:\n  - inner:\n    ```go\n    package main\n    ```\n- next\n",
			want: []string{"package main"},
		},
		"fence with blank line inside": {
			md:   "- Code:\n  ```python\n  x = 1\n\n  y = 2\n  ```\n",
			want: []string{"x = 1", "y = 2"},
		},
		"fence with a long unbreakable token": {
			md:   "- Token:\n  ```\n  " + strings.Repeat("A", 300) + "\n  ```\n",
			want: []string{strings.Repeat("A", 300)[:40]},
		},
		"two fences in one item": {
			md:   "- First:\n  ```bash\n  one\n  ```\n  then:\n  ```bash\n  two\n  ```\n",
			want: []string{"one", "two"},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			out := renderASTText(c.md, 60)
			for _, want := range c.want {
				if !strings.Contains(out, want) {
					t.Errorf("rendered output dropped %q\n--- got ---\n%s", want, out)
				}
			}
		})
	}
}

// TestCodeInListItemDrawsNoLanguageHeader is the duplicated-title regression.
// The item's own prose already introduces the language, so a second badge is a
// second claim about the same block — visually two titles, in two styles.
func TestCodeInListItemDrawsNoLanguageHeader(t *testing.T) {
	md := "Steps:\n- Install deps:\n  ```bash\n  npm install\n  ```\n- Run it.\n"
	out := renderASTText(md, 60)
	if strings.Contains(out, "bash") {
		t.Errorf("list-embedded fence drew a language badge:\n%s", out)
	}
	// The prose that introduced it must still be there — suppressing the badge
	// must not suppress the item.
	if !strings.Contains(out, "Install deps:") {
		t.Errorf("the introducing list item was lost along with the badge:\n%s", out)
	}
}

// TestTopLevelFenceStillDrawsItsHeader is the other half: the suppression is
// scoped to NESTED fences. Suppressing it everywhere would leave a bare code
// block completely unlabelled.
func TestTopLevelFenceStillDrawsItsHeader(t *testing.T) {
	out := renderASTText("Here:\n\n```bash\nnpm install\n```\n\nDone.\n", 60)
	if !strings.Contains(out, "bash") {
		t.Errorf("top-level fence lost its language header:\n%s", out)
	}
	if !strings.Contains(out, "npm install") {
		t.Errorf("top-level fence lost its body:\n%s", out)
	}
}

// TestNestedFenceIsIndentedByListDepth pins the dynamic margin at the surface.
// The code must land under its item and its rows must all share one left edge —
// a fence whose rows disagree about their own indent is unreadable as a block.
//
// The expected indent is 2 × depth, which is the whole point of computing it:
// the nested-list machinery has already prefixed the enclosing levels, and a
// fence that charged itself for them again would land at 2 × 2 × depth.
func TestNestedFenceIsIndentedByListDepth(t *testing.T) {
	for _, c := range []struct {
		name  string
		md    string
		rows  []string
		depth int
	}{
		{
			name:  "one level",
			md:    "Steps:\n- item:\n  ```go\n  a := 1\n  b := 2\n  ```\n- next\n",
			rows:  []string{"a := 1", "b := 2"},
			depth: 1,
		},
		{
			name:  "two levels",
			md:    "Steps:\n- outer:\n  - inner:\n    ```go\n    a := 1\n    b := 2\n    ```\n- next\n",
			rows:  []string{"a := 1", "b := 2"},
			depth: 2,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			lines := strings.Split(renderASTText(c.md, 60), "\n")
			var indents []int
			for _, l := range lines {
				trimmed := strings.TrimSpace(l)
				for _, want := range c.rows {
					if trimmed == want {
						indents = append(indents, len(l)-len(strings.TrimLeft(l, " ")))
					}
				}
			}
			if len(indents) != len(c.rows) {
				t.Fatalf("expected %d code rows, found %d in:\n%s",
					len(c.rows), len(indents), strings.Join(lines, "\n"))
			}
			for i, got := range indents {
				if got != indents[0] {
					t.Errorf("code rows disagree about their own indent: %v\n%s",
						indents, strings.Join(lines, "\n"))
					break
				}
				_ = i
			}
			want := 2 * c.depth
			if indents[0] != want {
				t.Errorf("code rows indented %d, want %d (2 cells × %d list level(s))\n%s",
					indents[0], want, c.depth, strings.Join(lines, "\n"))
			}
		})
	}
}

// TestStreamPipelineRecognisesIndentedFences is the LIVE-path regression and the
// more severe of the two. The streaming pipeline tested fences with
// HasPrefix(line, "```"), which is false for every fence inside a list item
// because a nested fence is indented. The markers were therefore never
// recognised as markers: they rendered as literal backticks, the body lost its
// highlighting and its width budget, and the document showed debris where the
// user's commands should have been.
func TestStreamPipelineRecognisesIndentedFences(t *testing.T) {
	cases := map[string]string{
		"unordered":             "Steps:\n- Install:\n  ```bash\n  npm install\n  npm run build\n  ```\n- Run it.\n",
		"ordered":               "Steps:\n1. Install:\n   ```bash\n   npm install\n   ```\n2. Run.\n",
		"nested":                "Steps:\n- outer:\n  - inner:\n    ```go\n    func main() {}\n    ```\n- next\n",
		"top level":             "Steps:\nHere:\n\n```bash\nnpm install\n```\n\nDone.\n",
		"tilde fence":           "Steps:\n- cmd:\n  ~~~sh\n  ls -la\n  ~~~\n- next\n",
		"unterminated":          "Steps:\n- cmd:\n  ```bash\n  npm install\n",
		"empty body":            "Steps:\n- cmd:\n  ```bash\n  ```\n- next\n",
		"blank in body":         "Steps:\n- cmd:\n  ```bash\n  a\n\n  b\n  ```\n- next\n",
		"info string":           "Steps:\n- cmd:\n  ```bash title=setup\n  npm i\n  ```\n- next\n",
		"fence then more items": "Steps:\n- a:\n  ```bash\n  one\n  ```\n- b\n- c\n",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			out := ansi.Strip(RenderDeterministicPipeline(raw, 60, false))
			// No leaked fence markers anywhere: an unrecognised marker is the
			// signature of this bug.
			if strings.Contains(out, "```") || strings.Contains(out, "~~~") {
				t.Errorf("fence markers leaked into the rendered document:\n%s", out)
			}
			// Info-string metadata must not surface either — the terminal has
			// nowhere to show "title=setup".
			if strings.Contains(out, "title=setup") {
				t.Errorf("fence info metadata leaked into the document:\n%s", out)
			}
			// The prose around the fence survived.
			if !strings.Contains(out, "Steps:") {
				t.Errorf("surrounding list was lost:\n%s", out)
			}
		})
	}
}

// TestStreamPipelinePreservesNestedFenceBody: recognising the marker is only half
// the fix — the body must still be rendered, and once, not dropped or doubled.
func TestStreamPipelinePreservesNestedFenceBody(t *testing.T) {
	raw := "Steps:\n- Install:\n  ```bash\n  npm install\n  npm run build\n  ```\n- Run it.\n"
	out := ansi.Strip(RenderDeterministicPipeline(raw, 60, false))
	for _, want := range []string{"npm install", "npm run build", "Install:", "Run it."} {
		if n := strings.Count(out, want); n != 1 {
			t.Errorf("%q appears %d times, want exactly 1:\n%s", want, n, out)
		}
	}
}

// TestStreamPipelineSuppressesNestedHeader mirrors the AST-path rule on the
// stream path, so a fence does not gain or lose its badge depending on which
// renderer happens to be live.
func TestStreamPipelineSuppressesNestedHeader(t *testing.T) {
	nested := ansi.Strip(RenderDeterministicPipeline("- Install:\n  ```bash\n  npm i\n  ```\n", 60, false))
	if strings.Contains(nested, "bash") {
		t.Errorf("nested fence drew a language header on the stream path:\n%s", nested)
	}
	top := ansi.Strip(RenderDeterministicPipeline("```bash\nnpm i\n```\n", 60, false))
	if !strings.Contains(top, "bash") {
		t.Errorf("top-level fence lost its header on the stream path:\n%s", top)
	}
}

// TestStreamPipelineKeepsOneLinePerCodeLine: a lexer emits `func`, ` `, `main`
// as three tokens, and a renderer that treats each token as its own row shatters
// one line of code into three. Blank lines in the body must also survive — a
// dropped blank renumbers everything after it, so a reader copying code out of
// the fence gets the wrong thing.
func TestStreamPipelineKeepsOneLinePerCodeLine(t *testing.T) {
	body := "func main() {\n\n\tprintln(1)\n}"
	raw := "```go\n" + body + "\n```\n"
	out := ansi.Strip(RenderDeterministicPipeline(raw, 80, false))

	lines := strings.Split(out, "\n")
	// The header is line 0, so the three code rows are lines 1..3.
	code := lines[1:]
	if len(code) < 3 {
		t.Fatalf("expected at least 3 code rows, got %d:\n%s", len(code), out)
	}
	if !strings.Contains(code[0], "func main() {") {
		t.Errorf("row 0 = %q, want it to hold the whole first statement on one line", code[0])
	}
	// The blank source line keeps its gutter (that is what makes a run of rows
	// read as one block) but carries no text.
	if trimmed := strings.TrimSpace(strings.TrimLeft(code[1], "│ ")); trimmed != "" {
		t.Errorf("row 1 = %q, want the blank source line preserved as an empty row", code[1])
	}
	if !strings.Contains(code[2], "println(1)") {
		t.Errorf("row 2 = %q, want the second statement", code[2])
	}
}

// TestNestedCodeStaysInsideThePaneWidth is the bounding contract for both
// renderers: a fence's rows, margin and gutter included, must never exceed the
// width the caller budgeted. Overrunning here is what pushes the right border
// off-screen and wraps the terminal.
func TestNestedCodeStaysInsideThePaneWidth(t *testing.T) {
	long := strings.Repeat("echo averyveryverylongtokenwithoutanyspaces ", 4)
	docs := []string{
		"```bash\n" + long + "\n```\n",
		"- cmd:\n  ```bash\n  " + long + "\n  ```\n",
		"- a:\n  - b:\n    ```bash\n    " + long + "\n    ```\n",
		"1. cmd:\n   ```\n   " + long + "\n   ```\n",
	}
	for i, raw := range docs {
		for _, w := range []int{40, 60, 80, 120} {
			for name, out := range map[string]string{
				"stream":  RenderDeterministicPipeline(raw, w, false),
				"astPath": renderASTText(raw, w),
			} {
				widest := 0
				for _, l := range strings.Split(ansi.Strip(out), "\n") {
					if x := ansi.StringWidth(l); x > widest {
						widest = x
					}
				}
				if widest > w {
					t.Errorf("doc %d %s at width %d: widest line %d cells\n%s", i, name, w, widest, out)
				}
			}
		}
	}
}

// TestCodeFenceDoesNotDisturbListOrdering: adding fence support must not break
// the items around it. A renderer that handles the new case by returning early
// would drop every subsequent sibling.
func TestCodeFenceDoesNotDisturbListOrdering(t *testing.T) {
	out := renderASTText("Steps:\n- first\n- second\n  ```bash\n  mid\n  ```\n- third\n- fourth\n", 60)
	for _, want := range []string{"first", "second", "mid", "third", "fourth"} {
		if !strings.Contains(out, want) {
			t.Errorf("item %q was lost when a sibling fence was rendered:\n%s", want, out)
		}
	}
	// Order is the whole point of a numbered procedure, so assert it.
	idx := func(s string) int { return strings.Index(out, s) }
	if idx("first") > idx("second") || idx("second") > idx("mid") ||
		idx("mid") > idx("third") || idx("third") > idx("fourth") {
		t.Errorf("list items are out of order:\n%s", out)
	}
}

// TestDegenerateFencesDoNotPanic: model output is not trusted input, and a
// renderer that panics on a malformed fence takes the whole session with it.
func TestDegenerateFencesDoNotPanic(t *testing.T) {
	for _, raw := range []string{
		"```\n",
		"```bash\n",
		"- a:\n  ```\n",
		"  ```\n  unterminated indented fence\n",
		"``````\n```\n",
		"- a:\n  ```\n  \n  ```\n",
		"```\n\n\n```\n",
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("panic on degenerate input %q: %v", raw, r)
				}
			}()
			_ = RenderDeterministicPipeline(raw, 60, false)
			_ = renderASTText(raw, 60)
		}()
	}
}
