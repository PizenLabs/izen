package audit

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/domain/signal"
	"github.com/PizenLabs/izen/internal/events"
)

func TestStoreWritesNDJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.ndjson")
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = s.Close() }()

	envs := []events.Envelope{
		events.NewEnvelope(events.DomainKindTelemetry, "telemetry.pipeline", "raw-1"),
		events.NewSignalEnvelope(signal.New(signal.SignalDepMissing, "investigate", map[string]string{"dependency": "github.com/foo/bar"}), "investigate"),
	}
	for _, env := range envs {
		if err := s.Write(env); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if err := s.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	lines := readNDJSON(t, path)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	for i, line := range lines {
		var back events.Envelope
		if err := json.Unmarshal(line, &back); err != nil {
			t.Fatalf("line %d is not valid JSON: %v", i, err)
		}
		if back.ID != envs[i].ID {
			t.Fatalf("line %d ID = %q, want %q", i, back.ID, envs[i].ID)
		}
		if back.Source != envs[i].Source {
			t.Fatalf("line %d Source = %q", i, back.Source)
		}
	}
}

func TestStoreCreatesParentDir(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "deep", "nested", "events.ndjson")
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if got := s.Path(); got != path {
		t.Fatalf("Path = %q, want %q", got, path)
	}
	if err := s.Write(events.NewEnvelope(events.DomainKindSystem, "sys", "x")); err != nil {
		t.Fatalf("Write after mkdir: %v", err)
	}
	_ = s.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created: %v", err)
	}
}

func TestStoreAppendsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.ndjson")
	s1, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := s1.Write(events.NewEnvelope(events.DomainKindTelemetry, "a", "1")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A second store instance must append, never truncate.
	s2, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore #2: %v", err)
	}
	if err := s2.Write(events.NewEnvelope(events.DomainKindTelemetry, "b", "2")); err != nil {
		t.Fatalf("Write #2: %v", err)
	}
	if err := s2.Close(); err != nil {
		t.Fatalf("Close #2: %v", err)
	}

	if lines := readNDJSON(t, path); len(lines) != 2 {
		t.Fatalf("expected 2 appended lines, got %d", len(lines))
	}
}

func TestStoreWriteAfterClose(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "events.ndjson"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Write(events.NewEnvelope(events.DomainKindTelemetry, "a", "x")); err == nil {
		t.Fatal("expected write after close to fail")
	}
}

func TestAuditLoggerPersistsEnvelopes(t *testing.T) {
	dir := t.TempDir()
	bus := events.NewBus(64)
	defer bus.Close()

	l, err := NewLogger(dir, bus)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	if err := l.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Typed domain events must be filtered out; envelopes must be persisted.
	bus.Publish(events.NewCommandReceived("/plan", "plan"))
	bus.PublishEnvelope(events.NewEnvelope(events.DomainKindTelemetry, "telemetry.pipeline", "raw"))
	bus.PublishEnvelope(events.NewSignalEnvelope(
		signal.New(signal.SignalBuildHalted, "investigate", nil), "investigate"))

	// Delivery is asynchronous through the bus; wait until both envelopes are
	// accepted before teardown so the assertion is deterministic.
	waitAccepted(t, l, 2)
	if l.Dropped() != 0 {
		t.Fatalf("Dropped = %d, want 0", l.Dropped())
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := readNDJSON(t, filepath.Join(dir, DefaultFileName))
	if len(lines) != 2 {
		t.Fatalf("expected 2 envelope lines (typed event filtered), got %d", len(lines))
	}
	var back events.Envelope
	if err := json.Unmarshal(lines[0], &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Kind != events.DomainKindTelemetry {
		t.Fatalf("first line kind = %q", back.Kind)
	}
	if err := json.Unmarshal(lines[1], &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Kind != events.DomainKindSignal {
		t.Fatalf("second line kind = %q", back.Kind)
	}
	if l.Dropped() != 0 {
		t.Fatalf("Dropped = %d, want 0", l.Dropped())
	}
}

func TestAuditLoggerNonBlockingUnderSlowWorker(t *testing.T) {
	dir := t.TempDir()
	bus := events.NewBus(4)
	defer bus.Close()

	l, err := NewLogger(dir, bus)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	// Tiny channel to force drops under the rapid burst.
	l.ch = make(chan events.Envelope, 2)
	if err := l.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = l.Close() }()

	// A typed subscriber proves publishers are never blocked by the audit path.
	received := make(chan events.DomainEvent, 1)
	sub := bus.Subscribe(events.EventCommandReceived, func(ev events.DomainEvent) { received <- ev })
	defer sub.Cancel()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			bus.PublishEnvelope(events.NewEnvelope(events.DomainKindTelemetry, "telemetry.pipeline", i))
			bus.Publish(events.NewCommandReceived("/x", "plan"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("publisher stalled behind the audit logger")
	}
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("typed subscriber starved while audit logger was under load")
	}
}

func TestAuditLoggerRestartAndDropped(t *testing.T) {
	dir := t.TempDir()
	bus := events.NewBus(8)
	defer bus.Close()

	l, err := NewLogger(dir, bus)
	if err != nil {
		t.Fatalf("NewLogger: %v", err)
	}
	if err := l.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	bus.PublishEnvelope(events.NewEnvelope(events.DomainKindTelemetry, "a", "1"))
	waitAccepted(t, l, 1)
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Re-open the same directory; the file must have been appended, not
	// truncated.
	l2, err := NewLogger(dir, bus)
	if err != nil {
		t.Fatalf("NewLogger #2: %v", err)
	}
	if err := l2.Start(); err != nil {
		t.Fatalf("Start #2: %v", err)
	}
	bus.PublishEnvelope(events.NewEnvelope(events.DomainKindTelemetry, "b", "2"))
	waitAccepted(t, l2, 1)
	if err := l2.Close(); err != nil {
		t.Fatalf("Close #2: %v", err)
	}

	if lines := readNDJSON(t, filepath.Join(dir, DefaultFileName)); len(lines) != 2 {
		t.Fatalf("expected 2 appended lines across restarts, got %d", len(lines))
	}
}

// waitAccepted polls the audit logger until it has accepted want envelopes
// (pushed onto the internal channel), timing out after 3 seconds.
func waitAccepted(t *testing.T, l *AuditLogger, want uint64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for l.Accepted() < want {
		if time.Now().After(deadline) {
			t.Fatalf("audit logger accepted %d envelopes, want %d", l.Accepted(), want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func readNDJSON(t *testing.T, path string) [][]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	var out [][]byte
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			out = append(out, []byte(line))
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return out
}
