package kernel

import (
	"fmt"
	"strings"
)

// Outcome is the closed, total vocabulary of terminal verdicts. Every
// completion claim reduces to exactly one of these, and exactly one of them
// authorizes the word "completed".
//
// The vocabulary exists to make the distinction between "we could not prove it"
// and "we proved it is false" impossible to lose. Collapsing them into
// FAILED is what makes a runtime untrustworthy: it reports a bug where it should
// have reported ignorance.
type Outcome string

const (
	// OutcomeProven: the evidence satisfies the contract. This is the ONLY
	// outcome that may authorize a completed transition, and reaching it
	// requires passing every applicable gate in adjudicate.
	OutcomeProven Outcome = "PROVEN"
	// OutcomeUnsubstantiated: the execution ended without evidence that
	// satisfies the contract. The claim is unsubstantiated — not false, simply
	// unproven. No completed transition is permitted.
	OutcomeUnsubstantiated Outcome = "UNSUBSTANTIATED"
	// OutcomeFailed: the evidence positively contradicts the contract: a write
	// was attempted and did not land, or verification ran and failed.
	OutcomeFailed Outcome = "FAILED"
	// OutcomeRequiresAuthorization: the evidence is complete but a boundary
	// refused to act. The decision belongs to a human, not to the runtime.
	OutcomeRequiresAuthorization Outcome = "REQUIRES_AUTHORIZATION"
	// OutcomeBudgetExhausted: an explicit bound was reached before the contract
	// could be satisfied. Reported as its own outcome so truncation is never
	// rendered as success.
	OutcomeBudgetExhausted Outcome = "BUDGET_EXHAUSTED"
	// OutcomeCancelled: the caller withdrew the execution deliberately.
	OutcomeCancelled Outcome = "CANCELLED"
	// OutcomeInterrupted: the execution stopped for a reason outside the kernel's
	// control. It did not choose its own ending.
	OutcomeInterrupted Outcome = "INTERRUPTED"
)

// Proves reports whether this is the only outcome that authorizes completion.
// Read this before every completed transition.
func (o Outcome) Proves() bool { return o == OutcomeProven }

// Terminal reports whether the outcome settles the claim. All four original
// verdicts do, as do the bound-driven and interruption-driven ones.
func (o Outcome) Terminal() bool {
	switch o {
	case OutcomeProven, OutcomeUnsubstantiated, OutcomeFailed,
		OutcomeRequiresAuthorization, OutcomeBudgetExhausted,
		OutcomeCancelled, OutcomeInterrupted:
		return true
	default:
		return false
	}
}

// String returns the raw outcome label.
func (o Outcome) String() string { return string(o) }

// allOutcomes is the canonical ordered vocabulary.
var allOutcomes = []Outcome{
	OutcomeProven,
	OutcomeUnsubstantiated,
	OutcomeFailed,
	OutcomeRequiresAuthorization,
	OutcomeBudgetExhausted,
	OutcomeCancelled,
	OutcomeInterrupted,
}

// AllOutcomes returns the canonical ordered outcome vocabulary.
func AllOutcomes() []Outcome {
	out := make([]Outcome, len(allOutcomes))
	copy(out, allOutcomes)
	return out
}

// Adjudication is the kernel's decision about whether an execution's evidence
// satisfies its contract, together with the reasoning that produced it.
//
// The reasoning is part of the value, not a diagnostic afterthought. A PROVEN
// verdict that cannot explain which evidence satisfied which clause is an
// assertion; one that can is a claim a reader can check.
type Adjudication struct {
	// Outcome is the evidence-gated verdict.
	Outcome Outcome
	// Unmet lists the contract clauses the evidence did not satisfy, in
	// canonical contract order.
	Unmet []string
	// Satisfied lists the contract clauses the evidence did satisfy, in
	// canonical contract order.
	Satisfied []string
	// Reason is the one-line deterministic explanation of the verdict.
	Reason string
}

// clause is one obligation the contract carries. Obligations are named, not
// implied, so a PROVEN verdict can be decomposed back into the specific facts
// that justified it.
type clause struct {
	name       string
	met        func(s State) bool
	refute     func(s State) bool
	whyUnmet   string
	whyRefuted string
}

// adjudicate is the pure function that decides terminal truth from evidence.
//
// It is where the acceptance invariant is actually enforced, so it is written
// to be read rather than to be clever. The structure is:
//
//  1. Positively contradicted evidence wins. If the filesystem axis or the
//     verification axis positively says the objective failed, the outcome is
//     FAILED regardless of anything else. A rollback is not progress.
//  2. Every applicable obligation must be met. Each unmet clause is named.
//  3. Only when nothing is refuted and nothing is unmet is the outcome PROVEN.
//
// There is no default, no fallback, and no "assume the rest". A clause the
// evidence cannot speak to is unmet, which is what makes an incomplete execution
// UNSUBSTANTIATED rather than accidentally PROVEN.
func adjudicate(s State) Adjudication {
	// ── Refutations: evidence that positively contradicts the contract ──
	if s.Mutation == MutationRolledBack {
		return Adjudication{
			Outcome: OutcomeFailed,
			Reason:  "filesystem boundary holds a rolled-back mutation, so nothing this execution wrote is durable",
		}
	}
	if s.Verify == VerifyFailed {
		return Adjudication{
			Outcome: OutcomeFailed,
			Reason:  "verification ran and did not pass, so the objective's obligations are not met",
		}
	}

	// ── Obligations ──
	clauses := contractClauses(s)
	var unmet, satisfied []string
	for _, c := range clauses {
		switch {
		case c.met != nil && c.met(s):
			satisfied = append(satisfied, c.name)
		case c.refute != nil && c.refute(s):
			// Refuted at clause level: the outcome is FAILED, not UNSUBSTANTIATED,
			// because something was positively observed to be wrong.
			return Adjudication{
				Outcome: OutcomeFailed,
				Unmet:   append(unmet, c.name),
				Reason:  clauseRefutation(c),
			}
		default:
			unmet = append(unmet, c.name)
		}
	}

	if len(unmet) > 0 {
		return Adjudication{
			Outcome:   OutcomeUnsubstantiated,
			Unmet:     unmet,
			Satisfied: satisfied,
			Reason:    "evidence does not satisfy contract clauses: " + strings.Join(unmet, ", "),
		}
	}
	return Adjudication{
		Outcome:   OutcomeProven,
		Satisfied: satisfied,
		Reason:    "evidence satisfies every contract clause: " + strings.Join(satisfied, ", "),
	}
}

// clauseRefutation returns the reason a clause positively failed.
func clauseRefutation(c clause) string {
	if c.whyRefuted != "" {
		return c.whyRefuted
	}
	return "contract clause " + c.name + " was positively contradicted by the evidence"
}

// contractClauses derives the obligation set for a state's contract from
// evidence. It is a pure function of the state, so the same state always yields
// the same obligations and therefore the same verdict.
func contractClauses(s State) []clause {
	c := s.Spec.Contract
	var clauses []clause

	// ANSWER's obligation is a delivered response. OBSERVE's is not: reporting
	// what a workspace contains does not require prose, and making it demand one
	// would collapse two genuinely different contracts into a single shape. Each
	// contract carries only the evidence obligation it actually has.
	if c.Kind == ContractAnswer {
		clauses = append(clauses, clause{
			name:     "response_produced",
			met:      func(st State) bool { return st.evidenceSet().has(EvidenceResponseProduced) },
			whyUnmet: "no delivered response was observed, so the execution answered nothing",
		})
	}

	// A contract that requires an observation needs a real one. This is the
	// clause that makes "the model said it looked" insufficient.
	if c.RequiresObservation {
		clauses = append(clauses, clause{
			name:     "workspace_observed",
			met:      func(st State) bool { return st.evidenceSet().has(EvidenceWorkspaceObserved) },
			whyUnmet: "no workspace observation was recorded, so nothing proves the runtime looked",
		})
	}

	switch c.Kind {
	case ContractObserve:
		// Every declared target must have been the subject of some observation.
		// A workspace-wide sweep does not satisfy a per-target obligation: "the
		// workspace has files" is not "this file was inspected".
		clauses = append(clauses, clause{
			name: "targets_observed",
			met: func(st State) bool {
				set := st.evidenceSet()
				for _, t := range c.Targets {
					if !set.observedAnywhere(t, EvidenceFilePresent, EvidenceFileRead, EvidenceFileAbsent, EvidenceWorkspaceObserved) {
						return false
					}
				}
				return len(c.Targets) > 0
			},
			whyUnmet: "not every declared target was the subject of a recorded observation",
		})

	case ContractCreate:
		clauses = append(clauses,
			clause{
				name: "target_written",
				met: func(st State) bool {
					set := st.evidenceSet()
					for _, t := range c.Targets {
						if !set.observed(EvidenceFileWritten, t) {
							return false
						}
					}
					return len(c.Targets) > 0
				},
				whyUnmet: "not every declared target carries write evidence",
			},
			clause{
				name: "target_exists",
				met: func(st State) bool {
					present := st.evidenceSet().presentTargets()
					return c.targetsAre(present)
				},
				whyUnmet: "not every declared target was observed to exist after execution",
			})

	case ContractPatch:
		clauses = append(clauses, clause{
			name: "target_delta_applied",
			met: func(st State) bool {
				set := st.evidenceSet()
				for _, t := range c.Targets {
					if !set.observed(EvidenceFileWritten, t) {
						return false
					}
				}
				return len(c.Targets) > 0
			},
			whyUnmet: "not every declared target carries an applied filesystem delta",
		})

	case ContractDelete:
		clauses = append(clauses,
			clause{
				name:     "mutation_applied",
				met:      func(st State) bool { return st.Mutation.Durable() },
				whyUnmet: "no durable mutation was recorded, so nothing was removed",
			},
			clause{
				name: "target_absent",
				met: func(st State) bool {
					absent := st.evidenceSet().absentTargets()
					return c.targetsAre(absent)
				},
				whyUnmet: "not every declared target was observed to be absent after execution",
			})
	}

	// Verification, when required, must be satisfied — and the axis records
	// whether that was a real pass or a provable non-applicability, so no
	// projection can render a skipped gate as a passed one.
	if c.RequiresVerification {
		clauses = append(clauses, clause{
			name:     "verification_satisfied",
			met:      func(st State) bool { return st.Verify.Satisfied() },
			whyUnmet: "verification is required and did not produce a satisfied verdict",
		})
	}

	return clauses
}

// outcomeForClass maps a blocking failure class onto the terminal outcome it
// implies. Keeping this mapping in one place is what stops a failure from being
// reported as a different kind of stop depending on which code path noticed it.
func outcomeForClass(class FailureClass) Outcome {
	switch class {
	case FailureAuthorization:
		return OutcomeRequiresAuthorization
	case FailureBudgetExhausted:
		return OutcomeBudgetExhausted
	case FailureCancelled:
		return OutcomeCancelled
	case FailureInterrupted:
		return OutcomeInterrupted
	case "":
		// No failure class and no evidence: the claim is simply unsubstantiated.
		return OutcomeUnsubstantiated
	default:
		// Every other class — invalid spec, missing capability, capability,
		// provider, execution, artifact, mutation, verification — is a positive
		// failure: something was attempted and it did not succeed.
		return OutcomeFailed
	}
}

// String renders a terminal verdict for a human reader.
func (t Terminal) String() string {
	var sb strings.Builder
	sb.WriteString(string(t.Outcome))
	if t.Class != "" {
		fmt.Fprintf(&sb, " (%s)", t.Class)
	}
	if t.Reason != "" {
		sb.WriteString(": ")
		sb.WriteString(t.Reason)
	}
	return sb.String()
}
