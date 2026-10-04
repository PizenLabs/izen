package execution

// ── TARGET IDENTITY AUTHORITY ───────────────────────────────────────────────
//
// This file owns one invariant and nothing else:
//
//	A capability request for a target is answered by COMPARISON against
//	authoritative evidence, never by similarity to it.
//
// THE FAILURE THIS EXISTS TO PREVENT. A resolved scope of
// `[index.html, script.js, styles.css]` and a model request for `style.css` used
// to produce a failed read whose cause was invisible: the runtime could not say
// "this path does not exist" because it never compared the request against the
// scope. The read failed, the failure was reported as a generic capability
// failure, and the loop retried the identical request.
//
// WHY NO MATCHING STRATEGY IS OFFERED. Every approximate strategy — suffix,
// extension, edit distance, "closest file", basename, model intent — has the
// same defect: it can authorize a mutation against a file the operator never
// named. `style.css` → `styles.css` is a plausible typo, but so is
// `styles.css` → `style.css` in a repo that has both, and so is
// `main.go` → `domain.go` when only one of them is in scope. A runtime that
// cannot tell those apart must not guess on the user's filesystem.
//
// So `ResolveTargetRequest` is exact. When the answer is not an exact member of
// the authoritative set, the request is a TYPED REFUSAL carrying the evidence
// that produced it, and the resolved scope is returned UNCHANGED.

import (
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// TargetRequestStatus is the deterministic verdict for one capability request
// against the authoritative resolved scope. It is a CLOSED vocabulary: the
// runtime must be able to say which of these it is, and a call site that needs
// a seventh meaning must add it here deliberately.
//
// The five members correspond exactly to the five questions the runtime owes an
// operator about any target a model asked for.
type TargetRequestStatus string

const (
	// TargetRequestInScope: the request is an exact member of the authoritative
	// resolved scope. This is the ONLY status that authorizes the operation.
	TargetRequestInScope TargetRequestStatus = "IN_SCOPE"
	// TargetRequestObserved: the request is NOT in the resolved scope but IS
	// present in observed workspace evidence. It is a real file the run may read
	// as CONTEXT; it is not a mutation target for this objective.
	TargetRequestObserved TargetRequestStatus = "OBSERVED"
	// TargetRequestNotFound: the request is neither in scope nor observed. The
	// named target does not exist. Nothing is substituted.
	TargetRequestNotFound TargetRequestStatus = "NOT_FOUND"
	// TargetRequestOutsideScope: the request escapes the workspace root, or is a
	// directory, or is otherwise not a usable file path at all.
	TargetRequestOutsideScope TargetRequestStatus = "OUTSIDE_SCOPE"
	// TargetRequestAmbiguous: more than one resolved-scope member is a possible
	// referent and nothing proved which was meant. A question, never a decision.
	TargetRequestAmbiguous TargetRequestStatus = "AMBIGUOUS"
)

// Authorizing reports whether the status permits a MUTATION against the request.
// Only an exact scope member does; every other status is a refusal carrying
// evidence.
func (s TargetRequestStatus) Authorizing() bool { return s == TargetRequestInScope }

// Readable reports whether the status permits a READ-ONLY operation.
//
// The distinction is load-bearing. A target that exists in the workspace but is
// not in this objective's resolved scope is legitimate CONTEXT: a runtime
// assembling a plan for `styles.css` is entitled to read `index.html`. Refusing
// it would make observation impossible, and would be a different kind of wrong —
// a cautious runtime is not a blind one.
//
// Reading is not writing. Readability never implies mutation authority: only
// TargetRequestInScope carries that, and it is `Authorizing` that the mutation
// boundary consults.
func (s TargetRequestStatus) Readable() bool {
	return s == TargetRequestInScope || s == TargetRequestObserved
}

// String returns the canonical status label.
func (s TargetRequestStatus) String() string { return string(s) }

// AllTargetRequestStatuses returns the closed vocabulary in canonical order. It
// exists so a test can assert no call site invented a sixth meaning.
func AllTargetRequestStatuses() []TargetRequestStatus {
	return []TargetRequestStatus{
		TargetRequestInScope,
		TargetRequestObserved,
		TargetRequestNotFound,
		TargetRequestOutsideScope,
		TargetRequestAmbiguous,
	}
}

// TargetRequest is the complete, evidence-carrying verdict on one requested
// target. It answers WHAT was asked, WHAT the runtime concluded, and WHY — so a
// trace can show the decision instead of a reader inferring it from a failure.
type TargetRequest struct {
	// Requested is the target EXACTLY as the model asked for it. It is never
	// rewritten: a request for `style.css` is preserved as `style.css` in the
	// evidence even when `styles.css` exists.
	Requested string
	// Status is the deterministic verdict.
	Status TargetRequestStatus
	// Resolved is the authoritative target this request names. It is populated
	// ONLY for an exact match. It is empty for every refusal, which is what
	// makes "no substitution happened" checkable rather than assumed.
	Resolved string
	// Scope is the authoritative resolved scope, verbatim and unchanged. A
	// refused request returns it so the caller can state what WAS authorized.
	Scope []string
	// Reason is the deterministic one-sentence justification.
	Reason string
}

// Authorized reports whether the request may MUTATE Resolved. Only an exact
// scope member qualifies.
func (r TargetRequest) Authorized() bool {
	return r.Status.Authorizing() && strings.TrimSpace(r.Resolved) != ""
}

// Readable reports whether the request may be READ. An in-scope target and an
// observed-but-out-of-scope target both qualify; neither implies mutation
// authority.
func (r TargetRequest) Readable() bool { return r.Status.Readable() }

// normalizeTargetRef reduces a target reference to its comparable workspace
// form. It strips decoration (@, quotes, ./ ) and unifies separators so that
// `./styles.css`, `@styles.css` and `styles.css` are the SAME target — a
// formatting difference, not a different file.
//
// It performs NO case folding on the significant path and NO suffix or extension
// manipulation. Only path-shape normalization, which is what makes comparison
// exact rather than approximate.
func normalizeTargetRef(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.Trim(s, "`\"'")
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "@")
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = filepath.ToSlash(s)
	s = path.Clean(s)
	s = strings.TrimPrefix(s, "./")
	s = strings.TrimPrefix(s, "/")
	return s
}

// sameTarget reports whether two references denote the same target after
// normalization. It is EXACT equality of the normalized form — the whole point
// of the file. A comment states the invariant so a future "helpful" relaxation
// cannot be added silently.
func sameTarget(a, b string) bool {
	na, nb := normalizeTargetRef(a), normalizeTargetRef(b)
	if na == "" || nb == "" {
		return false
	}
	// INVARIANT: exact equality only. No suffix, extension, edit-distance,
	// basename or "closest file" comparison belongs here. Adding one would
	// silently authorize a target the operator never named.
	return na == nb
}

// TargetScopeEvidence is the workspace-side fact a target request is judged
// against. It is supplied by the caller that already owns workspace discovery
// (the executor's TargetResolver, the adapter) so this file stays pure and
// testable without a filesystem.
type TargetScopeEvidence struct {
	// Scope is the authoritative resolved target set. Only an exact member of it
	// authorizes a mutation.
	Scope []string
	// Observed names paths the runtime has actually seen in the workspace. A
	// member is real evidence, never authority.
	Observed []string
	// Exists reports whether a path exists on disk. Nil means "not observed",
	// which is NOT the same as "does not exist" — the distinction is the whole
	// reason Observed and Scope are separate inputs.
	Exists func(path string) bool
	// IsDir reports whether a path is a directory. A directory is never a file.
	IsDir func(path string) bool
}

// ResolveTargetRequest classifies one requested target against authoritative
// evidence. It is a PURE function of its inputs and never touches a filesystem
// directly: existence and directory-ness arrive through the supplied probes, and
// when no probe is supplied the runtime reports what it can prove and nothing
// more.
//
// The resolution order is deliberate and total:
//
//	not a usable path            → OUTSIDE_SCOPE
//	directory statement          → OUTSIDE_SCOPE   (a directory is never a file)
//	exact member of the scope    → IN_SCOPE        (authorized)
//	exact observed, not in scope → OBSERVED        (context, never a target)
//	otherwise                    → NOT_FOUND       (nothing is substituted)
//
// AMBIGUOUS is reserved for the one case that cannot be answered by membership:
// an empty request with a multi-element scope, where "the target" has no single
// referent and guessing would be indistinguishable from inventing one.
func ResolveTargetRequest(requested string, ev TargetScopeEvidence) TargetRequest {
	req := strings.TrimSpace(requested)
	norm := normalizeTargetRef(req)
	out := TargetRequest{Requested: req, Scope: append([]string(nil), ev.Scope...)}

	if norm == "" {
		if len(ev.Scope) > 1 {
			out.Status = TargetRequestAmbiguous
			out.Reason = "no target was named and the resolved scope holds " +
				strconv.Itoa(len(ev.Scope)) + " targets; the request has no single referent"
			return out
		}
		out.Status = TargetRequestOutsideScope
		out.Reason = "no target was named"
		return out
	}

	// A directory is never promoted to a file. Checked before membership so a
	// directory named in the scope cannot be read as a file.
	if ev.IsDir != nil && ev.IsDir(norm) {
		out.Status = TargetRequestOutsideScope
		out.Reason = req + " is a directory, not a file"
		return out
	}

	// AUTHORIZATION: exact membership in the authoritative resolved scope.
	for _, member := range ev.Scope {
		if sameTarget(member, norm) {
			out.Status = TargetRequestInScope
			out.Resolved = normalizeTargetRef(member)
			out.Reason = req + " is an exact member of the resolved scope"
			return out
		}
	}

	// An EMPTY scope means the run holds no mutation target at all. That is not a
	// refusal: a read-only objective legitimately has none, and reading a real
	// workspace file is exactly what it should be able to do. Existence decides,
	// and nothing here can authorize a mutation.
	if len(ev.Scope) == 0 {
		if ev.Exists != nil && ev.Exists(norm) {
			out.Status = TargetRequestObserved
			out.Resolved = norm
			out.Reason = req + " exists in the workspace and this run holds no resolved scope; " +
				"it is readable as context"
			return out
		}
		out.Status = TargetRequestNotFound
		out.Reason = req + " does not exist in the workspace and this run holds no resolved scope"
		return out
	}

	// OBSERVED: real workspace evidence, but not this objective's target. The
	// runtime can read it as CONTEXT; it is never a mutation target here.
	//
	// Two evidence sources are consulted, in order: the observed set the caller
	// already holds, and the existence probe. The probe is what makes a caller
	// that bound no observed list still able to tell "this file exists" from
	// "this file does not" — otherwise an unbound caller would classify every
	// real file as absent, which is a false statement rather than a cautious one.
	for _, seen := range ev.Observed {
		if sameTarget(seen, norm) {
			out.Status = TargetRequestObserved
			out.Reason = req + " exists in workspace evidence but is not in the resolved scope"
			return out
		}
	}
	if ev.Exists != nil && ev.Exists(norm) {
		out.Status = TargetRequestObserved
		out.Reason = req + " exists in the workspace but is not in the resolved scope; " +
			"it is readable as context and is not a mutation target for this objective"
		return out
	}

	// NOT_FOUND. The resolved scope is returned UNCHANGED and Resolved stays
	// empty: nothing was substituted, and this is checkable rather than assumed.
	out.Status = TargetRequestNotFound
	if len(ev.Scope) == 0 {
		out.Reason = req + " does not exist and the run holds no resolved scope"
		return out
	}
	out.Reason = req + " does not exist; the resolved scope is [" +
		strings.Join(ev.Scope, ",") + "] and is unchanged"
	return out
}
