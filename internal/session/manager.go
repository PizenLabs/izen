package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Manager is the audit-driven, crash-resilient, concurrency-safe session
// authority of the Izen Session Management System. It owns the dual-slot
// session store (A/B), the atomic active pointer, the two-tier lock, and the
// INV-SESSION-14 recovery ladder.
//
// It is deliberately NOT an execution engine: all execution state remains
// owned by the single RuntimeExecutor authority. The Manager only serializes
// conversational session persistence and pointer switching; the RuntimeExecutor
// integration seam (BoundaryHook) drains pending mutations at a session
// boundary and nothing more.
//
// Layout under the workspace root:
//
//	.izen/sessions/
//	  active            # atomic pointer: "A\n" or "B\n"
//	  .lock             # flock target (two-tier lock tier 2)
//	  A/session.json    # authoritative session record
//	  A/context.json    # derived compact context (INV-SESSION-14 tier 2)
//	  A/history.ndjson  # append-only raw history (INV-SESSION-14 tier 3)
//	  A/checkpoint.json # latest valid checkpoint marker
//	  B/...             # dormant sibling slot
//
// Invariants enforced:
//
//   - Exactly one active slot at all times: (A active, B dormant) or vice
//     versa. Pointer switching is rename()-atomic.
//   - Every session mutation is transactional and idempotent: a retry after a
//     crash converges to a valid pointer state.
//   - A missing/corrupted compact context never renders a session
//     unrecoverable (INV-SESSION-14).
type Manager struct {
	root string
	dir  string

	lock     *sessionLock
	maxTurns int
	hook     BoundaryHook
	cpEngine CheckpointValidator

	// crash is the test-only fault-injection seam. It is invoked at
	// deterministic points inside NewSession/ResumeSession so tests can
	// simulate a hard crash mid-operation. Never set in production.
	crash func(CrashPoint) error

	// mu guards the in-memory active/session bookkeeping against concurrent
	// readers (the UI goroutine + background autonomy). The persistence
	// serialization lives in lock; this mutex only protects the mirror.
	mu      sync.RWMutex
	active  SlotID
	session *Session
	opened  bool

	pointerRecovered bool
	lastBoundaryErr  error
}

// Option configures a Manager.
type Option func(*Manager)

// WithMaxTurns sets the sliding-window turn limit applied on raw-history
// rebuild. It is a policy knob, not a hardcoded invariant (defaults to 5).
func WithMaxTurns(n int) Option {
	return func(m *Manager) {
		if n > 0 {
			m.maxTurns = n
		}
	}
}

// WithLockConfig tunes the cross-process flock acquisition loop.
func WithLockConfig(cfg LockConfig) Option {
	return func(m *Manager) {
		if cfg.Timeout > 0 {
			m.lock.timeout = cfg.Timeout
		}
		if cfg.Backoff > 0 {
			m.lock.backoff = cfg.Backoff
		}
	}
}

// WithBoundaryHook wires the RuntimeExecutor integration seam invoked after
// every atomic pointer commit.
func WithBoundaryHook(h BoundaryHook) Option {
	return func(m *Manager) {
		m.hook = h
	}
}

// WithCheckpointValidator wires an external validator for checkpoint ids (the
// RuntimeExecutor's checkpoint engine). When nil, marker ids are trusted.
func WithCheckpointValidator(v CheckpointValidator) Option {
	return func(m *Manager) {
		m.cpEngine = v
	}
}

// withCrashHook installs the test-only fault-injection seam.
func withCrashHook(fn func(CrashPoint) error) Option {
	return func(m *Manager) {
		m.crash = fn
	}
}

// NewManager constructs the session authority for the given workspace root.
// It is inert until Open is called.
func NewManager(root string, opts ...Option) *Manager {
	m := &Manager{
		root:     root,
		dir:      filepath.Join(root, ".izen", "sessions"),
		maxTurns: 5,
	}
	m.lock = newSessionLock(m.dir, DefaultLockConfig())
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// BoundaryHook is the RuntimeExecutor integration seam. It is invoked AFTER an
// atomic pointer commit with the previous and next active slots, so a session
// boundary can drain execution state through the single RuntimeExecutor
// authority. It must be fast and idempotent; its error is recorded but never
// rolls back the (already committed) pointer.
type BoundaryHook interface {
	OnSessionSwitch(ctx context.Context, prev, next SlotID) error
}

// BoundaryHookFunc adapts a function to BoundaryHook.
type BoundaryHookFunc func(ctx context.Context, prev, next SlotID) error

// OnSessionSwitch implements BoundaryHook.
func (f BoundaryHookFunc) OnSessionSwitch(ctx context.Context, prev, next SlotID) error {
	if f == nil {
		return nil
	}
	return f(ctx, prev, next)
}

// CheckpointValidator validates a checkpoint id against the workspace
// checkpoint store. Implemented by the checkpoint engine adapter.
type CheckpointValidator interface {
	// Valid reports whether the checkpoint id is present and loadable.
	Valid(id string) bool
}

// CrashPoint identifies the deterministic fault-injection boundary inside a
// session mutation. Test-only.
type CrashPoint int

const (
	// CrashAfterPersistActive simulates a crash after the active slot is
	// persisted but before the new slot is prepared.
	CrashAfterPersistActive CrashPoint = iota
	// CrashAfterPrepareNew simulates a crash after the dormant slot is
	// prepared but before the pointer commit.
	CrashAfterPrepareNew
	// CrashAfterPointerTmp simulates a crash between writing active.tmp and
	// the rename — the interruptible window of the pointer commit.
	CrashAfterPointerTmp
	// CrashAfterPointerCommit simulates a crash exactly at the atomic commit
	// boundary; the pointer has already switched.
	CrashAfterPointerCommit
)

// Open resolves the active pointer (running the crash-recovery protocol) and
// loads the active session through the INV-SESSION-14 ladder. It is idempotent
// and concurrency-safe.
func (m *Manager) Open(ctx context.Context) error {
	m.mu.RLock()
	if m.opened {
		m.mu.RUnlock()
		return nil
	}
	m.mu.RUnlock()

	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return err
	}
	if err := m.lock.acquire(ctx); err != nil {
		return err
	}
	defer func() { _ = m.lock.release() }()

	slot, err := m.recoverPointer()
	if err != nil {
		return err
	}
	sess, err := m.loadSlot(slot)
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.active = slot
	m.session = sess
	m.opened = true
	m.mu.Unlock()
	return nil
}

// Close releases the cross-process lock and removes the lockfile. Idempotent.
func (m *Manager) Close() error {
	if m == nil || m.lock == nil {
		return nil
	}
	if err := m.lock.release(); err != nil {
		m.lock.close()
		return err
	}
	m.lock.close()
	return nil
}

// Active returns the currently active slot. Panics-free before Open: returns "".
func (m *Manager) Active() SlotID {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.active
}

// Session returns the active in-memory session. The returned pointer IS the
// canonical session the presentation layer mutates; Persist/save flush it to
// its slot.
func (m *Manager) Session() *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.session
}

// PointerRecovered reports whether the active pointer required repair at Open.
func (m *Manager) PointerRecovered() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.pointerRecovered
}

// LastBoundaryErr returns the most recent non-fatal boundary-hook error (the
// RuntimeExecutor drain), if any.
func (m *Manager) LastBoundaryErr() error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastBoundaryErr
}

// NewSession implements `/new`: acquire lock -> persist current -> create new
// -> atomically commit active pointer -> release lock. The previous active
// session is preserved in its slot (dormant) and becomes resumable via
// ResumeSession.
func (m *Manager) NewSession(ctx context.Context) (*Session, error) {
	m.mu.RLock()
	opened := m.opened
	prev := m.active
	m.mu.RUnlock()
	if !opened {
		return nil, errors.New("session: manager not opened")
	}

	if err := m.lock.acquire(ctx); err != nil {
		return nil, err
	}
	defer func() { _ = m.lock.release() }()

	// 1. Persist current active session.
	if err := m.persistActiveLocked(); err != nil {
		return nil, err
	}
	if m.crash != nil {
		if err := m.crash(CrashAfterPersistActive); err != nil {
			return nil, err
		}
	}

	// 2. Create the new session in the dormant slot (prepare-before-commit).
	next := prev.Other()
	fresh := New()
	fresh.SessionID = newSessionID(next)
	m.attachSessionPaths(fresh, next)
	if err := m.persistSlotLocked(next, fresh); err != nil {
		return nil, err
	}
	if m.crash != nil {
		if err := m.crash(CrashAfterPrepareNew); err != nil {
			return nil, err
		}
	}

	// 3. Atomically commit the active pointer to the new slot.
	if err := m.commitPointer(next); err != nil {
		return nil, err
	}

	// 4. Swap the in-memory mirror and notify the execution boundary.
	m.mu.Lock()
	m.active = next
	m.session = fresh
	m.mu.Unlock()
	m.notifyBoundary(ctx, prev, next)

	return fresh, nil
}

// ResumeSession implements `/session resume <B>`: validate B -> acquire lock
// -> persist active A -> prepare B -> atomically commit active pointer ->
// release lock. Resuming the already-active slot is an idempotent no-op.
func (m *Manager) ResumeSession(ctx context.Context, target SlotID) (*Session, error) {
	if !validSlot(target) {
		return nil, fmt.Errorf("session: invalid target slot %q (expected A or B)", target)
	}
	m.mu.RLock()
	opened := m.opened
	prev := m.active
	m.mu.RUnlock()
	if !opened {
		return nil, errors.New("session: manager not opened")
	}

	// Validate B BEFORE acquiring the lock (spec order). The re-check under
	// the lock in prepare is the authoritative gate.
	if err := m.validateSlot(target); err != nil {
		return nil, err
	}
	if target == prev {
		m.mu.RLock()
		sess := m.session
		m.mu.RUnlock()
		return sess, nil
	}

	if err := m.lock.acquire(ctx); err != nil {
		return nil, err
	}
	defer func() { _ = m.lock.release() }()

	// Persist active A.
	if err := m.persistActiveLocked(); err != nil {
		return nil, err
	}
	if m.crash != nil {
		if err := m.crash(CrashAfterPersistActive); err != nil {
			return nil, err
		}
	}

	// Prepare B through the recovery ladder.
	targetSess, err := m.loadSlot(target)
	if err != nil {
		return nil, err
	}
	m.attachSessionPaths(targetSess, target)
	if m.crash != nil {
		if err := m.crash(CrashAfterPrepareNew); err != nil {
			return nil, err
		}
	}

	// Atomically commit the active pointer.
	if err := m.commitPointer(target); err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.active = target
	m.session = targetSess
	m.mu.Unlock()
	m.notifyBoundary(ctx, prev, target)

	return targetSess, nil
}

// Persist flushes the active session to its slot transactionally: session.json
// + derived compact context + checkpoint marker, all atomic.
func (m *Manager) Persist(ctx context.Context) error {
	if err := m.lock.acquire(ctx); err != nil {
		return err
	}
	defer func() { _ = m.lock.release() }()
	return m.persistActiveLocked()
}

// AppendHistory appends a raw turn to the active slot's durable history log —
// the INV-SESSION-14 rebuild source. It is concurrency-safe (O_APPEND).
func (m *Manager) AppendHistory(ctx context.Context, role, content string) error {
	m.mu.RLock()
	active := m.active
	m.mu.RUnlock()
	return m.appendRawHistory(active, role, content)
}

// SetCompactContext atomically replaces the slot's compact context with a
// compaction-engine generation. It is the integration seam the asynchronous
// Compaction Runner sinks into: the runner computes a generation off the
// history log and this method persists it without touching the authoritative
// session record or the raw history. Returns an error when the manager is not
// opened or the slot is invalid.
func (m *Manager) SetCompactContext(ctx context.Context, s SlotID, cc *CompactContext) error {
	if !validSlot(s) {
		return fmt.Errorf("session: invalid slot %q", s)
	}
	if err := m.lock.acquire(ctx); err != nil {
		return err
	}
	defer func() { _ = m.lock.release() }()
	if cc == nil {
		return nil
	}
	return m.writeCompactContextValue(s, cc)
}

// SlotInfo is a read-only projection of one slot for /session listing.
type SlotInfo struct {
	Slot      SlotID    `json:"slot"`
	Active    bool      `json:"active"`
	Exists    bool      `json:"exists"`
	Objective string    `json:"objective,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
	TurnCount int       `json:"turn_count"`
	Recovered bool      `json:"recovered,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// List projects both slots for observability. It never mutates state and
// never fails: unreadable slots surface as Exists=false with an Error string.
func (m *Manager) List(ctx context.Context) []SlotInfo {
	m.mu.RLock()
	active := m.active
	m.mu.RUnlock()

	infos := make([]SlotInfo, 0, 2)
	for _, s := range []SlotID{SlotA, SlotB} {
		info := SlotInfo{Slot: s, Active: s == active}
		sess, err := m.readSessionRecord(s)
		switch {
		case err != nil:
			info.Exists = true
			info.Error = err.Error()
		case sess != nil:
			info.Exists = true
			info.Objective = sess.ObjectiveIntent()
			info.SessionID = sess.SessionID
			info.UpdatedAt = sess.UpdatedAt
			info.TurnCount = len(sess.History)
			info.Recovered = sess.recoveredFromRawHistory()
		default:
			// Slot has durable data but no authoritative record.
			if cc, cerr := m.readCompactContext(s); cerr == nil && cc != nil {
				info.Exists = true
				info.Objective = cc.Objective
				info.SessionID = cc.SessionID
				info.UpdatedAt = cc.UpdatedAt
				info.TurnCount = cc.TurnCount
			} else if _, statErr := os.Stat(filepath.Join(m.slotDir(s), RawHistoryFileName)); statErr == nil {
				info.Exists = true
				info.Error = "recoverable from raw history"
			}
		}
		infos = append(infos, info)
	}
	return infos
}

// ── internals ──────────────────────────────────────────────────────────────

// persistActiveLocked flushes the current in-memory active session to its slot.
// Caller must hold the session lock.
func (m *Manager) persistActiveLocked() error {
	m.mu.RLock()
	active := m.active
	sess := m.session
	m.mu.RUnlock()
	if sess == nil {
		return nil
	}
	return m.persistSlotLocked(active, sess)
}

// persistSlotLocked writes the full durable set for a slot: session.json
// (atomic), derived compact context (atomic), checkpoint marker (atomic).
// Caller must hold the session lock.
func (m *Manager) persistSlotLocked(s SlotID, sess *Session) error {
	if sess == nil {
		return nil
	}
	sess.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(m.slotDir(s), sessionFile), data); err != nil {
		return err
	}
	if err := m.writeCompactContext(s, sess); err != nil {
		return err
	}
	if cp := latestCheckpointID(sess); cp != "" {
		if err := m.writeCheckpointMarker(s, cp); err != nil {
			return err
		}
	}
	return nil
}

// validateSlot asserts the target slot is a real, recoverable session. A slot
// is valid when any tier of the load ladder holds durable data.
func (m *Manager) validateSlot(s SlotID) error {
	dir := m.slotDir(s)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("session: no such session slot %q", s)
	}
	if _, err := os.Stat(filepath.Join(dir, sessionFile)); err == nil {
		return nil
	}
	if cc, err := m.readCompactContext(s); err == nil && cc != nil {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, RawHistoryFileName)); err == nil {
		return nil
	}
	return fmt.Errorf("session: slot %q contains no recoverable session data", s)
}

// notifyBoundary invokes the RuntimeExecutor drain after a committed switch.
// The hook error is recorded but never rolls back the committed pointer.
func (m *Manager) notifyBoundary(ctx context.Context, prev, next SlotID) {
	if m.hook == nil {
		return
	}
	if err := m.hook.OnSessionSwitch(ctx, prev, next); err != nil {
		m.mu.Lock()
		m.lastBoundaryErr = err
		m.mu.Unlock()
	}
}
