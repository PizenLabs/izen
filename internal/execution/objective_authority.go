// ── Objective Completion Authority (Phase 14) ────────────────────────────────
//
// This file is the runtime's OBJECTIVE TRUTH layer. It exists because the
// execution lifecycle carries FOUR independent "done" signals that are routinely
// and wrongly collapsed into one:
//
//	ProviderState = DONE      the model stopped emitting tokens      (transport)
//	ArtifactState = PRODUCED  a parser extracted a valid artifact    (syntax)
//	MutationBoundaryState = APPLIED  the filesystem was transformed  (state)
//	ObjectiveState = PROVEN   evidence + verification satisfied the
//	                          Task Contract                          (meaning)
//
// ONLY the fourth may authorize a `completed` transition. The first three are
// facts about a transport, a parser and a filesystem; none of them says the
// user's objective was achieved. A provider that returns `finish_reason=stop`
// after emitting plain prose satisfies NONE of the first three and therefore
// cannot — structurally, not by policy — reach PROVEN.
//
// The dependency direction is deliberate and one-way:
//
//	Driver -> ExecutionEvidence -> ObjectiveCompletionAuthority -> ObjectiveOutcome
//	       -> Presentation Projection
//
// This package (internal/execution) is DOMAIN. It imports no presentation type
// and no UI package, so a headless runtime can evaluate an objective without a
// terminal attached. The authority is a pure reducer over observed facts: it
// holds no authority of its own, mutates nothing, and computes nothing no
// event reported.
package execution

import (
	"errors"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/execution/strategy"
)

// ── The four independent lifecycle states ───────────────────────────────────

// ProviderState is the TRANSPORT boundary: what the model provider did. It
// carries NO task meaning whatsoever — `DONE` only says the socket closed.
type ProviderState string

const (
	// ProviderPending: no provider request has been issued yet.
	ProviderPending ProviderState = "PENDING"
	// ProviderStreaming: the provider is still emitting.
	ProviderStreaming ProviderState = "STREAMING"
	// ProviderDone: the provider stopped emitting tokens. NOT task completion.
	ProviderDone ProviderState = "DONE"
	// ProviderRefused: the provider refused or filtered the generation.
	ProviderRefused ProviderState = "REFUSED"
)

// Terminal reports whether the provider boundary has settled (it will emit no
// further tokens for this attempt). It is intentionally NOT a task fact.
func (s ProviderState) Terminal() bool {
	return s == ProviderDone || s == ProviderRefused
}

// String returns the canonical provider-state label.
func (s ProviderState) String() string { return string(s) }

// ArtifactState is the PARSER boundary: what the artifact parser extracted from
// the provider payload. Raw model text is NOT an artifact.
type ArtifactState string

const (
	// ArtifactNone: no artifact contract was found in the payload.
	ArtifactNone ArtifactState = "NONE"
	// ArtifactContinuing: the generation was truncated (finish_reason=length) and
	// the delivered prefix is PRESERVED as a partial candidate for a token
	// continuation. A continuing artifact is never a produced artifact.
	ArtifactContinuing ArtifactState = "CONTINUING"
	// ArtifactProduced: at least one valid artifact/patch was extracted.
	ArtifactProduced ArtifactState = "PRODUCED"
)

// String returns the canonical artifact-state label.
func (s ArtifactState) String() string { return string(s) }

// MutationBoundaryState is the FILESYSTEM boundary: what happened on disk. It
// is the Phase 14 "MutationState = APPLIED" axis; the identifier carries the
// Boundary suffix because `MutationState` in this package is already the
// MutationSet LIFECYCLE vocabulary (mutationset.go) and conflating the two
// would make "the set is applying" indistinguishable from "the disk changed".
type MutationBoundaryState string

const (
	// FilesystemNone: no apply ran against the filesystem.
	FilesystemNone MutationBoundaryState = "NONE"
	// FilesystemApplied: the declared targets were transformed on disk and the
	// transformation is durable (not rolled back).
	FilesystemApplied MutationBoundaryState = "APPLIED"
	// FilesystemRolledBack: an apply ran and was undone. Never durable truth.
	FilesystemRolledBack MutationBoundaryState = "ROLLED_BACK"
)

// Durable reports whether the filesystem boundary holds committed truth.
func (s MutationBoundaryState) Durable() bool { return s == FilesystemApplied }

// String returns the canonical filesystem-state label.
func (s MutationBoundaryState) String() string { return string(s) }

// ObjectiveState is the MEANING boundary: whether the evidence satisfies the
// Task Contract. It is the ONLY state that may authorize `completed`.
type ObjectiveState string

const (
	// ObjectiveStateUnproven: the evidence does not satisfy the Task Contract.
	ObjectiveStateUnproven ObjectiveState = "UNPROVEN"
	// ObjectiveStateProven: evidence + verification satisfied the Task Contract.
	ObjectiveStateProven ObjectiveState = "PROVEN"
)

// String returns the canonical objective-state label.
func (s ObjectiveState) String() string { return string(s) }

// Proven reports whether the MEANING boundary is satisfied. Read this before
// every `completed` transition.
func (s ObjectiveState) Proven() bool { return s == ObjectiveStateProven }

// ── ObjectiveOutcome ────────────────────────────────────────────────────────

// ObjectiveOutcome is the canonical, total verdict vocabulary of the
// ObjectiveCompletionAuthority. Every completion claim reduces to exactly one
// of these four states.
type ObjectiveOutcome string

const (
	// ObjectiveProven: the evidence satisfies the Task Contract. This is the
	// ONLY outcome that may authorize a `completed` loop transition.
	ObjectiveProven ObjectiveOutcome = "PROVEN"
	// ObjectiveUnsubstantiated: the execution terminated without evidence that
	// satisfies the Task Contract. The claim is unsubstantiated — not false,
	// simply unproven. No `completed` transition is permitted.
	ObjectiveUnsubstantiated ObjectiveOutcome = "UNSUBSTANTIATED"
	// ObjectiveFailed: the execution positively failed. Distinguished from
	// UNSUBSTANTIATED so the recovery matrix can apply the failure matrix.
	ObjectiveFailed ObjectiveOutcome = "FAILED"
	// ObjectiveRequiresAuthorization: the evidence is sufficient but the
	// mutation is held at a human gate. The decision belongs to the human.
	ObjectiveRequiresAuthorization ObjectiveOutcome = "REQUIRES_AUTHORIZATION"
)

// Proves reports whether the outcome is the only one that authorizes
// completion. Read this before every `completed` transition.
func (o ObjectiveOutcome) Proves() bool { return o == ObjectiveProven }

// Terminal reports whether the outcome settles the claim (all four do).
func (o ObjectiveOutcome) Terminal() bool {
	switch o {
	case ObjectiveProven, ObjectiveUnsubstantiated, ObjectiveFailed, ObjectiveRequiresAuthorization:
		return true
	default:
		return false
	}
}

// String returns the canonical outcome label.
func (o ObjectiveOutcome) String() string { return string(o) }

// ObjectiveState projects the outcome onto the MEANING boundary. Only PROVEN
// maps to ObjectiveProven; everything else is unproven meaning.
func (o ObjectiveOutcome) ObjectiveState() ObjectiveState {
	if o.Proves() {
		return ObjectiveStateProven
	}
	return ObjectiveStateUnproven
}

// ErrObjectiveUnsubstantiated is returned when evidence fails the Task
// Contract. The Driver MUST NOT emit `objective satisfied: completed` for it:
// the correct terminal truth is that the objective was not proven.
var ErrObjectiveUnsubstantiated = errors.New("execution: objective unsubstantiated — evidence does not satisfy the task contract")

// ErrObjectiveFailed is returned when the evidence positively contradicts the
// Task Contract (a failed apply, a failed verifier, a provider refusal).
var ErrObjectiveFailed = errors.New("execution: objective failed — evidence contradicts the task contract")

// ErrObjectiveRequiresAuthorization is returned when the evidence is complete
// but the mutation is held at a human gate.
var ErrObjectiveRequiresAuthorization = errors.New("execution: objective requires human authorization")

// ── TaskContract ────────────────────────────────────────────────────────────

// TaskKind is the canonical task contract vocabulary. Each kind carries a
// DIFFERENT evidence obligation: `mutation count > 0` is NOT a universal
// completion rule, and a CREATE contract is never silently relabelled as a
// PATCH contract.
type TaskKind string

const (
	// TaskCreate: create a brand-new target. Requires artifact + mutation +
	// durable target existence + verifier.
	TaskCreate TaskKind = "CREATE"
	// TaskPatch: modify an existing target. Requires artifact + mutation +
	// observed filesystem delta + verifier.
	TaskPatch TaskKind = "PATCH"
	// TaskDelete: remove a target. Requires mutation + target absence + verifier.
	TaskDelete TaskKind = "DELETE"
	// TaskRead: read/answer from the workspace. Requires an observation event +
	// a satisfied response contract. Mutation is forbidden.
	TaskRead TaskKind = "READ"
	// TaskReview: review/verify existing state. Requires an observation event
	// (including an AST parse event when one is applicable) + a response
	// contract. Mutation is forbidden.
	TaskReview TaskKind = "REVIEW"
	// TaskIdempotent: the objective was already satisfied on disk BEFORE
	// execution. The mutation count MAY be zero; the precondition + verifier
	// are the contract.
	TaskIdempotent TaskKind = "IDEMPOTENT"
)

// String returns the canonical task-kind label.
func (k TaskKind) String() string { return string(k) }

// RequiresMutation reports whether the contract demands a durable filesystem
// change. READ/REVIEW never do; IDEMPOTENT explicitly does not (its
// precondition already satisfies the mutation surface).
func (k TaskKind) RequiresMutation() bool {
	switch k {
	case TaskCreate, TaskPatch, TaskDelete:
		return true
	default:
		return false
	}
}

// Valid reports whether the kind is a member of the canonical vocabulary.
func (k TaskKind) Valid() bool {
	switch k {
	case TaskCreate, TaskPatch, TaskDelete, TaskRead, TaskReview, TaskIdempotent:
		return true
	default:
		return false
	}
}

// TaskContract is the obligation set a completion claim is judged against. It
// is derived ONCE per execution lifecycle from facts the runtime already owns
// (classified intent, resolved targets, artifact shape, pre-execution target
// state) and is immutable afterwards.
type TaskContract struct {
	// Kind is the canonical contract kind.
	Kind TaskKind
	// Targets are the workspace-relative targets the objective names. A target
	// list is part of the contract: evidence for a target outside this set
	// never substitutes for a target inside it.
	Targets []string
	// RequiresVerifier demands a verification verdict. A gate that is
	// provably NOT APPLICABLE (the language has no verification contract) is
	// recorded as such and is distinguishable from a pass.
	RequiresVerifier bool
	// RequiresObservation demands a workspace observation event — the evidence
	// that the model actually looked at the workspace rather than answered
	// from priors.
	RequiresObservation bool
	// RequiresResponse demands a non-empty delivered response.
	RequiresResponse bool
}

// RequiresMutation reports whether the contract demands a durable mutation.
func (c TaskContract) RequiresMutation() bool { return c.Kind.RequiresMutation() }

// targetsExist reports whether every declared target is present in the map.
func (c TaskContract) targetsExist(exists map[string]bool) bool {
	if len(c.Targets) == 0 {
		return false
	}
	for _, t := range c.Targets {
		if !exists[t] {
			return false
		}
	}
	return true
}

// targetsAbsent reports whether every declared target is absent from the map
// (an explicit absence observation, never a missing observation).
func (c TaskContract) targetsAbsent(absent map[string]bool) bool {
	if len(c.Targets) == 0 {
		return false
	}
	for _, t := range c.Targets {
		if !absent[t] {
			return false
		}
	}
	return true
}

// ── ObjectiveEvidence ───────────────────────────────────────────────────────
//
// NAMING NOTE. The Phase 14 specification calls the authority's input
// `ExecutionEvidence`. That identifier is already taken in this package by the
// sealed, immutable TERMINAL RECORD of one execution attempt (evidence.go) and
// the two carry DIFFERENT meaning (one is a runtime ledger entry, the other is
// a task-specific fact bundle). Renaming either would break the sealed-record
// API that the audit, evidence-event and presentation consumers already depend
// on, so the fact bundle is named `ObjectiveEvidence` and the sealed record
// keeps `ExecutionEvidence`. The authority consumes BOTH: the sealed record
// when the runtime has one (see SealedRecord) and the boundary facts otherwise.

type ObjectiveEvidence struct {
	// ── Boundary 1: PROVIDER (transport) ──────────────────────────────
	Provider ProviderState
	// FinishReason is the provider's terminal finish_reason, verbatim.
	FinishReason string
	// ProviderError is the invocation-level transport error, if any.
	ProviderError error

	// ── Boundary 2: ARTIFACT (parser) ─────────────────────────────────
	Artifact ArtifactState
	// ArtifactsParsed is the count of VALID artifacts extracted by the parser.
	// Prose, Markdown and an empty fence all count as ZERO.
	ArtifactsParsed int
	// PartialArtifact records that a truncated generation's delivered prefix was
	// preserved for token continuation.
	PartialArtifact bool

	// ── Boundary 3: MUTATION (filesystem) ─────────────────────────────
	Mutation MutationBoundaryState
	// MutatedFiles counts targets the boundary proved were applied AND
	// changed. A rolled-back write is NOT counted.
	MutatedFiles int
	// ObservedDeltaTargets names the declared targets whose filesystem delta the
	// boundary actually observed.
	ObservedDeltaTargets []string
	// TargetExists is the durable post-execution existence observation, keyed
	// by workspace-relative path. A missing key is an UNOBSERVED target, which
	// is never the same as an observed absence.
	TargetExists map[string]bool
	// TargetAbsent is the explicit post-execution absence observation, keyed by
	// workspace-relative path. Only DELETE contracts consume it.
	TargetAbsent map[string]bool

	// ── Boundary 4: VERIFICATION + RESPONSE CONTRACTS ─────────────────
	VerificationRan    bool
	VerificationPassed bool
	// VerificationSkipped reports that NO verification contract exists for the
	// target (semantically NOT APPLICABLE — distinct from a gate that ran and
	// failed). It never claims a pass.
	VerificationSkipped bool
	// WorkspaceObservations counts the workspace observation / AST parse events
	// this execution produced. It is the READ/REVIEW evidence of observation.
	WorkspaceObservations int
	// RepositoryObservations counts the workspace files the runtime admitted as
	// READ-ONLY investigation context for an objective whose target was
	// legitimately unknown (a targetless investigation / diagnosis).
	//
	// It is the observation evidence for a targetless read/review objective: the
	// runtime observed the repository and the model reasoned from that observed
	// material. It is EVIDENCE, never authority — it can satisfy the observation
	// obligation, and it can never bind a mutation target, authorize a write, or
	// stand in for the objective's own completion conditions.
	RepositoryObservations int
	// PreconditionSatisfied is a DETERMINISTIC PRE-EXECUTION determination that
	// the objective's target state already held before this execution. It
	// strengthens the recorded provenance; on its own it is NOT sufficient to
	// prove anything (existence is not satisfaction).
	PreconditionSatisfied bool
	// StructuralNoOpConfirmed is a deterministic structural-analysis verdict
	// that zero work was required for this objective (the executor's NO-OP
	// semantics gate). It is evidence, never a model claim, and it is the ONLY
	// licence for the IDEMPOTENT contract.
	StructuralNoOpConfirmed bool
	// ResponseProduced reports that the execution delivered a response.
	ResponseProduced bool
	// ResponseBytes is the delivered response length (0 = empty).
	ResponseBytes int

	// ── Control-plane facts ───────────────────────────────────────────
	// ApprovalPending reports that the mutation is held at a human gate.
	ApprovalPending bool
	// SealedRecord is the runtime's immutable terminal record when one exists.
	// When set it is authoritative over any derived field.
	SealedRecord *ExecutionEvidence

	// ── Boundary 5: OBJECTIVE (meaning, beyond execution shape) ───────
	//
	// Everything above is an EXECUTION fact: a transport spoke, a parser found
	// something, a filesystem changed, a gate ran. None of it says the user's
	// objective was achieved. These four fields carry the OUTCOME half.

	// Conditions are the authoritative completion conditions of the objective
	// lifecycle (objective_contract.go). When non-empty, EVERY condition must be
	// satisfied before the objective may be PROVEN.
	//
	// The authority RECOMPUTES each condition from the fields below and never
	// reads a caller-supplied status, so a caller cannot assert its own
	// homework. An empty condition set reproduces the pre-contract behaviour
	// exactly: no condition means no additional clause.
	Conditions []CompletionCondition
	// PostMutationObserved names declared targets the runtime RE-READ AFTER the
	// mutation landed. Requesting a target is not observing it, and a
	// pre-dispatch snapshot is not a post-mutation result.
	PostMutationObserved []string
	// DischargedRequirements names admitted requirements the runtime itself
	// attributed an observed execution fact to. A requirement is discharged by
	// evidence, never by the model saying so.
	DischargedRequirements []string
	// ClaimedRequirements names requirements the model CLAIMS it satisfied. They
	// are recorded so the trace can show a claim that carried no evidence; they
	// never count as discharge.
	ClaimedRequirements []string
}

// Mutated reports whether at least one declared target was durably transformed.
func (e ObjectiveEvidence) Mutated() bool { return e.Mutation.Durable() && e.MutatedFiles > 0 }

// VerifierVerdict is the truthful rendering of the verification boundary.
func (e ObjectiveEvidence) VerifierVerdict() string {
	switch {
	case e.VerificationRan && e.VerificationPassed:
		return "PASS"
	case e.VerificationSkipped:
		return "NOT_APPLICABLE"
	case e.VerificationRan:
		return "FAIL"
	default:
		return "NOT_RUN"
	}
}

// verifierSatisfied reports whether the verification requirement is met. A
// provably not-applicable gate satisfies the requirement the same way an
// untestable target's syntax is not a mutation failure — but the outcome
// records WHICH, so no projection can render "verified" for a skipped gate.
func (e ObjectiveEvidence) verifierSatisfied(required bool) bool {
	if !required {
		return true
	}
	return e.VerificationPassed || e.VerificationSkipped
}

// ── TaskContract derivation ─────────────────────────────────────────────────

// TaskClassification is the deterministic input set of DeriveTaskContract. It
// carries only facts the runtime already owns; it carries no verdict.
type TaskClassification struct {
	// Intent is the classified intent label ("modification", "explanation", …).
	Intent string
	// Objective is the user's own objective text. The verb scan reads the
	// objective, never the intent label: an unclassified label must not decide
	// the contract kind.
	Objective string
	// RequiresMutation reports whether the objective demands a workspace change.
	RequiresMutation bool
	// Targets are the resolved workspace-relative targets.
	Targets []string
	// TargetsExistedBefore records, per target, whether the target was present
	// BEFORE execution. It is the only input that can license the IDEMPOTENT
	// contract: "the objective was already satisfied on disk".
	TargetsExistedBefore map[string]bool
	// TargetsChanged records, per target, whether the boundary observed a
	// filesystem delta for it.
	TargetsChanged map[string]bool
	// ArtifactShape is the artifact contract the attempt was dispatched under
	// ("create_file", "replace_block", "search_replace", "full_file", …).
	ArtifactShape string
	// DeleteRequested reports that the objective explicitly asks for a removal.
	DeleteRequested bool
	// ResponseProduced reports that a delivered response exists.
	ResponseProduced bool
	// PreSatisfied is a DETERMINISTIC PRE-EXECUTION determination that the
	// objective's target state already holds. It strengthens the recorded
	// provenance but is NOT sufficient on its own to license the IDEMPOTENT
	// contract: existence is not satisfaction.
	PreSatisfied bool
	// StructuralNoOpConfirmed is a deterministic POST-EXECUTION structural
	// analysis verdict (the executor's NO-OP semantics gate) that zero work
	// was required for this objective. It is evidence, never a model claim, and
	// it is the ONLY licence for the IDEMPOTENT contract.
	StructuralNoOpConfirmed bool
}

// The creation and deletion verb vocabularies are owned by the strategy layer
// (semantics.go), which is the canonical semantic boundary. They used to be
// declared here as a SECOND copy, and the two had already drifted: "implement"
// was a creation verb here and unknown to the operation classifier, so the same
// request could be judged a mutation by the contract and read as unreadable by
// the gateway. One owner is the whole fix.

// deletionLookahead is how many tokens after the verb a declared target may
// appear and still bind the verb to it. It admits the natural object phrases
// ("delete note.txt", "remove the file big.go", "delete @index.html") while
// rejecting a distant object separated by content words
// ("remove every X comment from @big.go").
const deletionLookahead = 3

// requestsDeletionOf reports whether the objective asks for a DECLARED TARGET to
// be removed, as opposed to asking for content inside it to change.
//
// The binding rule is positional, not lexical: a removal verb must be followed
// within `deletionLookahead` tokens by one of the declared targets. That is what
// separates "remove note.txt" (DELETE) from "remove the redundant paragraphs
// from @index.html" (PATCH), and it is why the two contracts are never
// confused for a phrase that carries both verbs.
func requestsDeletionOf(objective string, targets []string) bool {
	if len(targets) == 0 {
		return false
	}
	lower := strings.ToLower(objective)
	lower = strings.ReplaceAll(lower, "@", " ")
	fields := strings.Fields(lower)
	for i, f := range fields {
		verb := strings.Trim(f, ".,:;!?\"'()")
		if !strategy.DeletionVerbs()[verb] {
			continue
		}
		for j := i + 1; j < len(fields) && j <= i+deletionLookahead; j++ {
			if matchesTarget(fields[j], targets) {
				return true
			}
		}
	}
	return false
}

// matchesTarget reports whether an objective token names one of the declared
// targets (bare, path-qualified or as a filename component).
func matchesTarget(token string, targets []string) bool {
	candidate := strings.Trim(token, ".,:;!?\"'()")
	candidate = strings.TrimPrefix(candidate, "./")
	if candidate == "" {
		return false
	}
	for _, t := range targets {
		base := strings.ToLower(strings.TrimPrefix(t, "./"))
		if base == "" {
			continue
		}
		if candidate == base || strings.HasSuffix(base, "/"+candidate) {
			return true
		}
	}
	return false
}

// reviewIntents are the read-only intents whose contract requires a workspace
// observation event and a response — the reviewer must have LOOKED.
var reviewIntents = map[string]bool{
	"verification":  true,
	"investigation": true,
	"debugging":     true,
	"planning":      true,
}

// DeriveTaskContract resolves the ONE canonical task contract of an execution
// lifecycle. It is deterministic and total: identical classification inputs
// always yield an identical contract.
//
// The rules, in precedence order:
//
//  1. a deletion objective                 → DELETE
//  2. a mutation objective that NAMES a target with no durable pre-existing
//     content (or whose artifact contract is a creation shape) → CREATE
//  3. a mutation objective that applied no delta AND carries a structural
//     confirmation that its target state was already satisfied
//     (pre-execution determination or the executor's NO-OP structural verdict)
//     → IDEMPOTENT
//  4. any other mutation objective        → PATCH
//  5. a review-classified read-only intent → REVIEW
//  6. any other read-only intent           → READ
//
// A mutation objective that names NO target (target DEFERRED) takes rule 4, not
// rule 2: an unresolved target is not evidence of a new artifact. Discovery
// resolves the target later and the execution-shape kind may then be re-derived
// against the concrete target; until then it is a modification, never a CREATE.
//
// Rule 3 is the ONLY path to IDEMPOTENT, and it REQUIRES a structural
// confirmation. "The file existed and nothing changed" is execution inertia, not
// an idempotent objective: without an explicit pre-execution determination or
// the executor's deterministic NO-OP structural verdict, a zero-mutation
// mutation objective is UNSUBSTANTIATED — never PROVEN.
func DeriveTaskContract(in TaskClassification) TaskContract {
	targets := append([]string(nil), in.Targets...)
	contract := TaskContract{Targets: targets}
	// The verb scan reads the OBJECTIVE, not the intent label; an unclassified
	// label must not silently decide the contract kind. Matching goes through
	// the canonical token matcher so "write" cannot be read inside "rewrite" —
	// that boundary used to be re-implemented locally here and hand-rolled there
	// in the classifier, and two hand-rolled boundaries eventually disagree.

	// 1 — DELETE.
	if in.RequiresMutation && (in.DeleteRequested || requestsDeletionOf(in.Objective, targets)) {
		contract.Kind = TaskDelete
		contract.RequiresVerifier = true
		return contract
	}

	if in.RequiresMutation {
		// 2 — CREATE: the objective NAMES a target AND that target has no
		// durable pre-existing content, or the dispatched artifact contract was
		// itself a creation shape.
		//
		// A DECLARED target is a precondition for CREATE. When the objective
		// names no target at all, its target is DEFERRED: the runtime has not
		// discovered it yet. The absence of a resolved target is NOT evidence
		// that the artifact is new, and reading it as CREATE is precisely the
		// defect this guard removes — it invents a creation objective (with an
		// empty creation scope) out of an unresolved one. A targetless mutation
		// objective stays a PATCH, a MODIFY whose concrete target discovery must
		// resolve, and is never relabelled. See objective_operation.go.
		if len(targets) > 0 {
			// `TargetsExistedBefore` nil means the runtime made NO pre-execution
			// observation; an EMPTY map means it observed that nothing existed.
			// Only the latter is positive evidence of a new artifact. Treating
			// an unobserved target as new is the same category error as treating
			// an unresolved target as new, one layer down.
			noExistingContent := in.TargetsExistedBefore != nil &&
				!anyTargetExisted(in.TargetsExistedBefore, targets)
			askedForNewFile := strategy.ContainsPhrase(in.Objective, strategy.CreationVerbs()) &&
				!allTargetsExisted(in.TargetsExistedBefore, targets)
			if noExistingContent || askedForNewFile {
				contract.Kind = TaskCreate
				contract.RequiresVerifier = true
				return contract
			}
			// A creation SHAPE is authoritative and outranks every other
			// mutation rule: a creation contract has no existing content to
			// anchor a bounded patch against, so relabelling it — by recovery
			// or by evidence — would ask the model for a patch against a file
			// that does not exist. It is only meaningful once the objective
			// names the file being created; a targetless creation shape is a
			// deferred target, not an invention.
			if creationShape(in.ArtifactShape) {
				contract.Kind = TaskCreate
				contract.RequiresVerifier = true
				return contract
			}
		}
		// 3 — IDEMPOTENT: every declared target already existed BEFORE
		// execution, the boundary applied no delta, AND a deterministic
		// structural verdict confirms the objective was already satisfied.
		//
		// The structural verdict is REQUIRED, not one option among several.
		// "The file existed and nothing changed" is indistinguishable from
		// execution inertia without it, and accepting the former would
		// rubber-stamp exactly the inertia the phase exists to stop.
		if allTargetsExisted(in.TargetsExistedBefore, targets) &&
			!anyTargetChanged(in.TargetsChanged, targets) &&
			in.StructuralNoOpConfirmed {
			contract.Kind = TaskIdempotent
			contract.RequiresVerifier = true
			return contract
		}
		// 4 — PATCH.
		contract.Kind = TaskPatch
		contract.RequiresVerifier = true
		return contract
	}

	// 5/6 — read-only contracts: the deliverable is the delivered response, and
	// the evidence that the model actually looked at the workspace.
	contract.RequiresResponse = true
	contract.RequiresObservation = true
	if reviewIntents[strings.ToLower(strings.TrimSpace(in.Intent))] {
		contract.Kind = TaskReview
		return contract
	}
	contract.Kind = TaskRead
	return contract
}

// creationShape reports whether the dispatched artifact contract was itself a
// creation shape. A creation contract is structurally impossible to relabel as
// a bounded patch, so it stays a CREATE for the whole lifecycle.
func creationShape(shape string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(shape)), "create")
}

func anyTargetExisted(before map[string]bool, targets []string) bool {
	for _, t := range targets {
		if before[t] {
			return true
		}
	}
	return false
}

func allTargetsExisted(before map[string]bool, targets []string) bool {
	if len(targets) == 0 {
		return false
	}
	for _, t := range targets {
		if !before[t] {
			return false
		}
	}
	return true
}

func anyTargetChanged(changed map[string]bool, targets []string) bool {
	for _, t := range targets {
		if changed[t] {
			return true
		}
	}
	return false
}

// ── ObjectiveCompletionAuthority ─────────────────────────────────────────────

// ObjectiveCompletionAuthority is the single authority that decides whether an
// execution PROVED its objective. It is a pure, deterministic reducer over
// (TaskContract, ObjectiveEvidence): it reads no globals, mutates nothing, and
// holds no completion authority of its own. The zero value is ready to use.
type ObjectiveCompletionAuthority struct{}

// NewObjectiveCompletionAuthority returns a ready-to-use authority.
func NewObjectiveCompletionAuthority() *ObjectiveCompletionAuthority {
	return &ObjectiveCompletionAuthority{}
}

// Evaluate reduces the evidence against the contract into the ONE canonical
// objective outcome. The rule precedence is deliberate:
//
//  0. a human gate is holding the mutation  → REQUIRES_AUTHORIZATION
//  1. a sealed record says FAILED / tainted  → FAILED (positive contradiction)
//  2. the generation was truncated           → UNSUBSTANTIATED (CONTINUING)
//  3. the provider refused or errored        → FAILED
//  4. the OBJECTIVE completion contract      → every condition must hold
//  5. the execution-shape contract           → per-kind
//  6. no rule was violated and no clause
//     was satisfied                          → UNSUBSTANTIATED (fail-closed)
//
// Clause 4 is what makes "a valid mutation happened" and "the user's objective
// has been satisfied" different states. It runs BEFORE the per-kind clauses so
// a lifecycle can never reach PROVEN by satisfying the mutation shape alone
// while leaving the objective's own conditions unmet.
//
// The function is TOTAL: it always returns an outcome, and it can never return
// PROVEN without satisfying every clause of both contracts.
func (a *ObjectiveCompletionAuthority) Evaluate(contract TaskContract, ev ObjectiveEvidence) ObjectiveOutcome {
	// ── 0 — a human gate owns the decision ──────────────────────────
	if ev.ApprovalPending {
		return ObjectiveRequiresAuthorization
	}

	// ── 1 — the sealed record is authoritative when it exists ──────
	if rec := ev.SealedRecord; rec != nil {
		switch {
		case rec.Outcome() == EvidenceFailed || rec.Outcome() == EvidenceAbortedOCC:
			return ObjectiveFailed
		case rec.Outcome() == EvidenceCancelled:
			// A clean human cancellation is not a task failure; the objective is
			// simply unproven. Completion is refused either way.
			return ObjectiveUnsubstantiated
		case rec.Outcome() == EvidenceRequiresReview:
			return ObjectiveUnsubstantiated
		case rec.Mutations().Tainted:
			// Partial, non-durable state is never proven truth.
			return ObjectiveUnsubstantiated
		}
	}

	// ── 2 — truncation is CONTINUING, never completion ─────────────
	if ev.Artifact == ArtifactContinuing || ev.PartialArtifact ||
		NormalizeFinishReason(ev.FinishReason) == CanonicalOutputExhausted {
		return ObjectiveUnsubstantiated
	}

	// ── 3 — a provider refusal / transport error is a failure ──────
	switch ev.Provider {
	case ProviderRefused:
		return ObjectiveFailed
	default:
		if ev.ProviderError != nil {
			return ObjectiveFailed
		}
	}

	// ── 4 — the OBJECTIVE completion contract ──────────────────────
	// Every completion condition the runtime authored must hold. This runs
	// before the per-kind execution clauses so an execution shape can never
	// stand in for the objective it was serving.
	if unmet := UnmetCondition(ev); unmet != nil {
		return ObjectiveUnsubstantiated
	}

	// ── 5 — the contract's own clauses ─────────────────────────────
	switch contract.Kind {
	case TaskCreate:
		return a.evaluateCreate(contract, ev)
	case TaskPatch:
		return a.evaluatePatch(contract, ev)
	case TaskDelete:
		return a.evaluateDelete(contract, ev)
	case TaskRead:
		return a.evaluateRead(contract, ev)
	case TaskReview:
		return a.evaluateReview(contract, ev)
	case TaskIdempotent:
		return a.evaluateIdempotent(contract, ev)
	default:
		// An unknown contract kind can never prove anything.
		return ObjectiveUnsubstantiated
	}
}

// UnmetCondition returns the FIRST completion condition the evidence fails, or
// nil when every condition holds (including the vacuous case of no conditions).
//
// It is the single authority for the objective half of the contract, and it is
// exposed so the continuation path, the trace and the tests all read the SAME
// computation rather than three lookalikes.
func UnmetCondition(ev ObjectiveEvidence) *CompletionCondition {
	if len(ev.Conditions) == 0 {
		return nil
	}
	for _, c := range ReduceConditions(ev.Conditions, ev) {
		if !c.Satisfied() {
			unmet := c
			return &unmet
		}
	}
	return nil
}

// SatisfiedConditions returns the recomputed condition states for a lifecycle.
// The returned statuses are RUNTIME-COMPUTED; the authored statuses on the input
// are discarded.
func SatisfiedConditions(ev ObjectiveEvidence) []CompletionCondition {
	return ReduceConditions(ev.Conditions, ev)
}

// evaluateCreate — CREATE: artifact parsed + mutation applied + durable target
// exists + verifier PASS.
func (a *ObjectiveCompletionAuthority) evaluateCreate(contract TaskContract, ev ObjectiveEvidence) ObjectiveOutcome {
	if ev.Artifact != ArtifactProduced || ev.ArtifactsParsed < 1 {
		return ObjectiveUnsubstantiated
	}
	if !ev.Mutated() {
		return ObjectiveUnsubstantiated
	}
	if !contract.targetsExist(ev.TargetExists) {
		return ObjectiveUnsubstantiated
	}
	if !ev.verifierSatisfied(contract.RequiresVerifier) {
		return ObjectiveUnsubstantiated
	}
	return ObjectiveProven
}

// evaluatePatch — PATCH: valid target + mutation applied + the EXPECTED
// filesystem delta observed on a DECLARED target + verifier PASS.
//
// "Applied" is not "changed": a no-op apply leaves the bytes identical, and an
// unchanged file is not a delivered patch. The delta must also land on a target
// the contract declared — an out-of-scope change never substitutes for it.
func (a *ObjectiveCompletionAuthority) evaluatePatch(contract TaskContract, ev ObjectiveEvidence) ObjectiveOutcome {
	if ev.Artifact != ArtifactProduced || ev.ArtifactsParsed < 1 {
		return ObjectiveUnsubstantiated
	}
	if !ev.Mutated() {
		return ObjectiveUnsubstantiated
	}
	if !declaresTarget(contract.Targets, ev.ObservedDeltaTargets) {
		return ObjectiveUnsubstantiated
	}
	// "Valid target": a patch against a target that is no longer there is not a
	// delivered patch. The delta and the existence observation are independent
	// facts and both are required.
	if !contract.targetsExist(ev.TargetExists) {
		return ObjectiveUnsubstantiated
	}
	if !ev.verifierSatisfied(contract.RequiresVerifier) {
		return ObjectiveUnsubstantiated
	}
	return ObjectiveProven
}

// evaluateDelete — DELETE: mutation applied + target absent + verifier PASS.
func (a *ObjectiveCompletionAuthority) evaluateDelete(contract TaskContract, ev ObjectiveEvidence) ObjectiveOutcome {
	if !ev.Mutated() {
		return ObjectiveUnsubstantiated
	}
	if !contract.targetsAbsent(ev.TargetAbsent) {
		return ObjectiveUnsubstantiated
	}
	if !ev.verifierSatisfied(contract.RequiresVerifier) {
		return ObjectiveUnsubstantiated
	}
	return ObjectiveProven
}

// responseSatisfied reports whether the execution DELIVERED an answer: either
// response content, or a deterministic structural verdict about the workspace.
// A structural verdict IS a complete delivery for a read/review objective —
// "nothing here needs changing, and here is the evidence" is an answer, not
// silence. What is NOT an answer is an empty execution that merely stopped.
func responseSatisfied(ev ObjectiveEvidence) bool {
	return ev.ResponseProduced || ev.StructuralNoOpConfirmed
}

// observedWorkspace reports whether the runtime holds authoritative evidence
// that it actually observed the workspace for this objective: either a declared
// target's bytes were read/projected, or a targetless investigation admitted
// bounded repository material. It is the single predicate the READ/REVIEW
// observation obligation consults, so the two evidence kinds can never be
// satisfied by different, disagreeing rules.
func observedWorkspace(ev ObjectiveEvidence) bool {
	return ev.WorkspaceObservations >= 1 || ev.RepositoryObservations >= 1
}

// evaluateRead — READ: a required workspace observation event was produced AND
// the response contract is satisfied. No mutation is required or permitted.
func (a *ObjectiveCompletionAuthority) evaluateRead(contract TaskContract, ev ObjectiveEvidence) ObjectiveOutcome {
	if contract.RequiresObservation && !observedWorkspace(ev) {
		return ObjectiveUnsubstantiated
	}
	if contract.RequiresResponse && !responseSatisfied(ev) {
		return ObjectiveUnsubstantiated
	}
	return ObjectiveProven
}

// evaluateReview — REVIEW: the observation obligation is STRICTER than a plain
// read. A review that produced no observation event (no workspace read, no AST
// parse) is a review of nothing; the response contract alone is insufficient.
func (a *ObjectiveCompletionAuthority) evaluateReview(contract TaskContract, ev ObjectiveEvidence) ObjectiveOutcome {
	if contract.RequiresObservation && !observedWorkspace(ev) {
		return ObjectiveUnsubstantiated
	}
	if contract.RequiresResponse && !responseSatisfied(ev) {
		return ObjectiveUnsubstantiated
	}
	return ObjectiveProven
}

// evaluateIdempotent — IDEMPOTENT: the target state was ALREADY satisfied
// before execution (a deterministic structural verdict) + verifier PASS. The
// mutation count is legitimately zero: that is the whole point of the contract.
//
// The structural confirmation is REQUIRED. Without it this is NOT an idempotent
// objective — it is a mutation request that delivered nothing, which is
// unsubstantiated. A zero mutation count is never, by itself, a completion
// proof, and neither is the bare observation that the file already existed.
//
// A confirmed structural NO-OP verdict IS the verification for this contract:
// there is no applied change for a command gate to grade, and the deterministic
// structural analysis over the assigned slice is the stronger evidence. It is
// recorded as such so a projection can say "structurally confirmed" instead of
// implying a command gate ran.
func (a *ObjectiveCompletionAuthority) evaluateIdempotent(contract TaskContract, ev ObjectiveEvidence) ObjectiveOutcome {
	if !ev.StructuralNoOpConfirmed {
		return ObjectiveUnsubstantiated
	}
	return ObjectiveProven
}

// declaresTarget reports whether the observed delta names at least one target
// the contract declared. An empty contract target set can never be satisfied by
// an undeclared delta.
func declaresTarget(contractTargets, observed []string) bool {
	if len(contractTargets) == 0 || len(observed) == 0 {
		return false
	}
	for _, o := range observed {
		for _, c := range contractTargets {
			if o == c {
				return true
			}
		}
	}
	return false
}

// ── Outcome explanation ─────────────────────────────────────────────────────

// ObjectiveEvaluation pairs the canonical outcome with the deterministic,
// user-readable justification the authority used. The reason is computed from
// observed facts only — it never speculates and never blames.
type ObjectiveEvaluation struct {
	Outcome ObjectiveOutcome
	// Reason is "" for PROVEN (the contract was satisfied in full).
	Reason string
	// Clause names the contract clause that decided the outcome ("" for PROVEN).
	Clause string
	// State is the MEANING boundary projection of the outcome.
	State ObjectiveState
}

// AuthorizeObjective is the one-call form of Evaluate that also explains
// itself. Callers that need a human-readable refusal should prefer it; callers
// that only branch should use Evaluate.
func AuthorizeObjective(contract TaskContract, ev ObjectiveEvidence) ObjectiveEvaluation {
	outcome := (&ObjectiveCompletionAuthority{}).Evaluate(contract, ev)
	return evaluationFor(contract, ev, outcome)
}

// Authorize is the method form of AuthorizeObjective.
func (a *ObjectiveCompletionAuthority) Authorize(contract TaskContract, ev ObjectiveEvidence) ObjectiveEvaluation {
	return evaluationFor(contract, ev, a.Evaluate(contract, ev))
}

// evaluationFor derives the deterministic reason for a non-PROVEN outcome.
func evaluationFor(contract TaskContract, ev ObjectiveEvidence, outcome ObjectiveOutcome) ObjectiveEvaluation {
	e := ObjectiveEvaluation{Outcome: outcome, State: outcome.ObjectiveState()}
	if outcome.Proves() {
		return e
	}
	switch outcome {
	case ObjectiveRequiresAuthorization:
		e.Clause = "human_gate"
		e.Reason = "the mutation is held at a human gate — the decision belongs to the human"
	case ObjectiveFailed:
		e.Clause = "evidence_failed"
		switch {
		case ev.SealedRecord != nil && ev.SealedRecord.Outcome() == EvidenceFailed:
			e.Reason = fmt.Sprintf("sealed execution evidence outcome is %s, not COMMITTED", ev.SealedRecord.Outcome())
		case ev.Provider == ProviderRefused:
			e.Reason = "the provider refused or filtered the generation"
		case ev.ProviderError != nil:
			e.Reason = "the provider invocation failed: " + ev.ProviderError.Error()
		default:
			e.Reason = "the execution evidence contradicts the task contract"
		}
	case ObjectiveUnsubstantiated:
		e.Clause, e.Reason = unsatisfiedClause(contract, ev)
	default:
		e.Clause = "unknown"
		e.Reason = "unrecognized objective outcome"
	}
	return e
}

// unsatisfiedClause names the FIRST contract clause the evidence fails. The
// order mirrors Evaluate so the reason and the verdict can never disagree.
//
// The OBJECTIVE completion contract is reported before the execution-shape
// clauses, matching Evaluate's precedence: "the mutation was the right shape
// but the objective's own conditions were not met" is the truthful reason, and
// it is the reason an operator needs.
func unsatisfiedClause(contract TaskContract, ev ObjectiveEvidence) (string, string) {
	if ev.SealedRecord != nil {
		switch {
		case ev.SealedRecord.Outcome() == EvidenceCancelled:
			return "execution_cancelled", "the execution was cancelled before the objective could be proven"
		case ev.SealedRecord.Outcome() == EvidenceRequiresReview:
			return "sealed_requires_review", "the sealed evidence is held for human review, not committed as truth"
		case ev.SealedRecord.Mutations().Tainted:
			return "mutations_tainted", "the sealed evidence carries tainted mutations; partial state is not truth"
		}
	}
	if ev.Artifact == ArtifactContinuing || ev.PartialArtifact ||
		NormalizeFinishReason(ev.FinishReason) == CanonicalOutputExhausted {
		return "artifact_continuing", fmt.Sprintf(
			"the provider stream ended at finish_reason=%s; the partial artifact is preserved for continuation and is not a completed artifact",
			orUnknown(ev.FinishReason))
	}
	if unmet := UnmetCondition(ev); unmet != nil {
		return unmetConditionClause(*unmet)
	}
	switch contract.Kind {
	case TaskCreate:
		switch {
		case ev.Artifact != ArtifactProduced || ev.ArtifactsParsed < 1:
			return "artifact_missing", "no valid artifact was parsed from the completed provider stream"
		case !ev.Mutated():
			return "mutation_missing", "no durable filesystem mutation was applied"
		case !contract.targetsExist(ev.TargetExists):
			return "target_absent", "the declared target does not exist on disk after the apply"
		}
	case TaskPatch:
		switch {
		case ev.Artifact != ArtifactProduced || ev.ArtifactsParsed < 1:
			return "artifact_missing", "no valid artifact was parsed from the completed provider stream"
		case !ev.Mutated():
			return "mutation_missing", "no durable filesystem mutation was applied"
		case !declaresTarget(contract.Targets, ev.ObservedDeltaTargets):
			return "delta_unobserved", "the expected filesystem delta was not observed on any declared target"
		case !contract.targetsExist(ev.TargetExists):
			return "target_absent", "the declared target does not exist on disk after the apply"
		}
	case TaskDelete:
		switch {
		case !ev.Mutated():
			return "mutation_missing", "no durable filesystem mutation was applied"
		case !contract.targetsAbsent(ev.TargetAbsent):
			return "target_present", "the declared target still exists after the delete mutation"
		}
	case TaskRead, TaskReview:
		if contract.RequiresObservation && !observedWorkspace(ev) {
			return "observation_missing", "no workspace observation event and no repository investigation evidence was produced for a read/review objective"
		}
		if contract.RequiresResponse && !responseSatisfied(ev) {
			return "response_empty", "the execution produced no response and no structural verdict"
		}
	case TaskIdempotent:
		// The structural confirmation is the whole clause. Without it the run
		// asked for a change and delivered none: execution inertia, not an
		// already-satisfied objective.
		if !ev.StructuralNoOpConfirmed {
			return "precondition_unproven", "no structural confirmation that the objective was already satisfied before execution"
		}
	}
	if !ev.verifierSatisfied(contract.RequiresVerifier) {
		return "verifier_" + strings.ToLower(ev.VerifierVerdict()),
			"the verification gate reported " + ev.VerifierVerdict()
	}
	if contract.RequiresMutation() && !ev.Mutated() {
		return "mutation_missing", "the contract requires a durable filesystem mutation and none was observed"
	}
	return "unproven", "the evidence does not satisfy the task contract"
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}

// unmetConditionClause renders the deterministic clause/reason pair for a
// completion condition the evidence failed.
//
// The clause label is DERIVED FROM THE OBLIGATION, not from the condition's
// authored identity, so a trace cannot be made to claim a specific, more
// flattering clause than the one that actually decided.
func unmetConditionClause(c CompletionCondition) (string, string) {
	detail := conditionDetail(c)
	switch c.Obligation {
	case ObligationScopeMutated:
		return "objective_scope_unmutated",
			fmt.Sprintf("completion condition %q is unmet: %s — a valid mutation of the right shape is not the objective being satisfied", c.ID, detail)
	case ObligationPostMutationReinspected:
		return "objective_result_uninspected",
			fmt.Sprintf("completion condition %q is unmet: %s — the resulting state was never inspected, so no claim about it is admissible", c.ID, detail)
	case ObligationRequirementDischarged:
		return "objective_requirement_undischarged",
			fmt.Sprintf("completion condition %q is unmet: %s — the model proposed this requirement and the runtime observed no evidence discharging it", c.ID, detail)
	case ObligationObserved:
		return "objective_scope_unobserved",
			fmt.Sprintf("completion condition %q is unmet: %s", c.ID, detail)
	case ObligationTargetExists:
		return "objective_target_unobserved",
			fmt.Sprintf("completion condition %q is unmet: %s", c.ID, detail)
	case ObligationScopeAbsent:
		return "objective_target_still_present",
			fmt.Sprintf("completion condition %q is unmet: %s", c.ID, detail)
	case ObligationIntegrityHeld:
		return "objective_integrity_" + strings.ToLower(string(c.VerificationState)),
			fmt.Sprintf("completion condition %q is unmet: %s", c.ID, detail)
	case ObligationResponseDelivered:
		return "objective_response_missing",
			fmt.Sprintf("completion condition %q is unmet: %s", c.ID, detail)
	default:
		return "objective_condition_unmet",
			fmt.Sprintf("completion condition %q is unmet: %s", c.ID, detail)
	}
}

// conditionDetail renders WHY one condition failed, from its recomputed
// verification state and target scope.
func conditionDetail(c CompletionCondition) string {
	scope := strings.Join(c.Targets, ", ")
	if scope == "" {
		scope = "(no declared target)"
	}
	if c.VerificationState == VerifyClaimedOnly {
		return "claimed by the model with no observed evidence, target scope " + scope
	}
	switch c.Obligation {
	case ObligationRequirementDischarged:
		return "requirement " + c.RequirementID + " has no discharging observation"
	case ObligationScopeMutated:
		return "no durable delta observed on " + scope
	case ObligationPostMutationReinspected:
		return "no post-mutation re-read of " + scope
	case ObligationObserved:
		return "no workspace observation event for " + scope
	case ObligationTargetExists:
		return "no durable existence observation for " + scope
	case ObligationScopeAbsent:
		return "no explicit absence observation for " + scope
	case ObligationIntegrityHeld:
		return "the verification gate reported " + string(c.VerificationState)
	case ObligationResponseDelivered:
		return "no response and no deterministic structural verdict"
	default:
		return "the runtime could not observe the obligation satisfied (" + string(c.VerificationState) + ")"
	}
}

// AuthorizeCompletion is the fail-closed convenience wrapper: it returns the
// canonical outcome AND the sentinel error the Driver refuses to complete on.
// PROVEN returns (ObjectiveProven, nil). Every other outcome returns its own
// sentinel so a caller can branch on the exact failure without string
// matching.
func AuthorizeCompletion(contract TaskContract, ev ObjectiveEvidence) (ObjectiveOutcome, error) {
	outcome := (&ObjectiveCompletionAuthority{}).Evaluate(contract, ev)
	switch outcome {
	case ObjectiveProven:
		return outcome, nil
	case ObjectiveFailed:
		return outcome, ErrObjectiveFailed
	case ObjectiveRequiresAuthorization:
		return outcome, ErrObjectiveRequiresAuthorization
	default:
		return outcome, ErrObjectiveUnsubstantiated
	}
}

// Authorize is the method form of AuthorizeCompletion.
func (a *ObjectiveCompletionAuthority) AuthorizeCompletion(contract TaskContract, ev ObjectiveEvidence) (ObjectiveOutcome, error) {
	outcome := a.Evaluate(contract, ev)
	switch outcome {
	case ObjectiveProven:
		return outcome, nil
	case ObjectiveFailed:
		return outcome, ErrObjectiveFailed
	case ObjectiveRequiresAuthorization:
		return outcome, ErrObjectiveRequiresAuthorization
	default:
		return outcome, ErrObjectiveUnsubstantiated
	}
}

// ── Boundary fact assembly ──────────────────────────────────────────────────

// ObserveProviderState projects an invocation's transport facts onto the
// provider boundary. It is the ONLY place a provider fact is turned into a
// lifecycle state, so a caller can never invent a DONE from an absent field.
func ObserveProviderState(finishReason string, calls int, err error) ProviderState {
	switch {
	case err != nil:
		return ProviderRefused
	case calls <= 0:
		return ProviderPending
	case NormalizeFinishReason(finishReason) == CanonicalProviderRefusal:
		return ProviderRefused
	case NormalizeFinishReason(finishReason) == CanonicalOutputExhausted:
		// The provider DID stop emitting — it is DONE as a transport fact. The
		// objective is nonetheless unproven, and Evaluate says so via the
		// artifact/truncation clause. Keeping the two facts separate is the
		// entire point of the four-state split.
		return ProviderDone
	case NormalizeFinishReason(finishReason) == CanonicalComplete:
		return ProviderDone
	default:
		return ProviderStreaming
	}
}

// ObserveArtifactState projects the parser's findings onto the artifact
// boundary. A prose-only payload over a COMPLETE stream is ArtifactNone with
// zero parsed artifacts — never ArtifactProduced.
func ObserveArtifactState(finishReason string, parsed int, partial bool) ArtifactState {
	if partial || NormalizeFinishReason(finishReason) == CanonicalOutputExhausted {
		return ArtifactContinuing
	}
	if parsed > 0 {
		return ArtifactProduced
	}
	return ArtifactNone
}

// ── Evidence assembly from an ExecutionResult ───────────────────────────────

// TargetPreState is the PRE-EXECUTION target state the runtime observed. It is
// captured before dispatch and is the ONLY input that can license the
// IDEMPOTENT contract — a completion claim about "already done" needs proof
// that the state was already done, not merely that nothing changed afterwards.
type TargetPreState struct {
	// Existed records, per target, whether the target was present on disk
	// BEFORE this execution started.
	Existed map[string]bool
}

// ObjectiveEvidenceFromResult assembles the task-specific evidence bundle from
// one terminal execution result plus the pre-execution target state.
//
// It is the ONLY place the four lifecycle boundaries are derived from an
// ExecutionResult, so a projection, a recovery matrix and the authority all
// read the same numbers. The derivation is total and fails closed: an absent
// field is an unobserved fact, never a favourable one.
func ObjectiveEvidenceFromResult(res *ExecutionResult, pre TargetPreState) ObjectiveEvidence {
	ev := ObjectiveEvidence{
		Provider:              ProviderPending,
		Artifact:              ArtifactNone,
		Mutation:              FilesystemNone,
		TargetExists:          map[string]bool{},
		TargetAbsent:          map[string]bool{},
		PreconditionSatisfied: preSatisfied(pre, res),
	}
	if res == nil {
		return ev
	}

	// ── Boundary 1: PROVIDER ─────────────────────────────────────────
	finishReason := terminalFinishReason(res)
	invocations := res.ModelCalls
	if res.Proof != nil && len(res.Proof.ModelInvocations) > 0 {
		invocations = res.Proof.ModelInvocations
	}
	// A transport failure is only a PROVIDER-boundary failure when the request
	// produced no invocation record at all. A schema rejection that consumed a
	// billed invocation is an ARTIFACT-boundary fact, not a transport one.
	transportCalls := len(invocations)
	if transportCalls == 0 && res.Err != nil {
		transportCalls = 1
	}
	ev.FinishReason = finishReason
	ev.Provider = ObserveProviderState(finishReason, transportCalls, nil)
	if transportCalls == 1 && len(invocations) == 0 && res.Err != nil {
		ev.ProviderError = res.Err
	}

	// ── Boundary 2: ARTIFACT ─────────────────────────────────────────
	parsed := parsedArtifacts(res)
	partial := hasPartialCandidate(res)
	ev.ArtifactsParsed = parsed
	ev.PartialArtifact = partial
	ev.Artifact = ObserveArtifactState(finishReason, parsed, partial)

	// ── Boundary 3: MUTATION (filesystem) ────────────────────────────
	ev.Mutation, ev.MutatedFiles, ev.ObservedDeltaTargets = mutationBoundary(res)
	targets := res.Targets
	if res.Proof != nil && len(res.Proof.Targets) > 0 {
		targets = res.Proof.Targets
	}
	for _, t := range targets {
		ev.TargetExists[t] = true
	}
	// A DELETE contract is satisfied by an explicit absence observation: the
	// caller supplies it from the filesystem, never from the sealed record.
	for _, t := range targets {
		if !ev.TargetExists[t] {
			ev.TargetAbsent[t] = true
		}
	}

	// ── Boundary 4: VERIFICATION + RESPONSE ──────────────────────────
	report := res.Verification
	if res.Proof != nil && (len(res.Proof.Verification.Results) > 0 || res.Proof.Verification.Skipped) {
		report = res.Proof.Verification
	}
	ev.VerificationRan = !report.Skipped && len(report.Results) > 0
	ev.VerificationPassed = report.Passed
	ev.VerificationSkipped = report.Skipped
	if res.Proof != nil {
		ev.WorkspaceObservations = res.Proof.WorkspaceObservations
		ev.RepositoryObservations = res.Proof.RepositoryObservations
		ev.StructuralNoOpConfirmed = res.Proof.Outcome == OutcomeNoOpObjectiveSatisfied
	}
	ev.ResponseProduced = res.Content != ""
	ev.ResponseBytes = len(res.Content)
	ev.ApprovalPending = res.PendingPatchID != ""
	ev.SealedRecord = res.Evidence
	return ev
}

// preSatisfied reports whether every declared target was ALREADY present before
// the execution ran. It is a structural precondition, not a claim.
func preSatisfied(pre TargetPreState, res *ExecutionResult) bool {
	if len(pre.Existed) == 0 || res == nil {
		return false
	}
	targets := res.Targets
	if res.Proof != nil && len(res.Proof.Targets) > 0 {
		targets = res.Proof.Targets
	}
	if len(targets) == 0 {
		return false
	}
	for _, t := range targets {
		if !pre.Existed[t] {
			return false
		}
	}
	return true
}

// terminalFinishReason returns the provider finish_reason of the LAST
// invocation, verbatim ("" when the execution never invoked the provider).
func terminalFinishReason(res *ExecutionResult) string {
	if res == nil {
		return ""
	}
	if n := len(res.ModelCalls); n > 0 {
		if fr := res.ModelCalls[n-1].FinishReason; fr != "" {
			return fr
		}
	}
	if res.Proof != nil {
		if n := len(res.Proof.ModelInvocations); n > 0 {
			if fr := res.Proof.ModelInvocations[n-1].FinishReason; fr != "" {
				return fr
			}
		}
	}
	for _, c := range res.ArtifactCandidates {
		if c.ExhaustedSteps > 0 {
			return "length"
		}
	}
	return ""
}

// parsedArtifacts counts the VALID artifacts the parser extracted. A rejected
// artifact, a held candidate and a prose-only payload all count ZERO.
func parsedArtifacts(res *ExecutionResult) int {
	records := res.Mutations
	if res.Proof != nil && len(res.Proof.Mutations) > len(records) {
		records = res.Proof.Mutations
	}
	n := 0
	for _, m := range records {
		if m.ArtifactPresent {
			n++
		}
	}
	return n
}

// hasPartialCandidate reports whether a truncated generation's delivered prefix
// was preserved for continuation. A preserved prefix is evidence, never an
// artifact.
func hasPartialCandidate(res *ExecutionResult) bool {
	for _, c := range res.ArtifactCandidates {
		if c.ExhaustedSteps > 0 || !c.Committed {
			return true
		}
	}
	return false
}

// mutationBoundary reduces the mutation evidence onto the filesystem boundary
// and reports the durable target set whose bytes actually changed.
func mutationBoundary(res *ExecutionResult) (MutationBoundaryState, int, []string) {
	records := res.Mutations
	if res.Proof != nil && len(res.Proof.Mutations) > len(records) {
		records = res.Proof.Mutations
	}
	var changed []string
	applied := false
	for _, m := range records {
		if m.ApplyExecutedChanged() {
			applied = true
			changed = appendUnique(changed, m.File)
		}
	}
	tainted := false
	if res.Evidence != nil {
		summary := res.Evidence.Mutations()
		tainted = summary.Tainted
		applied = applied || (summary.ApplyExecuted && summary.FilesMutated > 0)
	}
	switch {
	case tainted:
		return FilesystemRolledBack, 0, nil
	case applied && len(changed) > 0:
		return FilesystemApplied, len(changed), changed
	default:
		return FilesystemNone, 0, nil
	}
}

func appendUnique(list []string, v string) []string {
	if v == "" {
		return list
	}
	for _, existing := range list {
		if existing == v {
			return list
		}
	}
	return append(list, v)
}
