package execution

import (
	"fmt"
	"regexp"
	"strings"
)

// ── AST Structural Audit Feedback Injection ─────────────────────────────────
//
// When the V3 artifact pipeline (V3ArtifactPipeline / rule_html_tag_balance)
// rejects an artifact with a STRUCTURAL parse error (e.g. an unterminated
// <script> element), the raw rejection travels as opaque text. The retry loop
// cannot self-correct from "html: unterminated <script> element" alone — it
// does not know WHERE the document broke.
//
// StructuralAuditDirective rewrites the rejection into the exact AST-aware
// retry directive:
//
//	[CONTRACT FAILURE] Line <N>: <ParseError>. Re-emit ONLY the corrected
//	SEARCH/REPLACE block fixing the unclosed tag.
//
// The directive carries the precise opening line of the offending node plus
// the parse error itself, so the successor attempt anchors the corrected
// SEARCH/REPLACE block at the real defect — never a blind regeneration.

// structuralLineRe extracts "at line N" suffixes produced by the deterministic
// validators ("unterminated <script> element at line 7", "unterminated tag at
// line 3 (column 12)", "unterminated quoted attribute value at line 9").
var structuralLineRe = regexp.MustCompile(`at line\s+(\d+)`)

// structuralErrorRe matches the whole structural parse-error family so only
// genuine syntax failures are rewritten — never schema/semantic rejections.
var structuralErrorRe = regexp.MustCompile(`(?i)(unterminated|unclosed|dangling|unexpected\s+end|expected\s+(close|closing)|missing\s+closing)`)

// StructuralAuditDirective formats a Boundary-4 structural rejection into the
// AST-aware contract-failure directive injected into the retry loop prompt.
// When the detail does not carry a structural parse error (or no line can be
// resolved), the original detail is returned unchanged so non-structural
// rejections keep their existing evidence.
func StructuralAuditDirective(detail string) string {
	line, errMsg, ok := parseStructuralError(detail)
	if !ok {
		return detail
	}
	return fmt.Sprintf(
		"[CONTRACT FAILURE] Line %d: %s. Re-emit ONLY the corrected SEARCH/REPLACE block fixing the unclosed tag.",
		line, errMsg)
}

// structuralAuditForPayload runs the V3 artifact pipeline over a payload and,
// when it rejects it with a resolvable structural parse error, returns the
// [CONTRACT FAILURE] directive carrying the exact line. It is the bridge for
// payloads rejected earlier at the L1 ingestion pre-gate (syntax_invalid:
// unterminated tag): the ingestion layer does not parse line positions, but the
// artifact pipeline's deterministic validators do. Returns "" when the payload
// is not a structural parse failure.
func structuralAuditForPayload(target string, payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	gate := v3Artifact.ValidateContent(target, payload, 0)
	if gate.Passed || gate.Error == nil {
		return ""
	}
	audit := StructuralAuditDirective(gate.Error.Error())
	if strings.HasPrefix(audit, "[CONTRACT FAILURE]") {
		return audit
	}
	return ""
}

// parseStructuralError extracts the opening line and the parse-error text from
// a structural rejection detail. It returns ok=false when the detail carries
// no resolvable structural parse failure.
func parseStructuralError(detail string) (line int, errMsg string, ok bool) {
	trimmed := strings.TrimSpace(detail)
	if trimmed == "" {
		return 0, "", false
	}
	// Strip a wrapping executor prefix ("executor: mutation artifact rejected
	// with retry directive: <target>: <error>: <directive>") and keep the
	// validator's own message segment.
	msg := trimmed
	if idx := strings.LastIndex(msg, ": html:"); idx >= 0 {
		msg = strings.TrimSpace(msg[idx+2:]) // "html: unterminated ..."
	} else if idx := strings.LastIndex(msg, ": go:"); idx >= 0 {
		msg = strings.TrimSpace(msg[idx+2:])
	} else if idx := strings.LastIndex(msg, ": json:"); idx >= 0 {
		msg = strings.TrimSpace(msg[idx+2:])
	}
	if !structuralErrorRe.MatchString(msg) {
		return 0, "", false
	}
	m := structuralLineRe.FindStringSubmatch(msg)
	if m == nil {
		return 0, "", false // no line resolved: leave the detail as-is
	}
	n := 0
	for _, c := range m[1] {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	if n <= 0 {
		return 0, "", false
	}
	// Drop the trailing ", Line <N>" / " at line N" suffix so the parse error
	// reads cleanly ("unterminated <script> element").
	errMsg = strings.TrimSpace(msg)
	if i := strings.LastIndex(errMsg, " at line "+m[1]); i >= 0 {
		errMsg = strings.TrimSpace(errMsg[:i])
	}
	// Strip the validator language prefix ("html: ", "go: ", "json: ") so the
	// directive reads "unterminated <script> element", not "html: unterminated
	// <script> element".
	for _, prefix := range []string{"html: ", "go: ", "json: "} {
		if strings.HasPrefix(errMsg, prefix) {
			errMsg = strings.TrimSpace(strings.TrimPrefix(errMsg, prefix))
		}
	}
	return n, errMsg, true
}
