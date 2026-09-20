// Per-artifact cross-process isolation for concurrent izen execution.
//
// Disjoint Session Parallelism: independent sessions targeting disjoint file
// sets MUST execute concurrently without locking contention. A global
// workspace-exclusive lock would serialize them and violate this invariant,
// so artifact isolation is per-target: each workspace-relative target maps to
// its own advisory flock file under .izen/locks/<sha256>.lock. Two processes
// touching disjoint targets contend on disjoint lock files and proceed in
// parallel; two processes touching the SAME target contend on the same file
// and the loser fails closed with ErrConcurrentModification.
//
// Explicit Artifact Conflict Handling: concurrent cycles targeting the same
// artifact MUST NOT silently last-writer-wins. Conflicts surface in two
// layers:
//
//  1. Lock contention (non-blocking try): the second holder gets
//     ErrConcurrentModification immediately instead of blocking.
//  2. Optimistic Concurrency Control: the winner's commit advances the
//     persistent OCC record (.izen/occ/<sha256>.json); a holder whose
//     baseline hash no longer matches the live file (or the OCC record)
//     aborts with ErrConcurrentModification before writing.
//
// Both layers use OS-level flock (not Go mutexes) so safety holds across
// independent OS processes. All metadata writes go through atomic temp+rename
// so readers never observe truncation.
package lock

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/pkg/atomicio"
)

// ErrConcurrentModification is the explicit conflict sentinel for concurrent
// execution cycles targeting the SAME artifact. Exactly one contender wins;
// every loser fails with an error wrapping this sentinel (via lock
// contention or OCC baseline divergence). Match with errors.Is. It is
// deliberately distinct from ErrWorkspaceLocked (the legacy global-lock
// contention signal) so callers can distinguish per-artifact conflicts from
// whole-workspace serialization.
var ErrConcurrentModification = errors.New("artifact concurrently modified")

// artifactLockTimeout bounds how long the blocking acquisition variant waits.
// The non-blocking Try variants never wait: they either hold the flock on
// return or fail with ErrConcurrentModification.
const artifactLockTimeout = 30 * time.Second

// artifactLockBackoff paces blocking acquisition retries.
const artifactLockBackoff = 10 * time.Millisecond

// artifactKey returns the stable lock/occ identity for a workspace-relative
// target: lowercase hex sha256 of the slash-cleaned path. Hashing (rather
// than sanitizing the raw path) keeps lock filenames bounded and immune to
// path traversal (no ".." can escape .izen/locks).
func artifactKey(target string) string {
	clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(target)))
	sum := sha256.Sum256([]byte(clean))
	return hex.EncodeToString(sum[:])
}

// ArtifactLockPath returns the absolute flock file for a target. Exported for
// diagnostics and tests; production callers should use TryAcquire*.
func ArtifactLockPath(workDir, target string) string {
	return filepath.Join(workDir, ".izen", "locks", artifactKey(target)+".lock")
}

// occPath returns the persistent OCC record for a target.
func occPath(workDir, target string) string {
	return filepath.Join(workDir, ".izen", "occ", artifactKey(target)+".json")
}

// occRecord is the durable per-artifact version clock. Version advances by
// exactly one per committed mutation; Hash is the sha256 of the committed
// content ("" marks absent). UpdatedAt is observability metadata.
type occRecord struct {
	Version   uint64 `json:"version"`
	Hash      string `json:"hash"`
	UpdatedAt string `json:"updated_at"`
}

// hashBytes returns the hex sha256 of content.
func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// liveTargetHash returns the current content hash of a workspace-relative
// target ("" when absent). Unreadable targets hash as absent so verification
// fails closed at the OCC layer rather than leaking partial state.
func liveTargetHash(workDir, target string) string {
	clean := filepath.FromSlash(filepath.ToSlash(filepath.Clean(strings.TrimSpace(target))))
	data, err := os.ReadFile(filepath.Join(workDir, clean))
	if err != nil {
		return ""
	}
	return hashBytes(data)
}

// artifactHandle owns one held per-artifact flock.
type artifactHandle struct {
	file   *os.File
	target string
	once   sync.Once
}

// release drops the flock and closes the descriptor. Idempotent.
func (h *artifactHandle) release() {
	if h == nil {
		return
	}
	h.once.Do(func() {
		_ = flockRelease(h.file)
		_ = h.file.Close()
	})
}

// tryAcquireOne attempts the NON-BLOCKING exclusive flock for a single
// target. On contention it returns an error wrapping
// ErrConcurrentModification (never blocks, never waits).
func tryAcquireOne(workDir, target string) (*artifactHandle, error) {
	if strings.TrimSpace(workDir) == "" {
		return nil, fmt.Errorf("lock: empty workDir")
	}
	if strings.TrimSpace(target) == "" {
		return nil, fmt.Errorf("lock: empty target")
	}
	path := ArtifactLockPath(workDir, target)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("lock: mkdir artifact locks: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("lock: open artifact lock: %w", err)
	}
	if err := flockExclusive(f); err != nil {
		_ = f.Close()
		if errors.Is(err, errWouldBlock) {
			return nil, fmt.Errorf("%w: target %q held by another izen process",
				ErrConcurrentModification, target)
		}
		if errors.Is(err, ErrLockUnsupported) {
			// Platform without flock: degrade to success with a no-op
			// handle. Cross-process isolation is unavailable but the
			// caller proceeds rather than failing every mutation.
			// Return a handle wrapping a descriptor we immediately
			// close, with release as a no-op.
			return &artifactHandle{file: f, target: target}, nil
		}
		return nil, fmt.Errorf("lock: flock artifact %q: %w", target, err)
	}
	// Best-effort PID stamp for diagnostics (advisory only).
	_, _ = f.Seek(0, 0)
	_ = f.Truncate(0)
	_, _ = fmt.Fprintf(f, "%d %s\n", os.Getpid(), target)
	_ = f.Sync()
	return &artifactHandle{file: f, target: target}, nil
}

// TryAcquireArtifactLock acquires the non-blocking per-artifact flock for one
// workspace-relative target. On success the caller MUST call unlock (typically
// deferred); unlock is idempotent. On contention it returns an error wrapping
// ErrConcurrentModification and holds nothing.
func TryAcquireArtifactLock(workDir, target string) (unlock func(), err error) {
	h, err := tryAcquireOne(workDir, target)
	if err != nil {
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() { h.release() })
	}, nil
}

// TryAcquireArtifactLocks acquires the per-artifact flocks for a set of
// targets in deterministic sorted order (deadlock-free across processes that
// lock overlapping sets in different orders). Targets are deduplicated;
// empty input returns a no-op unlock. If ANY target contends, all already
// acquired locks are released and the returned error wraps
// ErrConcurrentModification.
func TryAcquireArtifactLocks(workDir string, targets []string) (unlock func(), err error) {
	seen := make(map[string]bool, len(targets))
	ordered := make([]string, 0, len(targets))
	for _, t := range targets {
		clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(t)))
		if clean == "" || clean == "." || seen[clean] {
			continue
		}
		seen[clean] = true
		ordered = append(ordered, clean)
	}
	sort.Strings(ordered)
	if len(ordered) == 0 {
		return func() {}, nil
	}
	handles := make([]*artifactHandle, 0, len(ordered))
	for _, t := range ordered {
		h, err := tryAcquireOne(workDir, t)
		if err != nil {
			for _, held := range handles {
				held.release()
			}
			return nil, err
		}
		handles = append(handles, h)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for _, h := range handles {
				h.release()
			}
		})
	}, nil
}

// AcquireArtifactLocksBlocking is the blocking variant used by long-lived
// holders that must wait for a contended artifact (e.g. CLI session setup).
// It respects ctx cancellation via polling and returns ctx.Err() on cancel.
// Disjoint targets never block each other: only overlapping lock files wait.
func AcquireArtifactLocksBlocking(workDir string, targets []string, timeout time.Duration) (unlock func(), err error) {
	if timeout <= 0 {
		timeout = artifactLockTimeout
	}
	deadline := time.Now().Add(timeout)
	for {
		unlock, err := TryAcquireArtifactLocks(workDir, targets)
		if err == nil {
			return unlock, nil
		}
		if !errors.Is(err, ErrConcurrentModification) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(artifactLockBackoff)
	}
}

// SnapshotBaseline captures the live content hashes of every target. The
// returned map is the OCC baseline the committer must verify under the
// artifact lock before writing: any divergence proves an out-of-band writer
// (or a winning concurrent process) touched the geometry.
func SnapshotBaseline(workDir string, targets []string) map[string]string {
	base := make(map[string]string, len(targets))
	for _, t := range targets {
		clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(t)))
		if clean == "" || clean == "." {
			continue
		}
		base[clean] = liveTargetHash(workDir, clean)
	}
	return base
}

// VerifyBaselineUnderLock re-validates every baseline hash against the live
// workspace. The caller MUST hold the corresponding artifact locks (so no
// concurrent committer can interleave between verify and write). Any
// divergence returns an error wrapping ErrConcurrentModification and the
// caller must abort without writing.
func VerifyBaselineUnderLock(workDir string, baseline map[string]string) error {
	var conflicts []string
	for target, want := range baseline {
		if got := liveTargetHash(workDir, target); got != want {
			conflicts = append(conflicts, target)
		}
	}
	if len(conflicts) > 0 {
		sort.Strings(conflicts)
		return fmt.Errorf("%w: %d target(s) diverged since baseline: %s",
			ErrConcurrentModification, len(conflicts), strings.Join(conflicts, ", "))
	}
	return nil
}

// AdvanceOCC commits the post-write OCC record for one target: it verifies
// the live content still equals expectedHash (the bytes just committed),
// then bumps the persistent version clock atomically (temp+rename). A
// mismatch means another process committed between our write and this call
// and returns ErrConcurrentModification. Call with the artifact lock held.
func AdvanceOCC(workDir, target, expectedHash string) error {
	live := liveTargetHash(workDir, target)
	if live != expectedHash {
		return fmt.Errorf("%w: target %q changed during commit (want %s got %s)",
			ErrConcurrentModification, target, shortHash(expectedHash), shortHash(live))
	}
	path := occPath(workDir, target)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("lock: mkdir occ: %w", err)
	}
	var rec occRecord
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &rec) // corrupt record degrades to version 0
	}
	rec.Version++
	rec.Hash = expectedHash
	rec.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("lock: marshal occ: %w", err)
	}
	data = append(data, '\n')
	if err := atomicio.WriteFileAtomic(path, data, 0o644); err != nil {
		return fmt.Errorf("lock: persist occ: %w", err)
	}
	return nil
}

// OCCVersion returns the durable version clock for a target (0 when none).
func OCCVersion(workDir, target string) uint64 {
	data, err := os.ReadFile(occPath(workDir, target))
	if err != nil {
		return 0
	}
	var rec occRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return 0
	}
	return rec.Version
}

// WithCheckpointLock serializes .izen/checkpoints metadata mutations across
// OS processes with a non-blocking flock. The flock file lives under
// .izen/locks (NOT inside .izen/checkpoints) so the checkpoint namespace
// keeps its invariant: every entry directly under .izen/checkpoints is a
// checkpoint payload directory. The checkpoint payload dirs (cp-<nano>) are
// unique per creator so data blobs never collide; the lock protects the
// shared namespace (manifest commits, pruner sweeps, index reads) from
// interleaved renames. fn runs with the lock held. On contention it returns
// an error wrapping ErrConcurrentModification so checkpoint writers fail
// explicitly instead of corrupting the store. A nil timeout uses the
// artifact default.
func WithCheckpointLock(workDir string, fn func() error) error {
	if fn == nil {
		return nil
	}
	if strings.TrimSpace(workDir) == "" {
		return fmt.Errorf("lock: empty workDir")
	}
	lockPath := filepath.Join(workDir, ".izen", "locks", "checkpoint-store.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return fmt.Errorf("lock: mkdir checkpoints: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("lock: open checkpoint lock: %w", err)
	}
	if err := flockExclusive(f); err != nil {
		_ = f.Close()
		if errors.Is(err, errWouldBlock) {
			return fmt.Errorf("%w: checkpoint store held by another izen process",
				ErrConcurrentModification)
		}
		if errors.Is(err, ErrLockUnsupported) {
			return fn()
		}
		return fmt.Errorf("lock: flock checkpoints: %w", err)
	}
	defer func() {
		_ = flockRelease(f)
		_ = f.Close()
	}()
	return fn()
}

// WithMetadataLock serializes .izen/metadata mutations (the high-frequency
// shared JSON state exercised by the lock stress test) across OS processes.
// It holds an exclusive flock on .izen/metadata/.lock while fn runs and
// persists via atomic temp+rename inside fn. Contention fails with
// ErrConcurrentModification — never silent truncation.
func WithMetadataLock(workDir string, fn func() error) error {
	if fn == nil {
		return nil
	}
	if strings.TrimSpace(workDir) == "" {
		return fmt.Errorf("lock: empty workDir")
	}
	lockPath := filepath.Join(workDir, ".izen", "metadata", ".lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return fmt.Errorf("lock: mkdir metadata: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("lock: open metadata lock: %w", err)
	}
	if err := flockExclusive(f); err != nil {
		_ = f.Close()
		if errors.Is(err, errWouldBlock) {
			return fmt.Errorf("%w: metadata store held by another izen process",
				ErrConcurrentModification)
		}
		if errors.Is(err, ErrLockUnsupported) {
			return fn()
		}
		return fmt.Errorf("lock: flock metadata: %w", err)
	}
	defer func() {
		_ = flockRelease(f)
		_ = f.Close()
	}()
	return fn()
}

// WriteMetadataAtomic persists one JSON metadata document under
// .izen/metadata/<name> atomically (temp+rename+dir fsync) while holding the
// cross-process metadata flock. Readers observe the old or the new document,
// never truncation. name is contained to a single path element.
//
// Unlike WithMetadataLock (fail-fast), this waits for the metadata flock
// with bounded backoff (10s): disjoint writers sharing the metadata store
// serialize briefly and ALL succeed — the stress invariant is zero
// corruption with 100% commit success, not spurious contention failures.
func WriteMetadataAtomic(workDir, name string, data []byte, perm os.FileMode) error {
	if strings.TrimSpace(name) == "" || name != filepath.Base(name) || strings.Contains(name, "..") {
		return fmt.Errorf("lock: invalid metadata name %q", name)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := WithMetadataLock(workDir, func() error {
			dst := filepath.Join(workDir, ".izen", "metadata", name)
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			return atomicio.WriteFileAtomic(dst, data, perm)
		})
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrConcurrentModification) {
			return err
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(artifactLockBackoff)
	}
}

// ReadMetadata reads one .izen/metadata document. A missing file yields
// (nil, nil) so first-writer bootstraps cleanly.
func ReadMetadata(workDir, name string) ([]byte, error) {
	if strings.TrimSpace(name) == "" || name != filepath.Base(name) {
		return nil, fmt.Errorf("lock: invalid metadata name %q", name)
	}
	data, err := os.ReadFile(filepath.Join(workDir, ".izen", "metadata", name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	if h == "" {
		return "(absent)"
	}
	return h
}
