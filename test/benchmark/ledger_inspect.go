package benchmark

// ── durable ledger inspection (CLI-boundary evidence) ──────────────────────
//
// The subprocess assertions read the ledger the real binary wrote to disk, for
// the same reason the in-process tests do: a transition a run claimed but never
// recorded must fail here rather than being trusted from a log line.

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// ledgerRecord is the one shape the reader needs. It is deliberately a subset:
// the audit is about presence and ordering of the event, not its full payload.
type ledgerRecord struct {
	EventType string         `json:"eventType"`
	TaskID    string         `json:"taskId"`
	Payload   map[string]any `json:"payload"`
}

// readLedgerEvents decodes the on-disk journal. An unreadable or torn line is
// reported, never silently skipped.
func readLedgerEvents(t *testing.T, path string) []ledgerRecord {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open ledger %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	var out []ledgerRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec ledgerRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode ledger line in %s: %v", path, err)
		}
		out = append(out, rec)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan ledger %s: %v", path, err)
	}
	return out
}

// hasLedgerEvent reports whether the journal contains an event of the type.
func hasLedgerEvent(events []ledgerRecord, typ string) bool {
	for _, ev := range events {
		if ev.EventType == typ {
			return true
		}
	}
	return false
}

// ledgerTypes lists the journal's event types in order, for a failure message
// that says what was recorded rather than only what was missing.
func ledgerTypes(events []ledgerRecord) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.EventType)
	}
	return out
}
