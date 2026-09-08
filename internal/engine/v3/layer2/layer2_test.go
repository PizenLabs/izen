package layer2

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/lea"
)

// goFixture is a small Go repository: a service package with a struct, an
// interface, methods and functions plus a main package that imports and calls
// into it.
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
`,
	}
}

// tsFixture is a small TypeScript repository: a server module with an
// interface, a type alias, a class and functions, plus a tiny dependency
// module.
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

func newTestSor(t *testing.T, files map[string]string, opts ...Option) *Sor {
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
	return NewSor(e, opts...)
}

func containsStr(hay []string, needle string) bool {
	for _, v := range hay {
		if v == needle {
			return true
		}
	}
	return false
}

func paths(files []FileContext) []string {
	out := make([]string, len(files))
	for i := range files {
		out[i] = files[i].Path
	}
	return out
}

func sourceOf(files []FileContext, path string) string {
	for _, f := range files {
		if f.Path == path {
			return f.Source
		}
	}
	return ""
}
