package stepadmission

import (
	"strings"

	"github.com/PizenLabs/izen/internal/mutationstrategy"
)

// CandidateStep is the bounded candidate step presented to admission.
// It is domain-neutral: Kind carries problem.StepKind values
// (INVESTIGATE/ANALYZE/EXPERIMENT/MUTATE/VERIFY/OBSERVE) without
// domain-specific subtypes (no ReactStep/GoStep).
type CandidateStep struct {
	// ID is the stable within-plan identity (e.g. step-01).
	ID string `json:"id"`
	// Kind is the problem-solving kind (domain-neutral).
	Kind string `json:"kind"`
	// Targets are workspace-relative paths from evidence-backed surface.
	// Always a subset of the durable scope; never invented.
	Targets []string `json:"targets"`
	// EvidenceRefs are the evidence signal keys backing this step.
	EvidenceRefs []string `json:"evidence_refs,omitempty"`
	// DependsOn lists predecessor step IDs for dependency awareness.
	DependsOn []string `json:"depends_on,omitempty"`
	// Rationale is a human-readable justification.
	Rationale string `json:"rationale,omitempty"`
	// Intent preserves the original task intent (refinement must not
	// destroy it; spec §14). Refinement copies it unchanged.
	Intent string `json:"intent,omitempty"`
	// StateDigest is the digest/fingerprint of the state this candidate
	// was derived from. Used for freshness checks (§25).
	StateDigest string `json:"state_digest,omitempty"`
	// Estimate is the predicted output/complexity requirement for this step.
	Estimate StepSizeEstimate `json:"estimate"`
	// RefinementDepth tracks how many times this candidate has been refined
	// to enforce finite refinement (spec §13, §24).
	RefinementDepth int `json:"refinement_depth"`
}

// Valid reports whether the candidate is well-formed for admission.
func (c CandidateStep) Valid() bool {
	if strings.TrimSpace(c.ID) == "" {
		return false
	}
	if strings.TrimSpace(c.Kind) == "" {
		return false
	}
	if !isValidKind(c.Kind) {
		return false
	}
	// Targets may be empty only for UNRESOLVED-style candidates, but
	// admission requires at least one evidence-backed target to be
	// admitted; empty targets will be blocked rather than admitted.
	return true
}

func isValidKind(k string) bool {
	switch strings.ToUpper(strings.TrimSpace(k)) {
	case "INVESTIGATE", "ANALYZE", "EXPERIMENT", "MUTATE", "VERIFY", "OBSERVE":
		return true
	default:
		return false
	}
}

// AdmissionState carries the current truthful state for freshness checks.
type AdmissionState struct {
	// CurrentFingerprint is the authoritative workspace/state digest (OCC).
	CurrentFingerprint string `json:"current_fingerprint"`
	// IsStale explicitly marks drift detected by caller (e.g. OCC gate).
	IsStale bool `json:"is_stale"`
}

// AdmissionAction is the pure admission verdict.
type AdmissionAction string

const (
	// ActionAdmit means the step fits the bounded envelope.
	ActionAdmit AdmissionAction = "ADMIT"
	// ActionRefine means the step is too large; a smaller bounded candidate
	// is available via RefinedStep.
	ActionRefine AdmissionAction = "REFINE"
	// ActionBlock means the step cannot be admitted nor refined (e.g. no
	// evidence to reduce, scope violation without approval, finite depth
	// exhausted).
	ActionBlock AdmissionAction = "BLOCK"
	// ActionStale means the candidate is based on stale state.
	ActionStale AdmissionAction = "STALE"
	// ActionAwaitingApproval means the candidate or its refinement would
	// require scope outside the existing envelope.
	ActionAwaitingApproval AdmissionAction = "AWAITING_APPROVAL"
)

// StepAdmissionDecision is the minimal pure admission contract (spec §10).
type StepAdmissionDecision struct {
	// Action is the verdict.
	Action AdmissionAction `json:"action"`
	// Reason explains the verdict (bounded, evidence-bearing).
	Reason string `json:"reason"`
	// Estimate is the predicted requirement for the examined candidate.
	Estimate StepSizeEstimate `json:"estimate"`
	// Budget is the available bounded step budget derived from capability.
	Budget mutationstrategy.StepBudget `json:"budget"`
	// Step is the examined candidate (always present).
	Step *CandidateStep `json:"step,omitempty"`
	// RefinedStep is present only when Action == REFINE.
	RefinedStep *CandidateStep `json:"refined_step,omitempty"`
	// Evidence carries authoritative evidence refs supporting the decision.
	Evidence []string `json:"evidence,omitempty"`
}

// MaxRefinementDepth bounds the refinement chain to avoid infinite loops
// (spec §13 finite, §24 no autonomous loop).
const MaxRefinementDepth = 5
