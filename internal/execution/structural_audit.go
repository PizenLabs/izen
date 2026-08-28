package execution

import (
	"bytes"
	"errors"
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

// ── Preflight Baseline Syntax Snapshot ──────────────────────────────────────
//
// The post-DAG global structural verifier fails a mutated document whose
// syntax is broken. When the PRE-DAG baseline was ALREADY broken and the DAG
// mutated nothing (a no-op run), that failure is a pre-existing condition,
// never a mutation regression. The preflight baseline snapshot records the
// target's syntax validity at staging time so the audit can attribute the
// failure correctly instead of parking a clean no-op run at awaiting_human.

// BaselineSyntaxPreexisting is the canonical warning marker emitted when a
// post-DAG global audit syntax failure is attributed to the PRE-EXISTING
// baseline (a broken document the DAG did not change), never to the DAG's own
// mutations. It names the warning log category for post-mortem searches.
const BaselineSyntaxPreexisting = "baseline_syntax_preexisting"

// ValidateDocumentSyntax runs the registered syntax validator for target's
// format over content. It returns nil when the document parses cleanly — or
// when the format has no registered validator (policy-neutral pass) — and a
// non-nil error carrying the deterministic parser diagnostics when it does
// not. It is the preflight baseline snapshot source of truth.
func ValidateDocumentSyntax(target string, content []byte) error {
	if len(content) == 0 {
		return nil // empty/creation target: nothing to validate
	}
	gate := v3Artifact.ValidateContent(target, content, 0)
	if gate.Passed {
		return nil
	}
	if gate.Error != nil {
		return gate.Error
	}
	return errors.New("artifact syntax validation failed")
}

// SyntaxDiagnostics returns the deterministic syntax diagnostic of a document
// for its format, or "" when it parses cleanly (or has no registered
// validator). It backs the "errors match baseline errors exactly" relaxation
// of the pre-existing-baseline check.
func SyntaxDiagnostics(target string, content []byte) string {
	gate := v3Artifact.ValidateContent(target, content, 0)
	if gate.Passed {
		return ""
	}
	if gate.Error != nil {
		return gate.Error.Error()
	}
	return "syntax validation failed"
}

// BaselineSyntaxRegression reports whether a post-DAG global audit syntax
// failure should be attributed to the PRE-DAG baseline rather than to this
// DAG's mutations. It returns true exactly when the document carries the same
// broken syntax it had before the DAG ran:
//
//   - the post-DAG bytes are IDENTICAL to the baseline (nothing mutated — a
//     checksum-identical no-op), OR
//   - the post-DAG document produces EXACTLY the same syntax diagnostics as
//     the baseline (content drifted elsewhere, the pre-existing defect did not).
//
// baselineValid is the preflight snapshot; when the baseline already parsed
// cleanly a syntax failure is always a regression.
func BaselineSyntaxRegression(target string, base, mutated []byte, baselineValid bool) bool {
	if baselineValid {
		return false
	}
	if bytes.Equal(base, mutated) {
		return true
	}
	baseDiag := SyntaxDiagnostics(target, base)
	return baseDiag != "" && baseDiag == SyntaxDiagnostics(target, mutated)
}
