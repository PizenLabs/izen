package app

import (
	"fmt"

	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/ir"
	"github.com/PizenLabs/izen/internal/kernel"
)

// StatusLine renders one runtime event as a compact human-readable status
// line. It is the observer contract a terminal TUI (or the CLI's stderr
// renderer) uses to subscribe to the pipeline bus and display live
// task.started / task.completed / task.failed updates.
func StatusLine(ev events.DomainEvent) string {
	switch ev.Type() {
	case events.EventTaskStarted:
		return fmt.Sprintf("task started  %s", taskIDOf(ev))
	case events.EventTaskCompleted:
		return fmt.Sprintf("task done     %s", taskIDOf(ev))
	case events.EventTaskFailed:
		msg := ""
		if p, ok := ev.Payload().(events.TaskFailedPayload); ok {
			if r, ok := p.Result.(kernel.TaskResult); ok && r.Error != nil {
				msg = ": " + r.Error.Error()
			}
		}
		return fmt.Sprintf("task failed   %s%s", taskIDOf(ev), msg)
	case events.EventTaskCanceled:
		return fmt.Sprintf("task canceled %s", taskIDOf(ev))
	case events.EventStateCheckpoint:
		if p, ok := ev.Payload().(events.StateCheckpointPayload); ok {
			if s, ok := p.Checkpoint.(StageEvent); ok {
				return fmt.Sprintf("pipeline      stage %s", s.Stage)
			}
		}
		return "pipeline      checkpoint"
	case events.EventClarificationRequired:
		if p, ok := ev.Payload().(events.ClarificationRequiredPayload); ok {
			if qs, ok := p.Questions.([]ir.ClarificationQuestion); ok {
				return fmt.Sprintf("pipeline      clarification required: %d question(s)", len(qs))
			}
		}
		return "pipeline      clarification required"
	default:
		return fmt.Sprintf("event         %s (%s)", ev.Type(), taskIDOf(ev))
	}
}

// taskIDOf extracts the task identity from a task lifecycle event payload.
func taskIDOf(ev events.DomainEvent) string {
	switch p := ev.Payload().(type) {
	case events.TaskStartedPayload:
		return p.TaskID
	case events.TaskCompletedPayload:
		return p.TaskID
	case events.TaskFailedPayload:
		return p.TaskID
	case events.TaskCanceledPayload:
		return p.TaskID
	case events.BudgetExceededPayload:
		return p.TaskID
	case events.StateCheckpointPayload:
		return p.TaskID
	case events.ClarificationRequiredPayload:
		return p.TaskID
	default:
		return ""
	}
}
