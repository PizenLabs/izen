package durable

import "strings"

// ── AUDIT-LINEAGE READ-BACK ─────────────────────────────────────────────────
//
// RecordCustomEvent appends events that carry no materialized state
// transition: Phase 4 guard/gateway lineage, human decisions, proposal
// snapshots. They are durable in ledger.ndjson, but until now they were
// WRITE-ONLY — replay folded them as a currentID touch and dropped the rest,
// so a journal a fresh process could append to was a journal it could never
// read back from. That made an automatic cross-process restore of this
// lineage impossible rather than merely absent.
//
// This file closes that gap for the audit types, and ONLY for them:
//
//   - the retained window is a CACHE over the journal, never a second source
//     of truth. ledger.ndjson stays the single authority and is never
//     rewritten by retention;
//   - retention is bounded, and the bound is REPORTED
//     (CustomEventsTruncated) rather than hidden, because a caller that
//     concludes "the human never decided" over a dropped window would be
//     inventing an absence;
//   - a corrupted line is skipped exactly like every other replay path: the
//     runtime recovers to the last coherent prefix instead of trusting a
//     payload it cannot decode.

// CustomEvent is one retained audit-lineage event, in journal order.
type CustomEvent struct {
	TaskID    string         `json:"taskId"`
	EventType EventType      `json:"eventType"`
	Sequence  uint64         `json:"sequence"`
	Timestamp string         `json:"timestamp"`
	Payload   map[string]any `json:"payload"`
}

// maxRetainedCustomEvents bounds how many audit-lineage events are retained
// per task. The window keeps the journal's TAIL, which is what a resume needs;
// when it is full the oldest entry is dropped and the drop is reported.
const maxRetainedCustomEvents = 512

// IsCustomEventType reports whether the event type belongs to the audit-lineage
// class appended through RecordCustomEvent. Those events carry no materialized
// state transition, so replay folds them as a currentID touch only; this
// predicate is what additionally marks them for read-back retention.
func IsCustomEventType(eventType EventType) bool {
	switch eventType {
	case EventScopeViolationRejected, EventStructuralAmbiguity,
		EventProposalAuthorized, EventWorkspaceSwitched, EventPhaseTransition,
		EventHumanDecision, EventProposalSnapshot:
		return true
	default:
		return false
	}
}

// retainCustom appends one audit-lineage event to its task's bounded window.
// It is called from apply, so the live append path and replay fold retain the
// same way.
func (s *TaskStore) retainCustom(ev LedgerEvent) {
	if !IsCustomEventType(ev.EventType) {
		return
	}
	if s.custom == nil {
		s.custom = make(map[string][]CustomEvent)
	}
	list := s.custom[ev.TaskID]
	if len(list) >= maxRetainedCustomEvents {
		list = list[1:]
		s.customTruncated[ev.TaskID] = true
	}
	s.custom[ev.TaskID] = append(list, CustomEvent{
		TaskID:    ev.TaskID,
		EventType: ev.EventType,
		Sequence:  ev.Sequence,
		Timestamp: ev.Timestamp,
		Payload:   ev.Payload,
	})
}

// CustomEvents returns the retained audit-lineage events for taskID, oldest
// first, filtered to types when any are given. It is READ-ONLY: it appends no
// event and transitions no task.
//
// A caller that needs the full lineage of a task longer than the retention
// window must check CustomEventsTruncated first; a clean result there means
// "the journal's audit tail is complete", not "the journal is complete".
func (s *TaskStore) CustomEvents(taskID string, types ...EventType) []CustomEvent {
	if s == nil || strings.TrimSpace(taskID) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	want := make(map[EventType]bool, len(types))
	for _, t := range types {
		want[t] = true
	}
	list := s.custom[taskID]
	out := make([]CustomEvent, 0, len(list))
	for _, ev := range list {
		if len(want) > 0 && !want[ev.EventType] {
			continue
		}
		out = append(out, ev)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// CustomEventsTruncated reports whether retention has already dropped audit
// lineage for the task. A negative conclusion drawn over a truncated window is
// an inference from silence, so the boundary is explicit.
func (s *TaskStore) CustomEventsTruncated(taskID string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.customTruncated[taskID]
}
