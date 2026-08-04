// Package metrics collects the raw, deterministic execution metrics of a
// runtime run — latency per phase, token usage, the selected strategy and the
// phase status — and emits them to one or more sinks (stdout, JSONL file).
// Emission is best-effort and non-blocking: a failing sink never aborts a
// run.
package metrics

import (
	"sync"
	"time"
)

// Status classifies one metric line.
type Status string

const (
	// StatusOK marks a completed phase.
	StatusOK Status = "ok"
	// StatusFailed marks a failed phase.
	StatusFailed Status = "failed"
	// StatusSkipped marks a phase that was deliberately skipped.
	StatusSkipped Status = "skipped"
)

// Metric is a single immutable execution metric line.
type Metric struct {
	RunID     string
	Phase     string
	Status    Status
	Latency   time.Duration
	Tokens    int
	Strategy  string
	Timestamp time.Time
	Err       string
}

// Sink consumes metrics. Implementations must be safe for concurrent use.
type Sink interface {
	Emit(m Metric) error
}

// Option configures a Collector.
type Option func(*Collector)

// WithClock overrides the collector clock (test seam).
func WithClock(now func() time.Time) Option {
	return func(c *Collector) {
		if now != nil {
			c.clock = now
		}
	}
}

// Collector fans metrics out to registered sinks. It is safe for concurrent
// use.
type Collector struct {
	mu    sync.Mutex
	sinks []Sink
	clock func() time.Time
}

// NewCollector returns an empty collector.
func NewCollector(opts ...Option) *Collector {
	c := &Collector{clock: time.Now}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Sink registers a sink. Nil sinks are ignored.
func (c *Collector) Sink(s Sink) *Collector {
	if s == nil {
		return c
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sinks = append(c.sinks, s)
	return c
}

// Sinks returns a snapshot of the registered sinks.
func (c *Collector) Sinks() []Sink {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Sink(nil), c.sinks...)
}

// Emit stamps a zero timestamp and forwards the metric to every sink.
// Sink errors are swallowed: metrics are observability, never correctness.
func (c *Collector) Emit(m Metric) {
	if m.Timestamp.IsZero() {
		m.Timestamp = c.clock()
	}
	for _, s := range c.Sinks() {
		_ = s.Emit(m)
	}
}
