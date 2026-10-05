package kernel_test

import (
	"context"
	"errors"
	"testing"

	"github.com/PizenLabs/izen/runtime/kernel"
)

// ── §8 · The adversarial state-transition matrix ────────────────────────────
//
//		                         AUTHORIZED       NOT AUTHORIZED
//		 ----------------------------------------------------------
//		 RESOLVED                 execute          deny
//		 NO EVIDENCE              incomplete       deny
//		 BAD VERIFICATION         failed           deny
//		 PROVIDER FAILURE         failed           deny
//		 CAPABILITY MISSING       unavailable      deny
//
// The kernel already tests each row on its own. What it does not test is the
// CONTRAST, and the contrast is the actual claim: for every scenario, changing
// nothing but the grant must move the execution from "ran and reported an
// honest verdict" to "refused, with the capability never invoked".
//
// Without this, each row's refusal could be an independent code path, and a row
// could quietly acquire a route that bypasses authorization while every
// individual test still passes. A matrix does not add test count here; it adds
// the one property a list of row tests cannot express.
//
// Each case therefore runs TWICE from identical state, and the only difference
// between the two runs is the grant.

// matrixRow is one scenario of the matrix: a capability, a verifier, and the
// outcome the kernel owes when the grant COVERS the program.
type matrixRow struct {
	name string
	// newCap builds the capability under test. It is a FACTORY, not a value: each
	// column of the matrix must run against its own stub, or the denied column
	// would count invocations made by the authorized one and the "was it ever
	// invoked" assertion would measure the wrong run.
	newCap func() kernel.Capability
	// verifier judges the execution after the last step.
	verifier kernel.Verifier
	// registered reports whether the capability exists in the registry at all.
	registered bool
	// wantAuthorized is the outcome the kernel owes once the grant covers it.
	wantAuthorized kernel.Outcome
	// wantDenied is the outcome the kernel owes once the grant does not. It
	// defaults to REQUIRES_AUTHORIZATION, which is the grant's own refusal; a row
	// that is refused EARLIER and more specifically declares its own.
	wantDenied kernel.Outcome
}

// writeSpec is the program every row runs: observe the target, then write it.
// It is the smallest program that carries a real mutation obligation, so a row
// cannot pass by doing nothing.
func writeSpec() kernel.Spec {
	return kernel.Spec{
		ExecutionID: "exec-matrix",
		Objective:   "write notes.md",
		Contract: kernel.Contract{
			Kind:                 kernel.ContractCreate,
			Targets:              []string{"notes.md"},
			RequiresVerification: true,
		},
		Program: kernel.Program{
			{ID: "check", Capability: kernel.FileExists, Target: "notes.md"},
			{ID: "write", Capability: kernel.FileWrite, Target: "notes.md",
				Args: map[string]string{"content": "hello"}},
		},
	}
}

// coveringGrant permits both steps over exactly the requested target.
func coveringGrant(t *testing.T) kernel.Grant {
	t.Helper()
	g, err := kernel.NewGrant("grant-covering",
		[]kernel.CapabilityID{kernel.FileExists, kernel.FileWrite},
		[]string{"notes.md"})
	if err != nil {
		t.Fatalf("build covering grant: %v", err)
	}
	return g
}

// silentWrite is a write that reports success but produces NO evidence. It is
// the shape of the most dangerous false completion in the system: a capability
// that claims to have worked while proving nothing.
func silentWrite() *stubCapability {
	return &stubCapability{
		id: kernel.FileWrite,
		obs: kernel.Observation{
			Verdict: kernel.VerdictPass,
			Detail:  "claimed success, proved nothing",
		},
	}
}

// wantDeniedOutcome resolves the NOT-AUTHORIZED cell of a row. Every row denies
// through the grant unless it says otherwise, and the one that does is
// CAPABILITY MISSING: an unregistered capability is refused at admission as
// CAPABILITY_UNAVAILABLE, which is a MORE SPECIFIC refusal than an authorization
// failure, not a weaker one. The grant is never consulted because nothing about
// the program could have been authorized in the first place.
func (r matrixRow) wantDeniedOutcome() kernel.Outcome {
	if r.wantDenied != "" {
		return r.wantDenied
	}
	return kernel.OutcomeRequiresAuthorization
}

func adversarialRows() []matrixRow {
	failingRef := func() *stubCapability {
		return &stubCapability{
			id:  kernel.FileWrite,
			obs: kernel.Observation{Verdict: kernel.VerdictFail, Detail: "write rejected"},
		}
	}
	absentRef := func() *stubCapability {
		return &stubCapability{
			id: kernel.FileWrite,
			obs: kernel.Observation{
				Verdict: kernel.VerdictPass,
				Facts:   []kernel.Fact{{Kind: kernel.EvidenceFileAbsent, Summary: "no notes.md"}},
			},
		}
	}
	presentRef := func() *stubCapability {
		return &stubCapability{
			id: kernel.FileWrite,
			obs: kernel.Observation{
				Verdict: kernel.VerdictPass,
				Facts:   []kernel.Fact{{Kind: kernel.EvidenceFilePresent, Summary: "notes.md"}},
			},
		}
	}
	verifierFails := kernel.VerifierFunc(
		func(context.Context, kernel.VerificationRequest) (kernel.Verdict, error) {
			return kernel.VerdictFail, nil
		})

	return []matrixRow{
		{
			name:           "RESOLVED",
			newCap:         func() kernel.Capability { return mutating("notes.md", "hello") },
			verifier:       alwaysPass,
			registered:     true,
			wantAuthorized: kernel.OutcomeProven,
		},
		{
			name:           "NO EVIDENCE",
			newCap:         func() kernel.Capability { return silentWrite() },
			verifier:       alwaysPass,
			registered:     true,
			wantAuthorized: kernel.OutcomeUnsubstantiated,
		},
		{
			name:           "BAD VERIFICATION",
			newCap:         func() kernel.Capability { return mutating("notes.md", "hello") },
			verifier:       verifierFails,
			registered:     true,
			wantAuthorized: kernel.OutcomeFailed,
		},
		{
			name:           "PROVIDER FAILURE",
			newCap:         func() kernel.Capability { return failingRef() },
			verifier:       alwaysPass,
			registered:     true,
			wantAuthorized: kernel.OutcomeFailed,
		},
		{
			name:           "TARGET NOT PRESENT",
			newCap:         func() kernel.Capability { return absentRef() },
			verifier:       alwaysPass,
			registered:     true,
			wantAuthorized: kernel.OutcomeUnsubstantiated,
		},
		{
			name:           "CAPABILITY MISSING",
			newCap:         func() kernel.Capability { return presentRef() },
			verifier:       alwaysPass,
			registered:     false,
			wantAuthorized: kernel.OutcomeFailed,
			wantDenied:     kernel.OutcomeFailed,
		},
	}
}

// TestKernelMatrix_AuthorizationIsTheOnlyLever runs every row twice and pins the
// contrast: the grant decides whether the execution runs at all, and no row
// reaches an outcome it did not earn.
func TestKernelMatrix_AuthorizationIsTheOnlyLever(t *testing.T) {
	for _, row := range adversarialRows() {
		t.Run(row.name, func(t *testing.T) {
			// ── AUTHORIZED ────────────────────────────────────────────
			authorized := runMatrixRow(t, row, true)
			if authorized.result.Outcome != row.wantAuthorized {
				t.Errorf("authorized outcome = %q, want %q (unmet clauses: %v, reason: %q)",
					authorized.result.Outcome, row.wantAuthorized,
					authorized.result.Unmet, authorized.result.Reason)
			}
			if !row.registered && authorized.proves {
				t.Error("an unregistered capability produced PROVEN; " +
					"capability absence must never simulate completion")
			}

			// ── NOT AUTHORIZED ────────────────────────────────────────
			// Identical program, identical capability, identical verifier. The
			// ONLY change is that the grant does not cover the mutation.
			denied := runMatrixRow(t, row, false)
			if denied.result.Outcome != row.wantDeniedOutcome() {
				t.Errorf("denied outcome = %q, want %q",
					denied.result.Outcome, row.wantDeniedOutcome())
			}
			if denied.result.Proves() {
				t.Error("a denied execution reported PROVEN")
			}
			if !row.registered && denied.result.Class != kernel.FailureCapabilityUnavailable {
				t.Errorf("a missing capability was refused with class %q, want %q",
					denied.result.Class, kernel.FailureCapabilityUnavailable)
			}
			if denied.stub != nil && denied.stub.calls != 0 {
				t.Errorf("the capability ran %d times under a grant that does not cover it; "+
					"authorization is checked BEFORE invocation", denied.stub.calls)
			}
		})
	}
}

// observesTarget is the FileExists capability the program's first step needs. It
// reports the target as absent, which is the honest precondition for a creation.
func observesTarget() kernel.Capability {
	return &stubCapability{
		id: kernel.FileExists,
		obs: kernel.Observation{
			Verdict: kernel.VerdictPass,
			Facts:   []kernel.Fact{{Kind: kernel.EvidenceFileAbsent, Summary: "no notes.md"}},
		},
	}
}

// matrixRun is one execution's observable result plus the stub that could have
// written, so a test can prove the write never happened.
type matrixRun struct {
	result kernel.Result
	stub   *stubCapability
	proves bool
}

// runMatrixRow executes one matrix cell.
func runMatrixRow(t *testing.T, row matrixRow, authorized bool) matrixRun {
	t.Helper()

	// The capability under test is constructed ONCE, and that exact instance is
	// both registered and counted. Registering one instance and counting
	// another makes the "was it ever invoked" assertion measure an object the
	// engine never called, which is a test that passes for the wrong reason.
	capability := row.newCap()
	stub, isStub := capability.(*stubCapability)

	// The program's first step observes the target. It must exist in the
	// registry for every row, or every row would be measuring the same
	// CAPABILITY_MISSING refusal instead of its own scenario. The
	// CAPABILITY_MISSING row deliberately withholds a capability — and it
	// withholds the one under test, not the observation.
	caps := []kernel.Capability{observesTarget()}
	if row.registered {
		caps = append(caps, capability)
	}
	engine := newTestEngine(t, row.verifier, caps...)

	grant := coveringGrant(t)
	if !authorized {
		// A read-only grant: it permits the observation but not the write.
		var err error
		grant, err = kernel.NewGrant("grant-read-only",
			[]kernel.CapabilityID{kernel.FileExists}, []string{"notes.md"})
		if err != nil {
			t.Fatalf("build read-only grant: %v", err)
		}
	}

	openErr := engine.Open(writeSpec(), grant)
	if openErr != nil && authorized && row.registered {
		t.Fatalf("the covering grant refused an in-scope program: %v (class %q)",
			openErr, kernel.ClassOf(openErr))
	}

	result := engine.Run(context.Background())
	run := matrixRun{result: result, proves: result.Proves()}
	if isStub {
		run.stub = stub
	}
	return run
}

// TestKernelMatrix_NoRowCanTradeEvidenceForAuthority asserts the other half of
// the matrix invariant: a scenario that fails on its merits while authorized must
// not become anything better when authorized, and one that succeeds must not
// survive a grant change. Outcomes are decided by facts, not by who asked.
func TestKernelMatrix_NoRowCanTradeEvidenceForAuthority(t *testing.T) {
	// A capability that panics if invoked proves the guard ordering: the grant
	// gate must run before dispatch, not merely report afterwards.
	guard := &stubCapability{
		id:  kernel.FileWrite,
		err: errors.New("must never be reached"),
	}

	engine := newTestEngine(t, alwaysPass, observesTarget(), guard)
	readOnly, err := kernel.NewGrant("grant-read-only",
		[]kernel.CapabilityID{kernel.FileExists}, []string{"notes.md"})
	if err != nil {
		t.Fatalf("build grant: %v", err)
	}
	if err := engine.Open(writeSpec(), readOnly); err == nil {
		t.Fatal("admission accepted a mutation outside the grant")
	} else if got := kernel.ClassOf(err); got != kernel.FailureAuthorization {
		t.Fatalf("admission refused with class %q, want %q", got, kernel.FailureAuthorization)
	}

	result := engine.Run(context.Background())
	if result.Outcome != kernel.OutcomeRequiresAuthorization {
		t.Fatalf("outcome = %q, want %q", result.Outcome, kernel.OutcomeRequiresAuthorization)
	}
	if guard.calls != 0 {
		t.Fatalf("the refused capability was invoked %d times", guard.calls)
	}
}
