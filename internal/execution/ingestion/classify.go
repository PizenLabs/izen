package ingestion

import (
	"regexp"
	"strings"
)

// PayloadClass classifies the integrity/disposition of a normalized payload
// before it crosses the L1 Execution Gate.
type PayloadClass string

const (
	// ClassValidPayload: the raw output already matched the transport contract
	// (no normalization was required) and it passes basic envelope integrity.
	// It may pass straight to the L1 Gate.
	ClassValidPayload PayloadClass = "VALID_PAYLOAD"
	// ClassTransportNormalized: transport noise (fences, envelopes, line
	// endings, padding) was stripped and the residual payload passes basic
	// envelope integrity. It may pass to the L1 Gate.
	ClassTransportNormalized PayloadClass = "TRANSPORT_NORMALIZED"
	// ClassSyntaxInvalid: the residual payload fails basic envelope integrity
	// (e.g. an unterminated code fence or an unclosed structural tag). Semantic
	// errors are NEVER repaired here — the payload is rejected to the contract
	// retry loop.
	ClassSyntaxInvalid PayloadClass = "SYNTAX_INVALID"
)

var (
	scriptOpen  = regexp.MustCompile(`(?i)<script[\s>]`)
	scriptClose = regexp.MustCompile(`(?i)</script>`)
	styleOpen   = regexp.MustCompile(`(?i)<style[\s>]`)
	styleClose  = regexp.MustCompile(`(?i)</style>`)
)

// Classify inspects a normalized payload (and the steps that produced it) for
// basic envelope integrity. It performs NO semantic repair: a malformed
// payload is reported as ClassSyntaxInvalid, never silently fixed.
//
// Envelope-integrity rules (transport/structural, NOT semantic):
//   - an unterminated markdown code fence (a residual ```) is invalid;
//   - an unbalanced structural tag pair (<script>/<style> open vs close) is
//     invalid;
//   - empty payloads are invalid.
func Classify(normalized string, steps []NormalizationStep) PayloadClass {
	if normalized == "" {
		return ClassSyntaxInvalid
	}
	// A residual fence delimiter means the fence was never closed — clear
	// transport corruption, not a semantic detail we may repair.
	if strings.Contains(normalized, "```") {
		return ClassSyntaxInvalid
	}
	// An open structural tag without its closing counterpart is a broken
	// envelope: the payload cannot be parsed into a coherent artifact. We do
	// NOT complete the tag — we reject it.
	if hasUnterminatedStructuralTag(normalized) {
		return ClassSyntaxInvalid
	}
	if len(steps) > 0 {
		return ClassTransportNormalized
	}
	return ClassValidPayload
}

func hasUnterminatedStructuralTag(s string) bool {
	if countMatch(scriptOpen, s) != countMatch(scriptClose, s) {
		return true
	}
	if countMatch(styleOpen, s) != countMatch(styleClose, s) {
		return true
	}
	return false
}

func countMatch(re *regexp.Regexp, s string) int {
	return len(re.FindAllString(s, -1))
}
