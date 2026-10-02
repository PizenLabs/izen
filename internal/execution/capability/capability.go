// Package capability is the PRIMITIVE layer of IZEN's executable capability
// substrate: real operations against the live workspace and the live runtime,
// each returning structured evidence that names what it actually observed.
//
// It owns NO authority. Nothing here decides what may run, what a mutation
// target is, whether an objective is proven, or whether an attempt should
// continue. Those decisions live in exactly two places, both pre-existing:
//
//   - internal/execution (RuntimeExecutor) is the single execution authority:
//     it derives the Grant below from the request's scope provenance and
//     capability set, and it is the only component that may mutate.
//   - internal/runtime/autonomy (Driver) is the single bounded continuation
//     engine: it sequences capabilities for one objective and stops.
//
// The split matters. A capability that could grant itself authority would make
// "the model asked for it" indistinguishable from "the Control Plane allowed
// it", which is the exact confusion this package exists to prevent.
//
// Every function here is deterministic given its inputs, side-effecting only
// where the capability's whole purpose is a side effect (serving a process,
// issuing an HTTP request), and always returns Evidence describing what
// happened — including when it failed. A capability that cannot observe cannot
// report, and a capability that cannot report is not usable by a runtime that
// must tell the truth.
package capability

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ID is the closed vocabulary of executable capability identifiers. It is
// deliberately small and total: a runtime that cannot name a capability cannot
// authorize it, and an identifier outside this set is a programming error, not
// a runtime condition.
type ID string

const (
	// WorkspaceDiscover enumerates the workspace and derives its shape — stack,
	// manifests, entry document, runnable target — from what is actually on
	// disk. Read-only.
	WorkspaceDiscover ID = "workspace.discover"
	// FileRead reads one bounded region of one workspace file. Read-only.
	FileRead ID = "file.read"
	// FileSearch locates literal matches across workspace text files.
	// Read-only.
	FileSearch ID = "file.search"
	// RuntimeServe starts the workspace's discovered runnable target and waits
	// until it actually answers. Process-spawning.
	RuntimeServe ID = "runtime.serve"
	// RuntimeFetch issues one HTTP request and reports the real response.
	// Network egress.
	RuntimeFetch ID = "runtime.fetch"
	// RuntimeInspect loads an entry document over HTTP, resolves every local
	// subresource it references, probes each one, and audits the served
	// document's structure. Network egress, read-only.
	RuntimeInspect ID = "runtime.inspect"
	// CommandRun executes one command under the caller's authorization and
	// returns its real stdout, stderr and exit code. Process-spawning.
	CommandRun ID = "command.run"
)

// AllIDs is the canonical ordered capability vocabulary. Order is stable so
// rendered catalogs are reproducible across runs and machines.
var AllIDs = []ID{
	WorkspaceDiscover,
	FileRead,
	FileSearch,
	RuntimeServe,
	RuntimeFetch,
	RuntimeInspect,
	CommandRun,
}

// Valid reports whether id is a member of the closed vocabulary.
func (id ID) Valid() bool {
	for _, known := range AllIDs {
		if known == id {
			return true
		}
	}
	return false
}

// String returns the raw identifier.
func (id ID) String() string { return string(id) }

// FailureClass is the truthful failure taxonomy: the closed vocabulary every
// blocked or failed outcome names exactly one member of. Collapsing them into
// "failed" is what makes a runtime impossible to debug and impossible to
// trust.
//
// It deliberately covers more than capability failures. Alongside what a
// capability could not do, the runtime's own control plane must be able to
// name the CONTROL-PLANE terminal outcomes it decides for itself:
//
//   - FailureNoProgress: repeated rounds left the runtime state unchanged,
//     so continuing is no longer honest work. Spec §36: NO_PROGRESS is more
//     truthful than OBJECTIVE_UNPROVEN, because it names WHY the bound
//     produced no proof.
//   - FailureHumanRequired: no bounded continuation can produce the proof,
//     so the decision must escalate to a human rather than be invented.
//
// Those two are still a closed vocabulary: adding an outcome requires editing
// the taxonomy, which is what makes a terminal result attributable.
type FailureClass string

const (
	// FailureAuthorizationBlocked: the capability exists and would work, but
	// the Control Plane did not grant it. The fix is an authorization change,
	// never a capability change.
	FailureAuthorizationBlocked FailureClass = "AUTHORIZATION_BLOCKED"
	// FailureCapabilityMissing: the capability is not implemented at all. No
	// amount of retrying, re-prompting or re-scoping can produce it. This is
	// the class that must surface instead of a fabricated success.
	FailureCapabilityMissing FailureClass = "CAPABILITY_MISSING"
	// FailureCapabilityFailed: the capability was granted and attempted, and the
	// attempt itself broke (a listener that would not bind, a client that
	// could not be constructed).
	FailureCapabilityFailed FailureClass = "CAPABILITY_FAILED"
	// FailureTargetUncertain: evidence produced more than one admissible
	// destination and nothing proved which one was meant.
	FailureTargetUncertain FailureClass = "TARGET_UNCERTAIN"
	// FailureContextInsufficient: the runtime could not assemble enough real
	// material for the model to reason about the current decision.
	FailureContextInsufficient FailureClass = "CONTEXT_INSUFFICIENT"
	// FailureExecutionFailed: an authorized execution ran and returned a real
	// non-success outcome (non-zero exit, refused command, no such toolchain).
	FailureExecutionFailed FailureClass = "EXECUTION_FAILED"
	// FailureObservationFailed: the thing ran but the runtime could not observe
	// it (no reachable URL, no served body, a probe that never returned).
	FailureObservationFailed FailureClass = "OBSERVATION_FAILED"
	// FailureDiagnosisUncertain: observation succeeded but the evidence does
	// not identify a cause, so no defensible repair exists yet.
	FailureDiagnosisUncertain FailureClass = "DIAGNOSIS_UNCERTAIN"
	// FailureRepairFailed: a repair was attempted under authorization and did
	// not produce the intended change.
	FailureRepairFailed FailureClass = "REPAIR_FAILED"
	// FailureVerificationFailed: a repair landed and the objective's own
	// requirements are still unmet.
	FailureVerificationFailed FailureClass = "VERIFICATION_FAILED"
	// FailureObjectiveUnproven: the runtime ran out of bounded attempts with
	// the workspace in a legitimate state but no proof of the objective.
	FailureObjectiveUnproven FailureClass = "OBJECTIVE_UNPROVEN"
	// FailureNoProgress: the runtime stopped because rounds stopped changing
	// anything — the same defects, the same evidence, no mutation. This is a
	// control-plane stop, not a capability failure: nothing broke, the loop
	// simply learned nothing.
	FailureNoProgress FailureClass = "NO_PROGRESS"
	// FailureHumanRequired: no bounded continuation can produce the proof the
	// objective needs, so the runtime escalates instead of guessing.
	FailureHumanRequired FailureClass = "HUMAN_REQUIRED"
)

// Valid reports whether c is a member of the taxonomy.
func (c FailureClass) Valid() bool {
	for _, known := range AllFailureClasses {
		if known == c {
			return true
		}
	}
	return false
}

// String returns the raw class label.
func (c FailureClass) String() string { return string(c) }

// AllFailureClasses is the canonical ordered taxonomy.
var AllFailureClasses = []FailureClass{
	FailureAuthorizationBlocked,
	FailureCapabilityMissing,
	FailureCapabilityFailed,
	FailureTargetUncertain,
	FailureContextInsufficient,
	FailureExecutionFailed,
	FailureObservationFailed,
	FailureDiagnosisUncertain,
	FailureRepairFailed,
	FailureVerificationFailed,
	FailureObjectiveUnproven,
	FailureNoProgress,
	FailureHumanRequired,
}

// Block is a truthful, attributable stop: what could not be done, which class
// it belongs to, and which capabilities or targets were actually involved.
//
// A Block is NOT a failure verdict about the objective. It is the runtime
// declining to continue on evidence, with the reason made inspectable.
type Block struct {
	// Class is the taxonomy entry this block belongs to.
	Class FailureClass
	// Reason is the one-line deterministic explanation.
	Reason string
	// Capability is the capability the block concerns ("" for objective-level
	// blocks).
	Capability ID
	// Candidates are the evidence-backed alternatives when the block is
	// TARGET_UNCERTAIN. They are candidates, never authorization.
	Candidates []string
	// Evidence names the Evidence IDs that produced the block, so a reader can
	// walk from the verdict back to the observation that caused it.
	Evidence []string
}

// Error renders the block for a terminal reason string.
func (b Block) Error() string {
	var sb strings.Builder
	sb.WriteString(string(b.Class))
	if b.Capability != "" {
		sb.WriteString(" [")
		sb.WriteString(string(b.Capability))
		sb.WriteString("]")
	}
	if b.Reason != "" {
		sb.WriteString(": ")
		sb.WriteString(b.Reason)
	}
	return sb.String()
}

// Evidence is the structured record ONE capability execution produced. Every
// field is an observation: nothing here is inferred from the request, and
// nothing here is written by a caller.
//
// Evidence is the only thing the runtime is permitted to reason from. An
// outcome with no Evidence is an unsubstantiated claim, and the runtime treats
// it as one.
type Evidence struct {
	// ID is a stable identity for this observation within one capability run.
	ID string
	// Capability is the capability that produced it.
	Capability ID
	// OK reports whether the capability observed the state it went looking for.
	// False is not an error: a 404 probe that answered 404 is a successful
	// observation of a missing resource.
	OK bool
	// Class is the failure class when OK is false ("" when OK).
	Class FailureClass
	// Summary is the one-line deterministic description.
	Summary string
	// Fields are bounded key/value observations (status code, byte count, path,
	// command, exit code). They are facts, not prose.
	Fields map[string]string
	// Detail is bounded additional context (a body excerpt, a stderr tail).
	Detail string
}

// Field returns the named observation field, or "" when absent.
func (e Evidence) Field(name string) string {
	if e.Fields == nil {
		return ""
	}
	return e.Fields[name]
}

// String renders the compact one-line form used in diagnostics and reasons.
func (e Evidence) String() string {
	if e.OK {
		return string(e.Capability) + ": " + e.Summary
	}
	return string(e.Capability) + ": " + string(e.Class) + ": " + e.Summary
}

// Verdict is the outcome of one behavioral observation.
type Verdict string

const (
	// VerdictPass: every observed requirement held.
	VerdictPass Verdict = "PASS"
	// VerdictFail: at least one observed requirement did not hold.
	VerdictFail Verdict = "FAIL"
	// VerdictNotApplicable: nothing was observable to check. Distinct from
	// Pass — "I did not look" is never "I looked and it was fine".
	VerdictNotApplicable Verdict = "NOT_APPLICABLE"
	// VerdictBlocked: observation could not be performed at all.
	VerdictBlocked Verdict = "BLOCKED"
)

// Grant is the authorization vector one capability run executes under.
//
// It is CONSTRUCTED by the execution authority from the request's scope
// provenance and the workspace capability set. This package never derives a
// Grant, never widens one, and never treats the presence of a capability as
// evidence of permission: an ungranted capability returns
// FailureAuthorizationBlocked no matter who asked for it.
type Grant struct {
	// Provenance names the directive that authorized this run, for evidence.
	Provenance string
	// Discover permits WorkspaceDiscover.
	Discover bool
	// Read permits FileRead and FileSearch.
	Read bool
	// Execute permits RuntimeServe and CommandRun. Spawning a process is an
	// irreversible side effect and is gated separately from reading.
	Execute bool
	// Network permits RuntimeFetch and RuntimeInspect. Egress is gated
	// separately because a workspace-local probe and an internet request are
	// not the same authority.
	Network bool
}

// Permits reports whether the grant authorizes a capability. An unknown
// capability is never permitted.
func (g Grant) Permits(id ID) bool {
	switch id {
	case WorkspaceDiscover:
		return g.Discover
	case FileRead, FileSearch:
		return g.Read
	case RuntimeServe, CommandRun:
		return g.Execute
	case RuntimeFetch, RuntimeInspect:
		return g.Network
	default:
		return false
	}
}

// Names lists the granted capability identifiers in canonical order.
func (g Grant) Names() []string {
	out := make([]string, 0, len(AllIDs))
	for _, id := range AllIDs {
		if g.Permits(id) {
			out = append(out, string(id))
		}
	}
	return out
}

// errNotAuthorized is the sentinel every authorization refusal wraps, so a caller
// can classify with errors.Is without string matching.
var errNotAuthorized = errors.New("capability: not authorized")

// IsAuthorizationBlocked reports whether err is an authorization refusal.
func IsAuthorizationBlocked(err error) bool { return errors.Is(err, errNotAuthorized) }

// errNotImplemented is the sentinel for "this capability is not implemented here".
var errNotImplemented = errors.New("capability: not implemented")

// IsCapabilityMissing reports whether err names a missing capability.
func IsCapabilityMissing(err error) bool { return errors.Is(err, errNotImplemented) }

// NotAuthorized builds the canonical authorization refusal for one capability.
func NotAuthorized(id ID) error {
	return fmt.Errorf("%w: %s is not granted for this run", errNotAuthorized, id)
}

// Spec describes one capability for a model-facing catalog. The catalog is what
// makes a capability REQUESTABLE; it never makes one PERMITTED — that is the
// grant's job, enforced at Invoke.
type Spec struct {
	ID          ID     `json:"id"`
	Summary     string `json:"summary"`
	RequiredArg string `json:"required_arg,omitempty"`
}

// catalog is the stable, ordered capability catalog. It is data, not logic, so
// the model-facing surface and the runtime's actual capability set cannot drift
// apart silently.
var catalog = map[ID]Spec{
	WorkspaceDiscover: {
		ID:      WorkspaceDiscover,
		Summary: "Enumerate the workspace and derive its stack, entry document and runnable target from what is on disk.",
	},
	FileRead: {
		ID:          FileRead,
		Summary:     "Read one workspace file, optionally restricted to a 1-based inclusive line range.",
		RequiredArg: "path",
	},
	FileSearch: {
		ID:          FileSearch,
		Summary:     "Search workspace text files for a literal string and return matching path:line records.",
		RequiredArg: "query",
	},
	RuntimeServe: {
		ID:          RuntimeServe,
		Summary:     "Start the discovered runnable target and report the URL that actually answers.",
		RequiredArg: "root",
	},
	RuntimeFetch: {
		ID:          RuntimeFetch,
		Summary:     "Issue one HTTP request and report its real status, content type and body excerpt.",
		RequiredArg: "url",
	},
	RuntimeInspect: {
		ID:          RuntimeInspect,
		Summary:     "Load a served document, resolve every local subresource it references, probe each one, and audit the served structure.",
		RequiredArg: "path",
	},
	CommandRun: {
		ID:          CommandRun,
		Summary:     "Run one authorized command and report its real stdout, stderr and exit code.",
		RequiredArg: "command",
	},
}

// Catalog returns the ordered catalog of capabilities the grant authorizes.
// A capability absent from the result cannot be requested, so a model is never
// invited to ask for something the Control Plane will refuse.
func (g Grant) Catalog() []Spec {
	out := make([]Spec, 0, len(AllIDs))
	for _, id := range AllIDs {
		if !g.Permits(id) {
			continue
		}
		out = append(out, catalog[id])
	}
	return out
}

// RenderCatalog renders the granted catalog as a bounded, deterministic text
// block for inclusion in a system prompt. Rendering is presentation only: it
// describes capabilities, it grants nothing.
func (g Grant) RenderCatalog() string {
	specs := g.Catalog()
	if len(specs) == 0 {
		return "No runtime capabilities are granted for this run."
	}
	var sb strings.Builder
	sb.WriteString("Authorized runtime capabilities (authorized via ")
	sb.WriteString(g.Provenance)
	sb.WriteString("):\n")
	for _, s := range specs {
		sb.WriteString("- ")
		sb.WriteString(string(s.ID))
		sb.WriteString(": ")
		sb.WriteString(s.Summary)
		if s.RequiredArg != "" {
			sb.WriteString(" (requires ")
			sb.WriteString(s.RequiredArg)
			sb.WriteString(")")
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// SpecFor returns the catalog entry for id.
func SpecFor(id ID) (Spec, bool) {
	s, ok := catalog[id]
	return s, ok
}

// sortEvidence orders evidence by capability then identity so a rendered
// evidence log is byte-reproducible across runs.
func sortEvidence(in []Evidence) {
	sort.SliceStable(in, func(i, j int) bool {
		if in[i].Capability != in[j].Capability {
			return in[i].Capability < in[j].Capability
		}
		return in[i].ID < in[j].ID
	})
}
