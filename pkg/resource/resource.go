// Package resource defines the Resource Abstraction Layer of the Izen Agent
// Runtime. It decouples operations from concrete targets — files, git
// repositories and shell terminals — so that the rest of the runtime interacts
// with target state through one deterministic interface.
//
// Every Resource supports three capabilities:
//
//   - ValidateState: verify the target is present and usable.
//   - Snapshot: capture a deterministic, non-destructive representation of
//     the current target state.
//   - Restore: return the target to a previously captured snapshot.
//
// The package is intentionally free of any AI, LLM or prompt dependencies.
package resource

import "context"

// ResourceKind identifies the concrete kind of target a Resource wraps.
type ResourceKind string

// Supported resource kinds.
const (
	// KindFile is a single file target within a workspace tree.
	KindFile ResourceKind = "res.file"
	// KindGitRepo is a git repository target.
	KindGitRepo ResourceKind = "res.git"
	// KindTerminal is a shell execution environment (working dir + env).
	KindTerminal ResourceKind = "res.terminal"
)

// String returns the machine-readable kind label.
func (k ResourceKind) String() string { return string(k) }

// Snapshot is an immutable, deterministic representation of a resource's
// state at the moment it was captured. Hash returns a stable identifier for
// the captured state; Data returns the concrete, typed payload.
type Snapshot interface {
	// Hash returns a stable identifier of the captured state. Two snapshots
	// of identical state MUST produce identical hashes.
	Hash() string
	// Data returns the concrete typed payload of the snapshot.
	Data() any
}

// Resource is the single interface through which operations interact with a
// target. Concrete adapters (file, git, terminal) implement it.
type Resource interface {
	// ID returns a stable unique identifier for this resource instance.
	ID() string
	// Kind returns the kind of target this resource wraps.
	Kind() ResourceKind
	// ValidateState verifies the target is present and usable, returning an
	// error describing the first problem found.
	ValidateState(ctx context.Context) error
	// Snapshot captures the current target state non-destructively.
	Snapshot(ctx context.Context) (Snapshot, error)
	// Restore returns the target to the captured state. Passing a snapshot
	// captured by a different resource or kind is an error.
	Restore(ctx context.Context, s Snapshot) error
}
