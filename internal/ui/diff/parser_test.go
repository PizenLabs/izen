package diff

import (
	"strings"
	"testing"
)

const multiFileDiff = `diff --git a/src/main.go b/src/main.go
index 1234567..89abcde 100644
--- a/src/main.go
+++ b/src/main.go
@@ -12,7 +12,9 @@ func ProcessData() {
 func main() {
 	x := 1
-	fmt.Println("old string")
+	fmt.Println("new string")
+	fmt.Println("extra line")
 	y := 2
 	_ = x
 	_ = y
diff --git a/internal/foo.go b/internal/foo.go
new file mode 100644
index 0000000..1111111
--- /dev/null
+++ b/internal/foo.go
@@ -0,0 +1,3 @@
+package foo
+
+func Foo() {}
diff --git a/old/bar.go b/old/bar.go
deleted file mode 100644
index 2222222..0000000
--- a/old/bar.go
+++ /dev/null
@@ -1,3 +0,0 @@
-package bar
-
-func Bar() {}
`

func TestParseMultiFile(t *testing.T) {
	files, err := ParseUnifiedDiff(multiFileDiff)
	if err != nil {
		t.Fatalf("ParseUnifiedDiff error: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("got %d files, want 3", len(files))
	}
	if files[0].OldPath != "src/main.go" || files[0].NewPath != "src/main.go" {
		t.Errorf("file0 paths = %q/%q", files[0].OldPath, files[0].NewPath)
	}
	if files[0].StatusLabel() != "[MODIFIED]" {
		t.Errorf("file0 status = %q", files[0].StatusLabel())
	}
	if !files[1].IsNew || files[1].StatusLabel() != "[CREATED]" {
		t.Errorf("file1 should be created, got IsNew=%v label=%q", files[1].IsNew, files[1].StatusLabel())
	}
	if !files[2].IsDel || files[2].StatusLabel() != "[DELETED]" {
		t.Errorf("file2 should be deleted, got IsDel=%v label=%q", files[2].IsDel, files[2].StatusLabel())
	}
}

func TestParseLineNumbers(t *testing.T) {
	files, err := ParseUnifiedDiff(multiFileDiff)
	if err != nil {
		t.Fatalf("ParseUnifiedDiff error: %v", err)
	}
	h := files[0].Hunks[0]
	if h.OldStart != 12 || h.OldLength != 7 || h.NewStart != 12 || h.NewLength != 9 {
		t.Fatalf("hunk range = %+v, want 12,7/12,9", h)
	}
	// Expected sequence: ctx(12,12) ctx(13,13) del(14,-) add(-,14) add(-,15) ctx(15,16) ctx(16,17) ctx(17,18)
	type want struct {
		typ      LineType
		old, new int
		text     string
	}
	wants := []want{
		{LineUnchanged, 12, 12, "func main() {"},
		{LineUnchanged, 13, 13, "\tx := 1"},
		{LineDeleted, 14, 0, "\tfmt.Println(\"old string\")"},
		{LineAdded, 0, 14, "\tfmt.Println(\"new string\")"},
		{LineAdded, 0, 15, "\tfmt.Println(\"extra line\")"},
		{LineUnchanged, 15, 16, "\ty := 2"},
		{LineUnchanged, 16, 17, "\t_ = x"},
		{LineUnchanged, 17, 18, "\t_ = y"},
	}
	if len(h.Lines) != len(wants) {
		t.Fatalf("got %d lines, want %d", len(h.Lines), len(wants))
	}
	for i, w := range wants {
		g := h.Lines[i]
		if g.Type != w.typ || g.OldNo != w.old || g.NewNo != w.new || g.Text != w.text {
			t.Errorf("line %d = %+v, want %+v", i, g, w)
		}
	}
}

func TestParseNewFileLineNumbers(t *testing.T) {
	files, err := ParseUnifiedDiff(multiFileDiff)
	if err != nil {
		t.Fatal(err)
	}
	h := files[1].Hunks[0]
	if len(h.Lines) != 3 {
		t.Fatalf("got %d lines", len(h.Lines))
	}
	for i, l := range h.Lines {
		if l.Type != LineAdded || l.OldNo != 0 || l.NewNo != i+1 {
			t.Errorf("line %d = %+v", i, l)
		}
	}
}

func TestParseNoNewlineMarker(t *testing.T) {
	raw := "diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1,2 +1,2 @@\n line1\n-old\n+new\n\\ No newline at end of file\n"
	files, err := ParseUnifiedDiff(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || len(files[0].Hunks) != 1 {
		t.Fatalf("parsed = %+v", files)
	}
	if len(files[0].Hunks[0].Lines) != 3 {
		t.Fatalf("lines = %+v", files[0].Hunks[0].Lines)
	}
}

func TestParseBinary(t *testing.T) {
	raw := "diff --git a/img.png b/img.png\nindex abc..def 100644\nBinary files a/img.png and b/img.png differ\n"
	files, err := ParseUnifiedDiff(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("files = %+v", files)
	}
	if len(files[0].Hunks) != 0 {
		t.Errorf("binary should have 0 hunks, got %d", len(files[0].Hunks))
	}
}

func TestParseMalformedHunkHeader(t *testing.T) {
	raw := "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ not a header @@\n foo\n"
	if _, err := ParseUnifiedDiff(raw); err == nil {
		t.Error("expected error for malformed hunk header")
	}
}

func TestParseEmpty(t *testing.T) {
	files, err := ParseUnifiedDiff("")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Errorf("empty diff should yield 0 files, got %d", len(files))
	}
}

func TestParseComplexHunkNoLengths(t *testing.T) {
	// "@@ -1 +1 @@" omits lengths (implicit 1).
	raw := "--- a/f\n+++ b/f\n@@ -1 +1 @@\n-a\n+b\n"
	files, err := ParseUnifiedDiff(raw)
	if err != nil {
		t.Fatal(err)
	}
	h := files[0].Hunks[0]
	if h.OldLength != 1 || h.NewLength != 1 {
		t.Errorf("lengths = %d/%d, want 1/1", h.OldLength, h.NewLength)
	}
	if len(h.Lines) != 2 {
		t.Fatalf("lines = %+v", h.Lines)
	}
	if h.Lines[0].OldNo != 1 || h.Lines[0].NewNo != 0 {
		t.Errorf("del = %+v", h.Lines[0])
	}
	if h.Lines[1].OldNo != 0 || h.Lines[1].NewNo != 1 {
		t.Errorf("add = %+v", h.Lines[1])
	}
}

func TestRenderPlainLineFormat(t *testing.T) {
	l := DiffLine{Type: LineDeleted, OldNo: 13, Text: "old"}
	s := RenderPlainLine(l)
	if !strings.Contains(s, "13") || !strings.Contains(s, "- old") {
		t.Errorf("plain line = %q", s)
	}
	if !strings.Contains(s, "│") {
		t.Errorf("missing gutter separator in %q", s)
	}
}
