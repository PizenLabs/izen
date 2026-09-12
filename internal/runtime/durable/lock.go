package durable

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultLeaseTTLMs is the default lock lease used when the caller does
// not specify one.
const DefaultLeaseTTLMs = int64(30000)

// procHolders tracks, per lock path, which handle in this process holds
// the runtime lock. It lets two handles in the same process contend like
// two OS processes (non-blocking failure) while the same handle may
// re-enter freely. Cross-process exclusion is enforced by the lock file's
// pid + lease below.
var (
	procMu      sync.Mutex
	procHolders = make(map[string]*FileLock)
)

// FileLock guards all ledger writes and workspace modifications.
// Lock file: <workDir>/.izen/runtime/lock with a JSON LockMetadata payload.
type FileLock struct {
	mu       sync.Mutex
	workDir  string
	path     string
	held     bool
	stopBeat chan struct{}
}

// NewFileLock returns an unacquired lock handle for workDir.
func NewFileLock(workDir string) *FileLock {
	return &FileLock{
		workDir: filepath.Clean(workDir),
		path:    filepath.Join(filepath.Clean(workDir), ".izen", "runtime", "lock"),
	}
}

// Path returns the lock file path.
func (l *FileLock) Path() string {
	return l.path
}

// Held reports whether this handle currently holds the lock.
func (l *FileLock) Held() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.held
}

// Acquire takes the process-level lock. If a lock file exists it inspects
// the recorded pid and lease: a dead pid or an expired lease (no fresh
// heartbeat) is forcefully reclaimed. Reclaim is reported via reclaimed=true
// so the caller can append STALE_LOCK_RECLAIMED to the ledger.
//
// leaseTTLMs <= 0 selects DefaultLeaseTTLMs. Acquire never blocks: a live
// holder causes an error describing the holder.
func (l *FileLock) Acquire(leaseTTLMs int64) (reclaimed bool, err error) {
	if l == nil {
		return false, fmt.Errorf("durable: nil FileLock")
	}
	if strings.TrimSpace(l.workDir) == "" {
		return false, fmt.Errorf("durable: empty workDir")
	}
	if leaseTTLMs <= 0 {
		leaseTTLMs = DefaultLeaseTTLMs
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return false, fmt.Errorf("durable: mkdir runtime: %w", err)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held {
		return false, nil
	}
	// Same-process contention: another handle already holds this path.
	procMu.Lock()
	if holder, ok := procHolders[l.path]; ok && holder != l {
		procMu.Unlock()
		return false, fmt.Errorf("durable: runtime locked by this process (holder %p)", holder)
	}
	procMu.Unlock()

	if meta, ok := readLockMeta(l.path); ok {
		if meta.PID == os.Getpid() {
			// Lock file carries our pid but no live handle in this
			// process holds it (e.g. a handle was dropped without
			// Release, or a previous test handle). Claim it as a
			// re-entrant hold and refresh the heartbeat.
			meta.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
			if meta.LeaseTTLMs <= 0 {
				meta.LeaseTTLMs = leaseTTLMs
			}
			_ = writeLockMetaAtomic(l.path, meta)
			l.held = true
			procMu.Lock()
			procHolders[l.path] = l
			procMu.Unlock()
			l.stopBeat = make(chan struct{})
			go l.heartbeat(leaseTTLMs, l.stopBeat)
			return false, nil
		}
		if pidAlive(meta.PID) && !leaseExpired(meta, leaseTTLMs) {
			return false, fmt.Errorf(
				"durable: runtime locked by live process pid=%d created_at=%s",
				meta.PID, meta.CreatedAt)
		}
		// Stale: dead pid or expired lease without heartbeat.
		reclaimed = true
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta := LockMetadata{
		PID:        os.Getpid(),
		CreatedAt:  now,
		LeaseTTLMs: leaseTTLMs,
		UpdatedAt:  now,
	}
	if err := writeLockMetaAtomic(l.path, meta); err != nil {
		return false, err
	}
	l.held = true
	procMu.Lock()
	procHolders[l.path] = l
	procMu.Unlock()
	l.stopBeat = make(chan struct{})
	go l.heartbeat(leaseTTLMs, l.stopBeat)
	return reclaimed, nil
}

// Release drops the lock held by this handle. It removes the lock file
// only when it still carries our pid (never deletes a successor's lock).
// It is idempotent.
func (l *FileLock) Release() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.held {
		return
	}
	if l.stopBeat != nil {
		close(l.stopBeat)
		l.stopBeat = nil
	}
	procMu.Lock()
	if holder, ok := procHolders[l.path]; ok && holder == l {
		delete(procHolders, l.path)
	}
	procMu.Unlock()
	if meta, ok := readLockMeta(l.path); ok {
		if meta.PID != os.Getpid() {
			l.held = false
			return
		}
	}
	_ = os.Remove(l.path)
	l.held = false
}

// Touch refreshes the heartbeat so a long-held lock does not look stale.
func (l *FileLock) Touch() {
	if l == nil {
		return
	}
	l.mu.Lock()
	held := l.held
	path := l.path
	l.mu.Unlock()
	if !held {
		return
	}
	if meta, ok := readLockMeta(path); ok {
		if meta.PID != os.Getpid() {
			return
		}
		meta.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		_ = writeLockMetaAtomic(path, meta)
	}
}

func (l *FileLock) heartbeat(leaseTTLMs int64, stop <-chan struct{}) {
	interval := time.Duration(leaseTTLMs/3) * time.Millisecond
	if interval < 500*time.Millisecond {
		interval = 500 * time.Millisecond
	}
	if interval > 10*time.Second {
		interval = 10 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			l.Touch()
		}
	}
}

func readLockMeta(path string) (LockMetadata, bool) {
	var m LockMetadata
	data, err := os.ReadFile(path)
	if err != nil || len(bytesTrimSpace(data)) == 0 {
		return m, false
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return LockMetadata{}, false
	}
	if m.PID <= 0 {
		return LockMetadata{}, false
	}
	return m, true
}

func leaseExpired(m LockMetadata, leaseTTLMs int64) bool {
	ttl := m.LeaseTTLMs
	if ttl <= 0 {
		ttl = leaseTTLMs
	}
	if ttl <= 0 {
		ttl = DefaultLeaseTTLMs
	}
	ref := m.UpdatedAt
	if ref == "" {
		ref = m.CreatedAt
	}
	ts, err := time.Parse(time.RFC3339Nano, ref)
	if err != nil {
		if ts2, err2 := time.Parse(time.RFC3339, ref); err2 == nil {
			ts = ts2
		} else {
			// Unparseable timestamp: treat as expired so a corrupt
			// lock can never hang the runtime forever.
			return true
		}
	}
	return time.Since(ts) > time.Duration(ttl)*time.Millisecond
}

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}
