package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/autonomy"
)

// ── AUTHORITATIVE CLI PATH (behavioral ownership) ────────────────────────────
//
// These tests pin the lifecycle-ownership contract of the migrated CLI path:
// the CLI drives the authoritative runtime through a structural port, plays the
// human at parked boundaries, issues the mutation authorization ONLY on an
// explicit approve, and projects the runtime's own terminal truth. The fake
// driver is the port, not a second loop: it has no provider and no execution.

// fakeDriver is a scriptable AuthoritativeDriver. It records every transition
// the CLI asks for and reports the parked boundary the script declares.
type fakeDriver struct {
	scope string

	state    autonomy.RuntimeState
	boundary *autonomy.HumanBoundary
	term     *autonomy.LoopTermination

	runTerm     *autonomy.LoopTermination
	runErr      error
	approveTerm *autonomy.LoopTermination
	approveErr  error
	rejectTerm  *autonomy.LoopTermination
	rejectErr   error
	clarifyTerm *autonomy.LoopTermination
	clarifyErr  error
	abortTerm   *autonomy.LoopTermination
	abortErr    error

	runCalls     int
	approveCalls int
	rejectCalls  int
	clarifyCalls int
	abortCalls   int

	lastObjective   string
	lastScope       string
	lastReject      string
	lastClarify     string
	lastAbortReason string
}

func (d *fakeDriver) Run(_ context.Context, objective string) (*autonomy.LoopTermination, error) {
	d.runCalls++
	d.lastObjective = objective
	d.state = autonomy.RuntimeAwaitingHuman
	d.term = d.runTerm
	return d.runTerm, d.runErr
}

func (d *fakeDriver) ResumeApprove(_ context.Context) (*autonomy.LoopTermination, error) {
	d.approveCalls++
	d.state = autonomy.RuntimeCompleted
	d.boundary = nil
	d.term = d.approveTerm
	return d.approveTerm, d.approveErr
}

func (d *fakeDriver) ResumeReject(_ context.Context, reason string) (*autonomy.LoopTermination, error) {
	d.rejectCalls++
	d.lastReject = reason
	d.state = autonomy.RuntimeAborted
	d.boundary = nil
	d.term = d.rejectTerm
	return d.rejectTerm, d.rejectErr
}

func (d *fakeDriver) ResumeClarify(_ context.Context, target string) (*autonomy.LoopTermination, error) {
	d.clarifyCalls++
	d.lastClarify = target
	d.state = autonomy.RuntimeCompleted
	d.boundary = nil
	d.term = d.clarifyTerm
	return d.clarifyTerm, d.clarifyErr
}

func (d *fakeDriver) Abort(reason string) (*autonomy.LoopTermination, error) {
	d.abortCalls++
	d.lastAbortReason = reason
	d.state = autonomy.RuntimeAborted
	d.boundary = nil
	d.term = d.abortTerm
	return d.abortTerm, d.abortErr
}

func (d *fakeDriver) State() autonomy.RuntimeState      { return d.state }
func (d *fakeDriver) Boundary() *autonomy.HumanBoundary { return d.boundary }
func (d *fakeDriver) SetScope(scope string)             { d.scope = scope; d.lastScope = scope }
func (d *fakeDriver) RunID() string                     { return "run-test-1" }

// scriptedBridge returns a fixed decision and records the boundary it saw.
type scriptedBridge struct {
	decision    HumanDecision
	decideErr   error
	seen        *autonomy.HumanBoundary
	decideCalls int
	parked      *autonomy.HumanBoundary
}

func (b *scriptedBridge) Decide(_ context.Context, bnd *autonomy.HumanBoundary) (HumanDecision, error) {
	b.decideCalls++
	b.seen = bnd
	if b.decideErr != nil {
		return HumanDecision{}, b.decideErr
	}
	return b.decision, nil
}

func (b *scriptedBridge) RenderParked(bnd *autonomy.HumanBoundary) error {
	b.parked = bnd
	return nil
}

func approvalBoundary() *autonomy.HumanBoundary {
	return &autonomy.HumanBoundary{
		Action:              autonomy.HumanBoundaryApproval,
		Reason:              "mutation awaiting approval",
		RequestID:           "req-1",
		PatchID:             "patch-1",
		Targets:             []string{"zuru.md"},
		CandidateID:         "cand-1",
		CandidateDigest:     "digest-1",
		CandidateTargets:    []string{"zuru.md"},
		CandidateOperation:  "CREATE",
		CandidateDiff:       "@@ -0,0 +1 @@\n+Hello everyone",
		CandidateAddedLines: 1,
		Resumable:           true,
	}
}

func completed() *autonomy.LoopTermination {
	return &autonomy.LoopTermination{State: autonomy.RuntimeCompleted, Reason: "objective satisfied"}
}

// TestAuthoritativeApproveIssuesAuthorizationAndApplies pins the approval
// ordering: the authorization is issued for the reviewed candidate BEFORE the
// driver is resumed, and the resume is an approve.
func TestAuthoritativeApproveIssuesAuthorizationAndApplies(t *testing.T) {
	t.Parallel()

	d := &fakeDriver{boundary: approvalBoundary(), approveTerm: completed()}
	bridge := &scriptedBridge{decision: HumanDecision{Action: HumanApprove}}

	var gotTargets []string
	var gotID, gotDigest string
	authorize := func(targets []string, id, digest string) error {
		gotTargets = append([]string(nil), targets...)
		gotID, gotDigest = id, digest
		return nil
	}

	res, err := RunAuthoritative(context.Background(), d, bridge, authorize, "$orchestrate", "Create zuru.md")
	if err != nil {
		t.Fatalf("RunAuthoritative: %v", err)
	}
	if d.runCalls != 1 || d.approveCalls != 1 {
		t.Fatalf("run/approve calls = %d/%d, want 1/1", d.runCalls, d.approveCalls)
	}
	if d.rejectCalls != 0 {
		t.Fatalf("reject calls = %d, want 0 on approve", d.rejectCalls)
	}
	if len(gotTargets) != 1 || gotTargets[0] != "zuru.md" {
		t.Fatalf("authorized targets = %v, want [zuru.md]", gotTargets)
	}
	if gotID != "cand-1" || gotDigest != "digest-1" {
		t.Fatalf("authorization identity = (%q,%q), want (cand-1,digest-1)", gotID, gotDigest)
	}
	if !res.Approved {
		t.Error("result Approved = false, want true")
	}
	if res.Termination == nil || res.Termination.State != autonomy.RuntimeCompleted {
		t.Fatalf("termination = %+v, want completed", res.Termination)
	}
	if d.scope != "$orchestrate" {
		t.Errorf("scope = %q, want $orchestrate", d.scope)
	}
}

// TestAuthoritativeRejectNeverAuthorizes pins that a rejection cannot apply.
func TestAuthoritativeRejectNeverAuthorizes(t *testing.T) {
	t.Parallel()

	d := &fakeDriver{boundary: approvalBoundary(), rejectTerm: &autonomy.LoopTermination{State: autonomy.RuntimeAborted, Reason: "rejected"}}
	bridge := &scriptedBridge{decision: HumanDecision{Action: HumanReject, Reason: "not now"}}

	authorized := false
	authorize := func([]string, string, string) error { authorized = true; return nil }

	res, err := RunAuthoritative(context.Background(), d, bridge, authorize, "$orchestrate", "Create zuru.md")
	if err != nil {
		t.Fatalf("RunAuthoritative: %v", err)
	}
	if authorized {
		t.Fatal("reject issued a mutation authorization")
	}
	if d.approveCalls != 0 || d.rejectCalls != 1 {
		t.Fatalf("approve/reject calls = %d/%d, want 0/1", d.approveCalls, d.rejectCalls)
	}
	if res.Approved {
		t.Error("result Approved = true on reject")
	}
}

// TestAuthoritativeInspectDoesNotApply pins the historical "i"/inspect
// vocabulary: it is a non-applying rejection, not an apply.
func TestAuthoritativeInspectDoesNotApply(t *testing.T) {
	t.Parallel()

	d := &fakeDriver{boundary: approvalBoundary(), rejectTerm: &autonomy.LoopTermination{State: autonomy.RuntimeAborted, Reason: "inspect"}}
	bridge := &scriptedBridge{decision: HumanDecision{Action: HumanInspect}}

	authorized := false
	res, err := RunAuthoritative(context.Background(), d, bridge,
		func([]string, string, string) error { authorized = true; return nil },
		"$orchestrate", "Create zuru.md")
	if err != nil {
		t.Fatalf("RunAuthoritative: %v", err)
	}
	if authorized {
		t.Fatal("inspect issued a mutation authorization")
	}
	if d.approveCalls != 0 || d.rejectCalls != 1 {
		t.Fatalf("approve/reject calls = %d/%d, want 0/1", d.approveCalls, d.rejectCalls)
	}
	if !res.Inspected || res.Approved {
		t.Fatalf("inspect result = %+v, want Inspected=true Approved=false", res)
	}
}

// TestAuthoritativeAuthorizationRefusalRejectsNotApplies pins that an
// authorization refusal is a rejection, never a silent apply.
func TestAuthoritativeAuthorizationRefusalRejectsNotApplies(t *testing.T) {
	t.Parallel()

	d := &fakeDriver{boundary: approvalBoundary(), rejectTerm: &autonomy.LoopTermination{State: autonomy.RuntimeAborted, Reason: "authorization refused"}}
	bridge := &scriptedBridge{decision: HumanDecision{Action: HumanApprove}}
	refused := errors.New("no checkpoint")

	res, err := RunAuthoritative(context.Background(), d, bridge,
		func([]string, string, string) error { return refused },
		"$orchestrate", "Create zuru.md")
	if err != nil {
		t.Fatalf("RunAuthoritative: %v", err)
	}
	if d.approveCalls != 0 {
		t.Fatal("authorization refusal still resumed approve")
	}
	if d.rejectCalls != 1 {
		t.Fatalf("reject calls = %d, want 1", d.rejectCalls)
	}
	if !strings.Contains(d.lastReject, "authorization refused") {
		t.Errorf("reject reason = %q, want it to name the refusal", d.lastReject)
	}
	if res.Approved {
		t.Error("result Approved = true after authorization refusal")
	}
}

// TestAuthoritativeClarifyResumesWithChosenTarget pins the clarify boundary.
func TestAuthoritativeClarifyResumesWithChosenTarget(t *testing.T) {
	t.Parallel()

	d := &fakeDriver{
		boundary:    &autonomy.HumanBoundary{Action: autonomy.HumanBoundaryClarify, Reason: "ambiguous", Options: []string{"a.md", "b.md"}, Resumable: true},
		clarifyTerm: completed(),
	}
	bridge := &scriptedBridge{decision: HumanDecision{Clarify: "b.md"}}

	res, err := RunAuthoritative(context.Background(), d, bridge, nil, "$orchestrate", "Update the file")
	if err != nil {
		t.Fatalf("RunAuthoritative: %v", err)
	}
	if d.clarifyCalls != 1 || d.lastClarify != "b.md" {
		t.Fatalf("clarify calls/target = %d/%q, want 1/b.md", d.clarifyCalls, d.lastClarify)
	}
	if res.Termination == nil || res.Termination.State != autonomy.RuntimeCompleted {
		t.Fatalf("termination = %+v, want completed", res.Termination)
	}
}

// TestAuthoritativeInformBoundaryParksWithoutResume pins that a non-resumable
// boundary is projected and returned parked, never converted into a resume.
func TestAuthoritativeInformBoundaryParksWithoutResume(t *testing.T) {
	t.Parallel()

	inform := &autonomy.HumanBoundary{Action: autonomy.HumanBoundaryInform, Reason: "recovery exhausted", Resumable: false}
	d := &fakeDriver{boundary: inform}
	bridge := &scriptedBridge{decision: HumanDecision{Action: HumanApprove}}

	res, err := RunAuthoritative(context.Background(), d, bridge, nil, "$orchestrate", "Create zuru.md")
	if err != nil {
		t.Fatalf("RunAuthoritative: %v", err)
	}
	if res.Parked == nil || res.Parked.Action != autonomy.HumanBoundaryInform {
		t.Fatalf("parked = %+v, want the inform boundary", res.Parked)
	}
	if d.approveCalls+d.rejectCalls+d.clarifyCalls+d.abortCalls != 0 {
		t.Fatal("a non-resumable boundary produced a resume/abort transition")
	}
	if bridge.parked == nil {
		t.Error("non-resumable boundary was not projected")
	}
}

// TestAuthoritativeBridgeFailureAborts pins that a human-surface failure is an
// error, not a fabricated reject.
func TestAuthoritativeBridgeFailureAborts(t *testing.T) {
	t.Parallel()

	d := &fakeDriver{boundary: approvalBoundary()}
	bridge := &scriptedBridge{decideErr: errors.New("stdin closed")}

	_, err := RunAuthoritative(context.Background(), d, bridge, nil, "$orchestrate", "Create zuru.md")
	if err == nil || !strings.Contains(err.Error(), "stdin closed") {
		t.Fatalf("err = %v, want the bridge failure", err)
	}
	if d.approveCalls+d.rejectCalls != 0 {
		t.Fatal("a bridge failure produced a resume transition")
	}
}

// TestAuthoritativeCancellationIsPreserved pins that a canceled run surfaces as
// the runtime's own failure, never as success.
func TestAuthoritativeCancellationIsPreserved(t *testing.T) {
	t.Parallel()

	d := &fakeDriver{
		runErr:  context.Canceled,
		runTerm: &autonomy.LoopTermination{State: autonomy.RuntimeAborted, Reason: "cancelled"},
	}
	bridge := &scriptedBridge{decision: HumanDecision{Action: HumanApprove}}

	res, err := RunAuthoritative(context.Background(), d, bridge, nil, "$orchestrate", "Create zuru.md")
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if res.Approved {
		t.Error("a canceled run reported Approved")
	}
	if res.Termination == nil || res.Termination.State != autonomy.RuntimeAborted {
		t.Fatalf("termination = %+v, want aborted", res.Termination)
	}
}

// TestAuthoritativeRejectsMissingPorts pins the fail-closed wiring checks.
func TestAuthoritativeRejectsMissingPorts(t *testing.T) {
	t.Parallel()

	if _, err := RunAuthoritative(context.Background(), nil, &scriptedBridge{}, nil, "", "x"); err == nil {
		t.Error("nil driver accepted")
	}
	if _, err := RunAuthoritative(context.Background(), &fakeDriver{}, nil, nil, "", "x"); err == nil {
		t.Error("nil bridge accepted")
	}
	if _, err := RunAuthoritative(context.Background(), &fakeDriver{}, &scriptedBridge{}, nil, "", "   "); err == nil {
		t.Error("empty objective accepted")
	}
}

// TestDriverTerminalBridgeVocabulary pins the preserved operator vocabulary:
// "y"/"yes" approves, "i"/"inspect" inspects, anything else rejects.
func TestDriverTerminalBridgeVocabulary(t *testing.T) {
	t.Parallel()

	cases := []struct {
		input string
		want  HumanAction
	}{
		{"y\n", HumanApprove},
		{"YES\n", HumanApprove},
		{"i\n", HumanInspect},
		{"inspect\n", HumanInspect},
		{"n\n", HumanReject},
		{"\n", HumanReject},
		{"garbage\n", HumanReject},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			var out bytes.Buffer
			b := NewDriverTerminalBridge(strings.NewReader(tc.input), &out)
			dec, err := b.Decide(context.Background(), approvalBoundary())
			if err != nil {
				t.Fatalf("Decide: %v", err)
			}
			if dec.Action != tc.want {
				t.Errorf("action = %q, want %q", dec.Action, tc.want)
			}
			// The review renders the runtime's OWN candidate diff, byte for byte.
			if !strings.Contains(out.String(), "+Hello everyone") {
				t.Errorf("review did not render the boundary's candidate diff:\n%s", out.String())
			}
		})
	}
}

// TestDriverTerminalBridgeClarifyReadsTarget pins clarify selection.
func TestDriverTerminalBridgeClarifyReadsTarget(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	b := NewDriverTerminalBridge(strings.NewReader("b.md\n"), &out)
	bnd := &autonomy.HumanBoundary{Action: autonomy.HumanBoundaryClarify, Reason: "ambiguous", Options: []string{"a.md", "b.md"}}
	dec, err := b.Decide(context.Background(), bnd)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec.Clarify != "b.md" {
		t.Errorf("clarify = %q, want b.md", dec.Clarify)
	}
	if !strings.Contains(out.String(), "1) a.md") || !strings.Contains(out.String(), "2) b.md") {
		t.Errorf("clarify options not rendered:\n%s", out.String())
	}
}

// TestDriverTerminalBridgeApprovalPolicies pins the non-interactive policies:
// "always" approves without reading input, "never" rejects.
func TestDriverTerminalBridgeApprovalPolicies(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	approve := NewDriverTerminalBridge(strings.NewReader(""), &out).WithPolicy(ApprovalAlways)
	dec, err := approve.Decide(context.Background(), approvalBoundary())
	if err != nil {
		t.Fatalf("always Decide: %v", err)
	}
	if dec.Action != HumanApprove {
		t.Fatalf("always policy action = %q, want approve", dec.Action)
	}

	reject := NewDriverTerminalBridge(strings.NewReader(""), &out).WithPolicy(ApprovalNever)
	dec, err = reject.Decide(context.Background(), approvalBoundary())
	if err != nil {
		t.Fatalf("never Decide: %v", err)
	}
	if dec.Action != HumanReject {
		t.Fatalf("never policy action = %q, want reject", dec.Action)
	}
}

// TestDriverTerminalBridgeClarifyNonInteractive pins that a non-interactive
// policy cannot answer a clarify boundary: it returns an empty decision so the
// lifecycle aborts with a clear human-required outcome instead of guessing.
func TestDriverTerminalBridgeClarifyNonInteractive(t *testing.T) {
	t.Parallel()

	for _, policy := range []ApprovalPolicy{ApprovalAlways, ApprovalNever} {
		var out bytes.Buffer
		b := NewDriverTerminalBridge(strings.NewReader(""), &out).WithPolicy(policy)
		bnd := &autonomy.HumanBoundary{Action: autonomy.HumanBoundaryClarify, Reason: "ambiguous", Options: []string{"a.md", "b.md"}}
		dec, err := b.Decide(context.Background(), bnd)
		if err != nil {
			t.Fatalf("policy %s Decide: %v", policy, err)
		}
		if dec.Clarify != "" {
			t.Fatalf("policy %s guessed clarify target %q", policy, dec.Clarify)
		}
	}
}

// TestRunAuthoritativeAlwaysPolicyApproves proves the always policy drives the
// SAME approve path (authorization then ResumeApprove) as a human "y".
func TestRunAuthoritativeAlwaysPolicyApproves(t *testing.T) {
	t.Parallel()

	d := &fakeDriver{boundary: approvalBoundary(), approveTerm: completed()}
	bridge := NewDriverTerminalBridge(strings.NewReader(""), io.Discard).WithPolicy(ApprovalAlways)

	authorized := false
	res, err := RunAuthoritative(context.Background(), d, bridge,
		func([]string, string, string) error { authorized = true; return nil },
		"$run", "Create zuru.md")
	if err != nil {
		t.Fatalf("RunAuthoritative: %v", err)
	}
	if !authorized {
		t.Fatal("always policy did not issue the candidate-bound authorization")
	}
	if d.approveCalls != 1 || !res.Approved {
		t.Fatalf("approveCalls=%d approved=%t, want 1/true", d.approveCalls, res.Approved)
	}
}

// TestRunAuthoritativeClarifyUnansweredIsHumanRequired proves an unanswered
// clarify boundary sets ClarifyRequired and aborts (never a silent guess).
func TestRunAuthoritativeClarifyUnansweredIsHumanRequired(t *testing.T) {
	t.Parallel()

	d := &fakeDriver{
		boundary:  &autonomy.HumanBoundary{Action: autonomy.HumanBoundaryClarify, Reason: "ambiguous", Options: []string{"a.md"}},
		abortTerm: &autonomy.LoopTermination{State: autonomy.RuntimeAborted, Reason: "clarify boundary left unanswered"},
	}
	bridge := NewDriverTerminalBridge(strings.NewReader(""), io.Discard).WithPolicy(ApprovalAlways)

	res, err := RunAuthoritative(context.Background(), d, bridge, nil, "$run", "Update the file")
	if err != nil {
		t.Fatalf("RunAuthoritative: %v", err)
	}
	if !res.ClarifyRequired {
		t.Fatal("ClarifyRequired = false, want true")
	}
	if d.clarifyCalls != 0 || d.abortCalls != 1 {
		t.Fatalf("clarify=%d abort=%d, want 0/1", d.clarifyCalls, d.abortCalls)
	}
}
