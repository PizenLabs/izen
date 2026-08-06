// Package event defines the runtime event model for the Izen Agent Runtime V3
// execution core: a minimal, dependency-free event type plus a thread-safe
// in-memory pub/sub bus.
//
// The package carries no AI/LLM concepts and performs no I/O; it only routes
// events between publishers and observers.
package event

import (
	"crypto/rand"
	"fmt"
	"time"
)

// EventType discriminates the kind of an Event.
type EventType string

// Core system event types emitted by the kernel and runtime layers.
const (
	TypeTaskStarted    EventType = "task.started"
	TypeTaskCompleted  EventType = "task.completed"
	TypeTaskFailed     EventType = "task.failed"
	TypeTaskCanceled   EventType = "task.canceled"
	TypeBudgetExceeded EventType = "budget.exceeded"
	TypeStateCheckpt   EventType = "state.checkpoint"
)

// Event is an immutable record of something that happened in the runtime.
// ID is unique per event; TaskID links the event to its originating task;
// Payload carries an optional, type-unspecified result.
type Event struct {
	ID        string
	Type      EventType
	TaskID    string
	Payload   any
	Timestamp time.Time
}

// Observer receives events synchronously on its subscription's dispatch
// goroutine. Observers must not block: a slow observer delays only its own
// subscription, never a publisher.
type Observer func(e Event)

// NewEvent builds an Event with a freshly generated unique ID and the current
// timestamp.
func NewEvent(typ EventType, taskID string, payload any) Event {
	return Event{
		ID:        newID(),
		Type:      typ,
		TaskID:    taskID,
		Payload:   payload,
		Timestamp: time.Now(),
	}
}

// newID returns a random UUIDv4-formatted identifier.
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("event-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
