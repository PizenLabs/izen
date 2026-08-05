package tips

import (
	"strings"
	"testing"
)

func TestPhaseFromString(t *testing.T) {
	cases := map[string]Phase{
		"analyze":          PhaseAnalyze,
		"analyzed":         PhaseAnalyze,
		"ask":              PhaseAnalyze,
		"investigate":      PhaseAnalyze,
		"plan":             PhasePlan,
		"planned":          PhasePlan,
		"policy_evaluated": PhasePlan,
		"execute":          PhaseExecute,
		"executing":        PhaseExecute,
		"build":            PhaseExecute,
		"validate":         PhaseValidate,
		"validating":       PhaseValidate,
		"review":           PhaseValidate,
		"  PLANNED  ":      PhasePlan,
	}
	for in, want := range cases {
		got, ok := PhaseFromString(in)
		if !ok || got != want {
			t.Errorf("PhaseFromString(%q) = (%v, %v), want (%v, true)", in, got, ok, want)
		}
	}
	if _, ok := PhaseFromString("nonsense"); ok {
		t.Fatal("PhaseFromString(nonsense) ok = true, want false")
	}
}

func TestTipForPhase(t *testing.T) {
	p := New()
	p.Add(
		Tip{Phases: []Phase{PhaseExecute}, Text: "ctrl-c"},
		Tip{Phases: []Phase{PhasePlan}, Text: "policy"},
	)
	if got := p.TipFor(PhaseExecute, ""); got != "ctrl-c" {
		t.Fatalf("TipFor(Execute) = %q, want ctrl-c", got)
	}
	if got := p.TipFor(PhasePlan, ""); got != "policy" {
		t.Fatalf("TipFor(Plan) = %q, want policy", got)
	}
}

func TestTipForStrategyWinsOverPhase(t *testing.T) {
	p := New()
	p.Add(
		Tip{Phases: []Phase{PhaseAnalyze}, Text: "phase-tip"},
		Tip{Strategies: []string{StrategyChat}, Text: "strategy-tip"},
	)
	if got := p.TipFor(PhaseAnalyze, StrategyChat); got != "strategy-tip" {
		t.Fatalf("TipFor(analyze, direct_chat) = %q, want strategy-tip to win", got)
	}
}

func TestTipForUniversalFallback(t *testing.T) {
	p := New()
	p.Add(Tip{Text: "universal"})
	if got := p.TipFor(PhaseValidate, ""); got != "universal" {
		t.Fatalf("TipFor(validate) = %q, want universal", got)
	}
}

func TestTipForEmptyProvider(t *testing.T) {
	p := New()
	if got := p.TipFor(PhaseAnalyze, ""); got != "" {
		t.Fatalf("empty provider returned %q, want empty", got)
	}
	var nilP *Provider
	if got := nilP.TipFor(PhaseAnalyze, ""); got != "" {
		t.Fatalf("nil provider returned %q, want empty", got)
	}
}

func TestTipRotates(t *testing.T) {
	p := New()
	p.Add(
		Tip{Phases: []Phase{PhaseAnalyze}, Text: "tip-a"},
		Tip{Phases: []Phase{PhaseAnalyze}, Text: "tip-b"},
		Tip{Phases: []Phase{PhaseAnalyze}, Text: "tip-c"},
	)
	seen := map[string]bool{}
	for i := 0; i < 6; i++ {
		seen[p.TipFor(PhaseAnalyze, "")] = true
	}
	if len(seen) < 2 {
		t.Fatalf("tips did not rotate, all picks were: %v", seen)
	}
}

func TestRender(t *testing.T) {
	if got := Render("Use /plan."); got != "└ Tip: Use /plan." {
		t.Fatalf("Render = %q, want %q", got, "└ Tip: Use /plan.")
	}
	if got := Render(""); got != "" {
		t.Fatalf("Render(\"\") = %q, want empty", got)
	}
}

func TestDefaultCoversRequiredTips(t *testing.T) {
	p := Default()
	// Rotation is random, so assert the corpus (not a single pick) contains
	// the three tips the spec calls out.
	var corpus []string
	for i := 0; i < 40; i++ {
		corpus = append(corpus, p.TipFor(PhaseExecute, ""))
		corpus = append(corpus, p.TipFor(PhasePlan, ""))
		corpus = append(corpus, p.TipFor(PhaseAnalyze, StrategyChat))
	}
	joined := strings.Join(corpus, "\n")
	for _, want := range []string{"Ctrl+C", "izen.yaml", "DirectChatStrategy"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("corpus does not contain required tip fragment %q", want)
		}
	}
}

func TestTipForPhaseString(t *testing.T) {
	p := New()
	p.Add(Tip{Phases: []Phase{PhaseValidate}, Text: "review-tip"})
	if got := p.TipForPhaseString("reviewing", ""); got != "review-tip" {
		t.Fatalf("TipForPhaseString(reviewing) = %q, want review-tip", got)
	}
	if got := p.TipForPhaseString("bogus", ""); got != "" {
		t.Fatalf("TipForPhaseString(bogus) = %q, want empty (no match)", got)
	}
}

func TestDefaultNeverEmptyForPhases(t *testing.T) {
	p := Default()
	for _, ph := range []Phase{PhaseAnalyze, PhasePlan, PhaseExecute, PhaseValidate} {
		if got := p.TipFor(ph, ""); got == "" {
			t.Fatalf("Default() returned empty tip for phase %v", ph)
		}
	}
}
