package executor

// This file is the FileExecutor's crossing point into the Runtime Kernel.
//
// FileExecutor is the execution authority of the CLI control-plane path:
//
//	cmd/izen/main.go:141            case "orchestrate"
//	  → cmd/izen/orchestrate.go:48  runOrchestrateCommand
//	  → internal/cli/cli.go:323     Wire
//	  → internal/cli/cli.go:392     Stack.Run
//	  → internal/runtime/orchestrator/engine.go:101  Orchestrator.RunCycle
//	  → FileExecutor.Commit / FileExecutor.Rollback    ← this file
//	  → kernelbridge.Apply / Delete
//	  → runtime/kernel                (admission, grant, dispatch, verify, adjudicate)
//	  → runtime/capabilities/filesystem
//
// Core keeps everything it owned before the migration and keeps it in
// file_executor.go: the snapshot decision (PrepareSnapshot), patch
// materialization, the symbol-baseline redundancy gate, use-time confinement,
// the rollback POLICY (failWithRollback), and the transaction that pairs a
// snapshot with the bytes derived from it. What moved is exactly one thing —
// the final filesystem effect of a commit and of a rollback. It used to be a
// hand-rolled temp-file-and-rename protocol driven by os.CreateTemp,
// os.Chmod, os.Rename, os.WriteFile and os.Remove: a grant the kernel could
// not see, an event nobody recorded, evidence nobody kept, and no independent
// check that the bytes a caller was told were written were the bytes on disk.
//
// Two rules govern what may be added here.
//
// The first is that this file may not become a second control plane. It
// composes a request, asks, and reports what came back. It does not choose a
// destination, does not decide a target may be written, does not retry, and
// does not widen a grant.
//
// The second is that a kernel outcome is not a Core outcome. kernelbridge.Apply
// reaching PROVEN proves that one filesystem effect happened and was
// verified. It does not prove the proposal committed: only FileExecutor, which
// owns the snapshot and rolls it back on any failure, may say that. Every
// helper here therefore returns the kernel's Applied to its caller rather than
// a boolean that would collapse the two claims.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/izen/internal/kernelbridge"
)

// writeContract is the obligation set every FileExecutor write is declared
// under.
//
// Commit replaces a destination's whole content and does not distinguish "this
// file is new" from "this file existed" — the FileBackup knows which, but the
// obligation set is the same either way. Declaring CREATE would assert a
// creation the proposal never claimed, and for an overwrite that assertion is
// false. PATCH is the honest declaration for a whole-content replace: it
// demands durable write evidence for the destination and nothing more.
//
// It applies to rollback too, and for the same reason. A rollback restores a
// snapshot; it is not the creation of a file, and the kernel's independent
// pre-write observation is what lets Applied.Created report the truth about
// creation if anyone asks. PATCH applied to a destination that did not exist
// under-claims, which is the safe direction: an obligation the runtime could
// not meet is reported, an obligation it invented is not.
const writeContract = kernelbridge.ContractPatch

// ErrUnboundWorkspace is returned when a mutation is attempted by an executor
// that was never told which workspace it may mutate.
//
// The kernel bridge forms a grant over exactly one workspace root. An executor
// with no declared workspace has nothing to name in that grant, and the one
// thing it must never do is guess — a fallback that quietly used the process
// working directory would place bytes outside the grant the caller believes it
// holds. Binding the workspace is therefore a construction requirement, not an
// optional hardening step, and the failure is explicit.
var ErrUnboundWorkspace = errors.New("executor: no workspace bound; construct the executor with WithWorkspace(root) so mutations can name a grant")

// placeThroughKernel places content at one execution target through the
// Runtime Kernel bridge.
//
// This is the FileExecutor's final filesystem primitive, and it serves both
// directions of the transaction: a commit places the proposal's bytes, and a
// rollback places the snapshot's. They are one operation and are written once
// here on purpose — a rollback that reimplemented the write would be a second
// place for the two to diverge, and divergence between them means a rollback
// that cannot undo its own commit.
//
// What stays in Core is the decision about WHAT the bytes are. Patch
// materialization — resolving a diff against the snapshot base — is the
// Control Plane's answer and never crosses this file. The kernel owns only
// the deterministic placement: it is invoked under an explicit grant naming
// exactly this destination, writes through the confined filesystem
// capability, re-reads the result from disk through code that did not write
// it, and adjudicates the outcome from that evidence.
//
// op names the effect being performed and appears only in a refusal message,
// so an operator reading a failure sees which of the two operations the
// kernel declined to prove.
//
// The Applied and the canonical target are returned alongside the error
// because "the kernel could not prove it" has two cases a caller must
// distinguish: nothing was written (an admission or confinement refusal) and
// bytes reached disk but the independent re-read disagreed (a concurrent
// writer). Only the second is an executed mutation that the transaction must
// undo rather than report as a no-op, and the caller can only tell them apart
// from the evidence returned here.
func (e *FileExecutor) placeThroughKernel(ctx context.Context, op, targetPath, content string) (kernelbridge.Applied, string, error) {
	rel, err := e.workspaceTarget(targetPath)
	if err != nil {
		return kernelbridge.Applied{}, "", err
	}
	applied := kernelbridge.Apply(ctx, e.workspace, []kernelbridge.Write{{
		Target:   rel,
		Content:  content,
		Contract: writeContract,
	}})
	if !applied.Landed(rel) {
		return applied, rel, e.kernelRefused(op, rel, applied)
	}
	return applied, rel, nil
}

// commitThroughKernel places a commit's resolved bytes through the kernel.
func (e *FileExecutor) commitThroughKernel(ctx context.Context, targetPath, content string) (kernelbridge.Applied, string, error) {
	return e.placeThroughKernel(ctx, "write", targetPath, content)
}

// restoreThroughKernel puts a snapshot's bytes back through the kernel.
//
// A rollback is not a lesser commit. It writes the same destination with the
// same primitive and earns the same evidence: if the restore cannot be
// proven, the caller must learn that the workspace was not fully restored
// rather than receiving a nil error from a syscall.
func (e *FileExecutor) restoreThroughKernel(ctx context.Context, targetPath, content string) error {
	_, _, err := e.placeThroughKernel(ctx, "restore", targetPath, content)
	return err
}

// removeThroughKernel deletes a target the transaction itself created, through
// the Runtime Kernel bridge.
//
// The distinction it preserves is the one a bare os.Remove made invisible.
// Removing a target that was not there is not a failure — it is the desired
// end state — but it is also not a removal, and reporting it as one would let
// a rollback claim to have deleted a file it never touched. The kernel
// distinguishes them exactly: a durable removal records FILE_DELETED, an
// already-absent destination records FILE_ABSENT and nothing else.
//
// So both outcomes are accepted, and which one happened is carried in the
// returned evidence rather than flattened into one success bit. A target the
// kernel refused to touch at all — a directory, a target outside the workspace,
// one it was never granted — is neither, and fails.
func (e *FileExecutor) removeThroughKernel(ctx context.Context, targetPath string) error {
	rel, err := e.workspaceTarget(targetPath)
	if err != nil {
		return err
	}
	applied := kernelbridge.Delete(ctx, e.workspace, []string{rel})
	switch {
	case applied.Deleted(rel):
		return nil
	case applied.Vanished(rel):
		// Already absent: the end state holds, nothing was removed, and the
		// evidence says so. This is the evidence-backed form of the
		// os.IsNotExist tolerance the legacy remove had.
		return nil
	default:
		return e.kernelRefused("delete", rel, applied)
	}
}

// workspaceTarget maps an execution target onto the workspace-relative
// spelling the kernel's grant and evidence log agree on.
//
// The containment check is lexical and runs before anything is asked of the
// kernel: a target that climbs out of the declared root names nothing the
// bridge could grant, and letting the request through would turn a Core
// decision into a kernel refusal with no Core-side record of it. The kernel
// then re-checks confinement independently from its own root handle, so the
// guarantee does not rest on this check alone.
func (e *FileExecutor) workspaceTarget(targetPath string) (string, error) {
	if e == nil || e.workspace == "" {
		return "", ErrUnboundWorkspace
	}
	root, err := filepath.Abs(e.workspace)
	if err != nil {
		return "", fmt.Errorf("executor: resolve workspace %q: %w", e.workspace, err)
	}
	abs := targetPath
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, abs)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", fmt.Errorf("%w: target %q escapes workspace %q", ErrWorkspaceEscape, targetPath, e.workspace)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: target %q escapes workspace %q", ErrWorkspaceEscape, targetPath, e.workspace)
	}
	return filepath.ToSlash(rel), nil
}

// kernelRefused builds the error a caller sees when the kernel did not prove
// the effect it was asked for.
//
// It reports the whole verdict rather than a bare failure, because "the
// runtime refused to run" and "the runtime ran and could not prove it" are
// different facts and a caller that cannot tell them apart will retry a
// refusal as though it were a flake.
func (e *FileExecutor) kernelRefused(op, rel string, applied kernelbridge.Applied) error {
	return fmt.Errorf(
		"kernel did not prove the %s of %s: outcome=%s class=%s verify=%s reason=%s execution=%s",
		op, rel, applied.Outcome, applied.Class, applied.Verify, applied.Reason, applied.ExecutionID)
}
