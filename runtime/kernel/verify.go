package kernel

import "context"

// VerificationRequest is the input to a verifier.
//
// It carries evidence and the contract those evidence must satisfy. It does not
// carry a step, a capability, a provider, or a view of the engine: a verifier
// judges facts, and nothing that could bias it toward a particular conclusion is
// reachable from here.
type VerificationRequest struct {
	// ExecutionID identifies the execution being verified.
	ExecutionID string
	// Contract is the obligation set the verdict is judged against.
	Contract Contract
	// Targets are the contract's declared destinations.
	Targets []string
	// Evidence is the observation record available to this verification. It is
	// the ONLY source the verifier may reason from, which is what makes its
	// verdict evidence-backed rather than self-reported.
	Evidence []Evidence
}

// Verifier is the kernel's verification seam.
//
// Verification is separate from execution on purpose. A verifier runs after the
// fact, judges against the contract, and has no way to change the workspace. That
// asymmetry is what gives its verdict meaning: it cannot be the same code that
// produced the thing being judged.
//
// A verifier MUST NOT mutate the workspace and MUST NOT report VerdictPass
// without having examined the supplied evidence. The kernel cannot enforce that
// mechanically, and says so plainly rather than pretending otherwise.
type Verifier interface {
	// Verify judges the request and returns a truthful verdict.
	Verify(ctx context.Context, req VerificationRequest) (Verdict, error)
}

// VerifierFunc adapts a function to Verifier.
type VerifierFunc func(ctx context.Context, req VerificationRequest) (Verdict, error)

// Verify implements Verifier.
func (f VerifierFunc) Verify(ctx context.Context, req VerificationRequest) (Verdict, error) {
	if f == nil {
		return VerdictUnknown, nil
	}
	return f(ctx, req)
}

// Compile-time proof the adapter satisfies the boundary.
var _ Verifier = VerifierFunc(nil)

// EvidenceVerifier is a verifier that judges the supplied evidence against a
// caller-supplied predicate.
//
// It exists because "did the evidence satisfy the contract" is the question the
// kernel must ask, and in most deployments the predicate is small and entirely
// mechanical. Wrapping it here means a deployment writes one function instead of
// inventing a verifier type, and cannot accidentally reach outside the evidence
// to reach a conclusion.
type EvidenceVerifier struct {
	// Judge reports the verdict for the given request. It receives only evidence
	// and the contract, never the engine or the workspace.
	Judge func(req VerificationRequest) Verdict
}

// Verify implements Verifier.
func (v EvidenceVerifier) Verify(_ context.Context, req VerificationRequest) (Verdict, error) {
	if v.Judge == nil {
		return VerdictUnknown, nil
	}
	verdict := v.Judge(req)
	if !verdict.Valid() {
		// An invalid verdict is reported as UNKNOWN rather than coerced. An
		// undetermined outcome is not a pass.
		return VerdictUnknown, nil
	}
	return verdict, nil
}

// Compile-time proof the adapter satisfies the boundary.
var _ Verifier = EvidenceVerifier{}

// VerifierChain runs verifiers in order and returns the first verdict that
// actually judges something.
//
// A NotApplicable verdict does not short-circuit the chain: a check that proved
// unnecessary must not mask a later check that would have run. A chain that
// reaches the end having judged nothing returns NotApplicable, which is
// distinguishable from a pass by construction.
type VerifierChain struct {
	verifiers []Verifier
}

// NewVerifierChain builds a chain, refusing nil members rather than silently
// skipping them.
func NewVerifierChain(verifiers ...Verifier) (*VerifierChain, error) {
	kept := make([]Verifier, 0, len(verifiers))
	for _, v := range verifiers {
		if v == nil {
			return nil, blockf(FailureVerification, "", "", "verifier chain received a nil verifier")
		}
		kept = append(kept, v)
	}
	return &VerifierChain{verifiers: kept}, nil
}

// Verify implements Verifier.
func (c *VerifierChain) Verify(ctx context.Context, req VerificationRequest) (Verdict, error) {
	if c == nil {
		return VerdictNotApplicable, nil
	}
	for _, v := range c.verifiers {
		verdict, err := v.Verify(ctx, req)
		if err != nil {
			return VerdictUnknown, err
		}
		if verdict == VerdictNotApplicable {
			continue
		}
		return verdict, nil
	}
	return VerdictNotApplicable, nil
}

// Compile-time proof the adapter satisfies the boundary.
var _ Verifier = (*VerifierChain)(nil)
