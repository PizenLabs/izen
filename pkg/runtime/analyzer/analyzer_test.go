package analyzer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFixture writes a file at path under root with the given content,
// creating parent directories as needed.
func writeFixture(t *testing.T, root, path, content string) string {
	t.Helper()
	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return full
}

func TestParseIntent(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    Intent
		wantSub string
	}{
		{"bug fix", "fix the login bug that panics on empty input", IntentBugFix, "matched"},
		{"question", "how does the router work?", IntentQuestion, "how"},
		{"feature", "add a new export endpoint", IntentFeature, "add"},
		{"refactor", "refactor the parser to reduce complexity", IntentRefactor, "refactor"},
		{"tie bugfix wins", "how do I fix the panic?", IntentBugFix, "matched"},
		{"unknown", "do the thing", IntentUnknown, "no intent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := ParseIntent(tt.input)
			if got != tt.want {
				t.Errorf("ParseIntent(%q) = %s, want %s", tt.input, got, tt.want)
			}
			if reason == "" {
				t.Error("ParseIntent returned empty reason")
			}
			if tt.wantSub != "" && !strings.Contains(reason, tt.wantSub) {
				t.Errorf("ParseIntent(%q) reason %q missing %q", tt.input, reason, tt.wantSub)
			}
		})
	}
}

func TestAnalyzeTargets(t *testing.T) {
	root := t.TempDir()
	target := writeFixture(t, root, "pkg/main.go", `package main
import "fmt"
func main() { fmt.Println("hi") }
`)
	writeFixture(t, root, "pkg/other.go", "package main\n")

	a := New(root)
	facts, err := a.Analyze(context.Background(), Request{
		Input:   "fix the bug",
		Targets: []string{"pkg/main.go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if facts.Intent != IntentBugFix {
		t.Errorf("Intent = %s, want bug_fix", facts.Intent)
	}
	if facts.Files != 1 {
		t.Errorf("Files = %d, want 1", facts.Files)
	}
	if len(facts.TargetFiles) != 1 || facts.TargetFiles[0] != target {
		t.Errorf("TargetFiles = %v, want [%s]", facts.TargetFiles, target)
	}
	if facts.TokenEstimate == 0 {
		t.Error("TokenEstimate should be non-zero")
	}
	deps := facts.DependencyFanout[target]
	if len(deps) != 1 || deps[0] != "fmt" {
		t.Errorf("DependencyFanout[main.go] = %v, want [fmt]", deps)
	}
	if len(facts.ModifiedScopes) != 1 {
		t.Fatalf("ModifiedScopes = %d, want 1", len(facts.ModifiedScopes))
	}
	sc := facts.ModifiedScopes[0]
	if sc.Kind != "func" || sc.Name != "main" || sc.Path != target {
		t.Errorf("scope = %+v, want func main", sc)
	}
}

func TestAnalyzeWorkspaceScan(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "a.go", "package a\n")
	writeFixture(t, root, "sub/b.go", "package b\n")
	writeFixture(t, root, "sub/c.ts", "import x from 'dep'\n")
	writeFixture(t, root, "node_modules/ignored.go", "package huge\n")
	writeFixture(t, root, ".git/ignored.go", "package git\n")
	writeFixture(t, root, "README.md", "not source\n")

	a := New(root)
	facts, err := a.Analyze(context.Background(), Request{Input: "what is here?"})
	if err != nil {
		t.Fatal(err)
	}
	// node_modules/.git excluded; README.md not source; .ts file counted.
	if facts.Files != 3 {
		t.Errorf("Files = %d, want 3", facts.Files)
	}
	// TS import heuristic should capture 'dep'.
	found := false
	for _, deps := range facts.DependencyFanout {
		for _, d := range deps {
			if d == "dep" {
				found = true
			}
		}
	}
	if !found {
		t.Error("TS dependency 'dep' not detected")
	}
}

func TestAnalyzeMissingTarget(t *testing.T) {
	a := New(t.TempDir())
	_, err := a.Analyze(context.Background(), Request{Targets: []string{"nope.go"}})
	if err == nil {
		t.Fatal("expected error for missing target")
	}
}

func TestAnalyzeMaxFiles(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		writeFixture(t, root, "f"+string(rune('a'+i))+".go", "package p\n")
	}
	a := New(root, WithMaxFiles(3))
	facts, err := a.Analyze(context.Background(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	if facts.Files != 3 {
		t.Errorf("Files = %d, want 3 (capped)", facts.Files)
	}
}

func TestAnalyzeContextCancel(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "a.go", "package a\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := New(root)
	if _, err := a.Analyze(ctx, Request{}); err == nil {
		t.Fatal("expected context cancellation error")
	}
}

func TestAnalyzeDeterministic(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "b.go", "package b\n")
	writeFixture(t, root, "a.go", "package a\n")

	a := New(root)
	f1, err := a.Analyze(context.Background(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	f2, err := a.Analyze(context.Background(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	if f1.Files != f2.Files || f1.TokenEstimate != f2.TokenEstimate || f1.MaxFanout != f2.MaxFanout {
		t.Error("analysis is not deterministic across runs")
	}
}
