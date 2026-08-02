package capabilities

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/domain/ports"
)

const sampleDiff = `--- a/file.go
+++ b/file.go
@@ -1,3 +1,4 @@
 line1
 line2
 line3
+line4
`

const sampleSearchReplace = `<<<<<<< SEARCH
old line
=======
new line
>>>>>>> REPLACE
`

func TestPatchParseDiff(t *testing.T) {
	p := NewPatchAdapter(t.TempDir())
	var _ ports.PatchPort = p

	pp, err := p.Parse(context.Background(), sampleDiff)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if pp.IsFullRewrite {
		t.Error("diff parsed as full rewrite")
	}
	if pp.File != "file.go" {
		t.Errorf("File = %q, want file.go", pp.File)
	}
	if detectStrategy(pp.Modified) != strategyDiff {
		t.Errorf("strategy = %q, want DIFF_PATCH", detectStrategy(pp.Modified))
	}
}

func TestPatchParseSearchReplace(t *testing.T) {
	p := NewPatchAdapter(t.TempDir())
	pp, err := p.Parse(context.Background(), sampleSearchReplace)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if pp.IsFullRewrite {
		t.Error("SEARCH/REPLACE parsed as full rewrite")
	}
	if detectStrategy(pp.Modified) != strategySearchReplace {
		t.Errorf("strategy = %q, want SEARCH_REPLACE", detectStrategy(pp.Modified))
	}
}

func TestPatchParseWholeFile(t *testing.T) {
	p := NewPatchAdapter(t.TempDir())
	pp, err := p.Parse(context.Background(), "```go\npackage main\n```\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !pp.IsFullRewrite {
		t.Error("whole file payload not marked as full rewrite")
	}
	if pp.Modified != "package main\n" {
		t.Errorf("Modified = %q, want fenced content", pp.Modified)
	}
}

func TestPatchParseEmpty(t *testing.T) {
	p := NewPatchAdapter(t.TempDir())
	if _, err := p.Parse(context.Background(), "  \n"); err == nil {
		t.Fatal("Parse of empty payload returned nil error")
	}
}

func TestPatchValidate(t *testing.T) {
	p := NewPatchAdapter(t.TempDir())
	ctx := context.Background()

	if err := p.Validate(ctx, ports.PatchPayload{Modified: sampleDiff}, "line1\nline2\nline3\n"); err != nil {
		t.Errorf("Validate matching diff: %v", err)
	}
	if err := p.Validate(ctx, ports.PatchPayload{Modified: sampleDiff}, "unrelated\ncontent\n"); err == nil {
		t.Error("Validate mismatched diff returned nil error")
	}
	if err := p.Validate(ctx, ports.PatchPayload{Modified: sampleSearchReplace}, "before old line after"); err != nil {
		t.Errorf("Validate matching SEARCH/REPLACE: %v", err)
	}
	if err := p.Validate(ctx, ports.PatchPayload{Modified: sampleSearchReplace}, "no match here"); err == nil {
		t.Error("Validate non-matching SEARCH/REPLACE returned nil error")
	}
	if err := p.Validate(ctx, ports.PatchPayload{Modified: "anything", IsFullRewrite: true}, "ignored"); err != nil {
		t.Errorf("Validate full rewrite: %v", err)
	}
}

func TestPatchApplyUnifiedDiff(t *testing.T) {
	root := t.TempDir()
	p := NewPatchAdapter(root)
	ctx := context.Background()

	file := filepath.Join("src", "file.go")
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, file), []byte("line1\nline2\nline3\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := p.Apply(ctx, ports.PatchPayload{File: file, Modified: sampleDiff})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !res.Applied {
		t.Error("Apply did not report applied")
	}
	if res.LinesAdded != 1 || res.LinesRemoved != 0 {
		t.Errorf("line delta = +%d -%d, want +1 -0", res.LinesAdded, res.LinesRemoved)
	}

	data, err := os.ReadFile(filepath.Join(root, file))
	if err != nil {
		t.Fatalf("read after apply: %v", err)
	}
	if got := string(data); got != "line1\nline2\nline3\nline4\n" {
		t.Errorf("content = %q, want updated content", got)
	}
}

func TestPatchApplySearchReplace(t *testing.T) {
	root := t.TempDir()
	p := NewPatchAdapter(root)
	ctx := context.Background()

	file := "a.txt"
	if err := os.WriteFile(filepath.Join(root, file), []byte("before old line after"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := p.Apply(ctx, ports.PatchPayload{File: file, Modified: sampleSearchReplace}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(root, file))
	if got := string(data); got != "before new line after" {
		t.Errorf("content = %q, want replaced content", got)
	}
}

func TestPatchApplyWholeFileCreates(t *testing.T) {
	root := t.TempDir()
	p := NewPatchAdapter(root)
	ctx := context.Background()

	res, err := p.Apply(ctx, ports.PatchPayload{File: "new.go", Modified: "package new", IsFullRewrite: true})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.LinesAdded != 1 {
		t.Errorf("LinesAdded = %d, want 1", res.LinesAdded)
	}
	if _, err := os.Stat(filepath.Join(root, "new.go")); err != nil {
		t.Error("new file not created")
	}
}

func TestPatchApplyEscapesWorkspace(t *testing.T) {
	root := t.TempDir()
	p := NewPatchAdapter(root)

	_, err := p.Apply(context.Background(), ports.PatchPayload{File: "../escape.go", Modified: "x"})
	if err == nil {
		t.Fatal("Apply with traversal path returned nil error")
	}
	if !strings.Contains(err.Error(), "escape") {
		t.Errorf("error = %q, want escape warning", err)
	}
}

func TestPatchApplyMissingFile(t *testing.T) {
	p := NewPatchAdapter(t.TempDir())
	if _, err := p.Apply(context.Background(), ports.PatchPayload{Modified: "x"}); err == nil {
		t.Fatal("Apply with empty file returned nil error")
	}
}

func TestPatchApplyNonMatching(t *testing.T) {
	p := NewPatchAdapter(t.TempDir())
	file := "f.txt"
	if err := os.WriteFile(filepath.Join(t.TempDir(), file), []byte("other"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Root mismatch: file is not under the adapter root, so it reads as empty.
	_, err := p.Apply(context.Background(), ports.PatchPayload{File: file, Modified: sampleDiff})
	if err == nil {
		t.Fatal("Apply with non-matching hunk returned nil error")
	}
}

func TestLineCounts(t *testing.T) {
	if got := lineCount(""); got != 0 {
		t.Errorf("lineCount('') = %d, want 0", got)
	}
	if got := lineCount("a\nb"); got != 2 {
		t.Errorf("lineCount = %d, want 2", got)
	}
	if got := lineCount("a\nb\n"); got != 3 {
		t.Errorf("lineCount = %d, want 3", got)
	}
}
