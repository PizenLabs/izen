package ports

import "sync"

// Capability is a bitmask describing a single permission held by an execution
// context. The bits mirror the mode capability matrix so the domain layer can
// reason about authorization without importing the presentation or runtime
// layers.
type Capability uint64

const (
	// CapRead grants read access to files, git history, and the code graph.
	CapRead Capability = 1 << iota
	// CapWrite grants the right to modify files in the workspace.
	CapWrite
	// CapShell grants the right to execute arbitrary shell commands.
	CapShell
	// CapTest grants the right to run test suites.
	CapTest
	// CapPatch grants the right to generate or apply patches.
	CapPatch
	// CapCheckpoint grants the right to create or restore git checkpoints.
	CapCheckpoint
)

// Has reports whether c contains every bit set in other.
func (c Capability) Has(other Capability) bool {
	return c&other == other
}

// String returns a human-readable label for the capability bit.
func (c Capability) String() string {
	switch c {
	case CapRead:
		return "read"
	case CapWrite:
		return "write"
	case CapShell:
		return "shell"
	case CapTest:
		return "test"
	case CapPatch:
		return "patch"
	case CapCheckpoint:
		return "checkpoint"
	default:
		return "unknown"
	}
}

// CapabilitySet is a thread-safe mutable set of capability bits.
type CapabilitySet struct {
	mu   sync.RWMutex
	bits Capability
}

// NewCapabilitySet builds a set seeded with the given bits.
func NewCapabilitySet(bits Capability) *CapabilitySet {
	return &CapabilitySet{bits: bits}
}

// Has reports whether the set contains every bit in c.
func (s *CapabilitySet) Has(c Capability) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.bits.Has(c)
}

// Add turns on the given bits.
func (s *CapabilitySet) Add(c Capability) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bits |= c
}

// Remove turns off the given bits.
func (s *CapabilitySet) Remove(c Capability) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bits &^= c
}

// Bits returns a snapshot of the underlying bitmask.
func (s *CapabilitySet) Bits() Capability {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.bits
}

// String returns the comma-joined names of every set bit.
func (s *CapabilitySet) String() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := []string{}
	for _, c := range []Capability{CapRead, CapWrite, CapShell, CapTest, CapPatch, CapCheckpoint} {
		if s.bits.Has(c) {
			names = append(names, c.String())
		}
	}
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ","
		}
		out += n
	}
	return out
}
