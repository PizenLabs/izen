package substrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/kernelbridge"
)

// Path A is the workspace mutation route `izen run` takes:
//
//	cmd/izen/runtime.go → app.Pipeline.Run → ConcreteSubstrate.Execute
//
// The tests below drive that surface directly and assert what it does at its
// edges. Two things make them worth having beyond the happy path.
//
// The first is that the final filesystem effect now crosses the Runtime Kernel
// rather than a FilePort, so every refusal and every proof that used to be an
// implicit consequence of a syscall has become an adjudicated outcome. These
// tests pin those outcomes, because the failure vocabulary is the part a caller
// depends on and the part a migration most easily reshapes by accident.
//
// The second is that `kernel PROVEN` and `objective PROVEN` are different claims
// and the proof keeps them apart. A test that only checked "the file has the
// right bytes" would pass equally well for a runtime that lied about everything
// else, so the assertions below read the evidence chain as well as the disk.

// pathA runs one proposal through Path A and returns its proof.
func pathA(t *testing.T, root, id string, ops ...Operation) (ExecutionProof, error) {
	t.Helper()
	return NewConcreteSubstrate(root).Execute(context.Background(), Proposal{ID: id, Intent: "path A", Operations: ops})
}

// onlyMutation returns the single recorded mutation evidence, failing when the
// proposal recorded none or more than one.
func onlyMutation(t *testing.T, proof ExecutionProof) MutationEvidence {
	t.Helper()
	if len(proof.Mutations) != 1 {
		t.Fatalf("proof carries %d mutation records; want exactly 1", len(proof.Mutations))
	}
	return proof.Mutations[0]
}

// TestPathA_WriteIsAdjudicatedByTheKernel walks the whole chain for a write.
//
// The point of asserting each link separately is that any one of them could be
// satisfied by a runtime that only pretended: the bytes on disk prove the write
// happened, the PROVEN outcome proves the contract was satisfied, the PASSED
// verify axis proves a check actually ran, and the recorded execution id is what
// makes the verdict correlatable with the log that produced it.
func TestPathA_WriteIsAdjudicatedByTheKernel(t *testing.T) {
	root := t.TempDir()
	const content = "// written through the kernel\n"

	proof, err := pathA(t, root, "p-write", Operation{
		Type: OpFileWrite, Target: "pkg/hello.txt", Content: []byte(content),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if proof.Status != "committed" {
		t.Fatalf("status = %q (%v); want committed", proof.Status, proof.Error)
	}

	// The bytes, read from the disk rather than from the capability that wrote
	// them, and in a directory the kernel had to create on the way.
	body, err := os.ReadFile(filepath.Join(root, "pkg", "hello.txt"))
	if err != nil {
		t.Fatalf("reading the written file: %v", err)
	}
	if string(body) != content {
		t.Errorf("content on disk = %q; want %q", string(body), content)
	}

	m := onlyMutation(t, proof)
	if m.Op != OpFileWrite {
		t.Errorf("op = %s; want %s", m.Op, OpFileWrite)
	}
	if m.Target != "pkg/hello.txt" {
		t.Errorf("target = %q; want the canonical workspace-relative destination", m.Target)
	}
	if m.Outcome != "PROVEN" {
		t.Errorf("outcome = %s (%s): %s; want PROVEN", m.Outcome, m.Class, m.Reason)
	}
	if m.Verify != "PASSED" {
		t.Errorf("verify = %s; want PASSED — a write nobody re-read is not a verified write", m.Verify)
	}
	if !m.Landed {
		t.Error("landed = false on a committed write")
	}
	if m.Contract == "" || m.ExecutionID == "" {
		t.Errorf("evidence is missing its chain: contract=%q execution=%q", m.Contract, m.ExecutionID)
	}

	// The durable proof artifact must carry the same chain. An evidence record
	// that only exists in memory is not evidence.
	artifact, err := os.ReadFile(proof.EvidencePath)
	if err != nil {
		t.Fatalf("reading the proof artifact: %v", err)
	}
	if !contains(string(artifact), m.Format()) {
		t.Errorf("proof artifact does not record the mutation:\n%s", artifact)
	}
}

// TestPathA_DeleteIsAdjudicatedByTheKernel is the DELETE counterpart: the
// removal is proven by a durable FILE_DELETED record plus a fresh observation
// that the destination is gone, not by os.Remove having returned nil.
func TestPathA_DeleteIsAdjudicatedByTheKernel(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "del.txt")
	if err := os.WriteFile(target, []byte("to delete"), 0o644); err != nil {
		t.Fatal(err)
	}

	proof, err := pathA(t, root, "p-delete", Operation{Type: OpFileDelete, Target: "del.txt"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if proof.Status != "committed" {
		t.Fatalf("status = %q (%v); want committed", proof.Status, proof.Error)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("the destination survived the delete; stat err = %v", err)
	}

	m := onlyMutation(t, proof)
	if !m.Deleted {
		t.Errorf("deleted = false; outcome %s, reason %q", m.Outcome, m.Reason)
	}
	if m.Outcome != "PROVEN" || m.Verify != "PASSED" {
		t.Errorf("outcome/verify = %s/%s; want PROVEN/PASSED", m.Outcome, m.Verify)
	}
	if m.Vanished {
		t.Error("vanished = true for a destination that was actually removed")
	}
}

// TestPathA_DeleteOfAnAbsentTargetIsNotClaimedAsRemoval pins the distinction
// Path A has always made and a bare os.Remove made invisible.
//
// Removing a destination that is not there is not a failure — the desired end
// state already holds — but it is equally not a removal. Reporting it as one
// would let a proposal claim to have deleted a file it never touched, and the
// evidence would be the only place that difference could be seen.
func TestPathA_DeleteOfAnAbsentTargetIsNotClaimedAsRemoval(t *testing.T) {
	root := t.TempDir()

	proof, err := pathA(t, root, "p-delete-absent", Operation{Type: OpFileDelete, Target: "never-existed.txt"})
	if err != nil {
		t.Fatalf("deleting an absent target must stay a tolerated no-op, got: %v", err)
	}
	if proof.Status != "committed" {
		t.Fatalf("status = %q; want committed", proof.Status)
	}

	m := onlyMutation(t, proof)
	if !m.Vanished {
		t.Errorf("vanished = false; the kernel observed the destination absent (%s: %s)", m.Outcome, m.Reason)
	}
	if m.Deleted {
		t.Error("deleted = true for a destination nothing removed")
	}
	if m.Outcome == "PROVEN" {
		t.Error("outcome = PROVEN for a DELETE contract that removed nothing; " +
			"an unmet obligation must be reported, not rounded up to a success")
	}
}

// TestPathA_RefusesWriteOutsideTheWorkspace is the unauthorized-write case.
//
// Path A's authority was always "the workspace under this root", enforced by the
// substrate's FD-anchored use-time verification. Migrating the primitive must not
// have loosened it, and a target that climbs out of the root still has to fail
// closed with the same sentinel, before the kernel is asked to touch anything.
func TestPathA_RefusesWriteOutsideTheWorkspace(t *testing.T) {
	for _, tc := range []struct{ name, target string }{
		{"relative traversal", "../escaped.txt"},
		{"nested traversal", "a/../../escaped.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			outside := filepath.Join(filepath.Dir(root), "escaped.txt")

			proof, err := pathA(t, root, "p-escape", Operation{
				Type: OpFileWrite, Target: tc.target, Content: []byte("pwned\n"),
			})
			if !errors.Is(err, ErrWorkspaceEscape) {
				t.Fatalf("err = %v; want ErrWorkspaceEscape", err)
			}
			if proof.Status != "failed" {
				t.Errorf("status = %q; want failed", proof.Status)
			}
			if _, statErr := os.Stat(outside); !os.IsNotExist(statErr) {
				t.Fatalf("a target outside the workspace was written anyway; stat err = %v", statErr)
			}
		})
	}
}

// TestPathA_RefusesDeleteOutsideTheWorkspace is the unauthorized-delete case,
// and it matters separately because a delete that escapes takes something with
// it rather than adding something.
func TestPathA_RefusesDeleteOutsideTheWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "victim.txt")
	if err := os.WriteFile(outside, []byte("survivor"), 0o644); err != nil {
		t.Fatal(err)
	}

	proof, err := pathA(t, root, "p-escape-del", Operation{Type: OpFileDelete, Target: "../victim.txt"})
	if !errors.Is(err, ErrWorkspaceEscape) {
		t.Fatalf("err = %v; want ErrWorkspaceEscape", err)
	}
	if proof.Status != "failed" {
		t.Errorf("status = %q; want failed", proof.Status)
	}
	body, readErr := os.ReadFile(outside)
	if readErr != nil || string(body) != "survivor" {
		t.Fatalf("the file outside the workspace was removed or altered: %q (%v)", string(body), readErr)
	}
}

// TestPathA_RefusesSymlinkEscape covers the case lexical containment cannot see:
// a target that is lexically inside the root but resolves outside it. A symlink
// inside the workspace pointing at a sibling directory passes every lexical
// check, which is exactly why the check that catches it is a real one.
func TestPathA_RefusesSymlinkEscape(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   OperationType
	}{
		{"write", OpFileWrite},
		{"delete", OpFileDelete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			outsideDir := t.TempDir()
			victim := filepath.Join(outsideDir, "victim.go")
			if err := os.WriteFile(victim, []byte("package victim\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outsideDir, filepath.Join(root, "link")); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}

			proof, err := pathA(t, root, "p-symlink", Operation{
				Type: tc.op, Target: "link/victim.go", Content: []byte("package escaped\n"),
			})
			if !errors.Is(err, ErrWorkspaceEscape) {
				t.Fatalf("err = %v; want ErrWorkspaceEscape", err)
			}
			if proof.Status != "failed" {
				t.Errorf("status = %q; want failed", proof.Status)
			}

			// The victim outside the workspace must be exactly as it was: not
			// overwritten through the link, and not removed through it.
			body, readErr := os.ReadFile(victim)
			if readErr != nil {
				t.Fatalf("the delete escaped the workspace and removed %s: %v", victim, readErr)
			}
			if string(body) != "package victim\n" {
				t.Errorf("the write escaped the workspace and altered %s: %q", victim, body)
			}
		})
	}
}

// TestPathA_RefusesEmptyTarget is the degenerate destination. An empty target
// names no file; joining it to the root would silently produce the root
// directory itself, which is not something any proposal asked to mutate.
func TestPathA_RefusesEmptyTarget(t *testing.T) {
	for _, op := range []OperationType{OpFileWrite, OpFileDelete} {
		t.Run(string(op), func(t *testing.T) {
			root := t.TempDir()
			proof, err := pathA(t, root, "p-empty", Operation{Type: op, Target: ""})
			if err == nil {
				t.Fatalf("an empty target was accepted")
			}
			if proof.Status != "failed" {
				t.Errorf("status = %q; want failed", proof.Status)
			}
			if len(proof.Mutations) != 0 {
				t.Errorf("a refused target still produced %d mutation record(s); nothing should have been requested", len(proof.Mutations))
			}
		})
	}
}

// TestPathA_RefusesDirectoryTarget is the case where the target kind, not its
// existence, is the problem. Removing a tree is a different operation with a
// different blast radius than removing one file, so the kernel refuses it rather
// than quietly widening what FILE_DELETE means.
func TestPathA_RefusesDirectoryTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}

	proof, err := pathA(t, root, "p-dir", Operation{Type: OpFileDelete, Target: "dir"})
	if err == nil {
		t.Fatal("deleting a directory through FILE_DELETE was accepted")
	}
	if proof.Status != "failed" {
		t.Errorf("status = %q; want failed", proof.Status)
	}
	if _, statErr := os.Stat(filepath.Join(root, "dir")); statErr != nil {
		t.Fatalf("the directory was removed anyway: %v", statErr)
	}

	// Core's snapshot admission refuses this before the kernel is ever asked: a
	// directory is not a file this transaction could restore, so there is
	// nothing to snapshot and nothing to undo. That refusal is why no execution
	// ran, and the proof must say so rather than carry a verdict nothing
	// adjudicated.
	if len(proof.Mutations) != 0 {
		t.Errorf("proof carries %d mutation record(s) for an operation that never reached the kernel", len(proof.Mutations))
	}
}

// TestPathA_CancellationMutatesNothing is the cancellation case.
func TestPathA_CancellationMutatesNothing(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	proof, err := NewConcreteSubstrate(root).Execute(ctx, Proposal{
		ID: "p-cancel", Intent: "cancelled",
		Operations: []Operation{{Type: OpFileWrite, Target: "cancelled.txt", Content: []byte("never\n")}},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v; want context.Canceled", err)
	}
	if proof.Status != "failed" {
		t.Errorf("status = %q; want failed", proof.Status)
	}
	if _, statErr := os.Stat(filepath.Join(root, "cancelled.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("a cancelled execution wrote to the workspace; stat err = %v", statErr)
	}
}

// TestPathA_DeadlineMutatesNothing is the deadline case. It is the same
// withdrawal as cancellation arriving by a different route, and the difference
// matters because `izen run` bounds every cycle with one.
func TestPathA_DeadlineMutatesNothing(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	proof, err := NewConcreteSubstrate(root).Execute(ctx, Proposal{
		ID: "p-deadline", Intent: "expired",
		Operations: []Operation{{Type: OpFileWrite, Target: "expired.txt", Content: []byte("never\n")}},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v; want context.DeadlineExceeded", err)
	}
	if proof.Status != "failed" {
		t.Errorf("status = %q; want failed", proof.Status)
	}
	if _, statErr := os.Stat(filepath.Join(root, "expired.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("an expired execution wrote to the workspace; stat err = %v", statErr)
	}
}

// TestPathA_UnprovenWriteFailsTheOperation is the case that separates "the
// syscall returned nil" from "the runtime can prove what it did".
//
// The destination's directory exists and is not writable, so the kernel's
// attempt to stage the replacement file fails and no write evidence is ever
// produced. The operation must fail on that absence of evidence. Under the
// legacy FilePort the same failure surfaced as a raw permission error from a
// syscall, which is easy to swallow and carried no statement at all about
// whether anything had happened.
func TestPathA_UnprovenWriteFailsTheOperation(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions; this case cannot be constructed")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.MkdirAll(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	proof, err := pathA(t, root, "p-unproven", Operation{
		Type: OpFileWrite, Target: "locked/child.txt", Content: []byte("nope\n"),
	})
	if err == nil {
		t.Fatal("a write the kernel could not perform was reported as a success")
	}
	if proof.Status != "failed" {
		t.Errorf("status = %q; want failed", proof.Status)
	}
	if _, statErr := os.Stat(filepath.Join(locked, "child.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("the refused write left a file behind; stat err = %v", statErr)
	}

	// An execution really did run here, so there must be exactly one record,
	// and it must not claim a landing.
	m := onlyMutation(t, proof)
	if m.Outcome == "PROVEN" {
		t.Fatalf("outcome = PROVEN for a write that never happened; reason = %q", m.Reason)
	}
	if m.Landed {
		t.Error("landed = true on an execution with no durable write behind it")
	}
	if m.Verify == "PASSED" {
		t.Error("verify = PASSED on an execution whose write produced no evidence")
	}
	if m.Class == "" {
		t.Error("the refusal carries no failure class, so a reader cannot tell a refused write from a broken one")
	}
}

// TestPathA_RollsBackPartiallyExecutedBatch is rollback after a partially
// executed mutation.
//
// The first operation is proven by the kernel and lands; the second is one this
// runtime cannot execute. The transaction, not the primitive, is what reports
// failure — which is why the first operation's evidence says PROVEN while the
// proof as a whole says failed. Those two facts are exactly what a single status
// field could not carry.
func TestPathA_RollsBackPartiallyExecutedBatch(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "keep.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	proof, err := pathA(t, root, "p-partial",
		Operation{Type: OpFileWrite, Target: "keep.txt", Content: []byte("changed")},
		Operation{Type: "NOT_AN_OPERATION", Target: "x"},
	)
	if err == nil {
		t.Fatal("a proposal containing an unknown operation was reported as committed")
	}
	if proof.Status != "failed" {
		t.Errorf("status = %q; want failed", proof.Status)
	}

	body, readErr := os.ReadFile(filepath.Join(root, "keep.txt"))
	if readErr != nil || string(body) != "original" {
		t.Fatalf("rollback did not restore the original: %q (%v)", string(body), readErr)
	}
	if len(proof.Mutations) != 1 {
		t.Fatalf("proof carries %d mutation records; want 1 — the aborted operation never reached the kernel", len(proof.Mutations))
	}
	if !proof.Mutations[0].Landed {
		t.Error("the first operation's evidence does not record that it landed; " +
			"a rollback that erases the fact a write happened cannot taint what it undid")
	}
}

// TestPathA_TransactionAbortRestoresADeletedFile is rollback after a delete,
// which is the harder direction: the original bytes still exist, but only in the
// snapshot, because the transaction removed the copy the workspace had.
func TestPathA_TransactionAbortRestoresADeletedFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "victim.txt"), []byte("restore me"), 0o644); err != nil {
		t.Fatal(err)
	}

	proof, err := pathA(t, root, "p-abort",
		Operation{Type: OpFileDelete, Target: "victim.txt"},
		Operation{Type: "NOT_AN_OPERATION", Target: "x"},
	)
	if err == nil {
		t.Fatal("a proposal containing an unknown operation was reported as committed")
	}
	if proof.Status != "failed" {
		t.Errorf("status = %q; want failed", proof.Status)
	}

	body, readErr := os.ReadFile(filepath.Join(root, "victim.txt"))
	if readErr != nil || string(body) != "restore me" {
		t.Fatalf("rollback did not restore the deleted file: %q (%v)", string(body), readErr)
	}
	if !proof.Mutations[0].Deleted {
		t.Error("the delete's evidence does not record that it happened before the abort")
	}
}

// TestPathA_ContendedCommitsAreDetectedAndNeverClobber is the concurrent
// out-of-band modification case.
//
// Many transactions commit to the same destination at once. Whatever order they
// interleave in, three things must hold, and all three are asserted on a
// property that is true of every interleaving rather than on one that has to be
// won.
//
// First, the bytes on disk must be exactly one writer's whole payload: never a
// prefix, never a blend. That is what an atomic placement buys over a
// truncating write.
//
// Second, a writer whose content was replaced before the kernel re-read it must
// be told it failed. This is the property the migration introduces and the one a
// bare syscall cannot have: the destination's content is checked by code that
// did not write it, so a lost race is reported instead of silently believed.
//
// Third — and this is the one that matters most — the writers that lost must not
// undo the winner. A rollback that restores its own idea of the file over
// somebody else's committed work is not recovery, it is data loss, and it is
// the failure mode a newly-detecting migration is most likely to introduce.
func TestPathA_ContendedCommitsAreDetectedAndNeverClobber(t *testing.T) {
	root := t.TempDir()
	const writers = 8
	payload := func(i int) string { return fmt.Sprintf("writer-%d payload\n", i) }

	// Each writer runs on its own substrate rather than through the pathA helper:
	// that helper reports failures through *testing.T, and t.Fatalf from a
	// non-test goroutine stops the wrong goroutine. Collecting the results here
	// keeps a lost race visible instead of turning it into a missing file.
	type result struct {
		proof ExecutionProof
		err   error
	}
	results := make([]result, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i].proof, results[i].err = NewConcreteSubstrate(root).Execute(context.Background(), Proposal{
				ID:     fmt.Sprintf("p-race-%d", i),
				Intent: "contended commit",
				Operations: []Operation{{
					Type: OpFileWrite, Target: "contended.txt", Content: []byte(payload(i)),
				}},
			})
		}(i)
	}
	wg.Wait()

	// A writer that committed must be corroborated by its own evidence: the
	// kernel proved that exact content was on disk when it checked.
	committed := 0
	for i, res := range results {
		if res.err != nil {
			if res.proof.Status != "failed" {
				t.Errorf("writer %d failed but reported status %q", i, res.proof.Status)
			}
			continue
		}
		committed++
		if len(res.proof.Mutations) != 1 || !res.proof.Mutations[0].Landed {
			t.Errorf("writer %d reported success with no proven landing: %+v", i, res.proof.Mutations)
		}
	}
	if committed == 0 {
		t.Fatalf("no writer committed; the test proves nothing (errors: %v)", results[0].err)
	}

	// Whatever happened above, the destination must still hold one whole
	// payload. A file that is missing, truncated, or blended means a rollback
	// reached past its own effect.
	body, err := os.ReadFile(filepath.Join(root, "contended.txt"))
	if err != nil {
		t.Fatalf("the contended destination is gone: %v\n"+
			"A writer whose commit lost the race rolled its rollback over the winner's file.", err)
	}
	matched := -1
	for i := range writers {
		if string(body) == payload(i) {
			matched = i
		}
	}
	if matched < 0 {
		t.Fatalf("content on disk = %q; want exactly one writer's whole payload", string(body))
	}

	// The strongest form of the guarantee: whatever is on disk belongs to a
	// writer that committed. A writer whose content was lost must have taken its
	// own bytes back with it — either by restoring the original or by removing
	// what it created — while leaving every other writer's work alone. Payloads
	// are distinct, so a surviving payload from a failed writer would mean its
	// rollback neither undid its own effect nor respected anyone else's.
	if results[matched].err != nil {
		t.Errorf("the payload on disk belongs to writer %d, which reported failure: %v\n"+
			"A writer that lost its commit left its own bytes behind.", matched, results[matched].err)
	}
}

// TestMutationEvidenceNeverClaimsALandingTheKernelRefused is the projection's
// own honesty check, and it is the one that keeps the evidence chain meaningful.
//
// A refused Apply carries no write evidence and no adjudicated outcome. If the
// projection into Core evidence inferred a landing anyway — from the request, or
// from the mere fact that a destination was named — then every consumer
// downstream, including the transaction that decides whether to roll back, would
// be reasoning from a claim the kernel never made.
func TestMutationEvidenceNeverClaimsALandingTheKernelRefused(t *testing.T) {
	root := t.TempDir()
	applied := refusedApplyForTest(t, root)
	m := mutationEvidenceFor(OpFileWrite, "pkg/thing.go", applied)

	if m.Outcome == "PROVEN" {
		t.Fatalf("a refused execution projected as PROVEN; reason = %q", m.Reason)
	}
	if m.Landed || m.Deleted || m.Vanished {
		t.Errorf("a refused execution projected as landed=%t deleted=%t vanished=%t", m.Landed, m.Deleted, m.Vanished)
	}
	if m.Verify == "PASSED" {
		t.Error("a refused execution projected a passed verification")
	}
	if m.Target != "pkg/thing.go" {
		t.Errorf("target = %q; a refusal must still name the destination it refused", m.Target)
	}
	if m.Class == "" {
		t.Error("a refusal projected without a failure class; the class is what tells a human must decide from the runtime broke")
	}
}

// refusedApplyForTest produces a real refused kernel execution rather than a
// hand-built literal, so the projection is tested against the shape the seam
// really returns on a refusal.
func refusedApplyForTest(t *testing.T, root string) kernelbridge.Applied {
	t.Helper()
	applied := kernelbridge.Apply(context.Background(), root, []kernelbridge.Write{{
		Target:   "../outside.txt",
		Content:  "nope\n",
		Contract: kernelbridge.ContractPatch,
	}})
	if applied.Proven() {
		t.Fatalf("a write outside the workspace was proven; outcome = %s", applied.Outcome)
	}
	return applied
}

// contains reports whether the proof artifact recorded the mutation. It exists
// so the artifact assertions read as intent rather than as string plumbing.
func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }
