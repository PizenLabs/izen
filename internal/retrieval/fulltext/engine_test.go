package fulltext

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestIndexAndSearch(t *testing.T) {
	dir := t.TempDir()
	eng := NewEngine(dir)

	writeTestFile(t, dir, "main.go", `package main

import "fmt"

func main() {
	fmt.Println("hello world")
}
`)

	ok := eng.IndexFile("main.go")
	if !ok {
		t.Fatal("IndexFile returned false")
	}
	if eng.DocCount() != 1 {
		t.Fatalf("expected 1 doc, got %d", eng.DocCount())
	}
	if eng.TokenCount() == 0 {
		t.Fatal("expected non-zero token count")
	}

	matches, err := eng.Search(context.Background(), "fmt", DefaultSearchOptions())
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("expected matches for 'fmt'")
	}
	found := false
	for _, m := range matches {
		if strings.Contains(m.Content, "fmt") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected content containing 'fmt'")
	}

	matches, err = eng.Search(context.Background(), "println", DefaultSearchOptions())
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("expected matches for 'println'")
	}
}

func TestExactSearch(t *testing.T) {
	dir := t.TempDir()
	eng := NewEngine(dir)

	writeTestFile(t, dir, "server.go", `package server

type Server struct {
	Port int
}

func NewServer(port int) *Server {
	return &Server{Port: port}
}
`)

	eng.IndexFile("server.go")

	opts := DefaultSearchOptions()
	opts.Exact = true

	matches, err := eng.Search(context.Background(), "NewServer", opts)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("expected exact match for 'NewServer'")
	}
	if matches[0].Score < 0.8 {
		t.Fatalf("expected high score for exact match, got %.3f", matches[0].Score)
	}
}

func TestPhraseSearch(t *testing.T) {
	dir := t.TempDir()
	eng := NewEngine(dir)

	writeTestFile(t, dir, "handler.go", `package handler

import (
	"net/http"
)

func GetUser(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("user data"))
}
`)

	eng.IndexFile("handler.go")

	opts := DefaultSearchOptions()
	opts.Phrase = true

	matches, err := eng.Search(context.Background(), "write header", opts)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) == 0 {
		matches, err = eng.Search(context.Background(), "WriteHeader", opts)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(matches) == 0 {
			t.Fatal("expected phrase match for 'WriteHeader'")
		}
	}
}

func TestFuzzySearch(t *testing.T) {
	dir := t.TempDir()
	eng := NewEngine(dir)

	writeTestFile(t, dir, "utils.go", `package utils

func Helper() string {
	return "help"
}
`)

	eng.IndexFile("utils.go")

	opts := DefaultSearchOptions()
	opts.Fuzzy = true

	matches, err := eng.Search(context.Background(), "helperr", opts)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("expected fuzzy match for 'helperr' -> 'Helper'")
	}
}

func TestMultipleFiles(t *testing.T) {
	dir := t.TempDir()
	eng := NewEngine(dir)

	writeTestFile(t, dir, "a.go", `package a
func Foo() string { return "foo" }
`)
	writeTestFile(t, dir, "b.go", `package b
func Bar() string { return "bar" }
`)
	writeTestFile(t, dir, "sub/c.go", `package c
func Baz() string { return "baz" }
`)

	eng.IndexFile("a.go")
	eng.IndexFile("b.go")
	eng.IndexFile("sub/c.go")

	if eng.DocCount() != 3 {
		t.Fatalf("expected 3 docs, got %d", eng.DocCount())
	}

	matches, err := eng.Search(context.Background(), "foo", DefaultSearchOptions())
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("expected match for 'foo'")
	}
}

func TestEmptyQuery(t *testing.T) {
	dir := t.TempDir()
	eng := NewEngine(dir)

	writeTestFile(t, dir, "a.go", `package a; func Foo() {}`)
	eng.IndexFile("a.go")

	matches, err := eng.Search(context.Background(), "", DefaultSearchOptions())
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) != 0 {
		t.Fatal("expected 0 matches for empty query")
	}
}

func TestIndexEmptyFile(t *testing.T) {
	dir := t.TempDir()
	eng := NewEngine(dir)

	writeTestFile(t, dir, "empty.go", "")
	ok := eng.IndexFile("empty.go")
	if !ok {
		t.Fatal("IndexFile should return true for empty file")
	}
}

func TestNonExistentFile(t *testing.T) {
	dir := t.TempDir()
	eng := NewEngine(dir)

	ok := eng.IndexFile("nonexistent.go")
	if ok {
		t.Fatal("IndexFile should return false for non-existent file")
	}
}

func TestIndexWorkspace(t *testing.T) {
	dir := t.TempDir()
	eng := NewEngine(dir)

	writeTestFile(t, dir, "main.go", `package main; func main() {}`)
	writeTestFile(t, dir, "lib.go", `package lib; func Help() {}`)
	writeTestFile(t, dir, "README.md", `# Project`)
	writeTestFile(t, dir, "ignored.bin", "binary\x00data")

	count, err := eng.IndexWorkspace(context.Background())
	if err != nil {
		t.Fatalf("IndexWorkspace: %v", err)
	}

	if count < 3 {
		t.Fatalf("expected at least 3 indexed files (.go, .md), got %d", count)
	}
	if eng.DocCount() < 3 {
		t.Fatalf("expected at least 3 docs, got %d", eng.DocCount())
	}
}

func TestRefreshIndex(t *testing.T) {
	dir := t.TempDir()
	eng := NewEngine(dir)

	writeTestFile(t, dir, "main.go", `package main; func main() {}`)
	eng.IndexFile("main.go")
	initialTokenCount := eng.TokenCount()

	count, err := eng.RefreshIndex(context.Background())
	if err != nil {
		t.Fatalf("RefreshIndex: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 refreshes (no changes), got %d", count)
	}

	writeTestFile(t, dir, "main.go", `package main; func main() { println("updated") }`)
	count, err = eng.RefreshIndex(context.Background())
	if err != nil {
		t.Fatalf("RefreshIndex: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 refresh, got %d", count)
	}
	if eng.TokenCount() == initialTokenCount {
		t.Fatal("expected token count to change after update")
	}
}

func TestMaxResults(t *testing.T) {
	dir := t.TempDir()
	eng := NewEngine(dir)

	for i := 0; i < 10; i++ {
		name := filepath.Join("pkg", "file"+strings.Repeat("0", i)+".go")
		writeTestFile(t, dir, name, `package pkg
func F`+string(rune('A'+i))+`() int { return `+string(rune('0'+i))+` }
`)
		eng.IndexFile(name)
	}

	opts := DefaultSearchOptions()
	opts.MaxResults = 3

	matches, err := eng.Search(context.Background(), "func", opts)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) > 3 {
		t.Fatalf("expected max 3 results, got %d", len(matches))
	}
}

func TestNoMatches(t *testing.T) {
	dir := t.TempDir()
	eng := NewEngine(dir)

	writeTestFile(t, dir, "main.go", `package main; func main() {}`)
	eng.IndexFile("main.go")

	matches, err := eng.Search(context.Background(), "zzz_nonexistent_zzz", DefaultSearchOptions())
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) != 0 {
		t.Fatal("expected 0 matches for non-existent term")
	}
}

func TestFuzzyMaxErrors(t *testing.T) {
	dir := t.TempDir()
	eng := NewEngine(dir)

	writeTestFile(t, dir, "math.go", `package math
func Add(a, b int) int { return a + b }
`)

	eng.IndexFile("math.go")

	opts := DefaultSearchOptions()
	opts.Fuzzy = true
	opts.MaxErrors = 1

	matches, err := eng.Search(context.Background(), "ad", opts)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("expected fuzzy match for 'ad' -> 'Add'")
	}

	opts.MaxErrors = 0
	matches, err = eng.Search(context.Background(), "ad", opts)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) != 0 {
		t.Fatal("expected 0 matches with MaxErrors=0 for misspelled query")
	}
}

func TestLevenshteinDistance(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"a", "", 1},
		{"", "a", 1},
		{"kitten", "sitting", 3},
		{"cat", "cat", 0},
		{"cat", "cats", 1},
		{"cat", "cut", 1},
	}
	for _, tt := range tests {
		got := levenshtein(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestTokenize(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"hello world", []string{"hello", "world"}},
		{"HelloWorld", []string{"helloworld"}},
		{"fmt.Println", []string{"fmt", "println"}},
		{"a b c", nil},
		{"", nil},
		{"func main() {", []string{"func", "main"}},
		{"snake_case", []string{"snake_case"}},
	}
	for _, tt := range tests {
		got := tokenize(tt.input)
		if !stringSliceEqual(got, tt.want) {
			t.Errorf("tokenize(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestShouldIndex(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"main.go", true},
		{"test.ts", true},
		{"test.py", true},
		{"test.java", true},
		{"test.rs", true},
		{"test.c", true},
		{"test.cpp", true},
		{"test.md", true},
		{"test.json", true},
		{"test.yaml", true},
		{"Dockerfile", true},
		{"Makefile", true},
		{"Gemfile", true},
		{"image.png", false},
		{"data.bin", false},
		{"archive.zip", false},
		{"library.so", false},
		{"test.pdf", false},
	}
	for _, tt := range tests {
		got := shouldIndex(tt.path)
		if got != tt.want {
			t.Errorf("shouldIndex(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestDetectLang(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"main.go", "go"},
		{"server.ts", "typescript"},
		{"app.tsx", "typescript"},
		{"app.js", "javascript"},
		{"app.jsx", "javascript"},
		{"app.py", "python"},
		{"App.java", "java"},
		{"lib.rs", "rust"},
		{"code.c", "c"},
		{"code.h", "c"},
		{"code.cpp", "cpp"},
		{"code.hpp", "cpp"},
		{"config.yaml", "yaml"},
		{"config.json", "json"},
		{"readme.md", "markdown"},
		{"script.sh", "shell"},
		{"unknown.xyz", "text"},
	}
	for _, tt := range tests {
		got := detectLang(tt.path)
		if got != tt.want {
			t.Errorf("detectLang(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestStats(t *testing.T) {
	dir := t.TempDir()
	eng := NewEngine(dir)

	writeTestFile(t, dir, "a.go", `package a; func A() {}`)
	writeTestFile(t, dir, "b.go", `package b; func B() {}`)
	eng.IndexFile("a.go")
	eng.IndexFile("b.go")

	stats := eng.Stats()
	if stats.DocCount != 2 {
		t.Fatalf("expected 2 docs, got %d", stats.DocCount)
	}
	if stats.TokenCount == 0 {
		t.Fatal("expected non-zero token count")
	}
}

func TestNoIndexSearch(t *testing.T) {
	dir := t.TempDir()
	eng := NewEngine(dir)

	matches, err := eng.Search(context.Background(), "anything", DefaultSearchOptions())
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) != 0 {
		t.Fatal("expected 0 matches on empty index")
	}
}

func TestRemoveAndReindex(t *testing.T) {
	dir := t.TempDir()
	eng := NewEngine(dir)

	writeTestFile(t, dir, "temp.go", `package temp; func Temp() {}`)
	eng.IndexFile("temp.go")
	if eng.DocCount() != 1 {
		t.Fatalf("expected 1 doc, got %d", eng.DocCount())
	}

	eng.mu.Lock()
	eng.removeDoc("temp.go")
	eng.mu.Unlock()
	if eng.DocCount() != 0 {
		t.Fatalf("expected 0 docs after remove, got %d", eng.DocCount())
	}

	eng.IndexFile("temp.go")
	if eng.DocCount() != 1 {
		t.Fatalf("expected 1 doc after reindex, got %d", eng.DocCount())
	}
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
