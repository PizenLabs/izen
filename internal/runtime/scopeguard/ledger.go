package scopeguard

import (
	"github.com/PizenLabs/izen/internal/runtime/durable"
)

// StoreLedger adapts a *durable.TaskStore to the ScopeLedger/Ledger
// seam so guard, structural and gateway tiers record audit lineage in
// ledger.ndjson. A nil store disables lineage (all methods no-op nil).
type StoreLedger struct {
	Store *durable.TaskStore
}

// RecordCustomEvent implements ScopeLedger and Ledger.
func (l StoreLedger) RecordCustomEvent(taskID string, eventType string, payload map[string]any) error {
	if l.Store == nil {
		return nil
	}
	return l.Store.RecordCustomEvent(taskID, durable.EventType(eventType), payload)
}

// MemoryLedger is an in-memory ScopeLedger for tests: it records events
// without touching the filesystem and counts tool-dispatch attempts.
type MemoryLedger struct {
	Events []MemoryEvent
}

// MemoryEvent is one recorded audit event.
type MemoryEvent struct {
	TaskID  string
	Type    string
	Payload map[string]any
}

// RecordCustomEvent implements ScopeLedger and Ledger.
func (l *MemoryLedger) RecordCustomEvent(taskID string, eventType string, payload map[string]any) error {
	l.Events = append(l.Events, MemoryEvent{TaskID: taskID, Type: eventType, Payload: payload})
	return nil
}

// Count returns the number of events with the given type.
func (l *MemoryLedger) Count(typ string) int {
	n := 0
	for _, e := range l.Events {
		if e.Type == typ {
			n++
		}
	}
	return n
}
