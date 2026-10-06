package autonomy

// ── Failure ledger: the runtime's memory of what it has already tried ────────
//
// This file owns the answer to the question the recovery matrix could not
// previously ask:
//
//	has this exact failure already happened, under this exact evidence, with no
//	change in between?
//
// THE FAILURE IT REPLACES. `autonomy.Observation` carries AttemptNum and
// RecoveryCycle — COUNTERS, not a record of WHAT failed. Two consecutive
// `read_file(style.css)` calls incremented a counter and therefore looked like
// progress to `RuntimeLoop.recordUsage`, which compares ACTIONS rather than
// FAILURES. A counter cannot distinguish "the same deterministic refusal, twice"
// from "two different problems", so the runtime kept spending provider calls on
// a request whose answer could not change.
//
// WHAT A LEDGER ENTRY IS. A deterministic fingerprint of one failure: its class,
// the target it concerns, and the artifact/anchor identity when applicable. An
// entry with the SAME fingerprint recorded twice, with no intervening new
// evidence, is by definition NON_PROGRESSING_EXECUTION — the strategy has
// produced nothing new and must be replanned or abandoned.
//
// The ledger is OBJECTIVE-SCOPED and reset per run, exactly like the objective
// contract. A dead end under one objective must not constrain a different one.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/execution"
)

// FailureLedger is the per-objective record of observed execution failures.
// It is owned by the Driver and consulted by the recovery matrix before it
// proposes any continuation.
type FailureLedger struct {
	// entries is keyed by the deterministic failure fingerprint.
	entries map[string]*ledgerEntry
	// evidenceEpoch counts distinct workspace/evidence states observed during
	// the objective. A failure recorded under the SAME epoch as its predecessor
	// had no new evidence in between.
	evidenceEpoch int
}

// ledgerEntry is one recorded failure and its observation count.
type ledgerEntry struct {
	// Class is the structured classification assigned when it was first seen.
	// A class that CHANGES on a later observation is genuinely new evidence and
	// resets the count: the request reached a different failure this time.
	Class execution.FailureClass
	// Epoch is the evidence epoch at which the failure was first observed.
	Epoch int
	// Count is how many times this identical failure was observed.
	Count int
	// Evidence is the deterministic justification recorded at first observation.
	Evidence string
}

// newFailureLedger returns an empty ledger. Nil-safe: every method tolerates a
// nil receiver so a zero-value Driver keeps working.
func newFailureLedger() *FailureLedger {
	return &FailureLedger{entries: make(map[string]*ledgerEntry)}
}

// FailureFingerprint is the deterministic identity of one failure.
//
// It is built from the STRUCTURED fields only — class, target, and the
// artifact/anchor identity when one applies. It deliberately does not include
// free-form diagnostic prose: two runs whose messages differ but whose class and
// target agree are the SAME failure, and treating them as different would let a
// reworded diagnostic defeat the whole mechanism.
func FailureFingerprint(f execution.ExecutionFailure) string {
	var sb strings.Builder
	sb.WriteString(f.Class.String())
	sb.WriteString("\x00")
	sb.WriteString(f.Target)
	sb.WriteString("\x00")
	sb.WriteString(f.Canonical.String())
	sb.WriteString("\x00")
	sb.WriteString(f.FinishReason)
	return sb.String()
}

// Observe records a failure and reports whether it is a REPEAT of an identical
// failure already recorded under the same evidence epoch.
//
// It returns the total observation count and whether the failure is now
// non-progressing (observed more than once with nothing changed in between).
func (l *FailureLedger) Observe(f execution.ExecutionFailure) (count int, nonProgressing bool) {
	if l == nil {
		return 0, false
	}
	if l.entries == nil {
		l.entries = make(map[string]*ledgerEntry)
	}
	key := FailureFingerprint(f)
	entry, ok := l.entries[key]
	if !ok {
		l.entries[key] = &ledgerEntry{
			Class:    f.Class,
			Epoch:    l.evidenceEpoch,
			Count:    1,
			Evidence: f.Evidence,
		}
		return 1, false
	}
	if entry.Class != f.Class {
		// A DIFFERENT class is the new evidence that makes another attempt
		// meaningful. The record is replaced rather than counted.
		entry.Class = f.Class
		entry.Epoch = l.evidenceEpoch
		entry.Count = 1
		entry.Evidence = f.Evidence
		return 1, false
	}
	if entry.Epoch != l.evidenceEpoch {
		// The workspace moved. That is real new evidence: the same request may
		// now have a different answer, so the count restarts.
		entry.Epoch = l.evidenceEpoch
		entry.Count = 1
		return 1, false
	}
	entry.Count++
	return entry.Count, entry.Count > 1
}

// AdvanceEvidence records that the workspace or evidence state changed, opening a
// new epoch. It is called by the driver after a mutation lands or after a
// recovery re-observes the workspace.
func (l *FailureLedger) AdvanceEvidence() {
	if l == nil {
		return
	}
	l.evidenceEpoch++
}

// epoch returns the current evidence epoch. It is a nil-safe read of the
// runtime-owned counter, used by the continuation router to fingerprint the
// authoritative evidence state across lifecycle attempts.
func (l *FailureLedger) epoch() int {
	if l == nil {
		return 0
	}
	return l.evidenceEpoch
}

// NonProgressing reports whether a failure fingerprint has been observed more
// than once under the current evidence epoch.
func (l *FailureLedger) NonProgressing(f execution.ExecutionFailure) bool {
	if l == nil {
		return false
	}
	entry, ok := l.entries[FailureFingerprint(f)]
	if !ok {
		return false
	}
	return entry.Count > 1 && entry.Epoch == l.evidenceEpoch
}

// Count returns how many times a failure fingerprint was observed under the
// current evidence epoch.
func (l *FailureLedger) Count(f execution.ExecutionFailure) int {
	if l == nil {
		return 0
	}
	if entry, ok := l.entries[FailureFingerprint(f)]; ok && entry.Epoch == l.evidenceEpoch {
		return entry.Count
	}
	return 0
}

// entriesOrNil exposes the raw entry count for a nil-safe emptiness check
// without leaking the map itself.
func (l *FailureLedger) entriesOrNil() map[string]*ledgerEntry {
	if l == nil {
		return nil
	}
	return l.entries
}

// LedgerReport is the bounded, deterministic rendering of the ledger for a trace
// and a human boundary. It states WHAT failed, HOW OFTEN, and — crucially — that
// repeating it is forbidden, so the reason a run parked is legible.
func (l *FailureLedger) LedgerReport() []string {
	if l == nil || len(l.entries) == 0 {
		return nil
	}
	keys := make([]string, 0, len(l.entries))
	for k := range l.entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		e := l.entries[k]
		verdict := "may retry with new evidence"
		if e.Count > 1 && e.Epoch == l.evidenceEpoch {
			verdict = "NON_PROGRESSING_EXECUTION — replan or terminate"
		}
		line := fmt.Sprintf("%s target=%q seen=%d policy=%s: %s",
			e.Class, firstFingerprintField(k), e.Count, verdict, e.Evidence)
		out = append(out, line)
	}
	return out
}

// firstFingerprintField returns the target component of a fingerprint key.
func firstFingerprintField(key string) string {
	parts := strings.Split(key, "\x00")
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

// ── Observation → structured classification ────────────────────────────────
//
// The bridge from the observation the executor produced to the typed record the
// recovery matrix branches on. Every branch below reads an AUTHORITATIVE field —
// an outcome, a finish_reason, a lifecycle state — and only falls back to the
// diagnostic string when no typed field exists at all.

// ClassifyObservation reduces an observation onto the structured failure
// taxonomy, with deterministic evidence for the assignment.
//
// The precedence is the load-bearing part. In particular:
//
//	finish_reason=length wins over zero-artifacts.
//
// Those are two INDEPENDENT facts. `finish_reason=length` is a TRANSPORT fact
// (the generation was cut off); "zero artifacts parsed" is a PARSER fact (the
// delivered payload held nothing the contract accepts). The previous classifier
// checked zero-artifacts FIRST, so a run that exhausted its budget without
// emitting a parseable artifact was reported as "the model wrote prose" — a
// statement about the model's CHOICE, when the truth was that it never got to
// finish. That mislabel sent the recovery matrix down a same-contract re-prompt
// path, which could only exhaust again.
func ClassifyObservation(o autonomy.Observation) execution.ExecutionFailure {
	f := execution.ExecutionFailure{
		Target:        o.Target,
		FinishReason:  o.FinishReason,
		Canonical:     execution.NormalizeFinishReason(o.FinishReason),
		ProviderState: o.Objective.Provider,
		ArtifactState: o.Objective.Artifact,
	}
	if f.ProviderState == "" {
		f.ProviderState = execution.ProviderPending
	}
	if f.ArtifactState == "" {
		f.ArtifactState = execution.ArtifactNone
	}

	// 1 — Control-plane refusals that never dispatched a provider request. They
	// are decided before any model involvement, so nothing else can explain them.
	switch o.Outcome {
	case autonomy.OutcomePreflightInfeasible:
		f.Class = execution.FailurePreflightInfeasible
		f.Evidence = fmt.Sprintf("preflight refused the objective before any provider request (budget=%d)", o.MaxOutputTokens)
		return f
	case autonomy.OutcomeWorkspaceDrift:
		f.Class = execution.FailureStaleCandidate
		f.Evidence = "the workspace version changed between attempts; the candidate describes ground that no longer exists"
		return f
	case autonomy.OutcomePendingApproval:
		f.Class = execution.FailureAuthorizationRequired
		f.Evidence = "the mutation is held at a human gate"
		return f
	}
	if o.ClarificationRequired {
		f.Class = execution.FailureTargetAmbiguous
		f.Evidence = "the execution requires human clarification before any mutation"
		return f
	}

	// 2 — Target identity, when the executor typed it. This must precede every
	// string-derived branch: a nonexistent target is a fact about the REQUEST,
	// and no amount of finish_reason or diagnostic text changes it.
	if cls, target, ok := typedFailureFromDiagnostic(o.Diagnostic); ok {
		f.Class = cls
		f.Target = target
		f.Evidence = o.Diagnostic
		return f
	}

	// 3 — Provider boundary. TRANSPORT facts are checked before artifact facts
	// because a cut-off generation tells us nothing about what the model would
	// have produced.
	if f.Canonical == execution.CanonicalOutputExhausted {
		f.Class = execution.FailureOutputExhausted
		f.Evidence = fmt.Sprintf(
			"the provider stopped at its output ceiling (finish_reason=%s, budget=%d); "+
				"artifact state is %s, which the transport boundary does not imply",
			o.FinishReason, o.MaxOutputTokens, f.ArtifactState)
		return f
	}
	if f.Canonical == execution.CanonicalProviderRefusal {
		f.Class = execution.FailureProviderRefusal
		f.Evidence = fmt.Sprintf("the provider refused or filtered the generation (finish_reason=%s)", o.FinishReason)
		return f
	}

	// 4 — Patch application. An anchor failure is an ARTIFACT failure about a
	// specific region, and it is only reachable when an artifact was actually
	// parsed — the anchor is in the bytes.
	switch {
	case isHallucinatedAnchor(o):
		f.Class = execution.FailureAnchorNotFound
		f.Evidence = fmt.Sprintf(
			"a patch anchor matched 0 regions of %s; the candidate is invalid and is never applied approximately", o.Target)
		return f
	case isAnchorContinuation(o):
		f.Class = execution.FailureAnchorAmbiguous
		f.Evidence = fmt.Sprintf("a patch anchor matched more than one region of %s", o.Target)
		return f
	case isNonRetryableAmbiguous(o):
		f.Class = execution.FailureAnchorAmbiguous
		f.Evidence = fmt.Sprintf("ambiguous anchors in %s with no line-offset bounds", o.Target)
		return f
	}

	// 5 — Artifact boundary. The distinction between "the model wrote prose" and
	// "the parser rejected a real artifact" is the difference between a
	// contract re-prompt and a format re-prompt; they have different recoveries.
	if isZeroArtifacts(o) {
		f.Class = execution.FailureArtifactEmpty
		f.Evidence = fmt.Sprintf(
			"the provider stream completed carrying no artifact for %s; the artifact state is %s", o.Target, f.ArtifactState)
		return f
	}
	switch o.Outcome {
	case autonomy.OutcomeArtifactRejected, autonomy.OutcomeArtifactRetryableRejected:
		f.Class = execution.FailureArtifactInvalid
		f.Evidence = boundedEvidence(o.Diagnostic, fmt.Sprintf(
			"the parser rejected the artifact produced for %s", o.Target))
		return f
	case autonomy.OutcomeApplyFailed, autonomy.OutcomeSkipped:
		// An apply gate ran and failed. The ground was rolled back, so the
		// disposition is the established one: no automatic retry over it.
		f.Class = execution.FailureMutationFailure
		f.Evidence = boundedEvidence(o.Diagnostic, "an apply gate ran and failed")
		return f
	case autonomy.OutcomeVerifyFailed:
		f.Class = execution.FailureVerificationFailure
		f.Evidence = boundedEvidence(o.Diagnostic, "the objective's own requirements are still unmet")
		return f
	case autonomy.OutcomeNoOpObjectiveUnresolved:
		f.Class = execution.FailureVerificationFailure
		f.Evidence = "a NO_CHANGES_REQUIRED claim conflicted with structural evidence"
		return f
	case autonomy.OutcomeFailed, autonomy.OutcomePatchGenFailed, autonomy.OutcomePatchFailed,
		autonomy.OutcomeCancelled, autonomy.OutcomeRejected:
		// No typed cause survived the boundary: the invocation itself failed.
		// This is the ONLY class for which an identical re-execution is
		// admissible, which is why it must not absorb failures that DID carry a
		// cause — a nonexistent target reaching here is what caused the repeat.
		f.Class = execution.FailureTransportError
		f.Evidence = boundedEvidence(o.Diagnostic, "the invocation failed without a typed cause")
		return f
	}

	f.Class = execution.FailureHardExecutionFailure
	f.Evidence = boundedEvidence(o.Diagnostic, "terminal execution failure with no narrower classification")
	return f
}

// typedFailureFromDiagnostic recognises the TYPED target-identity refusals that
// cross the observation boundary. The executor's structured error text is the
// transport for these (the same mechanism AMBIGUOUS_ANCHOR uses), and matching
// on the TYPE MARKER rather than a free-form message means a reworded sentence
// cannot change the classification.
func typedFailureFromDiagnostic(diagnostic string) (execution.FailureClass, string, bool) {
	if diagnostic == "" {
		return execution.FailureNone, "", false
	}
	switch {
	case strings.Contains(diagnostic, execution.FailureNonProgressing.String()):
		return execution.FailureNonProgressing, "", true
	case strings.Contains(diagnostic, string(execution.FailureTargetIdentityMismatch)):
		return execution.FailureTargetIdentityMismatch, diagnosticTarget(diagnostic), true
	case strings.Contains(diagnostic, string(execution.FailureTargetNotFound)):
		return execution.FailureTargetNotFound, diagnosticTarget(diagnostic), true
	case strings.Contains(diagnostic, string(execution.FailureTargetAmbiguous)):
		return execution.FailureTargetAmbiguous, diagnosticTarget(diagnostic), true
	case strings.Contains(diagnostic, string(execution.FailureAuthorizationRequired)):
		return execution.FailureAuthorizationRequired, "", true
	}
	return execution.FailureNone, "", false
}

// diagnosticTarget extracts the bracketed target a typed refusal names, e.g.
// `TARGET_NOT_FOUND [style.css]: ...`.
func diagnosticTarget(diagnostic string) string {
	start := strings.Index(diagnostic, "[")
	if start < 0 {
		return ""
	}
	end := strings.Index(diagnostic[start:], "]")
	if end < 0 {
		return ""
	}
	return diagnostic[start+1 : start+end]
}

// boundedEvidence prefers the observation's own diagnostic and falls back to a
// deterministic statement when there is none. It never invents detail.
func boundedEvidence(diagnostic, fallback string) string {
	if strings.TrimSpace(diagnostic) == "" {
		return fallback
	}
	if len(diagnostic) > maxLedgerEvidenceBytes {
		return diagnostic[:maxLedgerEvidenceBytes] + "…"
	}
	return diagnostic
}

// maxLedgerEvidenceBytes bounds one ledger evidence string. The ledger is a
// trace artifact and must stay readable in full; a longer diagnostic belongs in
// the observation, not in a per-failure summary.
const maxLedgerEvidenceBytes = 320
