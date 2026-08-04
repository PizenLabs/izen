package metrics

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// FormatLine renders a metric as one human-readable line, e.g.
//
//	metric run=abc phase=analyze status=ok latency=1.2ms tokens=123 strategy=patch
func FormatLine(m Metric) string {
	latency := m.Latency.String()
	if m.Latency == 0 {
		latency = "-"
	}
	line := fmt.Sprintf(
		"metric run=%s phase=%s status=%s latency=%s tokens=%d strategy=%s",
		m.RunID, m.Phase, m.Status, latency, m.Tokens, m.Strategy,
	)
	if m.Err != "" {
		line += " error=" + m.Err
	}
	return line
}

// jsonMetric is the serializable projection of a Metric.
type jsonMetric struct {
	RunID     string    `json:"run_id"`
	Phase     string    `json:"phase"`
	Status    Status    `json:"status"`
	LatencyNS int64     `json:"latency_ns"`
	Tokens    int       `json:"tokens"`
	Strategy  string    `json:"strategy,omitempty"`
	Error     string    `json:"error,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// JSONLine marshals a metric into a JSON object suitable for a JSONL file.
func JSONLine(m Metric) ([]byte, error) {
	return json.Marshal(jsonMetric{
		RunID:     m.RunID,
		Phase:     m.Phase,
		Status:    m.Status,
		LatencyNS: int64(m.Latency),
		Tokens:    m.Tokens,
		Strategy:  m.Strategy,
		Error:     m.Err,
		Timestamp: m.Timestamp,
	})
}

// StdoutSink writes one human-readable line per metric to a writer.
type StdoutSink struct {
	mu sync.Mutex
	w  io.Writer
}

// NewStdoutSink returns a sink that writes to os.Stdout.
func NewStdoutSink() Sink {
	return &StdoutSink{w: os.Stdout}
}

// StdoutSinkWriter returns a sink that writes to the given writer (test
// seam).
func StdoutSinkWriter(w io.Writer) Sink {
	return &StdoutSink{w: w}
}

// Emit implements Sink.
func (s *StdoutSink) Emit(m Metric) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := fmt.Fprintln(s.w, FormatLine(m))
	return err
}

// FileSink appends one JSON line per metric to a file.
type FileSink struct {
	mu sync.Mutex
	f  *os.File
}

// NewFileSink opens (creating if needed) a JSONL file for append.
func NewFileSink(path string) (*FileSink, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("metrics: open %s: %w", path, err)
	}
	return &FileSink{f: f}, nil
}

// Emit implements Sink.
func (s *FileSink) Emit(m Metric) error {
	line, err := JSONLine(m)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.f.Write(append(line, '\n'))
	return err
}

// Close closes the underlying file.
func (s *FileSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return nil
	}
	err := s.f.Close()
	s.f = nil
	return err
}
