package plan

import (
	"strings"
	"testing"
)

// fakeStatus is a test double for TaskStatusSource.
type fakeStatus struct {
	completed map[int]bool
}

func (f *fakeStatus) IsCompleted(taskID int) bool {
	return f.completed[taskID]
}

func sampleTasks() []Task {
	return []Task{
		{StepNum: 1, IsDone: false, Status: "idle", Type: "FILE_MUTATE", Target: "a.go", Description: "add handler"},
		{StepNum: 2, IsDone: false, Status: "idle", Type: "SHELL_EXEC", Target: "go build ./...", Description: "compile"},
	}
}

func TestRenderChecklistOpenState(t *testing.T) {
	out := RenderChecklist(sampleTasks(), nil)
	if !strings.Contains(out, "- [ ] FILE_MUTATE: a.go | add handler") {
		t.Fatalf("expected open checkbox line, got:\n%s", out)
	}
	if strings.Contains(out, "[✓]") {
		t.Fatal("did not expect completed marker for open tasks")
	}
	if strings.Contains(out, "~~") {
		t.Fatal("did not expect strike-through for open tasks")
	}
}

func TestRenderChecklistCompletedState(t *testing.T) {
	src := &fakeStatus{completed: map[int]bool{1: true}}
	out := RenderChecklist(sampleTasks(), src)

	lines := strings.Split(out, "\n")
	if !strings.Contains(lines[0], "- [✓]") {
		t.Fatalf("expected task 1 checked, got:\n%s", lines[0])
	}
	if !strings.Contains(lines[0], "~~") {
		t.Fatalf("expected strike-through on completed task, got:\n%s", lines[0])
	}
	// Open task keeps the unchecked state.
	if !strings.Contains(lines[1], "- [ ]") {
		t.Fatalf("expected task 2 still open, got:\n%s", lines[1])
	}
}

func TestParseMarkdownToTasksWithStatus(t *testing.T) {
	md := "- [ ] FILE_MUTATE: a.go | add handler\n- [ ] SHELL_EXEC: go build ./... | compile"
	src := &fakeStatus{completed: map[int]bool{2: true}}

	tasks := ParseMarkdownToTasksWithStatus(md, src)
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
	if tasks[0].IsDone || tasks[0].Status == "done" {
		t.Fatal("task 1 should remain open")
	}
	if !tasks[1].IsDone || tasks[1].Status != "done" {
		t.Fatal("task 2 should be marked completed")
	}
}

func TestParseMarkdownToTasksWithStatusNilSource(t *testing.T) {
	md := "- [ ] FILE_MUTATE: a.go | add handler"
	tasks := ParseMarkdownToTasksWithStatus(md, nil)
	if tasks[0].IsDone {
		t.Fatal("expected no completion with nil source")
	}
}
