package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ── Phase 3 P3 — Optimistic Concurrency Control (OCC) engine ────────────────
//
// The OCC engine is the production pre-commit state verifier of the runtime's
// commit pipeline. It replaces the historical noop placeholder with a
// target-scoped optimistic-concurrency gate:
//
//  1. BASELINE — at admission (Execute) the runtime fingerprints EXACTLY the
//     resolved targets of the execution contract (sha256 content hashes plus a
//     size/mtime fast path). Snapshotting is strictly bounded to the target
//     geometry: no workspace-wide walk ever happens here.
//
//  2. VERIFY — immediately before the commit pipeline applies any mutation or
//     executes any final file write (Approve), the runtime re-validates every
//     baseline target against the live workspace. Any divergence caused by an
//     out-of-band writer (LSP edit, formatter, watcher, parallel tool) fails
//     closed BEFORE a single byte is written.
//
//  3. CLEAN ABORT — a conflict terminates the attempt with the canonical
//     ABORTED_OCC evidence outcome, tainted mutations and zero partial writes:
//     the apply stage never runs, so nothing can leak to disk.
//
// Operational telemetry (check durations, fingerprint cache hits, mismatch
// frequencies) is accumulated race-safely on the verifier.

// ErrWorkspaceStateConflict is the sentinel error of an OCC baseline mismatch.
// Every conflict error returned by the engine wraps this sentinel, so callers
// can classify aborts with errors.Is regardless of the concrete conflicts.
var ErrWorkspaceStateConflict = errors.New("execution: workspace state conflict")

// OCCConflictKind is the taxonomy of a single diverged target.
type OCCConflictKind string

// Canonical OCC conflict kinds.
const (
	// OCCModified: the target exists at baseline and now, but its content hash
	// diverged (an out-of-band writer changed the bytes).
	OCCModified OCCConflictKind = "modified"
	// OCCDeleted: the target existed at baseline but vanished before commit.
	OCCDeleted OCCConflictKind = "deleted"
	// OCCCreated: the target was absent at baseline (a creation intent) but
	// appeared before commit.
	OCCCreated OCCConflictKind = "created"
	// OCCUnreadable: the target could not be stat'ed/read at verify time for a
	// reason other than absence. Verification fails closed.
	OCCUnreadable OCCConflictKind = "unreadable"
)

// OCCConflict is one diverged target of a baseline verification.
type OCCConflict struct {
	// Path is the workspace-relative target that diverged.
	Path string
	// Kind is the deterministic divergence taxonomy.
	Kind OCCConflictKind
	// Detail explains the divergence (hashes, sizes, the underlying error).
	Detail string
}

// String renders the compact "path: kind" identity of the conflict.
func (c OCCConflict) String() string { return c.Path + ": " + string(c.Kind) }

// WorkspaceStateConflict is the aggregate error of one failed OCC
// verification. It carries EVERY diverged target (never just the first), so a
// multi-file abort reports its complete conflict surface.
type WorkspaceStateConflict struct {
	Conflicts []OCCConflict
}

// Error implements error and aggregates all conflicts.
func (e *WorkspaceStateConflict) Error() string {
	parts := make([]string, 0, len(e.Conflicts))
	for _, c := range e.Conflicts {
		if c.Detail != "" {
			parts = append(parts, fmt.Sprintf("%s (%s: %s)", c.Path, c.Kind, c.Detail))
			continue
		}
		parts = append(parts, c.String())
	}
	return ErrWorkspaceStateConflict.Error() + ": " + strconv.Itoa(len(e.Conflicts)) +
		" diverged target(s): " + strings.Join(parts, "; ")
}

// Unwrap exposes the sentinel so errors.Is(err, ErrWorkspaceStateConflict)
// classifies every conflict abort identically.
func (e *WorkspaceStateConflict) Unwrap() error { return ErrWorkspaceStateConflict }

// occFingerprint is the immutable per-target observation of one workspace
// state: the content hash plus the cheap size/mtime pair used as the
// verification fast path.
type occFingerprint struct {
	size    int64
	modNano int64
	hash    string // sha256 hex of the content; "" marks an absent target
}

// absentFingerprint is the canonical fingerprint of a target that does not
// exist on disk.
func absentFingerprint() occFingerprint { return occFingerprint{size: -1} }

// WorkspaceBaseline is the immutable target-scoped snapshot of one execution
// contract's resolved targets, taken at admission time. It is safe for
// concurrent reads after construction.
type WorkspaceBaseline struct {
	targets      []string                  // deduplicated, original order
	fingerprints map[string]occFingerprint // target → observation
	digest       string                    // content-addressed identity of this baseline
	createdAt    time.Time
}

// Targets returns a copy of the baselined target paths (deduplicated,
// first-recorded order).
func (b *WorkspaceBaseline) Targets() []string {
	if b == nil {
		return nil
	}
	out := make([]string, len(b.targets))
	copy(out, b.targets)
	return out
}

// Digest returns the content-addressed identity of the baseline: two baselines
// over identical target states share a digest; ANY divergence forks it.
func (b *WorkspaceBaseline) Digest() string {
	if b == nil {
		return ""
	}
	return b.digest
}

// CreatedAt returns when the baseline was captured.
func (b *WorkspaceBaseline) CreatedAt() time.Time {
	if b == nil {
		return time.Time{}
	}
	return b.createdAt
}

// OCCTelemetry is the operational metrics snapshot of one OCC verifier:
// check durations, fingerprint cache hits and mismatch frequencies.
type OCCTelemetry struct {
	// Snapshots counts completed baseline captures.
	Snapshots int
	// Verifications counts completed pre-commit verifications.
	Verifications int
	// CacheHits counts fingerprint short-circuits: a size+mtime match (or a
	// cached content hash) that avoided a redundant content read.
	CacheHits int
	// Mismatches counts verifications that found at least one conflict.
	Mismatches int
	// ConflictsFound is the cumulative number of diverged targets observed.
	ConflictsFound int
	// SnapshotNanos / VerifyNanos accumulate wall-clock check durations.
	SnapshotNanos int64
	VerifyNanos   int64
}

// SnapshotDuration returns the cumulative baseline-capture duration.
func (t OCCTelemetry) SnapshotDuration() time.Duration { return time.Duration(t.SnapshotNanos) }

// VerifyDuration returns the cumulative pre-commit verification duration.
func (t OCCTelemetry) VerifyDuration() time.Duration { return time.Duration(t.VerifyNanos) }

// OCCVerifier is the production optimistic-concurrency state verifier. It is
// safe for concurrent use; all telemetry accumulates under one lock.
type OCCVerifier struct {
	root string

	mu      sync.Mutex
	cache   map[string]occFingerprint // content-hash cache keyed by absolute path
	metrics OCCTelemetry
	nowFunc func() time.Time // test seam
}

// NewOCCVerifier constructs a verifier scoped to the workspace root.
func NewOCCVerifier(root string) *OCCVerifier {
	return &OCCVerifier{
		root:    root,
		cache:   make(map[string]occFingerprint),
		nowFunc: time.Now,
	}
}

// Metrics returns the current telemetry snapshot.
func (v *OCCVerifier) Metrics() OCCTelemetry {
	if v == nil {
		return OCCTelemetry{}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.metrics
}

// SnapshotBaseline captures a lightweight fingerprint of EXACTLY the declared
// targets — never a workspace scan. A target that does not exist is recorded
// as legitimately absent (a creation intent), so capturing never fails: any
// real divergence is detected at verify time instead. Duplicate targets are
// collapsed; order is preserved for evidence readability.
func (v *OCCVerifier) SnapshotBaseline(targets []string) *WorkspaceBaseline {
	if v == nil {
		return nil
	}
	start := v.nowFunc()

	dedup := make([]string, 0, len(targets))
	seen := make(map[string]bool, len(targets))
	for _, t := range targets {
		t = filepath.ToSlash(filepath.Clean(strings.TrimSpace(t)))
		if t == "" || t == "." || seen[t] {
			continue
		}
		seen[t] = true
		dedup = append(dedup, t)
	}

	fps := make(map[string]occFingerprint, len(dedup))
	hits := 0
	for _, t := range dedup {
		fp, hit := v.fingerprint(t)
		if hit {
			hits++
		}
		fps[t] = fp
	}

	b := &WorkspaceBaseline{
		targets:      dedup,
		fingerprints: fps,
		digest:       baselineDigest(fps),
		createdAt:    v.nowFunc(),
	}

	v.mu.Lock()
	v.metrics.Snapshots++
	v.metrics.CacheHits += hits
	v.metrics.SnapshotNanos += int64(v.nowFunc().Sub(start))
	v.mu.Unlock()

	log.Printf("[occ] baseline request_targets=%d scoped=%d digest=%s cache_hits=%d duration=%s",
		len(targets), len(dedup), shortDigest(b.digest), hits, v.nowFunc().Sub(start).Round(time.Microsecond))
	return b
}

// VerifyAgainst re-validates every baseline target against the LIVE workspace.
// It returns nil when the workspace still matches the baseline exactly, or a
// *WorkspaceStateConflict aggregating every diverged target — the caller must
// then halt before committing any state. A nil baseline verifies trivially
// (no admitted geometry to protect).
func (v *OCCVerifier) VerifyAgainst(b *WorkspaceBaseline) error {
	if v == nil || b == nil || len(b.targets) == 0 {
		return nil
	}
	start := v.nowFunc()

	var conflicts []OCCConflict
	hits := 0
	for _, t := range b.targets {
		base := b.fingerprints[t]
		full := filepath.Join(v.root, filepath.FromSlash(t))

		info, err := os.Stat(full)
		switch {
		case err != nil && !os.IsNotExist(err):
			conflicts = append(conflicts, OCCConflict{Path: t, Kind: OCCUnreadable, Detail: err.Error()})
			continue
		case err != nil: // absent now
			if base.hash != "" {
				conflicts = append(conflicts, OCCConflict{Path: t, Kind: OCCDeleted, Detail: "target existed at baseline"})
			}
			continue
		case base.hash == "":
			conflicts = append(conflicts, OCCConflict{Path: t, Kind: OCCCreated, Detail: "target was absent at baseline"})
			continue
		}
		if info.IsDir() {
			conflicts = append(conflicts, OCCConflict{Path: t, Kind: OCCModified, Detail: "target replaced by a directory"})
			continue
		}

		// Fast path: unchanged size AND mtime prove the ordinary-writer case
		// (any normal write updates mtime) without re-reading the bytes.
		if info.Size() == base.size && info.ModTime().UnixNano() == base.modNano {
			hits++
			continue
		}
		fp, _ := v.fingerprint(t)
		if fp.hash != base.hash {
			conflicts = append(conflicts, OCCConflict{
				Path: t, Kind: OCCModified,
				Detail: fmt.Sprintf("baseline %s… vs current %s…", shortDigest(base.hash), shortDigest(fp.hash)),
			})
		}
	}

	elapsed := v.nowFunc().Sub(start)

	v.mu.Lock()
	v.metrics.Verifications++
	v.metrics.CacheHits += hits
	v.metrics.VerifyNanos += int64(elapsed)
	if len(conflicts) > 0 {
		v.metrics.Mismatches++
		v.metrics.ConflictsFound += len(conflicts)
	}
	v.mu.Unlock()

	log.Printf("[occ] verify targets=%d conflicts=%d cache_hits=%d duration=%s",
		len(b.targets), len(conflicts), hits, elapsed.Round(time.Microsecond))

	if len(conflicts) == 0 {
		return nil
	}
	return &WorkspaceStateConflict{Conflicts: conflicts}
}

// fingerprint observes one target: stat + content hash, using the cached hash
// when size and mtime are unchanged since the last observation. The second
// return reports whether the cached observation was reused (a telemetry cache
// hit).
func (v *OCCVerifier) fingerprint(target string) (occFingerprint, bool) {
	full := filepath.Join(v.root, filepath.FromSlash(target))
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		return absentFingerprint(), false
	}
	v.mu.Lock()
	cached, ok := v.cache[target]
	v.mu.Unlock()
	if ok && cached.size == info.Size() && cached.modNano == info.ModTime().UnixNano() && cached.hash != "" {
		return cached, true
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return absentFingerprint(), false
	}
	sum := sha256.Sum256(data)
	fp := occFingerprint{
		size:    info.Size(),
		modNano: info.ModTime().UnixNano(),
		hash:    hex.EncodeToString(sum[:]),
	}
	v.mu.Lock()
	v.cache[target] = fp
	v.mu.Unlock()
	return fp, false
}

// baselineDigest derives the content-addressed identity of a fingerprint set:
// a deterministic, length-prefixed encoding (injection-proof, same scheme as
// the contract/context encodings) hashed with sha256.
func baselineDigest(fps map[string]occFingerprint) string {
	keys := make([]string, 0, len(fps))
	for k := range fps {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("izen-occ-baseline-v1")
	for _, k := range keys {
		fp := fps[k]
		b.WriteByte(0)
		b.WriteString(strconv.Itoa(len(k)))
		b.WriteByte(':')
		b.WriteString(k)
		b.WriteByte(0)
		b.WriteString(strconv.FormatInt(fp.size, 10))
		b.WriteByte(':')
		b.WriteString(strconv.FormatInt(fp.modNano, 10))
		b.WriteByte(':')
		b.WriteString(fp.hash)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// shortDigest renders the leading edge of a hex digest for compact evidence.
func shortDigest(d string) string {
	if len(d) > 12 {
		return d[:12]
	}
	if d == "" {
		return "(absent)"
	}
	return d
}
