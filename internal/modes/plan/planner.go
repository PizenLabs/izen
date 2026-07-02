package plan

import "strings"

// Task represents a single tactical operation in the markdown-based task system.
// Structure: - [ ] TYPE: Target | Description
// Where TYPE is: "FILE_MUTATE", "SHELL_EXEC", "GIT_ACTION"
type Task struct {
	StepNum     int    `json:"step_num"`
	IsDone      bool   `json:"is_done"`
	Status      string `json:"status"`      // "idle", "processing", "done"
	Type        string `json:"type"`        // "FILE_MUTATE", "SHELL_EXEC", "GIT_ACTION"
	Target      string `json:"target"`      // File path or exact CLI command
	Description string `json:"description"` // Explanation of why this step exists
}

// ParseMarkdownToTasks converts markdown content into structured Task objects.
// It finds lines starting with - [ ] or - [x] and parses them into structured Task objects.
// It accepts the syntax: - [ ] TYPE: Target | Description
func ParseMarkdownToTasks(mdContent string) []Task {
	var tasks []Task
	lines := strings.Split(mdContent, "\n")
	taskCount := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- [ ]") || strings.HasPrefix(line, "- [x]") {
			taskCount++
			prefix := "- [x]"
			if strings.HasPrefix(line, "- [ ]") {
				prefix = "- [ ]"
			}
			content := strings.TrimPrefix(line, prefix)
			content = strings.TrimSpace(content)

			// Split by first colon to get Type
			parts := strings.SplitN(content, ":", 2)
			if len(parts) < 2 {
				continue
			}
			typeStr := strings.TrimSpace(parts[0])
			rest := strings.TrimSpace(parts[1])

			// Split rest by vertical pipe to get Target and Description
			if !strings.Contains(rest, "|") {
				continue
			}
			targetParts := strings.SplitN(rest, "|", 2)
			target := strings.TrimSpace(targetParts[0])
			desc := strings.TrimSpace(targetParts[1])

			isDone := false
			status := "idle"
			if strings.HasPrefix(line, "- [x]") {
				isDone = true
				status = "done"
			}
			task := Task{
				StepNum:     taskCount,
				IsDone:      isDone,
				Status:      status,
				Type:        typeStr,
				Target:      target,
				Description: desc,
			}
			tasks = append(tasks, task)
		}
	}
	return tasks
}
