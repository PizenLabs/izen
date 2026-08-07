package ui

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/modes/plan"
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

// TestIsFullRewriteIntent proves rewrite-context detection fires for the exact
// "CLEAR ALL EXISTING CODE" scenario and for redesign/rewrite/from-scratch
// phrasings, and stays silent for ordinary edits.
func TestIsFullRewriteIntent(t *testing.T) {
	rewrite := []string{
		"Clear all existing code and create a brand new portfolio website",
		"redesign my portfolio",
		"rewrite index.html from scratch",
		"replace existing workspace with a portfolio",
		"wipe out the todo app and build a portfolio",
	}
	for _, in := range rewrite {
		if !isFullRewriteIntent(in) {
			t.Errorf("isFullRewriteIntent(%q) = false, want true", in)
		}
	}
	notRewrite := []string{"fix the button color", "add a contact form", ""}
	for _, in := range notRewrite {
		if isFullRewriteIntent(in) {
			t.Errorf("isFullRewriteIntent(%q) = true, want false", in)
		}
	}
}

// TestStripModePrefix proves the mode-command prefix is removed so the raw
// user intent is available for rewrite-context decisions.
func TestStripModePrefix(t *testing.T) {
	if got := stripModePrefix("/plan Clear all existing code"); got != "Clear all existing code" {
		t.Errorf("got %q", got)
	}
	if got := stripModePrefix("/build make a portfolio"); got != "make a portfolio" {
		t.Errorf("got %q", got)
	}
	if got := stripModePrefix("plain prompt"); got != "plain prompt" {
		t.Errorf("got %q", got)
	}
}

// TestFastTrackGoals_RewritePrepending proves the explicit user intent is
// prepended to the task goals under a full-rewrite context.
func TestFastTrackGoals_RewritePrepending(t *testing.T) {
	tasks := []plan.Task{
		{StepNum: 1, Type: "FILE_MUTATE", Target: "index.html", Description: "CREATE index.html"},
		{StepNum: 2, Type: "FILE_MUTATE", Target: "styles.css", Description: "CREATE styles.css"},
	}
	intent := "Clear all existing code and create a brand new portfolio website for Alex Josie"
	goals := fastTrackGoals(intent, tasks)
	if !strings.Contains(goals, intent) {
		t.Errorf("goals must embed the explicit user intent:\n%s", goals)
	}
	if !strings.Contains(goals, "DO NOT USE OR REFERENCE ANY EXISTING CODE") && !strings.Contains(goals, "CREATE FROM SCRATCH") {
		t.Errorf("goals must carry the create-from-scratch directive:\n%s", goals)
	}
}

// TestFastTrackFileContext_RewriteStripsObsoleteContent is the TaskContext
// hygiene regression core: under a rewrite intent the current file contents
// are NEVER injected into the build prompt — only the target name and the
// create-from-scratch directive.
func TestFastTrackFileContext_RewriteStripsObsoleteContent(t *testing.T) {
	intent := "CLEAR ALL EXISTING CODE and build a brand new portfolio website"
	targets := []string{"index.html", "styles.css"}
	obsolete := "<title>To-Do App</title>"
	ctx := fastTrackFileContext(intent, targets, func(string) ([]byte, error) {
		return []byte(obsolete), nil
	})
	if strings.Contains(ctx, obsolete) || strings.Contains(ctx, "To-Do App") {
		t.Fatalf("obsolete workspace content leaked into the rewrite task context:\n%s", ctx)
	}
	if !strings.Contains(ctx, "index.html") || !strings.Contains(ctx, "styles.css") {
		t.Errorf("rewrite context must name every target file:\n%s", ctx)
	}
	if !strings.Contains(ctx, "DO NOT USE OR REFERENCE ANY EXISTING CODE IN THE WORKSPACE. CREATE FROM SCRATCH.") {
		t.Errorf("rewrite context must carry the create-from-scratch directive:\n%s", ctx)
	}
}

// TestFastTrackFileContext_NonRewriteKeepsBaseline proves ordinary edits still
// receive the current file contents (bounded baseline) — the stripping only
// applies under a rewrite context.
func TestFastTrackFileContext_NonRewriteKeepsBaseline(t *testing.T) {
	intent := "fix the button color"
	ctx := fastTrackFileContext(intent, []string{"styles.css"}, func(string) ([]byte, error) {
		return []byte("body { color: red; }"), nil
	})
	if !strings.Contains(ctx, "body { color: red; }") {
		t.Errorf("non-rewrite context must keep baseline content:\n%s", ctx)
	}
	if strings.Contains(ctx, "CREATE FROM SCRATCH") {
		t.Errorf("non-rewrite context must not carry the rewrite directive:\n%s", ctx)
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
