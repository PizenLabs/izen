package substrate

import (
	"bytes"
	"context"
	"os"
)

// This file is Core's transaction memory for Path A: the snapshot taken before a
// mutation and the rollback that undoes it.
//
// It is deliberately NOT part of the kernel migration, and the reason is
// architectural rather than convenient. Recovery is a decision about what the
// runtime *meant*, not a deterministic primitive: only Core knows that
// operation 3 failing means operations 1 and 2 must be undone, which originals
// they had, and whether the transaction as a whole is now tainted. Moving that
// into the kernel would hand it a transaction it has no mandate over.
//
// So the recovery writes below stay on the substrate's own FilePort. They are
// RECOVERY, not EXECUTION: they undo a change nobody asked for, they are never
// authorized by a proposal, and no evidence they produce is reported as a
// committed mutation.

// snapshot is one target's pre-mutation state, plus what this transaction put
// there afterwards.
type snapshot struct {
	path    string
	content []byte
	exists  bool
	mode    os.FileMode

	// placed is what this transaction itself left on the target; placedAbsent
	// says the intended post-state is absence rather than bytes. Recovery
	// compares the disk against it before undoing anything, so it only ever
	// takes back its own effect.
	placed       []byte
	placedAbsent bool
	placedSet    bool
}

// recordedSnapshots is Core's rollback ledger for one proposal, keyed by the
// absolute target path the transaction mutated.
type recordedSnapshots struct {
	byTarget map[string]*snapshot
}

func newRecordedSnapshots() *recordedSnapshots {
	return &recordedSnapshots{byTarget: make(map[string]*snapshot)}
}

// record captures a target's current bytes so a later failure can put them back.
//
// It is idempotent per target, which matters because a proposal may touch the
// same file twice: capturing on the second visit would overwrite the original
// with the already-mutated content, and the rollback would then restore the
// wrong bytes.
//
// A target that does not exist is recorded as an absence rather than skipped.
// That distinction is the whole reason the ledger exists: "restore these bytes"
// and "delete what this transaction created" are different actions, and only
// the pre-state knows which one applies.
func (r *recordedSnapshots) record(s *ConcreteSubstrate, target string) error {
	if _, ok := r.byTarget[target]; ok {
		return nil
	}
	data, err := os.ReadFile(target)
	if err != nil {
		if os.IsNotExist(err) {
			r.byTarget[target] = &snapshot{path: target, exists: false}
			return nil
		}
		return err
	}
	info, _ := os.Stat(target)
	mode := os.FileMode(0o644)
	if info != nil {
		mode = info.Mode().Perm()
	}
	r.byTarget[target] = &snapshot{
		path:    target,
		content: append([]byte(nil), data...),
		exists:  true,
		mode:    mode,
	}
	return nil
}

// note records what the transaction intends to leave on a target.
//
// This is the attribution half of the ledger. A rollback that restores a target
// unconditionally is only correct while nothing else is writing: the first time
// two transactions touch the same destination, the one that loses the race can
// restore its own idea of the file over the winner's work, which is not recovery
// but data loss.
//
// Recording what we are about to place is what lets recovery tell the two cases
// apart. Bytes on disk that match what this transaction placed are ours to undo.
// Bytes that do not belong to somebody else, and must be left alone.
//
// It is deliberately recorded before the commit rather than from the commit's
// result. The whole point is that an operation whose effect could not be proven —
// including one whose verification failed because a concurrent writer got there
// first — still has to be recognised as this transaction's attempt, so the ledger
// knows what it was reaching for.
func (r *recordedSnapshots) note(target string, content []byte, absent bool) {
	sp, ok := r.byTarget[target]
	if !ok {
		return
	}
	sp.placed = append([]byte(nil), content...)
	sp.placedAbsent = absent
	sp.placedSet = true
}

// rollbackRecorded restores every target this proposal can prove it changed.
//
// It restores even when the caller's context is already cancelled or its deadline
// has passed, and it restores unconditionally, because a rollback that skipped
// the targets it could not reach would leave a workspace that is neither the one
// the user had nor the one the runtime said it produced. Those writes go through
// the substrate FilePort under a context detached from the caller's, for the
// same reason: the caller's cancellation withdraws the execution, it does not
// withdraw the undoing of it.
//
// What it will not do is overwrite a destination it no longer owns. If what is on
// disk differs from what this transaction placed, another writer got there first,
// and restoring would destroy work this transaction never did.
func (s *ConcreteSubstrate) rollbackRecorded(ctx context.Context, snaps *recordedSnapshots) {
	if snaps == nil {
		return
	}
	//nolint:contextcheck // rollback outlives the caller's context by design
	undoCtx := context.WithoutCancel(ctx)
	for _, sp := range snaps.byTarget {
		if !sp.placedSet {
			// Never reached the commit, so there is nothing of ours to undo.
			continue
		}
		s.undoTarget(undoCtx, sp)
	}
}

// undoTarget reverses one target's effect, but only while the destination still
// holds what this transaction put there.
func (s *ConcreteSubstrate) undoTarget(ctx context.Context, sp *snapshot) {
	current, present, regular := readForAttribution(sp.path)

	if sp.placedAbsent {
		// A removal's post-state is absence. If the destination is gone the
		// removal took and the original is ours to put back. If something is
		// there, either the removal never happened — in which case the original
		// is already in place and there is nothing to do — or another writer
		// recreated it, which is likewise not ours to remove.
		if !present && sp.exists {
			s.writeForRecovery(ctx, sp.path, sp.content, sp.mode)
		}
		return
	}

	if !present || !regular {
		// Gone, or no longer a file this transaction wrote. Nothing to undo.
		return
	}
	if !bytes.Equal(current, sp.placed) {
		// Somebody else's bytes are on disk. Leaving them is the only safe move;
		// the failure that triggered this rollback is already in the proof.
		return
	}
	if sp.exists {
		s.writeForRecovery(ctx, sp.path, sp.content, sp.mode)
		return
	}
	s.removeForRecovery(ctx, sp.path)
}

// readForAttribution reads a target's current bytes for the ownership check.
//
// The third answer matters: a destination that has become a directory, or is no
// longer readable, is not this transaction's file, and must not be treated as
// either "still ours" or "somebody else's bytes to compare".
func readForAttribution(path string) (content []byte, present, regular bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, false
	}
	return data, true, true
}

// writeForRecovery puts a captured original back.
func (s *ConcreteSubstrate) writeForRecovery(ctx context.Context, path string, content []byte, mode os.FileMode) {
	if s.delegate != nil && s.delegate.file != nil {
		_ = s.delegate.file.Write(ctx, path, string(content))
		return
	}
	_ = os.WriteFile(path, content, mode)
}

// removeForRecovery deletes a target this transaction created.
func (s *ConcreteSubstrate) removeForRecovery(ctx context.Context, path string) {
	if s.delegate != nil && s.delegate.file != nil {
		_ = s.delegate.file.Remove(ctx, path)
		return
	}
	_ = os.Remove(path)
}
