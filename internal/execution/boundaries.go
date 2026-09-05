package execution

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ── 5-Boundary Zero-Trust Execution Architecture ───────────────────────────
//
// Every generation pipeline crosses five mandatory boundaries. Each boundary
// owns exactly one fail-closed decision and never trusts the previous stage:
//
//	B1  Intent Gateway        (intent.go)      resolve intent + scope authorization
//	B2  Preflight Guard       (this file)      EstimatedTokens = TargetFileTokens × Multiplier
//	                                           must fit max_output; infeasible ⇒ reject or
//	                                           require explicit re-scoping (never silently
//	                                           alter user intent)
//	B3  Output Gate           (this file)      normalize provider finish_reason into a
//	                                           CanonicalOutcome; OUTPUT_EXHAUSTED trips an
//	                                           immediate transport circuit break — no
//	                                           incomplete stream ever reaches B4
//	B4  Artifact Gate         (executor.go)    validate SEARCH/REPLACE hunk schemas and
//	                                           syntax; Recovery Isolation: rejected
//	                                           artifacts are NEVER re-injected into prompt
//	                                           context — only advisory DiagnosticSignals cross
//	B5  Mutation Authority    (occ.go)         validate the workspace tree digest
//	                                           SHA256(Σ path(f)+hash(f)); abort when the
//	                                           workspace version changes between attempts
//
// Invariants enforced here:
//
//	I1  Output Boundary Integrity — OUTPUT_EXHAUSTED strictly halts execution.
//	I2  Recovery Isolation        — Workspace State authoritative, Diagnostics
//	                                advisory, rejected artifacts isolated.
//	I5  Preflight Feasibility     — generation budget verified BEFORE any
//	                                provider request.

// CanonicalOutcome is the normalized terminal classification of one provider
// generation, derived ONLY from authoritative provider facts (finish_reason,
// transport errors). Downstream stages must branch on canonical outcomes —
// never on raw provider strings.
type CanonicalOutcome string

const (
	// CanonicalComplete: the provider finished its generation normally.
	CanonicalComplete CanonicalOutcome = "COMPLETE"
	// CanonicalOutputExhausted: the generation hit max_output before
	// completing (finish_reason=length). The emitted bytes are an INCOMPLETE
	// prefix by definition (I1).
	CanonicalOutputExhausted CanonicalOutcome = "OUTPUT_EXHAUSTED"
	// CanonicalProviderRefusal: the provider refused or filtered the content.
	CanonicalProviderRefusal CanonicalOutcome = "PROVIDER_REFUSAL"
	// CanonicalTransportError: the invocation failed at the transport layer
	// (no complete terminal reason was observed).
	CanonicalTransportError CanonicalOutcome = "TRANSPORT_ERROR"
	// CanonicalUnknown: no terminal reason was reported. Callers fail closed.
	CanonicalUnknown CanonicalOutcome = "UNKNOWN"
)

// String returns the canonical label.
func (c CanonicalOutcome) String() string { return string(c) }

// HaltsGeneration reports whether the canonical outcome must halt the
// execution pipeline (I1). Only COMPLETE may proceed to the artifact gate.
func (c CanonicalOutcome) HaltsGeneration() bool {
	return c != CanonicalComplete
}

// NormalizeFinishReason maps a provider finish_reason onto the canonical
// outcome vocabulary. Deterministic and total: unknown non-empty reasons map
// to CanonicalUnknown (fail-closed), never silently to COMPLETE.
func NormalizeFinishReason(reason string) CanonicalOutcome {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "":
		return CanonicalUnknown
	case "stop", "end_turn", "endturn", "stop_sequence":
		return CanonicalComplete
	case "length", "max_tokens", "max_output_tokens", "truncated", "token_limit":
		return CanonicalOutputExhausted
	case "content_filter", "refusal", "safety", "moderation", "blocked":
		return CanonicalProviderRefusal
	default:
		return CanonicalUnknown
	}
}

// ── Boundary 2 — Preflight Guard (I5) ──────────────────────────────────────

// FullRewriteTokenMultiplier is the generation expansion factor of a full-file
// rewrite: regenerating an existing file costs a multiple of its own token
// size (context echo + rewritten body + structural overhead). A full rewrite
// whose estimate exceeds max_output is INFEASIBLE BY CONSTRUCTION and must be
// trapped at this boundary instead of being truncated mid-generation.
const FullRewriteTokenMultiplier = 3

// BoundedPatchTokenMultiplier is the generation expansion factor of a bounded
// SEARCH/REPLACE patch over an existing file: the model echoes a small
// runtime-derived window plus its replacement, not a whole-file regeneration.
// Targeted modification prompts ($prompt / $hot) on markup/text targets issue
// bounded patches, so their preflight budget uses this multiplier instead of
// the full-rewrite multiplier ($3×).
const BoundedPatchTokenMultiplier = 2

// ErrPreflightInfeasible is the deterministic Boundary-2 rejection: the
// estimated output budget of the requested artifact exceeds max_output. The
// request is refused BEFORE any provider request; the user must explicitly
// re-scope (reduce scope, raise the budget, or accept a bounded patch
// contract) — the runtime never silently alters user intent.
var ErrPreflightInfeasible = errors.New("executor: preflight infeasible — estimated output budget exceeds max_output")

// PreflightRequest carries the feasibility inputs of one proposed generation.
type PreflightRequest struct {
	// ArtifactBounded reports whether the artifact contract is anchored to a
	// located block (SEARCH/REPLACE window) rather than a full-file rewrite.
	ArtifactBounded bool
	// TargetBytes is the byte size of the existing target content (0 when the
	// target does not exist yet — a creation cannot be size-estimated).
	TargetBytes int
	// MaxOutputTokens is the output budget enforced on the invocation
	// (0 = unbounded / provider default).
	MaxOutputTokens int
	// StagedScopes carries the sub-task windows of a staged decomposition
	// plan when DAG execution is active. When non-empty, the monolithic
	// full_rewrite estimate is SUPPRESSED: each sub-task window is evaluated
	// individually against the same budget instead.
	StagedScopes []SubTaskScope
}

// SubTaskScope is the Boundary-2 view of one staged decomposition sub-task:
// its identity, its inclusive 1-indexed line window over the target, and its
// own generation estimate under the canonical accounting
// (region_bytes/4 × FullRewriteTokenMultiplier). It is plain data so the
// executor can preflight every unit of an approved ExecutionDAG individually
// without importing the planner.
type SubTaskScope struct {
	// ID is the stable sub-task identity ("st-1", "st-2", ...).
	ID string
	// StartLine / EndLine bound the sub-task's change window in the target.
	StartLine int
	EndLine   int
	// EstimatedTokens is this window's generation estimate under the same
	// accounting as the monolithic formula.
	EstimatedTokens int
}

// PreflightVerdict is the Boundary-2 decision record.
type PreflightVerdict struct {
	// Feasible reports whether the generation may proceed.
	Feasible bool
	// EstimatedTokens = TargetFileTokens × FullRewriteTokenMultiplier for a
	// monolithic request, or the largest staged scope's estimate when DAG
	// execution is active.
	EstimatedTokens int
	// Budget is the max_output the invocation would run under.
	Budget int
	// Reason explains an infeasible verdict (stable evidence string).
	Reason string
}

// EvaluatePreflight applies invariant I5. When the request carries staged
// decomposition scopes (DAG execution active), the monolithic full-file
// rewrite estimation is short-circuited and EVERY sub-task window must pass
// the budget individually — the plan executes as bounded units, never as one
// monolithic regeneration. Otherwise EstimatedTokens =
// TargetFileTokens × Multiplier must fit MaxOutputTokens for full-file
// artifacts. Bounded artifacts fit by construction (their copyable source is
// capped far below any ceiling) and creations have no estimable baseline —
// both pass. An infeasible verdict REJECTS the request at this boundary.
func EvaluatePreflight(req PreflightRequest) PreflightVerdict {
	switch {
	case len(req.StagedScopes) > 0:
		// DAG EXECUTION ACTIVE: judge every staged sub-task individually.
		// The original monolithic target size is irrelevant evidence here —
		// no full rewrite will ever be requested under this contract.
		if req.MaxOutputTokens <= 0 {
			// Unbounded budget: not provably infeasible at this boundary.
			return PreflightVerdict{Feasible: true}
		}
		largest := 0
		for _, sc := range req.StagedScopes {
			if sc.EstimatedTokens <= 0 || sc.EstimatedTokens > req.MaxOutputTokens {
				return PreflightVerdict{
					EstimatedTokens: sc.EstimatedTokens,
					Budget:          req.MaxOutputTokens,
					Reason: fmt.Sprintf(
						"staged sub_task %s estimates %d tokens but max_output=%d — every unit of an approved decomposition plan must fit the output budget individually",
						sc.ID, sc.EstimatedTokens, req.MaxOutputTokens),
				}
			}
			if sc.EstimatedTokens > largest {
				largest = sc.EstimatedTokens
			}
		}
		return PreflightVerdict{Feasible: true, EstimatedTokens: largest, Budget: req.MaxOutputTokens}
	case req.ArtifactBounded:
		// A bounded patch echoes at most its small runtime-derived window;
		// it fits any workable budget by construction.
		return PreflightVerdict{Feasible: true}
	case req.TargetBytes == 0:
		// Creation intent: nothing exists to regenerate, so there is no
		// size-provable infeasibility at this boundary.
		return PreflightVerdict{Feasible: true}
	case req.MaxOutputTokens <= 0:
		// Unbounded budget (provider default): not provably infeasible here.
		// The Output Gate remains the authority on the actual result.
		return PreflightVerdict{Feasible: true}
	}
	targetTokens := req.TargetBytes / 4 // same accounting heuristic as context compilation
	estimated := targetTokens * FullRewriteTokenMultiplier
	v := PreflightVerdict{
		EstimatedTokens: estimated,
		Budget:          req.MaxOutputTokens,
	}
	if estimated > req.MaxOutputTokens {
		v.Feasible = false
		v.Reason = fmt.Sprintf(
			"full_rewrite of %d-byte target estimates ~%d tokens (target_tokens=%d × multiplier=%d) but max_output=%d — reduce scope, raise max_output, or explicitly re-scope to a bounded SEARCH/REPLACE patch",
			req.TargetBytes, estimated, targetTokens, FullRewriteTokenMultiplier, req.MaxOutputTokens)
		return v
	}
	v.Feasible = true
	return v
}

// preflightTargetBytes reads the current size of one resolved target.
// A missing file reports 0 (creation intent).
func preflightTargetBytes(root, target string) int {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(target)))
	if err != nil {
		return 0
	}
	return len(data)
}

// ── Boundary 3 — Output Gate circuit break (I1) ────────────────────────────

// ErrOutputExhausted is the canonical Boundary-3 circuit break: the provider
// reported finish_reason=length. The partial generation was DISCARDED at the
// gate — it never reached artifact parsing, approval, or mutation surfaces.
// It wraps the historical ErrOutputTruncated sentinel so existing
// classification keeps working.
var ErrOutputExhausted = fmt.Errorf("%w — discarded at the output gate", ErrOutputTruncated)

// ErrPayloadTruncated is the explicit truncation signal required by the
// LLM stream deadlock fix: finish_reason == "length" must fail fast before
// JSON/envelope parsing. It wraps the legacy ErrOutputTruncated sentinel so
// errors.Is checks for both succeed, but its message matches the directive's
// required string.
var ErrPayloadTruncated = fmt.Errorf("%w: model output exceeded max_tokens limit", ErrOutputTruncated)

// ErrProviderRefused is the canonical refusal circuit break: the provider
// refused or filtered the generation. Nothing was parsed or staged.
var ErrProviderRefused = errors.New("executor: provider refused generation")

// ErrGenerationIncomplete is the fail-closed circuit break for an UNKNOWN
// terminal state: a stream without a provably COMPLETE finish reason is never
// parsed as an artifact.
var ErrGenerationIncomplete = errors.New("executor: generation ended without a complete terminal reason")

// OutputGateError is the structured error returned when Boundary 3 halts a
// generation. It carries the canonical outcome and the authoritative provider
// finish reason; it never carries generated content.
type OutputGateError struct {
	// Outcome is the canonical classification of the halt.
	Outcome CanonicalOutcome
	// Target is the workspace-relative target being generated.
	Target string
	// FinishReason is the verbatim provider terminal reason.
	FinishReason string
}

// Error implements error.
func (e *OutputGateError) Error() string {
	return fmt.Sprintf("output gate %s: target %s finish_reason=%q", e.Outcome, e.Target, e.FinishReason)
}

// Unwrap exposes the sentinel so errors.Is classifies the family.
func (e *OutputGateError) Unwrap() error {
	switch e.Outcome {
	case CanonicalOutputExhausted:
		return ErrOutputExhausted
	case CanonicalProviderRefusal:
		return ErrProviderRefused
	default:
		return ErrGenerationIncomplete
	}
}

// gateFor normalizes one authoritative invocation result into a gate error,
// or nil when generation may proceed to the artifact boundary. A KNOWN
// non-complete terminal reason (output exhausted / refusal) is an absolute
// circuit break (I1): the bytes are an incomplete prefix by definition and are
// never parsed. An UNREPORTED reason ("" — some providers and transports omit
// it) is not proof of incompleteness: it proceeds, and Boundary 4 remains the
// authority on content validity.
func gateFor(target, finishReason string) *OutputGateError {
	outcome := NormalizeFinishReason(finishReason)
	switch outcome {
	case CanonicalOutputExhausted:
		return &OutputGateError{Outcome: outcome, Target: target, FinishReason: finishReason}
	case CanonicalProviderRefusal:
		return &OutputGateError{Outcome: outcome, Target: target, FinishReason: finishReason}
	default:
		return nil
	}
}

// ── Boundary 4 — Diagnostic Signals (I2 recovery isolation) ────────────────

// Failure subtype keys carried by DiagnosticSignal. They are the I4
// retryability keys: the recovery matrix branches on these subtypes, never on
// generic execution errors.
const (
	// SignalOutputExhausted: generation hit max_output before completing.
	SignalOutputExhausted = string(CanonicalOutputExhausted)
	// SignalProviderRefusal: provider refused or filtered the content.
	SignalProviderRefusal = string(CanonicalProviderRefusal)
	// SignalTransportError: transport-level invocation failure.
	SignalTransportError = string(CanonicalTransportError)
	// SignalSchemaViolation: artifact failed Boundary-4 schema/syntax validation.
	SignalSchemaViolation = "SCHEMA_VIOLATION"
	// SignalPreflightInfeasible: Boundary 2 refused the budget estimate (I5).
	SignalPreflightInfeasible = "PREFLIGHT_INFEASIBLE"
	// SignalWorkspaceDrift: Boundary 5 detected workspace divergence (B5).
	SignalWorkspaceDrift = "WORKSPACE_DRIFT"
	// SignalNoOpObjectiveUnresolved: a NO_CHANGES_REQUIRED claim was
	// contradicted by deterministic structural analysis — escalation trigger.
	SignalNoOpObjectiveUnresolved = "NO_OP_OBJECTIVE_UNRESOLVED"
	// SignalNoOpRequiresReview: a NO_CHANGES_REQUIRED claim carried candidate
	// edit regions below the structural safety threshold — review hold.
	SignalNoOpRequiresReview = "NO_OP_REQUIRES_REVIEW"
)

// boundaryOf names the architectural boundary that produced a subtype.
func boundaryOf(subtype string) string {
	switch subtype {
	case SignalPreflightInfeasible:
		return "B2-preflight-guard"
	case SignalOutputExhausted, SignalProviderRefusal, SignalTransportError:
		return "B3-output-gate"
	case SignalSchemaViolation:
		return "B4-artifact-gate"
	case SignalWorkspaceDrift:
		return "B5-mutation-authority"
	case SignalNoOpObjectiveUnresolved, SignalNoOpRequiresReview:
		return "B4-noop-semantics"
	default:
		return "B4-artifact-gate"
	}
}

// DiagnosticSignal is the ONLY form in which a rejected attempt crosses back
// toward recovery. It is advisory metadata: bounded text describing WHAT
// failed and WHAT to do differently — NEVER the rejected artifact bytes. A
// signal can therefore be safely surfaced to a human or attached to a NEW
// contract's evidence ledger without poisoning prompt context with a partial
// or malformed generation.
type DiagnosticSignal struct {
	// Subtype is the canonical failure subtype (I4 retryability key).
	Subtype string `json:"subtype"`
	// Target is the workspace-relative target of the failed attempt.
	Target string `json:"target"`
	// Detail is the bounded human-readable explanation (metadata only).
	Detail string `json:"detail"`
	// Directive is the corrective instruction for a successor attempt.
	Directive string `json:"directive,omitempty"`
	// Retryable reports whether the I4 matrix allows a typed continuation.
	Retryable bool `json:"retryable"`
}

// Boundary names which boundary produced the signal.
func (d DiagnosticSignal) Boundary() string {
	return boundaryOf(d.Subtype)
}

// diagnosticSignal builds the advisory signal for a rejected attempt.
func diagnosticSignal(subtype, target, detail, directive string, retryable bool) DiagnosticSignal {
	return DiagnosticSignal{
		Subtype:   subtype,
		Target:    target,
		Detail:    detail,
		Directive: directive,
		Retryable: retryable,
	}
}
