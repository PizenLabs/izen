package layer1

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDetect_Go(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":  "module github.com/example/proj\n",
		"main.go": "package main\n",
	})

	g, err := Detect(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if g.Stack() != StackGo {
		t.Fatalf("expected stack go, got %s", g.Stack())
	}

	want := map[Capability]string{
		CapBuild:  "go build ./...",
		CapTest:   "go test ./...",
		CapLint:   "go vet ./...",
		CapFormat: "gofmt -w .",
	}
	for cap, cmd := range want {
		if !g.Supports(cap) {
			t.Fatalf("expected support for %s", cap)
		}
		if got, ok := g.Resolve(cap); !ok || got != cmd {
			t.Fatalf("%s: expected %q, got %q (ok=%v)", cap, cmd, got, ok)
		}
	}
}

func TestDetect_GoWithGolangci(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":        "module github.com/example/proj\n",
		".golangci.yml": "version: \"2\"\n",
	})

	g, err := Detect(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, _ := g.Resolve(CapLint); got != "golangci-lint run ./..." {
		t.Fatalf("lint: expected golangci-lint run ./..., got %q", got)
	}
}

func TestDetect_GoWithDockerfile(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":     "module github.com/example/proj\n",
		"Dockerfile": "FROM golang:1.26\n",
	})

	g, err := Detect(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !g.Supports(CapContainer) {
		t.Fatal("expected container support with Dockerfile")
	}
	if got, _ := g.Resolve(CapContainer); got != "docker build -t "+filepath.Base(dir)+" ." {
		t.Fatalf("container: unexpected command %q", got)
	}
}

func TestDetect_NodePnpm(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"package.json":   `{"scripts":{"build":"next build","test":"jest","lint":"eslint ."}}`,
		"pnpm-lock.yaml": "lockfileVersion: '9.0'",
	})

	g, err := Detect(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if g.Stack() != StackNode {
		t.Fatalf("expected stack node, got %s", g.Stack())
	}

	want := map[Capability]string{
		CapBuild: "pnpm run build",
		CapTest:  "pnpm test",
		CapLint:  "pnpm run lint",
	}
	for cap, cmd := range want {
		if got, ok := g.Resolve(cap); !ok || got != cmd {
			t.Fatalf("%s: expected %q, got %q (ok=%v)", cap, cmd, got, ok)
		}
	}
	if g.Supports(CapFormat) {
		t.Fatal("expected no format capability without format script or prettier")
	}
}

func TestDetect_NodeNpmDefaultManager(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"package.json": `{"scripts":{"test":"vitest run"}}`,
	})

	g, err := Detect(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, _ := g.Resolve(CapTest); got != "npm test" {
		t.Fatalf("test: expected npm test, got %q", got)
	}
	if g.Supports(CapBuild) {
		t.Fatal("expected no build capability without build script")
	}
}

func TestDetect_NodeBun(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"package.json": `{"scripts":{"build":"bun build src/index.ts"}}`,
		"bun.lockb":    "binary",
	})

	g, err := Detect(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, _ := g.Resolve(CapBuild); got != "bun run build" {
		t.Fatalf("build: expected bun run build, got %q", got)
	}
}

func TestDetect_NodePrettierFormat(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"package.json": `{"name":"x","scripts":{"test":"jest"}}`,
		".prettierrc":  `{"semi":true}`,
	})

	g, err := Detect(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, _ := g.Resolve(CapFormat); got != "npm exec prettier --write ." {
		t.Fatalf("format: expected prettier fallback, got %q", got)
	}
}

func TestDetect_StaticHtmlCssNoFakeCommands(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"index.html": "<!doctype html><title>hi</title>",
		"style.css":  "body { color: #333; }",
	})

	g, err := Detect(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if g.Stack() != StackStatic {
		t.Fatalf("expected stack static, got %s", g.Stack())
	}

	for _, cap := range []Capability{CapBuild, CapTest, CapLint, CapFormat} {
		if g.Supports(cap) {
			t.Fatalf("static site must not support %s", cap)
		}
		if cmd, ok := g.Resolve(cap); ok || cmd != "" {
			t.Fatalf("%s: expected (\"\", false), got (%q, %v)", cap, cmd, ok)
		}
	}

	data := g.ToCompactJSON()
	if !strings.Contains(string(data), `"stack":"static"`) {
		t.Fatalf("unexpected compact json: %s", data)
	}
}

func TestDetect_StaticWithDockerfile(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"index.html": "<!doctype html>",
		"Dockerfile": "FROM nginx:alpine\n",
	})

	g, err := Detect(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !g.Supports(CapContainer) {
		t.Fatal("static + Dockerfile must support container")
	}
	if g.Supports(CapBuild) {
		t.Fatal("static site must not fabricate build")
	}
}

func TestDetect_Rust(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"Cargo.toml":  "[package]\nname = \"cli\"\n",
		"src/main.rs": "fn main() {}\n",
	})

	g, err := Detect(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if g.Stack() != StackRust {
		t.Fatalf("expected stack rust, got %s", g.Stack())
	}

	want := map[Capability]string{
		CapBuild:  "cargo build",
		CapTest:   "cargo test",
		CapLint:   "cargo clippy --all-targets -- -D warnings",
		CapFormat: "cargo fmt --all",
	}
	for cap, cmd := range want {
		if got, ok := g.Resolve(cap); !ok || got != cmd {
			t.Fatalf("%s: expected %q, got %q (ok=%v)", cap, cmd, got, ok)
		}
	}
}

func TestDetect_PythonPytest(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"pyproject.toml": "[build-system]\nrequires=[\"setuptools\"]\n\n[tool.pytest.ini_options]\n",
		"src/lib.py":     "def f(): return 1\n",
	})

	g, err := Detect(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if g.Stack() != StackPython {
		t.Fatalf("expected stack python, got %s", g.Stack())
	}
	if got, _ := g.Resolve(CapBuild); got != "python -m build" {
		t.Fatalf("build: expected python -m build, got %q", got)
	}
	if got, _ := g.Resolve(CapTest); got != "pytest" {
		t.Fatalf("test: expected pytest, got %q", got)
	}
}

func TestDetect_PythonPoetry(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"pyproject.toml": "[tool.poetry]\nname = \"lib\"\n\n[tool.pytest.ini_options]\n",
		"poetry.lock":    "[[package]]\n",
	})

	g, err := Detect(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, _ := g.Resolve(CapBuild); got != "poetry build" {
		t.Fatalf("build: expected poetry build, got %q", got)
	}
	if got, _ := g.Resolve(CapTest); got != "poetry run pytest" {
		t.Fatalf("test: expected poetry run pytest, got %q", got)
	}
}

func TestDetect_PythonRuff(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"pyproject.toml": "[tool.ruff]\ntarget-version = \"py311\"\n",
	})

	g, err := Detect(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, _ := g.Resolve(CapLint); got != "ruff check ." {
		t.Fatalf("lint: expected ruff check ., got %q", got)
	}
	if got, _ := g.Resolve(CapFormat); got != "ruff format ." {
		t.Fatalf("format: expected ruff format ., got %q", got)
	}
}

func TestDetect_DockerOnly(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"Dockerfile":         "FROM nginx\n",
		"docker-compose.yml": "services:\n  web:\n    build: .\n",
	})

	g, err := Detect(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if g.Stack() != StackDocker {
		t.Fatalf("expected stack docker, got %s", g.Stack())
	}
	if got, _ := g.Resolve(CapContainer); got != "docker compose build" {
		t.Fatalf("container: expected docker compose build, got %q", got)
	}
	if g.Supports(CapBuild) || g.Supports(CapTest) {
		t.Fatal("docker-only workspace must not fabricate build/test")
	}
}

func TestDetect_Unknown(t *testing.T) {
	dir := t.TempDir()
	g, err := Detect(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if g.Stack() != StackUnknown {
		t.Fatalf("expected stack unknown, got %s", g.Stack())
	}
	for _, cap := range AllCapabilities() {
		if g.Supports(cap) {
			t.Fatalf("unknown stack must not support %s", cap)
		}
	}
}

func TestDetect_MissingRoot(t *testing.T) {
	if _, err := Detect(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected error for missing root")
	}
}

func TestDetect_NotADirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Detect(file); err == nil {
		t.Fatal("expected error for non-directory root")
	}
}

func TestConfigOverride_StaticGetsBuild(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"index.html": "<!doctype html>",
	})

	cfg := Config{Commands: map[Capability]string{CapBuild: "npm run build"}}
	g, err := DetectWithConfig(dir, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !g.Supports(CapBuild) {
		t.Fatal("config override must enable build for static site")
	}
	if got, _ := g.Resolve(CapBuild); got != "npm run build" {
		t.Fatalf("build: expected configured command, got %q", got)
	}
	// Test remains off even with a build override.
	if g.Supports(CapTest) {
		t.Fatal("static site must not fabricate test")
	}
}

func TestConfigOverride_Disable(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod": "module github.com/example/proj\n",
	})

	cfg := Config{Commands: map[Capability]string{CapTest: ""}}
	g, err := DetectWithConfig(dir, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if g.Supports(CapTest) {
		t.Fatal("empty override must disable test")
	}
	if !g.Supports(CapBuild) {
		t.Fatal("build must remain supported")
	}
}

func TestToCompactJSON_DeterministicAndWellFormed(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":  "module github.com/example/proj\n",
		"main.go": "package main\n",
	})

	g, err := Detect(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	a := string(g.ToCompactJSON())
	b := string(g.ToCompactJSON())
	if a != b {
		t.Fatalf("compact json must be deterministic: %q vs %q", a, b)
	}

	var doc map[string]any
	if err := json.Unmarshal([]byte(a), &doc); err != nil {
		t.Fatalf("compact json must be valid: %v", err)
	}
	if doc["stack"] != "go" {
		t.Fatalf("expected stack go, got %v", doc["stack"])
	}
	caps, ok := doc["caps"].(map[string]any)
	if !ok {
		t.Fatalf("expected caps object, got %v", doc)
	}
	if caps["build"] != "go build ./..." {
		t.Fatalf("expected build command in caps, got %v", caps)
	}
}

func TestInvalidCapability(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"go.mod": "module x\n"})
	g, err := Detect(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if g.Supports(Capability("deploy")) {
		t.Fatal("unknown capability must not be supported")
	}
	if cmd, ok := g.Resolve(Capability("deploy")); ok || cmd != "" {
		t.Fatalf("expected (\"\", false) for unknown capability")
	}
	if Capability("deploy").Valid() {
		t.Fatal("deploy must be invalid")
	}
	if !CapBuild.Valid() {
		t.Fatal("CapBuild must be valid")
	}
	if CapBuild.String() != "build" {
		t.Fatalf("unexpected string form %q", CapBuild.String())
	}
}

func TestAllCapabilitiesDefensiveCopy(t *testing.T) {
	first := AllCapabilities()
	first[0] = Capability("mutated")
	if got := AllCapabilities(); got[0] != CapBuild {
		t.Fatal("AllCapabilities must return a defensive copy")
	}
	if len(AllCapabilities()) != 5 {
		t.Fatalf("expected 5 capabilities, got %d", len(AllCapabilities()))
	}
}

func TestGraphConcurrentReads(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":        "module github.com/example/proj\n",
		".golangci.yml": "version: \"2\"\n",
	})

	g, err := Detect(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	const n = 16
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if !g.Supports(CapBuild) || !g.Supports(CapLint) {
				t.Error("capabilities vanished during concurrent reads")
			}
			if got, ok := g.Resolve(CapLint); !ok || got != "golangci-lint run ./..." {
				t.Errorf("unexpected lint resolution: %q (ok=%v)", got, ok)
			}
			_ = g.ToCompactJSON()
		}()
	}
	wg.Wait()
}
