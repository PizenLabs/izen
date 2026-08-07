package capability

import (
	"errors"
	"strings"
	"testing"
)

const todoHtml = `<!DOCTYPE html>
<html><head><title>Todo App</title></head><body>
<div><input id="newTodo" placeholder="Add a task"></div>
<div><button onclick="addTask()">Add</button></div>
<div id="taskList"></div>
<script>let todos = [];</script>
</body></html>`

const portfolioHtml = `<!DOCTYPE html>
<html><head><title>My Portfolio</title></head><body>
<main>
<section id="about"><h1>About me</h1></section>
<section id="projects"><article><h2>Project one</h2></article></section>
</main>
</body></html>`

// TestCheckAlignmentPortfolioRejectsTodoApp proves the portfolio rule rejects
// generated content describing a To-Do App with ErrSemanticMismatch.
func TestCheckAlignmentPortfolioRejectsTodoApp(t *testing.T) {
	check, err := CheckAlignment("portfolio", []AlignmentFile{
		{Path: "index.html", Content: []byte(todoHtml)},
	})
	if err == nil || !errors.Is(err, ErrSemanticMismatch) {
		t.Fatalf("expected ErrSemanticMismatch, got %v", err)
	}
	if check.Passed() {
		t.Error("check must not pass for a to-do artifact")
	}
	if len(check.Mismatches) != 1 {
		t.Fatalf("mismatches = %d, want 1", len(check.Mismatches))
	}
	m := check.Mismatches[0]
	if m.Path != "index.html" {
		t.Errorf("mismatch path = %q, want index.html", m.Path)
	}
	if m.Detected != "To-Do App" {
		t.Errorf("mismatch detected = %q, want To-Do App", m.Detected)
	}
}

// TestCheckAlignmentPortfolioAcceptsPortfolio proves a genuine portfolio
// artifact passes the alignment gate.
func TestCheckAlignmentPortfolioAcceptsPortfolio(t *testing.T) {
	check, err := CheckAlignment("portfolio", []AlignmentFile{
		{Path: "index.html", Content: []byte(portfolioHtml)},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !check.Passed() {
		t.Fatalf("portfolio artifact must align, mismatches = %v", check.Mismatches)
	}
}

// TestCheckAlignmentScopedToTargetType proves the gate is keyed to the
// requested target type: a To-Do App artifact only mismatches a portfolio
// target, never a website, todo_app or workspace target.
func TestCheckAlignmentScopedToTargetType(t *testing.T) {
	for _, target := range []string{"website", "landing_page", "todo_app", "workspace", ""} {
		check, err := CheckAlignment(target, []AlignmentFile{
			{Path: "index.html", Content: []byte(todoHtml)},
		})
		if err != nil {
			t.Errorf("CheckAlignment(%q) unexpected error: %v", target, err)
		}
		if !check.Passed() {
			t.Errorf("CheckAlignment(%q) must not trigger the portfolio rule", target)
		}
	}
}

// TestCheckAlignmentExtractsTitleToken proves the primary text token of the
// artifact (its <title> payload) is surfaced on the mismatch.
func TestCheckAlignmentExtractsTitleToken(t *testing.T) {
	check, err := CheckAlignment("portfolio", []AlignmentFile{
		{Path: "index.html", Content: []byte(todoHtml)},
	})
	if !errors.Is(err, ErrSemanticMismatch) {
		t.Fatalf("expected ErrSemanticMismatch, got %v", err)
	}
	if len(check.Mismatches) != 1 {
		t.Fatalf("mismatches = %d, want 1", len(check.Mismatches))
	}
	joined := strings.Join(check.Mismatches[0].Tokens, "|")
	if !strings.Contains(joined, "Todo App") {
		t.Errorf("mismatch tokens must include the title payload, got %q", joined)
	}
}

// TestCheckAlignmentEmptyArtifactsPasses proves the gate is a no-op when no
// file artifacts were generated.
func TestCheckAlignmentEmptyArtifactsPasses(t *testing.T) {
	check, err := CheckAlignment("portfolio", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !check.Passed() {
		t.Error("empty artifact set must pass the alignment gate")
	}
}

// TestDisplayTargetType covers the human-facing label mapping.
func TestDisplayTargetType(t *testing.T) {
	cases := map[string]string{
		"portfolio":    "Portfolio",
		"website":      "Website",
		"landing_page": "Landing Page",
		"rest_api":     "REST API",
		"todo_app":     "To-Do App",
	}
	for in, want := range cases {
		if got := DisplayTargetType(in); got != want {
			t.Errorf("DisplayTargetType(%q) = %q, want %q", in, got, want)
		}
	}
}
