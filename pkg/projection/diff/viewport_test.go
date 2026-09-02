package diff

import (
	"strings"
	"testing"
)

func evidence(lines ...PatchLine) MutationEvidence {
	ev := MutationEvidence{TargetFile: "test.go", Lines: lines}
	for _, l := range lines {
		switch l.Type {
		case MutationAdd:
			ev.Added++
		case MutationDelete:
			ev.Deleted++
		}
	}
	return ev
}

func TestCellWidthCalculation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		line     string
		tabWidth int
		want     int
	}{
		{name: "plain ascii", line: "hello", tabWidth: 4, want: 5},
		{name: "default tab expansion", line: "a\tb", tabWidth: 4, want: 6},
		{name: "custom tab expansion", line: "a\tb", tabWidth: 8, want: 10},
		{name: "zero tab width falls back to default", line: "a\tb", tabWidth: 0, want: 6},
		{name: "negative tab width falls back to default", line: "\t", tabWidth: -1, want: 4},
		{name: "ansi color stripped", line: "\x1b[31mred\x1b[0m", tabWidth: 4, want: 3},
		{name: "pure ansi sequence", line: "\x1b[31m", tabWidth: 4, want: 0},
		{name: "osc hyperlink stripped", line: "\x1b]8;;http://example.com\x1b\\link\x1b]8;;\x1b\\", tabWidth: 4, want: 4},
		{name: "two byte escape stripped", line: "\x1b7mid\x1bM", tabWidth: 4, want: 3},
		{name: "trailing lone escape", line: "ab\x1b", tabWidth: 4, want: 2},
		{name: "cjk double width", line: "中文", tabWidth: 4, want: 4},
		{name: "mixed cjk and ascii", line: "a中b", tabWidth: 4, want: 4},
		{name: "combining mark is zero width", line: "e\u0301", tabWidth: 4, want: 1},
		{name: "empty line", line: "", tabWidth: 4, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := CalculateCellWidth(tt.line, tt.tabWidth); got != tt.want {
				t.Errorf("CalculateCellWidth(%q, %d) = %d, want %d", tt.line, tt.tabWidth, got, tt.want)
			}
		})
	}
}

func TestFullInlineRenderPlan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cfg       ViewportConfig
		lines     []PatchLine
		wantTotal int
	}{
		{
			name: "small diff fits budget",
			cfg:  ViewportConfig{TermWidth: 80, TermHeight: 20, GutterWidth: 5, PrefixWidth: 2, BudgetRatio: 0.40, TabWidth: 4},
			lines: []PatchLine{
				{Type: MutationAdd, Content: "func foo() {"},
				{Type: MutationAdd, Content: "    return 1"},
				{Type: MutationDelete, Content: "func bar() {"},
			},
			wantTotal: 3,
		},
		{
			name: "wide line still fits",
			cfg:  ViewportConfig{TermWidth: 80, TermHeight: 20, GutterWidth: 5, PrefixWidth: 2, BudgetRatio: 0.40, TabWidth: 4},
			lines: []PatchLine{
				{Type: MutationModify, Content: strings.Repeat("中", 60)},
			},
			wantTotal: 2,
		},
		{
			name:      "empty evidence",
			cfg:       ViewportConfig{TermWidth: 80, TermHeight: 20, GutterWidth: 5, PrefixWidth: 2},
			lines:     nil,
			wantTotal: 0,
		},
		{
			name: "exact boundary total equals allowed",
			cfg:  ViewportConfig{TermWidth: 80, TermHeight: 10, GutterWidth: 5, PrefixWidth: 2},
			lines: []PatchLine{
				{Type: MutationAdd, Content: "a"},
				{Type: MutationAdd, Content: "b"},
				{Type: MutationAdd, Content: "c"},
				{Type: MutationAdd, Content: "d"},
			},
			wantTotal: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			plan := ComputeRenderPlan(evidence(tt.lines...), tt.cfg)

			if plan.Mode != RenderModeFullInline {
				t.Errorf("Mode = %v, want RenderModeFullInline", plan.Mode)
			}
			if plan.TotalVisual != tt.wantTotal {
				t.Errorf("TotalVisual = %d, want %d", plan.TotalVisual, tt.wantTotal)
			}
			if plan.TruncatedAt != 0 {
				t.Errorf("TruncatedAt = %d, want 0", plan.TruncatedAt)
			}
			if len(plan.VisibleLines) != len(tt.lines) {
				t.Errorf("VisibleLines len = %d, want %d", len(plan.VisibleLines), len(tt.lines))
			}
			if plan.AllowedRows != int(float64(tt.cfg.TermHeight)*0.40) {
				t.Errorf("AllowedRows = %d, want %d", plan.AllowedRows, int(float64(tt.cfg.TermHeight)*0.40))
			}
		})
	}
}

func TestTruncatedHeadTailRenderPlan(t *testing.T) {
	t.Parallel()

	t.Run("balanced uniform lines", func(t *testing.T) {
		t.Parallel()
		cfg := ViewportConfig{TermWidth: 80, TermHeight: 10, GutterWidth: 5, PrefixWidth: 2}
		lines := make([]PatchLine, 0, 7)
		for i := 0; i < 7; i++ {
			lines = append(lines, PatchLine{Type: MutationAdd, Content: "line"})
		}
		plan := ComputeRenderPlan(evidence(lines...), cfg)

		if plan.Mode != RenderModeTruncatedHeadTail {
			t.Fatalf("Mode = %v, want RenderModeTruncatedHeadTail", plan.Mode)
		}
		if plan.TruncatedAt != 3 {
			t.Errorf("TruncatedAt = %d, want 3", plan.TruncatedAt)
		}
		if len(plan.VisibleLines) != 4 {
			t.Fatalf("VisibleLines len = %d, want 4", len(plan.VisibleLines))
		}
		// Symmetric selection: first two head lines and last two tail lines.
		wantContents := []string{"line", "line", "line", "line"}
		for i, want := range wantContents {
			if plan.VisibleLines[i].Content != want {
				t.Errorf("VisibleLines[%d].Content = %q, want %q", i, plan.VisibleLines[i].Content, want)
			}
		}
		if plan.VisibleLines[0] != lines[0] || plan.VisibleLines[1] != lines[1] {
			t.Errorf("head slice does not mirror the evidence head")
		}
		if plan.VisibleLines[2] != lines[5] || plan.VisibleLines[3] != lines[6] {
			t.Errorf("tail slice does not mirror the evidence tail")
		}
	})

	t.Run("mixed long and short lines", func(t *testing.T) {
		t.Parallel()
		cfg := ViewportConfig{TermWidth: 30, TermHeight: 20, GutterWidth: 4, PrefixWidth: 2}
		contentWidth := 24
		long := strings.Repeat("a", 100)
		lines := []PatchLine{}
		for i := 0; i < 10; i++ {
			if i%2 == 0 {
				lines = append(lines, PatchLine{Type: MutationAdd, Content: "x"})
			} else {
				lines = append(lines, PatchLine{Type: MutationModify, Content: long})
			}
		}
		plan := ComputeRenderPlan(evidence(lines...), cfg)

		if plan.Mode != RenderModeTruncatedHeadTail {
			t.Fatalf("Mode = %v, want RenderModeTruncatedHeadTail", plan.Mode)
		}
		if plan.TruncatedAt <= 0 {
			t.Errorf("TruncatedAt = %d, want > 0", plan.TruncatedAt)
		}
		if plan.TotalVisual != 30 {
			t.Errorf("TotalVisual = %d, want 30", plan.TotalVisual)
		}
		if plan.VisibleLines[0] != lines[0] {
			t.Errorf("first visible line should mirror evidence head")
		}
		if plan.VisibleLines[len(plan.VisibleLines)-1] != lines[len(lines)-1] {
			t.Errorf("last visible line should mirror evidence tail")
		}
		// The selected head/tail slices must respect the allowed budget.
		visibleRows := 0
		for _, l := range plan.VisibleLines {
			w := CalculateCellWidth(l.Content, cfg.TabWidth)
			visibleRows += (w + contentWidth - 1) / contentWidth
		}
		if visibleRows > plan.AllowedRows {
			t.Errorf("visible rows %d exceed AllowedRows %d", visibleRows, plan.AllowedRows)
		}
	})

	t.Run("single oversized line degrades gracefully", func(t *testing.T) {
		t.Parallel()
		cfg := ViewportConfig{TermWidth: 30, TermHeight: 4, GutterWidth: 4, PrefixWidth: 2}
		long := strings.Repeat("a", 200)
		lines := []PatchLine{{Type: MutationAdd, Content: long}}
		plan := ComputeRenderPlan(evidence(lines...), cfg)

		if plan.Mode != RenderModeTruncatedHeadTail {
			t.Fatalf("Mode = %v, want RenderModeTruncatedHeadTail", plan.Mode)
		}
		if len(plan.VisibleLines) != 1 {
			t.Fatalf("VisibleLines len = %d, want 1", len(plan.VisibleLines))
		}
		if plan.VisibleLines[0] != lines[0] {
			t.Errorf("the single line should still be visible")
		}
	})

	t.Run("zero height degrades gracefully", func(t *testing.T) {
		t.Parallel()
		cfg := ViewportConfig{TermWidth: 80, TermHeight: 0}
		lines := []PatchLine{
			{Type: MutationAdd, Content: "a"},
			{Type: MutationAdd, Content: "b"},
		}
		plan := ComputeRenderPlan(evidence(lines...), cfg)

		if plan.Mode != RenderModeTruncatedHeadTail {
			t.Fatalf("Mode = %v, want RenderModeTruncatedHeadTail", plan.Mode)
		}
		if plan.AllowedRows != 0 {
			t.Errorf("AllowedRows = %d, want 0", plan.AllowedRows)
		}
		if len(plan.VisibleLines) != 1 {
			t.Fatalf("VisibleLines len = %d, want 1", len(plan.VisibleLines))
		}
	})

	t.Run("tiny content width guards division", func(t *testing.T) {
		t.Parallel()
		cfg := ViewportConfig{TermWidth: 2, TermHeight: 20, GutterWidth: 10, PrefixWidth: 10}
		lines := []PatchLine{
			{Type: MutationAdd, Content: strings.Repeat("b", 50)},
			{Type: MutationAdd, Content: strings.Repeat("c", 50)},
			{Type: MutationAdd, Content: strings.Repeat("d", 50)},
		}
		plan := ComputeRenderPlan(evidence(lines...), cfg)

		if plan.AllowedRows != 8 {
			t.Errorf("AllowedRows = %d, want 8", plan.AllowedRows)
		}
		if plan.Mode != RenderModeTruncatedHeadTail {
			t.Fatalf("Mode = %v, want RenderModeTruncatedHeadTail", plan.Mode)
		}
		// Each 50-cell line occupies 50 rows at content width 1, so only the
		// first line can be kept by the graceful-degradation fallback.
		if len(plan.VisibleLines) != 1 {
			t.Errorf("VisibleLines len = %d, want 1", len(plan.VisibleLines))
		}
		if plan.TruncatedAt != 2 {
			t.Errorf("TruncatedAt = %d, want 2", plan.TruncatedAt)
		}
	})
}

func TestConfigDefaultsApplied(t *testing.T) {
	t.Parallel()

	cfg := ViewportConfig{TermWidth: 80, TermHeight: 20} // zero BudgetRatio/TabWidth
	plan := ComputeRenderPlan(
		evidence(PatchLine{Type: MutationAdd, Content: "a\tb"}),
		cfg,
	)

	if plan.AllowedRows != 8 {
		t.Errorf("AllowedRows = %d, want 8 (default ratio 0.40)", plan.AllowedRows)
	}
	// Default tab width 4 must apply during width calculation.
	if plan.Mode != RenderModeFullInline {
		t.Errorf("Mode = %v, want RenderModeFullInline", plan.Mode)
	}
}

func TestNegativeGeometryClamped(t *testing.T) {
	t.Parallel()

	cfg := ViewportConfig{TermWidth: -5, TermHeight: -3, GutterWidth: -1, PrefixWidth: -2}
	plan := ComputeRenderPlan(
		evidence(PatchLine{Type: MutationAdd, Content: "x"}),
		cfg,
	)
	// TermHeight clamped to 0, so the plan must degrade to head-only.
	if plan.AllowedRows != 0 {
		t.Errorf("AllowedRows = %d, want 0", plan.AllowedRows)
	}
	if len(plan.VisibleLines) != 1 {
		t.Errorf("VisibleLines len = %d, want 1", len(plan.VisibleLines))
	}
}
