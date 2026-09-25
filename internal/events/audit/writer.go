package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PizenLabs/izen/internal/events"
)

// channelBuffer is the capacity of the audit logger's internal buffered
// channel. It absorbs bursts between the bus dispatch goroutine and the disk
// worker; the domain bus already bounds back-pressure at its own per-
// subscription buffer, so this buffer only needs to smooth scheduling.
const channelBuffer = 2048

// AuditLogger subscribes to the domain event bus and appends every
// events.Envelope instance to the NDJSON audit store asynchronously.
//
// Envelopes are pushed onto an internal buffered channel and a single worker
// goroutine drains them and writes to disk sequentially. The push is
// non-blocking (select-default): when the worker cannot keep up, envelopes are
// dropped and counted instead of stalling the bus dispatch goroutine. Because
// the domain bus itself publishes non-blocking and drops on slow per-
// subscription buffers, disk I/O can never slow down event propagation or TUI
// rendering. Dropped counts are exposed via Dropped() so operators can detect
// sustained back-pressure.
type AuditLogger struct {
	bus      *events.Bus
	store    *Store
	sub      *events.Subscription
	ch       chan events.Envelope
	done     chan struct{}
	wg       sync.WaitGroup
	start    atomic.Bool
	dropped  atomic.Uint64
	accepted atomic.Uint64
	written  atomic.Uint64 // envelopes the disk worker has attempted (ok or fail)
	writeErr atomic.Value  // stores the first error from the disk worker

	// sessionID returns the active session id to stamp onto every persisted
	// record (INV-SESSION-10). It is resolved at event-handling time — the
	// moment the event crosses the bus — so each audit line maps to the session
	// that produced it. A nil resolver leaves session_id empty (harness mode).
	sessionID func() string
}

// SetSessionResolver wires the active-session correlation source
// (INV-SESSION-10). The resolver is invoked on the bus dispatch goroutine when
// an event is accepted; it must be fast and non-blocking. It is typically the
// SessionManager's active-session accessor.
func (l *AuditLogger) SetSessionResolver(fn func() string) {
	if l == nil {
		return
	}
	l.sessionID = fn
}

// NewLogger creates an audit logger rooted at dir that persists envelopes
// published on bus to dir/events.ndjson. A nil bus is an error. The logger is
// not subscribed until Start is called.
func NewLogger(dir string, bus *events.Bus) (*AuditLogger, error) {
	if bus == nil {
		return nil, errors.New("audit: nil event bus")
	}
	store, err := NewStore(filepath.Join(dir, DefaultFileName))
	if err != nil {
		return nil, err
	}
	return &AuditLogger{
		bus:   bus,
		store: store,
		ch:    make(chan events.Envelope, channelBuffer),
		done:  make(chan struct{}),
	}, nil
}

// Start subscribes the logger to every event on the bus and launches the disk
// worker. It is idempotent. It returns nil when the logger cannot be started
// (e.g. the bus is closed or already has no room for another subscriber).
func (l *AuditLogger) Start() error {
	if l == nil || l.bus == nil || l.store == nil {
		return nil
	}
	if !l.start.CompareAndSwap(false, true) {
		return nil
	}
	l.sub = l.bus.SubscribeAll(l.handle)
	if l.sub == nil {
		l.start.Store(false)
		return errors.New("audit: failed to subscribe to the event bus")
	}
	l.wg.Add(1)
	go l.run()
	return nil
}

// Stop unsubscribes the logger and stops the worker after draining buffered
// envelopes. It does NOT close the store file; call Close for full teardown.
func (l *AuditLogger) Stop() {
	if l == nil || !l.start.CompareAndSwap(true, false) {
		return
	}
	if l.sub != nil {
		l.sub.Cancel()
	}
	close(l.done)
	l.wg.Wait()
}

// Close stops the logger (draining buffered envelopes) and closes the store
// file. Safe to call when never started; idempotent.
func (l *AuditLogger) Close() error {
	if l == nil {
		return nil
	}
	l.Stop()
	if l.store != nil {
		return l.store.Close()
	}
	return nil
}

// Dropped returns the number of envelopes dropped because the disk worker
// could not keep up. A non-zero value indicates sustained write back-pressure.
func (l *AuditLogger) Dropped() uint64 {
	if l == nil {
		return 0
	}
	return l.dropped.Load()
}

// Accepted returns the number of envelopes the logger accepted for persistence
// (pushed onto the internal channel). It is primarily a test/diagnostic
// hook to observe asynchronous delivery deterministically.
func (l *AuditLogger) Accepted() uint64 {
	if l == nil {
		return 0
	}
	return l.accepted.Load()
}

// Flush is the synchronous durability seam: it blocks until every accepted
// envelope has been attempted by the disk worker, then flushes + fsyncs the
// underlying NDJSON file. It returns the first write/flush error encountered
// (worker error takes precedence) and MUST NOT be swallowed: audit
// persistence failure structurally invalidates execution success
// (ErrAuditPersistenceFailed in internal/runtime/orchestrator).
//
// It never stops the logger and is safe to call from signal handlers, defer
// chains, and session finalization paths.
func (l *AuditLogger) Flush() error {
	if l == nil || l.store == nil {
		return nil
	}
	// Drain: wait until the worker has attempted every accepted envelope.
	// accepted counts pushes; written counts worker attempts; their gap is
	// the pending backlog (channel + in-flight write).
	deadline := time.Now().Add(5 * time.Second)
	for l.accepted.Load() > l.written.Load() {
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	// Also wait for the channel itself to empty (covers a racing handle push
	// that incremented accepted after the last check).
	deadline = time.Now().Add(2 * time.Second)
	for len(l.ch) > 0 {
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	// Give the worker one final scheduling quantum to finish an in-flight
	// bufio write before we flush it.
	time.Sleep(5 * time.Millisecond)
	if err := l.store.Flush(); err != nil {
		if werr := l.Err(); werr != nil {
			return fmt.Errorf("audit flush: %w (worker: %w)", err, werr)
		}
		return err
	}
	if werr := l.Err(); werr != nil {
		return fmt.Errorf("audit: persisted flush ok but worker reported: %w", werr)
	}
	return nil
}

// InjectWriteError arms a deterministic I/O failure on the underlying store
// (simulated disk write error / read-only permission on events.ndjson). It is
// the adversarial seam for audit-persistence-failure tests. Pass nil to clear.
func (l *AuditLogger) InjectWriteError(err error) {
	if l == nil || l.store == nil {
		return
	}
	l.store.InjectWriteError(err)
}

// Path returns the NDJSON log file path, or "" when the logger is nil.
func (l *AuditLogger) Path() string {
	if l == nil || l.store == nil {
		return ""
	}
	return l.store.Path()
}

// Err returns the first non-nil write error encountered by the disk worker,
// or nil when all writes succeeded.
func (l *AuditLogger) Err() error {
	if l == nil {
		return nil
	}
	if err := l.writeErr.Load(); err != nil {
		return err.(error)
	}
	return nil
}

// redactAuditPayload removes raw user/model bytes while retaining the
// structural counters and fingerprints carried by the event. This is applied
// only at the durable audit boundary; in-process projections still receive the
// original typed event.
func redactAuditPayload(payload any) (any, bool) {
	switch value := payload.(type) {
	case events.ExecutionStartedPayload:
		value.Prompt = ""
		return value, true
	case events.IntentParsedPayload:
		value.Raw = ""
		return value, true
	case events.CommandReceivedPayload:
		value.Command = ""
		return value, true
	case events.PromptAdmittedPayload:
		value.Prompt = ""
		return value, true
	case events.ProviderStreamDeltaPayload:
		value.Delta = ""
		return value, true
	case events.ReasoningPayload:
		value.Chunk = ""
		return value, true
	case events.ApprovalRequiredPayload:
		value.Preview = ""
		return value, true
	case events.ExecutionFailedPayload:
		value.Error = ""
		return value, true
	default:
		return redactGenericPayload(payload)
	}
}

func redactGenericPayload(payload any) (any, bool) {
	data, err := json.Marshal(payload)
	if err != nil {
		return payload, false
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return payload, false
	}
	redacted, changed := redactJSONValue(value)
	if !changed {
		return payload, false
	}
	return redacted, true
}

func redactJSONValue(value any) (any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		changed := false
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			switch strings.ToLower(key) {
			case "prompt", "raw", "command", "delta", "chunk", "content", "output", "preview", "error":
				out[key] = ""
				changed = true
				continue
			}
			redacted, childChanged := redactJSONValue(child)
			out[key] = redacted
			changed = changed || childChanged
		}
		return out, changed
	case []any:
		changed := false
		out := make([]any, 0, len(typed))
		for _, child := range typed {
			redacted, childChanged := redactJSONValue(child)
			out = append(out, redacted)
			changed = changed || childChanged
		}
		return out, changed
	default:
		return value, false
	}
}

// handle runs on the bus dispatch goroutine. It forwards every envelope onto
// the internal channel with a non-blocking push so a stalled disk worker can
// never block the publisher or the TUI projection. The channel is never
// closed, so a late handler invocation can never send on a closed channel.
//
// Every event — typed domain events AND envelopes — is persisted: typed events
// are wrapped into envelopes with their canonical Type() preserved in Source,
// so the NDJSON audit log is a complete, session-correlated record of the
// whole stream.
func (l *AuditLogger) handle(ev events.DomainEvent) {
	if ev == nil {
		return
	}
	env, ok := events.EnvelopeFromEvent(ev)
	if !ok {
		env = events.Envelope{
			ID:        events.NewEnvelopeID(),
			Timestamp: ev.Timestamp(),
			Source:    ev.Type(),
			Kind:      events.DomainKindSystem,
			Payload:   ev.Payload(),
		}
	}
	if safe, redacted := redactAuditPayload(env.Payload); redacted {
		env.Payload = safe
		env.Redacted = true
	}
	if l.sessionID != nil {
		if sid := l.sessionID(); sid != "" {
			env.SessionID = sid
		}
	}
	select {
	case l.ch <- env:
		l.accepted.Add(1)
	default:
		l.dropped.Add(1)
	}
}

// run drains the internal channel and writes envelopes sequentially. It exits
// after Stop signals via l.done AND the channel is drained, so buffered
// envelopes are flushed before teardown. Any final write error is retained
// for Err().
func (l *AuditLogger) run() {
	defer l.wg.Done()
	for {
		select {
		case env := <-l.ch:
			if err := l.store.Write(env); err != nil && l.writeErr.Load() == nil {
				l.writeErr.Store(err)
			}
			l.written.Add(1)
		case <-l.done:
			for {
				select {
				case env := <-l.ch:
					if err := l.store.Write(env); err != nil && l.writeErr.Load() == nil {
						l.writeErr.Store(err)
					}
					l.written.Add(1)
				default:
					return
				}
			}
		}
	}
}
