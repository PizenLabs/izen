package preflight

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/pkg/provider/capability"
	"github.com/PizenLabs/izen/pkg/runtime/context"
)

// TestBudgetAdvisorRecommendation is the core adaptive-preflight contract: a
// 7.7KB file targeted by FULL_REWRITE against a 1,024-token model must produce
// a BOUNDED_PATCH recommendation and a valid two-choice DecisionSurface —
// without failing execution.
func TestBudgetAdvisorRecommendation(t *testing.T) {
	t.Parallel()

	advisor := NewBudgetAdvisor()
	advice := advisor.Advise(BudgetAdvisoryRequest{
		TargetFile:       "index.html",
		FileSizeBytes:    7780,
		Strategy:         StrategyFullRewrite,
		MaxOutputTokens:  1024,
		ModelDisplayName: "Dots3-Note Preview",
		Effort:           capability.EffortMedium,
	})

	if !advice.Overflow {
		t.Fatal("FULL_REWRITE of a 7.7KB file on a 1,024-token model must overflow")
	}
	if advice.Requested != StrategyFullRewrite {
		t.Errorf("Requested = %q, want FULL_REWRITE", advice.Requested)
	}
	if advice.FileTokens != 1945 {
		t.Errorf("FileTokens = %d, want 1945 (7780/4)", advice.FileTokens)
	}
	// FULL_REWRITE: 1945 × 1.25 + 256 = 2687.
	if advice.RequiredTokens != 2687 {
		t.Errorf("RequiredTokens = %d, want 2687", advice.RequiredTokens)
	}

	rec := advice.Recommendation
	if rec == nil {
		t.Fatal("recommendation must be present on overflow")
	}
	if rec.Strategy != StrategyBoundedPatch {
		t.Errorf("recommended strategy = %q, want BOUNDED_PATCH", rec.Strategy)
	}
	if rec.Effort != capability.EffortHigh {
		t.Errorf("recommended effort = %q, want high", rec.Effort)
	}
	// BOUNDED_PATCH: 1945 × 0.35 + 256 = 936, which fits 1,024.
	if rec.EstimatedOutput != 936 {
		t.Errorf("estimated output = %d, want 936", rec.EstimatedOutput)
	}
	if rec.MaxTokens != 936 {
		t.Errorf("max tokens = %d, want 936", rec.MaxTokens)
	}
	if !rec.FitsBudget {
		t.Error("bounded patch estimate must fit the model budget")
	}

	surface := advice.Surface
	if surface == nil {
		t.Fatal("decision surface must be present on overflow")
	}
	if surface.Target != "index.html" {
		t.Errorf("surface target = %q, want index.html", surface.Target)
	}
	if surface.ModelMaxOutput != 1024 {
		t.Errorf("surface model max = %d, want 1024", surface.ModelMaxOutput)
	}
	if surface.TargetSizeBytes != 7780 || surface.TargetSizeTokens != 1945 {
		t.Errorf("surface size = %d bytes / %d tokens", surface.TargetSizeBytes, surface.TargetSizeTokens)
	}

	if len(surface.Choices) != 2 {
		t.Fatalf("surface must expose exactly 2 choices, got %d", len(surface.Choices))
	}
	if !surface.Has(ChoiceApplyRecommendation) {
		t.Error("surface must offer apply-recommendation")
	}
	if !surface.Has(ChoiceForceCurrentSettings) {
		t.Error("surface must offer force-current-settings")
	}
	if choice := surface.Choice(ChoiceApplyRecommendation); choice == nil || !choice.Recommended {
		t.Error("apply-recommendation must be the marked recommended option")
	}
}

// TestForceSkipBudgetAdvisor is the user-intent-preservation contract: choosing
// "Skip / Force Current Settings" on the DecisionSurface must let the runtime
// proceed with the original user configuration, unmodified.
func TestForceSkipBudgetAdvisor(t *testing.T) {
	t.Parallel()

	advisor := NewBudgetAdvisor()
	original := ExecutionConfig{
		Strategy:        StrategyFullRewrite,
		Effort:          capability.EffortMedium,
		MaxOutputTokens: 1024,
	}

	advice := advisor.Advise(BudgetAdvisoryRequest{
		TargetFile:      "index.html",
		FileSizeBytes:   7780,
		Strategy:        original.Strategy,
		MaxOutputTokens: original.MaxOutputTokens,
		Effort:          original.Effort,
	})
	if advice.Surface == nil {
		t.Fatal("overflow surface required for this scenario")
	}

	// Forcing current settings must yield the byte-for-byte original config.
	applied := advice.Surface.Resolve(ChoiceForceCurrentSettings, original)
	if applied.Strategy != original.Strategy {
		t.Errorf("strategy changed: %q != %q", applied.Strategy, original.Strategy)
	}
	if applied.Effort != original.Effort {
		t.Errorf("effort changed: %q != %q", applied.Effort, original.Effort)
	}
	if applied.MaxOutputTokens != original.MaxOutputTokens {
		t.Errorf("max tokens changed: %d != %d", applied.MaxOutputTokens, original.MaxOutputTokens)
	}

	// Contrast: applying the recommendation rescopes the config.
	applied = advice.Surface.Resolve(ChoiceApplyRecommendation, original)
	if applied.Strategy != StrategyBoundedPatch {
		t.Errorf("apply strategy = %q, want BOUNDED_PATCH", applied.Strategy)
	}
	if applied.Effort != capability.EffortHigh {
		t.Errorf("apply effort = %q, want high", applied.Effort)
	}
	if applied.MaxOutputTokens != advice.Recommendation.MaxTokens {
		t.Errorf("apply max tokens = %d, want %d", applied.MaxOutputTokens, advice.Recommendation.MaxTokens)
	}
}

func TestBudgetAdvisorNoOverflow(t *testing.T) {
	t.Parallel()

	advisor := NewBudgetAdvisor()
	// A small target on a large model must fit without a surface.
	advice := advisor.Advise(BudgetAdvisoryRequest{
		TargetFile:      "note.txt",
		FileSizeBytes:   400,
		Strategy:        StrategyFullRewrite,
		MaxOutputTokens: 4096,
	})
	if advice.Overflow {
		t.Fatal("small file on large model must not overflow")
	}
	if advice.Recommendation != nil {
		t.Error("no overflow must not carry a recommendation")
	}
	if advice.Surface != nil {
		t.Error("no overflow must not build a surface")
	}
	// 400 bytes → 100 tokens; FULL_REWRITE → 125 + 256 = 381.
	if advice.RequiredTokens != 381 {
		t.Errorf("RequiredTokens = %d, want 381", advice.RequiredTokens)
	}
}

func TestBudgetAdvisorEdgeCases(t *testing.T) {
	t.Parallel()
	advisor := NewBudgetAdvisor()

	t.Run("zero-byte file", func(t *testing.T) {
		if got := advisor.EstimateFileTokens(0); got != 0 {
			t.Errorf("EstimateFileTokens(0) = %d, want 0", got)
		}
		if got := advisor.EstimateFileTokens(-5); got != 0 {
			t.Errorf("EstimateFileTokens(-5) = %d, want 0", got)
		}
	})

	t.Run("sub-token payload rounds up to one token", func(t *testing.T) {
		if got := advisor.EstimateFileTokens(3); got != 1 {
			t.Errorf("EstimateFileTokens(3) = %d, want 1", got)
		}
	})

	t.Run("empty strategy defaults to full rewrite", func(t *testing.T) {
		advice := advisor.Advise(BudgetAdvisoryRequest{FileSizeBytes: 400, MaxOutputTokens: 100})
		if advice.Requested != StrategyFullRewrite {
			t.Errorf("Requested = %q, want FULL_REWRITE default", advice.Requested)
		}
		if !advice.Overflow {
			t.Error("defaulted full rewrite must overflow a 100-token model")
		}
	})

	t.Run("zero max output never overflows", func(t *testing.T) {
		advice := advisor.Advise(BudgetAdvisoryRequest{
			FileSizeBytes: 7780,
			Strategy:      StrategyFullRewrite,
		})
		if advice.Overflow {
			t.Error("unknown model budget must not overflow (conservative, not blocking)")
		}
		if advice.Surface != nil {
			t.Error("unknown budget must not build a surface")
		}
	})

	t.Run("bounded patch fits on moderate model", func(t *testing.T) {
		advice := advisor.Advise(BudgetAdvisoryRequest{
			FileSizeBytes:   7780,
			Strategy:        StrategyBoundedPatch,
			MaxOutputTokens: 1024,
		})
		if advice.Overflow {
			t.Error("bounded patch of 936 tokens must fit 1,024")
		}
	})
}

func TestExecutionStrategy(t *testing.T) {
	t.Parallel()
	for _, s := range []ExecutionStrategy{StrategyFullRewrite, StrategyBoundedPatch} {
		if !s.Valid() {
			t.Errorf("%q must be valid", s)
		}
		if s.String() != string(s) {
			t.Errorf("String() = %q", s.String())
		}
	}
	if ExecutionStrategy("INLINE").Valid() {
		t.Error("unknown strategy must be invalid")
	}
}

func TestDecisionSurfaceChoiceLookup(t *testing.T) {
	t.Parallel()

	advisor := NewBudgetAdvisor()
	advice := advisor.Advise(BudgetAdvisoryRequest{
		TargetFile:      "x.go",
		FileSizeBytes:   7780,
		Strategy:        StrategyFullRewrite,
		MaxOutputTokens: 1024,
	})
	surface := advice.Surface
	if surface == nil {
		t.Fatal("surface required")
	}
	if c := surface.Choice(ChoiceApplyRecommendation); c == nil || c.Label != "Apply Recommendation (Recommended)" {
		t.Errorf("apply choice = %+v", c)
	}
	if c := surface.Choice(ChoiceForceCurrentSettings); c == nil || c.Label != "Skip / Force Current Settings" {
		t.Errorf("force choice = %+v", c)
	}
	if surface.Choice(DecisionChoiceID("bogus")) != nil {
		t.Error("unknown choice must be nil")
	}

	var nilSurface *DecisionSurface
	if nilSurface.Choice(ChoiceApplyRecommendation) != nil {
		t.Error("nil surface Choice must be nil")
	}
	if nilSurface.Has(ChoiceApplyRecommendation) {
		t.Error("nil surface Has must be false")
	}
	original := ExecutionConfig{Strategy: StrategyFullRewrite}
	if got := nilSurface.Resolve(ChoiceApplyRecommendation, original); got != original {
		t.Error("nil surface Resolve must return original config")
	}
}

func TestDecisionSurfaceRender(t *testing.T) {
	t.Parallel()

	advisor := NewBudgetAdvisor()
	advice := advisor.Advise(BudgetAdvisoryRequest{
		TargetFile:       "index.html",
		FileSizeBytes:    7780,
		Strategy:         StrategyFullRewrite,
		MaxOutputTokens:  1024,
		ModelDisplayName: "Dots3-Note Preview",
		Effort:           capability.EffortMedium,
	})
	surface := advice.Surface
	if surface == nil {
		t.Fatal("surface required")
	}

	rendered := surface.Render(72)
	for _, want := range []string{
		"◆ BUDGET & EFFORT ADVISORY",
		"[index.html]",
		"Current Target Size : 7,780 bytes (~1,945 tokens)",
		"Model Max Output    : 1,024 tokens (Dots3-Note Preview)",
		"Requested Strategy  : FULL_REWRITE (Requires ~2,687 output tokens)",
		"► [1] Apply Recommendation (Recommended)",
		"Switch strategy to BOUNDED_PATCH and set effort to HIGH.",
		"Estimated output: ~936 tokens (Fits model budget).",
		"[2] Skip / Force Current Settings",
		"Proceed with FULL_REWRITE using 1,024 max tokens.",
		"High risk of truncation (OUTPUT_EXHAUSTED).",
		"↑/↓ navigate · Enter select · Esc cancel",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("render missing %q:\n%s", want, rendered)
		}
	}

	t.Run("nil surface renders empty", func(t *testing.T) {
		var nilSurface *DecisionSurface
		if got := nilSurface.Render(60); got != "" {
			t.Errorf("nil surface render = %q, want empty", got)
		}
	})

	t.Run("narrow width clamps to minimum", func(t *testing.T) {
		rendered := surface.Render(10)
		if !strings.Contains(rendered, "BUDGET") {
			t.Errorf("clamped render lost title: %q", rendered)
		}
	})

	t.Run("missing model name falls back", func(t *testing.T) {
		advice := advisor.Advise(BudgetAdvisoryRequest{
			TargetFile:      "x.go",
			FileSizeBytes:   7780,
			Strategy:        StrategyFullRewrite,
			MaxOutputTokens: 1024,
		})
		rendered := advice.Surface.Render(60)
		if !strings.Contains(rendered, "(unknown model)") {
			t.Errorf("render must fall back to unknown model:\n%s", rendered)
		}
	})

	t.Run("empty target falls back", func(t *testing.T) {
		advice := advisor.Advise(BudgetAdvisoryRequest{
			FileSizeBytes:   7780,
			Strategy:        StrategyFullRewrite,
			MaxOutputTokens: 1024,
		})
		rendered := advice.Surface.Render(60)
		if !strings.Contains(rendered, "(target not resolved)") {
			t.Errorf("render must fall back target:\n%s", rendered)
		}
	})
}

func TestFormatInt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{-7, "0"},
		{7, "7"},
		{999, "999"},
		{1000, "1,000"},
		{7780, "7,780"},
		{1234567, "1,234,567"},
	}
	for _, tt := range tests {
		if got := formatInt(tt.n); got != tt.want {
			t.Errorf("formatInt(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestDisplayName(t *testing.T) {
	t.Parallel()
	if got := displayName("O3 mini"); got != "O3 mini" {
		t.Errorf("displayName = %q", got)
	}
	if got := displayName("  "); got != "unknown model" {
		t.Errorf("blank displayName = %q, want fallback", got)
	}
	if got := displayName(""); got != "unknown model" {
		t.Errorf("empty displayName = %q, want fallback", got)
	}
}

func TestBoxStringAndRuneLen(t *testing.T) {
	t.Parallel()
	boxed := boxString("hello", 10)
	if !strings.HasPrefix(boxed, "┌") || !strings.HasSuffix(boxed, "┘") {
		t.Errorf("box frame malformed:\n%s", boxed)
	}
	if runeLen("héllo") != 5 {
		t.Errorf("runeLen = %d, want 5", runeLen("héllo"))
	}
}

// TestPreflightEngineBudgetAdvisory verifies the engine integration: an
// advisory request on the PreflightRequest produces a BudgetAdvice (with a
// DecisionSurface on overflow) in the CompiledRequest, while a request without
// an advisory leaves the advice zero.
func TestPreflightEngineBudgetAdvisory(t *testing.T) {
	t.Parallel()

	engine := NewEngine(
		&fakeResolver{ref: ref("index.html", 0, false, true)},
		context.NewCompiler(),
	)

	t.Run("advisory overflow attaches surface", func(t *testing.T) {
		compiled, err := engine.Execute(PreflightRequest{
			RawInput:    "update index.html",
			WorkDir:     ".",
			TokenBudget: 1000,
			BudgetAdvisory: BudgetAdvisoryRequest{
				TargetFile:       "index.html",
				FileSizeBytes:    7780,
				Strategy:         StrategyFullRewrite,
				MaxOutputTokens:  1024,
				ModelDisplayName: "Dots3-Note Preview",
			},
		})
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}
		if !compiled.BudgetAdvice.Overflow {
			t.Fatal("advisory must detect overflow")
		}
		if compiled.BudgetAdvice.Surface == nil {
			t.Fatal("compiled request must carry the decision surface")
		}
		if compiled.BudgetAdvice.Surface.Target != "index.html" {
			t.Errorf("surface target = %q", compiled.BudgetAdvice.Surface.Target)
		}
	})

	t.Run("advisory fit leaves no surface", func(t *testing.T) {
		compiled, err := engine.Execute(PreflightRequest{
			RawInput:    "update index.html",
			WorkDir:     ".",
			TokenBudget: 1000,
			BudgetAdvisory: BudgetAdvisoryRequest{
				TargetFile:      "index.html",
				FileSizeBytes:   400,
				Strategy:        StrategyFullRewrite,
				MaxOutputTokens: 4096,
			},
		})
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}
		if compiled.BudgetAdvice.Overflow {
			t.Fatal("small file must not overflow")
		}
		if compiled.BudgetAdvice.Surface != nil {
			t.Fatal("fit must not build a surface")
		}
	})

	t.Run("no advisory request yields zero advice", func(t *testing.T) {
		compiled, err := engine.Execute(PreflightRequest{
			RawInput:    "update index.html",
			WorkDir:     ".",
			TokenBudget: 1000,
		})
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}
		if compiled.BudgetAdvice.Surface != nil || compiled.BudgetAdvice.Overflow {
			t.Errorf("unrequested advisory must be zero, got %+v", compiled.BudgetAdvice)
		}
	})

	t.Run("custom advisor is honored via WithBudgetAdvisor", func(t *testing.T) {
		wired := engine.WithBudgetAdvisor(NewBudgetAdvisor())
		if wired == nil {
			t.Fatal("WithBudgetAdvisor must return the engine")
		}
		compiled, err := wired.Execute(PreflightRequest{
			RawInput:    "update index.html",
			WorkDir:     ".",
			TokenBudget: 1000,
			BudgetAdvisory: BudgetAdvisoryRequest{
				TargetFile:      "index.html",
				FileSizeBytes:   7780,
				Strategy:        StrategyFullRewrite,
				MaxOutputTokens: 1024,
			},
		})
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}
		if !compiled.BudgetAdvice.Overflow {
			t.Fatal("wired advisor must still detect overflow")
		}
	})

	t.Run("nil engine WithBudgetAdvisor is a no-op", func(t *testing.T) {
		var nilEngine *PreflightEngine
		if got := nilEngine.WithBudgetAdvisor(NewBudgetAdvisor()); got != nil {
			t.Error("nil engine must return nil")
		}
	})
}
