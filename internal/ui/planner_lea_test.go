package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/lea"
)

// TestPlanContextForAskUsesLeaEngine verifies the /ask flow's Context Planner
// is served from the Phase 3 Lea structural engine when one is attached (and
// no native graph exists): symbol definitions resolve from the Lea index.
func TestPlanContextForAskUsesLeaEngine(t *testing.T) {
	root := leaArchRepo(t)
	e := lea.NewEngine(root)
	t.Cleanup(func() { _ = e.Close() })
	if _, err := e.Index(context.Background()); err != nil {
		t.Fatalf("lea Index: %v", err)
	}

	m := newTestModel()
	m.leaEng = e
	m.graph = nil
	m.workspaceRoot = "."

	out := m.planContextForAsk("what is the Service struct in this project")
	if !strings.Contains(out, "PLANNED CONTEXT") {
		t.Fatalf("expected planned context header from the lea adapter, got:\n%s", out)
	}
	if !strings.Contains(out, "SYMBOL DEFINITIONS") {
		t.Errorf("expected symbol definitions section, got:\n%s", out)
	}
	if !strings.Contains(out, "Service") {
		t.Errorf("expected the Service symbol resolved from the lea graph, got:\n%s", out)
	}
	if m.planner == nil {
		t.Error("contextPlanner did not cache the planner")
	}
}
