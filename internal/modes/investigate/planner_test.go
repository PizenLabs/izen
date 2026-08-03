package investigate

import (
	"context"
	"strings"
	"testing"
)

// fakePlanner is a scripted ContextPlanner.
type fakePlanner struct {
	planned string
	err     error
	calls   int
}

func (f *fakePlanner) PlanAssembled(_ context.Context, _ string) (string, error) {
	f.calls++
	return f.planned, f.err
}

func TestEnrichWithPlanner(t *testing.T) {
	diagnostics := "panic: nil pointer dereference\nmain.handle (server.go:42)"

	t.Run("nil planner is a no-op", func(t *testing.T) {
		e := &Engine{}
		if got := e.enrichWithPlanner(context.Background(), diagnostics); got != diagnostics {
			t.Errorf("nil planner changed diagnostics:\n%s", got)
		}
	})

	t.Run("planner enriches as prefix", func(t *testing.T) {
		fp := &fakePlanner{planned: "## TOOL LOG\npanic trace"}
		e := &Engine{planner: fp}
		got := e.enrichWithPlanner(context.Background(), diagnostics)
		if !strings.HasPrefix(got, "## TOOL LOG") {
			t.Errorf("planned context must prefix diagnostics, got:\n%s", got)
		}
		if !strings.HasSuffix(got, diagnostics) {
			t.Errorf("original diagnostics must be preserved verbatim, got:\n%s", got)
		}
		if fp.calls != 1 {
			t.Errorf("planner called %d times, want 1", fp.calls)
		}
	})

	t.Run("empty plan keeps diagnostics", func(t *testing.T) {
		fp := &fakePlanner{planned: ""}
		e := &Engine{planner: fp}
		if got := e.enrichWithPlanner(context.Background(), diagnostics); got != diagnostics {
			t.Errorf("empty plan changed diagnostics:\n%s", got)
		}
	})

	t.Run("planner error keeps diagnostics", func(t *testing.T) {
		fp := &fakePlanner{err: context.Canceled}
		e := &Engine{planner: fp}
		if got := e.enrichWithPlanner(context.Background(), diagnostics); got != diagnostics {
			t.Errorf("planner error changed diagnostics:\n%s", got)
		}
	})

	t.Run("empty diagnostics skips planner", func(t *testing.T) {
		fp := &fakePlanner{planned: "unused"}
		e := &Engine{planner: fp}
		if got := e.enrichWithPlanner(context.Background(), ""); got != "" {
			t.Errorf("empty diagnostics should stay empty, got %q", got)
		}
		if fp.calls != 0 {
			t.Errorf("planner called %d times for empty diagnostics, want 0", fp.calls)
		}
	})
}
