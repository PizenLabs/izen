package planner

import (
	"testing"

	ir "github.com/PizenLabs/izen/pkg/engine/ir/logical"
)

// verificationPrompt is the TUI verification prompt: a greenfield static
// website request using HTML, CSS and JavaScript.
const verificationPrompt = "Design a website introducing JAY, describing your job as a software engineer, using HTML, CSS, and JavaScript."

func TestGenerateGreenfieldWebPlan(t *testing.T) {
	lp, err := NewIRPlanner().Generate(verificationPrompt)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if lp.Len() != 3 {
		t.Fatalf("plan nodes = %d, want 3 (page + style + script)", lp.Len())
	}
	if lp.KindCount(ir.NodeCreatePage) != 1 {
		t.Fatalf("pages = %d, want 1", lp.KindCount(ir.NodeCreatePage))
	}
	if lp.KindCount(ir.NodeCreateStyle) != 1 {
		t.Fatalf("styles = %d, want 1", lp.KindCount(ir.NodeCreateStyle))
	}
	if lp.KindCount(ir.NodeCreateScript) != 1 {
		t.Fatalf("scripts = %d, want 1", lp.KindCount(ir.NodeCreateScript))
	}

	page, ok := lp.Node("page-index")
	if !ok {
		t.Fatal("expected page-index node")
	}
	if page.NodeName() != "index" {
		t.Errorf("page name = %q, want index", page.NodeName())
	}
}

func TestGenerateRejectsNonWebPrompt(t *testing.T) {
	for _, prompt := range []string{
		"the handler crashes with a nil pointer on startup",
		"refactor the checkout module",
		"",
	} {
		if _, err := NewIRPlanner().Generate(prompt); err == nil {
			t.Errorf("Generate(%q) must error", prompt)
		}
	}
}

func TestGenerateStyleAndScriptSignals(t *testing.T) {
	// css only → no script node.
	lp, err := NewIRPlanner().Generate("make a website with html and css")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if lp.KindCount(ir.NodeCreateStyle) != 1 {
		t.Errorf("styles = %d, want 1", lp.KindCount(ir.NodeCreateStyle))
	}
	if lp.KindCount(ir.NodeCreateScript) != 0 {
		t.Errorf("scripts = %d, want 0 (no js signal)", lp.KindCount(ir.NodeCreateScript))
	}

	// js mention → script node.
	lp2, err := NewIRPlanner().Generate("make a website with javascript")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if lp2.KindCount(ir.NodeCreateScript) != 1 {
		t.Errorf("scripts = %d, want 1", lp2.KindCount(ir.NodeCreateScript))
	}
	if lp2.KindCount(ir.NodeCreateStyle) != 0 {
		t.Errorf("styles = %d, want 0 (no css signal)", lp2.KindCount(ir.NodeCreateStyle))
	}
}

func TestGenerateAboutPageMention(t *testing.T) {
	lp, err := NewIRPlanner().Generate("make a website for my company with an about page")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if lp.KindCount(ir.NodeCreatePage) != 2 {
		t.Fatalf("pages = %d, want 2 (index + about)", lp.KindCount(ir.NodeCreatePage))
	}
	if _, ok := lp.Node("page-about"); !ok {
		t.Fatal("expected an about page node")
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	a, _ := NewIRPlanner().Generate(verificationPrompt)
	b, _ := NewIRPlanner().Generate(verificationPrompt)
	if a.Len() != b.Len() {
		t.Fatalf("plan lengths differ across runs: %d vs %d", a.Len(), b.Len())
	}
	for i := range a.Nodes() {
		if a.Nodes()[i].NodeID() != b.Nodes()[i].NodeID() {
			t.Fatalf("node %d differs across runs", i)
		}
	}
}

func TestDerivePageTitle(t *testing.T) {
	got := derivePageTitle(verificationPrompt)
	if got != "Introducing JAY" {
		t.Errorf("title = %q, want %q", got, "Introducing JAY")
	}
	if got := derivePageTitle("make a website for my startup"); got != "For my startup" {
		t.Errorf("title = %q, want %q", got, "For my startup")
	}
	if got := derivePageTitle("create a simple page"); got != "Simple page" {
		t.Errorf("title = %q, want %q", got, "Simple page")
	}
}
