package metrics

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFormatLine(t *testing.T) {
	line := FormatLine(Metric{
		RunID: "run-1", Phase: "execute", Status: StatusOK,
		Latency: 1500 * time.Microsecond, Tokens: 42, Strategy: "patch",
	})
	if !strings.Contains(line, "run=run-1") || !strings.Contains(line, "phase=execute") ||
		!strings.Contains(line, "status=ok") || !strings.Contains(line, "tokens=42") ||
		!strings.Contains(line, "strategy=patch") {
		t.Errorf("unexpected line: %q", line)
	}
}

func TestCollectorStdout(t *testing.T) {
	var buf bytes.Buffer
	c := NewCollector()
	c.Sink(StdoutSinkWriter(&buf))
	c.Sink(nil) // nil sinks are ignored
	c.Emit(Metric{RunID: "r", Phase: "analyze", Status: StatusOK, Tokens: 7})
	c.Emit(Metric{RunID: "r", Phase: "execute", Status: StatusFailed, Err: "boom"})
	out := buf.String()
	if strings.Count(out, "metric run=") != 2 {
		t.Errorf("expected 2 metric lines, got %q", out)
	}
	if !strings.Contains(out, "error=boom") {
		t.Errorf("failed metric should carry error: %q", out)
	}
}

func TestCollectorStampsTimestamp(t *testing.T) {
	fixed := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	c := NewCollector(WithClock(func() time.Time { return fixed }))
	var got Metric
	sink := sinkFunc(func(m Metric) error {
		got = m
		return nil
	})
	c.Sink(sink)
	c.Emit(Metric{RunID: "r", Phase: "policy"})
	if !got.Timestamp.Equal(fixed) {
		t.Errorf("timestamp = %v, want %v", got.Timestamp, fixed)
	}
}

func TestFileSinkJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.jsonl")
	s, err := NewFileSink(path)
	if err != nil {
		t.Fatal(err)
	}
	m := Metric{RunID: "run-9", Phase: "validate", Status: StatusOK, Tokens: 3, Timestamp: time.Now()}
	if err := s.Emit(m); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		RunID     string `json:"run_id"`
		Phase     string `json:"phase"`
		LatencyNS int64  `json:"latency_ns"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.RunID != "run-9" || decoded.Phase != "validate" || decoded.LatencyNS != 0 {
		t.Errorf("decoded = %+v", decoded)
	}
}

func TestJSONLine(t *testing.T) {
	m := Metric{RunID: "r", Phase: "plan", Status: StatusSkipped, Timestamp: time.Now()}
	data, err := JSONLine(m)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) {
		t.Error("JSONLine should produce valid JSON")
	}
}

// sinkFunc adapts a function into a Sink.
type sinkFunc func(Metric) error

func (f sinkFunc) Emit(m Metric) error { return f(m) }
