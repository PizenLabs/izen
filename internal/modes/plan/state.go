package plan

import (
	"os"
	"strings"
)

// PlanStore manages plan data, providing markdown storage and task progression operations.
// It writes raw LLM outputs to .izen/plans/plan-<id>.md and .izen/plans/current.md.
// PlanStore provides SaveRawMarkdown for persisting raw output and TickTaskHoanThanh for marking tasks as completed.
type PlanStore struct {
	path string
}

// NewPlanStore creates a new PlanStore instance.
func NewPlanStore() *PlanStore {
	return &PlanStore{}
}

// SaveRawMarkdown writes the raw LLM output directly to .izen/plans/plan-<id>.md
// and overrides .izen/plans/current.md.
func (s *PlanStore) SaveRawMarkdown(id string, content string) error {
	if s == nil {
		return nil
	}

	planDir := ".izen/plans"
	if err := os.MkdirAll(planDir, 0755); err != nil {
		return err
	}

	// Save to plan-<id>.md
	targetPath := planDir + "/plan-" + id + ".md"
	if err := os.WriteFile(targetPath, []byte(content), 0644); err != nil {
		return err
	}

	// Override current.md
	if err := os.WriteFile(planDir+"/current.md", []byte(content), 0644); err != nil {
		return err
	}

	return nil
}

// TickTaskHoanThanh reads .izen/plans/current.md line by line, finds the exact N-th task item,
// replaces - [ ] with - [x], and flushes the update back to the file without destroying
// other prose/headers.
func (s *PlanStore) TickTaskHoanThanh(stepNum int) error {
	if stepNum <= 0 {
		return nil
	}

	currentPath := ".izen/plans/current.md"
	content, err := os.ReadFile(currentPath)
	if err != nil {
		return err
	}

	lines := strings.Split(string(content), "\n")
	taskCount := 0
	modified := false

	for i, line := range lines {
		if strings.HasPrefix(line, "- [ ]") || strings.HasPrefix(line, "- [x]") {
			taskCount++
			if taskCount == stepNum {
				// Replace - [ ] with - [x]
				if strings.HasPrefix(line, "- [ ]") {
					lines[i] = "- [x]" + strings.TrimPrefix(line, "- [ ]")
					modified = true
				}
			}
		}
	}

	if !modified {
		return nil
	}

	updatedContent := strings.Join(lines, "\n")
	return os.WriteFile(currentPath, []byte(updatedContent), 0644)
}
