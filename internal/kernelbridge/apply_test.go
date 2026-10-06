package kernelbridge_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/kernelbridge"
	"github.com/PizenLabs/izen/runtime/kernel"
)

// TestApply_WritesRealBytesAndProvesThem drives the write chain through the seam
// and asserts each link separately.
//
// These tests prove the SEAM. They do not prove the product uses it; that is
// internal/ui/toolcall_write_kernel_test.go, which drives the real approval key
// path.

// TestApply_WritesRealBytesAndProvesThem walks event → state → evidence →
// verification → PROVEN, and confirms the bytes are on disk afterwards.
func TestApply_WritesRealBytesAndProvesThem(t *testing.T) {
	root := t.TempDir()
	const target = "pkg/thing.go"
	const content = "package pkg\n"

	applied := kernelbridge.Apply(context.Background(), root, []kernelbridge.Write{{
		Target:   target,
		Content:  content,
		Contract: kernelbridge.ContractCreate,
	}})

	if !applied.Proven() {
		t.Fatalf("outcome = %s (%s): %s; want PROVEN", applied.Outcome, applied.Class, applied.Reason)
	}
	if applied.Contract != kernel.ContractCreate {
		t.Errorf("contract = %s; want the declared %s", applied.Contract, kernelbridge.ContractCreate)
	}
	if applied.State.Mutation != kernel.MutationApplied {
		t.Errorf("mutation axis = %s; want %s", applied.State.Mutation, kernel.MutationApplied)
	}
	if applied.Verify != kernel.VerifyPassed {
		t.Errorf("verify axis = %s; want %s", applied.Verify, kernel.VerifyPassed)
	}
	if !applied.Landed(target) {
		t.Error("Landed reported false on a PROVEN execution")
	}
	if !applied.Created(target) {
		t.Error("Created reported false; the destination was observed absent immediately before the write")
	}

	// The bytes, from the disk rather than from the capability.
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(target)))
	if err != nil {
		t.Fatalf("reading the written file: %v", err)
	}
	if string(body) != content {
		t.Errorf("content on disk = %q; want %q", string(body), content)
	}

	// A creation declares the stronger obligation, so the target must be observed
	// present as well as written. Losing either clause would let a PROVEN verdict
	// describe a file that does not exist.
	if !applied.Exists(target) {
		t.Error("the execution did not prove the target present after writing it")
	}
	if wrote := applied.Written(); len(wrote) != 1 || wrote[0] != target {
		t.Errorf("Written = %v; want exactly [%s]", wrote, target)
	}
}

// TestApply_OverwriteIsNotACreation is the semantic boundary between the two
// contracts' observable consequences, and the case a guess gets wrong.
func TestApply_OverwriteIsNotACreation(t *testing.T) {
	root := t.TempDir()
	const target = "existing.go"
	writeFile(t, filepath.Join(root, target), "before\n")

	applied := kernelbridge.Apply(context.Background(), root, []kernelbridge.Write{{
		Target:   target,
		Content:  "after\n",
		Contract: kernelbridge.ContractPatch,
	}})

	if !applied.Proven() {
		t.Fatalf("outcome = %s: %s; want PROVEN", applied.Outcome, applied.Reason)
	}
	if applied.Created(target) {
		t.Error("Created reported true for an overwrite; the destination existed before the execution")
	}
	if !applied.Landed(target) {
		t.Error("Landed reported false on a PROVEN overwrite")
	}
	body, err := os.ReadFile(filepath.Join(root, target))
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if string(body) != "after\n" {
		t.Errorf("content = %q; want the replacement", string(body))
	}
}

// TestApply_AnEmptyExistingFileIsNotACreation is the case the deleted
// `orig == ""` heuristic could not express at all.
//
// A zero-byte file is present and empty. Reading it returns no bytes, and a
// length-based observation of "no content" must never be mistaken for "no file".
func TestApply_AnEmptyExistingFileIsNotACreation(t *testing.T) {
	root := t.TempDir()
	const target = "empty.go"
	writeFile(t, filepath.Join(root, target), "")

	applied := kernelbridge.Apply(context.Background(), root, []kernelbridge.Write{{
		Target:   target,
		Content:  "now populated\n",
		Contract: kernelbridge.ContractPatch,
	}})

	if !applied.Proven() {
		t.Fatalf("outcome = %s: %s; want PROVEN", applied.Outcome, applied.Reason)
	}
	if applied.Created(target) {
		t.Error("a zero-byte file was reported as created; nothing was created")
	}
	if !applied.Exists(target) {
		t.Error("the empty target was not proven present after the write")
	}
}

// TestApply_VerificationIsARealOrderedPass proves the write's verification is a
// check rather than a decoration.
//
// It asserts the spine: mutation first, then verification started, then
// verification passed. The reduction refuses a pass with no started check behind
// it, so this cannot be satisfied by a log line alone.
//
// The verifier's FAIL branch — on-disk content differing from the request — is
// the check that gives a mutation its meaning, and it is deliberately not faked
// here. Producing it requires the file to change between the write and the
// re-read, which from outside the seam needs a hook the seam does not expose and
// should not expose: a caller able to interpose between a capability and its
// verifier could also interpose between a mutation and its verification. The
// branch is reachable in production by exactly the thing it exists to catch, which
// is a concurrent writer — and a runtime that cannot survive that is one that
// should fail loudly.
func TestApply_VerificationIsARealOrderedPass(t *testing.T) {
	root := t.TempDir()
	const target = "checked.go"

	applied := kernelbridge.Apply(context.Background(), root, []kernelbridge.Write{{
		Target:   target,
		Content:  "package checked\n",
		Contract: kernelbridge.ContractCreate,
	}})

	if !applied.Proven() {
		t.Fatalf("outcome = %s: %s; want PROVEN", applied.Outcome, applied.Reason)
	}
	positions := map[kernel.EventKind]int{}
	for i, e := range applied.Events {
		if _, seen := positions[e.Kind]; !seen {
			positions[e.Kind] = i
		}
	}
	for _, kind := range []kernel.EventKind{
		kernel.EventExecutionStarted,
		kernel.EventMutationApplied,
		kernel.EventVerificationStarted,
		kernel.EventVerificationPassed,
		kernel.EventExecutionFinished,
	} {
		if _, ok := positions[kind]; !ok {
			t.Fatalf("event %s is missing; a mutation whose verification is not in the log was not verified", kind)
		}
	}
	if positions[kernel.EventMutationApplied] > positions[kernel.EventVerificationStarted] {
		t.Error("the mutation happened after verification started")
	}
	if positions[kernel.EventVerificationStarted] > positions[kernel.EventVerificationPassed] {
		t.Error("verification passed before it started")
	}
	// A skipped check would satisfy the contract just as well while proving
	// nothing, so the axis value itself has to be asserted.
	if applied.Verify != kernel.VerifyPassed {
		t.Errorf("verify axis = %s; want %s", applied.Verify, kernel.VerifyPassed)
	}
}

// TestApply_RefusesADestinationOutsideTheWorkspace proves the mutating gate.
//
// The old path joined the working directory with whatever the model asked for, so
// `../` left the workspace with no grant behind it. Here the refusal is recorded
// as an authorization failure attributed to the step, in the kernel's own
// vocabulary.
func TestApply_RefusesADestinationOutsideTheWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "escaped.txt")
	t.Cleanup(func() { _ = os.Remove(outside) })

	applied := kernelbridge.Apply(context.Background(), root, []kernelbridge.Write{{
		Target:   "../escaped.txt",
		Content:  "nope\n",
		Contract: kernelbridge.ContractPatch,
	}})

	if applied.Proven() {
		t.Fatal("a destination outside the workspace reached PROVEN")
	}
	if !strings.Contains(string(applied.Outcome), string(kernel.OutcomeRequiresAuthorization)) &&
		applied.Outcome != kernel.OutcomeFailed {
		t.Errorf("outcome = %s (%s); want the kernel's authorization or capability refusal, got: %s",
			applied.Outcome, applied.Class, applied.Reason)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatalf("%s exists; the workspace boundary was crossed", outside)
	}
	if applied.Landed("../escaped.txt") {
		t.Error("Landed reported true for a refused destination")
	}
}

// TestApply_RefusesADestinationThatSymlinksOutsideTheWorkspace is the escape the
// lexical confinement checks cannot see.
//
// A symlink inside the workspace pointing at a directory outside it satisfies
// every lexical rule: no "..", and the joined path is textually under the root.
// Before the capability's real-path check, this reached PROVEN with the bytes
// written beyond the boundary — and the verifier CONFIRMED them there, because it
// followed the same symlink.
//
// This is asserted at the seam because that is where the claim is made. A grant
// names workspace-relative paths; a capability that can be redirected through a
// symlink is a capability no grant can reason about.
func TestApply_RefusesADestinationThatSymlinksOutsideTheWorkspace(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{root, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	applied := kernelbridge.Apply(context.Background(), root, []kernelbridge.Write{{
		Target:   "link/escaped.go",
		Content:  "package escaped\n",
		Contract: kernelbridge.ContractPatch,
	}})

	if applied.Proven() {
		t.Fatal("a write through a symlink leaving the workspace reached PROVEN")
	}
	if applied.Landed("link/escaped.go") {
		t.Error("Landed reported true for a refused destination")
	}
	if _, err := os.Stat(filepath.Join(outside, "escaped.go")); err == nil {
		t.Fatal("the workspace boundary was crossed through a symlink")
	}
	if !strings.Contains(applied.Reason, "outside the workspace root") {
		t.Errorf("reason = %q; want the refusal to name the boundary", applied.Reason)
	}
}

// TestApply_RefusesRatherThanTruncating proves an oversized write set is refused
// rather than silently trimmed.
//
// Truncation would produce an execution that looks complete while a write the
// caller approved never happened. The refusal is reported in the kernel's own
// outcome vocabulary, not as a Go error.
func TestApply_RefusesRatherThanTruncating(t *testing.T) {
	root := t.TempDir()

	writes := make([]kernelbridge.Write, 0, kernelbridge.MaxAppliedTargets+1)
	for i := 0; i <= kernelbridge.MaxAppliedTargets; i++ {
		writes = append(writes, kernelbridge.Write{
			Target:   filepath.ToSlash(filepath.Join("d", "f"+strconv.Itoa(i)+".txt")),
			Content:  "x\n",
			Contract: kernelbridge.ContractPatch,
		})
	}

	applied := kernelbridge.Apply(context.Background(), root, writes)

	if applied.Proven() {
		t.Fatal("an oversized write set reached PROVEN")
	}
	if applied.Outcome != kernel.OutcomeBudgetExhausted {
		t.Errorf("outcome = %s (%s): %s; want %s",
			applied.Outcome, applied.Class, applied.Reason, kernel.OutcomeBudgetExhausted)
	}
	// Nothing may have been written: refusal happens before dispatch.
	entries, err := os.ReadDir(filepath.Join(root, "d"))
	if err == nil && len(entries) > 0 {
		t.Errorf("%d entries were written by a refused request; a refusal must happen before any mutation",
			len(entries))
	}
}

// TestApply_RefusesAnAmbiguousWriteSet proves the seam refuses requests whose
// meaning is not decidable, rather than picking one reading.
func TestApply_RefusesAnAmbiguousWriteSet(t *testing.T) {
	root := t.TempDir()

	t.Run("repeated destination", func(t *testing.T) {
		applied := kernelbridge.Apply(context.Background(), root, []kernelbridge.Write{
			{Target: "a.txt", Content: "one\n", Contract: kernelbridge.ContractPatch},
			{Target: "./a.txt", Content: "two\n", Contract: kernelbridge.ContractPatch},
		})
		if applied.Proven() {
			t.Fatal("a destination named twice reached PROVEN; the evidence log could not say which write a record describes")
		}
		if applied.Outcome != kernel.OutcomeFailed {
			t.Errorf("outcome = %s (%s); want a refusal", applied.Outcome, applied.Class)
		}
		if _, err := os.Stat(filepath.Join(root, "a.txt")); err == nil {
			t.Error("the ambiguous request wrote anyway")
		}
	})

	t.Run("mixed contracts", func(t *testing.T) {
		applied := kernelbridge.Apply(context.Background(), root, []kernelbridge.Write{
			{Target: "a.txt", Content: "one\n", Contract: kernelbridge.ContractCreate},
			{Target: "b.txt", Content: "two\n", Contract: kernelbridge.ContractPatch},
		})
		if applied.Proven() {
			t.Fatal("a write set with two declared contracts reached PROVEN; one execution carries one contract")
		}
		if applied.Outcome != kernel.OutcomeFailed {
			t.Errorf("outcome = %s (%s); want a refusal", applied.Outcome, applied.Class)
		}
		entries, err := os.ReadDir(root)
		if err == nil && len(entries) > 0 {
			t.Errorf("%d files were written by a refused request", len(entries))
		}
	})

	t.Run("no declared contract", func(t *testing.T) {
		applied := kernelbridge.Apply(context.Background(), root, []kernelbridge.Write{
			{Target: "a.txt", Content: "one\n"},
		})
		if applied.Proven() {
			t.Fatal("a write with no declared obligation reached PROVEN")
		}
		if !strings.Contains(applied.Reason, string(kernelbridge.ContractPatch)) {
			t.Errorf("reason = %q; want the refusal to name the two admissible contracts", applied.Reason)
		}
	})

	t.Run("no destination", func(t *testing.T) {
		applied := kernelbridge.Apply(context.Background(), root, nil)
		if applied.Proven() {
			t.Fatal("an empty write set reached PROVEN")
		}
	})
}

// TestApply_AnUnusableWorkspaceIsAFailureNotACompletion proves the mutation axis
// is derived from what happened rather than from what was attempted.
//
// The workspace root here is a regular file, so the capability surface cannot be
// built at all. No write evidence exists, so the mutation axis must stay NONE and
// no write may be reported as landed. A runtime that set APPLIED on intent would
// announce a completed mutation for a workspace nobody changed.
func TestApply_AnUnusableWorkspaceIsAFailureNotACompletion(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "not-a-directory.go")
	writeFile(t, root, "I am a file\n")

	applied := kernelbridge.Apply(context.Background(), root, []kernelbridge.Write{
		{Target: "a.go", Content: "one\n", Contract: kernelbridge.ContractPatch},
	})

	if applied.Proven() {
		t.Fatal("a write against an unusable workspace root reached PROVEN")
	}
	// The request was refused before admission, so there is no state to read an
	// axis from. What must hold is that nothing is claimed: no written
	// destinations, nothing landed, no verification.
	if got := applied.Written(); len(got) != 0 {
		t.Errorf("Written = %v; want nothing", got)
	}
	if applied.Landed("a.go") {
		t.Error("Landed reported true for a workspace the runtime could not open")
	}
	if applied.Verify == kernel.VerifyPassed {
		t.Error("verification passed on a request that never reached the kernel")
	}
	if len(applied.Events) != 0 {
		t.Errorf("%d events recorded for a request that never ran; the log must not describe work that did not happen",
			len(applied.Events))
	}
}

// TestApply_LandedRefusesAnUnprovenExecution is the fail-closed rule.
//
// Write evidence beside an unproven outcome describes a workspace the runtime
// cannot vouch for. Landed must not present that as a completed write, or the
// distinction between "written" and "could not prove it was written" collapses
// back into one bit at the call site.
func TestApply_LandedRefusesAnUnprovenExecution(t *testing.T) {
	applied := kernelbridge.Applied{}

	if applied.Landed("anything") {
		t.Error("Landed reported true on a zero-value execution")
	}
	if applied.Written() != nil {
		t.Error("a zero-value execution claims written destinations")
	}
	if applied.Created("anything") {
		t.Error("Created reported true on a zero-value execution")
	}
}

// TestApply_ExecutionIdentityIsDeterministicAndObligationBound proves the name a
// log line carries is correlatable without the seam keeping state.
//
// It also proves the contract is part of the identity: the same destinations under
// different obligations are two different claims, and a log that conflated them
// could not be reconstructed.
func TestApply_ExecutionIdentityIsDeterministicAndObligationBound(t *testing.T) {
	root := t.TempDir()
	write := func(contract kernel.ContractKind) kernelbridge.Applied {
		return kernelbridge.Apply(context.Background(), root, []kernelbridge.Write{{
			Target:   "id.txt",
			Content:  "x\n",
			Contract: contract,
		}})
	}

	first := write(kernelbridge.ContractCreate)
	second := write(kernelbridge.ContractCreate)
	if first.ExecutionID != second.ExecutionID {
		t.Errorf("execution ids differ for the same request: %q vs %q",
			first.ExecutionID, second.ExecutionID)
	}
	patch := write(kernelbridge.ContractPatch)
	if patch.ExecutionID == first.ExecutionID {
		t.Error("the same destinations under different contracts share an execution id; the log could not be reconstructed")
	}
}
