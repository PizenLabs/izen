package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/kernelbridge"
	"github.com/PizenLabs/izen/runtime/kernel"
)

// This file is the strangler proof for the first vertical slice.
//
// The unit tests in internal/kernelbridge prove the bridge works. They do NOT
// prove the product uses it. These tests drive the real user-facing entry point —
// the autonomy build target resolution a user reaches by typing
// `@path/to/file.go` in build mode — and assert that the workspace fact it
// returns came from a complete kernel chain rather than from a syscall.

// TestAutonomyTargetResolution_ExplicitPathIsKernelProven drives the slice end to
// end and walks every link of event → state → evidence → verification → PROVEN.
func TestAutonomyTargetResolution_ExplicitPathIsKernelProven(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "internal/ui/gateway.go", "package ui\n")
	m := &model{workspaceRoot: root}

	// The real caller passes trace.Intent.Target(), which the intent extractor has
	// already stripped of the "@" sigil. Passing it with the sigil would be a
	// different question, and the resolution would (correctly) prove the sigiled
	// name absent.
	res := m.resolveAutonomyBuildTarget("internal/ui/gateway.go")

	// ── The decision ─────────────────────────────────────────────────────
	if len(res.candidates) != 1 || res.candidates[0] != "internal/ui/gateway.go" {
		t.Fatalf("candidates = %v; want exactly [internal/ui/gateway.go]", res.candidates)
	}
	if res.resolved != "internal/ui/gateway.go" {
		t.Errorf("resolved = %q; want %q", res.resolved, "internal/ui/gateway.go")
	}

	// ── The chain behind that decision ───────────────────────────────────
	if len(res.observations) != 1 {
		t.Fatalf("observations = %d; want 1 (an explicit path needs no workspace walk)", len(res.observations))
	}
	obs := res.observations[0]

	if !obs.Proven() || obs.Outcome != kernel.OutcomeProven {
		t.Fatalf("outcome = %s (%s): %s; want PROVEN", obs.Outcome, obs.Class, obs.Reason)
	}

	// Events: the full spine, in order.
	assertResolutionEventSpine(t, obs.Events, "internal/ui/gateway.go")

	// State: settled, at a revision, with the transport axis settled and every
	// task-meaning axis still empty. A settled invocation is not a completion.
	if obs.State.Status != kernel.StatusSettled {
		t.Errorf("state status = %s; want %s", obs.State.Status, kernel.StatusSettled)
	}
	if obs.State.Provider != kernel.ProviderDone {
		t.Errorf("provider axis = %s; want %s", obs.State.Provider, kernel.ProviderDone)
	}
	if obs.State.Artifact != kernel.ArtifactNone {
		t.Errorf("artifact axis = %s; want %s", obs.State.Artifact, kernel.ArtifactNone)
	}
	if obs.State.Mutation != kernel.MutationNone {
		t.Errorf("mutation axis = %s; want %s (resolution must not mutate)", obs.State.Mutation, kernel.MutationNone)
	}

	// Evidence: a named FILE_PRESENT about the exact resolved target.
	if !hasEvidence(obs, kernel.EvidenceFilePresent, "internal/ui/gateway.go") {
		t.Errorf("no FILE_PRESENT evidence for the resolved target:\n%s", renderEvidence(obs))
	}

	// Verification: a real pass, never a skip.
	if obs.Verify != kernel.VerifyPassed {
		t.Errorf("verify axis = %s; want %s", obs.Verify, kernel.VerifyPassed)
	}

	// The contract was OBSERVE and it forbade mutation, so the spec must have
	// declared that — otherwise the kernel would have accepted a program that
	// writes during target resolution.
	if obs.State.Spec.Contract.Kind != kernel.ContractObserve {
		t.Errorf("contract kind = %s; want %s", obs.State.Spec.Contract.Kind, kernel.ContractObserve)
	}
	if !obs.State.Spec.Contract.Kind.ForbidsMutation() {
		t.Error("OBSERVE contract does not forbid mutation; resolution could have written")
	}
}

// TestAutonomyTargetResolution_AbsentTargetIsProvenAbsent is the case the deleted
// os.Stat path could not express. The resolution must be able to say "this does
// not exist" with evidence behind it, and must keep that distinct from a failure
// to answer.
func TestAutonomyTargetResolution_AbsentTargetIsProvenAbsent(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "README.md", "# repo\n")
	m := &model{workspaceRoot: root}

	res := m.resolveAutonomyBuildTarget("nowhere/missing.go")

	if len(res.candidates) != 0 {
		t.Fatalf("candidates = %v; want none", res.candidates)
	}
	if res.unproven() {
		t.Errorf("resolution is unproven: %s", describeTargetEvidence(res))
	}
	if !res.provenAbsent("nowhere/missing.go") {
		t.Errorf("provenAbsent = false; the runtime proved absence and must say so.\nevidence:\n%s",
			renderResolutions(res))
	}
	for _, obs := range res.observations {
		if hasEvidence(obs, kernel.EvidenceFilePresent, "nowhere/missing.go") {
			t.Error("a target that does not exist produced FILE_PRESENT evidence")
		}
	}
}

// TestAutonomyTargetResolution_BareNameFallsBackToDiscovery proves the explicit
// path is a fast path, not the only path: a bare filename still resolves, and
// every candidate it yields is kernel-proven.
func TestAutonomyTargetResolution_BareNameFallsBackToDiscovery(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "src/app.go", "package app\n")
	writeTarget(t, root, "vendor/lib/app.go", "package app\n")
	m := &model{workspaceRoot: root}

	res := m.resolveAutonomyBuildTarget("app.go")

	if len(res.candidates) != 2 {
		t.Fatalf("candidates = %v; want both discovered files", res.candidates)
	}
	obs := res.observations[len(res.observations)-1]
	if !obs.Proven() {
		t.Fatalf("outcome = %s (%s): %s; want PROVEN", obs.Outcome, obs.Class, obs.Reason)
	}
	for _, candidate := range res.candidates {
		if !hasEvidence(obs, kernel.EvidenceFilePresent, candidate) {
			t.Errorf("candidate %q was offered without FILE_PRESENT evidence:\n%s", candidate, renderEvidence(obs))
		}
	}
	// Both files are proven present, so the ambiguity is real and the selector —
	// a human decision — is the correct next step.
	if len(res.candidates) <= 1 {
		t.Error("two proven candidates must remain ambiguous; the runtime must not pick for the human")
	}
}

// TestAutonomyTargetResolution_NeverOffersAnUnprovenTarget is the fail-closed
// rule: a name the walk found but the runtime could not account for must not
// reach the caller, because the caller may hand it to a mutation.
func TestAutonomyTargetResolution_NeverOffersAnUnprovenTarget(t *testing.T) {
	root := t.TempDir()
	writeTarget(t, root, "src/app.go", "package app\n")
	m := &model{workspaceRoot: root}

	res := m.resolveAutonomyBuildTarget("app.go")

	for _, obs := range res.observations {
		for target, presence := range obs.Presence {
			if !obs.Proven() && presence.Present && !obs.Exists(target) {
				t.Errorf("an unproven observation leaked presence for %q", target)
			}
		}
	}
	for _, candidate := range res.candidates {
		found := false
		for _, obs := range res.observations {
			if obs.Exists(candidate) {
				found = true
			}
		}
		if !found {
			t.Errorf("candidate %q was offered without a PROVEN observation", candidate)
		}
	}
}

// TestDescribeTargetEvidence_SaysWhenItKnowsNothing keeps the user-facing
// diagnosis honest: the old implementation asserted an absence that no recorded
// fact supported.
func TestDescribeTargetEvidence_SaysWhenItKnowsNothing(t *testing.T) {
	if got := describeTargetEvidence(targetResolution{}); !strings.Contains(got, "nothing") {
		t.Errorf("empty resolution rendered as %q; want an explicit 'nothing'", got)
	}

	unproven := targetResolution{observations: []kernelbridge.Observation{{
		ExecutionID: "observe-deadbeef",
		Outcome:     kernel.OutcomeBudgetExhausted,
		Class:       kernel.FailureBudgetExhausted,
	}}}
	got := describeTargetEvidence(unproven)
	if !strings.Contains(got, "PROVEN") || strings.Contains(got, "does not contain") {
		t.Errorf("unproven resolution rendered as %q; want an explicit non-PROVEN report", got)
	}

	provenAbsent := targetResolution{observations: []kernelbridge.Observation{{
		Outcome:  kernel.OutcomeProven,
		Presence: map[string]kernelbridge.Presence{"a.go": {Target: "a.go", Observed: true}},
	}}}
	if got := describeTargetEvidence(provenAbsent); !strings.Contains(got, "does not contain: a.go") {
		t.Errorf("proven absence rendered as %q; want the named target", got)
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

func assertResolutionEventSpine(t *testing.T, events []kernel.Event, target string) {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("no events recorded; a resolution whose truth has no log cannot be replayed or audited")
	}
	seen := map[kernel.EventKind]int{}
	for i, e := range events {
		if _, dup := seen[e.Kind]; !dup {
			seen[e.Kind] = i
		}
		if i > 0 && e.Revision != events[i-1].Revision+1 {
			t.Fatalf("revision jumped %d → %d at %s; the log has a gap", events[i-1].Revision, e.Revision, e.Kind)
		}
	}
	spine := []kernel.EventKind{
		kernel.EventExecutionStarted,
		kernel.EventStepStarted,
		kernel.EventCapabilityInvoked,
		kernel.EventEvidenceProduced,
		kernel.EventVerificationStarted,
		kernel.EventVerificationPassed,
		kernel.EventExecutionFinished,
	}
	prev := -1
	for _, kind := range spine {
		at, ok := seen[kind]
		if !ok {
			t.Fatalf("event %s missing from the resolution chain", kind)
		}
		if at <= prev {
			t.Fatalf("event %s at %d is not after the previous spine event at %d", kind, at, prev)
		}
		prev = at
	}
	for i, e := range events {
		if e.Target != "" && e.Target != target {
			t.Errorf("event %d (%s) targets %q; want %q", i, e.Kind, e.Target, target)
		}
	}
}

func hasEvidence(obs kernelbridge.Observation, kind kernel.EvidenceKind, target string) bool {
	for _, e := range obs.Evidence {
		if e.Kind == kind && e.Target == target {
			return true
		}
	}
	return false
}

func renderEvidence(obs kernelbridge.Observation) string {
	var sb strings.Builder
	for _, e := range obs.Evidence {
		sb.WriteString("  " + e.ID + " " + string(e.Kind) + " " + e.Target + " (" + string(e.Capability) + "/" + e.Step + ")\n")
	}
	if sb.Len() == 0 {
		return "  (no evidence recorded)\n"
	}
	return sb.String()
}

func renderResolutions(res targetResolution) string {
	var sb strings.Builder
	for i, obs := range res.observations {
		sb.WriteString("  execution " + obs.ExecutionID + " → " + string(obs.Outcome) + "\n")
		sb.WriteString(renderEvidence(obs))
		_ = i
	}
	if sb.Len() == 0 {
		return "  (no observation performed)\n"
	}
	return sb.String()
}

func writeTarget(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(abs), err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", abs, err)
	}
}
