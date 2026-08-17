package autonomy

import "testing"

func TestCompileHTMLSemanticBlocks(t *testing.T) {
	content := `<!doctype html>
<html>
  <body>
    <header id="site-header">Title</header>
    <main>
      <section class="hero"><h1>Welcome</h1></section>
    </main>
    <footer>Copyright</footer>
  </body>
</html>`
	ctx := CompileContext("index.html", content)
	if ctx.Kind != KindHTML {
		t.Fatalf("kind = %s, want html", ctx.Kind)
	}
	if ctx.HTML == nil {
		t.Fatal("html understanding missing")
	}
	if len(ctx.HTML.Blocks) < 3 {
		t.Errorf("blocks = %d, want >= 3 (header/main/footer)", len(ctx.HTML.Blocks))
	}
	foundHeader := false
	for _, b := range ctx.HTML.Blocks {
		if b.Tag == "header" && b.ID == "site-header" {
			foundHeader = true
		}
	}
	if !foundHeader {
		t.Error("header semantic block with id not found")
	}
}

func TestCompileHTMLOrphanContent(t *testing.T) {
	content := `<html><body>
	  <main><p>Wrapped content</p></main>
	  stray text directly inside body
	</body></html>`
	ctx := CompileContext("index.html", content)
	if len(ctx.HTML.OrphanContent) == 0 {
		t.Error("expected orphan text finding")
	}
	for _, f := range ctx.HTML.OrphanContent {
		if f.Type != "html.orphan_text" {
			t.Errorf("orphan finding type = %s", f.Type)
		}
	}
}

func TestCompileHTMLUnclosedTag(t *testing.T) {
	content := `<html><body><div><p>unclosed p`
	ctx := CompileContext("index.html", content)
	hasUnclosed := false
	for _, f := range ctx.HTML.InvalidRegion {
		if f.Type == "html.unclosed_tag" {
			hasUnclosed = true
		}
	}
	if !hasUnclosed {
		t.Error("expected unclosed tag finding")
	}
}

func TestCompileGoCode(t *testing.T) {
	content := `package server

import (
	"fmt"
	"strings"
)

type Handler struct{}

func (h *Handler) Serve() error {
	fmt.Println(strings.TrimSpace("x"))
	return nil
}

func NewHandler() *Handler {
	return &Handler{}
}`
	ctx := CompileContext("server.go", content)
	if ctx.Kind != KindCode {
		t.Fatalf("kind = %s, want code", ctx.Kind)
	}
	if len(ctx.Code.Symbols) < 3 {
		t.Errorf("symbols = %d, want >= 3 (type Handler, Serve, NewHandler)", len(ctx.Code.Symbols))
	}
	if len(ctx.Code.Dependencies) < 2 {
		t.Errorf("dependencies = %v, want fmt+strings", ctx.Code.Dependencies)
	}
	if len(ctx.Code.AffectedScope) == 0 {
		t.Error("affected scope must be populated")
	}
}

func TestCompilePython(t *testing.T) {
	content := `import os
from datetime import datetime

def handler(event, context):
    return os.environ["X"]

class App:
    pass`
	ctx := CompileContext("app.py", content)
	if len(ctx.Code.Dependencies) == 0 {
		t.Error("python dependencies missing")
	}
	if len(ctx.Code.Symbols) < 2 {
		t.Errorf("python symbols = %d, want >= 2", len(ctx.Code.Symbols))
	}
}

func TestCompileText(t *testing.T) {
	ctx := CompileContext("README.md", "line one\nline two\n")
	if ctx.Kind != KindText {
		t.Fatalf("kind = %s, want text", ctx.Kind)
	}
	if len(ctx.Evidence()) == 0 {
		t.Error("text artifact must produce evidence")
	}
}

func TestCompileHTMLParseErrorDegrades(t *testing.T) {
	// Garbage input must degrade to findings, never panic.
	ctx := CompileContext("index.html", "\x00\x01broken")
	if ctx.HTML == nil {
		t.Fatal("html understanding must still exist")
	}
}

func TestArtifactKindOf(t *testing.T) {
	if KindOf("a.html") != KindHTML {
		t.Error("a.html must be html")
	}
	if KindOf("a.go") != KindCode {
		t.Error("a.go must be code")
	}
	if KindOf("a.md") != KindText {
		t.Error("a.md must be text")
	}
}
