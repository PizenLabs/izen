package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Active-Pointer Management (Crash Safety Guarantee B).
//
// The active pointer (.izen/sessions/active) is the single authority that
// decides which of the two slots (A, B) is live. Switching it is a commit
// protocol built entirely on the atomic filesystem rename(2) primitive:
//
//	1. write active.tmp   (O_SYNC + fsync)
//	2. rename(active.tmp, active)   // atomic: readers see old OR new, never
//	                                // a partial or dual state
//
// Crash-window analysis (each window leaves a valid pointer state):
//
//	- Crash during (1): active.tmp may be partial; active is untouched → the
//	  pointer still names the pre-switch slot. Recovery discards active.tmp.
//	- Crash between (1) and (2): active.tmp is complete; active is untouched.
//	  Recovery discards active.tmp (the rename never happened).
//	- Crash during/after (2): rename consumed the tmp; active names the new
//	  slot. No partial pointer file can exist at the committed name.
//
// Therefore the ONLY post-crash pointer states are "active names slot A" or
// "active names slot B" — the forbidden states (Active→Nonexistent, Active→
// Partially written, Active→Dual active) are unreachable from the protocol.
// External corruption of the pointer file (the one path that CAN produce an
// invalid name) is handled by recoverPointer below.

// activeFile is the pointer filename inside the sessions directory.
const activeFile = "active"

// activeTmpFile is the staging name the pointer is written to before the
// atomic rename. Its presence after a crash is the recovery signal that a
// switch was interrupted.
const activeTmpFile = "active.tmp"

// SlotID identifies one of the two durable session slots. The two-slot scheme
// is the crash-safety boundary: at any instant exactly one slot is active and
// the other is dormant.
type SlotID string

const (
	// SlotA is the first session slot (default active on first run).
	SlotA SlotID = "A"
	// SlotB is the second session slot.
	SlotB SlotID = "B"
)

// validSlot reports whether id is a well-formed slot identifier.
func validSlot(id SlotID) bool { return id == SlotA || id == SlotB }

// String implements fmt.Stringer.
func (s SlotID) String() string { return string(s) }

// Other returns the dormant sibling slot.
func (s SlotID) Other() SlotID {
	if s == SlotA {
		return SlotB
	}
	return SlotA
}

// slotDir returns the per-slot data directory.
func (m *Manager) slotDir(s SlotID) string { return filepath.Join(m.dir, string(s)) }

// readPointer loads the active slot from the pointer file. It returns
// (slot, nil) when the pointer is present and valid. A missing file yields
// ("", nil) so callers can bootstrap. An unreadable/parseable-but-invalid
// file yields a corruption error so callers can run recoverPointer.
func (m *Manager) readPointer() (SlotID, error) {
	data, err := os.ReadFile(filepath.Join(m.dir, activeFile))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	s := SlotID(strings.TrimSpace(string(data)))
	if !validSlot(s) {
		return "", fmt.Errorf("session: corrupt active pointer %q", strings.TrimSpace(string(data)))
	}
	return s, nil
}

// commitPointer atomically switches the active pointer to slot. It writes the
// candidate to active.tmp, fsyncs it, then rename(2)s it over active. The
// caller MUST hold the session lock; this is the atomic commit point of every
// session switch.
//
// The tmp-write and the rename are separate so the crash-simulation seam can
// inject a failure between them (the only interruptible window); recovery
// discards a stale active.tmp.
func (m *Manager) commitPointer(s SlotID) error {
	if !validSlot(s) {
		return fmt.Errorf("session: cannot commit invalid active pointer %q", s)
	}
	tmp := filepath.Join(m.dir, activeTmpFile)
	// 1. Write the candidate and fsync it to a well-known staging path.
	if err := writeFileSync(tmp, []byte(string(s)+"\n")); err != nil {
		return err
	}
	// 2. Crash window: between the staging write and the atomic rename. A
	//    crash here leaves active.tmp + the OLD pointer — recovery discards
	//    the tmp and keeps the old pointer.
	if m.crash != nil {
		if err := m.crash(CrashAfterPointerTmp); err != nil {
			return err
		}
	}
	// 3. The atomic commit. Readers observe old OR new, never partial/dual.
	if err := os.Rename(tmp, filepath.Join(m.dir, activeFile)); err != nil {
		return err
	}
	// 4. Crash window: exactly at the atomic boundary. The pointer HAS
	//    switched; the switch is complete and valid.
	if m.crash != nil {
		if err := m.crash(CrashAfterPointerCommit); err != nil {
			// The rename already committed; record the committed slot and
			// treat the switch as complete. The post-commit crash is observed,
			// not propagated: the pointer HAS switched.
			m.active = s
			return nil //nolint:nilerr // post-commit crash observation is intentional
		}
	}
	return nil
}

// recoverPointer brings the pointer back to a valid state after any crash or
// external corruption. It returns the resolved active slot. Invariants:
//
//   - An orphan active.tmp (crash during a switch) is discarded — the rename
//     never happened, so the committed pointer is authoritative.
//   - A missing pointer on a FRESH workspace (no slot has any durable data) is
//     a normal bootstrap to slot A and is NOT flagged as recovery.
//   - A missing or corrupt pointer with existing slot data defaults to the
//     most recently updated valid slot, rewrites the pointer atomically, and
//     is flagged as a genuine pointer recovery.
func (m *Manager) recoverPointer() (SlotID, error) {
	// Discard any interrupted-switch staging file.
	_ = os.Remove(filepath.Join(m.dir, activeTmpFile))

	slot, err := m.readPointer()
	if err == nil && validSlot(slot) {
		return slot, nil
	}

	// Missing or corrupt pointer: default deterministically to the slot with
	// the newest valid session record, else slot A.
	chosen := SlotA
	aTime, aOK := m.slotRecordTime(SlotA)
	bTime, bOK := m.slotRecordTime(SlotB)
	switch {
	case aOK && bOK && bTime.After(aTime):
		chosen = SlotB
	case aOK && !bOK:
		chosen = SlotA
	case !aOK && bOK:
		chosen = SlotB
	}

	// A pointer absent on a truly fresh workspace (no durable slot data) is
	// normal first-run bootstrap, not a recovery.
	if !m.slotHasAnyData(SlotA) && !m.slotHasAnyData(SlotB) {
		if err := writeFileAtomic(filepath.Join(m.dir, activeFile), []byte(string(chosen)+"\n")); err != nil {
			return "", err
		}
		return chosen, nil
	}

	if err := writeFileAtomic(filepath.Join(m.dir, activeFile), []byte(string(chosen)+"\n")); err != nil {
		return "", err
	}
	m.pointerRecovered = true
	return chosen, nil
}

// slotHasAnyData reports whether the slot holds any durable artifact
// (authoritative record, compact context, or raw history).
func (m *Manager) slotHasAnyData(s SlotID) bool {
	for _, f := range []string{sessionFile, compactContextFileName, RawHistoryFileName} {
		if _, err := os.Stat(filepath.Join(m.slotDir(s), f)); err == nil {
			return true
		}
	}
	return false
}

// slotRecordTime returns the mtime of the slot's durable session record. The
// boolean is false when the slot has no valid session record.
func (m *Manager) slotRecordTime(s SlotID) (time.Time, bool) {
	fi, err := os.Stat(filepath.Join(m.slotDir(s), sessionFile))
	if err != nil {
		return time.Time{}, false
	}
	return fi.ModTime(), true
}

// writeFileSync writes data to path with O_SYNC + fsync but WITHOUT a rename:
// the path reflects the bytes once the write returns, and callers that need
// atomic visibility must rename the file themselves (see commitPointer).
func writeFileSync(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|os.O_SYNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// atomic writeFileAtomic writes data to path via a same-directory temp file +
// fsync + rename. The temp name is deterministic (path + ".tmp") so crash
// recovery and concurrent readers see a fully-written file at path, never a
// partial one.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := writeFileSync(tmp, data); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	// Best-effort directory fsync so the rename itself is durable.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// ErrPointerGone is returned when no pointer and no session records exist.
var ErrPointerGone = errors.New("session: no active pointer and no recoverable session records")
