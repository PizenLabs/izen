package plan

import (
	"context"
	"strings"

	"github.com/PizenLabs/izen/internal/runtime/substrate"
)

// PlanStore manages plan data, providing markdown storage and task progression operations.
// It writes raw LLM outputs to .izen/plans/plan-<id>.md and .izen/plans/current.md.
// PlanStore provides SaveRawMarkdown for persisting raw output and TickTaskHoanThanh for marking tasks as completed.
type PlanStore struct {
}

// NewPlanStore creates a new PlanStore instance.
func NewPlanStore() *PlanStore {
	return &PlanStore{}
}

// SaveRawMarkdown compiles the raw LLM output into a Proposal and executes
// via Substrate. The semantic layer never performs direct file writes.
func (s *PlanStore) SaveRawMarkdown(id string, content string) error {
	if s == nil {
		return nil
	}
	sub := substrate.NewConcreteSubstrate(".")
	prop := substrate.Proposal{
		ID:     "plan-" + id,
		Intent: "save plan markdown",
		Operations: []substrate.Operation{
			{Type: substrate.OpFileWrite, Target: ".izen/plans/plan-" + id + ".md", Content: []byte(content)},
			{Type: substrate.OpFileWrite, Target: ".izen/plans/current.md", Content: []byte(content)},
		},
	}
	_, err := sub.Execute(context.Background(), prop)
	return err
}

// TickTaskHoanThanh reads current plan via ReadScope, finds the N-th task,
// and compiles a Proposal to update it. No direct file writes.
func (s *PlanStore) TickTaskHoanThanh(stepNum int) error {
	if stepNum <= 0 {
		return nil
	}
	currentPath := ".izen/plans/current.md"
	rs := substrate.NewFSReadScope(".")
	content, err := rs.ReadFile(currentPath)
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
	sub := substrate.NewConcreteSubstrate(".")
	prop := substrate.Proposal{ID: "tick-task", Intent: "tick task", Operations: []substrate.Operation{{Type: substrate.OpFileWrite, Target: currentPath, Content: []byte(updatedContent)}}}
	_, err = sub.Execute(context.Background(), prop)
	return err
}
