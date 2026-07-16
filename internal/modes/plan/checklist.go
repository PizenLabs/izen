package plan

import (
	"fmt"
	"strings"
)

// TaskStatusSource reads task completion state without importing the context
// package, which avoids an import cycle (context → plan). The context ledger
// satisfies this interface structurally via its IsCompleted method.
type TaskStatusSource interface {
	IsCompleted(taskID int) bool
}

// RenderChecklist renders the task list as a Markdown checklist, consulting the
// supplied status source. Tasks flagged Completed are rendered with a checked
// state [✓] and strike-through text instead of the open [ ] state.
func RenderChecklist(tasks []Task, src TaskStatusSource) string {
	var b strings.Builder
	for _, t := range tasks {
		done := src != nil && src.IsCompleted(t.StepNum)
		if done {
			fmt.Fprintf(&b, "- [✓] ~~%s: %s | %s~~\n", t.Type, t.Target, t.Description)
		} else {
			fmt.Fprintf(&b, "- [ ] %s: %s | %s\n", t.Type, t.Target, t.Description)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// ParseMarkdownToTasksWithStatus parses the markdown checklist and merges
// completion state from the supplied task status source, marking tasks as
// Completed in both IsDone and Status so the checklist renders the [✓] state.
func ParseMarkdownToTasksWithStatus(mdContent string, src TaskStatusSource) []Task {
	tasks := ParseMarkdownToTasks(mdContent)
	if src == nil {
		return tasks
	}
	for i := range tasks {
		if src.IsCompleted(tasks[i].StepNum) {
			tasks[i].IsDone = true
			tasks[i].Status = "done"
		}
	}
	return tasks
}
