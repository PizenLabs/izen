package execution

import (
	"fmt"
	"regexp"
	"strings"
)

// ── NO-OP semantic classification (deterministic structural analysis) ───────
//
// A NO_CHANGES_REQUIRED answer is a MODEL CLAIM. Treating every raw claim as a
// terminal success created a false-success boundary: the runtime completed a
// DAG cleanly without verifying whether the user's semantic intent was
// actually satisfied.
//
// This file owns the deterministic, provider-free verification layer between
// the raw claim (ExtractNoOpClaim) and the terminal outcome vocabulary
// (OutcomeNoOpObjectiveSatisfied / OutcomeNoOpNoSafeMutation /
// OutcomeNoOpObjectiveUnresolved). The analysis is heuristic BY DESIGN and
// always fails toward human review — it never invents work and never
// confirms success against contradicting structural evidence.

// NoOpVerdict is the tri-state semantic classification of a no-op claim.
type NoOpVerdict string

const (
	// NoOpObjectiveSatisfied: structural analysis found zero
	// counter-evidence — the objective's structural signature is absent from
	// the assigned window, so "no change required" stands (terminal success).
	NoOpObjectiveSatisfied NoOpVerdict = "NO_OP_OBJECTIVE_SATISFIED"
	// NoOpNoSafeMutation: candidate edit regions matching the objective were
	// detected, but confidence is below the safety threshold to
	// auto-classify. Requires human/model review (terminal warning).
	NoOpNoSafeMutation NoOpVerdict = "NO_OP_NO_SAFE_MUTATION"
	// NoOpObjectiveUnresolved: the objective's structural signature is still
	// present in the assigned window — the claim conflicts with the
	// observable workspace state (escalation trigger).
	NoOpObjectiveUnresolved NoOpVerdict = "NO_OP_OBJECTIVE_UNRESOLVED"
)

// VerdictToOutcome maps a classification verdict onto its canonical
// MutationOutcome sub-state.
func VerdictToOutcome(v NoOpVerdict) MutationOutcome {
	switch v {
	case NoOpObjectiveSatisfied:
		return OutcomeNoOpObjectiveSatisfied
	case NoOpNoSafeMutation:
		return OutcomeNoOpNoSafeMutation
	case NoOpObjectiveUnresolved:
		return OutcomeNoOpObjectiveUnresolved
	default:
		return OutcomeNoOpNoSafeMutation
	}
}

// String returns the canonical verdict label.
func (v NoOpVerdict) String() string { return string(v) }

// NoOpAssessment is the deterministic classification record for one raw
// no-op claim. It travels on the executor's typed error so downstream
// consumers (autonomy escalation, evidence sealing) read the verdict and its
// bounded rationale without re-deriving either.
type NoOpAssessment struct {
	// Verdict is the classified sub-state of the claim.
	Verdict NoOpVerdict
	// Reason is the bounded deterministic rationale (what matched, what did
	// not). It never carries raw file bytes.
	Reason string
}

// noopDirectiveRe matches removal/de-duplication directives inside an
// objective: remove/delete/strip/drop/eliminate + an explicit payload in
// quotes or backticks, plus redundancy language (duplicate/redundant/repeat).
var (
	noopQuotedPayloadsRe = regexp.MustCompile(
		"(?i)(?:remove|delete|strip|drop|eliminate)[^\\n`\"']*[`\"']([^`\\n\"']{3,120})[`\"']")
	noopRedundancyRe = regexp.MustCompile(`(?i)\b(deduplicat|duplicate[sd]?|redundan|repeated)\b`)
)

// noopMinPayloadRun is the shortest meaningful line-content run counted as a
// duplicate candidate; shorter lines are boilerplate noise ("{", ")", "//").
const noopMinPayloadRun = 8

// ClassifyNoOpClaim classifies a raw model no-op claim against the content
// the claim is about. objective is the prompt text the model answered for;
// sliceContent is exactly the context the model judged (the bounded patch
// window under the SEARCH/REPLACE contract, otherwise the whole target).
//
// Decision matrix (total, deterministic, conservative):
//
//	removal directive with explicit payloads:
//	  any payload present verbatim            → UNRESOLVED  (work remains)
//	  only fuzzy/partial matches              → NO_SAFE_MUTATION (below threshold)
//	  all payloads absent                     → SATISFIED   (already gone)
//	redundancy directive (dedupe/redundant):
//	  duplicated meaningful runs present      → UNRESOLVED  (redundancy remains)
//	  none                                    → SATISFIED
//	no parseable structural directive         → SATISFIED   (claim uncontradicted)
//
// The final row preserves the historical contract: when the objective gives
// the analyzer nothing structural to check, the raw claim is honored.
func ClassifyNoOpClaim(objective, sliceContent string) NoOpAssessment {
	if assess := assessRemovalDirectives(objective, sliceContent); assess.Verdict != "" {
		return assess
	}
	if assess := assessRedundancyDirective(objective, sliceContent); assess.Verdict != "" {
		return assess
	}
	return NoOpAssessment{
		Verdict: NoOpObjectiveSatisfied,
		Reason:  "objective carries no parseable structural directive; the raw claim stands uncontradicted",
	}
}

// assessRemovalDirectives evaluates quoted removal payloads. It returns a
// zero Verdict when the objective names no removable payload.
func assessRemovalDirectives(objective, sliceContent string) NoOpAssessment {
	matches := noopQuotedPayloadsRe.FindAllStringSubmatch(objective, -1)
	if len(matches) == 0 {
		return NoOpAssessment{}
	}
	var (
		exact, fuzzy, missing []string
	)
	for _, m := range matches {
		payload := strings.TrimSpace(m[1])
		if payload == "" {
			continue
		}
		switch classifyPayload(payload, sliceContent) {
		case payloadExact:
			exact = append(exact, payload)
		case payloadFuzzy:
			fuzzy = append(fuzzy, payload)
		default:
			missing = append(missing, payload)
		}
	}
	switch {
	case len(exact) > 0:
		return NoOpAssessment{
			Verdict: NoOpObjectiveUnresolved,
			Reason: fmt.Sprintf("targeted content still present in the assigned slice: %q",
				exact[0]),
		}
	case len(fuzzy) > 0 && len(missing) > 0:
		return NoOpAssessment{
			Verdict: NoOpNoSafeMutation,
			Reason: fmt.Sprintf("partial structural match (%d/%d payloads): %q matches only approximately; confidence below safety threshold",
				len(fuzzy), len(fuzzy)+len(missing), fuzzy[0]),
		}
	case len(fuzzy) > 0:
		return NoOpAssessment{
			Verdict: NoOpNoSafeMutation,
			Reason: fmt.Sprintf("payload %q matches only after normalization (case/whitespace drift); confidence below safety threshold",
				fuzzy[0]),
		}
	default:
		return NoOpAssessment{
			Verdict: NoOpObjectiveSatisfied,
			Reason:  fmt.Sprintf("all %d targeted payload(s) absent from the assigned slice", len(missing)),
		}
	}
}

type payloadMatch int

const (
	payloadMissing payloadMatch = iota
	payloadFuzzy
	payloadExact
)

// classifyPayload checks one removal payload against the slice: exact
// byte-for-byte presence, normalized (lowercased, whitespace-collapsed)
// presence, or absence.
func classifyPayload(payload, sliceContent string) payloadMatch {
	if strings.Contains(sliceContent, payload) {
		return payloadExact
	}
	normSlice := noopNormalize(sliceContent)
	if strings.Contains(normSlice, noopNormalize(payload)) {
		return payloadFuzzy
	}
	// Word-level fallback: multi-token payloads whose words all appear
	// nearby count as approximate matches, never as exact ones.
	if noopWordHit(payload, normSlice) {
		return payloadFuzzy
	}
	return payloadMissing
}

// noopNormalize lowercases and collapses whitespace runs so formatting-only
// drift does not mask a real match.
func noopNormalize(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// noopWordHit reports whether every word of a multi-word payload appears in
// the normalized slice (approximate containment for punctuation-heavy
// payloads such as function call sites).
func noopWordHit(payload, normSlice string) bool {
	words := strings.Fields(noopNormalize(payload))
	if len(words) < 2 {
		return false
	}
	for _, w := range words {
		if !strings.Contains(normSlice, w) {
			return false
		}
	}
	return true
}

// assessRedundancyDirective evaluates deduplication objectives: duplicated
// meaningful line runs inside the slice are the structural signature the
// claim is checked against.
func assessRedundancyDirective(objective, sliceContent string) NoOpAssessment {
	if !noopRedundancyRe.MatchString(objective) {
		return NoOpAssessment{}
	}
	dupes := duplicatedRuns(sliceContent)
	if len(dupes) == 0 {
		return NoOpAssessment{
			Verdict: NoOpObjectiveSatisfied,
			Reason:  "deduplication objective: no duplicated content runs remain in the assigned slice",
		}
	}
	return NoOpAssessment{
		Verdict: NoOpObjectiveUnresolved,
		Reason: fmt.Sprintf("redundant content still present: %q occurs %d times in the assigned slice",
			truncateNoopEvidence(dupes[0].line), dupes[0].count),
	}
}

type duplicateRun struct {
	line  string
	count int
}

// duplicatedRuns returns the duplicated meaningful line runs of the content,
// most frequent first. Blank lines, single characters and very short runs
// are ignored (structural noise, not redundancy candidates).
func duplicatedRuns(content string) []duplicateRun {
	counts := make(map[string]int)
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) < noopMinPayloadRun {
			continue
		}
		counts[trimmed]++
	}
	var dupes []duplicateRun
	for line, n := range counts {
		if n > 1 {
			dupes = append(dupes, duplicateRun{line: line, count: n})
		}
	}
	// Deterministic order: highest count, then lexicographic.
	for i := 1; i < len(dupes); i++ {
		for j := i; j > 0 && (dupes[j].count > dupes[j-1].count ||
			(dupes[j].count == dupes[j-1].count && dupes[j].line < dupes[j-1].line)); j-- {
			dupes[j], dupes[j-1] = dupes[j-1], dupes[j]
		}
	}
	return dupes
}

// truncateNoopEvidence bounds an evidence excerpt carried in an assessment
// reason (bounded diagnostics travel; raw slices never do).
func truncateNoopEvidence(s string) string {
	const max = 60
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
}
