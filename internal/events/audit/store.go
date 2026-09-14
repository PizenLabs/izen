// Package audit implements the append-only event audit store of the Izen
// control plane.
//
// The package answers exactly one question: "How are event envelopes persisted
// on disk?" A Store appends events.Envelope values to a JSON-Lines (NDJSON)
// file in strict append mode, and an AuditLogger subscribes to the domain
// event bus and persists every envelope asynchronously so disk I/O never slows
// down event propagation or TUI rendering.
//
// Dependency rule: the package depends on internal/events and internal/domain
// only — dependency strictly flows DOWN. It performs no policy evaluation and
// no routing.
package audit

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/PizenLabs/izen/internal/events"
)

// DefaultFileName is the NDJSON audit log file name inside the audit
// directory.
const DefaultFileName = "events.ndjson"

// Store is the append-only JSON-Lines (NDJSON) writer for events.Envelope
// values. It creates the target directory and opens the log file in strict
// append mode so historical records are never rewritten. All writes are
// serialized through an internal mutex, making the Store safe for concurrent
// use; in practice only the AuditLogger's worker goroutine writes.
type Store struct {
	mu     sync.Mutex
	f      *os.File
	w      *bufio.Writer
	path   string
	closed bool
	// failWrites is a test/adversarial hook: when non-nil, Write and Flush
	// fail with its error instead of touching disk. It simulates I/O disk
	// write errors and read-only permissions deterministically without
	// chmod races. Nil disables injection.
	failWrites error
}

// NewStore opens (creating as needed) the NDJSON log file at path, creating
// its parent directory. An empty path is an error.
func NewStore(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("audit: empty store path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("audit: create dir %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("audit: open %s: %w", path, err)
	}
	return &Store{
		f:    f,
		w:    bufio.NewWriter(f),
		path: path,
	}, nil
}

// Path returns the absolute/relative path of the NDJSON log file.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// InjectWriteError arms a deterministic I/O failure: subsequent Write and
// Flush calls fail with err instead of touching disk. Pass nil to clear.
// It is the adversarial seam for audit-persistence-failure tests (simulated
// disk write error / read-only permission on events.ndjson).
func (s *Store) InjectWriteError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failWrites = err
}

// Write marshals one envelope as a single NDJSON line and appends it. It is
// safe for concurrent use.
func (s *Store) Write(env events.Envelope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failWrites != nil {
		return fmt.Errorf("audit: injected write failure on %s: %w", s.path, s.failWrites)
	}
	if s.closed {
		return errors.New("audit: store closed")
	}
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("audit: encode envelope %s: %w", env.ID, err)
	}
	if _, err := s.w.Write(data); err != nil {
		return fmt.Errorf("audit: write %s: %w", s.path, err)
	}
	if err := s.w.WriteByte('\n'); err != nil {
		return fmt.Errorf("audit: write %s: %w", s.path, err)
	}
	return nil
}

// Flush pushes any buffered lines to the underlying file and fsyncs it for
// durability. Safe to call from any goroutine. A flush failure MUST NOT be
// swallowed: callers bind it into the Truthful State Transition evaluation
// (ErrAuditPersistenceFailed in internal/runtime/orchestrator).
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failWrites != nil {
		return fmt.Errorf("audit: injected flush failure on %s: %w", s.path, s.failWrites)
	}
	if s.closed {
		return nil
	}
	if err := s.w.Flush(); err != nil {
		return fmt.Errorf("audit: flush %s: %w", s.path, err)
	}
	if err := s.f.Sync(); err != nil {
		return fmt.Errorf("audit: sync %s: %w", s.path, err)
	}
	return nil
}

// Close flushes buffered lines, fsyncs, and closes the underlying file.
// Subsequent writes fail. Idempotent.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.failWrites != nil {
		_ = s.f.Close()
		return fmt.Errorf("audit: injected close failure on %s: %w", s.path, s.failWrites)
	}
	ferr := s.w.Flush()
	var serr error
	if ferr == nil {
		serr = s.f.Sync()
	}
	cerr := s.f.Close()
	if ferr != nil {
		return fmt.Errorf("audit: flush %s: %w", s.path, ferr)
	}
	if serr != nil {
		return fmt.Errorf("audit: sync %s: %w", s.path, serr)
	}
	if cerr != nil {
		return fmt.Errorf("audit: close %s: %w", s.path, cerr)
	}
	return nil
}
