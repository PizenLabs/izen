package kernel

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// CapabilityID is the closed vocabulary of executable capability identifiers. It
// is deliberately small and total: a runtime that cannot name a capability
// cannot authorize it, and an identifier outside this set is a programming error,
// not a runtime condition.
//
// The set is domain-neutral. Nothing here says "HTML", "CSS", or "website",
// because the kernel executes operations on a workspace, not a task domain. A
// domain vocabulary belongs to the layer that classifies intent, above this
// one.
type CapabilityID string

const (
	// WorkspaceDiscover enumerates the workspace and derives its shape from what
	// is actually on disk. Read-only. This is the real discovery capability: a
	// step that needs to know what exists must actually look, and the kernel
	// records the observation as evidence.
	WorkspaceDiscover CapabilityID = "workspace.discover"

	// FileRead reads one bounded region of one workspace file. Read-only.
	FileRead CapabilityID = "file.read"

	// FileSearch locates literal matches across workspace text files.
	// Read-only.
	FileSearch CapabilityID = "file.search"

	// FileWrite replaces the whole content of one workspace file. Mutating.
	FileWrite CapabilityID = "file.write"

	// FileDelete removes one workspace file. Mutating, and the counterpart of
	// FileWrite: together they are the two ways a declared destination's
	// content can change. A delete is its own capability rather than a write of
	// zero bytes, because "the target is gone" and "the target now holds an
	// empty string" are different filesystem facts and a contract that could
	// not tell them apart could never demand either one.
	FileDelete CapabilityID = "file.delete"

	// FileExists reports whether a declared target is present. Read-only, and
	// distinct from FileRead: existence is its own observation, and claiming a
	// file exists without reading the directory is a claim, not an observation.
	FileExists CapabilityID = "file.exists"

	// CommandRun executes one command under the caller's authorization and
	// returns its real stdout, stderr and exit code. Process-spawning.
	CommandRun CapabilityID = "command.run"
)

// allCapabilityIDs is the canonical ordered vocabulary.
var allCapabilityIDs = []CapabilityID{
	WorkspaceDiscover,
	FileRead,
	FileSearch,
	FileExists,
	FileWrite,
	FileDelete,
	CommandRun,
}

// AllCapabilityIDs returns the canonical ordered capability vocabulary.
func AllCapabilityIDs() []CapabilityID {
	out := make([]CapabilityID, len(allCapabilityIDs))
	copy(out, allCapabilityIDs)
	return out
}

// Valid reports whether id is a member of the closed vocabulary.
func (id CapabilityID) Valid() bool {
	for _, known := range allCapabilityIDs {
		if known == id {
			return true
		}
	}
	return false
}

// String returns the raw identifier.
func (id CapabilityID) String() string { return string(id) }

// Mutating reports whether invoking the capability can change the workspace.
// The authorization boundary reads this, so a read-only grant can never be
// widened into a mutating one by accident.
func (id CapabilityID) Mutating() bool {
	switch id {
	case FileWrite, FileDelete, CommandRun:
		return true
	default:
		return false
	}
}

// Request is the immutable, fully-resolved input to one capability invocation.
//
// It is constructed by the kernel from a Step and a Grant. A capability never
// sees a Step, never sees a Grant, and never sees the execution: it sees only
// what it is being asked to do and under which authorization. That narrowness
// is what stops a capability from deciding that it may act elsewhere.
type Request struct {
	// Capability is the capability being invoked.
	Capability CapabilityID
	// Step is the identifier of the step that produced this request.
	Step string
	// Target is the concrete, workspace-relative destination. It is never
	// inferred: the kernel passes whatever the Step declared, and a capability
	// that cannot act on it says so rather than substituting a guess.
	Target string
	// Args are the capability-specific, non-authorizing arguments. They are
	// carried verbatim and are never interpreted by the kernel.
	Args map[string]string
	// Grant is the authorization under which this invocation is permitted. A
	// capability must check it before touching anything.
	Grant Grant
}

// Arg returns a named argument and whether it was present.
//
// It is exported because capabilities are external implementations of this
// boundary. A capability that had to guess at its arguments — or reach into the
// step, or the engine, to find them — would be a capability operating outside its
// own contract.
func (r Request) Arg(name string) (string, bool) {
	if r.Args == nil {
		return "", false
	}
	v, ok := r.Args[name]
	return v, ok
}

// IntArg returns a named argument parsed as a non-negative integer.
//
// It refuses rather than guessing when the value is absent or malformed, because
// a silently defaulted bound is a limit nobody chose.
func (r Request) IntArg(name string) (int, bool) {
	raw, ok := r.Arg(name)
	if !ok {
		return 0, false
	}
	var n int
	if _, err := fmt.Sscanf(raw, "%d", &n); err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// Capability is the boundary the kernel executes through. The shape is fixed:
//
//	Authorize -> Invoke -> Observation -> Evidence -> State transition
//
// A capability is a real boundary, not an interface with a single incidental
// implementation: it is the only place in the kernel that can observe or change
// anything outside the process's own memory.
//
// Two obligations are non-negotiable and are enforced by contract rather than by
// trust:
//
//  1. Authorize MUST refuse when the request's Grant does not permit this
//     capability or target. Authorize never widens a grant; it only ever
//     narrows one further.
//  2. Invoke MUST NOT return an Observation describing something it did not
//     actually observe. There is no "presumed" or "expected" observation. A
//     capability that did not look reports that it did not look.
//
// A capability that wants to change the world must also be honest about whether
// it changed it: the mutation axis of the kernel is derived from what Invoke
// reports, so a capability that lies about a write produces an unsatisfied
// mutation, not a durable success.
type Capability interface {
	// ID returns the capability identifier. It must be a member of the closed
	// vocabulary.
	ID() CapabilityID

	// Authorize reports whether this capability, under this exact grant, may act
	// on this exact request. It returns a Block when it may not.
	//
	// Authorize is a second, capability-local gate beneath the kernel's
	// authorization gate. The kernel has already checked the grant; Authorize
	// exists because a capability may have preconditions the grant cannot
	// express (a port already bound, a path outside the workspace).
	Authorize(Request) error

	// Invoke performs the capability and reports what it actually observed. It
	// must be safe to call with a cancelled context and must return promptly
	// when the context is done.
	Invoke(ctx context.Context, req Request) (Observation, error)
}

// CapabilityFuncs adapts a pair of functions to Capability. It exists so a
// capability can be written in one place without a named type, which keeps
// tests short without inventing an interface hierarchy.
type CapabilityFuncs struct {
	// CapID is the identifier the capability answers to.
	CapID CapabilityID
	// AuthorizeFn is the capability-local gate. A nil value means "no additional
	// precondition beyond the grant", which is a legitimate position for a pure
	// read-only capability but not for a mutating one.
	AuthorizeFn func(Request) error
	// InvokeFn performs the work.
	InvokeFn func(ctx context.Context, req Request) (Observation, error)
}

// ID implements Capability.
func (c CapabilityFuncs) ID() CapabilityID { return c.CapID }

// Authorize implements Capability.
func (c CapabilityFuncs) Authorize(req Request) error {
	if c.AuthorizeFn == nil {
		return nil
	}
	return c.AuthorizeFn(req)
}

// Invoke implements Capability.
func (c CapabilityFuncs) Invoke(ctx context.Context, req Request) (Observation, error) {
	if c.InvokeFn == nil {
		return Observation{}, errors.New("kernel: capability has no Invoke implementation")
	}
	return c.InvokeFn(ctx, req)
}

// Compile-time proof that the adapter satisfies the boundary.
var _ Capability = CapabilityFuncs{}

// Registry is the runtime's authoritative answer to "what can this kernel
// actually do". The kernel consults it before authorizing a step, so a step
// naming an unregistered capability is refused rather than attempted.
//
// The registry is immutable once the engine starts running: it is built at
// composition time and never mutated by the execution itself. That is what makes
// "the runtime knew what it could do" a checkable statement rather than a
// hopeful one.
type Registry struct {
	mu    sync.RWMutex
	caps  map[CapabilityID]Capability
	order []CapabilityID
}

// NewRegistry builds a registry from the given capabilities. Duplicate
// identifiers are refused: two implementations claiming one identity would make
// the capability surface undecidable.
func NewRegistry(caps ...Capability) (*Registry, error) {
	r := &Registry{caps: make(map[CapabilityID]Capability, len(caps))}
	for _, c := range caps {
		if c == nil {
			return nil, newBlock(FailureCapabilityUnavailable, "", "", "registry received a nil capability")
		}
		id := c.ID()
		if !id.Valid() {
			return nil, blockf(FailureCapabilityUnavailable, "", id, "capability identifier is outside the closed vocabulary")
		}
		if _, dup := r.caps[id]; dup {
			return nil, blockf(FailureCapabilityUnavailable, "", id, "capability identifier registered twice")
		}
		r.caps[id] = c
		r.order = append(r.order, id)
	}
	return r, nil
}

// Lookup returns the capability for id.
func (r *Registry) Lookup(id CapabilityID) (Capability, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.caps[id]
	return c, ok
}

// Has reports whether id is registered.
func (r *Registry) Has(id CapabilityID) bool {
	_, ok := r.Lookup(id)
	return ok
}

// IDs returns the registered identifiers in canonical vocabulary order, so two
// kernels with the same capabilities render the same catalog.
func (r *Registry) IDs() []CapabilityID {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	registered := make(map[CapabilityID]bool, len(r.caps))
	for id := range r.caps {
		registered[id] = true
	}
	out := make([]CapabilityID, 0, len(registered))
	for _, id := range allCapabilityIDs {
		if registered[id] {
			out = append(out, id)
		}
	}
	return out
}

// Missing returns the capability identifiers referenced by the program that are
// not registered, in program order. The engine reports these before dispatching
// anything, so an unsatisfiable program fails as a whole rather than halfway
// through having already mutated something.
func (r *Registry) Missing(program Program) []CapabilityID {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []CapabilityID
	for _, step := range program {
		if !step.Capability.Valid() {
			continue
		}
		if _, registered := r.caps[step.Capability]; !registered {
			out = append(out, step.Capability)
		}
	}
	return out
}

// String renders the registry's capability surface.
func (r *Registry) String() string {
	ids := r.IDs()
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := ""
	for i, id := range ids {
		if i > 0 {
			out += ", "
		}
		out += string(id)
	}
	if out == "" {
		return "kernel: no capabilities registered"
	}
	return out
}

// joinIDs renders capability identifiers in the given order.
func joinIDs(ids []CapabilityID) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, string(id))
	}
	return strings.Join(parts, " ")
}
