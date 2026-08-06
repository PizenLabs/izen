// Package policy defines the pure business rules that gate operations in the
// domain: safety, approval, execution, and workflow-transition policies. Each
// policy is an interface with a deterministic default implementation so outer
// layers can swap in stricter rules without touching domain logic.
package policy

import "strings"

// OperationKind discriminates the operation classes the policies evaluate.
type OperationKind string

const (
	// OpFileWrite is a direct workspace file write.
	OpFileWrite OperationKind = "file.write"
	// OpShellExec is an arbitrary shell command execution.
	OpShellExec OperationKind = "shell.exec"
	// OpPatchApply is a structured patch application.
	OpPatchApply OperationKind = "patch.apply"
	// OpGitCommit is a version-control commit.
	OpGitCommit OperationKind = "git.commit"
	// OpLLMGenerate is a model inference call.
	OpLLMGenerate OperationKind = "llm.generate"
)

// Operation is the descriptor every policy evaluates. It is intentionally
// free of outer-layer types so the rules stay pure.
type Operation struct {
	// Kind identifies the class of operation.
	Kind OperationKind
	// Target is the primary coordinate: a file path, command, or subject.
	Target string
	// Files lists affected files for batch operations like patches.
	Files []string
}

// SafetyVerdict is the result of a safety evaluation.
type SafetyVerdict struct {
	// Allowed reports whether the operation is safe to perform.
	Allowed bool
	// Reason explains a rejection.
	Reason string
}

// SafetyPolicy gates whether an operation is safe to perform.
type SafetyPolicy interface {
	// Evaluate returns the safety verdict for an operation.
	Evaluate(op Operation) SafetyVerdict
}

// DefaultSafetyPolicy implements the baseline safety rules: an operation must
// carry a usable target, a patch must reference at least one file, and the
// operation kind must be recognized. It never rejects on severity alone, so
// outer layers may layer stricter rules on top.
type DefaultSafetyPolicy struct{}

// Evaluate applies the baseline safety rules.
func (DefaultSafetyPolicy) Evaluate(op Operation) SafetyVerdict {
	switch op.Kind {
	case OpFileWrite, OpShellExec, OpGitCommit:
		if strings.TrimSpace(op.Target) == "" {
			return SafetyVerdict{Allowed: false, Reason: "operation target is empty"}
		}
	case OpPatchApply:
		if len(op.Files) == 0 {
			return SafetyVerdict{Allowed: false, Reason: "patch targets no files"}
		}
	case OpLLMGenerate:
		// Model inference is read-only and always permitted by default.
	default:
		return SafetyVerdict{Allowed: false, Reason: "unknown operation kind"}
	}
	return SafetyVerdict{Allowed: true}
}

// ApprovalPolicy gates whether an operation requires explicit human approval.
type ApprovalPolicy interface {
	// RequiresApproval reports whether the operation needs human sign-off.
	RequiresApproval(op Operation) bool
}

// DefaultApprovalPolicy implements the baseline approval rules: commits and
// shell executions require approval, while file writes, patch applications,
// and model calls proceed without it. Callers may override the defaults per
// operation kind through AutoApprove and Manual.
type DefaultApprovalPolicy struct {
	// AutoApprove lists kinds that never require approval. A nil or empty map
	// falls back to the baseline rules.
	AutoApprove map[OperationKind]bool
	// Manual lists kinds that always require approval. A nil or empty map
	// falls back to the baseline rules.
	Manual map[OperationKind]bool
}

// RequiresApproval evaluates the approval rule for an operation.
func (p DefaultApprovalPolicy) RequiresApproval(op Operation) bool {
	if p.Manual[op.Kind] {
		return true
	}
	if p.AutoApprove[op.Kind] {
		return false
	}
	return p.baseline(op.Kind)
}

func (p DefaultApprovalPolicy) baseline(kind OperationKind) bool {
	switch kind {
	case OpGitCommit, OpShellExec:
		return true
	default:
		return false
	}
}

// ExecutionPolicy gates how operations may be executed.
type ExecutionPolicy interface {
	// Allowed reports whether the operation kind may be executed at all.
	Allowed(op Operation) bool
	// MaxAttempts returns the retry budget for the operation kind.
	MaxAttempts(op Operation) int
}

// DefaultExecutionPolicy implements the baseline execution rules: only the
// declared operation kinds are allowed, each with a fixed retry budget.
type DefaultExecutionPolicy struct {
	attempts map[OperationKind]int
}

// NewDefaultExecutionPolicy builds a policy with the baseline retry budgets.
func NewDefaultExecutionPolicy() *DefaultExecutionPolicy {
	return &DefaultExecutionPolicy{
		attempts: map[OperationKind]int{
			OpFileWrite:   1,
			OpShellExec:   3,
			OpPatchApply:  2,
			OpGitCommit:   1,
			OpLLMGenerate: 1,
		},
	}
}

// Allowed reports whether the operation kind is known to the execution engine.
func (p *DefaultExecutionPolicy) Allowed(op Operation) bool {
	_, ok := p.attempts[op.Kind]
	return ok
}

// MaxAttempts returns the retry budget for the operation kind, defaulting to 1.
func (p *DefaultExecutionPolicy) MaxAttempts(op Operation) int {
	if n, ok := p.attempts[op.Kind]; ok {
		return n
	}
	return 1
}

// ── Unified Policy Engine domain models (Sprint 5) ──────────────────────
// The types below are the vocabulary of the single authoritative governance
// owner — the PolicyEngine. An Action is an intended operation, a
// PolicyContext carries the runtime governance inputs, and a Verdict is the
// pure answer to "is this action permitted?". These models deliberately carry
// no physical-facts vocabulary: capabilities live in the CapabilityGraph, not
// in the policy domain.

// ActionKind discriminates the action classes the PolicyEngine adjudicates.
type ActionKind string

const (
	// ActionFileRead is a read-only file access.
	ActionFileRead ActionKind = "FILE_READ"
	// ActionFileWrite is a direct workspace file write.
	ActionFileWrite ActionKind = "FILE_WRITE"
	// ActionShellExec is an arbitrary shell command execution.
	ActionShellExec ActionKind = "SHELL_EXEC"
	// ActionPatchApply is a structured patch application.
	ActionPatchApply ActionKind = "PATCH_APPLY"
)

// Action is an intended action the PolicyEngine is asked to adjudicate.
type Action struct {
	// Kind identifies the class of the action.
	Kind ActionKind `json:"kind"`
	// Target is the primary coordinate: a file path or shell command.
	Target string `json:"target,omitempty"`
}

// Mutating reports whether the action modifies the workspace.
func (a Action) Mutating() bool {
	switch a.Kind {
	case ActionFileWrite, ActionShellExec, ActionPatchApply:
		return true
	default:
		return false
	}
}

// PolicyContext carries the runtime governance inputs for one evaluation:
// the active mode, the remaining token budget and whether a human has
// approved the action. It is assembled by the caller from live runtime state
// and never probes the OS itself.
type PolicyContext struct {
	ActiveMode      string `json:"active_mode"`
	RemainingTokens int    `json:"remaining_tokens"`
	IsHumanApproved bool   `json:"is_human_approved"`
}

// Verdict is the outcome of a policy evaluation. Allowed is one of the
// Verdict constants: ALLOW, DENY or REQUIRE_APPROVAL.
type Verdict struct {
	Allowed string `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
}

// Verdict constants returned by the PolicyEngine.
const (
	// VerdictAllow permits the action unconditionally.
	VerdictAllow = "ALLOW"
	// VerdictDeny forbids the action.
	VerdictDeny = "DENY"
	// VerdictRequireApproval gates the action behind human approval.
	VerdictRequireApproval = "REQUIRE_APPROVAL"
)

// IsAllowed reports whether the verdict permits the action unconditionally.
func (v Verdict) IsAllowed() bool { return v.Allowed == VerdictAllow }

// RequiresApproval reports whether the verdict gates the action behind
// human approval.
func (v Verdict) RequiresApproval() bool { return v.Allowed == VerdictRequireApproval }

// String renders the machine-readable verdict with its rationale.
func (v Verdict) String() string {
	if v.Reason != "" {
		return v.Allowed + ": " + v.Reason
	}
	return v.Allowed
}
