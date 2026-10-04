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

// TestDelete_RemovesRealBytesAndProvesThem drives the delete chain through the
// seam and asserts each link separately: event → state → evidence → verification
// → PROVEN, plus the target's absence on disk.
func TestDelete_RemovesRealBytesAndProvesThem(t *testing.T) {
	root := t.TempDir()
	const target = "obsolete.go"
	writeFile(t, filepath.Join(root, target), "package obsolete\n")

	deleted := kernelbridge.Delete(context.Background(), root, []string{target})

	if !deleted.Proven() {
		t.Fatalf("outcome = %s (%s): %s; want PROVEN", deleted.Outcome, deleted.Class, deleted.Reason)
	}
	if deleted.Contract != kernel.ContractDelete {
		t.Errorf("contract = %s; want %s", deleted.Contract, kernel.ContractDelete)
	}
	if deleted.State.Mutation != kernel.MutationApplied {
		t.Errorf("mutation axis = %s; want %s", deleted.State.Mutation, kernel.MutationApplied)
	}
	if deleted.Verify != kernel.VerifyPassed {
		t.Errorf("verify axis = %s; want %s", deleted.Verify, kernel.VerifyPassed)
	}
	if !deleted.Deleted(target) {
		t.Error("Deleted reported false on a PROVEN removal")
	}
	if !deleted.Absent(target) {
		t.Error("Absent reported false after a PROVEN removal")
	}
	if deleted.Exists(target) {
		t.Error("Exists reported true after the target was removed")
	}
	if removed := deleted.Removed(); len(removed) != 1 || removed[0] != target {
		t.Errorf("Removed = %v; want exactly [%s]", removed, target)
	}

	// The absence, from the disk rather than from the capability.
	if _, err := os.Stat(filepath.Join(root, target)); !os.IsNotExist(err) {
		t.Fatalf("the target is still on disk after a PROVEN removal (stat err = %v)", err)
	}
}

// TestDelete_MissingTargetIsNotAMutation proves a delete of a target that is
// already gone is a NO-OP, not a proven removal. A contract that demands a
// durable change cannot be satisfied by having found nothing to change.
func TestDelete_MissingTargetIsNotAMutation(t *testing.T) {
	root := t.TempDir()
	const target = "never-existed.go"

	deleted := kernelbridge.Delete(context.Background(), root, []string{target})

	if deleted.Proven() {
		t.Fatal("deleting an absent target reached PROVEN; no mutation happened")
	}
	if deleted.Outcome != kernel.OutcomeUnsubstantiated {
		t.Errorf("outcome = %s (%s): %s; want %s",
			deleted.Outcome, deleted.Class, deleted.Reason, kernel.OutcomeUnsubstantiated)
	}
	if len(deleted.Removed()) != 0 {
		t.Errorf("Removed = %v; want nothing for a target that was never there", deleted.Removed())
	}
	if deleted.Deleted(target) {
		t.Error("Deleted reported true for a target that was already absent")
	}
	// The observation is still real: absence was observed and recorded, even
	// though the execution as a whole is not PROVEN. Observation.Absent is
	// deliberately reserved for PROVEN executions, so the presence record is
	// asserted directly here.
	presence, ok := deleted.Presence[target]
	if !ok || !presence.Observed || presence.Present {
		t.Errorf("presence = %+v (ok=%v); want an observed absence even when the contract is unmet", presence, ok)
	}
	if deleted.Verify != kernel.VerifyPassed {
		t.Errorf("verify axis = %s; the absence report was truthful and should verify", deleted.Verify)
	}
}

// TestDelete_MultipleTargetsInOneExecution proves a batch is adjudicated as one
// contract over every named destination.
func TestDelete_MultipleTargetsInOneExecution(t *testing.T) {
	root := t.TempDir()
	targets := []string{"a.go", "nested/b.go", "c.go"}
	for _, target := range targets {
		writeFile(t, filepath.Join(root, filepath.FromSlash(target)), "x\n")
	}

	deleted := kernelbridge.Delete(context.Background(), root, targets)

	if !deleted.Proven() {
		t.Fatalf("outcome = %s (%s): %s; want PROVEN", deleted.Outcome, deleted.Class, deleted.Reason)
	}
	if removed := deleted.Removed(); len(removed) != len(targets) {
		t.Errorf("Removed = %v; want all %d targets", removed, len(targets))
	}
	for _, target := range targets {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(target))); !os.IsNotExist(err) {
			t.Errorf("target %s survived a PROVEN batch removal (stat err = %v)", target, err)
		}
	}
}

// TestDelete_RefusesATargetOutsideTheWorkspace proves the mutating gate covers
// delete the same way it covers write.
func TestDelete_RefusesATargetOutsideTheWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "survivor.go")
	writeFile(t, outside, "do not remove me\n")
	t.Cleanup(func() { _ = os.Remove(outside) })

	deleted := kernelbridge.Delete(context.Background(), root, []string{"../survivor.go"})

	if deleted.Proven() {
		t.Fatal("a target outside the workspace reached PROVEN")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("the outside file was removed by a refused request: %v", err)
	}
	if deleted.Deleted("../survivor.go") {
		t.Error("Deleted reported true for a refused destination")
	}
}

// TestDelete_RefusesASymlinkEscape is the escape lexical confinement cannot see,
// applied to the removing direction.
func TestDelete_RefusesASymlinkEscape(t *testing.T) {
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
	victim := filepath.Join(outside, "victim.go")
	writeFile(t, victim, "must survive\n")

	deleted := kernelbridge.Delete(context.Background(), root, []string{"link/victim.go"})

	if deleted.Proven() {
		t.Fatal("a delete through a symlink leaving the workspace reached PROVEN")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("the file outside the workspace was removed through a symlink: %v", err)
	}
}

// TestDelete_RefusesADirectory proves the capability removes one file, not a
// tree, and that a refusal produces no mutation evidence.
func TestDelete_RefusesADirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	deleted := kernelbridge.Delete(context.Background(), root, []string{"dir"})

	if deleted.Proven() {
		t.Fatal("a directory reached PROVEN as a file deletion")
	}
	if len(deleted.Removed()) != 0 {
		t.Errorf("Removed = %v; a refused delete produced mutation evidence", deleted.Removed())
	}
	if _, err := os.Stat(filepath.Join(root, "dir")); err != nil {
		t.Fatalf("the directory was removed by a refused request: %v", err)
	}
}

// TestDelete_RefusesRatherThanTruncating proves an oversized target set is
// refused before any removal happens.
func TestDelete_RefusesRatherThanTruncating(t *testing.T) {
	root := t.TempDir()
	targets := make([]string, 0, kernelbridge.MaxAppliedTargets+1)
	for i := 0; i <= kernelbridge.MaxAppliedTargets; i++ {
		targets = append(targets, filepath.ToSlash(filepath.Join("d", "f"+strconv.Itoa(i)+".txt")))
	}

	deleted := kernelbridge.Delete(context.Background(), root, targets)

	if deleted.Proven() {
		t.Fatal("an oversized delete set reached PROVEN")
	}
	if deleted.Outcome != kernel.OutcomeBudgetExhausted {
		t.Errorf("outcome = %s (%s): %s; want %s",
			deleted.Outcome, deleted.Class, deleted.Reason, kernel.OutcomeBudgetExhausted)
	}
}

// TestDelete_EmptyTargetSetProvesNothing proves the seam refuses a request that
// names no destination rather than reporting a vacuous success.
func TestDelete_EmptyTargetSetProvesNothing(t *testing.T) {
	deleted := kernelbridge.Delete(context.Background(), t.TempDir(), nil)
	if deleted.Proven() {
		t.Fatal("an empty delete set reached PROVEN")
	}
	if !strings.Contains(deleted.Reason, "at least one target") {
		t.Errorf("reason = %q; want the refusal to explain that an empty set proves nothing", deleted.Reason)
	}
}

// TestDelete_VerificationIsARealOrderedPass asserts the same spine the write
// direction asserts: mutation first, then verification started, then passed.
func TestDelete_VerificationIsARealOrderedPass(t *testing.T) {
	root := t.TempDir()
	const target = "ordered.go"
	writeFile(t, filepath.Join(root, target), "gone soon\n")

	deleted := kernelbridge.Delete(context.Background(), root, []string{target})
	if !deleted.Proven() {
		t.Fatalf("outcome = %s: %s; want PROVEN", deleted.Outcome, deleted.Reason)
	}

	positions := map[kernel.EventKind]int{}
	for i, e := range deleted.Events {
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
			t.Fatalf("event %s is missing from a proven removal", kind)
		}
	}
	if positions[kernel.EventMutationApplied] > positions[kernel.EventVerificationStarted] {
		t.Error("the removal happened after verification started")
	}
	if positions[kernel.EventVerificationStarted] > positions[kernel.EventVerificationPassed] {
		t.Error("verification passed before it started")
	}
}

// TestDelete_ZeroValueIsFailClosed mirrors the write direction's fail-closed
// rule.
func TestDelete_ZeroValueIsFailClosed(t *testing.T) {
	var deleted kernelbridge.Applied
	if deleted.Deleted("anything") {
		t.Error("Deleted reported true on a zero-value execution")
	}
	if deleted.Removed() != nil {
		t.Error("a zero-value execution claims removed destinations")
	}
}
