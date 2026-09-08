package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/lea"
)

// goPipelineFixture is a minimal Go repository: a library package, a caller
// package and a go.mod. It exercises knowledge resolution, capability
// detection, the lea SoR, generative execution and validation.
func goPipelineFixture() map[string]string {
	return map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.26\n",
		"svc/service.go": `package svc

// Compute doubles the input.
func Compute(n int) int {
	return n * 2
}

// helper is an internal implementation detail.
func helper(s string) string { return "[" + s + "]" }
`,
		"cmd/app/main.go": `package main

import (
	"fmt"

	"example.com/app/svc"
)

func main() {
	fmt.Println(svc.Compute(2))
}
`,
	}
}

// staticFixture is a pure static HTML/JS workspace with no toolchain. The
// engine must never fabricate build/test commands for it.
func staticFixture() map[string]string {
	return map[string]string{
		"index.html": `<!DOCTYPE html>
<html>
<body><h1>Hello</h1></body>
</html>
`,
		"app.js": `console.log("hi");
`,
	}
}

func writeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return root
}

func indexedEngine(t *testing.T, files map[string]string) (*Engine, *lea.Engine, string) {
	t.Helper()
	root := writeRepo(t, files)
	e := lea.NewEngine(root)
	t.Cleanup(func() { _ = e.Close() })
	stats, err := e.Index(context.Background())
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if stats.Files == 0 {
		t.Fatal("no files indexed")
	}
	eng := NewEngine(root, e)
	return eng, e, root
}
