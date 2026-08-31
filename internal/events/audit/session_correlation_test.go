package audit

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/events"
)

// INV-SESSION-10: every record persisted to .izen/audit/events.ndjson must be
// correlated with the active session_id. The AuditLogger stamps the session
// resolved by the wired resolver on EVERY record — typed domain events AND
// envelopes — so mutation traces, token usage and tool invocations map strictly
// to their originating session.
func TestAuditLoggerStampsSessionIDOnEveryRecord(t *testing.T) {
	dir := t.TempDir()
	bus := events.NewBus(64)
	defer bus.Close()

	l, err := NewLogger(dir, bus)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	l.SetSessionResolver(func() string { return "sess-8f31a2" })
	if err := l.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Mix of typed domain events (execution lifecycle + engine signals) and
	// envelopes must ALL carry the originating session.
	bus.Publish(events.NewExecutionStarted("req-1", "build", "fix index.html", ""))
	bus.Publish(events.NewExecutionEvidence(events.ExecutionEvidencePayload{
		RequestID:  "req-1",
		SessionID:  "sess-8f31a2",
		ContractID: "c-1",
		Outcome:    "committed",
		StartedAt:  time.Now(),
		FinishedAt: time.Now(),
	}))
	bus.Publish(events.NewActivity("search: 3 results"))
	bus.PublishEnvelope(events.NewEnvelope(events.DomainKindTelemetry, "telemetry.pipeline", "raw"))

	waitAccepted(t, l, 4)
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := readNDJSON(t, filepath.Join(dir, DefaultFileName))
	if len(lines) != 4 {
		t.Fatalf("expected 4 persisted records, got %d", len(lines))
	}
	for i, line := range lines {
		var env events.Envelope
		if err := json.Unmarshal(line, &env); err != nil {
			t.Fatalf("line %d is not a valid envelope: %v", i, err)
		}
		if env.SessionID != "sess-8f31a2" {
			t.Errorf("line %d (source=%s) session_id = %q, want sess-8f31a2", i, env.Source, env.SessionID)
		}
		// The typed execution.started record must preserve its canonical type
		// as the source discriminator.
		if i == 0 && env.Source != events.EventExecutionStarted {
			t.Errorf("line 0 source = %q, want %q (typed event preserved)", env.Source, events.EventExecutionStarted)
		}
	}
}

// A nil session resolver must leave session_id empty (harness mode) — the
// logger degrades gracefully, never fails.
func TestAuditLoggerWithoutSessionResolverLeavesEmptySessionID(t *testing.T) {
	dir := t.TempDir()
	bus := events.NewBus(16)
	defer bus.Close()

	l, err := NewLogger(dir, bus)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	if err := l.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	bus.Publish(events.NewExecutionStarted("req-1", "build", "x", ""))
	waitAccepted(t, l, 1)
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := readNDJSON(t, filepath.Join(dir, DefaultFileName))
	if len(lines) != 1 {
		t.Fatalf("expected 1 record, got %d", len(lines))
	}
	var env events.Envelope
	if err := json.Unmarshal(lines[0], &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.SessionID != "" {
		t.Fatalf("session_id = %q, want empty without a resolver", env.SessionID)
	}
}

// The resolver is consulted per event at handling time, so a session switch
// between events correlates each record to the session active when it crossed
// the bus.
func TestAuditLoggerSessionResolverTracksActiveSession(t *testing.T) {
	dir := t.TempDir()
	bus := events.NewBus(32)
	defer bus.Close()

	current := "sess-A"
	l, err := NewLogger(dir, bus)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	l.SetSessionResolver(func() string { return current })
	if err := l.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	bus.Publish(events.NewExecutionStarted("req-1", "build", "a", ""))
	waitAccepted(t, l, 1)
	current = "sess-B"
	bus.Publish(events.NewExecutionStarted("req-2", "build", "b", ""))
	waitAccepted(t, l, 2)
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := readNDJSON(t, filepath.Join(dir, DefaultFileName))
	if len(lines) != 2 {
		t.Fatalf("expected 2 records, got %d", len(lines))
	}
	for i, want := range []string{"sess-A", "sess-B"} {
		var env events.Envelope
		if err := json.Unmarshal(lines[i], &env); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		if env.SessionID != want {
			t.Errorf("line %d session_id = %q, want %q", i, env.SessionID, want)
		}
	}
}
