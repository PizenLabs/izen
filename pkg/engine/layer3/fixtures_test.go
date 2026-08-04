package layer3

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/lea"
	"github.com/PizenLabs/izen/pkg/engine/layer2"
)

// goFixture is a small Go repository used by rename tests. It includes a
// package-level function shadowed by a local variable in an unrelated
// function, an interface implemented by Service, and a caller in a separate
// package.
func goFixture() map[string]string {
	return map[string]string{
		"cmd/app/main.go": `package main

import (
	"fmt"

	"github.com/PizenLabs/izen/fixture/svc"
)

func main() {
	s := &svc.Service{}
	err := s.Run()
	if err != nil {
		fmt.Println("failed")
	}
	_ = svc.Compute(2)
}
`,
		"svc/service.go": `package svc

import "fmt"

// Service handles business logic.
type Service struct {
	Name string
}

// Run starts the service.
func (s *Service) Run() error {
	if s.Name == "" {
		return fmt.Errorf("empty name")
	}
	fmt.Println(s.helper())
	return nil
}

// helper formats the service name for display.
func (s *Service) helper() string {
	return s.Name + ":" + compute()
}

// compute derives a stable suffix.
func compute() string {
	return "x1"
}

// Runner is implemented by Service.
type Runner interface {
	Run() error
}

// Compute is the documented public entry point.
func Compute(n int) int {
	if n < 0 {
		return 0
	}
	return n * 2
}

// secretHelper is an internal implementation detail.
func secretHelper(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// other derives an unrelated value; its local Compute shadows the package
// function and must not be renamed by a Compute rename.
func other() int {
	Compute := 5
	return Compute
}
`,
	}
}

// goFormatFixture provides files for format and import-mutation tests.
func goFormatFixture() map[string]string {
	return map[string]string{
		"svc/messy.go": `package svc
func Messy( a int ) int {   return a+1  }
`,
		"svc/unused.go": `package svc

import (
	"fmt"
	"strings"
)

func FmtUsed() string { return fmt.Sprint("x") }
`,
	}
}

// tsFixture is a small TypeScript repository used by the token-level rewriter.
func tsFixture() map[string]string {
	return map[string]string{
		"web/server.ts": `import { thing } from "./dep";

export interface Config {
  host: string;
  port: number;
}

export type Mode = "dev" | "prod";

export class Server {
  start(): void {
    this.setup();
  }

  setup(): void {
    console.log(thing);
  }
}

export function createServer(): Server {
  const s = new Server();
  s.start();
  return s;
}

function internalHelper(): number {
  const value = 42;
  return value;
}
`,
		"web/dep.ts": `export const thing = "hello";
`,
	}
}

func writeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return root
}

func newTestSor(t *testing.T, files map[string]string) *layer2.Sor {
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
	return layer2.NewSor(e)
}

func newTestHandler(t *testing.T, files map[string]string, opts ...ASTRewriteHandlerOption) (*ASTRewriteHandler, *layer2.Sor) {
	t.Helper()
	sor := newTestSor(t, files)
	return NewASTRewriteHandler(sor, opts...), sor
}

func patchFor(t *testing.T, pr *PatchResult, path string) FilePatch {
	t.Helper()
	for _, p := range pr.Files {
		if p.Path == path {
			return p
		}
	}
	t.Fatalf("no patch for %q (have %v)", path, pr.Paths())
	return FilePatch{}
}

// Paths returns the sorted paths patched by a result.
func (r *PatchResult) Paths() []string {
	out := make([]string, 0, len(r.Files))
	for _, f := range r.Files {
		out = append(out, f.Path)
	}
	return out
}

func containsStr(hay []string, needle string) bool {
	for _, v := range hay {
		if v == needle {
			return true
		}
	}
	return false
}
