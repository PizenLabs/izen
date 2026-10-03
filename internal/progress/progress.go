// Package progress measures whether the runtime is actually getting anywhere.
//
// WHY runtime state and not repair counts — the tempting metric is "how many
// files did I change?" or "how many repair attempts did I make?", and it is a
// lie. A repair attempt is an event, not an outcome: a model can rewrite the
// same file twelve times, churn the workspace into a worse state, and still
// satisfy nothing. Worse, a *successful* repair is not evidence that the
// objective advanced — a mutated file that does not satisfy the requirement
// satisfies no one. The spec's rule (§33, §36) is that "insufficient evidence
// must never automatically become a defect" and that a loop must not keep
// spending rounds once it has stopped learning. Both of those decisions can
// only be made from observable runtime state: which requirements are
// satisfied, which remain unmet, and *which* failures are still open. That is
// exactly what Snapshot records, and why Fingerprint exists — an open failure
// with the same fingerprint in round N and round N+3 is a loop, not progress.
//
// This package is pure: no I/O, no clock, no model calls. Two runs that reach
// the same runtime state must reach the same verdict, or the termination
// decision is not trustworthy at all.
package progress

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/internal/execution/capability"
)

// ── Fingerprint ──────────────────────────────────────────────────────────

// Fingerprint is the stable identity of one failure: WHO observed it, WHAT
// class it is, and WHERE it happened. Two failures with equal fingerprints
// are the same failure observed twice, regardless of prose wording, and that
// is the only thing that distinguishes a stuck loop from a hard problem.
type Fingerprint struct {
	// Verifier is which observation or verification pass produced this.
	Verifier string
	// ErrorClass is the capability.FailureClass value. It is part of the
	// identity because the same location failing two different classes is
	// two different problems (e.g. EXECUTION_FAILED vs DIAGNOSIS_UNCERTAIN).
	ErrorClass string
	// Location is the entry point, resource, or step, when known.
	Location string
	// Artifact is the workspace-relative artifact the failure points at.
	Artifact string
	// Symbol is an optional symbol or selector, when known.
	Symbol string
}

// signature renders the canonical form hashed into Key.
//
// Canonicalization rules, and why each exists:
//   - fields joined with "\x00", a byte that cannot appear in an identifier
//     or path component, so ("ab","c") and ("a","bc") cannot collide;
//   - optional fields that are empty are dropped from the tail entirely, so a
//     failure recorded before/after a tool learned to populate Symbol maps to
//     the SAME key — otherwise the loop would read "the fingerprint changed"
//     and mistake bookkeeping noise for progress;
//   - remaining values are trimmed, so trailing whitespace in a served path
//     does not fork the identity.
func (f Fingerprint) signature() string {
	fields := []string{f.Verifier, f.ErrorClass, f.Location, f.Artifact, f.Symbol}
	out := make([]string, 0, len(fields))
	for _, v := range fields {
		out = append(out, strings.TrimSpace(v))
	}
	// ── trailing zero-valued optional fields are not part of the identity ──
	n := len(out)
	for n > 0 && out[n-1] == "" {
		n--
	}
	return strings.Join(out[:n], "\x00")
}

// Key returns the stable hex digest of the fingerprint. Equal keys mean the
// same failure; the digest is what gets compared across rounds.
func (f Fingerprint) Key() string {
	sum := sha256.Sum256([]byte(f.signature()))
	return hex.EncodeToString(sum[:])
}

// Empty reports whether the fingerprint carries no identity at all. An empty
// fingerprint must never be reported as an open failure: there is nothing to
// iterate on, and treating it as one would manufacture a no-progress loop out
// of missing evidence.
func (f Fingerprint) Empty() bool {
	return f.signature() == ""
}

// Fingerprints converts open defects into deduplicated, key-sorted
// fingerprints. Sorting makes the resulting slice directly comparable across
// rounds: two rounds observing the same defects in different probe order
// produce identical slices, and therefore identical snapshots.
func Fingerprints(defects []capability.Defect) []Fingerprint {
	if len(defects) == 0 {
		return nil
	}
	out := make([]Fingerprint, 0, len(defects))
	seen := make(map[string]struct{}, len(defects))
	for _, d := range defects {
		f := Fingerprint{
			Verifier:   d.Code,
			ErrorClass: string(d.Class),
			Location:   d.Entry,
		}
		if f.Empty() {
			continue
		}
		k := f.Key()
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, f)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}

// ── Snapshot ─────────────────────────────────────────────────────────────

// Snapshot is one comparable point of runtime progress.
type Snapshot struct {
	// Round is the behavior-loop round this snapshot closes.
	Round int
	// Satisfied counts observed requirements that hold.
	Satisfied int
	// Unmet counts observed requirements that do not hold.
	Unmet int
	// FingerprintKeys are the sorted keys of every open defect.
	FingerprintKeys []string
	// EvidenceDigest identifies the evidence this snapshot was derived from.
	EvidenceDigest string
	// Mutations counts cumulative runtime-applied mutations so far.
	Mutations int
}

// same compares the parts of two snapshots that constitute runtime state.
// Round is deliberately excluded: it is a counter, not state, and including
// it would make every round differ and silently defeat the detector.
func (s Snapshot) same(o Snapshot) bool {
	if s.Satisfied != o.Satisfied || s.Unmet != o.Unmet {
		return false
	}
	if s.EvidenceDigest != o.EvidenceDigest {
		return false
	}
	if len(s.FingerprintKeys) != len(o.FingerprintKeys) {
		return false
	}
	for i := range s.FingerprintKeys {
		if s.FingerprintKeys[i] != o.FingerprintKeys[i] {
			return false
		}
	}
	return true
}

// ── Detector ─────────────────────────────────────────────────────────────

// Verdict is the classification of progress between two snapshots.
type Verdict string

const (
	// VerdictUnknown: too few observations to decide yet.
	VerdictUnknown Verdict = "UNKNOWN"
	// VerdictProgress: runtime state moved forward.
	VerdictProgress Verdict = "PROGRESS"
	// VerdictNoProgress: the loop is stuck and must stop (spec §36:
	// NO_PROGRESS / HUMAN_REQUIRED).
	VerdictNoProgress Verdict = "NO_PROGRESS"
)

// Detector classifies progress between successive snapshots.
//
// It is stateful on purpose: "no progress" is not a property of one
// snapshot, it is a property of a *run* of identical snapshots. A single
// quiet round after a fresh observation is normal; three in a row with the
// same fingerprints is a loop.
type Detector struct {
	threshold int
	last      Snapshot
	have      bool
	stagnant  int
	reason    string
}

// NewDetector returns a Detector that reports NO_PROGRESS once the stagnant
// counter reaches stagnantThreshold. A non-positive threshold is normalized to
// 2: 1 would fire on the very first observation and stop every loop before it
// could react to anything, which is the same failure as no limit at all.
func NewDetector(stagnantThreshold int) *Detector {
	if stagnantThreshold <= 0 {
		stagnantThreshold = 2
	}
	return &Detector{threshold: stagnantThreshold}
}

// Observe folds one snapshot into the detector and classifies it.
func (d *Detector) Observe(s Snapshot) Verdict {
	mutated := d.have && s.Mutations > d.last.Mutations
	if !d.have {
		// ── first observation: baseline, never a verdict of stagnation ──
		d.last = s
		d.have = true
		d.stagnant = 1
		d.reason = ""
		return VerdictUnknown
	}
	if mutated || !s.same(d.last) {
		d.last = s
		d.stagnant = 0
		d.reason = ""
		return VerdictProgress
	}
	d.stagnant++
	d.last = s
	if d.stagnant >= d.threshold {
		d.reason = d.buildReason()
		return VerdictNoProgress
	}
	d.reason = ""
	return VerdictUnknown
}

// buildReason names the fingerprints that kept recurring, bounded so the
// string stays safe for a one-line runtime reason field.
func (d *Detector) buildReason() string {
	const maxKeys = 3
	keys := d.last.FingerprintKeys
	shown := keys
	suffix := ""
	if len(keys) > maxKeys {
		shown = keys[:maxKeys]
		suffix = ",…"
	}
	if len(shown) == 0 {
		return "no progress: runtime state unchanged and no mutation applied"
	}
	return "no progress: repeated " + strings.Join(shown, ",") + suffix
}

// StagnantRounds returns how many consecutive identical observations the
// detector has seen.
func (d *Detector) StagnantRounds() int { return d.stagnant }

// Last returns the most recent snapshot observed.
func (d *Detector) Last() Snapshot { return d.last }

// Reason returns the bounded one-line explanation for a NO_PROGRESS verdict,
// and the empty string otherwise — a reason attached to a progress verdict
// is itself misleading to a reader.
func (d *Detector) Reason() string { return d.reason }

// Reset clears all detector state. Used when the objective or workspace is
// replaced, because a counter carried across two different objectives would
// stop a loop that has barely started.
func (d *Detector) Reset() {
	d.last = Snapshot{}
	d.have = false
	d.stagnant = 0
	d.reason = ""
}
