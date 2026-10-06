package execution

// ── R6-C: PROCESS / CRASH FAILURE FORENSICS (execution bounds) ──────────────
//
// These tests pin the executor-side failure boundaries that sit closest to the
// workspace and the process:
//
//	C2 command failure   → a non-zero exit is a failure, never a success
//	C3 before mutation    → an authorization refusal leaves a zero delta
//	C4 during mutation    → a partial apply rolls back atomically
//	C6 during verification→ a failed gate restores the pre-apply bytes
//	C6 verification loss  → a command that never ran is NEVER reported PASSED
//	C7 panic after write  → a panic is recovered to a FAILURE, never a success
//
// The R6-C question is not "does it recover" but "does it stay truthful". Every
// assertion below is an invariant about state, never about recovery machinery.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/core/authorization"
	capability "github.com/PizenLabs/izen/internal/execution/capability"
	"github.com/PizenLabs/izen/internal/infrastructure/capabilities"
)

// TestR6C_Verification_CancelledContextNeverPasses is the R6-C hard invariant:
//
//	verification absence != verification success
//
// A verification step whose command never started (the context was already
// withdrawn) must NOT be recorded as PASSED. Before the fix, runStep treated a
// non-nil Run error with ExitCode 0 as a pass — exactly the shape a
// context-cancelled exec.Start produces — so a cancelled verification could
// have gated a mutation as verified.
func TestR6C_Verification_CancelledContextNeverPasses(t *testing.T) {
	root := t.TempDir()
	v := NewVerifier(root)
	v.SetAuthorization(testAuth())
	v.SetCustomSteps([]VerificationStep{{Name: "noop", Command: "true"}})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the command can never start

	report := v.RunAll(ctx)
	if report.Skipped {
		t.Fatalf("gate reported SKIPPED; a contract existed and was attempted: %+v", report)
	}
	if report.Passed {
		t.Fatalf("a verification step whose command never ran was reported PASSED — verification absence became verification success: %+v", report)
	}
}

// TestR6C_Verification_FailingCommandFailsTheGate is the positive control: a
// step that genuinely runs and exits non-zero fails the gate.
func TestR6C_Verification_FailingCommandFailsTheGate(t *testing.T) {
	root := t.TempDir()
	v := NewVerifier(root)
	v.SetAuthorization(testAuth())
	v.SetCustomSteps([]VerificationStep{{Name: "fail", Command: "exit 5"}})

	report := v.RunAll(context.Background())
	if report.Passed {
		t.Fatalf("a non-zero verification command was reported PASSED: %+v", report)
	}
	if report.Skipped {
		t.Fatalf("gate reported SKIPPED instead of a real failure: %+v", report)
	}
}

// TestR6C_VerificationFailureRestoresTheWorkspace is mutation-boundary case C6:
// the mutation is applied, the deterministic gate runs and FAILS, and the
// pre-apply bytes are restored. The resulting workspace state is definitively
// unchanged — not partial, not "unknown".
func TestR6C_VerificationFailureRestoresTheWorkspace(t *testing.T) {
	root := t.TempDir()
	const target = "file.txt"
	const original = "original content"
	writeTarget(t, root, target, original)

	pm := NewPatchManager(root)
	pm.SetAuthorization(testAuth())
	ms := NewMutationSet()
	pm.SetMutationSet(ms)
	pm.SetVerifier(failingVerifier(root))

	patch := &Patch{ID: "r6c-verify-fail", File: target, Original: original, Modified: "patched content"}
	err := pm.ApplyContext(context.Background(), patch)
	if err == nil {
		t.Fatal("a failed verification gate did not fail the apply")
	}
	if got := readFileString(t, root, target); got != original {
		t.Fatalf("verification failure left the workspace changed: %q — a failed gate must restore the pre-apply bytes", got)
	}
	if ms.Committed() {
		t.Fatal("a verification-failed mutation boundary was committed")
	}
}

// TestR6C_AuthorizationFailureBeforeMutationLeavesZeroDelta is case C3:
// authorized != applied. A mutation that fails the authorization check before
// the write must leave the workspace byte-identical.
func TestR6C_AuthorizationFailureBeforeMutationLeavesZeroDelta(t *testing.T) {
	root := t.TempDir()
	const target = "file.txt"
	const original = "original content"
	writeTarget(t, root, target, original)

	pm := NewPatchManager(root)
	pm.SetAuthorization(&authorization.MutationAuthorization{
		ID:        authorization.NewAuthorizationID(),
		ExpiresAt: time.Now().Add(-time.Hour), // expired BEFORE any apply
	})

	patch := &Patch{ID: "r6c-auth-fail", File: target, Original: original, Modified: "patched content"}
	if err := pm.ApplyContext(context.Background(), patch); err == nil {
		t.Fatal("an unauthorized mutation applied anyway")
	}
	if got := readFileString(t, root, target); got != original {
		t.Fatalf("authorization failure mutated the workspace: %q", got)
	}
}

// TestR6C_MutationSetPartialApplyRollsBackAtomically is case C4 at the narrowest
// seam the architecture owns: the MutationSet transaction. A multi-target apply
// that lands one file and then fails rolls the WHOLE boundary back, so the
// post-failure state is definitively the pre-mutation state.
func TestR6C_MutationSetPartialApplyRollsBackAtomically(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a.txt")
	b := filepath.Join(root, "b.txt")
	if err := os.WriteFile(a, []byte("A0"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("B0"), 0o644); err != nil {
		t.Fatal(err)
	}

	ms := NewMutationSet()
	if err := ms.Record(a); err != nil {
		t.Fatalf("record a: %v", err)
	}
	if err := ms.Record(b); err != nil {
		t.Fatalf("record b: %v", err)
	}

	// Partial application: a is written, b never is (the failure happens
	// between the two writes).
	if err := os.WriteFile(a, []byte("A1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if errs := ms.RollbackTo(MutationFailed); len(errs) > 0 {
		t.Fatalf("rollback errors: %v", errs)
	}

	if got, _ := os.ReadFile(a); string(got) != "A0" {
		t.Fatalf("a.txt = %q after rollback, want A0", got)
	}
	if got, _ := os.ReadFile(b); string(got) != "B0" {
		t.Fatalf("b.txt = %q after rollback, want B0", got)
	}
	if ms.Committed() {
		t.Fatal("a rolled-back mutation set reports committed")
	}
}

// TestR6C_PanicAfterWriteIsRecoveredAsFailureNotSuccess is case C7 at the one
// real panic boundary the execution path owns: PatchManager.ApplyContext
// recovers a panic from the apply body and converts it to an ERROR. It must
// never become a success, and it must never commit the mutation boundary.
//
// The write has ALREADY landed when the injected panic fires (the callback runs
// immediately after the kernel write). This is the exact post-mutation /
// pre-terminal window: the boundary refuses to report it as complete, and the
// enclosing transaction owner is responsible for the rollback (as the comment
// on ApplyContext states).
func TestR6C_PanicAfterWriteIsRecoveredAsFailureNotSuccess(t *testing.T) {
	root := t.TempDir()
	const target = "file.txt"
	const original = "original content"
	writeTarget(t, root, target, original)

	pm := NewPatchManager(root)
	pm.SetAuthorization(testAuth())
	ms := NewMutationSet()
	pm.SetMutationSet(ms)
	pm.SetOnMutation(func(string, []byte) { panic("injected post-write panic") })

	patch := &Patch{ID: "r6c-panic", File: target, Original: original, Modified: "patched content"}
	err := pm.ApplyContext(context.Background(), patch)
	if err == nil {
		t.Fatal("a panic inside apply was swallowed into a success")
	}
	if !strings.Contains(err.Error(), "panic") {
		t.Fatalf("err = %v, want the recovered panic surfaced as an error", err)
	}
	if ms.Committed() {
		t.Fatal("a panicked apply committed the mutation boundary")
	}
	// The bytes had already reached disk before the panic: the apply boundary
	// itself performs no rollback. The state is "changed on disk, no success
	// recorded" — the caller must roll the transaction back. Pin that fact
	// rather than assuming an atomic rollback that did not happen here.
	if got := readFileString(t, root, target); got != "patched content" {
		t.Fatalf("expected the write to have landed before the panic, got %q", got)
	}
}

// TestR6C_CommandNonZeroExitIsAFailure is case C2: a command that runs and exits
// non-zero is a failure, never a success, and therefore can never satisfy a
// behavioural requirement.
//
// NOTE (recorded limitation, classification E): the REAL shell adapter
// (shellCommandRunner) discards the ShellResult whenever the shell port returns
// an error — and the port returns an error for EVERY non-zero exit. The command
// is therefore reported under FailureCapabilityFailed with the reason "command
// could not be started", and the exit code / stdout / stderr are lost. The
// capability taxonomy itself expresses the correct outcome
// (FailureExecutionFailed + exit_code); only the adapter fails to use it. See
// R6_CRASH_FAILURE_REPORT.md §5. This test pins the invariant that survives the
// defect: the command is still a FAILURE.
func TestR6C_CommandNonZeroExitIsAFailure(t *testing.T) {
	root := t.TempDir()
	port := capabilities.NewExecShell(10 * time.Second)
	r := capability.NewRunner(root, capability.WithCommandRunner(shellCommandRunner{
		port:    port,
		dir:     root,
		root:    root,
		timeout: 10 * time.Second,
	}))
	r.SetGrant(capability.Grant{Provenance: "$prompt", Execute: true})

	res, ev, err := r.Command(context.Background(), capability.CommandRequest{Command: "exit 7"})
	if err == nil {
		t.Fatalf("a non-zero command exit was reported as success: res=%+v ev=%+v", res, ev)
	}
	if ev.OK {
		t.Fatalf("a non-zero command exit produced OK=true: %+v", ev)
	}
	if ev.Class == "" {
		t.Fatalf("a failed command carried no failure class: %+v", ev)
	}
}
