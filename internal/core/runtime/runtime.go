// Package runtime aggregates the capability guard, mutation budget, artifact
// registry, workspace snapshot cache, and archetype capability registry into a
// single RuntimeContext for the execution engine.
package runtime

import (
	"fmt"

	"github.com/PizenLabs/izen/internal/core/artifact"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/capability"
	wscap "github.com/PizenLabs/izen/internal/workspace/capability"
	"github.com/PizenLabs/izen/internal/workspace/snapshot"
)

// RuntimeContext is the aggregation point for the capability guard, mutation
// budget, and artifact registry. It provides high-level evaluation functions
// that the execution engine uses to authorise operations.
type RuntimeContext struct {
	Artifacts   *artifact.Store
	Caps        *capability.CapabilitySet
	Budget      *budget.MutationBudget
	SnapCache   *snapshot.SnapshotCache
	CapRegistry *wscap.ArchetypeCapabilityRegistry
}

// New creates a RuntimeContext with the given dependencies.
func New(store *artifact.Store, caps *capability.CapabilitySet, b *budget.MutationBudget) *RuntimeContext {
	return &RuntimeContext{
		Artifacts:   store,
		Caps:        caps,
		Budget:      b,
		SnapCache:   snapshot.NewSnapshotCache(),
		CapRegistry: wscap.NewArchetypeCapabilityRegistry(),
	}
}

// NewWithSnapRegistry creates a RuntimeContext with the given dependencies
// including explicit snapshot cache and capability registry instances.
func NewWithSnapRegistry(
	store *artifact.Store,
	caps *capability.CapabilitySet,
	b *budget.MutationBudget,
	snapCache *snapshot.SnapshotCache,
	capReg *wscap.ArchetypeCapabilityRegistry,
) *RuntimeContext {
	return &RuntimeContext{
		Artifacts:   store,
		Caps:        caps,
		Budget:      b,
		SnapCache:   snapCache,
		CapRegistry: capReg,
	}
}

// CanMutateFile checks whether the capability set allows mutating the given
// file path.
func (rc *RuntimeContext) CanMutateFile(path string) bool {
	return rc.Caps.CanMutateFile(path)
}

// CanExecuteCommand checks whether the capability set allows executing the
// given shell command.
func (rc *RuntimeContext) CanExecuteCommand(cmd string) bool {
	return rc.Caps.CanExecuteCommand(cmd)
}

// CanRead checks whether the capability set allows reading.
func (rc *RuntimeContext) CanRead() bool { return rc.Caps.CanRead() }

// CanWrite checks whether the capability set allows writing.
func (rc *RuntimeContext) CanWrite() bool { return rc.Caps.CanWrite() }

// CanTest checks whether the capability set allows running tests.
func (rc *RuntimeContext) CanTest() bool { return rc.Caps.CanTest() }

// CanPatch checks whether the capability set allows patching.
func (rc *RuntimeContext) CanPatch() bool { return rc.Caps.CanPatch() }

// CanCheckpoint checks whether the capability set allows checkpoints.
func (rc *RuntimeContext) CanCheckpoint() bool { return rc.Caps.CanCheckpoint() }

// CanRollback checks whether the capability set allows rollback.
func (rc *RuntimeContext) CanRollback() bool { return rc.Caps.CanRollback() }

// ConsumeBudget attempts to consume the given delta against the mutation
// budget. Returns a BudgetExhaustedError if any limit is exceeded.
func (rc *RuntimeContext) ConsumeBudget(delta budget.BudgetDelta) error {
	return rc.Budget.Consume(delta)
}

// IsBudgetExhausted returns true if the mutation budget has been exhausted.
func (rc *RuntimeContext) IsBudgetExhausted() bool {
	return rc.Budget.IsExhausted()
}

// MutateFile is a convenience method that checks capability and consumes
// file budget in a single call.
func (rc *RuntimeContext) MutateFile(path string) error {
	if !rc.CanMutateFile(path) {
		return fmt.Errorf("runtime: capability denied: mutate file %q", path)
	}
	return rc.ConsumeBudget(budget.BudgetDelta{Files: 1})
}

// ExecuteCommand is a convenience method that checks capability and consumes
// shell command budget in a single call.
func (rc *RuntimeContext) ExecuteCommand(cmd string) error {
	if !rc.CanExecuteCommand(cmd) {
		return fmt.Errorf("runtime: capability denied: execute %q", cmd)
	}
	return rc.ConsumeBudget(budget.BudgetDelta{ShellCmds: 1})
}
