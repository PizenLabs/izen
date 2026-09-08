package layer0

import (
	"encoding/json"
	"fmt"
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

func mustConvention(t *testing.T, k *ResolvedKnowledge, name string) Convention {
	t.Helper()
	c, ok := k.Convention(name)
	if !ok {
		t.Fatalf("expected convention %q, active=%+v", name, k.ActiveConventions)
	}
	return c
}

func findConflict(t *testing.T, k *ResolvedKnowledge, name string) *Conflict {
	t.Helper()
	for i := range k.Conflicts {
		if k.Conflicts[i].Name == name {
			return &k.Conflicts[i]
		}
	}
	return nil
}

func TestResolve_GoWorkspace(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":    "module github.com/example/proj\n\ngo 1.26\n",
		"main.go":   "package main\n",
		"README.md": "# proj\nRun `go test ./...` to test.\n",
	})

	k, err := NewKnowledgeResolver(dir).Resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if k.PrimaryManager != ManagerGo {
		t.Fatalf("expected primary manager go, got %s", k.PrimaryManager)
	}
	if len(k.Managers) != 1 || k.Managers[0] != ManagerGo {
		t.Fatalf("expected managers [go], got %v", k.Managers)
	}

	if got := mustConvention(t, k, "install").Value; got != "go mod tidy" {
		t.Fatalf("install: expected go mod tidy, got %s", got)
	}
	if got := mustConvention(t, k, "build").Value; got != "go build ./..." {
		t.Fatalf("build: expected go build ./..., got %s", got)
	}
	if got := mustConvention(t, k, "test").Value; got != "go test ./..." {
		t.Fatalf("test: expected go test ./..., got %s", got)
	}
	if got := mustConvention(t, k, "lint").Value; got != "go vet ./..." {
		t.Fatalf("lint: expected go vet ./..., got %s", got)
	}
	if got := mustConvention(t, k, "format").Value; got != "gofmt -w ." {
		t.Fatalf("format: expected gofmt -w ., got %s", got)
	}
	if c := mustConvention(t, k, "test"); c.Priority != RuntimeFacts {
		t.Fatalf("test convention must be a runtime fact, got priority %s", c.Priority)
	}

	if len(k.Conflicts) != 0 {
		t.Fatalf("expected no conflicts (README confirms runtime), got %+v", k.Conflicts)
	}

	var modulePath *string
	for i := range k.StructuralConstraints {
		if k.StructuralConstraints[i].Kind == "go_module_path" {
			v := k.StructuralConstraints[i].Value
			modulePath = &v
		}
	}
	if modulePath == nil || *modulePath != "github.com/example/proj" {
		t.Fatalf("expected go_module_path constraint, constraints=%+v", k.StructuralConstraints)
	}

	if k.Readme == nil {
		t.Fatal("expected README discovery")
	}
	if k.Readme.Priority != HumanReads {
		t.Fatalf("expected README at HumanReads, got %s", k.Readme.Priority)
	}
}

func TestResolve_NodePnpmWithScripts(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"package.json":   `{"name":"web","scripts":{"build":"next build","test":"jest","lint":"eslint ."}}`,
		"pnpm-lock.yaml": "lockfileVersion: '9.0'",
		"next.config.js": "module.exports = {}",
	})

	k, err := NewKnowledgeResolver(dir).Resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if k.PrimaryManager != ManagerPnpm {
		t.Fatalf("expected primary manager pnpm, got %s", k.PrimaryManager)
	}
	if got := mustConvention(t, k, "install").Value; got != "pnpm install" {
		t.Fatalf("install: expected pnpm install, got %s", got)
	}
	if got := mustConvention(t, k, "build").Value; got != "pnpm run build" {
		t.Fatalf("build: expected pnpm run build, got %s", got)
	}
	if got := mustConvention(t, k, "test").Value; got != "pnpm test" {
		t.Fatalf("test: expected pnpm test, got %s", got)
	}
	if got := mustConvention(t, k, "lint").Value; got != "pnpm run lint" {
		t.Fatalf("lint: expected pnpm run lint, got %s", got)
	}

	var framework *string
	for i := range k.StructuralConstraints {
		if k.StructuralConstraints[i].Kind == "framework" {
			v := k.StructuralConstraints[i].Value
			framework = &v
		}
	}
	if framework == nil || *framework != "next" {
		t.Fatalf("expected framework=next constraint, constraints=%+v", k.StructuralConstraints)
	}
}

func TestResolve_ConflictReadmeVsRuntime(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"package.json":   `{"name":"web"}`,
		"pnpm-lock.yaml": "lockfileVersion: '9.0'",
		"README.md":      "# web\nRun `npm install` and then `npm test`.\n",
	})

	k, err := NewKnowledgeResolver(dir).Resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Runtime facts must override the README's npm install.
	c := mustConvention(t, k, "install")
	if c.Value != "pnpm install" {
		t.Fatalf("install: runtime facts must win, expected pnpm install, got %s", c.Value)
	}
	if c.Priority != RuntimeFacts {
		t.Fatalf("install: expected RuntimeFacts priority, got %s", c.Priority)
	}

	cf := findConflict(t, k, "install")
	if cf == nil {
		t.Fatalf("expected recorded conflict for install, got %+v", k.Conflicts)
	}
	if cf.WinningValue != "pnpm install" || cf.DeclaredValue != "npm install" {
		t.Fatalf("conflict fields wrong: %+v", *cf)
	}
	if cf.WinningAt != RuntimeFacts || cf.DeclaredAt != HumanReads {
		t.Fatalf("conflict priorities wrong: %+v", *cf)
	}
	if !strings.Contains(cf.Resolution, "runtime_facts") {
		t.Fatalf("expected runtime-facts resolution message, got %q", cf.Resolution)
	}

	// No runtime test fact exists, so the README declaration is adopted.
	if c := mustConvention(t, k, "test"); c.Value != "npm test" {
		t.Fatalf("test: expected fallback to README npm test, got %s", c.Value)
	} else if c.Priority != HumanReads {
		t.Fatalf("test: expected HumanReads priority, got %s", c.Priority)
	}
}

func TestResolve_PriorityCascade(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":    "module github.com/example/x\n",
		"AGENTS.md": "# Rules\nUse `pnpm install` for dependencies.\n",
		"README.md": "Run `npm install`.\n",
	})

	k, err := NewKnowledgeResolver(dir).Resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Runtime fact (go) outranks both the agent rule and the README.
	c := mustConvention(t, k, "install")
	if c.Value != "go mod tidy" {
		t.Fatalf("install: expected go mod tidy, got %s", c.Value)
	}

	var agentConflict, readmeConflict *Conflict
	for i := range k.Conflicts {
		switch k.Conflicts[i].DeclaredSrc {
		case "AGENTS.md":
			agentConflict = &k.Conflicts[i]
		case "README.md":
			readmeConflict = &k.Conflicts[i]
		}
	}
	if agentConflict == nil {
		t.Fatalf("expected conflict from AGENTS.md, got %+v", k.Conflicts)
	}
	if readmeConflict == nil {
		t.Fatalf("expected conflict from README.md, got %+v", k.Conflicts)
	}
	if agentConflict.DeclaredAt != AgentRules {
		t.Fatalf("AGENTS conflict should be at AgentRules, got %s", agentConflict.DeclaredAt)
	}
	if readmeConflict.DeclaredAt != HumanReads {
		t.Fatalf("README conflict should be at HumanReads, got %s", readmeConflict.DeclaredAt)
	}
}

func TestResolve_AgentRuleDiscovery(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":            "module github.com/example/x\n",
		"AGENTS.md":         "# root rules",
		".github/AGENTS.md": "# org rules",
		"CLAUDE.md":         "# claude rules",
		".cursorrules":      "rule: always use gofmt",
	})

	k, err := NewKnowledgeResolver(dir).Resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(k.AgentRules) != 4 {
		t.Fatalf("expected 4 agent rule docs, got %d: %+v", len(k.AgentRules), k.AgentRules)
	}
	for _, d := range k.AgentRules {
		if d.Priority != AgentRules {
			t.Fatalf("agent rule %s must be at AgentRules priority, got %s", d.Path, d.Priority)
		}
	}

	// The .cursorrules declares gofmt, which confirms the runtime format fact.
	if c := mustConvention(t, k, "format"); c.Value != "gofmt -w ." {
		t.Fatalf("format: expected gofmt -w ., got %s", c.Value)
	}
}

func TestResolve_ProjectDocsDiscovery(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":                            "module github.com/example/x\n",
		"docs/architecture.md":              "# Architecture\n",
		"docs/architecture/ARCHITECTURE.md": "# Nested arch\n",
		"CONTRIBUTING.md":                   "# Contributing\n",
		"README.md":                         "# Project\n",
	})

	k, err := NewKnowledgeResolver(dir).Resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(k.ProjectDocs) != 3 {
		t.Fatalf("expected 3 project docs, got %d: %+v", len(k.ProjectDocs), k.ProjectDocs)
	}
	for _, d := range k.ProjectDocs {
		if d.Priority != ProjectDocs {
			t.Fatalf("doc %s must be at ProjectDocs priority, got %s", d.Path, d.Priority)
		}
	}
	if k.Readme == nil {
		t.Fatal("expected README discovery")
	}
}

// TestFindInDirReturnsActualOnDiskName guards the case-sensitive filesystem
// regression: findInDir must return the ACTUAL on-disk entry name, never the
// canonical candidate spelling. Reporting "ARCHITECTURE.md" as "architecture.md"
// builds a path that os.Stat cannot resolve on Linux CI, silently dropping the
// doc. This asserts the returned STRING, so it fails deterministically on any
// filesystem.
func TestFindInDirReturnsActualOnDiskName(t *testing.T) {
	dir := t.TempDir()
	// On-disk spelling differs from the canonical candidate name.
	if err := os.WriteFile(filepath.Join(dir, "ARCHITECTURE.md"), []byte("# x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := findInDir(dir, []string{"architecture.md", "ARCHITECTURE.md"})
	if len(got) != 1 {
		t.Fatalf("findInDir = %v, want exactly 1 match", got)
	}
	if got[0] != "ARCHITECTURE.md" {
		t.Fatalf("findInDir = %q, want the actual on-disk name %q", got[0], "ARCHITECTURE.md")
	}
}

// TestResolve_ProjectDocsPreservesOnDiskCase pins the end-to-end contract: a
// nested docs/architecture/ARCHITECTURE.md must be discovered with its real
// path so readDoc resolves it on case-sensitive filesystems. The nested doc
// plus docs/architecture.md and CONTRIBUTING.md must all surface.
func TestResolve_ProjectDocsPreservesOnDiskCase(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":                            "module github.com/example/x\n",
		"docs/architecture.md":              "# Architecture\n",
		"docs/architecture/ARCHITECTURE.md": "# Nested arch\n",
		"CONTRIBUTING.md":                   "# Contributing\n",
	})

	k, err := NewKnowledgeResolver(dir).Resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var nested *SourceDoc
	for i := range k.ProjectDocs {
		if k.ProjectDocs[i].Path == "docs/architecture/ARCHITECTURE.md" {
			nested = &k.ProjectDocs[i]
			break
		}
	}
	if nested == nil {
		t.Fatalf("nested doc missing (paths=%+v)", k.ProjectDocs)
	}
	if !strings.Contains(nested.Head, "Nested arch") {
		t.Fatalf("nested doc head not read (readDoc failed to stat the on-disk path): %+v", nested)
	}
}

func TestResolve_StaticHtmlOnly(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"index.html": "<!doctype html><title>hi</title>",
		"style.css":  "body { color: #333; }",
	})

	k, err := NewKnowledgeResolver(dir).Resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if k.PrimaryManager != ManagerUnknown {
		t.Fatalf("expected unknown primary manager, got %s", k.PrimaryManager)
	}
	if len(k.Managers) != 0 {
		t.Fatalf("expected no managers, got %v", k.Managers)
	}
	if k.Supports("build") || k.Supports("test") {
		t.Fatalf("static site must not fabricate conventions: %+v", k.ActiveConventions)
	}

	var static *string
	for i := range k.StructuralConstraints {
		if k.StructuralConstraints[i].Kind == "static_html_only" {
			v := k.StructuralConstraints[i].Value
			static = &v
		}
	}
	if static == nil || *static != "true" {
		t.Fatalf("expected static_html_only constraint, constraints=%+v", k.StructuralConstraints)
	}
}

func TestResolve_IgnoredDirsExcludedFromTree(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":                  "module github.com/example/x\n",
		"main.go":                 "package main\n",
		"node_modules/foo/lib.js": "export default 1;",
		"dist/bundle.js":          "console.log(1);",
	})

	k, err := NewKnowledgeResolver(dir).Resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, rel := range k.FileTree {
		if strings.Contains(rel, "node_modules") || strings.Contains(rel, "dist/") {
			t.Fatalf("file tree must exclude ignored dirs, found %q", rel)
		}
	}
	if !containsPath(k.FileTree, "main.go") || !containsPath(k.FileTree, "go.mod") {
		t.Fatalf("expected main.go and go.mod in tree, got %v", k.FileTree)
	}
}

func TestResolve_MultipleManagers(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"package.json":      `{"name":"x"}`,
		"package-lock.json": "{}",
		"yarn.lock":         "# yarn",
	})

	k, err := NewKnowledgeResolver(dir).Resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(k.Managers) != 2 {
		t.Fatalf("expected managers [npm yarn], got %v", k.Managers)
	}
	if k.Managers[0] != ManagerNpm || k.Managers[1] != ManagerYarn {
		t.Fatalf("expected sorted managers, got %v", k.Managers)
	}
	if k.PrimaryManager != ManagerYarn {
		t.Fatalf("expected primary yarn by rank, got %s", k.PrimaryManager)
	}

	var mono *string
	for i := range k.StructuralConstraints {
		if k.StructuralConstraints[i].Kind == "monorepo" {
			v := k.StructuralConstraints[i].Value
			mono = &v
		}
	}
	if mono == nil || *mono != "true" {
		t.Fatalf("expected monorepo constraint, constraints=%+v", k.StructuralConstraints)
	}
}

func TestResolve_PythonPoetry(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"pyproject.toml": "[tool.poetry]\nname = \"lib\"\n",
		"poetry.lock":    "[[package]]\n",
	})

	k, err := NewKnowledgeResolver(dir).Resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if k.PrimaryManager != ManagerPoetry {
		t.Fatalf("expected primary poetry, got %s", k.PrimaryManager)
	}
	if got := mustConvention(t, k, "install").Value; got != "poetry install" {
		t.Fatalf("install: expected poetry install, got %s", got)
	}
	if got := mustConvention(t, k, "build").Value; got != "poetry build" {
		t.Fatalf("build: expected poetry build, got %s", got)
	}
}

func TestResolve_PythonUv(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"pyproject.toml": "[project]\nname = \"lib\"\n",
		"uv.lock":        "version = 1\n",
	})

	k, err := NewKnowledgeResolver(dir).Resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if k.PrimaryManager != ManagerUv {
		t.Fatalf("expected primary uv, got %s", k.PrimaryManager)
	}
	if got := mustConvention(t, k, "install").Value; got != "uv sync" {
		t.Fatalf("install: expected uv sync, got %s", got)
	}
}

func TestResolve_NodeNoScriptsNoFakeCommands(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"package.json": `{"name":"lib"}`,
	})

	k, err := NewKnowledgeResolver(dir).Resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := mustConvention(t, k, "install").Value; got != "npm install" {
		t.Fatalf("install: expected npm install, got %s", got)
	}
	if k.Supports("build") || k.Supports("test") {
		t.Fatalf("no scripts -> no build/test conventions, got %+v", k.ActiveConventions)
	}
}

func TestResolve_ConcurrentSafety(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":    "module github.com/example/x\n",
		"main.go":   "package main\n",
		"README.md": "Run `npm install`.\n",
	})

	r := NewKnowledgeResolver(dir)
	const n = 16
	results := make([][]byte, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			k, err := r.Resolve()
			if err != nil {
				t.Errorf("concurrent resolve error: %v", err)
				return
			}
			data, err := json.Marshal(k)
			if err != nil {
				t.Errorf("marshal error: %v", err)
				return
			}
			results[i] = data
		}(i)
	}
	wg.Wait()

	for i := 1; i < n; i++ {
		if string(results[i]) != string(results[0]) {
			t.Fatalf("concurrent resolves diverged at index %d", i)
		}
	}
}

func TestResolve_ConflictingConfig(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":    "module github.com/example/x\n",
		"main.go":   "package main\n",
		"CLAUDE.md": "Run `npm test` to verify.\n",
	})

	k, err := NewKnowledgeResolver(dir).Resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	c := mustConvention(t, k, "test")
	if c.Value != "go test ./..." {
		t.Fatalf("runtime fact must win over CLAUDE.md, got %s", c.Value)
	}
	cf := findConflict(t, k, "test")
	if cf == nil {
		t.Fatalf("expected conflict for test, got %+v", k.Conflicts)
	}
	if cf.DeclaredSrc != "CLAUDE.md" || cf.DeclaredValue != "npm test" {
		t.Fatalf("expected conflict declared by CLAUDE.md, got %+v", *cf)
	}
}

func TestResolve_MissingRoot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	if _, err := NewKnowledgeResolver(dir).Resolve(); err == nil {
		t.Fatal("expected error for missing workspace root")
	}
}

func TestResolve_NotADirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewKnowledgeResolver(file).Resolve(); err == nil {
		t.Fatal("expected error for non-directory root")
	}
}

func TestResolve_DocHeadPreview(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":    "module github.com/example/x\n",
		"README.md": strings.Repeat("line\n", 100),
	})

	k, err := NewKnowledgeResolver(dir).Resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if k.Readme == nil {
		t.Fatal("expected README")
	}
	lines := strings.Count(k.Readme.Head, "\n")
	if lines > 60 {
		t.Fatalf("head preview must be capped, got %d lines", lines)
	}
}

func TestManagerAndPriorityString(t *testing.T) {
	if Priority(RuntimeFacts).String() != "runtime_facts" {
		t.Fatalf("unexpected priority string: %s", RuntimeFacts)
	}
	if fmt.Sprint(HumanReads) != "human_reads" {
		t.Fatalf("unexpected priority string: %s", HumanReads)
	}
}

func containsPath(list []string, want string) bool {
	for _, p := range list {
		if p == want {
			return true
		}
	}
	return false
}
