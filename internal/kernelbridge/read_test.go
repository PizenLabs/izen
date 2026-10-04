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

// TestReadFiles_ReturnsProvenContent walks the read chain link by link.
//
// These tests prove the SEAM. The product-side proof — that the diff a human
// approves is computed from this content — is in
// internal/ui/toolcall_write_kernel_test.go.

// TestReadFiles_ReturnsProvenContent reads one file that exists.
func TestReadFiles_ReturnsProvenContent(t *testing.T) {
	root := t.TempDir()
	const target = "src/app.go"
	const content = "package app\n"
	writeFile(t, filepath.Join(root, target), content)

	read := kernelbridge.ReadFiles(context.Background(), root, []string{target})

	if !read.Proven() {
		t.Fatalf("outcome = %s (%s): %s; want PROVEN", read.Outcome, read.Class, read.Reason)
	}
	if !read.Found(target) {
		t.Fatalf("Found(%s) = false on a PROVEN read of an existing file", target)
	}
	if read.Absent(target) {
		t.Errorf("Absent(%s) = true for an existing file", target)
	}
	if got := read.Content(target); got != content {
		t.Errorf("Content = %q; want %q", got, content)
	}
	if n, ok := read.BytesOnDisk(target); !ok || n != len(content) {
		t.Errorf("BytesOnDisk = (%d, %t); want (%d, true) — an independently measured size", n, ok, len(content))
	}
	if read.Verify != kernel.VerifyPassed {
		t.Errorf("verify axis = %s; want %s", read.Verify, kernel.VerifyPassed)
	}
	// A read must not cross the mutation boundary. The contract forbids it, so
	// the axis must stay empty of any claim.
	if read.State.Mutation != kernel.MutationNone {
		t.Errorf("mutation axis = %s; want %s — a read that mutated would be a defect",
			read.State.Mutation, kernel.MutationNone)
	}
	if !read.State.Spec.Contract.Kind.ForbidsMutation() {
		t.Error("the read contract does not forbid mutation")
	}
}

// TestReadFiles_AbsentIsAProvenFact is the case the old buffer could not
// express at all.
//
// The old path called os.ReadFile and treated ANY error as "the file is empty", so
// a missing file, a permission error and a directory were the same thing. Here
// absence is a named observation that reaches PROVEN, and it is distinguishable
// from an empty file — which is also PROVEN, and also reads zero bytes.
func TestReadFiles_AbsentIsAProvenFact(t *testing.T) {
	root := t.TempDir()
	const missing = "not/there.go"
	emptyPath := "empty.go"
	writeFile(t, filepath.Join(root, emptyPath), "")

	read := kernelbridge.ReadFiles(context.Background(), root, []string{missing, emptyPath})

	if !read.Proven() {
		t.Fatalf("outcome = %s (%s): %s; want PROVEN — a truthful report of absence is a satisfied contract",
			read.Outcome, read.Class, read.Reason)
	}

	// The absent destination: a real fact, not an error and not empty content.
	if !read.Absent(missing) {
		t.Errorf("Absent(%s) = false; the runtime proved absence and must say so", missing)
	}
	if read.Found(missing) {
		t.Errorf("Found(%s) = true for a file that does not exist", missing)
	}
	if got := read.Content(missing); got != "" {
		t.Errorf("Content(%s) = %q; want \"\"", missing, got)
	}
	if _, ok := read.BytesOnDisk(missing); ok {
		t.Errorf("BytesOnDisk(%s) reported a measurement; nothing was read", missing)
	}
	assertEvidence(t, read, kernel.EvidenceFileAbsent, missing)

	// The empty destination: present, zero bytes, and NOT absent. This is the pair
	// that a "did I get bytes back" check cannot tell apart.
	if !read.Found(emptyPath) {
		t.Errorf("Found(%s) = false; a zero-byte file exists and was read", emptyPath)
	}
	if read.Absent(emptyPath) {
		t.Errorf("Absent(%s) = true for a file that exists and is empty", emptyPath)
	}
	if n, ok := read.BytesOnDisk(emptyPath); !ok || n != 0 {
		t.Errorf("BytesOnDisk(%s) = (%d, %t); want (0, true) — an empty file IS measured", emptyPath, n, ok)
	}
	assertEvidence(t, read, kernel.EvidenceFileRead, emptyPath)
}

// TestReadFiles_NeverAnswersFromAnEmptyString is the fail-closed rule.
//
// Content is "" for three different situations: an absent file, an empty file, and
// a read that reached no verdict. A caller that only looked at Content would treat
// all three as "the file is empty" and overwrite whatever was there. Absent and
// Found are what separate them, and they must be false when the runtime could not
// answer.
func TestReadFiles_NeverAnswersFromAnEmptyString(t *testing.T) {
	unusable := filepath.Join(t.TempDir(), "not-a-directory.go")
	writeFile(t, unusable, "I am a file\n")

	read := kernelbridge.ReadFiles(context.Background(), unusable, []string{"a.go"})

	if read.Proven() {
		t.Fatal("a read against an unusable workspace root reached PROVEN")
	}
	if read.Found("a.go") || read.Absent("a.go") {
		t.Error("a read that could not run reported a workspace fact anyway")
	}
	if got := read.Content("a.go"); got != "" {
		t.Errorf("Content = %q; want \"\" for a read that produced nothing", got)
	}
	if _, ok := read.BytesOnDisk("a.go"); ok {
		t.Error("BytesOnDisk reported a measurement for a read that never ran")
	}
	if read.Verify == kernel.VerifyPassed {
		t.Error("verification passed on a request that never reached the kernel")
	}
}

// TestReadFiles_RefusesRatherThanTruncating proves an oversized read set is
// refused rather than silently trimmed.
func TestReadFiles_RefusesRatherThanTruncating(t *testing.T) {
	root := t.TempDir()

	targets := make([]string, 0, kernelbridge.MaxObservedTargets+1)
	for i := 0; i <= kernelbridge.MaxObservedTargets; i++ {
		targets = append(targets, filepath.ToSlash(filepath.Join("d", "f"+strconv.Itoa(i)+".go")))
	}

	read := kernelbridge.ReadFiles(context.Background(), root, targets)

	if read.Proven() {
		t.Fatal("an oversized read set reached PROVEN")
	}
	if read.Outcome != kernel.OutcomeBudgetExhausted {
		t.Errorf("outcome = %s (%s): %s; want %s",
			read.Outcome, read.Class, read.Reason, kernel.OutcomeBudgetExhausted)
	}
	for _, target := range targets {
		if read.Found(target) || read.Absent(target) {
			t.Errorf("target %q was answered by a refused request", target)
		}
	}
}

// TestReadFiles_RefusesAnEmptyTargetSet proves there is no such thing as reading
// nothing successfully.
func TestReadFiles_RefusesAnEmptyTargetSet(t *testing.T) {
	root := t.TempDir()

	read := kernelbridge.ReadFiles(context.Background(), root, nil)

	if read.Proven() {
		t.Fatal("an empty read set reached PROVEN; reading nothing proves nothing")
	}
	if read.Outcome != kernel.OutcomeUnsubstantiated {
		t.Errorf("outcome = %s; want %s", read.Outcome, kernel.OutcomeUnsubstantiated)
	}
}

// TestReadFiles_ExecutionIdentityIsDeterministic proves the name a log line
// carries is correlatable without the seam keeping any state.
func TestReadFiles_ExecutionIdentityIsDeterministic(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "stable.go"), "package stable\n")

	first := kernelbridge.ReadFiles(context.Background(), root, []string{"stable.go"})
	second := kernelbridge.ReadFiles(context.Background(), root, []string{"stable.go"})

	if first.ExecutionID != second.ExecutionID {
		t.Errorf("execution ids differ for the same request: %q vs %q",
			first.ExecutionID, second.ExecutionID)
	}
	if first.ExecutionID == "" {
		t.Error("an execution nobody can name cannot be audited")
	}
}

// TestReadFiles_AFileAboveTheBoundIsRefusedNotTruncated proves the bound fails
// closed.
//
// A read that quietly returned a prefix would hand the caller a diff computed from
// a truncated baseline and still report PROVEN — the read would be "successful" and
// wrong. So the capability records a truncated byte count, the verifier's
// independent re-read measures the whole file, the two disagree, and the execution
// must not settle as proven.
func TestReadFiles_AFileAboveTheBoundIsRefusedNotTruncated(t *testing.T) {
	root := t.TempDir()
	const target = "huge.go"
	oversized := strings.Repeat("x", kernelbridge.MaxReadBytes+1)
	if err := os.WriteFile(filepath.Join(root, target), []byte(oversized), 0o644); err != nil {
		t.Fatalf("seeding an oversized file: %v", err)
	}

	read := kernelbridge.ReadFiles(context.Background(), root, []string{target})

	if read.Proven() {
		t.Fatal("an oversized read reached PROVEN; the reported content would be a truncated prefix")
	}
	if read.Found(target) || read.Absent(target) {
		t.Error("an oversized read reported a workspace fact anyway")
	}
	if got := read.Content(target); got != "" {
		t.Errorf("Content = %d bytes; want \"\" — a partial read must never be served as content",
			len(got))
	}
	if read.Verify == kernel.VerifyPassed {
		t.Error("verification passed on a read whose content disagreed with the filesystem")
	}
}

func assertEvidence(t *testing.T, read kernelbridge.Read, kind kernel.EvidenceKind, target string) {
	t.Helper()
	for _, e := range read.Evidence {
		if e.Kind == kind && e.Target == target {
			if e.Capability != kernel.FileRead && e.Capability != kernel.FileExists {
				t.Errorf("evidence %s about %s claims capability %q", kind, target, e.Capability)
			}
			return
		}
	}
	t.Errorf("no %s evidence for %s; the observation was asserted rather than recorded", kind, target)
}
