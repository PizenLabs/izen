package snapshot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectArchetype_GoModule(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n\ngo 1.26\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	archetype := DetectArchetype(dir)
	if archetype != GO_MODULE {
		t.Fatalf("expected GO_MODULE, got %s", archetype)
	}
}

func TestDetectArchetype_VanillaWeb(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"index.html", "style.css", "app.js"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(""), 0644); err != nil {
			t.Fatal(err)
		}
	}

	archetype := DetectArchetype(dir)
	if archetype != VANILLA_WEB {
		t.Fatalf("expected VANILLA_WEB, got %s", archetype)
	}
}

func TestDetectArchetype_NodeApp(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}

	archetype := DetectArchetype(dir)
	if archetype != NODE_APP {
		t.Fatalf("expected NODE_APP, got %s", archetype)
	}
}

func TestDetectArchetype_RustCargo(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package]\nname=\"test\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	archetype := DetectArchetype(dir)
	if archetype != RUST_CARGO {
		t.Fatalf("expected RUST_CARGO, got %s", archetype)
	}
}

func TestDetectArchetype_PythonEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("flask\n"), 0644); err != nil {
		t.Fatal(err)
	}

	archetype := DetectArchetype(dir)
	if archetype != PYTHON_ENV {
		t.Fatalf("expected PYTHON_ENV, got %s", archetype)
	}

	dir2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir2, "setup.py"), []byte("from setuptools import setup\n"), 0644); err != nil {
		t.Fatal(err)
	}

	archetype2 := DetectArchetype(dir2)
	if archetype2 != PYTHON_ENV {
		t.Fatalf("expected PYTHON_ENV for setup.py, got %s", archetype2)
	}

	dir3 := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir3, "pyproject.toml"), []byte("[build-system]\n"), 0644); err != nil {
		t.Fatal(err)
	}

	archetype3 := DetectArchetype(dir3)
	if archetype3 != PYTHON_ENV {
		t.Fatalf("expected PYTHON_ENV for pyproject.toml, got %s", archetype3)
	}

	dir4 := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir4, "Pipfile"), []byte("[[source]]\n"), 0644); err != nil {
		t.Fatal(err)
	}

	archetype4 := DetectArchetype(dir4)
	if archetype4 != PYTHON_ENV {
		t.Fatalf("expected PYTHON_ENV for Pipfile, got %s", archetype4)
	}
}

func TestDetectArchetype_GenericText(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# hello"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data.txt"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	archetype := DetectArchetype(dir)
	if archetype != GENERIC_TEXT {
		t.Fatalf("expected GENERIC_TEXT, got %s", archetype)
	}
}

func TestDetectArchetype_GoModuleTakesPriority(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html></html>"), 0644); err != nil {
		t.Fatal(err)
	}

	archetype := DetectArchetype(dir)
	if archetype != GO_MODULE {
		t.Fatalf("expected GO_MODULE (highest priority), got %s", archetype)
	}
}

func TestBuildSnapshot_Basic(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	snap, err := BuildSnapshot(dir)
	if err != nil {
		t.Fatalf("BuildSnapshot failed: %v", err)
	}

	if snap.Archetype != GO_MODULE {
		t.Fatalf("expected GO_MODULE, got %s", snap.Archetype)
	}
	if !snap.HasManifest("go.mod") {
		t.Fatal("expected go.mod manifest")
	}
	if snap.FileCount() < 1 {
		t.Fatal("expected at least 1 file in tree")
	}
	if _, ok := snap.FileTree["main.go"]; !ok {
		t.Fatal("expected main.go in file tree")
	}
	if snap.RootPath == "" {
		t.Fatal("expected non-empty root path")
	}
}

func TestBuildSnapshot_NonExistentDir(t *testing.T) {
	_, err := BuildSnapshot("/tmp/nonexistent-izen-test-dir-98765")
	if err == nil {
		t.Fatal("expected error for nonexistent directory")
	}
}

func TestBuildSnapshot_FileNotDir(t *testing.T) {
	dir := t.TempDir()
	tmpFile := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(tmpFile, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := BuildSnapshot(tmpFile)
	if err == nil {
		t.Fatal("expected error when passing a file instead of directory")
	}
}

func TestBuildSnapshot_EmptyDir(t *testing.T) {
	dir := t.TempDir()

	snap, err := BuildSnapshot(dir)
	if err != nil {
		t.Fatalf("BuildSnapshot failed for empty dir: %v", err)
	}
	if snap.Archetype != GENERIC_TEXT {
		t.Fatalf("expected GENERIC_TEXT for empty dir, got %s", snap.Archetype)
	}
}

func TestSnapshotCache_GetBuildsOnMiss(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cache := NewSnapshotCache()

	snap, err := cache.GetSnapshot(dir)
	if err != nil {
		t.Fatalf("GetSnapshot failed: %v", err)
	}
	if snap.Archetype != GO_MODULE {
		t.Fatalf("expected GO_MODULE, got %s", snap.Archetype)
	}

	// Second call should return cached version
	snap2, err := cache.GetSnapshot(dir)
	if err != nil {
		t.Fatalf("GetSnapshot (cached) failed: %v", err)
	}
	if snap2.ID != snap.ID {
		t.Fatal("expected same cached snapshot")
	}
}

func TestSnapshotCache_Refreshes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cache := NewSnapshotCache()
	snap, _ := cache.GetSnapshot(dir)
	if snap.Archetype != GO_MODULE {
		t.Fatalf("expected GO_MODULE initially, got %s", snap.Archetype)
	}

	// Remove go.mod and add index.html — archetype should change
	if err := os.Remove(filepath.Join(dir, "go.mod")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html></html>"), 0644); err != nil {
		t.Fatal(err)
	}

	// Without Refresh, cache should still return GO_MODULE
	stale := cache.Current()
	if stale != snap {
		t.Fatal("Current() should return same snapshot before Refresh")
	}

	// After Refresh, should detect VANILLA_WEB
	snap2, err := cache.Refresh(dir)
	if err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}
	if snap2.Archetype != VANILLA_WEB {
		t.Fatalf("expected VANILLA_WEB after Refresh, got %s", snap2.Archetype)
	}
}

func TestHasManifest(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte("all:\n\techo hi\n"), 0644); err != nil {
		t.Fatal(err)
	}

	snap, err := BuildSnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.HasManifest("go.mod") {
		t.Error("expected HasManifest(go.mod) = true")
	}
	if !snap.HasManifest("Makefile") {
		t.Error("expected HasManifest(Makefile) = true")
	}
	if snap.HasManifest("Cargo.toml") {
		t.Error("expected HasManifest(Cargo.toml) = false")
	}
}

func TestBuildSnapshot_SkipsDotDirs(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html></html>"), 0644); err != nil {
		t.Fatal(err)
	}

	snap, err := BuildSnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snap.FileTree[".git/HEAD"]; ok {
		t.Fatal("expected .git/HEAD to be excluded from file tree")
	}
	if _, ok := snap.FileTree["index.html"]; !ok {
		t.Fatal("expected index.html to be in file tree")
	}
}

func TestDetectArchetype_VanillaWebNoHTML(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "styles.css"), []byte("body {}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "script.js"), []byte("console.log('hi')"), 0644); err != nil {
		t.Fatal(err)
	}

	archetype := DetectArchetype(dir)
	if archetype != VANILLA_WEB {
		t.Fatalf("expected VANILLA_WEB for CSS+JS, got %s", archetype)
	}
}
