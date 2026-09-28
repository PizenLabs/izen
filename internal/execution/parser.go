// ── Raw model text is NOT an artifact (Phase 14) ────────────────────────────
//
// A model that answers a mutation request with a well-written paragraph has
// done exactly one thing: it stopped emitting tokens. The bytes it left behind
// are prose, not a mutation artifact, and treating them as one writes the
// paragraph to the user's file and reports the objective satisfied. That is
// the single most common false completion in a mutation runtime.
//
// This file is the artifact boundary's parser for the full-artifact (non
// bounded-patch) mutation contracts. It answers one question deterministically:
// does this payload carry a RECOGNIZABLE artifact? A payload that carries no
// artifact contract and reads as natural language is rejected with
// ErrZeroArtifactsParsed, which is a TYPED, REPROMPTABLE outcome — never a
// silent write and never a completion claim.
//
// Three properties matter and are all structural, not heuristic guesses about
// meaning:
//
//  1. RECOGNIZED DELIMITERS ARE AUTHORITATIVE. A fenced artifact, a
//     FILE_CREATE envelope, a SEARCH/REPLACE block, a unified-diff hunk or a
//     :::artifact contract is an artifact by construction. No prose test can
//     reject it.
//  2. PROSE IS REJECTED ONLY WHEN IT IS UNAMBIGUOUS. The prose test requires
//     EVERY non-empty line to be sentence-shaped AND the whole payload to be
//     free of code/document delimiters. Anything ambiguous falls through to
//     the legacy best-effort body resolution — the parser never invents a
//     rejection it cannot justify.
//  3. TRUNCATION IS NOT A PARSE FAILURE. A payload from a stream that ended at
//     finish_reason=length carries a preserved partial artifact and is handled
//     by the continuation path, not by this function. Callers must branch on
//     the finish reason BEFORE calling the parser.
package execution

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// ErrZeroArtifactsParsed is returned when a COMPLETED provider stream carried
// no recognizable mutation artifact and reads as prose. It is the typed
// sentinel the recovery matrix classifies: the correct response is a STRUCTURED
// RE-PROMPT under the SAME artifact contract — never a write, never a
// completion, and never a relabelling of the task (a creation contract stays a
// creation contract).
var ErrZeroArtifactsParsed = errors.New("execution: zero artifacts parsed from a completed provider stream — raw model text is not an artifact")

// ArtifactForm names the recognized mutation-artifact representation a payload
// carried. It is diagnostic metadata: it tells the recovery matrix WHICH
// contract to re-prompt under, so a creation is never re-asked as a patch.
type ArtifactForm string

const (
	// ArtifactFormNone: no artifact delimiter was present.
	ArtifactFormNone ArtifactForm = "none"
	// ArtifactFormContractFence: ":::artifact <path>" … ":::" or a
	// "```lang:path" … "```" contract fence.
	ArtifactFormContractFence ArtifactForm = "contract_fence"
	// ArtifactFormFileCreate: an explicit "<<<<<<< FILE_CREATE <path>" envelope.
	ArtifactFormFileCreate ArtifactForm = "file_create"
	// ArtifactFormSearchReplace: a bounded SEARCH/REPLACE patch.
	ArtifactFormSearchReplace ArtifactForm = "search_replace"
	// ArtifactFormUnifiedDiff: a unified diff with @@ hunk headers.
	ArtifactFormUnifiedDiff ArtifactForm = "unified_diff"
	// ArtifactFormRawBody: an unstructured body accepted as the replacement
	// content. Only reachable when the payload is provably NOT prose.
	ArtifactFormRawBody ArtifactForm = "raw_body"
)

// MutationArtifact is the parsed outcome of one mutation payload.
type MutationArtifact struct {
	// Content is the artifact body the parser resolved. Empty only when the
	// payload carried no artifact at all.
	Content string
	// Form names the recognized representation.
	Form ArtifactForm
	// Structural reports whether the payload carried an explicit artifact
	// delimiter. A structural artifact is authoritative: it is never
	// prose-tested and never rejected here.
	Structural bool
}

// ParseMutationArtifacts resolves a mutation payload into an artifact.
//
// Precedence:
//
//  1. an explicit contract fence / FILE_CREATE envelope / SEARCH/REPLACE block /
//     unified diff  → STRUCTURAL artifact (authoritative);
//  2. an empty payload, or one that reads unambiguously as prose →
//     ErrZeroArtifactsParsed;
//  3. anything else → the payload body itself, accepted best-effort (the legacy
//     full-file resolution) — an ambiguous payload is never rejected.
//
// The delimiter scan runs on the RAW payload before any Markdown fence is
// stripped. That ordering is load-bearing: a "```go:main.go" fence DECLARES a
// target path, and stripping the fence first would erase the contract that
// makes the payload an artifact at all.
func ParseMutationArtifacts(raw string) (MutationArtifact, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return MutationArtifact{Form: ArtifactFormNone},
			fmt.Errorf("%w: the provider stream completed with an empty payload", ErrZeroArtifactsParsed)
	}
	if form, ok := RecognizeArtifactForm(trimmed); ok {
		return MutationArtifact{Form: form, Structural: true}, nil
	}
	body := stripOuterFence(trimmed)
	if strings.TrimSpace(body) == "" {
		return MutationArtifact{Form: ArtifactFormNone},
			fmt.Errorf("%w: the provider stream completed with an empty payload", ErrZeroArtifactsParsed)
	}
	// The fence strip may have revealed a contract (a fence whose info string
	// only became visible once the outer wrapper was removed).
	if form, ok := RecognizeArtifactForm(body); ok {
		return MutationArtifact{Form: form, Structural: true}, nil
	}
	if LooksLikeProse(body) {
		return MutationArtifact{Form: ArtifactFormNone}, fmt.Errorf(
			"%w: the provider stream completed with %d line(s) of natural-language text and no artifact contract",
			ErrZeroArtifactsParsed, len(nonEmptyLines(body)))
	}
	return MutationArtifact{Content: body, Form: ArtifactFormRawBody}, nil
}

// RecognizeArtifactForm reports the explicit artifact representation a payload
// carries, if any. It is a pure delimiter scan: no prose test participates, so
// a fenced payload is never second-guessed.
func RecognizeArtifactForm(body string) (ArtifactForm, bool) {
	switch {
	case strings.Contains(body, "<<<"), strings.Contains(body, ">>>"):
		// FILE_CREATE envelopes also use the marker syntax; the SEARCH/REPLACE
		// probe below is the discriminator, so test the more specific form
		// first.
		if strings.Contains(body, "<<<<<<< SEARCH") {
			return ArtifactFormSearchReplace, true
		}
		if strings.Contains(body, "FILE_CREATE") {
			return ArtifactFormFileCreate, true
		}
		return ArtifactFormSearchReplace, true
	case strings.Contains(body, ":::artifact"):
		return ArtifactFormContractFence, true
	case strings.Contains(body, "@@"):
		return ArtifactFormUnifiedDiff, true
	}
	// A "```lang:path" fence declares a target path and is therefore an
	// explicit artifact contract.
	if isPathDeclaringFence(body) {
		return ArtifactFormContractFence, true
	}
	return ArtifactFormNone, false
}

// isPathDeclaringFence reports whether the payload opens a fence whose info
// string names a path ("```go:main.go"), which is the strict V3 artifact
// contract. A bare "```" fence is prose formatting and proves nothing.
func isPathDeclaringFence(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "```") || trimmed == "```" {
			continue
		}
		info := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
		if info == "" {
			continue
		}
		// "lang:path" carries a path; a bare language tag ("go") does not.
		if idx := strings.Index(info, ":"); idx > 0 && idx < len(info)-1 {
			return true
		}
	}
	return false
}

// stripOuterFence removes a single wrapping Markdown code fence so the payload
// body is examined, not its formatting. It never inspects inner fences.
func stripOuterFence(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	rest := strings.TrimPrefix(trimmed, "```")
	if idx := strings.Index(rest, "\n"); idx >= 0 {
		rest = rest[idx+1:]
	} else {
		return ""
	}
	rest = strings.TrimSuffix(strings.TrimSpace(rest), "```")
	return strings.TrimSpace(rest)
}

// nonEmptyLines returns the payload's non-blank lines.
func nonEmptyLines(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

// codeDelimiters are the character pairs a code or markup body carries and a
// paragraph of prose essentially never does. Their ABSENCE is the first half
// of the prose test: a payload containing any of them is never classified as
// prose, so a legitimate document can never be rejected for lacking prose.
var codeDelimiters = []string{
	"{", "}", "(", ")", "[", "]", "=", ";", "<", ">", "=>", ":=",
	"#include", "import ", "package ", "func ", "def ", "class ",
	"return ", "public ", "private ", "const ", "var ", "let ", "function ",
	"</", "/>", "<!--", "<?", ":::", "|>", "->",
}

// proseOpeners are the canonical English sentence openers. A line that starts
// with one of them is a sentence, not a code statement.
var proseOpeners = []string{
	"the ", "this ", "that ", "these ", "those ", "there ", "here ", "i ", "we ",
	"you ", "it ", "they ", "he ", "she ", "a ", "an ", "my ", "our ", "your ",
	"to ", "in ", "on ", "for ", "if ", "when ", "as ", "and ", "or ", "but ",
	"however", "unfortunately", "sorry", "done", "ok", "okay", "sure", "note",
	"explanation", "summary", "changes", "what ", "why ", "how ", "which ",
}

// proseMarkers are the Markdown structural markers a rendered answer carries.
var proseMarkers = []string{"#", "- ", "* ", "> ", "1. ", "2. ", "3. ", "**", "| "}

// explanatoryMarkers are the phrases that make prose META-PROSE: text talking
// ABOUT the requested change rather than BEING the change. A file body can
// contain any English word; only an answer that addresses the request in the
// second person, reports what it did, or names its own refusal is unmistakably
// an explanation.
var explanatoryMarkers = []string{
	"i ", "i'", "i’ve", "i've", "i have", "i will", "i can", "i cannot",
	"we ", "we'", "we’ve", "we've", "we have", "you ", "your ", "you're",
	"let me", "here is", "here are", "here's", "below is", "below are",
	"the following", "as requested", "as per your", "note that", "please note",
	"summary:", "changes:", "changes made", "what i ", "what we ",
	"explanation", "explanation:", "sorry", "unfortunately", "i'm afraid",
	"the file ", "this file ", "your request", "the request ",
}

// MetaAcknowledgements is the CLOSED vocabulary of pure acknowledgement and
// refusal responses. Each entry is a complete answer in its own right and
// carries no artifact whatsoever — writing any of them to a user's file is the
// false completion in its most compact form.
var MetaAcknowledgements = map[string]bool{
	"done": true, "done.": true, "ok": true, "ok.": true, "okay": true, "okay.": true,
	"k": true, "k.": true, "yes": true, "yes.": true, "no": true, "no.": true,
	"acknowledged": true, "acknowledged.": true, "applied": true, "applied.": true,
	"completed": true, "completed.": true, "success": true, "success.": true,
	"finished": true, "finished.": true, "sure": true, "sure.": true, "thanks": true,
	"all set": true, "all done": true, "nothing to do": true, "no changes": true,
	"no changes needed": true, "no changes required": true, "not needed": true,
	"i cannot do that": true, "i can't do that": true, "i cannot help with that": true,
	"i can't help with that": true, "i'm unable to": true, "unable to comply": true,
}

// Prose thresholds. The essay rule is deliberately demanding: a payload must
// clear EVERY bar to be classified as an explanation, so a short or ambiguous
// document body is never rejected for lack of code punctuation.
const (
	// MinProseLines is the smallest number of non-blank lines an essay can have.
	MinProseLines = 2
	// MinProseBytes is the smallest body an essay can have. Below this, a
	// payload is a terse document, not an explanation.
	MinProseBytes = 120
)

// LooksLikeProse reports whether a payload reads as natural-language prose
// rather than an artifact body. It is the CONJUNCTION of two independent rules,
// and neither can fire alone:
//
//	(A) META-ACKNOWLEDGEMENT — the whole payload is a member of the closed
//	    acknowledgement/refusal vocabulary. Exact, vocabulary-based, no
//	    heuristics.
//
//	(B) ESSAY — the payload is substantial (≥ MinProseLines lines AND
//	    ≥ MinProseBytes bytes), free of every code/document delimiter, made up
//	    ENTIRELY of sentence-shaped lines, AND carries at least one
//	    explanatory marker (second-person address, a report of what was done,
//	    or a self-declared refusal).
//
// Rule (B)'s four conjunctions are what make it safe to reject on: a single
// ambiguous line, one stray bracket, or a missing meta-reference is enough to
// make the payload an artifact. The false rejection it can produce is bounded
// to long, punctuation-free, entirely-English, second-person answers — exactly
// the "the model wrote me an essay instead of the file" case the invariant
// exists to stop.
func LooksLikeProse(body string) bool {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return false
	}
	if MetaAcknowledgements[strings.ToLower(trimmed)] {
		return true
	}
	return looksLikeEssay(trimmed)
}

// looksLikeEssay is rule (B) of LooksLikeProse.
func looksLikeEssay(body string) bool {
	lines := nonEmptyLines(body)
	if len(lines) < MinProseLines || len(body) < MinProseBytes {
		return false
	}
	for _, delimiter := range codeDelimiters {
		if strings.Contains(body, delimiter) {
			return false
		}
	}
	for _, line := range lines {
		if !sentenceShaped(line) {
			return false
		}
	}
	return hasExplanatoryMarker(body)
}

// hasExplanatoryMarker reports whether the body addresses the request about the
// change rather than being the change.
func hasExplanatoryMarker(body string) bool {
	lower := " " + strings.ToLower(body) + " "
	for _, marker := range explanatoryMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// sentenceShaped reports whether one line reads as a sentence.
func sentenceShaped(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	for _, marker := range proseMarkers {
		if strings.HasPrefix(lower, marker) {
			return true
		}
	}
	for _, opener := range proseOpeners {
		if strings.HasPrefix(lower, opener) {
			return true
		}
	}
	// A closing-sentence line: terminal punctuation followed by a word run.
	if endsSentence(trimmed) && wordCount(trimmed) >= 4 {
		return true
	}
	return false
}

// sentenceTerminators are the marks that end a written sentence.
const sentenceTerminators = ".!?;:"

// endsSentence reports whether the line closes a sentence: its final rune is
// sentence-terminating punctuation.
//
// The "node.js" false positive is handled by the CALLER, not here: a filename
// line is one or two words, so it can never satisfy the ≥4-word clause, and
// every line in the payload must be sentence-shaped before the payload counts
// as prose at all.
func endsSentence(line string) bool {
	runes := []rune(line)
	if len(runes) == 0 {
		return false
	}
	return strings.ContainsRune(sentenceTerminators, runes[len(runes)-1])
}

// wordCount counts the space-separated word runs of a line.
func wordCount(line string) int {
	n := 0
	inWord := false
	for _, r := range line {
		switch {
		case unicode.IsSpace(r):
			inWord = false
		case !inWord:
			inWord = true
			n++
		}
	}
	return n
}

// ZeroArtifactRepromptDirective is the structured recovery instruction the
// runtime appends when a mutation attempt produced prose. It re-states the SAME
// artifact contract the attempt was dispatched under; it never proposes a
// different artifact shape, and in particular it never converts a creation into
// a patch.
//
// `form` is the attempt's own artifact contract ("create_file",
// "search_replace", "full_file", …). The directive names THAT contract so the
// successor attempt is a re-issue, not a re-scope.
func ZeroArtifactRepromptDirective(target, form string) string {
	normalized := strings.ToLower(strings.TrimSpace(form))
	contract := "the artifact in its declared " + form + " form"
	switch {
	case normalized == "":
		contract = "the artifact for " + target
	case creationShape(normalized):
		// A creation has no existing content to anchor a patch against, so the
		// directive asks for the file body and nothing else.
		contract = "the complete new file content for " + target
	case normalized == string(ArtifactFormSearchReplace) || normalized == "replace_block":
		contract = "exactly one SEARCH/REPLACE block for " + target
	case normalized == string(ArtifactFormFileCreate):
		contract = "a <<<<<<< FILE_CREATE " + target + " … >>>>>>> END_FILE envelope"
	case normalized == string(ArtifactFormUnifiedDiff):
		contract = "a unified diff with @@ hunk headers for " + target
	}
	return "[ARTIFACT CONTRACT VIOLATION] The response was natural-language prose, " +
		"which is not an artifact. Re-emit " + contract + " with no surrounding " +
		"explanation. Explanation text is discarded at the artifact boundary; only " +
		"the artifact bytes reach the workspace."
}
