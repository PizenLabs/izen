// code_block_test.go — NORMALIZATION CONTRACTS FOR LIST-EMBEDDED CODE FENCES.
//
// Each test here corresponds to one of the three failures the module exists to
// prevent (see the package doc in code_block.go): the dropped fence, the
// duplicated title, and the ragged margin. They are written against the exported
// surface rather than the renderer so a regression names the broken invariant
// instead of a diff of terminal output.
package markdown

import (
	"strings"
	"testing"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/text"
)

// TestCodeBlockInListItemSuppressesLanguageHeader pins the duplicated-title
// rule: a fence nested in a list item must not draw a language badge, at every
// depth, and a top-level fence must.
//
// The rule is stated over the whole depth range rather than a single case
// because the failure mode is a rule that was written for depth 1 and never
// generalised — which is how "works for one bullet, doubles the title inside a
// numbered list" ships.
func TestCodeBlockInListItemSuppressesLanguageHeader(t *testing.T) {
	for level := 0; level <= 5; level++ {
		block := NewCodeBlock("bash", []string{"npm install"}, level)
		if got, want := block.ShowLanguage(), level == 0; got != want {
			t.Errorf("level %d: ShowLanguage() = %v, want %v", level, got, want)
		}
		if got, want := block.InListItem(), level > 0; got != want {
			t.Errorf("level %d: InListItem() = %v, want %v", level, got, want)
		}
		if level > 0 && block.HeaderLine() != "" {
			t.Errorf("level %d: HeaderLine() = %q, want empty (a list-embedded fence draws no badge)",
				level, block.HeaderLine())
		}
		if level == 0 && block.HeaderLine() != "bash" {
			t.Errorf("level 0: HeaderLine() = %q, want %q", block.HeaderLine(), "bash")
		}
	}
}

// TestLeftMarginIsTwoCellsPerLevel pins the dynamic margin: 2×level, computed
// rather than assumed. A hardcoded margin per call site is exactly the defect
// this replaces — it is right at depth 1 and wrong everywhere else, and the
// error is invisible until the reader notices the fence is not under its bullet.
func TestLeftMarginIsTwoCellsPerLevel(t *testing.T) {
	for level, want := range map[int]int{0: 0, 1: 2, 2: 4, 3: 6, 7: 14} {
		if got := NewCodeBlock("go", nil, level).LeftMargin(); got != want {
			t.Errorf("level %d: LeftMargin() = %d, want %d", level, got, want)
		}
	}
	// A negative depth is not a level; it must degrade to top level rather than
	// produce a negative indent that walks the content off the left edge.
	if got := NewCodeBlock("go", nil, -3).LeftMargin(); got != 0 {
		t.Errorf("negative level: LeftMargin() = %d, want 0", got)
	}
}

// TestContentWidthSubtractsTheMargin is the anti-tearing property: the wrap
// budget and the indent come from ONE number, so a wrapped line can never be
// wrapped at the un-indented width and then indented afterwards. That ordering
// bug is worth one cell of width per level, and the cell that goes missing is
// always the right border.
func TestContentWidthSubtractsTheMargin(t *testing.T) {
	const available = 60
	for level := 0; level <= 4; level++ {
		block := NewCodeBlock("go", nil, level)
		want := available - ListIndentPerLevel*level
		if got := block.ContentWidth(available); got != want {
			t.Errorf("level %d: ContentWidth(%d) = %d, want %d", level, available, got, want)
		}
	}
	// The margin is floored, not allowed to go negative: a deep fence in a
	// narrow pane still gets a usable content column.
	deep := NewCodeBlock("go", nil, 100)
	if got := deep.ContentWidth(20); got < 1 {
		t.Errorf("ContentWidth with an overwhelming margin = %d, want >= 1", got)
	}
}

// TestSplitFenceAcceptsIndentedMarkers is the streaming-path fix. The previous
// test was HasPrefix(line, "```"), which is FALSE for every fence inside a list
// item — so a nested fence leaked its markers into the document as literal
// backticks and its body lost highlighting, width, and block identity.
func TestSplitFenceAcceptsIndentedMarkers(t *testing.T) {
	cases := []struct {
		line     string
		indent   int
		language string
		ok       bool
	}{
		{"```bash", 0, "bash", true},
		{"```", 0, "", true},
		{"  ```bash", 2, "bash", true},
		{"    ```go", 4, "go", true},
		{"\t```sh", 1, "sh", true},
		{"~~~python", 0, "python", true},
		{"```go title=main.go", 0, "go", true},
		// A line that merely contains backticks is not a marker.
		{"npm run `build`", 0, "", false},
		{"a```b", 0, "", false},
		{"", 0, "", false},
		{"``", 0, "", false},
		{"plain text", 0, "", false},
	}
	for _, c := range cases {
		indent, lang, ok := SplitFence(c.line)
		if ok != c.ok {
			t.Errorf("SplitFence(%q) ok = %v, want %v", c.line, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if indent != c.indent || lang != c.language {
			t.Errorf("SplitFence(%q) = (%d, %q), want (%d, %q)", c.line, indent, lang, c.indent, c.language)
		}
	}
}

// TestIndentLevelUsesTheIndentQuantum ties an observed indent back to a list
// depth. The floor for a non-multiple is deliberate: under-indenting a fence by
// one cell is cosmetic, over-indenting it pushes the right border off-screen.
func TestIndentLevelUsesTheIndentQuantum(t *testing.T) {
	for indent, want := range map[int]int{0: 0, 1: 0, 2: 1, 3: 1, 4: 2, 6: 3, 8: 4} {
		if got := IndentLevel(indent); got != want {
			t.Errorf("IndentLevel(%d) = %d, want %d", indent, got, want)
		}
	}
}

// TestDedentRemovesStructuralIndent covers the other half of nesting: a nested
// fence's body is indented in the source to line up with its marker, and that
// indent must be stripped BEFORE the fence's own margin is applied, or the
// block sits visibly to the right of the item that owns it.
func TestDedentRemovesStructuralIndent(t *testing.T) {
	t.Run("strips the common prefix", func(t *testing.T) {
		got := Dedent([]string{"  npm install", "  npm run build"})
		want := []string{"npm install", "npm run build"}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("Dedent = %q, want %q", got, want)
		}
	})
	t.Run("keeps relative indentation", func(t *testing.T) {
		// The extra two cells are the code's own nesting (an if-block inside
		// the function) and must survive: Dedent removes the SHARED prefix, not
		// indentation, or every block loses its structure.
		got := Dedent([]string{"  func main() {", "      println(1)", "  }"})
		want := []string{"func main() {", "    println(1)", "}"}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("Dedent = %q, want %q", got, want)
		}
	})
	t.Run("blank lines do not defeat the common prefix", func(t *testing.T) {
		// A whitespace-only line would otherwise measure a prefix of its own
		// length and collapse the common prefix to zero, silently disabling the
		// whole operation for any block containing a blank line — which is
		// nearly every block.
		got := Dedent([]string{"  a", "", "  b"})
		want := []string{"a", "", "b"}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("Dedent = %q, want %q", got, want)
		}
	})
	t.Run("unindented input is unchanged", func(t *testing.T) {
		in := []string{"npm install", "npm run build"}
		if strings.Join(Dedent(in), "|") != strings.Join(in, "|") {
			t.Error("Dedent modified unindented input")
		}
	})
}

// parseFences parses src and returns every fenced code block with its resolved
// language and list depth, exercising the same read-only walk a renderer uses.
func parseFences(t *testing.T, src string) []CodeBlock {
	t.Helper()
	md := goldmark.New(goldmark.WithExtensions(extension.Table))
	source := []byte(src)
	doc := md.Parser().Parse(text.NewReader(source))
	var out []CodeBlock
	VisitFencedCodeBlocks(doc, source, func(b CodeBlock) { out = append(out, b) })
	return out
}

// TestListDepthCountsListItemAncestors is the depth measurement the whole module
// keys off. It reads the AST's own parent chain, so it cannot be out of sync
// with the document the way a counter threaded through a recursive renderer can.
func TestListDepthCountsListItemAncestors(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		depth int
	}{
		{"top level", "```\nx\n```\n", 0},
		{"one bullet", "- item\n  ```\n  x\n  ```\n", 1},
		{"ordered", "1. item\n   ```\n   x\n   ```\n", 1},
		{"two levels", "- a\n  - b\n    ```\n    x\n    ```\n", 2},
		{"outside a list", "text\n\n```\nx\n```\n", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fences := parseFences(t, c.src)
			if len(fences) != 1 {
				t.Fatalf("found %d fences, want 1", len(fences))
			}
			if fences[0].Level != c.depth {
				t.Errorf("level = %d, want %d", fences[0].Level, c.depth)
			}
			if got, want := fences[0].LeftMargin(), ListIndentPerLevel*c.depth; got != want {
				t.Errorf("LeftMargin() = %d, want %d", got, want)
			}
		})
	}
}

// TestFenceLanguageAndLines pins the AST read. The info string and the line
// contents are stored as offsets into the source, so a caller that forgets to
// pass the source gets empty strings — this asserts both are actually resolved.
func TestFenceLanguageAndLines(t *testing.T) {
	fences := parseFences(t, "```go title=demo\npackage main\n\nfunc main() {}\n```\n")
	if len(fences) != 1 {
		t.Fatalf("found %d fences, want 1", len(fences))
	}
	f := fences[0]
	if f.Language != "go" {
		t.Errorf("Language = %q, want %q (info metadata must be dropped)", f.Language, "go")
	}
	want := []string{"package main", "", "func main() {}"}
	if len(f.Lines) != len(want) {
		t.Fatalf("Lines = %q, want %q", f.Lines, want)
	}
	for i := range want {
		if f.Lines[i] != want[i] {
			t.Errorf("Lines[%d] = %q, want %q", i, f.Lines[i], want[i])
		}
	}
	// The trailing newline on the LAST line must be stripped too, or the fence
	// always renders one phantom blank row.
	if strings.HasSuffix(f.Lines[len(f.Lines)-1], "\n") {
		t.Error("last line still carries a trailing newline")
	}
}

// TestVisitFencedCodeBlocksToleratesMissingInputs: a walker that panics on a nil
// subtree forces every caller to guard its own traversal, which is how walkers
// accumulate redundant, slightly different guards.
func TestVisitFencedCodeBlocksToleratesMissingInputs(t *testing.T) {
	VisitFencedCodeBlocks(nil, nil, func(CodeBlock) { t.Error("callback ran for a nil node") })
	md := goldmark.New()
	doc := md.Parser().Parse(text.NewReader([]byte("hello")))
	VisitFencedCodeBlocks(doc, []byte("hello"), nil) // nil callback: no panic
}

// TestFenceAccessorsTolerateNil is the same guarantee one level down: a nil
// fence is a legitimate "nothing here" value, not a crash.
func TestFenceAccessorsTolerateNil(t *testing.T) {
	if got := FenceLanguage(nil, nil); got != "" {
		t.Errorf("FenceLanguage(nil) = %q, want empty", got)
	}
	if got := FenceLines(nil, nil); got != nil {
		t.Errorf("FenceLines(nil) = %q, want nil", got)
	}
	if got := ListDepth(nil); got != 0 {
		t.Errorf("ListDepth(nil) = %d, want 0", got)
	}
}

// TestListDepthIgnoresNonListAncestors: a fence inside a blockquote that is
// inside a list is at depth 1, and a fence inside two sibling lists is still at
// depth 1. Depth counts CONTAINMENT, not the number of ancestors.
func TestListDepthIgnoresNonListAncestors(t *testing.T) {
	fences := parseFences(t, "> - quoted item\n>   ```\n>   x\n>   ```\n")
	if len(fences) != 1 {
		t.Fatalf("found %d fences, want 1", len(fences))
	}
	if fences[0].Level != 1 {
		t.Errorf("level = %d, want 1", fences[0].Level)
	}
}

// TestCodeBlockZeroValueIsTopLevel documents that the zero value is usable and
// means "not in a list", so a caller that constructs the struct literally is
// never silently treated as nested.
func TestCodeBlockZeroValueIsTopLevel(t *testing.T) {
	var zero CodeBlock
	if zero.InListItem() || !zero.ShowLanguage() {
		t.Error("the zero value must be a top-level fence")
	}
	if zero.LeftMargin() != 0 {
		t.Errorf("zero LeftMargin = %d, want 0", zero.LeftMargin())
	}
	if got := zero.Indent("x"); got != "x" {
		t.Errorf("zero Indent = %q, want %q", got, "x")
	}
}

// TestIndentDoesNotPadEmptyLines: a margin is a prefix, and prefixing an empty
// string with spaces manufactures trailing whitespace on blank rows inside a
// fence.
func TestIndentDoesNotPadEmptyLines(t *testing.T) {
	block := NewCodeBlock("go", nil, 2)
	if got := block.Indent(""); got != "" {
		t.Errorf("Indent(\"\") = %q, want empty", got)
	}
	if got := block.Indent("x"); got != "    x" {
		t.Errorf("Indent(\"x\") = %q, want %q", got, "    x")
	}
}
