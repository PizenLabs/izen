// ── Objective Completion Contract ────────────────────────────────────────────
//
// TaskContract (objective_authority.go) answers ONE question: "what SHAPE of
// filesystem work does this objective's evidence have to take?" — CREATE, PATCH,
// DELETE, READ, REVIEW, IDEMPOTENT. Every clause it carries is an EXECUTION
// fact: an artifact was parsed, a delta was observed, a target exists, a
// verifier ran.
//
// That is not the same question as "was the user's objective satisfied?".
// A minimal patch satisfies every clause of a PATCH contract while leaving a
// broad objective almost entirely unaddressed, which is precisely the
// minimum-patch failure this file exists to close.
//
// THE MISSING BOUNDARY. IZEN had an execution completion contract and no
// objective completion contract. Nothing in the runtime represented the
// user's INTENDED OUTCOME as a set of obligations, so the only thing
// authority could ask was "did a mutation of the right shape land?" — and the
// answer to that is yes far too often.
//
// This file adds the missing half, and it keeps the three authority levels
// apart on purpose:
//
//	USER CONSTRAINT          clauses/constraints carried by the request text.
//	                         Runtime-owned. Deterministic segmentation.
//	MODEL-DERIVED REQUIREMENT a structured interpretation the model proposed.
//	                         ADVISORY and traceable. The runtime admits or
//	                         rejects it; the model never discharges it.
//	AUTHORITATIVE CONDITION   an obligation the runtime itself authored and
//	                         will judge. Deterministic and fail-closed.
//
// The dependency direction stays one-way:
//
//	ControlPlane -> ObjectiveContract -> ObjectiveCompletionAuthority
//
// Like the authority itself, this file is DOMAIN. It imports no presentation
// type and no UI package, so a headless runtime can derive and evaluate an
// objective contract without a terminal attached. It holds no authority: it is
// a pure reducer over facts the runtime already owns.
package execution

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ── Requirement origin ──────────────────────────────────────────────────────

// RequirementOrigin records WHO authored a requirement. The three values are
// deliberately distinct: a model assertion and a runtime obligation are never
// the same thing, even when they agree.
type RequirementOrigin string

const (
	// OriginUser marks a requirement carried by the user's own request text.
	// The runtime segments it deterministically; no model was consulted.
	OriginUser RequirementOrigin = "USER"
	// OriginModel marks a structured interpretation the model proposed. It is
	// advisory until the runtime admits it, and it is never discharged by the
	// model's own word — only by an independently observed execution fact.
	OriginModel RequirementOrigin = "MODEL"
	// OriginRuntime marks an obligation the runtime authored from the task
	// contract kind and the resolved scope.
	OriginRuntime RequirementOrigin = "RUNTIME"
)

// String returns the canonical origin label.
func (o RequirementOrigin) String() string { return string(o) }

// ── Requirement status ──────────────────────────────────────────────────────

// RequirementStatus is the admissibility verdict on a proposed requirement.
type RequirementStatus string

const (
	// RequirementAdmitted: the runtime accepted the requirement into the
	// completion contract. It now carries an obligation.
	RequirementAdmitted RequirementStatus = "ADMITTED"
	// RequirementRejected: the runtime refused the requirement and recorded why.
	// A rejected requirement never blocks completion.
	RequirementRejected RequirementStatus = "REJECTED"
	// RequirementUnproven: the requirement was never proposed and the runtime
	// therefore holds no evidence about it.
	RequirementUnproven RequirementStatus = "UNPROVEN"
)

// String returns the canonical requirement-status label.
func (s RequirementStatus) String() string { return string(s) }

// ── ObjectiveClause ─────────────────────────────────────────────────────────

// ClauseKind is the deterministic class of one clause of the user's request.
type ClauseKind string

const (
	// ClauseAction: the clause asks for a change to the workspace. It carries a
	// mutation obligation.
	ClauseAction ClauseKind = "ACTION"
	// ClauseInquiry: the clause asks the runtime to look at something and
	// report. It carries an observation obligation.
	ClauseInquiry ClauseKind = "INQUIRY"
	// ClauseConstraint: the clause constrains the work ("keep the API stable").
	// It is a user constraint; it gates the mutation surface rather than adding
	// a completion obligation of its own.
	ClauseConstraint ClauseKind = "CONSTRAINT"
	// ClauseContext: the clause supplies grounding the model must have ("the
	// author's name is TomHunter"). It carries NO obligation: a fact the user
	// supplied is not an outcome anyone has to prove.
	ClauseContext ClauseKind = "CONTEXT"
)

// String returns the canonical clause-kind label.
func (k ClauseKind) String() string { return string(k) }

// ObjectiveClause is ONE deterministic segment of the user's request, together
// with the class the runtime assigned it. The clause set is the USER half of
// the contract: it exists before any model call and is never re-derived to
// make a completion claim succeed.
type ObjectiveClause struct {
	// ID is the stable clause identity within one objective lifecycle
	// ("cl-1", "cl-2", …).
	ID string
	// Text is the clause as the user wrote it, trimmed.
	Text string
	// Kind is the runtime-assigned deterministic class.
	Kind ClauseKind
	// Targets are the declared workspace-relative targets this clause names
	// explicitly. Empty means "whatever the objective's resolved scope is" —
	// NOT "no targets".
	Targets []string
	// Obligation reports whether the clause carries a completion obligation.
	Obligation bool
}

// ── DerivedRequirement ──────────────────────────────────────────────────────

// DerivedRequirement is one requirement of the objective contract. It is the
// unit that survives bounded provider calls and continuation: the objective
// identity holds the same requirement set from the first computation to the
// final proof.
type DerivedRequirement struct {
	// ID is the requirement identity within the objective ("req-1", …).
	ID string `json:"id"`
	// Text is the requirement description.
	Text string `json:"text"`
	// Origin records who authored it.
	Origin RequirementOrigin `json:"origin"`
	// Status is the runtime's admissibility verdict. A caller that constructs a
	// requirement without setting it gets RequirementUnproven, which the
	// authority treats as NOT admitted — fail-closed.
	Status RequirementStatus `json:"status"`
	// ClauseIDs are the user clauses this requirement was bound to.
	ClauseIDs []string `json:"clause_ids,omitempty"`
	// Targets are the evidence-bound targets this requirement must be
	// discharged against. A requirement with no grounded target is REJECTED,
	// never admitted as an unverifiable obligation.
	Targets []string `json:"targets,omitempty"`
	// RejectReason explains a rejection deterministically.
	RejectReason string `json:"reject_reason,omitempty"`
}

// Admitted reports whether the runtime accepted this requirement into the
// completion contract.
func (r DerivedRequirement) Admitted() bool {
	return r.Status == RequirementAdmitted
}

// ── Completion conditions ───────────────────────────────────────────────────

// ConditionSource records who authored a completion condition. It is the
// explicit USER-vs-MODEL-vs-RUNTIME distinction the runtime must never
// collapse.
type ConditionSource string

const (
	// SourceUser: authored by the runtime FROM the user's own clause set.
	SourceUser ConditionSource = "USER"
	// SourceModel: authored by the runtime FROM an admitted model requirement.
	SourceModel ConditionSource = "MODEL"
	// SourceRuntime: authored by the runtime from the task contract kind and the
	// resolved scope.
	SourceRuntime ConditionSource = "RUNTIME"
)

// String returns the canonical condition-source label.
func (s ConditionSource) String() string { return string(s) }

// Obligation is the deterministic kind of obligation a completion condition
// carries. Every obligation is a LIFECYCLE obligation. None of them judges
// content quality, aesthetics, or change volume: the runtime has no vocabulary
// for "professional" and must never grow one.
type Obligation string

const (
	// ObligationScopeMutated: every target the condition names was durably
	// transformed.
	ObligationScopeMutated Obligation = "SCOPE_MUTATED"
	// ObligationScopeAbsent: every target the condition names was observed
	// absent after execution.
	ObligationScopeAbsent Obligation = "SCOPE_ABSENT"
	// ObligationTargetExists: every target the condition names was observed
	// present after execution.
	ObligationTargetExists Obligation = "TARGET_EXISTS"
	// ObligationObserved: the runtime observed the workspace for the objective's
	// scope. Requesting a target is not observing it.
	ObligationObserved Obligation = "OBSERVED"
	// ObligationPostMutationReinspected: the runtime RE-READ every declared
	// target AFTER the mutation. This is the condition that makes "inspect the
	// result" structural instead of aspirational.
	ObligationPostMutationReinspected Obligation = "POST_MUTATION_REINSPECTED"
	// ObligationIntegrityHeld: the verification gate passed, or was provably not
	// applicable. The condition records WHICH, so no projection can render
	// "verified" for a skipped gate.
	ObligationIntegrityHeld Obligation = "INTEGRITY_HELD"
	// ObligationResponseDelivered: a response was delivered, or a deterministic
	// structural verdict was reached.
	ObligationResponseDelivered Obligation = "RESPONSE_DELIVERED"
	// ObligationRequirementDischarged: the named requirement was discharged by
	// an execution fact the runtime observed itself.
	ObligationRequirementDischarged Obligation = "REQUIREMENT_DISCHARGED"
)

// String returns the canonical obligation label.
func (o Obligation) String() string { return string(o) }

// ConditionStatus is the runtime-computed satisfaction state of one condition.
//
// The authority NEVER reads a caller-supplied status: it recomputes every
// condition from the evidence. The field exists so telemetry and tests can
// read a rendered status, not so a caller can assert one.
type ConditionStatus string

const (
	// ConditionPending: no evidence has been observed for the condition yet.
	ConditionPending ConditionStatus = "PENDING"
	// ConditionSatisfied: the runtime observed the fact the condition demands.
	ConditionSatisfied ConditionStatus = "SATISFIED"
	// ConditionUnsatisfied: the runtime observed the opposite, or observed
	// nothing where something was required.
	ConditionUnsatisfied ConditionStatus = "UNSATISFIED"
	// ConditionNotApplicable: the obligation is provably void for this
	// contract kind. Distinct from SATISFIED: nothing can render "verified".
	ConditionNotApplicable ConditionStatus = "NOT_APPLICABLE"
)

// String returns the canonical condition-status label.
func (s ConditionStatus) String() string { return string(s) }

// VerificationState records HOW a satisfied condition was established. It is
// kept separate from ConditionStatus so "a gate ran and passed" is never
// rendered as "a gate was not applicable" or vice versa.
type VerificationState string

const (
	// VerifyNone: no verification contributed to the condition.
	VerifyNone VerificationState = "NONE"
	// VerifyObserved: an execution fact the runtime recorded satisfied it.
	VerifyObserved VerificationState = "OBSERVED"
	// VerifyGatePass: a verification command gate ran and passed.
	VerifyGatePass VerificationState = "GATE_PASS"
	// VerifyGateFail: a verification command gate ran and failed.
	VerifyGateFail VerificationState = "GATE_FAIL"
	// VerifyGateNotApplicable: no verification contract exists for the target.
	VerifyGateNotApplicable VerificationState = "GATE_NOT_APPLICABLE"
	// VerifyClaimedOnly: only the model claimed it; no observed fact supports
	// it. It is UNSATISFIED, and this state records exactly why.
	VerifyClaimedOnly VerificationState = "CLAIMED_ONLY"
)

// String returns the canonical verification-state label.
func (v VerificationState) String() string { return string(v) }

// CompletionCondition is ONE explicit, traceable obligation of the objective
// lifecycle. It is the unit the authority judges and the unit telemetry
// projects, so "what remains unresolved" is answerable without parsing prose.
type CompletionCondition struct {
	// ID is the stable condition identity ("cond-1", "cond-scope-mutated", …).
	ID string `json:"id"`
	// Description is the human-readable obligation.
	Description string `json:"description"`
	// Source records who authored it.
	Source ConditionSource `json:"source"`
	// RequirementID names the requirement a REQUIREMENT_DISCHARGED condition
	// came from ("" for every other obligation).
	RequirementID string `json:"requirement_id,omitempty"`
	// Obligation is the deterministic obligation class.
	Obligation Obligation `json:"obligation"`
	// Targets are the declared targets the obligation is judged against.
	Targets []string `json:"targets,omitempty"`
	// Status is the runtime-COMPUTED satisfaction state.
	Status ConditionStatus `json:"status"`
	// VerificationState records how the status was established.
	VerificationState VerificationState `json:"verification_state"`
	// EvidenceRefs name the runtime facts that decided the condition. They are
	// runtime event identities, never model prose.
	EvidenceRefs []string `json:"evidence_refs,omitempty"`
}

// Satisfied reports whether the condition may authorize completion.
func (c CompletionCondition) Satisfied() bool {
	return c.Status == ConditionSatisfied || c.Status == ConditionNotApplicable
}

// ── ObjectiveContract ───────────────────────────────────────────────────────

// ObjectiveContract is the domain-neutral, authoritative description of what
// must be true before the user's objective may be reported satisfied.
//
// It is derived ONCE per objective lifecycle from facts the runtime already
// owns, and it is never re-derived to make a completion claim succeed. The
// TaskContract remains the EXECUTION-shape half; this is the OUTCOME half, and
// the authority requires both.
type ObjectiveContract struct {
	// ObjectiveID is the stable identity of the objective across bounded
	// provider calls, continuation and replanning.
	ObjectiveID string `json:"objective_id"`
	// Request is the user's own objective text, verbatim.
	Request string `json:"request"`
	// TaskKind is the execution-shape contract the objective was dispatched
	// under. It is recorded here for traceability; the authority still reads it
	// from its own TaskContract argument.
	TaskKind TaskKind `json:"task_kind"`
	// Scope is the declared, evidence-bound target set of the objective.
	Scope []string `json:"scope,omitempty"`
	// Clauses is the deterministic segmentation of the user's request.
	Clauses []ObjectiveClause `json:"clauses,omitempty"`
	// Requirements is the full requirement ledger, including REJECTED entries,
	// so a rejection stays auditable instead of vanishing.
	Requirements []DerivedRequirement `json:"requirements,omitempty"`
	// Conditions is the authoritative completion contract. EVERY condition must
	// be satisfied before the objective may be PROVEN.
	Conditions []CompletionCondition `json:"conditions,omitempty"`
}

// AdmittedRequirements returns the subset of the requirement ledger the
// runtime accepted into the completion contract.
func (c ObjectiveContract) AdmittedRequirements() []DerivedRequirement {
	if len(c.Requirements) == 0 {
		return nil
	}
	out := make([]DerivedRequirement, 0, len(c.Requirements))
	for _, r := range c.Requirements {
		if r.Admitted() {
			out = append(out, r)
		}
	}
	return out
}

// Requirement returns the ledger entry with the given ID.
func (c ObjectiveContract) Requirement(id string) (DerivedRequirement, bool) {
	for _, r := range c.Requirements {
		if r.ID == id {
			return r, true
		}
	}
	return DerivedRequirement{}, false
}

// PendingConditionIDs returns the conditions that are not yet satisfied, in
// contract order. It is the deterministic "what remains unresolved" answer
// the continuation path and the trace both read.
func (c ObjectiveContract) PendingConditionIDs() []string {
	var out []string
	for _, cond := range c.Conditions {
		if !cond.Satisfied() {
			out = append(out, cond.ID)
		}
	}
	return out
}

// RequirementIDs returns the IDs of every requirement in the ledger, in order.
func (c ObjectiveContract) RequirementIDs() []string {
	if len(c.Requirements) == 0 {
		return nil
	}
	out := make([]string, 0, len(c.Requirements))
	for _, r := range c.Requirements {
		out = append(out, r.ID)
	}
	return out
}

// ── Derivation ──────────────────────────────────────────────────────────────

// ObjectiveDerivation is the deterministic input set of
// DeriveObjectiveContract. It carries only facts the runtime already owns; it
// carries no verdict and no model prose.
type ObjectiveDerivation struct {
	// ObjectiveID is the lifecycle identity. Empty yields a deterministic
	// identity derived from the request so two identical requests still get two
	// distinguishable contracts.
	ObjectiveID string
	// Request is the user's objective text.
	Request string
	// Kind is the canonical task contract kind.
	Kind TaskKind
	// Scope is the evidence-bound declared target set.
	Scope []string
	// Clauses is the deterministic clause segmentation of the request. When
	// empty the runtime segments the request itself.
	Clauses []ObjectiveClause
	// Proposals are the model-proposed requirements. The runtime admits or
	// rejects each one; a proposal is never trusted verbatim.
	Proposals []DerivedRequirement
	// ClaimedRequirements are requirement ids the model CLAIMS it satisfied in
	// its own output. They are recorded for telemetry as CLAIMED_ONLY and never
	// count as discharge.
	ClaimedRequirements []string
	// TargetNames is the resolved target vocabulary the grounding gate admits
	// against. Empty falls back to Scope.
	TargetNames []string
}

// DeriveObjectiveContract resolves the ONE authoritative completion contract of
// an objective lifecycle. It is deterministic and total: identical derivation
// inputs always yield an identical contract.
//
// The derivation has three stages and they never blur:
//
//  1. CLAUSES      — the user's own request, segmented deterministically. No
//     model is consulted; nothing here can be talked into
//     satisfying a completion claim.
//  2. REQUIREMENTS — every model proposal passes the ADMISSIBILITY GATE. A
//     proposal is admitted only when it is traceable to the
//     user's request AND grounded in a concrete target inside
//     the objective's evidence-bound scope. An ungrounded
//     proposal is REJECTED with a reason, so a model cannot
//     manufacture an obligation it will never discharge — and
//     equally cannot shrink the contract by staying silent
//     about the parts it chose to skip.
//  3. CONDITIONS   — the runtime authors the obligation set. Runtime
//     obligations always come first; admitted model requirements
//     become additional REQUIREMENT_DISCHARGED conditions.
//
// NOTE ON WHAT IS NOT HERE. There is no minimum-change-size rule, no file-type
// rule and no domain vocabulary. "Professional", "thorough" and "better" are
// not obligations, because a deterministic runtime that could enforce them
// would be a quality scorer wearing a determinism costume. What the runtime
// CAN enforce — and does — is that every obligation it holds must be discharged
// by evidence it observed itself.
func DeriveObjectiveContract(in ObjectiveDerivation) ObjectiveContract {
	scope := normalizeScope(append(append([]string(nil), in.Scope...), in.TargetNames...))
	clauses := in.Clauses
	if len(clauses) == 0 {
		clauses = SegmentObjectiveRequest(in.Request)
	}

	contract := ObjectiveContract{
		ObjectiveID: deriveObjectiveID(in.ObjectiveID, in.Request),
		Request:     in.Request,
		TaskKind:    in.Kind,
		Scope:       scope,
		Clauses:     clauses,
	}

	// ── 2 — the admissibility gate ──────────────────────────────────
	contract.Requirements = admitRequirements(in.Proposals, in.Request, scope, clauses)

	// ── 3 — the runtime's own obligations ────────────────────────────
	contract.Conditions = authorRuntimeConditions(in.Kind, scope)

	// …then the obligations the admitted model requirements add.
	for _, r := range contract.Requirements {
		if !r.Admitted() {
			continue
		}
		contract.Conditions = append(contract.Conditions, CompletionCondition{
			ID:                "cond-" + r.ID,
			Description:       r.Text,
			Source:            SourceModel,
			RequirementID:     r.ID,
			Obligation:        ObligationRequirementDischarged,
			Targets:           append([]string(nil), r.Targets...),
			Status:            ConditionPending,
			VerificationState: VerifyNone,
		})
	}
	return contract
}

// authorRuntimeConditions builds the obligations the runtime owns. They are a
// function of the task contract KIND and the resolved SCOPE only — never of the
// size, shape or domain of the change.
func authorRuntimeConditions(kind TaskKind, scope []string) []CompletionCondition {
	cond := func(id, desc string, ob Obligation, targets []string) CompletionCondition {
		return CompletionCondition{
			ID:                id,
			Description:       desc,
			Source:            SourceRuntime,
			Obligation:        ob,
			Targets:           append([]string(nil), targets...),
			Status:            ConditionPending,
			VerificationState: VerifyNone,
		}
	}
	scopeCopy := append([]string(nil), scope...)

	switch kind {
	case TaskCreate:
		return []CompletionCondition{
			cond("cond-scope-mutated",
				"every declared target was durably created", ObligationScopeMutated, scopeCopy),
			cond("cond-target-exists",
				"every declared target exists on disk after the apply", ObligationTargetExists, scopeCopy),
			cond("cond-observed",
				"the declared scope was observed, not answered from priors", ObligationObserved, scopeCopy),
			cond("cond-post-mutation-reinspected",
				"every declared target was re-read after the mutation",
				ObligationPostMutationReinspected, scopeCopy),
			cond("cond-integrity-held",
				"the verification gate for the declared scope passed or is provably not applicable",
				ObligationIntegrityHeld, scopeCopy),
		}
	case TaskPatch:
		return []CompletionCondition{
			cond("cond-scope-mutated",
				"every declared target was durably transformed", ObligationScopeMutated, scopeCopy),
			cond("cond-observed",
				"the declared scope was observed, not answered from priors", ObligationObserved, scopeCopy),
			cond("cond-post-mutation-reinspected",
				"every declared target was re-read after the mutation",
				ObligationPostMutationReinspected, scopeCopy),
			cond("cond-integrity-held",
				"the verification gate for the declared scope passed or is provably not applicable",
				ObligationIntegrityHeld, scopeCopy),
		}
	case TaskDelete:
		return []CompletionCondition{
			cond("cond-scope-mutated",
				"every declared target was durably removed", ObligationScopeMutated, scopeCopy),
			cond("cond-scope-absent",
				"every declared target was observed absent after the delete", ObligationScopeAbsent, scopeCopy),
			cond("cond-integrity-held",
				"the verification gate for the declared scope passed or is provably not applicable",
				ObligationIntegrityHeld, scopeCopy),
		}
	case TaskRead, TaskReview:
		return []CompletionCondition{
			cond("cond-observed",
				"the declared scope was observed, not answered from priors", ObligationObserved, scopeCopy),
			cond("cond-response-delivered",
				"a response (or a deterministic structural verdict) was delivered",
				ObligationResponseDelivered, nil),
		}
	case TaskIdempotent:
		return []CompletionCondition{
			cond("cond-post-mutation-reinspected",
				"every declared target was re-read after execution",
				ObligationPostMutationReinspected, scopeCopy),
			cond("cond-integrity-held",
				"the structural verdict for the declared scope holds",
				ObligationIntegrityHeld, scopeCopy),
		}
	default:
		// An unknown contract kind authors NO completion conditions, which means
		// it can prove nothing. Fail-closed by construction.
		return nil
	}
}

// ── The admissibility gate ──────────────────────────────────────────────────

// groundingFloor is the minimum share of a proposal's significant words that
// must be ANCHORED for the proposal to count as a traceable restatement of the
// request.
//
// A word is anchored when it appears in the user's own request, or in the
// resolved target vocabulary (which the user named indirectly by asking for that
// scope), or in the basename of a declared target. It is a TRACEABILITY
// threshold — it separates "a restatement of what was asked" from "vocabulary
// the request never contained" — and it never rewards a proposal for being
// elaborate. A requirement written entirely in the request's own vocabulary is
// admitted no matter how long it is; one written entirely in invented
// vocabulary is rejected no matter how plausible it sounds.
const groundingFloor = 1.0 / 3.0

// admitRequirements runs the deterministic admissibility gate over the model's
// proposals. Every proposal gets a verdict and a reason; nothing is dropped
// silently.
//
// A proposal is ADMITTED only when all three hold:
//
//	GROUNDED IN TEXT     its significant words are traceable to the request
//	GROUNDED IN SCOPE    it names a target inside the objective's evidence-bound
//	                     scope — the requirement therefore says WHERE it must be
//	                     discharged, so discharge is decidable deterministically
//	TRACEABLE            it is non-empty and carries an identity
//
// The third rule is the one that makes this a contract rather than a wish list.
// A requirement the runtime cannot decide the satisfaction of is not a
// requirement; it is a slogan, and admitting slogans would let a model make
// any objective permanently unprovable.
func admitRequirements(proposals []DerivedRequirement, request string, scope []string, clauses []ObjectiveClause) []DerivedRequirement {
	out := make([]DerivedRequirement, 0, len(proposals))
	seen := map[string]bool{}
	n := 0
	for _, p := range proposals {
		if p.ID == "" {
			n++
			p.ID = fmt.Sprintf("req-%d", n)
		} else if seen[p.ID] {
			// A duplicated identity would make two obligations indistinguishable
			// in the ledger. Renaming is deterministic and loses nothing.
			n++
			p.ID = fmt.Sprintf("%s-dup-%d", p.ID, n)
		}
		seen[p.ID] = true

		if p.Origin == "" {
			p.Origin = OriginModel
		}
		text := strings.TrimSpace(p.Text)
		if text == "" {
			p.Status = RequirementRejected
			p.RejectReason = "empty requirement text — nothing to prove"
			out = append(out, p)
			continue
		}
		p.Text = text

		if !groundedInRequest(text, request, scope) {
			p.Status = RequirementRejected
			p.RejectReason = "not traceable to the user's request — introduces vocabulary the request never used"
			out = append(out, p)
			continue
		}
		grounded := groundTargets(text, p.Targets, scope)
		if len(grounded) == 0 {
			p.Status = RequirementRejected
			p.RejectReason = "not grounded in any target of the objective's resolved scope — the runtime cannot decide its satisfaction"
			out = append(out, p)
			continue
		}
		p.Targets = grounded
		p.ClauseIDs = bindClauses(text, clauses)
		p.Status = RequirementAdmitted
		p.RejectReason = ""
		out = append(out, p)
	}
	return out
}

// significantWords lowercases and reduces a text to the tokens used for
// grounding: it drops punctuation and a small closed set of English function
// words, which carry no topical content in any domain.
func significantWords(text string) []string {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '.' && r != '_' && r != '-' && r != '/'
	})
	seen := map[string]bool{}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if len(f) < 3 || functionWords[f] || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// functionWords is a closed, domain-neutral stop list: words that carry no
// topical content and would otherwise let every requirement look grounded.
var functionWords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "that": true,
	"this": true, "these": true, "those": true, "into": true, "from": true,
	"its": true, "it": true, "has": true, "have": true, "had": true,
	"was": true, "were": true, "are": true, "you": true, "your": true,
	"our": true, "their": true, "them": true, "they": true, "all": true,
	"any": true, "each": true, "every": true, "also": true, "then": true,
	"than": true, "but": true, "not": true, "can": true, "will": true,
	"should": true, "would": true, "could": true, "make": true, "made": true,
	"using": true, "use": true, "used": true, "please": true, "need": true,
	"needs": true, "must": true, "new": true, "old": true, "more": true,
	"most": true, "some": true, "such": true, "via": true, "per": true,
}

// groundedInRequest reports whether a proposal is traceable to the user's own
// request.
//
// Traceability is measured as the share of the proposal's significant words
// that are ANCHORED — present in the request, present in the resolved target
// vocabulary, or a component of a declared target's path. It is deliberately a
// LOWER BOUND on traceability, not a similarity score:
//
//   - a paraphrase passes, because naming the target the user asked about is
//     itself an anchor;
//   - invented vocabulary fails, because none of its words anchor anywhere.
//
// What it is NOT: a quality bar, a completeness measure, or a reason to reject a
// requirement for being worded differently from the request.
func groundedInRequest(text, request string, scope []string) bool {
	words := significantWords(text)
	if len(words) == 0 {
		return false
	}
	anchors := map[string]bool{}
	for _, w := range significantWords(request) {
		anchors[w] = true
	}
	for _, t := range scope {
		for _, w := range significantWords(t) {
			anchors[w] = true
		}
		// The basename and the stem of a declared target are anchors too: a
		// requirement naming "styles" for "public/styles.css" is naming the file
		// the scope declared.
		base := strings.ToLower(t)
		if idx := strings.LastIndexAny(base, "/\\"); idx >= 0 {
			base = base[idx+1:]
		}
		if idx := strings.LastIndex(base, "."); idx > 0 {
			base = base[:idx]
		}
		if len(base) >= 3 {
			anchors[base] = true
		}
	}
	anchored := 0
	for _, w := range words {
		if anchors[w] {
			anchored++
		}
	}
	return float64(anchored)/float64(len(words)) >= groundingFloor
}

// groundTargets resolves the targets a requirement is judged against. The pool
// is the objective's EVIDENCE-BOUND RESOLVED SCOPE and nothing else.
//
// A proposal may NARROW that pool — "this requirement is about styles.css
// specifically" — but it may never ADD to it. Letting a proposal's own declared
// targets join the pool would let a model ground an invented obligation against a
// file the runtime never resolved, which is precisely the widening the gate
// exists to prevent.
//
// The match is deliberately POSITIONAL and lexical: a target is only bound when
// the requirement names that target (bare, path-qualified or as a filename
// component). It never infers a target from a file extension, a language or a
// domain concept — that inference is how a domain-agnostic runtime grows a
// portfolio-specific evaluator by accident.
func groundTargets(text string, declared []string, scope []string) []string {
	if len(scope) == 0 {
		return nil
	}
	var out []string
	lower := strings.ToLower(text)
	for _, t := range scope {
		if t == "" {
			continue
		}
		if namesTarget(lower, t) || containsTargetList(declared, t) {
			out = appendUnique(out, t)
		}
	}
	return out
}

// namesTarget reports whether a lowercased text names a target, bare,
// path-qualified, or as a filename component ("styles.css" matches
// "public/styles.css").
func namesTarget(lowerText, target string) bool {
	base := strings.ToLower(strings.TrimPrefix(target, "./"))
	if base == "" {
		return false
	}
	if strings.Contains(lowerText, base) {
		return true
	}
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		leaf := base[idx+1:]
		return leaf != "" && strings.Contains(lowerText, leaf)
	}
	return false
}

func containsTargetList(list []string, target string) bool {
	for _, t := range list {
		if strings.EqualFold(strings.TrimPrefix(t, "./"), strings.TrimPrefix(target, "./")) {
			return true
		}
	}
	return false
}

// bindClauses returns the ids of the user clauses the proposal is traceable to.
// A requirement that restates no clause is still admissible — it may be a
// legitimate reading of the objective as a whole — but the linkage is recorded
// so a reviewer can see WHAT it claims to come from.
func bindClauses(text string, clauses []ObjectiveClause) []string {
	var out []string
	for _, c := range clauses {
		if c.Text == "" {
			continue
		}
		pool := significantWords(c.Text)
		if len(pool) == 0 {
			continue
		}
		share := 0
		present := map[string]bool{}
		for _, w := range significantWords(text) {
			present[w] = true
		}
		for _, w := range pool {
			if present[w] {
				share++
			}
		}
		if float64(share)/float64(len(pool)) >= 0.5 {
			out = append(out, c.ID)
		}
	}
	return out
}

// ── Deterministic segmentation of the user's request ────────────────────────

// clauseSeparators are the boundary markers the deterministic segmenter splits
// on. They are punctuation and coordinating words, not domain vocabulary, so the
// segmentation behaves identically for a Go refactor, a documentation update and
// a page redesign.
//
// `.` is handled separately (see splitSentences) because a full stop is ALSO the
// most common character inside a file path, and splitting on it would tear
// "@index.html" into "@index" and "html" — silently losing the target reference
// the clause was written to name.
var clauseSeparators = []string{
	";", "!", "?", "\n",
	" and then ", " and also ", ", and ", " and ",
	" then ", " also ", " plus ", ", plus ",
	" as well as ", " plus ",
}

// SegmentObjectiveRequest splits the user's objective into deterministic
// clauses. It is a pure function of the request text: no model is consulted and
// no workspace is read.
//
// It is deliberately conservative about obligation assignment. A clause only
// carries an obligation when it asks for something the runtime can observe —
// an ACTION it can check as a mutation, an INQUIRY it can check as an
// observation. A clause that merely supplies context or constrains the work is
// recorded (so the request is fully represented) without being turned into an
// obligation the runtime could never discharge.
func SegmentObjectiveRequest(request string) []ObjectiveClause {
	trimmed := strings.TrimSpace(request)
	if trimmed == "" {
		return nil
	}
	segments := splitClauses(trimmed)
	out := make([]ObjectiveClause, 0, len(segments))
	for i, seg := range segments {
		text := strings.TrimSpace(seg)
		if text == "" {
			continue
		}
		kind := classifyClause(text)
		out = append(out, ObjectiveClause{
			ID:         fmt.Sprintf("cl-%d", len(out)+1),
			Text:       text,
			Kind:       kind,
			Targets:    clauseTargets(text),
			Obligation: kind == ClauseAction || kind == ClauseInquiry,
		})
		_ = i
	}
	return out
}

// splitClauses performs the segmentation. It is length-first so a longer
// marker (" and then ") always wins over a shorter prefix (" and ").
func splitClauses(text string) []string {
	working := text
	for _, marker := range clauseSeparators {
		working = strings.ReplaceAll(working, marker, "\x00")
	}
	var out []string
	for _, part := range strings.Split(working, "\x00") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		for _, sentence := range splitSentences(part) {
			if sentence = strings.TrimSpace(sentence); sentence != "" {
				out = append(out, sentence)
			}
		}
	}
	return out
}

// splitSentences splits a segment on full stops that actually END a sentence.
//
// The rule is positional, not lexical: a `.` terminates a sentence only when it
// is followed by whitespace or the end of the text. A `.` with a word character
// on both sides is part of a token — a file extension, a version number, a
// hostname — and splitting there would destroy the very target reference the
// clause names.
func splitSentences(part string) []string {
	var out []string
	start := 0
	for i := 0; i < len(part); i++ {
		if part[i] != '.' {
			continue
		}
		if i+1 < len(part) && !isSpaceByte(part[i+1]) {
			continue
		}
		// A full stop preceded by no word character is an ellipsis or a bare
		// separator; it still ends the segment.
		out = append(out, part[start:i+1])
		start = i + 1
	}
	if start < len(part) {
		out = append(out, part[start:])
	}
	if len(out) == 0 {
		return []string{part}
	}
	return out
}

func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// clauseChangeVerbs is the shared, domain-neutral table of change requests. It
// is the SAME vocabulary the intent classifier uses, for the same reason: an
// objective that classifies as a mutation request must not have its clauses
// silently re-read as commentary.
var clauseChangeVerbs = []string{
	"add", "apply", "build", "change", "convert", "correct", "create", "cut",
	"debug", "delete", "disable", "drop", "enable", "erase", "extract", "fix",
	"implement", "improve", "include", "insert", "introduce", "make",
	"modernise", "modernize", "move", "overhaul", "patch", "port", "re-create",
	"recreate", "redesign", "reduce", "refactor", "remove", "rename",
	"reorganize", "reorganise", "repair", "replace", "restructure", "restyle",
	"rework", "revamp", "rewrite", "simplify", "split", "strip", "swap",
	"tidy", "unify", "unlink", "update", "upgrade", "write",
}

// clauseInquiryVerbs is the shared, domain-neutral table of requests to LOOK and
// REPORT. These discharge on an observation, not on a mutation.
var clauseInquiryVerbs = []string{
	"analyse", "analyze", "audit", "check", "describe", "explain", "find",
	"identify", "inspect", "list", "locate", "report", "review", "search",
	"show", "summarise", "summarize", "survey", "trace", "understand",
}

// clauseConstraintMarkers mark a clause as a CONSTRAINT: it states a rule the
// work must respect rather than an outcome to produce.
var clauseConstraintMarkers = []string{
	"must ", "must not", "do not ", "don't ", "without ", "keep ", "preserve ",
	"retain ", "maintain ", "never ", "avoid ", "ensure ", "only ",
}

// clauseContextMarkers mark a clause as pure CONTEXT: grounding the user
// supplied, with no outcome to prove. A fact the user handed the runtime is not
// an obligation, and turning it into one would make the objective permanently
// unprovable.
var clauseContextMarkers = []string{
	" is ", " are ", " was ", " were ", " named ", " called ", " named as ",
	" currently ", " today ", " version ", " author is ",
}

func classifyClause(text string) ClauseKind {
	lower := " " + strings.ToLower(strings.TrimSpace(text)) + " "
	for _, m := range clauseConstraintMarkers {
		if strings.Contains(lower, m) {
			return ClauseConstraint
		}
	}
	for _, v := range clauseChangeVerbs {
		if clauseCarriesVerb(lower, v) {
			return ClauseAction
		}
	}
	for _, v := range clauseInquiryVerbs {
		if clauseCarriesVerb(lower, v) {
			return ClauseInquiry
		}
	}
	for _, m := range clauseContextMarkers {
		if strings.Contains(lower, m) {
			return ClauseContext
		}
	}
	// No observable verb at all. It is recorded as CONTEXT: the runtime cannot
	// decide its satisfaction, so it is not turned into an obligation.
	return ClauseContext
}

// clauseCarriesVerb reports whether a verb appears as a word (or a word
// prefix, for the inflected forms the tables already carry) rather than as a
// substring of an unrelated word.
func clauseCarriesVerb(lowerPadded, verb string) bool {
	if strings.Contains(lowerPadded, " "+verb) {
		return true
	}
	// Tolerate regular inflections of the listed stems.
	for _, suffix := range []string{"s", "es", "ed", "ing"} {
		if strings.Contains(lowerPadded, " "+verb+suffix) {
			return true
		}
	}
	return false
}

// clauseTargets returns the explicit @file / bare-path references inside one
// clause.
//
// The extraction is deliberately conservative — only concrete, targetable
// references are recognised — because a clause-level target is used to BIND an
// obligation to a file. A loose matcher would invent obligations against files
// the user never named, which is how a domain-agnostic runtime quietly grows a
// file-type heuristic.
func clauseTargets(text string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(raw string) {
		t := strings.TrimSuffix(strings.TrimSpace(raw), ",")
		t = strings.TrimSuffix(t, ".")
		if t == "" || seen[t] {
			return
		}
		seen[t] = true
		out = append(out, t)
	}
	for _, m := range clauseTargetRefPattern.FindAllStringSubmatch(text, -1) {
		if len(m) > 1 {
			add(m[1])
		}
	}
	for _, m := range clauseBarePathPattern.FindAllStringSubmatch(text, -1) {
		if len(m) > 1 {
			add(m[1])
		}
	}
	return out
}

// clauseTargetRefPattern matches an explicit @file reference. clauseBarePathPattern
// matches a bare path. Both accept the same closed extension set the intent
// classifier uses, so target extraction behaves identically everywhere.
var (
	clauseTargetRefPattern = regexp.MustCompile(`(?i)@([a-zA-Z0-9_./\\-]+\.[a-zA-Z0-9]+)`)
	clauseBarePathPattern  = regexp.MustCompile(`(?:^|[\s(])([a-zA-Z0-9_.-]+(?:/[a-zA-Z0-9_.-]+)*\.[a-zA-Z0-9]{1,6})`)
)

// ── Small shared helpers ────────────────────────────────────────────────────

func normalizeScope(in []string) []string { return uniqueStrings(in) }

func uniqueStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "./"))
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// deriveObjectiveID resolves the stable objective identity. An explicitly
// supplied identity always wins; otherwise a deterministic digest of the
// request keeps two different objectives apart without a clock or a counter.
func deriveObjectiveID(supplied, request string) string {
	if s := strings.TrimSpace(supplied); s != "" {
		return s
	}
	return "obj-" + objectiveIdentityDigest(request)
}

// objectiveIdentityDigest renders a compact, deterministic FNV-1a digest of the
// request text. It is an IDENTITY LABEL, not a security boundary: its only job
// is to keep two objectives apart without a clock, a counter, or a random
// source that would make the contract non-deterministic.
func objectiveIdentityDigest(s string) string {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	var h uint64 = offset64
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime64
	}
	return renderBase36(h)
}

func renderBase36(h uint64) string {
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	if h == 0 {
		return "0"
	}
	var buf [13]byte
	i := len(buf)
	for h > 0 {
		i--
		buf[i] = digits[h%36]
		h /= 36
	}
	return string(buf[i:])
}

// ── Sorting helper used by trace projections ────────────────────────────────

// SortedConditionIDs returns the condition ids in lexical order. Telemetry uses
// it so two renderings of the same contract always compare equal.
func (c ObjectiveContract) SortedConditionIDs() []string {
	out := make([]string, 0, len(c.Conditions))
	for _, cond := range c.Conditions {
		out = append(out, cond.ID)
	}
	sort.Strings(out)
	return out
}
