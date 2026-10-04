package execution

// ── Objective operation / scope / target / discovery semantics ──────────────
//
// §AUDIT (the unresolved-target → CREATE defect).
//
// The runtime path for `$prompt check this project and rewrite it` is:
//
//	ui.routePromptDirective
//	  → runAutonomyRoutedCmdExplicit
//	  → dispatchAutonomyTrace → executeAutonomyWorkspace
//	  → autonomousDriver.Run(ctx, objective)          (internal/runtime/autonomy)
//	      d.resolved = adapter.Resolve(objective)      (strategy gateway)
//	      d.deriveEvidenceScope()                      (evidence-bound discovery)
//	      d.taskContract()                             (execution shape)
//	      d.objectiveContract()                        (completion contract)
//
// Two facts about that prompt:
//
//	autonomy.Classify("check this project and rewrite it").RequiresMutation() == true
//	  ("rewrite" is a change verb)
//	autonomy.Classify(...).Targets == nil
//	  (the prompt names no file, so no target reference is extracted)
//
// In DeriveTaskContract the CREATE branch read:
//
//	noExistingContent := !anyTargetExisted(in.TargetsExistedBefore, targets)
//	if noExistingContent { contract.Kind = TaskCreate }
//
// `anyTargetExisted` returns false for an EMPTY target set, so `noExistingContent`
// was true for any mutation objective that had not resolved a target yet. The
// ABSENCE of a resolved target was therefore read as positive evidence that the
// artifact is new, and the objective compiled as CREATE with an empty scope.
//
// That is a category error. The three dimensions are independent:
//
//	OPERATION   what the user intends       CREATE | MODIFY | DELETE | READ | REVIEW
//	SCOPE       what is currently known     RESOLVED | UNRESOLVED
//	TARGET      how concrete the target is  CONCRETE | DEFERRED
//	DISCOVERY   must we look first?         REQUIRED | NOT_REQUIRED
//
// so `MODIFY + UNRESOLVED + DEFERRED + REQUIRED` is a perfectly valid objective,
// and `UNRESOLVED TARGET ≠ CREATE`. This file names those dimensions and derives
// them deterministically from the execution-shape kind and the scope. It carries
// no authority of its own: it is a pure label over facts the runtime already
// holds, and it is what lets a caller see that a targetless modification is a
// deferred modification rather than an invented creation.

// Operation is the SEMANTIC operation a user intends, independent of whether the
// concrete target is known. It is deliberately separate from TaskKind: TaskKind
// is the execution SHAPE the evidence must prove, while Operation is the intent
// the objective expresses. A single Operation can map onto more than one shape
// (a MODIFY of an existing file is PATCH, a MODIFY already satisfied on disk is
// IDEMPOTENT), and that is exactly why the two must not be conflated.
type Operation string

const (
	// OperationCreate: the user intends to bring a new artifact into existence.
	OperationCreate Operation = "CREATE"
	// OperationModify: the user intends to change existing state.
	OperationModify Operation = "MODIFY"
	// OperationDelete: the user intends to remove an artifact.
	OperationDelete Operation = "DELETE"
	// OperationRead: the user intends to look something up and be told.
	OperationRead Operation = "READ"
	// OperationReview: the user intends a review/verification of existing state.
	OperationReview Operation = "REVIEW"
)

// String returns the canonical operation label.
func (o Operation) String() string { return string(o) }

// RequiresMutation reports whether the operation writes to the workspace. READ
// and REVIEW never do.
func (o Operation) RequiresMutation() bool {
	switch o {
	case OperationCreate, OperationModify, OperationDelete:
		return true
	default:
		return false
	}
}

// OperationForTaskKind projects an execution-shape kind onto its semantic
// operation. The mapping NEVER produces CREATE for an unresolved/ambiguous kind:
// PATCH, IDEMPOTENT and any unrecognised kind are MODIFY, because treating an
// unknown shape as a creation is the exact inference this file removes.
func OperationForTaskKind(kind TaskKind) Operation {
	switch kind {
	case TaskCreate:
		return OperationCreate
	case TaskDelete:
		return OperationDelete
	case TaskRead:
		return OperationRead
	case TaskReview:
		return OperationReview
	default:
		// TaskPatch, TaskIdempotent and any unknown kind.
		return OperationModify
	}
}

// ScopeState is the objective's current target-resolution state. It is a
// statement about what the runtime KNOWS, never a verdict about what is needed.
type ScopeState string

const (
	// ScopeStateResolved: at least one concrete target is bound.
	ScopeStateResolved ScopeState = "RESOLVED"
	// ScopeStateUnresolved: no concrete target is bound yet. This is an ABSENCE
	// OF A VERDICT — the runtime has not invented one and does not treat the
	// emptiness as a decision.
	ScopeStateUnresolved ScopeState = "UNRESOLVED"
)

// String returns the canonical scope-state label.
func (s ScopeState) String() string { return string(s) }

// TargetDisposition is how concrete the objective's target is. It is distinct
// from ScopeState only in emphasis: an UNRESOLVED scope yields a DEFERRED target,
// and a DEFERRED target means discovery must run before execution.
type TargetDisposition string

const (
	// TargetConcrete: the target names a specific artifact.
	TargetConcrete TargetDisposition = "CONCRETE"
	// TargetDeferred: the target is not known yet; discovery must resolve it.
	TargetDeferred TargetDisposition = "DEFERRED"
)

// String returns the canonical target-disposition label.
func (t TargetDisposition) String() string { return string(t) }

// DiscoveryState reports whether the runtime must observe the workspace to
// resolve the objective's target before it may execute. It is REQUIRED exactly
// when the operation mutates and the scope is unresolved: a read-only objective
// needs no mutation target, and a resolved mutation needs no discovery.
type DiscoveryState string

const (
	// DiscoveryRequired: the target must be resolved from workspace evidence
	// before any mutation is proposed.
	DiscoveryRequired DiscoveryState = "REQUIRED"
	// DiscoveryNotRequired: the objective already holds what it needs.
	DiscoveryNotRequired DiscoveryState = "NOT_REQUIRED"
)

// String returns the canonical discovery-state label.
func (d DiscoveryState) String() string { return string(d) }

// ObjectiveSemantics is the independent semantic model of one objective. It is
// what makes `operation = MODIFY, scope = UNRESOLVED, target = DEFERRED,
// discovery = REQUIRED` expressible, and it is the invariant this file exists to
// protect:
//
//	UNRESOLVED TARGET ≠ CREATE
type ObjectiveSemantics struct {
	// Operation is the intended semantic operation.
	Operation Operation `json:"operation"`
	// Scope is the current target-resolution state.
	Scope ScopeState `json:"scope"`
	// Target is how concrete the target is.
	Target TargetDisposition `json:"target"`
	// Discovery reports whether discovery is required before execution.
	Discovery DiscoveryState `json:"discovery"`
}

// RequiresDiscovery reports whether the objective must resolve its target from
// workspace evidence before a mutation may be proposed.
func (s ObjectiveSemantics) RequiresDiscovery() bool {
	return s.Discovery == DiscoveryRequired
}

// DeriveObjectiveSemantics deterministically derives the semantic model from an
// operation and the currently-known scope. It is total and pure.
//
// The single rule that matters here: discovery is REQUIRED for a MUTATING
// objective whose scope is UNRESOLVED. Both halves are independent — the
// operation comes from what the user intends, the scope from what the runtime
// has bound — and neither is used as evidence for the other.
func DeriveObjectiveSemantics(operation Operation, scope []string) ObjectiveSemantics {
	resolved := len(normalizeScope(scope)) > 0

	sem := ObjectiveSemantics{Operation: operation}
	if resolved {
		sem.Scope = ScopeStateResolved
		sem.Target = TargetConcrete
	} else {
		sem.Scope = ScopeStateUnresolved
		sem.Target = TargetDeferred
	}

	if operation.RequiresMutation() && !resolved {
		sem.Discovery = DiscoveryRequired
	} else {
		sem.Discovery = DiscoveryNotRequired
	}
	return sem
}
