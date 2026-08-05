package task

import (
	"errors"
	"reflect"
	"sync"
	"testing"
)

func TestGraphAddGetHasLen(t *testing.T) {
	g := New()
	if g.Len() != 0 {
		t.Fatalf("fresh graph Len = %d, want 0", g.Len())
	}
	a := Node{ID: "a", Description: "first", Dependencies: []string{"c"}}
	if err := g.Add(a); err != nil {
		t.Fatalf("Add(a): %v", err)
	}
	if !g.Has("a") {
		t.Error("Has(a) = false after Add")
	}
	if g.Has("missing") {
		t.Error("Has(missing) = true")
	}
	got, ok := g.Get("a")
	if !ok {
		t.Fatal("Get(a) not found")
	}
	if !reflect.DeepEqual(got, a) {
		t.Errorf("Get(a) = %+v, want %+v", got, a)
	}
	if g.Len() != 1 {
		t.Errorf("Len = %d, want 1", g.Len())
	}
}

func TestGraphAddRejectsInvalid(t *testing.T) {
	g := New()
	if err := g.Add(Node{ID: ""}); !errors.Is(err, ErrEmptyTaskID) {
		t.Errorf("empty id err = %v, want ErrEmptyTaskID", err)
	}
	if err := g.Add(Node{ID: "a"}); err != nil {
		t.Fatalf("Add(a): %v", err)
	}
	if err := g.Add(Node{ID: "a"}); !errors.Is(err, ErrDuplicateTask) {
		t.Errorf("duplicate err = %v, want ErrDuplicateTask", err)
	}
	if err := g.Add(Node{ID: "b", Dependencies: []string{"b"}}); !errors.Is(err, ErrSelfDependency) {
		t.Errorf("self dep err = %v, want ErrSelfDependency", err)
	}
}

func TestGraphDependenciesAndDependents(t *testing.T) {
	g := New()
	mustAdd(t, g, Node{ID: "a"})
	mustAdd(t, g, Node{ID: "b", Dependencies: []string{"a"}})
	mustAdd(t, g, Node{ID: "c", Dependencies: []string{"a"}})
	mustAdd(t, g, Node{ID: "d", Dependencies: []string{"b", "c"}})

	if got := g.Dependencies("d"); !reflect.DeepEqual(got, []string{"b", "c"}) {
		t.Errorf("Dependencies(d) = %v", got)
	}
	if got := g.Dependencies("a"); len(got) != 0 {
		t.Errorf("Dependencies(a) = %v, want empty", got)
	}
	if got := g.Dependents("a"); !reflect.DeepEqual(got, []string{"b", "c"}) {
		t.Errorf("Dependents(a) = %v, want [b c]", got)
	}
	if got := g.Dependents("missing"); len(got) != 0 {
		t.Errorf("Dependents(missing) = %v, want empty", got)
	}
}

func TestGraphTasksSnapshotInsertionOrder(t *testing.T) {
	g := New()
	mustAdd(t, g, Node{ID: "x"})
	mustAdd(t, g, Node{ID: "y"})
	mustAdd(t, g, Node{ID: "z"})
	tasks := g.Tasks()
	if len(tasks) != 3 {
		t.Fatalf("Tasks len = %d, want 3", len(tasks))
	}
	ids := make([]string, 0, 3)
	for _, ts := range tasks {
		ids = append(ids, ts.ID)
	}
	if !reflect.DeepEqual(ids, []string{"x", "y", "z"}) {
		t.Errorf("Tasks order = %v, want [x y z]", ids)
	}
}

func TestGraphValidateValid(t *testing.T) {
	g := New()
	mustAdd(t, g, Node{ID: "a"})
	mustAdd(t, g, Node{ID: "b", Dependencies: []string{"a"}})
	mustAdd(t, g, Node{ID: "c", Dependencies: []string{"a", "b"}})
	if err := g.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestGraphValidateEmptyGraph(t *testing.T) {
	if err := New().Validate(); err != nil {
		t.Fatalf("empty graph Validate: %v", err)
	}
}

func TestGraphValidateMissingDependency(t *testing.T) {
	g := New()
	mustAdd(t, g, Node{ID: "a", Dependencies: []string{"ghost"}})
	err := g.Validate()
	if !errors.Is(err, ErrMissingDependency) {
		t.Fatalf("Validate err = %v, want ErrMissingDependency", err)
	}
}

func TestGraphValidateCycle(t *testing.T) {
	g := New()
	mustAdd(t, g, Node{ID: "a", Dependencies: []string{"b"}})
	mustAdd(t, g, Node{ID: "b", Dependencies: []string{"a"}})
	err := g.Validate()
	if !errors.Is(err, ErrCycleDetected) {
		t.Fatalf("Validate err = %v, want ErrCycleDetected", err)
	}
}

func TestGraphTopologicalOrder(t *testing.T) {
	g := New()
	mustAdd(t, g, Node{ID: "a"})
	mustAdd(t, g, Node{ID: "b", Dependencies: []string{"a"}})
	mustAdd(t, g, Node{ID: "c"})
	mustAdd(t, g, Node{ID: "d", Dependencies: []string{"b", "c"}})
	order, err := g.TopologicalOrder()
	if err != nil {
		t.Fatalf("TopologicalOrder: %v", err)
	}
	if len(order) != 4 {
		t.Fatalf("order len = %d, want 4", len(order))
	}
	pos := map[string]int{}
	for i, id := range order {
		pos[id] = i
	}
	for _, id := range []string{"d"} {
		for _, dep := range g.Dependencies(id) {
			if pos[dep] > pos[id] {
				t.Errorf("dependency %s scheduled after %s", dep, id)
			}
		}
	}
}

func TestGraphTopologicalOrderCycle(t *testing.T) {
	g := New()
	mustAdd(t, g, Node{ID: "a", Dependencies: []string{"b"}})
	mustAdd(t, g, Node{ID: "b", Dependencies: []string{"a"}})
	if _, err := g.TopologicalOrder(); !errors.Is(err, ErrCycleDetected) {
		t.Fatalf("TopologicalOrder err = %v, want ErrCycleDetected", err)
	}
}

func TestGraphReady(t *testing.T) {
	g := New()
	mustAdd(t, g, Node{ID: "a"})
	mustAdd(t, g, Node{ID: "b", Dependencies: []string{"a"}})
	mustAdd(t, g, Node{ID: "c", Dependencies: []string{"b"}})

	if got := g.Ready(nil); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("Ready(nil) = %v, want [a]", got)
	}
	if got := g.Ready(map[string]bool{"a": true}); !reflect.DeepEqual(got, []string{"b"}) {
		t.Errorf("Ready({a}) = %v, want [b]", got)
	}
	if got := g.Ready(map[string]bool{"a": true, "b": true}); !reflect.DeepEqual(got, []string{"c"}) {
		t.Errorf("Ready({a,b}) = %v, want [c]", got)
	}
	if got := g.Ready(map[string]bool{"a": true, "b": true, "c": true}); len(got) != 0 {
		t.Errorf("Ready(all) = %v, want empty", got)
	}
}

func mustAdd(t *testing.T, g *Graph, ts Node) {
	t.Helper()
	if err := g.Add(ts); err != nil {
		t.Fatalf("Add(%s): %v", ts.ID, err)
	}
}

func TestCursorLinearChain(t *testing.T) {
	g := New()
	mustAdd(t, g, Node{ID: "a"})
	mustAdd(t, g, Node{ID: "b", Dependencies: []string{"a"}})
	mustAdd(t, g, Node{ID: "c", Dependencies: []string{"b"}})
	c := NewCursor(g)

	for _, want := range []string{"a", "b", "c"} {
		id, err := c.Advance()
		if err != nil {
			t.Fatalf("Advance: %v", err)
		}
		if id != want {
			t.Errorf("Advance = %q, want %q", id, want)
		}
		if got := c.Active(); !reflect.DeepEqual(got, []string{want}) {
			t.Errorf("Active() = %v, want [%s]", got, want)
		}
		if err := c.Complete(id); err != nil {
			t.Fatalf("Complete(%s): %v", id, err)
		}
	}
	if !c.IsComplete() {
		t.Error("IsComplete() = false after finishing all tasks")
	}
	if _, err := c.Advance(); !errors.Is(err, ErrCursorComplete) {
		t.Errorf("Advance after completion = %v, want ErrCursorComplete", err)
	}
	if got := c.Completed(); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("Completed() = %v, want [a b c]", got)
	}
	if len(c.Active()) != 0 {
		t.Errorf("Active() = %v, want empty", c.Active())
	}
}

func TestCursorSharedDependency(t *testing.T) {
	g := New()
	mustAdd(t, g, Node{ID: "setup"})
	mustAdd(t, g, Node{ID: "left", Dependencies: []string{"setup"}})
	mustAdd(t, g, Node{ID: "right", Dependencies: []string{"setup"}})
	c := NewCursor(g)

	id, err := c.Advance()
	if err != nil || id != "setup" {
		t.Fatalf("first Advance = (%q, %v), want (setup, nil)", id, err)
	}
	if err := c.Complete("setup"); err != nil {
		t.Fatalf("Complete(setup): %v", err)
	}
	for _, want := range []string{"left", "right"} {
		id, err := c.Advance()
		if err != nil {
			t.Fatalf("Advance: %v", err)
		}
		if id != want {
			t.Errorf("Advance = %q, want %q", id, want)
		}
	}
}

func TestCursorBlockedAndNoReady(t *testing.T) {
	g := New()
	mustAdd(t, g, Node{ID: "a", Dependencies: []string{"b"}})
	mustAdd(t, g, Node{ID: "b", Dependencies: []string{"a"}})
	c := NewCursor(g)

	if _, err := c.Advance(); !errors.Is(err, ErrNoReadyTask) {
		t.Fatalf("Advance on cycle = %v, want ErrNoReadyTask", err)
	}
	if got := c.Blocked(); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("Blocked() = %v, want [a b]", got)
	}
	if c.IsComplete() {
		t.Error("IsComplete() = true with blocked tasks")
	}
}

func TestCursorCompleteEnforcesDependencies(t *testing.T) {
	g := New()
	mustAdd(t, g, Node{ID: "a"})
	mustAdd(t, g, Node{ID: "b", Dependencies: []string{"a"}})
	c := NewCursor(g)

	if err := c.Complete("b"); !errors.Is(err, ErrDependenciesPending) {
		t.Errorf("Complete(b) before a = %v, want ErrDependenciesPending", err)
	}
	if err := c.Complete("ghost"); !errors.Is(err, ErrTaskNotFound) {
		t.Errorf("Complete(ghost) = %v, want ErrTaskNotFound", err)
	}

	if _, err := c.Advance(); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if err := c.Complete("a"); err != nil {
		t.Fatalf("Complete(a): %v", err)
	}
	if err := c.Complete("a"); !errors.Is(err, ErrTaskAlreadyCompleted) {
		t.Errorf("double Complete(a) = %v, want ErrTaskAlreadyCompleted", err)
	}
}

func TestCursorFail(t *testing.T) {
	g := New()
	mustAdd(t, g, Node{ID: "a"})
	mustAdd(t, g, Node{ID: "b", Dependencies: []string{"a"}})
	c := NewCursor(g)

	if _, err := c.Advance(); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if err := c.Fail("a"); err != nil {
		t.Fatalf("Fail(a): %v", err)
	}
	if got := c.Failed(); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("Failed() = %v, want [a]", got)
	}
	if len(c.Active()) != 0 {
		t.Errorf("Active() = %v after fail, want empty", c.Active())
	}
	// A failed dependency still blocks its dependents.
	if _, err := c.Advance(); !errors.Is(err, ErrNoReadyTask) {
		t.Errorf("Advance after failed dep = %v, want ErrNoReadyTask", err)
	}
}

func TestCursorProgress(t *testing.T) {
	g := New()
	mustAdd(t, g, Node{ID: "a"})
	mustAdd(t, g, Node{ID: "b", Dependencies: []string{"a"}})
	mustAdd(t, g, Node{ID: "c", Dependencies: []string{"b"}})
	c := NewCursor(g)

	if p := c.Progress(); p.Total != 3 || p.Pending != 1 || p.Blocked != 2 {
		t.Errorf("initial Progress = %+v, want Total=3 Pending=1 Blocked=2", p)
	}

	if _, err := c.Advance(); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if p := c.Progress(); p.Active != 1 || p.Pending != 0 || p.Blocked != 2 {
		t.Errorf("Progress after first advance = %+v, want Active=1 Blocked=2", p)
	}

	if err := c.Complete("a"); err != nil {
		t.Fatalf("Complete(a): %v", err)
	}
	// b is now ready; c remains blocked behind b.
	if p := c.Progress(); p.Completed != 1 || p.Pending != 1 || p.Blocked != 1 {
		t.Errorf("Progress after completing a = %+v, want Completed=1 Pending=1 Blocked=1", p)
	}
}

func TestCursorReset(t *testing.T) {
	g := New()
	mustAdd(t, g, Node{ID: "a"})
	mustAdd(t, g, Node{ID: "b", Dependencies: []string{"a"}})
	c := NewCursor(g)

	if _, err := c.Advance(); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if err := c.Complete("a"); err != nil {
		t.Fatalf("Complete(a): %v", err)
	}
	c.Reset()
	if got := c.Pending(); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("Pending() after reset = %v, want [a b]", got)
	}
	if len(c.Completed()) != 0 || len(c.Active()) != 0 {
		t.Error("cursor state not cleared by Reset")
	}
}

func TestCursorStatus(t *testing.T) {
	g := New()
	mustAdd(t, g, Node{ID: "a"})
	c := NewCursor(g)
	if st, ok := c.Status("a"); !ok || st != CursorStatusPending {
		t.Errorf("Status(a) = (%s, %v), want (pending, true)", st, ok)
	}
	if _, ok := c.Status("ghost"); ok {
		t.Error("Status(ghost) reported ok for unknown task")
	}
	if _, err := c.Advance(); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if st, _ := c.Status("a"); st != CursorStatusActive {
		t.Errorf("Status(a) = %s, want active", st)
	}
}

func TestCursorConcurrentReads(t *testing.T) {
	g := New()
	mustAdd(t, g, Node{ID: "a"})
	mustAdd(t, g, Node{ID: "b", Dependencies: []string{"a"}})
	mustAdd(t, g, Node{ID: "c", Dependencies: []string{"b"}})
	c := NewCursor(g)

	var wg sync.WaitGroup
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					_ = c.Active()
					_ = c.Pending()
					_ = c.Completed()
					_ = c.Blocked()
					_ = c.Failed()
					_ = c.Progress()
					_, _ = c.Status("a")
				}
			}
		}()
	}
	for _, id := range []string{"a", "b", "c"} {
		if _, err := c.Advance(); err != nil {
			t.Fatalf("Advance: %v", err)
		}
		if err := c.Complete(id); err != nil {
			t.Fatalf("Complete(%s): %v", id, err)
		}
	}
	close(done)
	wg.Wait()
}
