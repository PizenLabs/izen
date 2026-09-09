// Package audit event substrate (Phase 3).
//
// This file adds the append-only structured event trail
// (.izen/audit/events.ndjson) used by the Agent Execution Engine. Every
// event is one JSON object per line; writes are serialized by the Logger
// mutex in audit.go so concurrent goroutines never interleave lines.
package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/PizenLabs/izen/internal/state"
)

// Phase 3 audit event types.
const (
	EventRoleDispatched    = "role_dispatched"
	EventLLMRequest        = "llm_request"
	EventPatchStaged       = "patch_staged"
	EventPatchApplied      = "patch_applied"
	EventCheckpointCreated = "checkpoint_created"
)

// EventsFile is the ndjson event trail filename under .izen/audit/.
const EventsFile = "events.ndjson"

// Event is one structured audit line in events.ndjson.
type Event struct {
	Timestamp string `json:"timestamp"`
	SessionID string `json:"session_id"`
	EventType string `json:"event_type"`
	Payload   any    `json:"payload,omitempty"`
}

// LogEvent appends a structured event to .izen/audit/events.ndjson.
// It is safe for concurrent use.
func (l *Logger) LogEvent(sessionID, eventType string, payload any) error {
	if l == nil {
		return fmt.Errorf("audit: nil logger")
	}
	evt := Event{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		SessionID: sessionID,
		EventType: eventType,
		Payload:   payload,
	}
	data, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("audit: marshal event: %w", err)
	}
	return l.append(state.LocalPath(l.root, state.AuditDir, EventsFile), data)
}

// ReadEvents parses every line of .izen/audit/events.ndjson, skipping blank
// lines. A malformed line yields an error so interleave regressions are
// caught loudly by tests.
func (l *Logger) ReadEvents() ([]Event, error) {
	data, err := os.ReadFile(state.LocalPath(l.root, state.AuditDir, EventsFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Event
	for _, line := range splitLines(string(data)) {
		if line == "" {
			continue
		}
		var evt Event
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			return nil, fmt.Errorf("audit: decode event line: %w", err)
		}
		out = append(out, evt)
	}
	return out, nil
}
