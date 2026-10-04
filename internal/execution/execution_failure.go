package execution

// ── STRUCTURED EXECUTION FAILURE CLASSIFICATION ─────────────────────────────
//
// This file owns the answer to ONE question the runtime could previously only
// approximate:
//
//	what KIND of execution failure just happened, and what EVIDENCE proves it?
//
// THE DEFECT IT REPLACES. Every execution failure collapsed into a small set of
// generic words — `failed`, `repair`, `retry` — chosen by substring-matching a
// diagnostic string. That made three structurally different failures
// indistinguishable:
//
//	read_file(style.css)      -> "no such file or directory"   (target identity)
//	finish_reason=length      -> "truncated"                   (provider budget)
//	SEARCH block matched 0/N   -> "zero match"                 (patch anchor)
//
// The first was retried under an identical contract identity because it looked
// like a transport blip. The second was reclassified as "the model wrote prose"
// because the zero-artifact check ran before the finish_reason check. The third
// was reported as "Physical Output Budget Breach", a label about a completely
// different failure, because the only thing the recovery matrix had was a
// substring.
//
// RELATIONSHIP TO THE TAXONOMIES THAT ALREADY EXIST. Nothing here replaces them.
// It is a typed RECORD that carries an existing class plus the evidence that
// produced it:
//
//	FailureClass    the closed vocabulary below (recovery-facing, deterministic)
//	CapabilityClass capability.FailureClass, when the failure came from a
//	                capability seam (already a closed taxonomy)
//	Canonical       CanonicalOutcome, when the failure came from the provider
//	                boundary (already a closed taxonomy)
//
// The class is derived from those, never invented alongside them. A failure that
// already had an honest name keeps it; what was missing is the EVIDENCE, and
// that is what this file adds.

import (
	"errors"
	"fmt"
	"strings"
)

// FailureClass is the closed, domain-neutral taxonomy the Control Plane branches
// on. It is deliberately separate from:
//
//	capability.FailureClass — what a capability could not do
//	CanonicalOutcome        — what the provider transport did
//	autonomy.FailureSubtype — the legacy recovery matrix's retryability key
//
// because it answers a different question: NOT "what broke" but "what kind of
// recovery, if any, is admissible". Two failures can share a capability class
// and still require opposite recovery (a missing file is not retryable; an
// unreadable one might be).
type FailureClass string

const (
	// FailureNone: no execution failure. The classification exists only for
	// failures; a success never carries one.
	FailureNone FailureClass = ""

	// ── Target identity ────────────────────────────────────────────────
	// FailureTargetNotFound: the requested target does not exist. The request is
	// refused and NOTHING is substituted. Retrying the identical request cannot
	// change the answer, so recovery must replan against workspace evidence.
	FailureTargetNotFound FailureClass = "TARGET_NOT_FOUND"
	// FailureTargetIdentityMismatch: the request names something close to, but
	// not equal to, an authoritative target. Distinct from NOT_FOUND because it
	// is the case where a fuzzy mapper would have substituted — and this runtime
	// refuses to. There is no "closest file" answer; there is a refusal.
	FailureTargetIdentityMismatch FailureClass = "TARGET_IDENTITY_MISMATCH"
	// FailureTargetAmbiguous: more than one target is admissible and nothing
	// proved which was meant. A question for the human, never a guess.
	FailureTargetAmbiguous FailureClass = "TARGET_AMBIGUOUS"

	// ── Capability / transport ─────────────────────────────────────────
	// FailureCapabilityFailure: an authorized capability was attempted and the
	// attempt itself broke (a listener that would not bind, an unreadable file).
	// The capability taxonomy owns the fine-grained cause; this is the
	// recovery-facing summary.
	FailureCapabilityFailure FailureClass = "CAPABILITY_FAILURE"
	// FailureAuthorizationRequired: the Control Plane did not grant what the
	// request needed. An authorization change, never a retry.
	FailureAuthorizationRequired FailureClass = "AUTHORIZATION_REQUIRED"
	// FailureTransportError: the invocation failed at the transport layer. The
	// ONLY failure for which an identical re-execution is admissible.
	FailureTransportError FailureClass = "TRANSPORT_ERROR"

	// ── Output and artifact ────────────────────────────────────────────
	// FailureOutputExhausted: the provider hit its output ceiling
	// (finish_reason=length). The delivered bytes are an incomplete prefix by
	// definition. This says NOTHING about whether an artifact was attempted.
	FailureOutputExhausted FailureClass = "OUTPUT_EXHAUSTED"
	// FailureArtifactEmpty: the stream COMPLETED and carried no artifact — prose,
	// an empty fence, or whitespace. The model chose not to answer the contract.
	// Distinct from OUTPUT_EXHAUSTED: exhaustion means it was CUT OFF, this
	// means it finished without speaking the contract.
	FailureArtifactEmpty FailureClass = "ARTIFACT_EMPTY"
	// FailureArtifactInvalid: the provider DID produce an artifact and the
	// parser rejected it (schema violation, unbalanced structure, unterminated
	// block). This is the only artifact failure that licenses a re-prompt about
	// artifact FORMAT.
	FailureArtifactInvalid FailureClass = "ARTIFACT_INVALID"

	// ── Patch application ──────────────────────────────────────────────
	// FailureAnchorNotFound: a patch anchored on content that does not exist in
	// the authoritative target (N=0). The candidate is INVALID. It is never
	// applied approximately — a fuzzy patch application is a mutation nobody
	// authorized.
	FailureAnchorNotFound FailureClass = "ANCHOR_NOT_FOUND"
	// FailureAnchorAmbiguous: a patch anchor matched more than one region (N>1).
	// The candidate is invalid until the region is bounded explicitly.
	FailureAnchorAmbiguous FailureClass = "ANCHOR_AMBIGUOUS"
	// FailureStaleCandidate: the workspace moved between binding and applying, so
	// the candidate describes ground that no longer exists. Abort; never re-apply.
	FailureStaleCandidate FailureClass = "STALE_CANDIDATE"

	// ── Gates ──────────────────────────────────────────────────────────
	// FailureVerificationFailure: a gate ran and the objective's own
	// requirements are still unmet.
	FailureVerificationFailure FailureClass = "VERIFICATION_FAILURE"
	// FailurePreflightInfeasible: the objective cannot be dispatched within its
	// bound. Refused BEFORE any provider request.
	FailurePreflightInfeasible FailureClass = "PREFLIGHT_INFEASIBLE"
	// FailureMutationFailure: an apply or verify gate ran and failed. The ground
	// was rolled back; auto-retrying over it is prohibited.
	FailureMutationFailure FailureClass = "MUTATION_FAILURE"

	// ── Control plane ──────────────────────────────────────────────────
	// FailureProviderRefusal: the provider refused or filtered generation.
	// Never retried — the same request will be refused again.
	FailureProviderRefusal FailureClass = "PROVIDER_REFUSAL"
	// FailureNonProgressing: the runtime attempted the same thing again with no
	// new evidence and it failed the same way. The runtime STOPPED retrying; this
	// is the class that says so. Its existence is what makes "try again" and
	// "this strategy has no new evidence" distinguishable.
	FailureNonProgressing FailureClass = "NON_PROGRESSING_EXECUTION"
	// FailureHardExecutionFailure: a terminal runtime failure with no admissible
	// bounded recovery.
	FailureHardExecutionFailure FailureClass = "HARD_EXECUTION_FAILURE"
)

// AllFailureClasses returns the closed taxonomy in canonical order. A test
// asserts against it so no call site can invent a member locally.
func AllFailureClasses() []FailureClass {
	return []FailureClass{
		FailureTargetNotFound,
		FailureTargetIdentityMismatch,
		FailureTargetAmbiguous,
		FailureCapabilityFailure,
		FailureAuthorizationRequired,
		FailureTransportError,
		FailureOutputExhausted,
		FailureArtifactEmpty,
		FailureArtifactInvalid,
		FailureAnchorNotFound,
		FailureAnchorAmbiguous,
		FailureStaleCandidate,
		FailureVerificationFailure,
		FailurePreflightInfeasible,
		FailureMutationFailure,
		FailureProviderRefusal,
		FailureNonProgressing,
		FailureHardExecutionFailure,
	}
}

// Valid reports whether c is a member of the closed taxonomy.
func (c FailureClass) Valid() bool {
	for _, known := range AllFailureClasses() {
		if known == c {
			return true
		}
	}
	return false
}

// String returns the canonical class label.
func (c FailureClass) String() string { return string(c) }

// RetryPolicy is the bounded recovery ADMISSIBILITY of a failure class. It is the
// single place the runtime answers "is another identical attempt ever justified",
// so no recovery site re-derives it from a substring.
type RetryPolicy string

const (
	// RetryForbidden: no bounded continuation is admissible. The run must
	// replan against different evidence, escalate, or terminate truthfully.
	RetryForbidden RetryPolicy = "FORBIDDEN"
	// RetryOnNewEvidence: a continuation is admissible ONLY if the next attempt
	// carries materially new evidence. An identical re-request is not a retry.
	RetryOnNewEvidence RetryPolicy = "ON_NEW_EVIDENCE_ONLY"
	// RetryIdentical: an identical re-execution is admissible. Reserved for
	// transport-level failures, where the request itself was never delivered.
	RetryIdentical RetryPolicy = "IDENTICAL_OK"
)

// RetryPolicy returns the bounded retry admissibility for a class.
//
// THE INVARIANT. `RetryIdentical` is granted to transport errors and to nothing
// else. A target that does not exist, an anchor that does not match and an
// artifact the model declined to write are all deterministic facts: re-issuing
// the identical request reproduces them exactly, at the cost of another provider
// call. This is the rule that stops `read_file(style.css)` from being executed a
// third time.
func (c FailureClass) RetryPolicy() RetryPolicy {
	switch c {
	case FailureTransportError:
		return RetryIdentical
	case FailureTargetNotFound,
		FailureTargetIdentityMismatch,
		FailureTargetAmbiguous,
		FailureAuthorizationRequired,
		FailureProviderRefusal,
		FailureStaleCandidate,
		FailureMutationFailure,
		FailurePreflightInfeasible,
		FailureNonProgressing,
		FailureHardExecutionFailure:
		return RetryForbidden
	default:
		// OutputExhausted, ArtifactEmpty, ArtifactInvalid, AnchorNotFound,
		// AnchorAmbiguous, VerificationFailure, CapabilityFailure:
		// a continuation is admissible only when it carries new evidence.
		return RetryOnNewEvidence
	}
}

// ExecutionFailure is the TYPED, EVIDENCE-CARRYING record of one execution
// failure. It is what crosses the boundary from "something went wrong" into
// "this specific thing went wrong, for this specific reason".
//
// Every classification MUST carry deterministic evidence explaining why it was
// assigned. A failure with no evidence is an unsubstantiated claim about a
// failure, which is the state this whole file exists to eliminate.
type ExecutionFailure struct {
	// Class is the recovery-facing classification.
	Class FailureClass
	// Evidence is the deterministic, bounded justification. It is derived from
	// runtime-observed facts only: never model prose, never a guess.
	Evidence string
	// Target is the target the failure concerns, EXACTLY as requested. It is
	// never rewritten to a "closest" alternative.
	Target string
	// Capability is the capability the failure came from ("" for provider- or
	// artifact-boundary failures).
	Capability string
	// Canonical is the provider-boundary outcome when the failure crossed that
	// boundary, so a transport fact is never re-derived from a string.
	Canonical CanonicalOutcome
	// FinishReason is the provider's terminal finish_reason, verbatim.
	FinishReason string
	// ProviderState and ArtifactState are the two INDEPENDENT lifecycle facts the
	// classification was derived from. Keeping them as separate fields is what
	// prevents `finish_reason=length` + `zero artifacts parsed` from collapsing
	// into "invalid patch": they are a transport fact and a parser fact, and
	// neither implies the other.
	ProviderState ProviderState
	ArtifactState ArtifactState
	// Count is how many times this identical failure has been observed within
	// the current objective lifecycle. It is what makes NON_PROGRESSING
	// detectable rather than a matter of opinion.
	Count int
}

// Terminal reports whether the class admits no bounded continuation.
func (f ExecutionFailure) Terminal() bool { return f.Class.RetryPolicy() == RetryForbidden }

// RetryAdmissible reports whether another attempt is justified under this
// class's policy. It is a pure function of the class plus the recorded count, so
// no recovery site has to re-derive the rule.
func (f ExecutionFailure) RetryAdmissible() bool {
	switch f.Class.RetryPolicy() {
	case RetryIdentical:
		return true
	case RetryOnNewEvidence:
		return f.Count < 2
	default:
		return false
	}
}

// String renders the compact one-line form used in diagnostics and reasons.
func (f ExecutionFailure) String() string {
	var sb strings.Builder
	sb.WriteString(string(f.Class))
	if f.Target != "" {
		sb.WriteString(" target=")
		sb.WriteString(f.Target)
	}
	if f.Evidence != "" {
		sb.WriteString(": ")
		sb.WriteString(f.Evidence)
	}
	return sb.String()
}

// ── Typed sentinels ─────────────────────────────────────────────────────────
//
// The failures that MUST NOT be re-derived by string matching each get a typed
// error. `errors.Is` is then the classifier, so a diagnostic reworded by a
// provider cannot silently change a recovery decision.

// ErrTargetNotFound is the sentinel for "the requested target does not exist".
// It is the runtime's answer to `style.css` when the scope holds `styles.css`:
// a refusal carrying evidence, never a substitution.
var ErrTargetNotFound = errors.New("execution: TARGET_NOT_FOUND — the requested target does not exist")

// ErrTargetIdentityMismatch is the sentinel for "the request is close to an
// authoritative target but is not it". Separate from ErrTargetNotFound because
// this is exactly the case a fuzzy mapper would have resolved, and the refusal is
// the point.
var ErrTargetIdentityMismatch = errors.New("execution: TARGET_IDENTITY_MISMATCH — the requested target does not match the resolved scope")

// ErrNonProgressingExecution is the sentinel for "this identical attempt already
// failed and nothing changed in between".
var ErrNonProgressingExecution = errors.New("execution: NON_PROGRESSING_EXECUTION — the same request failed again with no new evidence")

// TargetNotFoundError is the STRUCTURED form of a target-identity refusal. It
// carries the full TargetRequest so a trace can show what was asked, what the
// resolved scope is, and what was NOT substituted.
type TargetNotFoundError struct {
	// Request is the complete verdict: the request verbatim, the status, the
	// unchanged scope and the reason.
	Request TargetRequest
	// Mismatch reports whether an in-scope target existed that the request did
	// not name. It distinguishes TARGET_IDENTITY_MISMATCH (a near-miss the
	// runtime refused to resolve) from a plain absence.
	Mismatch bool
}

// Error renders the structured refusal.
func (e *TargetNotFoundError) Error() string {
	if e == nil {
		return "<nil TargetNotFoundError>"
	}
	class := FailureTargetNotFound
	if e.Mismatch {
		class = FailureTargetIdentityMismatch
	}
	return fmt.Sprintf("%s [%s]: %s", class, e.Request.Requested, e.Request.Reason)
}

// Class returns the structured class this error carries.
func (e *TargetNotFoundError) Class() FailureClass {
	if e != nil && e.Mismatch {
		return FailureTargetIdentityMismatch
	}
	return FailureTargetNotFound
}

// Unwrap maps the error onto its typed sentinel so callers may classify with
// errors.Is instead of matching a message.
func (e *TargetNotFoundError) Unwrap() error {
	if e != nil && e.Mismatch {
		return ErrTargetIdentityMismatch
	}
	return ErrTargetNotFound
}

// NewTargetRefusal builds the structured refusal for a non-authorizing target
// verdict. `observed` is the workspace evidence the caller already holds; it is
// used only to decide whether a near-miss exists, never to pick one.
//
// The near-miss test is a pure PREFIX test on the extensionless stem, and it is
// used ONLY to choose between two refusal labels. It can never produce a
// target: `Resolved` is empty in both cases, so nothing is substituted either
// way. That distinction matters — the runtime is allowed to say "you probably
// meant styles.css" as DIAGNOSTIC TEXT, because a human reads it; it is not
// allowed to read it and act on it.
func NewTargetRefusal(req TargetRequest, observed []string) error {
	if req.Readable() {
		return nil
	}
	switch req.Status {
	case TargetRequestAmbiguous:
		return &AmbiguousTargetError{Request: req}
	default:
		return &TargetNotFoundError{
			Request:  req,
			Mismatch: hasNearMissScopeMember(req.Requested, req.Scope),
		}
	}
}

// AmbiguousTargetError is the structured form of a target-ambiguity refusal.
type AmbiguousTargetError struct {
	Request TargetRequest
}

// Error renders the ambiguity refusal.
func (e *AmbiguousTargetError) Error() string {
	if e == nil {
		return "<nil AmbiguousTargetError>"
	}
	return fmt.Sprintf("%s [%s]: %s", FailureTargetAmbiguous, e.Request.Requested, e.Request.Reason)
}

// Class returns the structured class.
func (e *AmbiguousTargetError) Class() FailureClass { return FailureTargetAmbiguous }

// Unwrap maps onto the target-identity sentinel family so an ambiguous request is
// still recognised as a target-identity refusal by errors.Is.
func (e *AmbiguousTargetError) Unwrap() error { return ErrTargetIdentityMismatch }

// hasNearMissScopeMember reports whether the resolved scope holds a target whose
// stem shares a prefix with the requested one (`style` / `styles`).
//
// READ THIS BEFORE CHANGING IT. This predicate selects a DIAGNOSTIC LABEL and
// nothing else. It cannot return a target: the caller ignores its boolean except
// to choose between TARGET_NOT_FOUND and TARGET_IDENTITY_MISMATCH, and both leave
// `Resolved` empty. It exists so an operator reading the trace is told "you named
// a file that differs from one in scope" instead of the less useful "that file
// does not exist" — the human does the matching, and the human is entitled to
// the information.
func hasNearMissScopeMember(requested string, scope []string) bool {
	reqStem := targetStem(requested)
	if reqStem == "" {
		return false
	}
	for _, member := range scope {
		stem := targetStem(member)
		if stem == "" || stem == reqStem {
			continue
		}
		// A genuine near-miss is a shared prefix with a length difference, so
		// `style` vs `styles` qualifies and `styles` vs `styleguide` does not
		// silently collide with every other prefix-sharing name.
		if strings.HasPrefix(stem, reqStem) || strings.HasPrefix(reqStem, stem) {
			return true
		}
	}
	return false
}

// targetStem strips a path's directory and its final extension.
func targetStem(raw string) string {
	s := normalizeTargetRef(raw)
	if s == "" {
		return ""
	}
	if idx := strings.LastIndexByte(s, '/'); idx >= 0 {
		s = s[idx+1:]
	}
	if idx := strings.LastIndexByte(s, '.'); idx > 0 {
		s = s[:idx]
	}
	return strings.ToLower(s)
}
