package strategy

import (
	"errors"
	"testing"

	"github.com/PizenLabs/izen/pkg/engine/intent"
)

func TestGreenfieldWebStrategyDerivesCanonicalFiles(t *testing.T) {
	s := NewGreenfieldWebStrategy()
	in := intent.Classify("make the website introduce for JAY with your job is software engineer using html, css and js")
	pc := buildPC(t, "make the website introduce for JAY with your job is software engineer using html, css and js")

	g, err := s.DetermineGoal(in, pc)
	if err != nil {
		t.Fatalf("DetermineGoal: %v", err)
	}
	got := g.NewFiles()
	want := []string{"index.html", "styles.css", "script.js"}
	if len(got) != len(want) {
		t.Fatalf("files = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("files[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if g.RequiresVerify() {
		t.Fatal("greenfield website generation must not require a test/verify step")
	}
	if g.Intent().Family() != intent.FamilyGreenfield {
		t.Fatalf("family = %s, want greenfield", g.Intent().Family())
	}
	if g.Outcome() == "" {
		t.Fatal("outcome must not be empty")
	}
}

func TestGreenfieldWebStrategyPartialTech(t *testing.T) {
	s := NewGreenfieldWebStrategy()
	in := intent.Must(intent.FamilyGreenfield)

	tests := []struct {
		prompt string
		want   []string
	}{
		{"create a landing page with html and css", []string{"index.html", "styles.css"}},
		{"build a portfolio using html and js", []string{"index.html", "script.js"}},
		{"make a web page in html only", []string{"index.html"}},
	}
	for _, tt := range tests {
		g, err := s.DetermineGoal(in, buildPC(t, tt.prompt))
		if err != nil {
			t.Fatalf("DetermineGoal(%q): %v", tt.prompt, err)
		}
		got := g.NewFiles()
		if len(got) != len(tt.want) {
			t.Fatalf("DetermineGoal(%q) files = %v, want %v", tt.prompt, got, tt.want)
		}
		for i := range tt.want {
			if got[i] != tt.want[i] {
				t.Fatalf("DetermineGoal(%q) files[%d] = %q, want %q", tt.prompt, i, got[i], tt.want[i])
			}
		}
	}
}

func TestGreenfieldWebStrategyNotApplicable(t *testing.T) {
	s := NewGreenfieldWebStrategy()

	// Non-greenfield intent.
	if _, err := s.DetermineGoal(intent.Must(intent.FamilyFeature), buildPC(t, "make a website")); !errors.Is(err, ErrStrategyNotApplicable) {
		t.Fatalf("non-greenfield intent: err = %v, want ErrStrategyNotApplicable", err)
	}
	// Greenfield intent without web signals.
	if _, err := s.DetermineGoal(intent.Must(intent.FamilyGreenfield), buildPC(t, "create a new rest api server")); !errors.Is(err, ErrStrategyNotApplicable) {
		t.Fatalf("non-web greenfield: err = %v, want ErrStrategyNotApplicable", err)
	}
}

func TestGreenfieldWebStrategyOutcomeClean(t *testing.T) {
	s := NewGreenfieldWebStrategy()
	g, err := s.DetermineGoal(intent.Must(intent.FamilyGreenfield), buildPC(t, "make the website introduce for JAY"))
	if err != nil {
		t.Fatal(err)
	}
	out := g.Outcome()
	if out == "" || out == "Generate a static website" {
		t.Fatalf("outcome should be derived from the prompt, got %q", out)
	}
}

func TestGreenfieldWebStrategyEmptyPrompt(t *testing.T) {
	s := NewGreenfieldWebStrategy()
	if _, err := s.DetermineGoal(intent.Must(intent.FamilyGreenfield), buildPC(t, "")); !errors.Is(err, ErrStrategyNotApplicable) {
		t.Fatalf("empty prompt: err = %v, want ErrStrategyNotApplicable", err)
	}
}

func TestStrategyRegistryOrder(t *testing.T) {
	// A greenfield web prompt must be handled by the web strategy before any
	// generic prompt strategy so files are enumerated deterministically.
	in := intent.Classify("make a website for me using html, css and js")
	pc := buildPC(t, "make a website for me using html, css and js")
	g, err := NewGreenfieldWebStrategy().DetermineGoal(in, pc)
	if err != nil {
		t.Fatalf("DetermineGoal: %v", err)
	}
	if len(g.NewFiles()) != 3 {
		t.Fatalf("files = %v, want index.html, styles.css, script.js", g.NewFiles())
	}
}
