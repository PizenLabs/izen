package ingestion

import (
	"regexp"
	"strings"
)

// NormalizationStep records a single transport-normalization transformation
// applied to the raw LLM output. Every step is recorded for forensic
// traceability — the ingestion layer never mutates a payload silently.
type NormalizationStep struct {
	// Kind names the transformation
	// (e.g. "strip_code_fence", "extract_code_block",
	// "normalize_line_endings", "trim_whitespace").
	Kind string `json:"kind"`
	// Detail is a human-readable description of what changed.
	Detail string `json:"detail"`
}

// backtick is the markdown code-fence delimiter, written as a hex escape so it
// can live inside a Go raw string literal.
const backtick = "\x60\x60\x60"

var (
	// outerFenceRe matches a payload that IS a single fenced code block:
	// optional leading whitespace, a ``` fence with an optional language tag,
	// the body, and a closing ```.
	outerFenceRe = regexp.MustCompile(`(?s)^[[:space:]]*` + backtick + `[^\n]*\n(.*?)\n?[[:space:]]*` + backtick + `[[:space:]]*$`)
	// innerFenceRe extracts the first fenced code block embedded anywhere in
	// the text (e.g. prose surrounding the actual artifact).
	innerFenceRe = regexp.MustCompile(`(?s)` + backtick + `[^\n]*\n(.*?)\n?[[:space:]]*` + backtick)
)

// NormalizeTransport performs TRANSPORT-ONLY normalization of a raw LLM
// response. It removes transport noise — markdown code fences (outer and
// inner), wrapper envelope padding, CRLF line endings, and surrounding
// whitespace — and records every transformation as a NormalizationStep.
//
// It deliberately performs NO semantic repair: a malformed payload (e.g. an
// unterminated <script> tag) is left EXACTLY as the model produced it. Semantic
// validity is the concern of the L1 Execution Gate / verifier, never this
// layer. The returned steps let a post-mortem replay the exact transport
// mutations that produced the normalized payload.
func NormalizeTransport(raw string) (string, []NormalizationStep) {
	var steps []NormalizationStep
	cur := raw

	// 1. Line-ending normalization (transport noise only).
	if strings.Contains(cur, "\r\n") {
		cur = strings.ReplaceAll(cur, "\r\n", "\n")
		steps = append(steps, NormalizationStep{
			Kind:   "normalize_line_endings",
			Detail: "converted CRLF line endings to LF",
		})
	}

	// 2. Strip an OUTER markdown code fence (the whole payload is one block).
	if m := outerFenceRe.FindStringSubmatch(cur); m != nil {
		cur = m[1]
		steps = append(steps, NormalizationStep{
			Kind:   "strip_code_fence",
			Detail: "removed outer markdown code fence",
		})
	} else if m := innerFenceRe.FindStringSubmatch(cur); m != nil {
		// 3. Otherwise extract an INNER fenced block (prose around the artifact).
		cur = m[1]
		steps = append(steps, NormalizationStep{
			Kind:   "extract_code_block",
			Detail: "extracted inner markdown code block payload",
		})
	}

	// 4. Remove surrounding whitespace padding.
	trimmed := strings.TrimSpace(cur)
	if trimmed != cur {
		cur = trimmed
		steps = append(steps, NormalizationStep{
			Kind:   "trim_whitespace",
			Detail: "removed leading/trailing whitespace padding",
		})
	}

	return cur, steps
}
