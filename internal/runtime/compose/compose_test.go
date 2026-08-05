package compose

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/domain/signal"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/events/audit"
)

func TestWireAuditDirCreatesLogger(t *testing.T) {
	root := t.TempDir()
	app, err := Wire(WithAuditDir(filepath.Join(root, ".izen", "audit")))
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}

	if app.Audit == nil {
		app.Close()
		t.Fatal("expected an AuditLogger to be wired")
	}
	if app.Audit.Path() != filepath.Join(root, ".izen", "audit", "events.ndjson") {
		app.Close()
		t.Fatalf("Audit path = %q", app.Audit.Path())
	}

	// Publish an envelope on the shared bus and wait for the audit logger to
	// accept it, then close to flush.
	auditPath := app.Audit.Path()
	app.Bus.PublishEnvelope(events.NewSignalEnvelope(
		signal.New(signal.SignalBuildHalted, "test", nil), "test"))
	waitAuditAccepted(t, app.Audit, 1)
	app.Close()

	f, err := os.Open(auditPath)
	if err != nil {
		t.Fatalf("open audit file: %v", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	lines := 0
	var last []byte
	for sc.Scan() {
		if sc.Text() != "" {
			lines++
			last = append([]byte(nil), sc.Bytes()...)
		}
	}
	if lines != 1 {
		t.Fatalf("expected 1 audit line, got %d", lines)
	}
	var back events.Envelope
	if err := json.Unmarshal(last, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Kind != events.DomainKindSignal {
		t.Fatalf("unexpected envelope kind %q", back.Kind)
	}
}

func TestWireNoAuditDir(t *testing.T) {
	app, err := Wire()
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}
	defer app.Close()
	if app.Audit != nil {
		t.Fatal("expected no AuditLogger when no audit dir is wired")
	}
}

// waitAuditAccepted polls the wired audit logger until it has accepted want
// envelopes, timing out after 3 seconds.
func waitAuditAccepted(t *testing.T, l *audit.AuditLogger, want uint64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for l.Accepted() < want {
		if time.Now().After(deadline) {
			t.Fatalf("audit logger accepted %d envelopes, want %d", l.Accepted(), want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
