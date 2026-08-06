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

// Write marshals one envelope as a single NDJSON line and appends it. It is
// safe for concurrent use.
func (s *Store) Write(env events.Envelope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
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

// Flush pushes any buffered lines to the underlying file. Safe to call from
// any goroutine.
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	return s.w.Flush()
}

// Close flushes buffered lines and closes the underlying file. Subsequent
// writes fail. Idempotent.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	ferr := s.w.Flush()
	cerr := s.f.Close()
	if ferr != nil {
		return ferr
	}
	return cerr
}
