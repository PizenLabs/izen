package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	guard    WorkspaceGuard

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

// WithWorkspaceGuard wires the workspace-boundary safety seam: on every session
// switch (/new, /session resume) the guard's dirty files are injected into the
// target session's Context Compiler view so uncommitted work from another
// session is never silently overwritten. A nil guard disables the boundary
// check.
func WithWorkspaceGuard(g WorkspaceGuard) Option {
	return func(m *Manager) {
		m.guard = g
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

// WorkspaceGuard is the workspace-boundary safety seam (workspace guard). It
// reports the workspace-relative files with uncommitted changes (Git status /
// checkpoint baseline divergence) that a session switch must surface instead of
// silently overwriting. It is wired by the composition root to the Git engine;
// the Session Manager only consumes its output and never executes mutations
// (INV-SESSION-09).
type WorkspaceGuard interface {
	// DirtyFiles returns the workspace-relative paths of files with uncommitted
	// changes (excluding .izen/). An error is non-fatal: the switch proceeds and
	// the failure is recorded as the last boundary error.
	DirtyFiles(ctx context.Context) ([]string, error)
}

// WorkspaceGuardFunc adapts a function to WorkspaceGuard.
type WorkspaceGuardFunc func(ctx context.Context) ([]string, error)

// DirtyFiles implements WorkspaceGuard.
func (f WorkspaceGuardFunc) DirtyFiles(ctx context.Context) ([]string, error) {
	if f == nil {
		return nil, nil
	}
	return f(ctx)
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

// ActiveSessionID returns the SessionID of the currently active session, or ""
// before Open / when no session is loaded. It is the INV-SESSION-10 correlation
// source the audit logger and the RuntimeExecutor resolve the active session
// from.
func (m *Manager) ActiveSessionID() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.session == nil {
		return ""
	}
	return m.session.SessionID
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

	// 1. Persist current active session (transitioned to dormant). Only
	//    explicit lifecycle commands may overwrite an archived session, so a
	//    dormant slot holding an archived session must be surfaced, never
	//    silently replaced.
	next := prev.Other()
	if m.slotLifecycle(next) == LifecycleArchived {
		return nil, fmt.Errorf("session: slot %s holds an ARCHIVED session — resume or delete it before /new (only explicit lifecycle commands may overwrite archived state)", next)
	}

	// 2. Persist the current active session as dormant.
	m.mu.RLock()
	if cur := m.session; cur != nil {
		cur.Lifecycle = LifecycleDormant
	}
	m.mu.RUnlock()
	if err := m.persistActiveLocked(); err != nil {
		return nil, err
	}
	if m.crash != nil {
		if err := m.crash(CrashAfterPersistActive); err != nil {
			return nil, err
		}
	}

	// 3. Create the new session in the dormant slot (prepare-before-commit).
	fresh := New()
	fresh.SessionID = newSessionID(next)
	fresh.Lifecycle = LifecycleActive
	dirty := m.resolveDirtyFiles(ctx)
	if len(dirty) > 0 {
		fresh.WorkspaceDirtyFiles = dirty
	}
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

	// Persist active A (transitioned to dormant).
	m.mu.RLock()
	if cur := m.session; cur != nil {
		cur.Lifecycle = LifecycleDormant
	}
	m.mu.RUnlock()
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
	targetSess.Lifecycle = LifecycleActive
	// Workspace boundary guard: inject the current uncommitted changes into the
	// target session's Context Compiler view so work left by the previous
	// session is never silently overwritten. Guard errors are non-fatal and
	// recorded as the last boundary error; the switch still commits.
	if dirty := m.resolveDirtyFiles(ctx); len(dirty) > 0 {
		targetSess.WorkspaceDirtyFiles = dirty
	}
	// Persist the prepared target so the injected dirty files (and the active
	// lifecycle) survive into the durable record and the derived compact
	// context — the Context Compiler's view — before the pointer commits.
	if err := m.persistSlotLocked(target, targetSess); err != nil {
		return nil, err
	}
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
	Slot       SlotID    `json:"slot"`
	Active     bool      `json:"active"`
	Exists     bool      `json:"exists"`
	Lifecycle  string    `json:"lifecycle,omitempty"`
	Objective  string    `json:"objective,omitempty"`
	SessionID  string    `json:"session_id,omitempty"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
	TurnCount  int       `json:"turn_count"`
	DirtyCount int       `json:"dirty_count"`
	Recovered  bool      `json:"recovered,omitempty"`
	Error      string    `json:"error,omitempty"`
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
			info.DirtyCount = len(sess.WorkspaceDirtyFiles)
			switch {
			case sess.Lifecycle != "":
				info.Lifecycle = string(sess.Lifecycle)
			case s == active:
				info.Lifecycle = string(LifecycleActive)
			default:
				info.Lifecycle = string(LifecycleDormant)
			}
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

// Inspect returns the full session record of a slot for `/session inspect`.
// It never mutates state. The returned session is detached from the manager
// (a value copy of the durable record) so the caller can render it freely.
func (m *Manager) Inspect(s SlotID) (*Session, error) {
	if !validSlot(s) {
		return nil, fmt.Errorf("session: invalid slot %q (expected A or B)", s)
	}
	sess, err := m.sessionData(s)
	if err != nil {
		return nil, err
	}
	// Present a detached read-only projection: never hand out the live pointer.
	cp := *sess
	cp.path = ""
	cp.slotDir = ""
	cp.recovered = false
	return &cp, nil
}

// Rename atomically updates the mutable session title in the slot's
// session.json (SESSION.md §7: the title is mutable, the ID is not). It is a
// crash-resilient transactional write through the two-tier lock.
func (m *Manager) Rename(ctx context.Context, s SlotID, title string) error {
	if !validSlot(s) {
		return fmt.Errorf("session: invalid slot %q (expected A or B)", s)
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return errors.New("session: rename requires a non-empty title")
	}
	if err := m.lock.acquire(ctx); err != nil {
		return err
	}
	defer func() { _ = m.lock.release() }()

	sess, err := m.liveSessionFor(s)
	if err != nil {
		return err
	}
	sess.Title = title
	if err := m.persistSlotLocked(s, sess); err != nil {
		return err
	}
	// Mirror the rename into the live in-memory session when it is active.
	m.mu.Lock()
	if m.active == s && m.session != nil {
		m.session.Title = title
	}
	m.mu.Unlock()
	return nil
}

// Archive transitions a session's explicit lifecycle to ARCHIVED (SESSION.md
// §25/§28). An archived session remains inspectable and resumable unless
// explicitly deleted; only explicit lifecycle commands may move it back or
// overwrite it. Archiving is idempotent.
func (m *Manager) Archive(ctx context.Context, s SlotID) error {
	if !validSlot(s) {
		return fmt.Errorf("session: invalid slot %q (expected A or B)", s)
	}
	if err := m.lock.acquire(ctx); err != nil {
		return err
	}
	defer func() { _ = m.lock.release() }()

	sess, err := m.liveSessionFor(s)
	if err != nil {
		return err
	}
	if sess.Lifecycle == LifecycleArchived {
		return nil
	}
	sess.Lifecycle = LifecycleArchived
	if err := m.persistSlotLocked(s, sess); err != nil {
		return err
	}
	m.mu.Lock()
	if m.active == s && m.session != nil {
		m.session.Lifecycle = LifecycleArchived
	}
	m.mu.Unlock()
	return nil
}

// Delete explicitly purges the session-owned state of a slot — and ONLY that
// state (INV-SESSION-12). It never touches project configuration, the project
// graph, project knowledge, or the global audit log. Deleting the active slot
// atomically moves the pointer to the sibling slot BEFORE the directory is
// removed (crash-safe: a leftover directory is a dormant leftover, never a
// dangling pointer). Deleting a dormant slot is a plain directory removal.
// Audit evidence for the deleted session remains in .izen/audit/events.ndjson
// for forensic integrity.
func (m *Manager) Delete(ctx context.Context, s SlotID) error {
	if !validSlot(s) {
		return fmt.Errorf("session: invalid slot %q (expected A or B)", s)
	}
	if err := m.lock.acquire(ctx); err != nil {
		return err
	}
	defer func() { _ = m.lock.release() }()

	m.mu.RLock()
	active := m.active
	m.mu.RUnlock()

	// Deleting the active slot: commit the pointer to the sibling first so no
	// committed pointer can ever name a removed slot.
	if s == active {
		other := s.Other()
		// The sibling must be a real, recoverable session; a bare sibling is
		// bootstrapped so the pointer always lands on durable state.
		if err := m.validateSlot(other); err != nil {
			if err := m.bootstrapSlot(other); err != nil {
				return err
			}
		}
		if err := m.commitPointer(other); err != nil {
			return err
		}
		otherSess, err := m.loadSlot(other)
		if err != nil {
			return err
		}
		otherSess.Lifecycle = LifecycleActive
		if err := m.persistSlotLocked(other, otherSess); err != nil {
			return err
		}
		m.mu.Lock()
		m.active = other
		m.session = otherSess
		m.mu.Unlock()
	}

	if err := os.RemoveAll(m.slotDir(s)); err != nil {
		return fmt.Errorf("session: delete slot %s: %w", s, err)
	}
	return nil
}

// CompactContext returns the slot's current compact context generation, or nil
// when none exists. It is the read seam for the manual `/session compact`
// trigger, which combines it with the raw history through the Generational
// Compactor.
func (m *Manager) CompactContext(s SlotID) (*CompactContext, error) {
	if !validSlot(s) {
		return nil, fmt.Errorf("session: invalid slot %q (expected A or B)", s)
	}
	return m.readCompactContext(s)
}

// bootstrapSlot materializes a fresh durable session in an empty sibling slot
// so a pointer commit never lands on a nonexistent slot. Caller must hold the
// session lock.
func (m *Manager) bootstrapSlot(s SlotID) error {
	fresh := New()
	fresh.SessionID = newSessionID(s)
	fresh.Lifecycle = LifecycleActive
	m.attachSessionPaths(fresh, s)
	return m.persistSlotLocked(s, fresh)
}

// ── internals ──────────────────────────────────────────────────────────────

// resolveDirtyFiles queries the workspace guard for uncommitted changes. Guard
// errors are non-fatal (the switch still commits) and recorded as the last
// boundary error so operators can observe the degraded boundary check. It is
// called with the session lock held; the guard must be fast and non-blocking.
func (m *Manager) resolveDirtyFiles(ctx context.Context) []string {
	if m.guard == nil {
		return nil
	}
	dirty, err := m.guard.DirtyFiles(ctx)
	if err != nil {
		m.mu.Lock()
		m.lastBoundaryErr = fmt.Errorf("workspace guard: %w", err)
		m.mu.Unlock()
		return nil
	}
	return dirty
}

// slotLifecycle reads the explicit lifecycle recorded on a slot's authoritative
// session record, falling back to the compact context. A slot with no durable
// data is treated as LifecycleActive (fresh bootstrap).
func (m *Manager) slotLifecycle(s SlotID) Lifecycle {
	sess, err := m.readSessionRecord(s)
	if err == nil && sess != nil {
		if sess.Lifecycle != "" {
			return sess.Lifecycle
		}
		return LifecycleActive
	}
	if cc, cerr := m.readCompactContext(s); cerr == nil && cc != nil {
		return LifecycleActive
	}
	return LifecycleActive
}

// liveSessionFor returns the session record a management write should mutate:
// the LIVE in-memory session when the slot is active (preserving unpersisted
// presentation state), otherwise the durable record. Caller must hold the
// session lock.
func (m *Manager) liveSessionFor(s SlotID) (*Session, error) {
	m.mu.RLock()
	active := m.active
	live := m.session
	m.mu.RUnlock()
	if s == active && live != nil {
		return live, nil
	}
	return m.sessionData(s)
}

// sessionData loads the authoritative session record of a slot for management
// operations (inspect / rename / archive / compact). It errors when the slot
// holds no recoverable session data.
func (m *Manager) sessionData(s SlotID) (*Session, error) {
	sess, err := m.readSessionRecord(s)
	if err != nil {
		return nil, err
	}
	if sess != nil {
		return sess, nil
	}
	cc, cerr := m.readCompactContext(s)
	if cerr == nil && cc != nil {
		return hydrateSession(cc), nil
	}
	if _, statErr := os.Stat(filepath.Join(m.slotDir(s), RawHistoryFileName)); statErr == nil {
		rebuilt, _, rerr := m.rebuildFromRawHistory(s)
		if rerr != nil {
			return nil, rerr
		}
		if rebuilt != nil {
			return rebuilt, nil
		}
	}
	return nil, fmt.Errorf("session: slot %q contains no recoverable session data", s)
}

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
