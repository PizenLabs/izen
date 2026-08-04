package ui

import (
	"testing"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/pkg/engine/pipeline"
)

func testRouterModel(t *testing.T, mode string) string {
	pe := pipeline.NewEngine(t.TempDir(), nil,
		pipeline.WithRouter(pipeline.NewRouter(
			pipeline.WithModel(pipeline.IntentReasoning, "heavy-1"),
			pipeline.WithModel(pipeline.IntentExecution, "fast-1"),
			pipeline.WithModel(pipeline.IntentInformational, "mini-1"),
		)),
	)
	m := &model{pipelineEngine: pe, cfg: config.Default()}
	return m.routeModel(mode)
}

func TestRouteModelIntentBased(t *testing.T) {
	cases := map[string]string{
		"plan":        "heavy-1",
		"investigate": "heavy-1",
		"review":      "heavy-1",
		"build":       "fast-1",
		"ask":         "mini-1",
	}
	for mode, want := range cases {
		if got := testRouterModel(t, mode); got != want {
			t.Errorf("routeModel(%q) = %q, want %q", mode, got, want)
		}
	}
}

func TestRouteModelFallsBackToConfig(t *testing.T) {
	m := &model{cfg: config.Default()}
	want := m.cfg.ActiveModelName()
	if got := m.routeModel("build"); got != want {
		t.Errorf("routeModel without pipeline = %q, want configured %q", got, want)
	}
}

func TestActiveRouteModelUsesCurrentMode(t *testing.T) {
	pe := pipeline.NewEngine(t.TempDir(), nil,
		pipeline.WithRouter(pipeline.NewRouter(
			pipeline.WithModel(pipeline.IntentReasoning, "heavy-1"),
			pipeline.WithModel(pipeline.IntentExecution, "fast-1"),
		)),
	)
	resolver := modes.NewResolver()
	resolver.Set(modes.ModePlan)
	m := &model{pipelineEngine: pe, cfg: config.Default(), resolver: resolver}
	if got := m.activeRouteModel(); got != "heavy-1" {
		t.Errorf("activeRouteModel in plan mode = %q, want heavy-1", got)
	}
}
