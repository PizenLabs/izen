package context

import (
	"strings"
	"testing"
)

func TestExpansionDepthGradient(t *testing.T) {
	t.Parallel()

	compiler := NewCompiler()
	tests := []struct {
		name       string
		confidence float64
		want       ExpansionDepth
	}{
		{name: "deep above upper boundary", confidence: 0.86, want: DepthDeep},
		{name: "deep at maximum", confidence: 1.0, want: DepthDeep},
		{name: "deep beyond one", confidence: 2.0, want: DepthDeep},
		{name: "conservative upper boundary", confidence: 0.85, want: DepthConservative},
		{name: "conservative middle", confidence: 0.70, want: DepthConservative},
		{name: "conservative lower boundary", confidence: 0.60, want: DepthConservative},
		{name: "minimal below lower boundary", confidence: 0.59, want: DepthMinimal},
		{name: "minimal zero", confidence: 0.0, want: DepthMinimal},
		{name: "minimal negative", confidence: -0.5, want: DepthMinimal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := compiler.DetermineDepth(tt.confidence); got != tt.want {
				t.Errorf("DetermineDepth(%v) = %v, want %v", tt.confidence, got, tt.want)
			}
		})
	}
}

func TestDepthKindAccessibility(t *testing.T) {
	t.Parallel()

	if !kindAccessible(KindTargetState, DepthMinimal) {
		t.Errorf("target state should be accessible at minimal depth")
	}
	if kindAccessible(KindManifest, DepthMinimal) {
		t.Errorf("manifest should not be accessible at minimal depth")
	}
	if !kindAccessible(KindManifest, DepthConservative) {
		t.Errorf("manifest should be accessible at conservative depth")
	}
	if kindAccessible(KindTopology, DepthConservative) {
		t.Errorf("topology should not be accessible at conservative depth")
	}
	if !kindAccessible(KindSourceSnippet, DepthDeep) {
		t.Errorf("source snippet should be accessible at deep depth")
	}
	if kindAccessible(KindTargetState, ExpansionDepth(99)) {
		t.Errorf("unknown depth should deny all kinds")
	}
}

func TestKnapsackPackingBudget(t *testing.T) {
	t.Parallel()

	compiler := NewCompiler()
	units := []ContextUnit{
		{ID: "a", Kind: KindTargetState, Content: "A", TokenCost: 100, Relevance: 0.90},
		{ID: "b", Kind: KindManifest, Content: "B", TokenCost: 50, Relevance: 0.95},
		{ID: "c", Kind: KindSourceSnippet, Content: "C", TokenCost: 30, Relevance: 0.80},
		{ID: "d", Kind: KindTopology, Content: "D", TokenCost: 200, Relevance: 0.70},
		{ID: "e", Kind: KindTargetState, Content: "E", TokenCost: 10, Relevance: 0.99},
	}

	t.Run("greedy ranking under budget", func(t *testing.T) {
		t.Parallel()
		cc, err := compiler.Compile(
			IntentSpec{ActionDescription: "refactor package", Confidence: 1.0},
			units,
			150,
		)
		if err != nil {
			t.Fatalf("Compile returned error: %v", err)
		}

		if cc.TotalTokens > cc.Budget {
			t.Errorf("TotalTokens %d exceeds budget %d", cc.TotalTokens, cc.Budget)
		}
		if cc.Budget != 150 {
			t.Errorf("Budget = %d, want 150", cc.Budget)
		}
		if cc.Depth != DepthDeep {
			t.Errorf("Depth = %v, want DepthDeep", cc.Depth)
		}
		if cc.Intent != "refactor package" {
			t.Errorf("Intent = %q, want %q", cc.Intent, "refactor package")
		}

		// Ranked order: e(0.99), b(0.95), c(0.80); a(0.90) costs 100 and would
		// push past the 150 budget, d(0.70) costs 200.
		wantIDs := []string{"e", "b", "c"}
		if len(cc.Units) != len(wantIDs) {
			t.Fatalf("Units len = %d, want %d (IDs %v)", len(cc.Units), len(wantIDs), wantIDs)
		}
		for i, wantID := range wantIDs {
			if cc.Units[i].ID != wantID {
				t.Errorf("Units[%d].ID = %q, want %q", i, cc.Units[i].ID, wantID)
			}
		}
		if cc.TotalTokens != 90 {
			t.Errorf("TotalTokens = %d, want 90", cc.TotalTokens)
		}

		// Monotonic relevance: no later unit may outrank an earlier one.
		for i := 1; i < len(cc.Units); i++ {
			if cc.Units[i].Relevance > cc.Units[i-1].Relevance {
				t.Errorf("Units[%d] relevance %v exceeds Units[%d] relevance %v",
					i, cc.Units[i].Relevance, i-1, cc.Units[i-1].Relevance)
			}
		}
	})

	t.Run("zero budget packs nothing", func(t *testing.T) {
		t.Parallel()
		cc, err := compiler.Compile(IntentSpec{Confidence: 1.0}, units, 0)
		if err != nil {
			t.Fatalf("Compile returned error: %v", err)
		}
		if len(cc.Units) != 0 {
			t.Errorf("Units len = %d, want 0", len(cc.Units))
		}
		if cc.TotalTokens != 0 {
			t.Errorf("TotalTokens = %d, want 0", cc.TotalTokens)
		}
	})

	t.Run("nil candidates degrade gracefully", func(t *testing.T) {
		t.Parallel()
		cc, err := compiler.Compile(IntentSpec{Confidence: 1.0}, nil, 1000)
		if err != nil {
			t.Fatalf("Compile returned error: %v", err)
		}
		if len(cc.Units) != 0 || cc.TotalTokens != 0 {
			t.Errorf("expected empty result, got %d units / %d tokens", len(cc.Units), cc.TotalTokens)
		}
	})

	t.Run("negative budget errors", func(t *testing.T) {
		t.Parallel()
		if _, err := compiler.Compile(IntentSpec{Confidence: 1.0}, units, -1); err == nil {
			t.Errorf("expected error for negative token budget")
		}
	})

	t.Run("malformed negative cost skipped", func(t *testing.T) {
		t.Parallel()
		bad := []ContextUnit{
			{ID: "bad", Kind: KindTargetState, TokenCost: -5, Relevance: 1.0},
			{ID: "ok", Kind: KindTargetState, TokenCost: 10, Relevance: 0.5},
		}
		cc, err := compiler.Compile(IntentSpec{Confidence: 1.0}, bad, 100)
		if err != nil {
			t.Fatalf("Compile returned error: %v", err)
		}
		if len(cc.Units) != 1 || cc.Units[0].ID != "ok" {
			t.Errorf("malformed unit not skipped, units = %+v", cc.Units)
		}
	})
}

func TestDepthFiltersCandidateKinds(t *testing.T) {
	t.Parallel()

	compiler := NewCompiler()
	units := []ContextUnit{
		{ID: "target", Kind: KindTargetState, TokenCost: 10, Relevance: 0.9},
		{ID: "manifest", Kind: KindManifest, TokenCost: 10, Relevance: 0.9},
		{ID: "topology", Kind: KindTopology, TokenCost: 10, Relevance: 0.9},
		{ID: "source", Kind: KindSourceSnippet, TokenCost: 10, Relevance: 0.9},
	}

	t.Run("minimal depth exposes only target state", func(t *testing.T) {
		t.Parallel()
		cc, err := compiler.Compile(IntentSpec{Confidence: 0.5}, units, 100)
		if err != nil {
			t.Fatalf("Compile returned error: %v", err)
		}
		if len(cc.Units) != 1 || cc.Units[0].ID != "target" {
			t.Errorf("minimal depth units = %+v, want only target", cc.Units)
		}
		if cc.Depth != DepthMinimal {
			t.Errorf("Depth = %v, want DepthMinimal", cc.Depth)
		}
	})

	t.Run("conservative depth adds manifests", func(t *testing.T) {
		t.Parallel()
		cc, err := compiler.Compile(IntentSpec{Confidence: 0.70}, units, 100)
		if err != nil {
			t.Fatalf("Compile returned error: %v", err)
		}
		got := map[string]bool{}
		for _, u := range cc.Units {
			got[u.ID] = true
		}
		if !got["target"] || !got["manifest"] {
			t.Errorf("conservative depth units = %v, want target and manifest", cc.Units)
		}
		if got["topology"] || got["source"] {
			t.Errorf("conservative depth must not expose topology/source: %v", cc.Units)
		}
		if cc.Depth != DepthConservative {
			t.Errorf("Depth = %v, want DepthConservative", cc.Depth)
		}
	})

	t.Run("deep depth exposes everything", func(t *testing.T) {
		t.Parallel()
		cc, err := compiler.Compile(IntentSpec{Confidence: 0.95}, units, 100)
		if err != nil {
			t.Fatalf("Compile returned error: %v", err)
		}
		if len(cc.Units) != 4 {
			t.Errorf("deep depth units len = %d, want 4", len(cc.Units))
		}
		if cc.Depth != DepthDeep {
			t.Errorf("Depth = %v, want DepthDeep", cc.Depth)
		}
	})
}

func TestTokenCostTieBreaksByRelevance(t *testing.T) {
	t.Parallel()

	compiler := NewCompiler()
	units := []ContextUnit{
		{ID: "eq-rel-costly", Kind: KindTargetState, TokenCost: 30, Relevance: 0.8},
		{ID: "eq-rel-cheap", Kind: KindTargetState, TokenCost: 10, Relevance: 0.8},
		{ID: "lower-rel", Kind: KindTargetState, TokenCost: 10, Relevance: 0.5},
		{ID: "over-budget", Kind: KindTargetState, TokenCost: 400, Relevance: 0.99},
	}
	cc, err := compiler.Compile(IntentSpec{Confidence: 1.0}, units, 50)
	if err != nil {
		t.Fatalf("Compile returned error: %v", err)
	}

	// Equal relevance must break by token cost ascending; the over-budget
	// highest-relevance unit is skipped by the greedy packer.
	wantIDs := []string{"eq-rel-cheap", "eq-rel-costly", "lower-rel"}
	if len(cc.Units) != len(wantIDs) {
		t.Fatalf("Units len = %d, want %d (IDs %v)", len(cc.Units), len(wantIDs), wantIDs)
	}
	for i, wantID := range wantIDs {
		if cc.Units[i].ID != wantID {
			t.Errorf("Units[%d].ID = %q, want %q", i, cc.Units[i].ID, wantID)
		}
	}
	if cc.TotalTokens != 50 {
		t.Errorf("TotalTokens = %d, want 50", cc.TotalTokens)
	}
}

func TestCompiledContextString(t *testing.T) {
	t.Parallel()

	var nilCC *CompiledContext
	if got := nilCC.String(); got != "context: <nil>" {
		t.Errorf("nil String() = %q, want %q", got, "context: <nil>")
	}

	cc := &CompiledContext{Intent: "update", Depth: DepthDeep, Units: []ContextUnit{{ID: "u"}}, TotalTokens: 10, Budget: 20}
	got := cc.String()
	if !strings.Contains(got, `intent="update"`) || !strings.Contains(got, "depth=deep") || !strings.Contains(got, "units=1") {
		t.Errorf("String() = %q, missing expected fields", got)
	}
}

func TestExpansionDepthString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		depth ExpansionDepth
		want  string
	}{
		{DepthMinimal, "minimal"},
		{DepthConservative, "conservative"},
		{DepthDeep, "deep"},
		{ExpansionDepth(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.depth.String(); got != tt.want {
			t.Errorf("ExpansionDepth(%d).String() = %q, want %q", tt.depth, got, tt.want)
		}
	}
}

func TestContextKindString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		kind ContextKind
		want string
	}{
		{KindTargetState, "target"},
		{KindManifest, "manifest"},
		{KindTopology, "topology"},
		{KindSourceSnippet, "source"},
		{ContextKind(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.kind.String(); got != tt.want {
			t.Errorf("ContextKind(%d).String() = %q, want %q", tt.kind, got, tt.want)
		}
	}
}

func TestXMLProvenanceOutput(t *testing.T) {
	t.Parallel()

	t.Run("exact structural output", func(t *testing.T) {
		t.Parallel()
		cc := &CompiledContext{
			Units: []ContextUnit{
				{ID: "target_state", Source: "README.md", Kind: KindTargetState,
					Content: "update title", TokenCost: 120, Relevance: 1.0},
				{ID: "manifest_go", Source: "go.mod", Kind: KindManifest,
					Content: "module", TokenCost: 1300, Relevance: 0.95},
			},
			TotalTokens: 1420,
			Budget:      2000,
			Depth:       DepthDeep,
			Intent:      "update readme",
		}

		got, err := RenderXML(cc)
		if err != nil {
			t.Fatalf("RenderXML returned error: %v", err)
		}

		want := `<compiled_context intent="update readme" budget_tokens="2000" used_tokens="1420" depth="deep">
  <context_unit id="target_state" source="README.md" kind="target" relevance="1.00" token_cost="120">
    update title
  </context_unit>
  <context_unit id="manifest_go" source="go.mod" kind="manifest" relevance="0.95" token_cost="1300">
    module
  </context_unit>
</compiled_context>`

		if got != want {
			t.Errorf("RenderXML output mismatch:\ngot:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("attribute and body escaping", func(t *testing.T) {
		t.Parallel()
		cc := &CompiledContext{
			Units: []ContextUnit{
				{ID: `a"<&'`, Source: `s&<`, Kind: KindSourceSnippet,
					Content: `<tag>&"` + "'", TokenCost: 5, Relevance: 0.5},
			},
			TotalTokens: 5,
			Budget:      100,
			Depth:       DepthConservative,
			Intent:      `intent<&"`,
		}

		got, err := RenderXML(cc)
		if err != nil {
			t.Fatalf("RenderXML returned error: %v", err)
		}

		want := `<compiled_context intent="intent&lt;&amp;&#34;" budget_tokens="100" used_tokens="5" depth="conservative">
  <context_unit id="a&#34;&lt;&amp;&#39;" source="s&amp;&lt;" kind="source" relevance="0.50" token_cost="5">
    &lt;tag&gt;&amp;&#34;&#39;
  </context_unit>
</compiled_context>`

		if got != want {
			t.Errorf("RenderXML escaping mismatch:\ngot:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("empty content inlines close tag", func(t *testing.T) {
		t.Parallel()
		cc := &CompiledContext{
			Units: []ContextUnit{
				{ID: "empty", Source: "s", Kind: KindTopology, TokenCost: 1, Relevance: 0.1},
			},
			TotalTokens: 1,
			Budget:      10,
			Depth:       DepthDeep,
		}

		got, err := RenderXML(cc)
		if err != nil {
			t.Fatalf("RenderXML returned error: %v", err)
		}

		want := `<compiled_context intent="unknown" budget_tokens="10" used_tokens="1" depth="deep">
  <context_unit id="empty" source="s" kind="topology" relevance="0.10" token_cost="1"></context_unit>
</compiled_context>`

		if got != want {
			t.Errorf("RenderXML empty-content mismatch:\ngot:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("empty units", func(t *testing.T) {
		t.Parallel()
		cc := &CompiledContext{Budget: 10, Depth: DepthMinimal, Intent: "noop"}

		got, err := RenderXML(cc)
		if err != nil {
			t.Fatalf("RenderXML returned error: %v", err)
		}

		want := `<compiled_context intent="noop" budget_tokens="10" used_tokens="0" depth="minimal">
</compiled_context>`

		if got != want {
			t.Errorf("RenderXML empty-units mismatch:\ngot:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("nil compiled context errors", func(t *testing.T) {
		t.Parallel()
		if _, err := RenderXML(nil); err == nil {
			t.Errorf("expected error for nil CompiledContext")
		}
	})
}
