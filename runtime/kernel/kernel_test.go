// Package kernel_test holds the kernel's black-box contract tests.
//
// These tests deliberately import the kernel as an external consumer would. They
// assert observable behaviour — outcomes, axes, events, evidence — and never the
// internal call graph, because the internal structure is free to change and the
// contract is not.
//
// Every test here corresponds to a rule from the package's constitutional
// principles. A rule with no test is a comment, and a comment does not hold.
package kernel_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/runtime/kernel"
)

// ── test doubles ───────────────────────────────────────────────────────────

// stubCapability is a capability whose observation and failure are supplied by
// the test. It exists so the tests can construct any evidence situation without
// touching a real workspace.
type stubCapability struct {
	id  kernel.CapabilityID
	obs kernel.Observation
	err error
	// authorizeErr, when set, makes the capability's own gate refuse.
	authorizeErr error
	// calls records how many times Invoke ran, so a test can prove a refused
	// step never reached the capability at all.
	calls int
}

func (s *stubCapability) ID() kernel.CapabilityID { return s.id }

func (s *stubCapability) Authorize(req kernel.Request) error {
	if s.authorizeErr != nil {
		return s.authorizeErr
	}
	return nil
}

func (s *stubCapability) Invoke(context.Context, kernel.Request) (kernel.Observation, error) {
	s.calls++
	return s.obs, s.err
}

// passing returns a stub that observes a response and succeeds.
//
// It defaults to FileRead because command.run is classified as mutating: it can
// spawn a build that writes files. A test using an OBSERVE or ANSWER contract must
// therefore use a read-only capability, and the admission refusal if it does not is
// the kernel working correctly.
func passing(id kernel.CapabilityID) *stubCapability {
	return &stubCapability{
		id: id,
		obs: kernel.Observation{
			Verdict:     kernel.VerdictPass,
			OutputBytes: 12,
			Detail:      "stub response",
			Facts: []kernel.Fact{{
				Kind:    kernel.EvidenceResponseProduced,
				Bytes:   12,
				Summary: "12 bytes delivered",
			}},
		},
	}
}

// mutating returns a stub that reports a durable write.
func mutating(target string, content string) *stubCapability {
	return &stubCapability{
		id: kernel.FileWrite,
		obs: kernel.Observation{
			Verdict:     kernel.VerdictPass,
			OutputBytes: len(content),
			Detail:      "stub write",
			Facts: []kernel.Fact{
				{Kind: kernel.EvidenceFileWritten, Target: target, Bytes: len(content)},
				{Kind: kernel.EvidenceFilePresent, Target: target, Bytes: len(content)},
			},
		},
	}
}

// observing returns a stub that reports a real inspection: the workspace was
// walked AND the named target was individually stat'd. An OBSERVE contract
// requires both, so a double that reported only one of them would be modelling
// exactly the gap the contract exists to close.
func observing(target string) *stubCapability {
	return &stubCapability{
		id: kernel.FileExists,
		obs: kernel.Observation{
			Verdict: kernel.VerdictPass,
			Detail:  "stub observation",
			Facts: []kernel.Fact{
				{
					Kind:    kernel.EvidenceWorkspaceObserved,
					Bytes:   3,
					Summary: "walked 3 entries",
				},
				{
					Kind:    kernel.EvidenceFilePresent,
					Target:  target,
					Summary: "stub stat",
				},
			},
		},
	}
}

// silent returns a stub that succeeds but observes nothing at all. It models the
// most dangerous capability behaviour there is: a provider that stops emitting
// tokens.
func silent(id kernel.CapabilityID) *stubCapability {
	return &stubCapability{
		id:  id,
		obs: kernel.Observation{Verdict: kernel.VerdictPass, Detail: "no observation"},
	}
}

// fullGrant permits everything in the closed vocabulary over the given targets.
func fullGrant(t *testing.T, id string, targets ...string) kernel.Grant {
	t.Helper()
	g, err := kernel.NewGrant(id, kernel.AllCapabilityIDs(), targets)
	if err != nil {
		t.Fatalf("build grant: %v", err)
	}
	return g
}

// newTestEngine builds an engine over the given stubs.
func newTestEngine(t *testing.T, verifier kernel.Verifier, caps ...kernel.Capability) *kernel.Engine {
	t.Helper()
	registry, err := kernel.NewRegistry(caps...)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	opts := []kernel.Option{}
	if verifier != nil {
		opts = append(opts, kernel.WithVerifier(verifier))
	}
	engine, err := kernel.NewEngine(registry, opts...)
	if err != nil {
		t.Fatalf("build engine: %v", err)
	}
	return engine
}

// alwaysPass is a verifier that passes unconditionally. It exists to isolate the
// verification axis in tests that are not about it.
var alwaysPass = kernel.VerifierFunc(func(context.Context, kernel.VerificationRequest) (kernel.Verdict, error) {
	return kernel.VerdictPass, nil
})

// ── Rule: no authorization, no execution ───────────────────────────────────

// TestUnauthorizedStepIsNeverInvoked proves the first limb of the acceptance
// invariant: a step outside the grant does not reach its capability.
//
// The assertion is on calls, not on the outcome. A refused step that still
// invoked its capability would be a far worse defect than a wrong outcome label,
// because the workspace would have changed.
func TestUnauthorizedStepIsNeverInvoked(t *testing.T) {
	caps := mutating("notes.md", "hello")
	caps.id = kernel.FileWrite
	engine := newTestEngine(t, alwaysPass, caps)

	spec := kernel.Spec{
		ExecutionID: "exec-auth-1",
		Objective:   "write notes.md",
		Contract: kernel.Contract{
			Kind:                 kernel.ContractCreate,
			Targets:              []string{"notes.md"},
			RequiresObservation:  false,
			RequiresVerification: true,
		},
		Program: kernel.Program{{
			ID:         "write",
			Capability: kernel.FileWrite,
			Target:     "notes.md",
			Args:       map[string]string{"content": "hello"},
		}},
	}

	// A read-only grant. It permits FileExists but not FileWrite.
	readOnly, err := kernel.NewGrant("grant-read", []kernel.CapabilityID{kernel.FileExists}, nil)
	if err != nil {
		t.Fatalf("build grant: %v", err)
	}

	if err := engine.Open(spec, readOnly); err == nil {
		t.Fatal("Open accepted a spec whose mutating step is outside the grant; " +
			"admission must refuse before anything is dispatched")
	} else if kernel.ClassOf(err) != kernel.FailureAuthorization {
		t.Fatalf("Open refused with class %q, want %q", kernel.ClassOf(err), kernel.FailureAuthorization)
	}

	result := engine.Run(context.Background())
	if result.Proves() {
		t.Fatal("an execution refused at admission reported PROVEN")
	}
	if result.Outcome != kernel.OutcomeRequiresAuthorization {
		t.Fatalf("outcome = %q, want %q", result.Outcome, kernel.OutcomeRequiresAuthorization)
	}
	if caps.calls != 0 {
		t.Fatalf("capability was invoked %d times despite the grant refusing it; "+
			"authorization must be checked before invocation", caps.calls)
	}
}

// TestNarrowedGrantCannotBeWidened proves that authority is a property of the
// value, not a convention: the only way to change a Grant is to narrow it.
func TestNarrowedGrantCannotBeWidened(t *testing.T) {
	full := fullGrant(t, "g1", "a.txt", "b.txt")
	narrow := full.Narrow([]kernel.CapabilityID{kernel.FileExists}, []string{"a.txt"})

	if narrow.Permits(kernel.FileWrite) {
		t.Error("a narrowed grant still permits file.write; Narrow must never widen authority")
	}
	if narrow.Permits(kernel.FileRead) {
		t.Error("a narrowed grant still permits file.read; Narrow must never widen authority")
	}
	if !narrow.Permits(kernel.FileExists) {
		t.Error("a narrowed grant dropped the capability it was narrowed to")
	}
	if narrow.Covers("b.txt", []string{"a.txt", "b.txt"}) {
		t.Error("a narrowed grant still covers a target it was narrowed away from")
	}
	if !narrow.Covers("a.txt", []string{"a.txt", "b.txt"}) {
		t.Error("a narrowed grant dropped the target it was narrowed to")
	}
	// The original must be unaffected: narrowing returns a copy.
	if !full.Permits(kernel.FileWrite) {
		t.Error("narrowing mutated the original grant")
	}
}

// TestGrantNeverCoversUnnamedTarget proves an empty grant target set does not
// become "everything".
func TestGrantNeverCoversUnnamedTarget(t *testing.T) {
	g, err := kernel.NewGrant("g1", []kernel.CapabilityID{kernel.FileRead}, nil)
	if err != nil {
		t.Fatalf("build grant: %v", err)
	}
	if g.Covers("secrets.env", []string{"notes.md"}) {
		t.Error("a grant with no declared targets covered an unrelated file; " +
			"an empty target set must mean the contract's targets only")
	}
}

// ── Rule: no capability, no fake capability ───────────────────────────────

// TestMissingCapabilityIsRefusedNotFaked proves the second limb: a program naming
// an unregistered capability is refused at admission.
//
// The test asserts on the class specifically. Reporting this as an execution
// failure would be a lie that sends an operator looking at the wrong subsystem.
func TestMissingCapabilityIsRefusedNotFaked(t *testing.T) {
	engine := newTestEngine(t, alwaysPass, passing(kernel.FileRead))

	spec := kernel.Spec{
		ExecutionID: "exec-missing-1",
		Contract:    kernel.Contract{Kind: kernel.ContractObserve, Targets: []string{"notes.md"}, RequiresObservation: true},
		Program: kernel.Program{{
			ID:         "read",
			Capability: kernel.CommandRun,
			Target:     "notes.md",
		}},
	}

	err := engine.Open(spec, fullGrant(t, "g1", "notes.md"))
	if err == nil {
		t.Fatal("Open accepted a spec requiring an unregistered capability")
	}
	if got := kernel.ClassOf(err); got != kernel.FailureCapabilityUnavailable {
		t.Fatalf("refusal class = %q, want %q", got, kernel.FailureCapabilityUnavailable)
	}
}

// ── Rule: no evidence, no PROVEN ───────────────────────────────────────────

// TestProviderCompletingWithoutObservationIsNotProven is the central test of the
// kernel.
//
// It models the failure this design exists to eliminate: a capability that
// reports success and observes nothing at all. The transport axis reaches DONE —
// the socket closed — and that must not produce a completion.
func TestProviderCompletingWithoutObservationIsNotProven(t *testing.T) {
	engine := newTestEngine(t, alwaysPass, silent(kernel.FileRead))

	spec := kernel.Spec{
		ExecutionID: "exec-unsubstantiated-1",
		Objective:   "inspect the workspace",
		Contract: kernel.Contract{
			Kind:                kernel.ContractObserve,
			Targets:             []string{"notes.md"},
			RequiresObservation: true,
		},
		Program: kernel.Program{{
			ID:         "observe",
			Capability: kernel.FileRead,
			Target:     "notes.md",
		}},
	}

	if err := engine.Open(spec, fullGrant(t, "g1", "notes.md")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	result := engine.Run(context.Background())

	if result.Proves() {
		t.Fatal("a capability that observed nothing was reported as PROVEN; " +
			"completion must be evidence-gated")
	}
	if result.Outcome != kernel.OutcomeUnsubstantiated {
		t.Fatalf("outcome = %q, want %q", result.Outcome, kernel.OutcomeUnsubstantiated)
	}
	if len(result.Unmet) == 0 {
		t.Error("an unsubstantiated outcome named no unmet clause; " +
			"a reader must be able to see which obligation went unsatisfied")
	}
	// The transport did complete. That fact must be preserved separately rather
	// than discarded, which is the whole point of the axis split.
	if result.State.Provider != kernel.ProviderDone {
		t.Errorf("provider axis = %q, want %q; the transport fact must be recorded even when it proves nothing",
			result.State.Provider, kernel.ProviderDone)
	}
	if result.State.Artifact != kernel.ArtifactNone {
		t.Errorf("artifact axis = %q, want %q; no artifact was extracted", result.State.Artifact, kernel.ArtifactNone)
	}
}

// TestVerdictUnknownIsNotSuccess proves an undetermined outcome cannot pass.
//
// VerdictUnknown is what a capability reports when it cannot tell whether its own
// work landed. Treating that as a pass is how unsubstantiated claims enter a
// runtime, so the reducer must refuse it.
func TestVerdictUnknownIsNotSuccess(t *testing.T) {
	unknown := &stubCapability{
		id: kernel.FileRead,
		obs: kernel.Observation{
			Verdict: kernel.VerdictUnknown,
			Detail:  "could not determine outcome",
			Facts: []kernel.Fact{{
				Kind:    kernel.EvidenceResponseProduced,
				Target:  "notes.md",
				Summary: "a response of unknown provenance",
			}},
		},
	}
	engine := newTestEngine(t, alwaysPass, unknown)

	spec := kernel.Spec{
		ExecutionID: "exec-unknown-1",
		Contract:    kernel.Contract{Kind: kernel.ContractAnswer},
		Program:     kernel.Program{{ID: "answer", Capability: kernel.FileRead}},
	}
	if err := engine.Open(spec, fullGrant(t, "g1")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	result := engine.Run(context.Background())

	if result.Outcome != kernel.OutcomeFailed {
		t.Fatalf("outcome = %q, want %q: an undetermined verdict is a failure, not a success",
			result.Outcome, kernel.OutcomeFailed)
	}
	if result.Class != kernel.FailureCapability {
		t.Fatalf("class = %q, want %q", result.Class, kernel.FailureCapability)
	}
}

// ── Rule: the four axes never collapse ─────────────────────────────────────

// TestAxesAreReportedSeparately proves the axis split survives to the result.
//
// The test reads Result.String() and checks that each axis appears with its own
// value. A projection that reported a single success word would have to lie about
// at least three boundaries.
func TestAxesAreReportedSeparately(t *testing.T) {
	engine := newTestEngine(t, alwaysPass, observing("notes.md"))

	spec := kernel.Spec{
		ExecutionID: "exec-axes-1",
		Contract:    kernel.Contract{Kind: kernel.ContractObserve, Targets: []string{"notes.md"}, RequiresObservation: true},
		Program:     kernel.Program{{ID: "stat", Capability: kernel.FileExists, Target: "notes.md"}},
	}
	if err := engine.Open(spec, fullGrant(t, "g1", "notes.md")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	result := engine.Run(context.Background())

	report := result.String()
	for _, want := range []string{
		"provider=" + string(kernel.ProviderDone),
		"artifact=" + string(kernel.ArtifactNone),
		"mutation=" + string(kernel.MutationNone),
		"verify=" + string(kernel.VerifyNotApplicable),
	} {
		if !strings.Contains(report, want) {
			t.Errorf("result report missing %q\nreport:\n%s", want, report)
		}
	}
}

// TestArtifactProducedDoesNotImplyMutation proves the second and third axes are
// genuinely independent: producing an artifact is not mutating the workspace.
func TestArtifactProducedDoesNotImplyMutation(t *testing.T) {
	engine := newTestEngine(t, alwaysPass, passing(kernel.FileRead))

	spec := kernel.Spec{
		ExecutionID: "exec-artifact-only",
		Contract:    kernel.Contract{Kind: kernel.ContractAnswer},
		Program:     kernel.Program{{ID: "answer", Capability: kernel.FileRead}},
	}
	if err := engine.Open(spec, fullGrant(t, "g1")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	result := engine.Run(context.Background())

	if result.State.Artifact != kernel.ArtifactProduced {
		t.Fatalf("artifact axis = %q, want %q", result.State.Artifact, kernel.ArtifactProduced)
	}
	if result.State.Mutation.Durable() {
		t.Fatal("an ANSWER contract reported a durable mutation; " +
			"a produced artifact must never imply the workspace changed")
	}
}

// ── Rule: mutation is authorized and evidence-backed ───────────────────────

// TestSuccessfulWriteProvesCreate proves the positive path: a real observation
// set satisfies a CREATE contract and PROVEN is reachable.
func TestSuccessfulWriteProvesCreate(t *testing.T) {
	write := mutating("notes.md", "hello world")
	engine := newTestEngine(t, alwaysPass, write)

	spec := kernel.Spec{
		ExecutionID: "exec-create-1",
		Objective:   "create notes.md",
		Contract: kernel.Contract{
			Kind:                 kernel.ContractCreate,
			Targets:              []string{"notes.md"},
			RequiresVerification: true,
		},
		Program: kernel.Program{{
			ID:         "write",
			Capability: kernel.FileWrite,
			Target:     "notes.md",
			Args:       map[string]string{"content": "hello world"},
		}},
	}
	if err := engine.Open(spec, fullGrant(t, "g1", "notes.md")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	result := engine.Run(context.Background())

	if !result.Proves() {
		t.Fatalf("a satisfied CREATE contract was not PROVEN\noutcome=%s reason=%s unmet=%v",
			result.Outcome, result.Reason, result.Unmet)
	}
	if !result.State.Mutation.Durable() {
		t.Error("a PROVEN create did not hold a durable mutation axis")
	}
	if mutated := result.MutatedTargets(); len(mutated) != 1 || mutated[0] != "notes.md" {
		t.Errorf("mutated targets = %v, want [notes.md]", mutated)
	}
	if result.State.Verify != kernel.VerifyPassed {
		t.Errorf("verify axis = %q, want %q", result.State.Verify, kernel.VerifyPassed)
	}
}

// TestWriteFailureIsNotMutation proves the mutation axis is derived from
// evidence rather than from intent to write.
func TestWriteFailureIsNotMutation(t *testing.T) {
	failing := &stubCapability{
		id: kernel.FileWrite,
		obs: kernel.Observation{
			Verdict: kernel.VerdictFail,
			Detail:  "commit failed: disk full",
		},
	}
	engine := newTestEngine(t, alwaysPass, failing)

	spec := kernel.Spec{
		ExecutionID: "exec-write-fail",
		Contract: kernel.Contract{
			Kind:                 kernel.ContractCreate,
			Targets:              []string{"notes.md"},
			RequiresVerification: true,
		},
		Program: kernel.Program{{
			ID:         "write",
			Capability: kernel.FileWrite,
			Target:     "notes.md",
			Args:       map[string]string{"content": "x"},
		}},
	}
	if err := engine.Open(spec, fullGrant(t, "g1", "notes.md")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	result := engine.Run(context.Background())

	if result.State.Mutation.Durable() {
		t.Error("a failed write left the mutation axis APPLIED; " +
			"the mutation axis must be derived from write evidence, never from intent")
	}
	if result.Proves() {
		t.Fatal("a failed write was reported PROVEN")
	}
	if result.Class != kernel.FailureCapability {
		t.Errorf("class = %q, want %q", result.Class, kernel.FailureCapability)
	}
}

// TestReadOnlyCapabilityCannotClaimMutation proves the reducer cross-checks
// evidence against the step that produced it.
//
// This is the check that stops a read-only capability from forging write
// evidence. It is a kernel invariant, not a trust assumption.
func TestReadOnlyCapabilityCannotClaimMutation(t *testing.T) {
	forger := &stubCapability{
		id: kernel.FileRead,
		obs: kernel.Observation{
			Verdict: kernel.VerdictPass,
			Facts: []kernel.Fact{{
				Kind:   kernel.EvidenceFileWritten,
				Target: "notes.md",
			}},
		},
	}
	engine := newTestEngine(t, alwaysPass, forger)

	spec := kernel.Spec{
		ExecutionID: "exec-forge",
		Contract: kernel.Contract{
			Kind:                 kernel.ContractCreate,
			Targets:              []string{"notes.md"},
			RequiresVerification: true,
		},
		Program: kernel.Program{{
			ID:         "write",
			Capability: kernel.FileRead,
			Target:     "notes.md",
		}},
	}
	// A CREATE contract with a read-only step is refused at admission, so this
	// program never runs. Assert that first: the reducer check is a backstop, not
	// the primary defence.
	if err := engine.Open(spec, fullGrant(t, "g1", "notes.md")); err == nil {
		t.Error("Open accepted a CREATE contract with no mutating step")
	}

	// Now force the same evidence through a program that does admit, and confirm
	// the reducer refuses the forged record.
	observeSpec := kernel.Spec{
		ExecutionID: "exec-forge-2",
		Contract:    kernel.Contract{Kind: kernel.ContractObserve, Targets: []string{"notes.md"}, RequiresObservation: true},
		Program:     kernel.Program{{ID: "read", Capability: kernel.FileRead, Target: "notes.md"}},
	}
	engine2 := newTestEngine(t, nil, forger)
	if err := engine2.Open(observeSpec, fullGrant(t, "g1", "notes.md")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	result := engine2.Run(context.Background())
	if result.State.Mutation.Durable() {
		t.Fatal("the reducer accepted write evidence from a read-only capability; " +
			"evidence must be cross-checked against the step that produced it")
	}
}

// ── Rule: verification is explicit ─────────────────────────────────────────

// TestFailedVerificationIsFailedNotUnproven proves FAILED is distinguishable from
// UNSUBSTANTIATED.
//
// The distinction is operational: a failed verification means "something is
// wrong and I can say what", while unsubstantiated means "I do not know". A
// runtime that conflates them sends an operator to the wrong subsystem.
func TestFailedVerificationIsFailedNotUnproven(t *testing.T) {
	verifier := kernel.VerifierFunc(func(context.Context, kernel.VerificationRequest) (kernel.Verdict, error) {
		return kernel.VerdictFail, nil
	})
	write := mutating("notes.md", "x")
	engine := newTestEngine(t, verifier, write)

	spec := kernel.Spec{
		ExecutionID: "exec-verify-fail",
		Contract: kernel.Contract{
			Kind:                 kernel.ContractCreate,
			Targets:              []string{"notes.md"},
			RequiresVerification: true,
		},
		Program: kernel.Program{{
			ID:         "write",
			Capability: kernel.FileWrite,
			Target:     "notes.md",
			Args:       map[string]string{"content": "x"},
		}},
	}
	if err := engine.Open(spec, fullGrant(t, "g1", "notes.md")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	result := engine.Run(context.Background())

	if result.Outcome != kernel.OutcomeFailed {
		t.Fatalf("outcome = %q, want %q: a failed verifier is a positive failure",
			result.Outcome, kernel.OutcomeFailed)
	}
	if result.State.Verify != kernel.VerifyFailed {
		t.Errorf("verify axis = %q, want %q", result.State.Verify, kernel.VerifyFailed)
	}
}

// TestVerificationSkippedIsNotVerificationPassed proves the not-applicable
// verdict stays distinguishable from a pass in the axis itself.
func TestVerificationSkippedIsNotVerificationPassed(t *testing.T) {
	verifier := kernel.VerifierFunc(func(context.Context, kernel.VerificationRequest) (kernel.Verdict, error) {
		return kernel.VerdictNotApplicable, nil
	})
	write := mutating("notes.md", "x")
	engine := newTestEngine(t, verifier, write)

	spec := kernel.Spec{
		ExecutionID: "exec-verify-na",
		Contract: kernel.Contract{
			Kind:                 kernel.ContractCreate,
			Targets:              []string{"notes.md"},
			RequiresVerification: true,
		},
		Program: kernel.Program{{
			ID:         "write",
			Capability: kernel.FileWrite,
			Target:     "notes.md",
			Args:       map[string]string{"content": "x"},
		}},
	}
	if err := engine.Open(spec, fullGrant(t, "g1", "notes.md")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	result := engine.Run(context.Background())

	if result.State.Verify != kernel.VerifyNotApplicable {
		t.Errorf("verify axis = %q, want %q", result.State.Verify, kernel.VerifyNotApplicable)
	}
	if result.State.Verify == kernel.VerifyPassed {
		t.Error("a skipped verification was recorded as passed")
	}
	if !result.Proves() {
		t.Errorf("a provably not-applicable gate should satisfy the requirement\noutcome=%s reason=%s",
			result.Outcome, result.Reason)
	}
}

// TestMissingVerifierCannotSatisfyARequiredGate proves the kernel refuses to
// invent a pass. Without a verifier, a contract that requires verification is
// unmet rather than quietly satisfied.
func TestMissingVerifierCannotSatisfyARequiredGate(t *testing.T) {
	write := mutating("notes.md", "x")
	engine := newTestEngine(t, nil, write)

	spec := kernel.Spec{
		ExecutionID: "exec-no-verifier",
		Contract: kernel.Contract{
			Kind:                 kernel.ContractCreate,
			Targets:              []string{"notes.md"},
			RequiresVerification: true,
		},
		Program: kernel.Program{{
			ID:         "write",
			Capability: kernel.FileWrite,
			Target:     "notes.md",
			Args:       map[string]string{"content": "x"},
		}},
	}
	if err := engine.Open(spec, fullGrant(t, "g1", "notes.md")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	result := engine.Run(context.Background())

	if result.Proves() {
		t.Fatal("a required verification was satisfied with no verifier bound; " +
			"the kernel must not manufacture a pass")
	}
	if result.State.Verify != kernel.VerifyNotRun {
		t.Errorf("verify axis = %q, want %q: a required check that never ran must not be recorded as skipped",
			result.State.Verify, kernel.VerifyNotRun)
	}
	if result.Class != kernel.FailureVerification {
		t.Errorf("class = %q, want %q: the missing verifier must be named as the cause", result.Class, kernel.FailureVerification)
	}
}

// ── Rule: budget exhaustion is explicit, never success ─────────────────────

// TestBudgetExhaustionIsNotSuccess proves truncation is never reported as
// completion.
//
// The test declares a two-step program with a one-step budget. The second step
// must be refused, the execution must settle as BUDGET_EXHAUSTED, and the first
// step's work must still be recorded honestly.
func TestBudgetExhaustionIsNotSuccess(t *testing.T) {
	first := mutating("a.txt", "a")
	second := &stubCapability{
		id: kernel.CommandRun,
		obs: kernel.Observation{
			Verdict:     kernel.VerdictPass,
			OutputBytes: 1,
			Detail:      "stub command",
			Facts: []kernel.Fact{
				{Kind: kernel.EvidenceFileWritten, Target: "b.txt", Bytes: 1},
				{Kind: kernel.EvidenceFilePresent, Target: "b.txt", Bytes: 1},
			},
		},
	}
	engine := newTestEngine(t, alwaysPass, first, second)

	spec := kernel.Spec{
		ExecutionID: "exec-budget-1",
		Contract: kernel.Contract{
			Kind:                 kernel.ContractPatch,
			Targets:              []string{"a.txt", "b.txt"},
			RequiresVerification: true,
		},
		Program: kernel.Program{
			{ID: "a", Capability: kernel.FileWrite, Target: "a.txt", Args: map[string]string{"content": "a"}},
			{ID: "b", Capability: kernel.FileWrite, Target: "b.txt", Args: map[string]string{"content": "b"}},
		},
		Budget: kernel.Budget{MaxSteps: 1},
	}
	if err := engine.Open(spec, fullGrant(t, "g1", "a.txt", "b.txt")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	result := engine.Run(context.Background())

	if result.Proves() {
		t.Fatal("a budget-exhausted execution reported PROVEN; " +
			"truncation must never be rendered as success")
	}
	if result.Outcome != kernel.OutcomeBudgetExhausted {
		t.Fatalf("outcome = %q, want %q", result.Outcome, kernel.OutcomeBudgetExhausted)
	}
	if result.Class != kernel.FailureBudgetExhausted {
		t.Errorf("class = %q, want %q", result.Class, kernel.FailureBudgetExhausted)
	}
	// The first step's work is real and must survive in the record.
	if second.calls != 0 {
		t.Errorf("the step beyond the budget was invoked %d times", second.calls)
	}
	if first.calls != 1 {
		t.Errorf("the step within the budget was invoked %d times, want 1", first.calls)
	}
	if result.Budget.StepsInvoked != 1 {
		t.Errorf("steps charged = %d, want 1", result.Budget.StepsInvoked)
	}
	if result.Budget.Remaining() != 0 {
		t.Errorf("remaining budget = %d, want 0", result.Budget.Remaining())
	}
}

// TestUnboundedBudgetIsNotZeroBudget proves the unbound sentinel is
// distinguishable from an exhausted budget.
//
// Reporting "unbounded" as zero remaining would make a caller believe a limit was
// reached when none was declared, which is how a runtime silently stops working.
func TestUnboundedBudgetIsNotZeroBudget(t *testing.T) {
	var acct kernel.Accounting
	if !kernel.UnboundedSentinel(acct.Remaining()) {
		t.Errorf("an undeclared budget reported %d remaining, want the unbound sentinel", acct.Remaining())
	}
	bounded := kernel.Accounting{Declared: kernel.Budget{MaxSteps: 3}}
	if kernel.UnboundedSentinel(bounded.Remaining()) {
		t.Error("a declared bound reported the unbound sentinel")
	}
	if bounded.Remaining() != 3 {
		t.Errorf("remaining = %d, want 3", bounded.Remaining())
	}
}

// TestNegativeBudgetIsRefused proves a limit is never silently clamped.
func TestNegativeBudgetIsRefused(t *testing.T) {
	engine := newTestEngine(t, alwaysPass, passing(kernel.FileRead))
	spec := kernel.Spec{
		ExecutionID: "exec-neg-budget",
		Contract:    kernel.Contract{Kind: kernel.ContractAnswer},
		Program:     kernel.Program{{ID: "x", Capability: kernel.FileRead}},
		Budget:      kernel.Budget{MaxSteps: -1},
	}
	err := engine.Open(spec, fullGrant(t, "g1"))
	if err == nil {
		t.Fatal("Open accepted a negative budget bound")
	}
	if got := kernel.ClassOf(err); got != kernel.FailureInvalidSpec {
		t.Errorf("class = %q, want %q", got, kernel.FailureInvalidSpec)
	}
}

// ── Rule: cancellation and interruption are distinct ───────────────────────

// TestCancellationIsDistinctFromFailure proves a deliberate withdrawal is
// classified as cancellation, not as a generic error.
func TestCancellationIsDistinctFromFailure(t *testing.T) {
	caps := mutating("notes.md", "x")
	engine := newTestEngine(t, alwaysPass, caps)
	engine.Cancel()

	spec := kernel.Spec{
		ExecutionID: "exec-cancel-1",
		Contract: kernel.Contract{
			Kind:                 kernel.ContractCreate,
			Targets:              []string{"notes.md"},
			RequiresVerification: true,
		},
		Program: kernel.Program{{
			ID:         "write",
			Capability: kernel.FileWrite,
			Target:     "notes.md",
			Args:       map[string]string{"content": "x"},
		}},
	}
	if err := engine.Open(spec, fullGrant(t, "g1", "notes.md")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	result := engine.Run(context.Background())

	if result.Outcome != kernel.OutcomeCancelled {
		t.Fatalf("outcome = %q, want %q", result.Outcome, kernel.OutcomeCancelled)
	}
	if result.Class != kernel.FailureCancelled {
		t.Errorf("class = %q, want %q", result.Class, kernel.FailureCancelled)
	}
	if caps.calls != 0 {
		t.Errorf("a cancelled execution still invoked its capability %d times", caps.calls)
	}
	// A cancelled execution is not an error the caller must handle as a defect.
	if err := result.Err(); err != nil {
		if kernel.ClassOf(err) == kernel.FailureCancelled {
			t.Log("cancellation surfaced as a typed error, which is acceptable")
		}
	}
}

// TestContextCancellationIsInterruption proves a context-driven stop is
// distinguished from a deliberate Cancel() call.
func TestContextCancellationIsInterruption(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	caps := mutating("notes.md", "x")
	engine := newTestEngine(t, alwaysPass, caps)

	spec := kernel.Spec{
		ExecutionID: "exec-interrupt-1",
		Contract: kernel.Contract{
			Kind:                 kernel.ContractCreate,
			Targets:              []string{"notes.md"},
			RequiresVerification: true,
		},
		Program: kernel.Program{{
			ID:         "write",
			Capability: kernel.FileWrite,
			Target:     "notes.md",
			Args:       map[string]string{"content": "x"},
		}},
	}
	if err := engine.Open(spec, fullGrant(t, "g1", "notes.md")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	result := engine.Run(ctx)

	if result.Outcome != kernel.OutcomeInterrupted {
		t.Fatalf("outcome = %q, want %q", result.Outcome, kernel.OutcomeInterrupted)
	}
	if caps.calls != 0 {
		t.Errorf("an interrupted execution still invoked its capability %d times", caps.calls)
	}
}

// ── Rule: events describe only what happened ───────────────────────────────

// TestNoEventOutrunsItsFact is the anti-optimism test.
//
// It walks the whole event log after a failed execution and asserts that no event
// claims something that did not occur: no mutation.applied without write evidence,
// no verification.passed without a verification event, no execution.finished
// before execution.started.
func TestNoEventOutrunsItsFact(t *testing.T) {
	verifier := kernel.VerifierFunc(func(context.Context, kernel.VerificationRequest) (kernel.Verdict, error) {
		return kernel.VerdictFail, nil
	})
	write := mutating("notes.md", "x")
	engine := newTestEngine(t, verifier, write)

	spec := kernel.Spec{
		ExecutionID: "exec-events-1",
		Contract: kernel.Contract{
			Kind:                 kernel.ContractCreate,
			Targets:              []string{"notes.md"},
			RequiresVerification: true,
		},
		Program: kernel.Program{{
			ID:         "write",
			Capability: kernel.FileWrite,
			Target:     "notes.md",
			Args:       map[string]string{"content": "x"},
		}},
	}
	if err := engine.Open(spec, fullGrant(t, "g1", "notes.md")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = engine.Run(context.Background())

	events := engine.Log().Events()
	if len(events) == 0 {
		t.Fatal("the event log is empty; the log is the durable record of execution truth")
	}

	var sawStart bool
	sawVerifyStarted := false
	for _, ev := range events {
		switch ev.Kind {
		case kernel.EventExecutionStarted:
			if sawStart {
				t.Error("execution.started appeared twice")
			}
			sawStart = true
		case kernel.EventExecutionFinished, kernel.EventExecutionFailed:
			if !sawStart {
				t.Error("the execution settled before it started")
			}
		case kernel.EventMutationApplied:
			// Cross-check: the write evidence must already be in the log.
			if len(engine.Log().OfKinds(kernel.EventEvidenceProduced)) == 0 {
				t.Error("mutation.applied appeared with no evidence.produced event before it")
			}
		case kernel.EventVerificationStarted:
			sawVerifyStarted = true
		case kernel.EventVerificationPassed:
			if !sawVerifyStarted {
				t.Error("verification.passed appeared without a verification.started event")
			}
		}
	}

	if !sawStart {
		t.Error("no execution.started event was recorded")
	}
}

// TestEventLogRevisionsAreMonotonic proves the log has exactly one order and one
// revision per transition, which is what lets a consumer detect a missed update.
func TestEventLogRevisionsAreMonotonic(t *testing.T) {
	engine := newTestEngine(t, alwaysPass, observing("notes.md"))
	spec := kernel.Spec{
		ExecutionID: "exec-rev-1",
		Contract:    kernel.Contract{Kind: kernel.ContractObserve, Targets: []string{"notes.md"}, RequiresObservation: true},
		Program:     kernel.Program{{ID: "stat", Capability: kernel.FileExists, Target: "notes.md"}},
	}
	if err := engine.Open(spec, fullGrant(t, "g1", "notes.md")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	engine.Run(context.Background())

	events := engine.Log().Events()
	var lastSeq, lastRev uint64
	for i, ev := range events {
		if ev.Seq != uint64(i+1) {
			t.Fatalf("event %d has seq %d, want %d", i, ev.Seq, i+1)
		}
		if ev.Revision <= lastRev {
			t.Fatalf("event %d has revision %d, which does not exceed the previous %d", i, ev.Revision, lastRev)
		}
		lastSeq, lastRev = ev.Seq, ev.Revision
	}
	if engine.Log().LastRevision() != lastRev {
		t.Errorf("LastRevision = %d, want %d", engine.Log().LastRevision(), lastRev)
	}
	if lastSeq != uint64(len(events)) {
		t.Errorf("last seq = %d, want %d", lastSeq, len(events))
	}
}

// TestSettledExecutionIsFinal proves terminal truth cannot be rewritten.
//
// A late event arriving after adjudication must be refused rather than applied,
// otherwise a slow capability could overwrite the verdict the runtime already
// published.
func TestSettledExecutionIsFinal(t *testing.T) {
	engine := newTestEngine(t, alwaysPass, observing("notes.md"))
	spec := kernel.Spec{
		ExecutionID: "exec-final-1",
		Contract:    kernel.Contract{Kind: kernel.ContractObserve, Targets: []string{"notes.md"}, RequiresObservation: true},
		Program:     kernel.Program{{ID: "stat", Capability: kernel.FileExists, Target: "notes.md"}},
	}
	if err := engine.Open(spec, fullGrant(t, "g1", "notes.md")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	result := engine.Run(context.Background())
	if !result.Settled() {
		t.Fatal("the execution did not settle")
	}
	before := engine.State().Revision
	outcome := engine.State().Terminal.Outcome

	// Drive a fresh run over a settled engine. It must not reopen anything.
	second := engine.Run(context.Background())
	if engine.State().Revision != before {
		t.Errorf("a settled execution advanced its revision from %d to %d; terminal truth must be final",
			before, engine.State().Revision)
	}
	if engine.State().Terminal.Outcome != outcome {
		t.Errorf("the settled outcome changed from %q to %q", outcome, engine.State().Terminal.Outcome)
	}
	if second.Proves() && !result.Proves() {
		t.Error("a second run reported PROVEN on an execution that had settled UNSUBSTANTIATED")
	}
}

// ── Rule: the spec is validated before anything runs ───────────────────────

// TestInvalidSpecsAreRefusedAtAdmission is the table-driven admission test.
//
// Every row is a spec that cannot be executed as written. Each must be refused by
// Open, before any capability is invoked, because a program that fails halfway
// through having already mutated something is the worst outcome available.
func TestInvalidSpecsAreRefusedAtAdmission(t *testing.T) {
	tests := []struct {
		name string
		spec kernel.Spec
		want kernel.FailureClass
	}{
		{
			name: "no execution id",
			spec: kernel.Spec{
				Contract: kernel.Contract{Kind: kernel.ContractAnswer},
				Program:  kernel.Program{{ID: "x", Capability: kernel.CommandRun}},
			},
			want: kernel.FailureInvalidSpec,
		},
		{
			name: "no steps",
			spec: kernel.Spec{
				ExecutionID: "e1",
				Contract:    kernel.Contract{Kind: kernel.ContractAnswer},
			},
			want: kernel.FailureInvalidSpec,
		},
		{
			name: "duplicate step id",
			spec: kernel.Spec{
				ExecutionID: "e2",
				Contract:    kernel.Contract{Kind: kernel.ContractAnswer},
				Program: kernel.Program{
					{ID: "x", Capability: kernel.CommandRun},
					{ID: "x", Capability: kernel.CommandRun},
				},
			},
			want: kernel.FailureInvalidSpec,
		},
		{
			name: "capability outside the vocabulary",
			spec: kernel.Spec{
				ExecutionID: "e3",
				Contract:    kernel.Contract{Kind: kernel.ContractAnswer},
				Program:     kernel.Program{{ID: "x", Capability: "html.generate"}},
			},
			want: kernel.FailureInvalidSpec,
		},
		{
			name: "mutation under a contract that forbids it",
			spec: kernel.Spec{
				ExecutionID: "e4",
				Contract:    kernel.Contract{Kind: kernel.ContractObserve, Targets: []string{"a.txt"}, RequiresObservation: true},
				Program:     kernel.Program{{ID: "w", Capability: kernel.FileWrite, Target: "a.txt", Args: map[string]string{"content": "x"}}},
			},
			want: kernel.FailureInvalidSpec,
		},
		{
			name: "mutation contract with no mutating step",
			spec: kernel.Spec{
				ExecutionID: "e5",
				Contract:    kernel.Contract{Kind: kernel.ContractCreate, Targets: []string{"a.txt"}},
				Program:     kernel.Program{{ID: "r", Capability: kernel.FileRead, Target: "a.txt"}},
			},
			want: kernel.FailureInvalidSpec,
		},
		{
			name: "mutation contract with no targets",
			spec: kernel.Spec{
				ExecutionID: "e6",
				Contract:    kernel.Contract{Kind: kernel.ContractCreate},
				Program:     kernel.Program{{ID: "w", Capability: kernel.FileWrite, Target: "a.txt", Args: map[string]string{"content": "x"}}},
			},
			want: kernel.FailureInvalidSpec,
		},
		{
			name: "step targets something the contract does not name",
			spec: kernel.Spec{
				ExecutionID: "e7",
				Contract:    kernel.Contract{Kind: kernel.ContractPatch, Targets: []string{"a.txt"}},
				Program: kernel.Program{
					{ID: "w", Capability: kernel.FileWrite, Target: "b.txt", Args: map[string]string{"content": "x"}},
				},
			},
			want: kernel.FailureInvalidSpec,
		},
		{
			name: "observation required but no observing step",
			spec: kernel.Spec{
				ExecutionID: "e8",
				Contract:    kernel.Contract{Kind: kernel.ContractObserve, Targets: []string{"a.txt"}, RequiresObservation: true},
				Program:     kernel.Program{{ID: "a", Capability: kernel.FileWrite, Target: "a.txt", Args: map[string]string{"content": "x"}}},
			},
			want: kernel.FailureInvalidSpec,
		},
		{
			name: "contract kind outside the vocabulary",
			spec: kernel.Spec{
				ExecutionID: "e9",
				Contract:    kernel.Contract{Kind: "REBUILD"},
				Program:     kernel.Program{{ID: "x", Capability: kernel.CommandRun}},
			},
			want: kernel.FailureInvalidSpec,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Register the whole vocabulary so each row is refused for the
			// structural reason it tests, not incidentally for a missing
			// capability. Otherwise this table would silently pass for the wrong
			// reason, which is worse than not testing at all.
			caps := []kernel.Capability{
				&stubCapability{id: kernel.FileRead},
				&stubCapability{id: kernel.FileWrite},
				&stubCapability{id: kernel.CommandRun},
				&stubCapability{id: kernel.WorkspaceDiscover},
			}
			registry, regErr := kernel.NewRegistry(caps...)
			if regErr != nil {
				t.Fatalf("registry: %v", regErr)
			}
			engine, engErr := kernel.NewEngine(registry, kernel.WithVerifier(alwaysPass))
			if engErr != nil {
				t.Fatalf("engine: %v", engErr)
			}

			err := engine.Open(tc.spec, fullGrant(t, "g1", "a.txt", "b.txt"))
			if err == nil {
				t.Fatalf("Open accepted an invalid spec; want class %q", tc.want)
			}
			if got := kernel.ClassOf(err); got != tc.want {
				t.Fatalf("refusal class = %q, want %q (reason: %v)", got, tc.want, err)
			}
			for _, c := range caps {
				if c.(*stubCapability).calls != 0 {
					t.Fatalf("a capability was invoked during admission; " +
						"admission must dispatch nothing")
				}
			}
		})
	}
}

// TestDomainNeutralVocabulary proves the kernel carries no domain vocabulary.
//
// A runtime that names an artifact format is a runtime that has been told what to
// build, and its authority model then answers questions its caller never asked.
// The closed capability and evidence vocabularies are the enforcement mechanism.
func TestDomainNeutralVocabulary(t *testing.T) {
	domainTerms := []string{"html", "css", "javascript", "typescript", "react",
		"index.html", "styles.css", "script.js", "website", "portfolio", "react-app"}

	for _, id := range kernel.AllCapabilityIDs() {
		lower := strings.ToLower(string(id))
		for _, term := range domainTerms {
			if strings.Contains(lower, term) {
				t.Errorf("capability %q contains the domain term %q; the kernel must stay domain-agnostic", id, term)
			}
		}
	}
	for _, kind := range kernel.AllEvidenceKinds() {
		lower := strings.ToLower(string(kind))
		for _, term := range domainTerms {
			if strings.Contains(lower, term) {
				t.Errorf("evidence kind %q contains the domain term %q", kind, term)
			}
		}
	}
	for _, class := range kernel.AllFailureClasses() {
		lower := strings.ToLower(string(class))
		for _, term := range domainTerms {
			if strings.Contains(lower, term) {
				t.Errorf("failure class %q contains the domain term %q", class, term)
			}
		}
	}
	for _, outcome := range kernel.AllOutcomes() {
		lower := strings.ToLower(string(outcome))
		for _, term := range domainTerms {
			if strings.Contains(lower, term) {
				t.Errorf("outcome %q contains the domain term %q", outcome, term)
			}
		}
	}
}

// TestTaxonomiesAreClosed proves the vocabularies are actually closed, which is
// what makes a terminal result attributable to a named cause.
func TestTaxonomiesAreClosed(t *testing.T) {
	if kernel.FailureClass("SOMETHING_ELSE").Valid() {
		t.Error("an unlisted failure class reported itself valid")
	}
	if kernel.CapabilityID("html.generate").Valid() {
		t.Error("an unlisted capability reported itself valid")
	}
	if kernel.EvidenceKind("SOMETHING_ELSE").Valid() {
		t.Error("an unlisted evidence kind reported itself valid")
	}
	if kernel.Verdict("MAYBE").Valid() {
		t.Error("an unlisted verdict reported itself valid")
	}
	if kernel.ContractKind("REBUILD").Valid() {
		t.Error("an unlisted contract kind reported itself valid")
	}
	if kernel.Outcome("MAYBE").Terminal() {
		t.Error("an unlisted outcome reported itself terminal")
	}

	// Every member of each closed set must round-trip its own validity.
	for _, c := range kernel.AllFailureClasses() {
		if !c.Valid() {
			t.Errorf("failure class %q is listed but reports itself invalid", c)
		}
	}
	for _, id := range kernel.AllCapabilityIDs() {
		if !id.Valid() {
			t.Errorf("capability %q is listed but reports itself invalid", id)
		}
	}
}

// TestDeterministicClassification proves kernel.ClassOf reads a typed class out
// of a wrapped error, so a typed failure is never reported as a generic one.
func TestDeterministicClassification(t *testing.T) {
	original := kernel.Block{
		Class:      kernel.FailureVerification,
		Step:       "verify",
		Capability: kernel.CommandRun,
		Reason:     "verifier said no",
	}
	wrapped := errors.New("outer: " + original.Error())
	if kernel.ClassOf(wrapped) != "" {
		t.Error("a plain error was assigned a failure class; " +
			"an untyped error must not be given a fabricated cause")
	}
	if got := kernel.ClassOf(original); got != kernel.FailureVerification {
		t.Errorf("ClassOf(block) = %q, want %q", got, kernel.FailureVerification)
	}
	if kernel.ClassOf(nil) != "" {
		t.Error("ClassOf(nil) returned a class")
	}
}

// TestEngineRequiresRegistry proves an engine with no capability surface is
// refused at construction rather than failing every step at runtime.
func TestEngineRequiresRegistry(t *testing.T) {
	if _, err := kernel.NewEngine(nil); err == nil {
		t.Fatal("NewEngine accepted a nil registry")
	}
}

// TestDuplicateCapabilityRegistrationIsRefused proves the capability surface is
// undecidable-proof: two implementations cannot claim one identity.
func TestDuplicateCapabilityRegistrationIsRefused(t *testing.T) {
	a := &stubCapability{id: kernel.FileRead}
	b := &stubCapability{id: kernel.FileRead}
	_, err := kernel.NewRegistry(a, b)
	if err == nil {
		t.Fatal("NewRegistry accepted two implementations claiming one capability identity")
	}
	if got := kernel.ClassOf(err); got != kernel.FailureCapabilityUnavailable {
		t.Errorf("class = %q, want %q", got, kernel.FailureCapabilityUnavailable)
	}
}

// TestRegistryRefusesOutOfVocabularyCapability proves a capability outside the
// closed vocabulary cannot be installed at all.
func TestRegistryRefusesOutOfVocabularyCapability(t *testing.T) {
	_, err := kernel.NewRegistry(&stubCapability{id: "html.generate"})
	if err == nil {
		t.Fatal("NewRegistry accepted a capability outside the closed vocabulary")
	}
}

// TestNilEngineIsSafe proves the zero value degrades rather than panics, because
// a consumer wiring a runtime incrementally must not crash the process.
func TestNilEngineIsSafe(t *testing.T) {
	var engine *kernel.Engine
	if engine.Registry() != nil {
		t.Error("nil engine returned a registry")
	}
	result := engine.Run(context.Background())
	if result.Proves() {
		t.Error("a nil engine reported PROVEN")
	}
	engine.Cancel()
}

// TestVerifierChainDoesNotMaskWithNotApplicable proves a provably unnecessary
// check cannot hide a later check that would have run.
func TestVerifierChainDoesNotMaskWithNotApplicable(t *testing.T) {
	skipped := kernel.VerifierFunc(func(context.Context, kernel.VerificationRequest) (kernel.Verdict, error) {
		return kernel.VerdictNotApplicable, nil
	})
	decisive := kernel.VerifierFunc(func(context.Context, kernel.VerificationRequest) (kernel.Verdict, error) {
		return kernel.VerdictFail, nil
	})
	chain, err := kernel.NewVerifierChain(skipped, decisive)
	if err != nil {
		t.Fatalf("build chain: %v", err)
	}
	verdict, err := chain.Verify(context.Background(), kernel.VerificationRequest{})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verdict != kernel.VerdictFail {
		t.Fatalf("verdict = %q, want %q: a skipped check must not mask a decisive one", verdict, kernel.VerdictFail)
	}

	allSkipped, err := kernel.NewVerifierChain(skipped)
	if err != nil {
		t.Fatalf("build chain: %v", err)
	}
	verdict, err = allSkipped.Verify(context.Background(), kernel.VerificationRequest{})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verdict != kernel.VerdictNotApplicable {
		t.Errorf("verdict = %q, want %q: a chain that judged nothing must say so", verdict, kernel.VerdictNotApplicable)
	}
}

// TestInvalidVerifierVerdictBecomesUnknown proves an out-of-vocabulary verdict is
// reported as unknown rather than coerced into a pass.
func TestInvalidVerifierVerdictBecomesUnknown(t *testing.T) {
	bad := kernel.EvidenceVerifier{Judge: func(kernel.VerificationRequest) kernel.Verdict {
		return kernel.Verdict("PROBABLY")
	}}
	verdict, err := bad.Verify(context.Background(), kernel.VerificationRequest{})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verdict != kernel.VerdictUnknown {
		t.Fatalf("verdict = %q, want %q", verdict, kernel.VerdictUnknown)
	}
	if verdict.Succeeded() {
		t.Error("an undetermined verdict reported itself as a success")
	}
}

// TestAuthorizerCanRefuseBeyondTheGrant proves the policy seam can narrow
// authority further than the grant already does.
func TestAuthorizerCanRefuseBeyondTheGrant(t *testing.T) {
	write := mutating("notes.md", "x")
	registry, err := kernel.NewRegistry(write)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	engine, err := kernel.NewEngine(registry, kernel.WithAuthorizer(kernel.AuthorizerFunc(
		func(req kernel.Request, _ kernel.Grant) error {
			return kernel.Block{
				Class:      kernel.FailureAuthorization,
				Step:       req.Step,
				Capability: req.Capability,
				Reason:     "policy forbids writing to markdown",
			}
		})))
	if err != nil {
		t.Fatalf("engine: %v", err)
	}

	spec := kernel.Spec{
		ExecutionID: "exec-policy-1",
		Contract: kernel.Contract{
			Kind:                 kernel.ContractCreate,
			Targets:              []string{"notes.md"},
			RequiresVerification: true,
		},
		Program: kernel.Program{{
			ID:         "write",
			Capability: kernel.FileWrite,
			Target:     "notes.md",
			Args:       map[string]string{"content": "x"},
		}},
	}
	if err := engine.Open(spec, fullGrant(t, "g1", "notes.md")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	result := engine.Run(context.Background())

	if result.Outcome != kernel.OutcomeRequiresAuthorization {
		t.Fatalf("outcome = %q, want %q", result.Outcome, kernel.OutcomeRequiresAuthorization)
	}
	if write.calls != 0 {
		t.Fatalf("a policy-refused step still invoked the capability %d times", write.calls)
	}
}

// TestCapabilityOwnGateCanRefuse proves the capability-local precondition is
// checked after the grant, not instead of it.
func TestCapabilityOwnGateCanRefuse(t *testing.T) {
	gated := &stubCapability{
		id: kernel.FileWrite,
		authorizeErr: kernel.Block{
			Class:      kernel.FailureAuthorization,
			Capability: kernel.FileWrite,
			Reason:     "target is locked by another process",
		},
	}
	engine := newTestEngine(t, alwaysPass, gated)

	spec := kernel.Spec{
		ExecutionID: "exec-gate-1",
		Contract: kernel.Contract{
			Kind:                 kernel.ContractCreate,
			Targets:              []string{"notes.md"},
			RequiresVerification: true,
		},
		Program: kernel.Program{{
			ID:         "write",
			Capability: kernel.FileWrite,
			Target:     "notes.md",
			Args:       map[string]string{"content": "x"},
		}},
	}
	if err := engine.Open(spec, fullGrant(t, "g1", "notes.md")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	result := engine.Run(context.Background())

	if result.Outcome != kernel.OutcomeRequiresAuthorization {
		t.Fatalf("outcome = %q, want %q", result.Outcome, kernel.OutcomeRequiresAuthorization)
	}
	if gated.calls != 0 {
		t.Fatalf("the capability was invoked %d times despite its own gate refusing", gated.calls)
	}
}

// TestEvidenceLogReadModel proves the consumer-facing projection answers the
// questions a consumer asks, in canonical order.
func TestEvidenceLogReadModel(t *testing.T) {
	write := mutating("notes.md", "hello")
	engine := newTestEngine(t, alwaysPass, write)
	spec := kernel.Spec{
		ExecutionID: "exec-readmodel",
		Contract: kernel.Contract{
			Kind:                 kernel.ContractCreate,
			Targets:              []string{"notes.md"},
			RequiresVerification: true,
		},
		Program: kernel.Program{{
			ID:         "write",
			Capability: kernel.FileWrite,
			Target:     "notes.md",
			Args:       map[string]string{"content": "hello"},
		}},
	}
	if err := engine.Open(spec, fullGrant(t, "g1", "notes.md")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	result := engine.Run(context.Background())

	log := kernel.NewEvidenceLog(result.Evidence)
	if !log.Has(kernel.EvidenceFileWritten) {
		t.Error("the read model does not report write evidence for an execution that wrote")
	}
	if !log.Has(kernel.EvidenceFilePresent) {
		t.Error("the read model does not report presence evidence")
	}
	kinds := log.Kinds()
	if len(kinds) == 0 {
		t.Fatal("the read model reports no evidence kinds")
	}
	if len(log.For(kernel.EvidenceFileWritten)) != 1 {
		t.Errorf("write evidence count = %d, want 1", len(log.For(kernel.EvidenceFileWritten)))
	}
	// Mutating the returned slice must not corrupt the model.
	records := log.For(kernel.EvidenceFileWritten)
	records[0].Target = "tampered"
	if log.For(kernel.EvidenceFileWritten)[0].Target == "tampered" {
		t.Error("the read model returned an aliased slice; a consumer could corrupt it")
	}
}

// TestStateCopyIsIndependent proves a consumer polling State cannot observe or
// cause a torn view.
func TestStateCopyIsIndependent(t *testing.T) {
	engine := newTestEngine(t, alwaysPass, observing("notes.md"))
	spec := kernel.Spec{
		ExecutionID: "exec-copy-1",
		Contract:    kernel.Contract{Kind: kernel.ContractObserve, Targets: []string{"notes.md"}, RequiresObservation: true},
		Program:     kernel.Program{{ID: "stat", Capability: kernel.FileExists, Target: "notes.md"}},
	}
	if err := engine.Open(spec, fullGrant(t, "g1", "notes.md")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	engine.Run(context.Background())

	snapshot := engine.State()
	before := len(snapshot.Evidence)
	if before == 0 {
		t.Fatal("the settled state carries no evidence")
	}
	// Mutating the copy must not affect the engine's state.
	snapshot.Evidence[0].Target = "tampered"
	if engine.State().Evidence[0].Target == "tampered" {
		t.Error("State() returned an aliased evidence slice")
	}
	if len(engine.State().Evidence) != before {
		t.Errorf("evidence count changed from %d to %d after mutating a copy", before, len(engine.State().Evidence))
	}
}

// TestLogSubscriberCannotBreakExecution proves a broken projection cannot corrupt
// runtime truth. A UI that panics must not be able to fail an execution.
func TestLogSubscriberCannotBreakExecution(t *testing.T) {
	engine := newTestEngine(t, alwaysPass, observing("notes.md"))

	var seen int
	engine.Log().Subscribe(func(kernel.Event) {
		seen++
		panic("projection exploded")
	})

	spec := kernel.Spec{
		ExecutionID: "exec-sub-1",
		Contract:    kernel.Contract{Kind: kernel.ContractObserve, Targets: []string{"notes.md"}, RequiresObservation: true},
		Program:     kernel.Program{{ID: "stat", Capability: kernel.FileExists, Target: "notes.md"}},
	}
	if err := engine.Open(spec, fullGrant(t, "g1", "notes.md")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	result := engine.Run(context.Background())

	if !result.Proves() {
		t.Fatalf("a panicking subscriber broke the execution\noutcome=%s reason=%s", result.Outcome, result.Reason)
	}
	if seen == 0 {
		t.Error("the subscriber was never called")
	}
}

// TestUnsubscribeStopsDelivery proves a consumer that goes away does not leak.
func TestUnsubscribeStopsDelivery(t *testing.T) {
	log := kernel.NewEventLog()
	var count int
	stop := log.Subscribe(func(kernel.Event) { count++ })

	log.Append(kernel.Event{Kind: kernel.EventExecutionStarted, ExecutionID: "x"})
	if count != 1 {
		t.Fatalf("subscriber called %d times, want 1", count)
	}
	stop()
	stop() // idempotent
	log.Append(kernel.Event{Kind: kernel.EventStepBlocked, ExecutionID: "x"})
	if count != 1 {
		t.Fatalf("subscriber called %d times after unsubscribing, want 1", count)
	}
	if log.Len() != 2 {
		t.Errorf("log length = %d, want 2: unsubscribing must not truncate the record", log.Len())
	}
}

// TestRunWithoutOpenIsUnsubstantiated proves an engine with no admitted
// execution reports ignorance rather than success.
func TestRunWithoutOpenIsUnsubstantiated(t *testing.T) {
	engine := newTestEngine(t, alwaysPass, passing(kernel.FileRead))
	result := engine.Run(context.Background())
	if result.Proves() {
		t.Error("an engine with no admitted execution reported PROVEN")
	}
	if result.Outcome != kernel.OutcomeUnsubstantiated {
		t.Errorf("outcome = %q, want %q", result.Outcome, kernel.OutcomeUnsubstantiated)
	}
}

// TestOutputBudgetBreachIsNotSuccess proves the truncation rule of the budget
// contract.
//
// A capability that returns more output than the declared bound is the mechanical
// shape of a provider that hit its token ceiling. The kernel cannot prevent the
// overshoot — the size of an output is unknowable until it arrives — so its only
// honest move is to refuse the result rather than admit a prefix as if it were
// whole.
func TestOutputBudgetBreachIsNotSuccess(t *testing.T) {
	verbose := &stubCapability{
		id: kernel.FileRead,
		obs: kernel.Observation{
			Verdict:     kernel.VerdictPass,
			OutputBytes: 5_000,
			Detail:      "a very long answer, cut off mid-sentence",
			Facts: []kernel.Fact{{
				Kind:  kernel.EvidenceResponseProduced,
				Bytes: 5_000,
			}},
		},
	}
	engine := newTestEngine(t, alwaysPass, verbose)

	spec := kernel.Spec{
		ExecutionID: "exec-output-budget",
		Contract:    kernel.Contract{Kind: kernel.ContractAnswer},
		Program:     kernel.Program{{ID: "answer", Capability: kernel.FileRead}},
		Budget:      kernel.Budget{MaxOutputBytes: 1_000},
	}
	if err := engine.Open(spec, fullGrant(t, "g1")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	result := engine.Run(context.Background())

	if result.Proves() {
		t.Fatal("an invocation that breached its output budget was reported PROVEN; " +
			"truncated output must never read as a complete answer")
	}
	if result.Outcome != kernel.OutcomeBudgetExhausted {
		t.Fatalf("outcome = %q, want %q", result.Outcome, kernel.OutcomeBudgetExhausted)
	}
	if result.State.Artifact == kernel.ArtifactProduced {
		t.Error("the artifact axis was promoted despite an output-budget breach; " +
			"an incomplete prefix is not a produced artifact")
	}
	if !mentions(result.Reason, "incomplete prefix") {
		t.Errorf("reason %q does not explain that the data was truncated", result.Reason)
	}
}

// mentions reports whether text contains any of the given fragments. It is a small
// local helper so the assertion reads as prose rather than as string plumbing.
func mentions(text string, fragments ...string) bool {
	for _, f := range fragments {
		if strings.Contains(text, f) {
			return true
		}
	}
	return false
}
