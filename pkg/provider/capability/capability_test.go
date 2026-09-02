package capability

import (
	"reflect"
	"testing"
)

func TestEffortLevelString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		level EffortLevel
		want  string
	}{
		{EffortAuto, "auto"},
		{EffortLow, "low"},
		{EffortMedium, "medium"},
		{EffortHigh, "high"},
		{EffortXHigh, "xhigh"},
		{EffortLevel(""), ""},
	}
	for _, tt := range tests {
		if got := tt.level.String(); got != tt.want {
			t.Errorf("EffortLevel(%q).String() = %q, want %q", tt.level, got, tt.want)
		}
	}
}

func TestEffortLevelValid(t *testing.T) {
	t.Parallel()
	for _, lvl := range []EffortLevel{EffortAuto, EffortLow, EffortMedium, EffortHigh, EffortXHigh} {
		if !lvl.Valid() {
			t.Errorf("EffortLevel(%q) should be valid", lvl)
		}
	}
	if EffortLevel("turbo").Valid() {
		t.Error("unknown effort level must be invalid")
	}
	if EffortLevel("").Valid() {
		t.Error("empty effort level must be invalid")
	}
}

func TestModelCapabilitiesEffortOptions(t *testing.T) {
	t.Parallel()
	base := []EffortLevel{EffortAuto, EffortLow, EffortMedium, EffortHigh}

	chat := ModelCapabilities{ModelID: "gpt-4o", SupportsReasoning: false}
	if opts := chat.EffortOptions(); opts != nil {
		t.Errorf("non-reasoning model must yield nil effort options, got %v", opts)
	}
	if chat.SupportsEffort(EffortHigh) {
		t.Error("non-reasoning model must not support any effort")
	}

	reasoning := ModelCapabilities{
		ModelID:           "o3-mini",
		SupportsReasoning: true,
		SupportedEfforts:  append([]EffortLevel(nil), base...),
	}
	opts := reasoning.EffortOptions()
	if !reflect.DeepEqual(opts, base) {
		t.Errorf("EffortOptions() = %v, want %v", opts, base)
	}
	// Mutation of the returned copy must not corrupt the source.
	opts[0] = EffortXHigh
	if reasoning.SupportedEfforts[0] != EffortAuto {
		t.Error("EffortOptions must return a defensive copy")
	}
	for _, e := range base {
		if !reasoning.SupportsEffort(e) {
			t.Errorf("model must support %q", e)
		}
	}
	if reasoning.SupportsEffort(EffortXHigh) {
		t.Error("model must not support xhigh")
	}
}

func TestModelCapabilitiesNormalize(t *testing.T) {
	t.Parallel()

	t.Run("fills name provider and heuristics", func(t *testing.T) {
		c := ModelCapabilities{ModelID: "deepseek-r1"}.Normalize()
		if c.Provider != "unknown" {
			t.Errorf("provider = %q, want unknown", c.Provider)
		}
		if c.Name != "deepseek-r1" {
			t.Errorf("name = %q, want model id", c.Name)
		}
		if c.ContextWindow != 128000 {
			t.Errorf("context window = %d, want 128000", c.ContextWindow)
		}
		if c.MaxOutputTokens != 65536 {
			t.Errorf("max output = %d, want 65536", c.MaxOutputTokens)
		}
	})

	t.Run("derives extended efforts for reasoning models", func(t *testing.T) {
		c := ModelCapabilities{Provider: "deepseek", ModelID: "deepseek-r1", SupportsReasoning: true}.Normalize()
		want := []EffortLevel{EffortAuto, EffortLow, EffortMedium, EffortHigh, EffortXHigh}
		if !reflect.DeepEqual(c.SupportedEfforts, want) {
			t.Errorf("SupportedEfforts = %v, want %v", c.SupportedEfforts, want)
		}
	})

	t.Run("clears efforts for non-reasoning models", func(t *testing.T) {
		c := ModelCapabilities{
			Provider:          "openai",
			ModelID:           "gpt-4o",
			SupportsReasoning: false,
			SupportedEfforts:  []EffortLevel{EffortHigh},
		}.Normalize()
		if c.SupportedEfforts != nil {
			t.Errorf("SupportedEfforts = %v, want nil", c.SupportedEfforts)
		}
	})

	t.Run("preserves advertised values", func(t *testing.T) {
		c := ModelCapabilities{
			Provider:        "openrouter",
			ModelID:         "openai/o3",
			Name:            "O3",
			ContextWindow:   300000,
			MaxOutputTokens: 50000,
		}.Normalize()
		if c.ContextWindow != 300000 || c.MaxOutputTokens != 50000 {
			t.Errorf("advertised values lost: %d/%d", c.ContextWindow, c.MaxOutputTokens)
		}
	})
}

func TestThinkingBudget(t *testing.T) {
	t.Parallel()

	model := ModelCapabilities{MaxOutputTokens: 32000}
	tests := []struct {
		effort EffortLevel
		want   int
	}{
		{EffortAuto, 0},
		{EffortLow, 4000},     // min(4000, 8000)
		{EffortMedium, 16000}, // min(16000, 16000)
		{EffortHigh, 25600},   // min(32000, 25600)
		{EffortXHigh, 32000},  // full budget
		{EffortLevel(""), 0},  // nil effort behaves like auto
		{EffortLevel("bogus"), 0},
	}
	for _, tt := range tests {
		if got := model.ThinkingBudget(tt.effort); got != tt.want {
			t.Errorf("ThinkingBudget(%q) = %d, want %d", tt.effort, got, tt.want)
		}
	}

	t.Run("caps low budget at 4k", func(t *testing.T) {
		big := ModelCapabilities{MaxOutputTokens: 65536}
		if got := big.ThinkingBudget(EffortLow); got != 4000 {
			t.Errorf("low budget = %d, want 4000", got)
		}
	})

	t.Run("fallback max output", func(t *testing.T) {
		zero := ModelCapabilities{}
		if got := zero.ThinkingBudget(EffortMedium); got != 4096 { // 50% of 8192
			t.Errorf("medium budget without max = %d, want 4096", got)
		}
	})
}

func TestTotalMaxTokens(t *testing.T) {
	t.Parallel()

	t.Run("auto effort uses advertised max", func(t *testing.T) {
		m := ModelCapabilities{MaxOutputTokens: 2048}
		if got := m.TotalMaxTokens(EffortAuto); got != 2048 {
			t.Errorf("TotalMaxTokens(auto) = %d, want 2048", got)
		}
	})

	t.Run("capped at advertised max", func(t *testing.T) {
		m := ModelCapabilities{MaxOutputTokens: 1024}
		// high budget = min(32000, 819) = 819; 819+4096 = 4915 → capped to 1024.
		if got := m.TotalMaxTokens(EffortHigh); got != 1024 {
			t.Errorf("TotalMaxTokens(high) = %d, want 1024", got)
		}
	})

	t.Run("xhigh yields advertised max", func(t *testing.T) {
		m := ModelCapabilities{MaxOutputTokens: 65536}
		if got := m.TotalMaxTokens(EffortXHigh); got != 65536 {
			t.Errorf("TotalMaxTokens(xhigh) = %d, want 65536", got)
		}
	})

	t.Run("fallback default when max unknown", func(t *testing.T) {
		m := ModelCapabilities{}
		// xhigh budget = 8192 (default); total = min(8192+4096, 8192) = 8192.
		if got := m.TotalMaxTokens(EffortXHigh); got != 8192 {
			t.Errorf("TotalMaxTokens(xhigh) without max = %d, want 8192", got)
		}
	})
}

func TestSplitVendor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in     string
		vendor string
		model  string
	}{
		{"openai/o3-mini", "openai", "o3-mini"},
		{"deepseek/deepseek-r1", "deepseek", "deepseek-r1"},
		{"gpt-4o", "", "gpt-4o"},
		{"", "", ""},
	}
	for _, tt := range tests {
		vendor, model := splitVendor(tt.in)
		if vendor != tt.vendor || model != tt.model {
			t.Errorf("splitVendor(%q) = (%q, %q), want (%q, %q)", tt.in, vendor, model, tt.vendor, tt.model)
		}
	}
}

func TestHasAnyPrefix(t *testing.T) {
	t.Parallel()
	if !hasAnyPrefix("o3-mini", "o1", "o3") {
		t.Error("o3-mini must match o3 prefix")
	}
	if hasAnyPrefix("gpt-4o", "o1", "o3") {
		t.Error("gpt-4o must not match o1/o3 prefixes")
	}
	if !hasAnyPrefix("abc", "") {
		t.Error("empty prefix must match everything")
	}
	if hasAnyPrefix("abc") {
		t.Error("no prefixes must not match")
	}
}
