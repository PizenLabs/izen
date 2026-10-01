package execution

// ── PHASE 16: PIPELINE-LEVEL EVIDENCE BOUNDS ────────────────────────────────
//
// The unit tests in resolver_test.go and binding_test.go pin the two new
// components. These pin their POSITION: a resolver that exists but is never
// consulted, or a binder that is consulted after the patch is already staged,
// satisfies every unit test and fixes nothing.
//
// The assertions are therefore about what did NOT happen: no provider call, no
// staged patch, no byte written. A positive assertion ("the right thing
// occurred") is satisfied by a run that also did three wrong things on the way.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
)

// TestPhase16_ExecutorRunsDiscoveryBeforeTheProvider is ACCEPTANCE TEST B at the
// pipeline level.
//
// An unresolved mutation objective in a workspace that offers several plausible
// files must HALT in AWAITING_DISAMBIGUATION — with the provider untouched.
//
// A provider call here would be real money spent to produce an answer the
// runtime has no way to bind. That is the whole defect: the cost is incurred
// before the missing fact is discovered.
func TestPhase16_ExecutorRunsDiscoveryBeforeTheProvider(t *testing.T) {
	root := writeTree(t, map[string]string{
		"index.html":     "<html><body>root</body></html>\n",
		"src/index.html": "<html><body>src</body></html>\n",
		"README.md":      "# readme\n",
	})
	before := snapshotTree(t, root)

	// A provider that would happily return a plausible artifact. It must never
	// be reached, and its call count is the assertion.
	mock := &mockProvider{responses: []*ai.Response{{Content: sampleReplace}}}
	x := phase4Executor(t, root, mock, events.NewBus(events.DefaultBufferSize))

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "Redesign index.html", Strategy: targetedMutationProfile(),
	})
	if err != nil {
		t.Fatalf("a halted resolution is not a failure: %v", err)
	}
	if mock.callCount != 0 {
		t.Fatalf("provider was invoked %d time(s) with an unresolved target; discovery must precede dispatch", mock.callCount)
	}
	if !res.ClarificationRequired {
		t.Error("an unresolved target must demand human input")
	}
	if res.Proof.Outcome.MutationSucceeded() {
		t.Fatalf("outcome = %s, want no mutation", res.Proof.Outcome)
	}
	if res.PendingPatchID != "" {
		t.Error("no patch may be staged for an unresolved target")
	}

	// The evidence record must survive the halt: a human asked to disambiguate
	// needs to see what the runtime looked at, not just that it gave up.
	if res.TargetBinding == nil || res.Proof.TargetBinding == nil {
		t.Fatal("the halted execution must carry its target-binding evidence")
	}
	if res.Proof.TargetBinding.Phase != PhaseAwaitingDisambiguation {
		t.Errorf("phase = %q, want %q", res.Proof.TargetBinding.Phase, PhaseAwaitingDisambiguation)
	}
	if !res.Proof.TargetBinding.DiscoveryPerformed {
		t.Error("workspace discovery must be recorded as performed")
	}
	if res.Proof.TargetBinding.Evidence == nil {
		t.Fatal("the discovery record must be attached to the evidence")
	}
	if len(res.Proof.TargetBinding.Candidates) < 2 {
		t.Errorf("the human must be offered the candidate set, got %v", res.Proof.TargetBinding.Candidates)
	}
	assertTreeUnchanged(t, root, before)
}

// TestPhase16_ExecutorHaltsOnAnEmptyWorkspace is the same boundary with no
// candidates at all. The outcome is UNSUBSTANTIATED, not AMBIGUOUS: "here is
// what it could have been" and "there was nothing" are different answers and
// the human needs to be told which one they are looking at.
func TestPhase16_ExecutorHaltsOnAnEmptyWorkspace(t *testing.T) {
	root := writeTree(t, nil)
	mock := &mockProvider{responses: []*ai.Response{{Content: sampleReplace}}}
	x := phase4Executor(t, root, mock, events.NewBus(events.DefaultBufferSize))

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "Redesign portfolio page", Strategy: targetedMutationProfile(),
	})
	if err != nil {
		t.Fatalf("a halted resolution is not a failure: %v", err)
	}
	if mock.callCount != 0 {
		t.Fatalf("provider was invoked %d time(s) against an empty workspace", mock.callCount)
	}
	if res.Proof.TargetBinding == nil {
		t.Fatal("the target-binding evidence must be recorded")
	}
	if res.Proof.TargetBinding.Phase != PhaseUnsubstantiated {
		t.Errorf("phase = %q, want UNSUBSTANTIATED", res.Proof.TargetBinding.Phase)
	}
	if res.Proof.TargetBinding.Dispatchable() {
		t.Error("an unsubstantiated target must never be dispatchable")
	}
	assertTreeUnchanged(t, root, map[string]string{})
}

// TestPhase16_ExecutorBindsTheArtifactBeforeStaging proves the binder sits
// BETWEEN the parser and the mutation engine: a self-addressed artifact naming a
// DIFFERENT file than the dispatch target is refused, and no patch is staged.
//
// The model here returned a perfectly valid, well-formed artifact. That is the
// point: the failure this catches is not "the model produced garbage", it is
// "the model produced something excellent for the wrong file".
func TestPhase16_ExecutorBindsTheArtifactBeforeStaging(t *testing.T) {
	root := writeTree(t, map[string]string{"index.html": "<html><body>before</body></html>\n"})
	before := snapshotTree(t, root)

	// A contract fence that names other.html — a file that exists in no
	// workspace here, and is not the dispatch target.
	mock := &mockProvider{responses: []*ai.Response{{
		Content: "```html:other.html\n<html><body>after</body></html>\n```",
	}}}
	x := phase4Executor(t, root, mock, events.NewBus(events.DefaultBufferSize))

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "redesign the page", Target: "index.html",
		Strategy: targetedMutationProfile(),
	})
	if err == nil {
		t.Fatal("an artifact addressed to another file must not be accepted")
	}
	if !errors.Is(err, ErrUnboundArtifact) {
		t.Fatalf("error = %v, want ErrUnboundArtifact", err)
	}
	if res != nil && res.PendingPatchID != "" {
		t.Error("an unbound artifact must never reach the approval gate")
	}
	if !strings.Contains(err.Error(), "other.html") || !strings.Contains(err.Error(), "index.html") {
		t.Errorf("the rejection must name both paths, got: %v", err)
	}
	assertTreeUnchanged(t, root, before)
	// The provider WAS called — this test is about what happens AFTER the model
	// answered, not about suppressing the call.
	if mock.callCount == 0 {
		t.Error("the provider must be reached for a correctly targeted mutation")
	}
}

// TestPhase16_ExecutorNeverInfersTheTargetFromArtifactContent is ACCEPTANCE TEST
// C at the pipeline level, stated precisely.
//
// The provider returned a bare "```html" fence: a LANGUAGE tag and no location.
// The invariant is that the runtime's destination came from the PROVEN TARGET
// BINDING and never from the payload's content type — so the content type had
// no power to name, add or change a file.
//
// A bare fence MAY still be applied here, and it is important to be clear about
// why: the target was already proven (an explicit, observed, digest-bound
// TargetBinding) before the provider was called. The gate is asking "may these
// bytes go to index.html?", and the answer is yes for reasons that predate the
// payload. ACCEPTANCE TEST C proper — where the artifact must be REFUSED — is
// the binder operating with no such evidence, and it is pinned in
// TestPhase16_HeuristicBindingRejection.
func TestPhase16_ExecutorNeverInfersTheTargetFromArtifactContent(t *testing.T) {
	root := writeTree(t, map[string]string{"index.html": "<html><body>before</body></html>\n"})
	before := snapshotTree(t, root)

	raw := "```html\n<html><body>redesigned</body></html>\n```"

	// The parser must name no path at all. This is the assertion the heuristic
	// would have failed: content type never becomes a location.
	artifact, parseErr := ParseMutationArtifacts(raw)
	if parseErr != nil {
		t.Fatalf("a fenced HTML payload is a recognizable artifact: %v", parseErr)
	}
	if artifact.DeclaredPath != "" {
		t.Fatalf("the parser inferred target path %q from a bare ```html fence", artifact.DeclaredPath)
	}

	mock := &mockProvider{responses: []*ai.Response{{Content: raw}}}
	x := phase4Executor(t, root, mock, events.NewBus(events.DefaultBufferSize))

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "redesign the page", Target: "index.html",
		Strategy: targetedMutationProfile(),
	})
	if err != nil {
		t.Fatalf("a target-bound dispatch must not be broken by the gate: %v", err)
	}

	// The execution's target list is EXACTLY what was proven. The payload's
	// content type contributed nothing to it — if it had, an HTML body would
	// have manufactured index.html out of nothing, and a JSON body would have
	// manufactured a different file just as silently.
	if len(res.Proof.Targets) != 1 || res.Proof.Targets[0] != "index.html" {
		t.Fatalf("execution targets = %v, want exactly the proven [index.html]", res.Proof.Targets)
	}
	// And no file the payload might have implied now exists.
	if _, statErr := os.Stat(filepath.Join(root, "other.html")); !os.IsNotExist(statErr) {
		t.Fatal("artifact content must never materialize a file the runtime did not prove")
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 1 {
		t.Fatalf("workspace gained files from a payload that named none: %v", entries)
	}
	assertTreeUnchanged(t, root, before)
}

// TestPhase16_ExecutorAppliesABoundArtifact is the positive control: the gate
// must not be so strict that a legitimate mutation stops working. An artifact
// produced under a contract that names the target, against the target's actual
// bytes, is bound and applied.
func TestPhase16_ExecutorAppliesABoundArtifact(t *testing.T) {
	root := writeTree(t, map[string]string{"note.txt": sampleOriginal})
	before := snapshotTree(t, root)

	mock := &mockProvider{responses: []*ai.Response{{
		Content: "```html:note.txt\n" + sampleReplace + "```",
	}}}
	x := phase4Executor(t, root, mock, events.NewBus(events.DefaultBufferSize))

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode: "build", Prompt: "rewrite the note", Target: "note.txt",
		Strategy: targetedMutationProfile(),
	})
	if err != nil {
		t.Fatalf("an explicitly addressed, context-matched artifact must bind: %v", err)
	}
	if res.PendingPatchID == "" {
		t.Fatal("a bound artifact must reach the approval gate")
	}
	if _, err := x.Approve(context.Background(), res.PendingPatchID); err != nil {
		t.Fatalf("a bound artifact must apply: %v", err)
	}
	// A non-zero disk delta is the observable proof the mutation was real.
	got := mustRead(t, root, "note.txt")
	if got == before["note.txt"] {
		t.Fatal("the applied mutation produced no disk delta — it was reported as a change but changed nothing")
	}
}

// TestPhase16_BindingEvidenceIsCarriedOnRejectedPatches: a rejection must be
// diagnosable after the fact. A run that halts and says nothing is
// indistinguishable from a provider outage.
func TestPhase16_BindingEvidenceIsCarriedOnRejectedPatches(t *testing.T) {
	root := writeTree(t, map[string]string{"index.html": "<html>a</html>\n"})

	binder := NewArtifactBinder(root)
	_, bindErr := binder.Bind(ArtifactEnvelope{
		Content:       "<html>b</html>\n",
		Form:          ArtifactFormContractFence,
		Structural:    true,
		DeclaredPath:  "index.html",
		ContextSHA256: sourceSHA256([]byte("<html>stale</html>\n")),
	}, TargetBinding{
		Path: "index.html", SourceSHA256: sourceSHA256([]byte("<html>a</html>\n")),
		Explicit: true, Exists: true,
	})
	if bindErr == nil {
		t.Fatal("precondition: a stale context must be refused")
	}
	// Every rejection names the criterion it failed, so the run's failure mode is
	// readable without re-running it under a debugger.
	for _, want := range []string{"context digest", "index.html"} {
		if !strings.Contains(bindErr.Error(), want) {
			t.Errorf("rejection must name %q, got: %v", want, bindErr)
		}
	}
}
