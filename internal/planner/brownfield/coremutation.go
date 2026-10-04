package brownfield

// This file is the brownfield planner's crossing point into Core execution.
//
// The brownfield graph describes WHAT should change: a target path, the desired
// content, and the operation intent. It must not own the final filesystem
// effect. Before this seam existed, every brownfield write node called
// file.FileResource.Write, which is a bare os.WriteFile — no Core
// authorization, no transaction owner, no snapshot, no rollback, no kernel
// grant, no primitive evidence and no verification. The graph/resource layer
// was an independent workspace execution authority.
//
// This seam removes that authority. A write node now targets a
// coreMutationTarget, which shapes the graph's intent into a
// substrate.Proposal and submits it to the Core execution authority the
// pipeline already uses (substrate.ProposalExecutor, concretely
// *substrate.ConcreteSubstrate). Core then does what it does for Path A:
//
//	proposal → use-time confinement (Core authorization)
//	         → snapshot + transaction (Core)
//	         → commitWrite / commitDelete → kernelbridge.Apply / Delete
//	         → runtime/kernel (admission, grant, dispatch, verify, adjudicate)
//	         → runtime/capabilities/filesystem (the final filesystem effect)
//	         → MutationEvidence + ExecutionProof (Core evidence)
//
// Two rules govern this file, copied from the other two migrated seams
// (internal/runtime/substrate/kernelcommit.go and
// internal/runtime/executor/kernelcommit.go) because they are the same
// decision taken on a third path:
//
//  1. It may not become a second control plane. It composes a request, asks,
//     and reports what came back. It does not choose a destination, does not
//     decide a target may be written, does not retry, and does not widen a
//     grant. The kernel Grant is constructed by kernelbridge from fixed seam
//     policy; nothing here can see or widen it.
//  2. A kernel outcome is not a Core outcome. A successful Execute proves the
//     mutation was authorized, transacted and adjudicated; it does not prove
//     the objective. This seam returns the authority's error unchanged rather
//     than deriving a boolean that could report success from a dispatched
//     graph operation instead of from filesystem evidence.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/PizenLabs/izen/internal/resource"
	"github.com/PizenLabs/izen/internal/resource/file"
	"github.com/PizenLabs/izen/internal/runtime/substrate"
)

// coreMutationTarget wraps a file.FileResource and routes its mutations to a
// Core execution authority instead of to the live workspace.
//
// It satisfies resource.Resource (so op.NewOperation accepts it and the graph
// can snapshot/validate it) and the graph's context-aware file writer/deleter
// contracts (so the write carries the operation's context into Core and
// observes cancellation and deadlines). It is a request shaper, not an
// executor: every effect is decided and performed by the authority it is
// bound to.
type coreMutationTarget struct {
	base *file.FileResource
	exec substrate.ProposalExecutor
	seq  *atomic.Uint64
}

// Compile-time assertions: the target is a Resource and speaks the
// context-aware mutation contracts graph.OpNode prefers.
var (
	_ resource.Resource = (*coreMutationTarget)(nil)
)

// WriteContext submits one FILE_WRITE operation to the Core authority and
// returns its adjudicated result.
//
// The operation is a whole-content replace of one workspace-relative target,
// so it is declared OpFileWrite. Core maps that to the honest kernel contract
// (PATCH: durable write evidence, never an invented CREATE). Success is
// whatever the authority proved, not that the graph dispatched a node.
func (t *coreMutationTarget) WriteContext(ctx context.Context, data []byte) error {
	return t.submit(ctx, substrate.Operation{
		Type:    substrate.OpFileWrite,
		Target:  t.base.RelPath(),
		Content: data,
	})
}

// Write is the contextless form of WriteContext. The graph prefers
// WriteContext; this exists so the target still satisfies fileWriter for any
// caller that reaches it without a context.
func (t *coreMutationTarget) Write(data []byte) error {
	return t.WriteContext(context.Background(), data)
}

// DeleteContext submits one FILE_DELETE operation to the Core authority.
//
// Brownfield planning currently only lowers file artifacts to writes, but the
// graph carries a delete intent too. Routing it here — rather than leaving the
// base resource as the fallback — keeps the operation truthful if a future
// planner emits a delete.
func (t *coreMutationTarget) DeleteContext(ctx context.Context) error {
	return t.submit(ctx, substrate.Operation{
		Type:   substrate.OpFileDelete,
		Target: t.base.RelPath(),
	})
}

// Delete is the contextless form of DeleteContext.
func (t *coreMutationTarget) Delete() error {
	return t.DeleteContext(context.Background())
}

// submit shapes the mutation request and hands it to the Core authority.
//
// The proposal carries exactly one operation for one target. The authority is
// the transaction owner: it snapshots before mutating, rolls the operation
// back on any failure, commits through the kernel under an explicit grant, and
// records the evidence. Nothing about that decision is made here.
func (t *coreMutationTarget) submit(ctx context.Context, op substrate.Operation) error {
	if t.exec == nil {
		return ErrNoMutationAuthority
	}
	prop := substrate.Proposal{
		ID:     t.proposalID(),
		Intent: "brownfield:" + t.base.RelPath(),
		Operations: []substrate.Operation{
			op,
		},
	}
	proof, err := t.exec.Execute(ctx, prop)
	if err != nil {
		// The authority refused or could not prove the effect. Its error already
		// names the reason (escape, refusal, failed verification); do not mask it
		// behind a graph-local success bit.
		return fmt.Errorf("brownfield: core mutation for %q: %w", t.base.RelPath(), err)
	}
	// Execute returns a nil error only after the transaction committed. Status
	// is Core's claim about the transaction; the per-operation Outcome inside
	// proof.Mutations is the kernel's claim about the primitive. A caller that
	// reported success from dispatch rather than from this result would be
	// asserting a completion nobody adjudicated.
	if proof.Status != "committed" {
		return fmt.Errorf("brownfield: core mutation for %q was not committed (status=%s)", t.base.RelPath(), proof.Status)
	}
	return nil
}

// proposalID derives a unique, filesystem-safe proposal identity. It is used
// by Core as the evidence artifact name, so it must not collide across
// writes; the monotonic sequence is paired with a nanosecond stamp because a
// fresh planner is constructed on every pipeline run.
func (t *coreMutationTarget) proposalID() string {
	seq := uint64(0)
	if t.seq != nil {
		seq = t.seq.Add(1)
	}
	return fmt.Sprintf("brownfield-%d-%d", time.Now().UnixNano(), seq)
}

// ID returns the wrapped file's canonical absolute path.
func (t *coreMutationTarget) ID() string { return t.base.ID() }

// Kind returns resource.KindFile.
func (t *coreMutationTarget) Kind() resource.ResourceKind { return t.base.Kind() }

// RelPath returns the workspace-relative path of the wrapped file.
func (t *coreMutationTarget) RelPath() string { return t.base.RelPath() }

// ValidateState delegates to the underlying file resource.
func (t *coreMutationTarget) ValidateState(ctx context.Context) error {
	return t.base.ValidateState(ctx)
}

// Snapshot delegates to the underlying file resource. Snapshotting reads; it
// does not mutate, so delegating does not reintroduce a second authority.
func (t *coreMutationTarget) Snapshot(ctx context.Context) (resource.Snapshot, error) {
	return t.base.Snapshot(ctx)
}

// Restore is deliberately NOT delegated to the base file resource. That
// delegation would call file.FileResource.Restore, which writes with a bare
// os.WriteFile — exactly the direct mutation this seam exists to remove. The
// transaction that undoes a mutation is Core's and lives in the authority
// (snapshot + rollback inside substrate.ConcreteSubstrate.Execute), so no
// graph execution path restores through this adapter.
func (t *coreMutationTarget) Restore(context.Context, resource.Snapshot) error {
	return ErrResourceRestoreUnsupported
}

// rootReporter is implemented by Core authorities that can name the workspace
// they are bound to (notably *substrate.ConcreteSubstrate). It lets the
// planner refuse a mismatched authority rather than place bytes in a different
// tree than the one it planned against.
type rootReporter interface {
	Root() string
}

// rootsMatch reports whether an authority is bound to the same workspace the
// planner is. An authority that cannot report its root is accepted; the
// substrate still re-checks confinement independently at use time.
func rootsMatch(exec substrate.ProposalExecutor, workspaceRoot string) bool {
	rr, ok := exec.(rootReporter)
	if !ok {
		return true
	}
	root := strings.TrimSpace(rr.Root())
	if root == "" {
		return true
	}
	return cleanAbs(root) == cleanAbs(workspaceRoot)
}

// cleanAbs normalises a workspace root for comparison, falling back to a
// lexical clean when the path cannot be made absolute.
func cleanAbs(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return abs
}
