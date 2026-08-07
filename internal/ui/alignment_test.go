package ui

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/execution"
)

const todoHtmlContent = `<!DOCTYPE html>
<html><head><title>To-Do App</title></head><body>
<div><input id="newTodo" placeholder="Add a task"></div>
<div><button onclick="addTask()">Add</button></div>
<div id="taskList"></div>
<script>let todos = [];</script>
</body></html>`

const portfolioHtmlContent = `<!DOCTYPE html>
<html><head><title>Alex Josie — Portfolio</title></head><body>
<main>
<section id="about"><h1>Alex Josie</h1></section>
<section id="projects"><article><h2>Project one</h2></article></section>
</main>
</body></html>`

func TestDetectBuildTargetType(t *testing.T) {
	if got := detectBuildTargetType("CLEAR ALL EXISTING CODE and build portfolio website"); got != "portfolio" {
		t.Errorf("got %q, want portfolio", got)
	}
	if got := detectBuildTargetType("build a todo app"); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := detectBuildTargetType(""); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// TestGateBuildProposals_RejectsTodoForPortfolioIntent is the regression core:
// a portfolio intent that produced a To-Do App proposal must be rejected by
// the gate so the mismatched diff can never render on the TUI.
func TestGateBuildProposals_RejectsTodoForPortfolioIntent(t *testing.T) {
	props := []SemanticProposal{{
		Target: SemanticTarget{QualifiedName: "index.html"},
		Patch:  &execution.Patch{Modified: todoHtmlContent},
	}}
	intent := "CLEAR ALL EXISTING CODE and build a brand new portfolio website"
	accepted, rejection := gateBuildProposals(intent, props)
	if rejection == nil {
		t.Fatal("expected rejection for a To-Do App proposal under portfolio intent")
	}
	if len(accepted) != 0 {
		t.Fatalf("accepted = %d, want 0", len(accepted))
	}
	if rejection.TargetType != "portfolio" {
		t.Errorf("target type = %q, want portfolio", rejection.TargetType)
	}
	if len(rejection.Files) != 1 || rejection.Files[0] != "index.html" {
		t.Errorf("rejected files = %v, want [index.html]", rejection.Files)
	}
	if !strings.Contains(rejection.Error(), "To-Do App") || !strings.Contains(rejection.Error(), "Portfolio") {
		t.Errorf("rejection directive = %q", rejection.Error())
	}
}

func TestGateBuildProposals_AcceptsPortfolio(t *testing.T) {
	props := []SemanticProposal{{
		Target: SemanticTarget{QualifiedName: "index.html"},
		Patch:  &execution.Patch{Modified: portfolioHtmlContent},
	}}
	accepted, rejection := gateBuildProposals("build portfolio website", props)
	if rejection != nil {
		t.Fatalf("unexpected rejection: %v", rejection)
	}
	if len(accepted) != 1 {
		t.Fatalf("accepted = %d, want 1", len(accepted))
	}
}

// TestGateBuildProposals_PassThroughWithoutTargetIntent proves the gate is
// keyed to the requested target type: a todo_app request passes unchanged.
func TestGateBuildProposals_PassThroughWithoutTargetIntent(t *testing.T) {
	props := []SemanticProposal{{
		Target: SemanticTarget{QualifiedName: "index.html"},
		Patch:  &execution.Patch{Modified: todoHtmlContent},
	}}
	accepted, rejection := gateBuildProposals("build a todo app", props)
	if rejection != nil {
		t.Fatalf("todo_app intent must not trigger the portfolio rule: %v", rejection)
	}
	if len(accepted) != 1 {
		t.Fatalf("accepted = %d, want 1", len(accepted))
	}
}

// TestGateBuildProposals_FiltersPartialMismatch proves only the mismatched
// files are dropped; aligned files still render.
func TestGateBuildProposals_FiltersPartialMismatch(t *testing.T) {
	props := []SemanticProposal{
		{Target: SemanticTarget{QualifiedName: "index.html"}, Patch: &execution.Patch{Modified: todoHtmlContent}},
		{Target: SemanticTarget{QualifiedName: "script.js"}, Patch: &execution.Patch{Modified: "const x = 1;"}},
	}
	accepted, rejection := gateBuildProposals("build portfolio website", props)
	if rejection == nil {
		t.Fatal("expected rejection")
	}
	if len(accepted) != 1 || accepted[0].Target.QualifiedName != "script.js" {
		t.Fatalf("accepted = %+v, want only script.js", accepted)
	}
}

func TestProposalNewContent(t *testing.T) {
	p := SemanticProposal{Target: SemanticTarget{QualifiedName: "a.html"}, Patch: &execution.Patch{Modified: "full"}}
	if got := proposalNewContent(p); got != "full" {
		t.Errorf("patch content = %q", got)
	}
	p = SemanticProposal{Target: SemanticTarget{QualifiedName: "a.html"}, Diff: "content"}
	if got := proposalNewContent(p); got != "content" {
		t.Errorf("full-content diff = %q", got)
	}
	p = SemanticProposal{Target: SemanticTarget{QualifiedName: "a.html"}, Diff: "@@ -1,1 +1,1 @@\n-foo\n+bar"}
	if got := proposalNewContent(p); got != "" {
		t.Errorf("diff hunk must be skipped (no full content), got %q", got)
	}
}
