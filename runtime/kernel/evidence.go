package kernel

import (
	"fmt"
	"sort"
	"strings"
)

// EvidenceKind is the closed vocabulary of what a capability can have observed.
//
// The vocabulary is closed on purpose. "Evidence" that cannot be named cannot
// be required, so a contract that demands a fact the vocabulary cannot express
// fails loudly at design time instead of quietly passing at runtime.
type EvidenceKind string

const (
	// EvidenceWorkspaceObserved: the workspace was actually enumerated. This is
	// the observation that distinguishes "the runtime looked" from "the runtime
	// assumed".
	EvidenceWorkspaceObserved EvidenceKind = "WORKSPACE_OBSERVED"
	// EvidenceFileRead: one file's real content was read.
	EvidenceFileRead EvidenceKind = "FILE_READ"
	// EvidenceFileAbsent: a declared target was looked for and was not there.
	// Absence is an observation with its own name; a missing observation is not
	// the same thing and never substitutes for one.
	EvidenceFileAbsent EvidenceKind = "FILE_ABSENT"
	// EvidenceFilePresent: a declared target was looked for and was there.
	EvidenceFilePresent EvidenceKind = "FILE_PRESENT"
	// EvidenceFileWritten: bytes reached a destination. Produced only by a
	// mutating capability that actually wrote.
	EvidenceFileWritten EvidenceKind = "FILE_WRITTEN"
	// EvidenceFileDeleted: a declared target was removed. Produced only by a
	// mutating capability that actually removed it. It is distinct from
	// EvidenceFileAbsent, which says the target was not there: this one says the
	// target WAS there and a mutation is what made it not there.
	EvidenceFileDeleted EvidenceKind = "FILE_DELETED"
	// EvidenceCommandRun: a command ran and its real exit code is recorded.
	EvidenceCommandRun EvidenceKind = "COMMAND_RUN"
	// EvidenceCommandTruncated: a command's output hit the declared bound, so
	// what was observed is an incomplete prefix.
	EvidenceCommandTruncated EvidenceKind = "COMMAND_TRUNCATED"
	// EvidenceResponseProduced: a non-empty response was delivered.
	EvidenceResponseProduced EvidenceKind = "RESPONSE_PRODUCED"
)

// allEvidenceKinds is the canonical ordered vocabulary.
var allEvidenceKinds = []EvidenceKind{
	EvidenceWorkspaceObserved,
	EvidenceFileRead,
	EvidenceFilePresent,
	EvidenceFileAbsent,
	EvidenceFileWritten,
	EvidenceFileDeleted,
	EvidenceCommandRun,
	EvidenceCommandTruncated,
	EvidenceResponseProduced,
}

// AllEvidenceKinds returns the canonical ordered evidence vocabulary.
func AllEvidenceKinds() []EvidenceKind {
	out := make([]EvidenceKind, len(allEvidenceKinds))
	copy(out, allEvidenceKinds)
	return out
}

// Valid reports whether k is a member of the closed vocabulary.
func (k EvidenceKind) Valid() bool {
	for _, known := range allEvidenceKinds {
		if known == k {
			return true
		}
	}
	return false
}

// Mutating reports whether this evidence kind implies the workspace changed. A
// mutating evidence kind produced by a read-only capability is a contradiction
// the kernel refuses to record, which is why the reducer cross-checks it.
//
// A write and a delete are both workspace changes: the destination's content
// after them is not what it was before, and either can satisfy a mutation
// obligation. They are not interchangeable as evidence — a contract that names
// one is not satisfied by the other — which is why they are separate kinds even
// though they share the mutation boundary.
func (k EvidenceKind) Mutating() bool {
	return k == EvidenceFileWritten || k == EvidenceFileDeleted
}

// Observation is what a capability reports immediately after it runs.
//
// It is deliberately narrow. It carries only the facts the capability observed
// and a truthful verdict about whether it actually performed what was asked. It
// carries no conclusion, no assessment of the objective, and no claim about
// anything the capability did not touch — those belong to the kernel, derived
// from evidence rather than reported alongside it.
type Observation struct {
	// Facts are the observations themselves, in the order they were made.
	Facts []Fact
	// Verdict is the capability's truthful report of what it did.
	Verdict Verdict
	// OutputBytes is how many bytes of output this invocation consumed. It is
	// declared by the capability that actually received them, and is charged
	// against the budget.
	OutputBytes int
	// Detail is an optional one-line description for a human reader. It carries
	// no authority: no transition reads it.
	Detail string
}

// Fact is one observed thing.
type Fact struct {
	// Kind is the closed-vocabulary classification of the observation.
	Kind EvidenceKind
	// Target is the concrete workspace-relative path the observation is about,
	// or "" for a workspace-wide observation.
	Target string
	// Bytes is how many bytes the observation concerns.
	Bytes int
	// ExitCode is the real exit code for a command observation.
	ExitCode int
	// Summary is an optional one-line description for a human reader.
	Summary string
}

// Verdict is a capability's truthful report of what it actually did. It is the
// direct replacement for a bare error or a bare success flag.
//
// The distinction that matters most is NotApplicable versus Pass: a gate that
// was skipped must never be rendered as one that passed, and only the kernel may
// decide which of the two a given contract tolerates.
type Verdict string

const (
	// VerdictPass: the capability did exactly what was asked and observed the
	// result.
	VerdictPass Verdict = "PASS"
	// VerdictFail: the capability attempted the work and it did not succeed.
	VerdictFail Verdict = "FAIL"
	// VerdictNotApplicable: the capability did not apply, and provably need not.
	// Recorded explicitly so it is never confused with a pass.
	VerdictNotApplicable Verdict = "NOT_APPLICABLE"
	// VerdictNoOp: the capability ran, found the requested state already held,
	// and changed nothing. Distinct from Fail: the objective may already be
	// satisfied, and this is the only evidence that can show that.
	VerdictNoOp Verdict = "NO_OP"
	// VerdictUnknown: the capability could not determine its own outcome.
	// A capability must never report Pass in this situation; an unknown
	// outcome is not a success.
	VerdictUnknown Verdict = "UNKNOWN"
)

// Valid reports whether v is a member of the closed vocabulary.
func (v Verdict) Valid() bool {
	switch v {
	case VerdictPass, VerdictFail, VerdictNotApplicable, VerdictNoOp, VerdictUnknown:
		return true
	default:
		return false
	}
}

// Succeeded reports whether the verdict means the capability did its work.
// VerdictUnknown deliberately does not: an undetermined outcome is not a
// success, and treating it as one is how unsubstantiated claims enter a runtime.
func (v Verdict) Succeeded() bool {
	return v == VerdictPass || v == VerdictNoOp
}

// String returns the raw verdict label.
func (v Verdict) String() string { return string(v) }

// Evidence is one durable, attributed observation in the execution's evidence
// log.
//
// It is a record of something that happened, stamped with the identity of the
// step that caused it. It carries no verdict about the objective: the kernel
// derives those from the evidence set, so a capability can add evidence but can
// never author a conclusion.
//
// The JSON tags are part of the durable contract. An evidence log is written once
// and read by readers that were not present when it was written, so these field
// names must not change casually — a rename here silently invalidates every
// archived execution.
type Evidence struct {
	// ID is the evidence identifier, unique within the execution.
	ID string `json:"id"`
	// Kind is the observation's classification.
	Kind EvidenceKind `json:"kind"`
	// Step is the identifier of the step that produced this evidence.
	Step string `json:"step"`
	// Capability is the capability that produced it.
	Capability CapabilityID `json:"capability"`
	// Target is the concrete path the observation concerns.
	Target string `json:"target,omitempty"`
	// Bytes is how many bytes the observation concerns.
	Bytes int `json:"bytes,omitempty"`
	// ExitCode is the real exit code for a command observation.
	ExitCode int `json:"exit_code,omitempty"`
	// Verdict is the producing capability's truthful report.
	Verdict Verdict `json:"verdict"`
	// Revision is the state revision at which this evidence entered the log. It
	// makes replay order reconstructible.
	Revision uint64 `json:"revision"`
	// Detail is the optional human-facing line.
	Detail string `json:"detail,omitempty"`
}

// observation converts an Observation plus its provenance into Evidence
// records. It is the only place capabilities become evidence, which is what
// guarantees every evidence record names a real step.
func observation(idSeq func() string, revision uint64, step Step, obs Observation) []Evidence {
	out := make([]Evidence, 0, len(obs.Facts))
	for _, f := range obs.Facts {
		out = append(out, Evidence{
			ID:         idSeq(),
			Kind:       f.Kind,
			Step:       step.ID,
			Capability: step.Capability,
			Target:     f.Target,
			Bytes:      f.Bytes,
			ExitCode:   f.ExitCode,
			Verdict:    obs.Verdict,
			Revision:   revision,
			Detail:     firstNonEmpty(f.Summary, obs.Detail),
		})
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// evidenceSet is the kernel's indexed view over the evidence log. It answers the
// questions the contract asks without letting a caller scan the log itself, so
// there is exactly one definition of "was this observed".
type evidenceSet struct {
	byKind map[EvidenceKind][]Evidence
	byStep map[string][]Evidence
	all    []Evidence
}

func newEvidenceSet() *evidenceSet {
	return &evidenceSet{
		byKind: make(map[EvidenceKind][]Evidence),
		byStep: make(map[string][]Evidence),
	}
}

func (s *evidenceSet) add(e Evidence) {
	if s == nil {
		return
	}
	s.all = append(s.all, e)
	s.byKind[e.Kind] = append(s.byKind[e.Kind], e)
	s.byStep[e.Step] = append(s.byStep[e.Step], e)
}

// has reports whether any evidence of the given kind was recorded.
func (s *evidenceSet) has(kind EvidenceKind) bool {
	return s != nil && len(s.byKind[kind]) > 0
}

// observed reports whether the given target was the subject of an observation of
// the given kind. It is the target-scoped question the contract actually asks,
// and it refuses to fall back on a workspace-wide observation: "the workspace
// exists" is not "this file exists".
func (s *evidenceSet) observed(kind EvidenceKind, target string) bool {
	if s == nil || target == "" {
		return false
	}
	for _, e := range s.byKind[kind] {
		if e.Target == target {
			return true
		}
	}
	return false
}

// observedAnywhere reports whether the target was observed under any of the
// given kinds.
func (s *evidenceSet) observedAnywhere(target string, kinds ...EvidenceKind) bool {
	for _, k := range kinds {
		if s.observed(k, target) {
			return true
		}
	}
	return false
}

// mutatedTargets returns the concrete targets the log proves were durably
// changed, in canonical sorted order.
//
// A write and a delete both count: each is a durable change to the target's
// content, and the mutation boundary is "the workspace changed", not "bytes were
// added". The kinds stay separate on the evidence record so a contract that
// names a write is never satisfied by a delete.
func (s *evidenceSet) mutatedTargets() []string {
	if s == nil {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	for _, e := range s.all {
		if !e.Kind.Mutating() || e.Target == "" || seen[e.Target] {
			continue
		}
		seen[e.Target] = true
		out = append(out, e.Target)
	}
	sort.Strings(out)
	return out
}

// presentTargets returns the targets the log proves are present.
func (s *evidenceSet) presentTargets() map[string]bool {
	out := make(map[string]bool)
	if s == nil {
		return out
	}
	for _, e := range s.byKind[EvidenceFilePresent] {
		if e.Target != "" {
			out[e.Target] = true
		}
	}
	for _, e := range s.byKind[EvidenceFileRead] {
		if e.Target != "" {
			out[e.Target] = true
		}
	}
	return out
}

// absentTargets returns the targets the log proves are absent.
func (s *evidenceSet) absentTargets() map[string]bool {
	out := make(map[string]bool)
	if s == nil {
		return out
	}
	for _, e := range s.byKind[EvidenceFileAbsent] {
		if e.Target != "" {
			out[e.Target] = true
		}
	}
	return out
}

// observedTargets returns every target the log has any observation about.
func (s *evidenceSet) observedTargets() []string {
	seen := make(map[string]bool)
	var out []string
	if s != nil {
		for _, e := range s.all {
			if e.Target != "" && !seen[e.Target] {
				seen[e.Target] = true
				out = append(out, e.Target)
			}
		}
	}
	sort.Strings(out)
	return out
}

// String renders the evidence log for a terminal summary.
func (s *evidenceSet) String() string {
	if s == nil || len(s.all) == 0 {
		return "no evidence"
	}
	counts := make(map[EvidenceKind]int, len(s.byKind))
	order := make([]EvidenceKind, 0, len(s.byKind))
	for _, k := range allEvidenceKinds {
		if n := len(s.byKind[k]); n > 0 {
			counts[k] = n
			order = append(order, k)
		}
	}
	parts := make([]string, 0, len(order))
	for _, k := range order {
		parts = append(parts, fmt.Sprintf("%s=%d", k, counts[k]))
	}
	return strings.Join(parts, " ")
}
