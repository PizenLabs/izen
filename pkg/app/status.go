package app

import (
	"fmt"

	"github.com/PizenLabs/izen/pkg/event"
	"github.com/PizenLabs/izen/pkg/ir"
	"github.com/PizenLabs/izen/pkg/kernel"
)

// StatusLine renders one runtime event as a compact human-readable status
// line. It is the observer contract a terminal TUI (or the CLI's stderr
// renderer) uses to subscribe to the pipeline bus and display live
// TypeTaskStarted / TypeTaskCompleted / TypeTaskFailed updates.
func StatusLine(e event.Event) string {
	switch e.Type {
	case event.TypeTaskStarted:
		return fmt.Sprintf("task started  %s", e.TaskID)
	case event.TypeTaskCompleted:
		return fmt.Sprintf("task done     %s", e.TaskID)
	case event.TypeTaskFailed:
		msg := ""
		if r, ok := e.Payload.(kernel.TaskResult); ok && r.Error != nil {
			msg = ": " + r.Error.Error()
		}
		return fmt.Sprintf("task failed   %s%s", e.TaskID, msg)
	case event.TypeTaskCanceled:
		return fmt.Sprintf("task canceled %s", e.TaskID)
	case event.TypeStateCheckpt:
		if s, ok := e.Payload.(StageEvent); ok {
			return fmt.Sprintf("pipeline      stage %s", s.Stage)
		}
		return "pipeline      checkpoint"
	case event.TypeClarificationRequired:
		if qs, ok := e.Payload.([]ir.ClarificationQuestion); ok {
			return fmt.Sprintf("pipeline      clarification required: %d question(s)", len(qs))
		}
		return "pipeline      clarification required"
	default:
		return fmt.Sprintf("event         %s (%s)", e.Type, e.TaskID)
	}
}
