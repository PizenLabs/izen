package execution

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
)

func newToolFixture(t *testing.T) *ReadOnlyToolRunner {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "code.go"), []byte("package sub\n\nfunc Greet() string {\n\treturn \"hi\"\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "blob.bin"), []byte{0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatal(err)
	}
	return NewReadOnlyToolRunner(root)
}

func runTool(t *testing.T, r *ReadOnlyToolRunner, name, args string) string {
	t.Helper()
	out, err := r.Run(context.Background(), ai.ToolCall{ID: "c", Type: "function", Function: ai.ToolCallFunction{Name: name, Arguments: args}})
	if err != nil {
		t.Fatalf("%s(%s): %v", name, args, err)
	}
	return out
}

func TestReadOnlyToolRunner_ReadFile(t *testing.T) {
	r := newToolFixture(t)
	full := runTool(t, r, ai.ToolReadFile, `{"path":"note.txt"}`)
	if !strings.Contains(full, "alpha") || !strings.Contains(full, "gamma") {
		t.Fatalf("read_file = %q, want full contents", full)
	}
	partial := runTool(t, r, ai.ToolReadFile, `{"path":"note.txt","start_line":2,"end_line":2}`)
	if strings.TrimSpace(partial) != "beta" {
		t.Fatalf("read_file range = %q, want beta", partial)
	}
}

func TestReadOnlyToolRunner_ListDirectory(t *testing.T) {
	r := newToolFixture(t)
	out := runTool(t, r, ai.ToolListDirectory, `{"path":""}`)
	if !strings.Contains(out, "note.txt") || !strings.Contains(out, "sub/") {
		t.Fatalf("list_directory = %q, want note.txt and sub/", out)
	}
}

func TestReadOnlyToolRunner_SearchCodebase(t *testing.T) {
	r := newToolFixture(t)
	out := runTool(t, r, ai.ToolSearchCodebase, `{"query":"Greet"}`)
	if !strings.Contains(out, "sub/code.go:3") {
		t.Fatalf("search_codebase = %q, want sub/code.go:3", out)
	}
	// Binary files must never be scanned.
	if strings.Contains(out, "blob.bin") {
		t.Fatalf("search must skip binary files, got %q", out)
	}
}

func TestReadOnlyToolRunner_SymbolLookup(t *testing.T) {
	r := newToolFixture(t)
	out := runTool(t, r, ai.ToolSymbolLookup, `{"symbol":"Greet"}`)
	if !strings.Contains(out, "sub/code.go") || !strings.Contains(out, "func Greet") {
		t.Fatalf("symbol_lookup = %q, want the Greet declaration", out)
	}
}

func TestReadOnlyToolRunner_RejectsTraversalAndUnknown(t *testing.T) {
	r := newToolFixture(t)
	if _, err := r.Run(context.Background(), ai.ToolCall{Function: ai.ToolCallFunction{Name: ai.ToolReadFile, Arguments: `{"path":"../../etc/passwd"}`}}); err == nil {
		t.Fatal("read_file must reject path traversal")
	}
	if _, err := r.Run(context.Background(), ai.ToolCall{Function: ai.ToolCallFunction{Name: ai.ToolWriteFile, Arguments: `{}`}}); err == nil {
		t.Fatal("runner must reject mutating tools")
	}
}
