package recon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectArchetype_GoBackend(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.26\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "handler.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, err := DetectArchetype(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.Type != GO_BACKEND {
		t.Fatalf("expected GO_BACKEND, got %s", ctx.Type)
	}
	if len(ctx.Entrypoints) == 0 {
		t.Fatal("expected at least one entrypoint")
	}
	if ctx.Entrypoints[0] != "main.go" {
		t.Fatalf("expected main.go entrypoint, got %v", ctx.Entrypoints)
	}
	if ctx.HasBuildStep {
		t.Fatal("expected HasBuildStep=false (no Makefile)")
	}
}

func TestDetectArchetype_GoBackendWithMakefile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte("build:\n\tgo build .\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, err := DetectArchetype(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.Type != GO_BACKEND {
		t.Fatalf("expected GO_BACKEND, got %s", ctx.Type)
	}
	if !ctx.HasBuildStep {
		t.Fatal("expected HasBuildStep=true when Makefile exists")
	}
}

func TestDetectArchetype_VanillaWeb(t *testing.T) {
	dir := t.TempDir()
	files := []string{"index.html", "style.css", "app.js"}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(""), 0644); err != nil {
			t.Fatal(err)
		}
	}

	ctx, err := DetectArchetype(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.Type != VANILLA_WEB {
		t.Fatalf("expected VANILLA_WEB, got %s", ctx.Type)
	}
	if len(ctx.Entrypoints) == 0 || ctx.Entrypoints[0] != "index.html" {
		t.Fatalf("expected index.html entrypoint, got %v", ctx.Entrypoints)
	}
	if ctx.HasBuildStep {
		t.Fatal("expected HasBuildStep=false for vanilla web")
	}
}

func TestDetectArchetype_VanillaWebNoIndex(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "page.html"), []byte("<html></html>"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "style.css"), []byte("body {}"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, err := DetectArchetype(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.Type != VANILLA_WEB {
		t.Fatalf("expected VANILLA_WEB, got %s", ctx.Type)
	}
	if len(ctx.Entrypoints) == 0 {
		t.Fatal("expected at least one entrypoint")
	}
}

func TestDetectArchetype_ReactNext(t *testing.T) {
	dir := t.TempDir()
	pkgJSON := `{
		"dependencies": {
			"react": "^18.0.0",
			"react-dom": "^18.0.0"
		}
	}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkgJSON), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html></html>"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, err := DetectArchetype(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.Type != REACT_NEXT {
		t.Fatalf("expected REACT_NEXT, got %s", ctx.Type)
	}
	if !ctx.HasBuildStep {
		t.Fatal("expected HasBuildStep=true for React project")
	}
}

func TestDetectArchetype_NextJS(t *testing.T) {
	dir := t.TempDir()
	pkgJSON := `{
		"dependencies": {
			"next": "^14.0.0",
			"react": "^18.0.0",
			"react-dom": "^18.0.0"
		}
	}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkgJSON), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, err := DetectArchetype(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.Type != REACT_NEXT {
		t.Fatalf("expected REACT_NEXT, got %s", ctx.Type)
	}
}

func TestDetectArchetype_ReactInDevDeps(t *testing.T) {
	dir := t.TempDir()
	pkgJSON := `{
		"devDependencies": {
			"react": "^18.0.0"
		}
	}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkgJSON), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, err := DetectArchetype(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.Type != REACT_NEXT {
		t.Fatalf("expected REACT_NEXT, got %s", ctx.Type)
	}
}

func TestDetectArchetype_UnknownGeneric(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data.txt"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, err := DetectArchetype(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.Type != UNKNOWN_GENERIC {
		t.Fatalf("expected UNKNOWN_GENERIC, got %s", ctx.Type)
	}
	if len(ctx.Entrypoints) != 0 {
		t.Fatalf("expected no entrypoints, got %v", ctx.Entrypoints)
	}
	if ctx.HasBuildStep {
		t.Fatal("expected HasBuildStep=false")
	}
}

func TestDetectArchetype_EmptyDir(t *testing.T) {
	dir := t.TempDir()

	ctx, err := DetectArchetype(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.Type != UNKNOWN_GENERIC {
		t.Fatalf("expected UNKNOWN_GENERIC for empty dir, got %s", ctx.Type)
	}
}

func TestDetectArchetype_NonExistentDir(t *testing.T) {
	_, err := DetectArchetype("/tmp/nonexistent-izen-test-dir-12345")
	if err == nil {
		t.Fatal("expected error for nonexistent directory")
	}
}

func TestDetectArchetype_FileNotDir(t *testing.T) {
	dir := t.TempDir()
	tmpFile := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(tmpFile, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := DetectArchetype(tmpFile)
	if err == nil {
		t.Fatal("expected error when passing a file instead of directory")
	}
}

func TestDetectArchetype_GoBackendHasBuildStep(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte("all:\n\tgo build\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, err := DetectArchetype(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.Type != GO_BACKEND {
		t.Fatalf("expected GO_BACKEND, got %s", ctx.Type)
	}
	if !ctx.HasBuildStep {
		t.Fatal("expected HasBuildStep=true")
	}
}

func TestDetectArchetype_VanillaWebOnlyCSS(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "styles.css"), []byte("body { color: red; }"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, err := DetectArchetype(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.Type != VANILLA_WEB {
		t.Fatalf("expected VANILLA_WEB for CSS-only dir, got %s", ctx.Type)
	}
}

func TestArchetypeContext_String(t *testing.T) {
	ctx := &ArchetypeContext{
		Type:        GO_BACKEND,
		Entrypoints: []string{"main.go"},
	}
	s := ctx.String()
	if s == "" {
		t.Fatal("expected non-empty JSON string")
	}
}

func TestDetectArchetype_IgnoreNodeModules(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node_modules", "react"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html></html>"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, err := DetectArchetype(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx.Type != VANILLA_WEB {
		t.Fatalf("expected VANILLA_WEB (node_modules ignored), got %s", ctx.Type)
	}
}
