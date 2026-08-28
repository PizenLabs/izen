package ingestion

import (
	"encoding/json"
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
	// leadingFenceLineRe matches an OPENING fence line ("```json", "```html"
	// or a bare "```") at the very start of a payload whose closing fence is
	// missing or malformed. The optional language tag is consumed with it.
	leadingFenceLineRe = regexp.MustCompile("^[[:space:]]*" + backtick + `[^\n]*\n`)
	// trailingFenceLineRe matches a RESIDUAL closing fence (two or more
	// backticks — a malformed "```" closer loses a tick in transit) stranded at
	// the very end of a payload whose opening fence was already stripped.
	trailingFenceLineRe = regexp.MustCompile(`\n?[[:space:]]*` + "``" + `+[[:space:]]*$`)
	// escapedQuoteRe matches a TRANSPORT-escaped double quote ("\"") — the
	// signature of a payload that was JSON-encoded before it reached the
	// transport layer (the fence delimiters and attribute quotes arrive
	// backslash-escaped).
	escapedQuoteRe = regexp.MustCompile(`\\"`)
	// searchReplaceBlockRe matches ONE complete SEARCH/REPLACE block — the
	// opening marker, the SEARCH body, the separator, the REPLACE body and the
	// closing marker. Both closing conventions (">>>>>" with or without a
	// " REPLACE" suffix) are tolerated. It is the AGGRESSIVE-INGESTION
	// recovery pattern for free-tier models that wrap the artifact in
	// conversational prose WITHOUT a markdown fence (e.g. Cohere's
	// "Here is the fix:" wrapper), so NormalizeTransport cannot rely on fence
	// extraction alone.
	searchReplaceBlockRe = regexp.MustCompile(`(?s)<<<<<<< SEARCH\s*(.*?)\s*=======\s*(.*?)\s*>>>>>>>(?: REPLACE)?`)
	// rawHTMLBlockRe matches the content of an outer ```html ... ``` code block
	// so preceding/trailing model conversational text is stripped entirely.
	rawHTMLBlockRe = regexp.MustCompile("(?s)```html[^\n]*\n([\\s\\S]*?)\n?```")
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

	// 4. Unescape TRANSPORT-level escaped raw quotes. A payload whose quotes
	// arrived backslash-escaped ("{\"a\":1}", <body class=\"shell\">) was
	// re-encoded by the transport; a payload that is ALREADY valid JSON keeps
	// its escapes untouched (they are legitimate JSON string escapes).
	if strings.Contains(cur, `\"`) && !json.Valid([]byte(cur)) {
		cur = escapedQuoteRe.ReplaceAllString(cur, `"`)
		steps = append(steps, NormalizationStep{
			Kind:   "unescape_escaped_quotes",
			Detail: "unescaped transport-escaped double quotes",
		})
	}

	// 5. Strip a MALFORMED fence residue: an unterminated opening fence line
	// ("```html" with no closer) or a stray closing fence left behind when the
	// wrapper could not be closed. These are transport artifacts, never payload
	// content — a syntactically valid payload must not be rejected because its
	// wrapper was damaged.
	cur = stripResidualFences(cur, &steps)

	// 5b. AGGRESSIVE RAW BLOCK RECOVERY: free-tier models frequently wrap the
	// artifact in conversational prose without any markdown fence ("Here is the
	// fix: <<<<<<< SEARCH …"). Fence extraction cannot rescue those, and
	// Classify would reject the envelope (the prose + unbalanced HTML snippets
	// of the patch). Scan the residual for a standard SEARCH/REPLACE block or
	// an outer ```html code block and lift it out directly. A payload that IS a
	// clean block already extracts to itself (identity — no step recorded).
	if block, kind, ok := recoverArtifactBlock(cur); ok &&
		strings.TrimSpace(block) != strings.TrimSpace(cur) {
		cur = block
		steps = append(steps, NormalizationStep{
			Kind:   kind,
			Detail: "recovered raw artifact block from conversational wrapper",
		})
	}

	// 6. Remove surrounding whitespace padding.
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

// stripResidualFences removes transport fence artifacts that survived the
// well-formed fence extraction: a leading OPENING fence line whose closing
// fence is missing or malformed, and any trailing RESIDUAL closing fence.
// Both are consumed along with their surrounding whitespace so the residual
// payload is the actual artifact, never the wrapper.
func stripResidualFences(s string, steps *[]NormalizationStep) string {
	cur := s
	changed := false
	if m := leadingFenceLineRe.FindString(cur); m != "" {
		cur = strings.TrimSpace(cur[len(m):])
		changed = true
	}
	if m := trailingFenceLineRe.FindString(cur); m != "" {
		cur = strings.TrimSpace(strings.TrimSuffix(cur, m))
		changed = true
	}
	if changed {
		*steps = append(*steps, NormalizationStep{
			Kind:   "strip_malformed_fence",
			Detail: "removed residual/unterminated markdown fence markers",
		})
	}
	return cur
}

// recoverArtifactBlock scans raw response text for a standard artifact block
// the transport pipeline failed to rescue from conversational prose:
//
//	a. a complete SEARCH/REPLACE block (<<<<<<< SEARCH … ======= … >>>>>>>),
//	b. the content of an outer ```html … ``` code block.
//
// It returns the matched block (whitespace-trimmed, preserved verbatim so the
// SEARCH anchor stays byte-exact for downstream anchor resolution), the
// normalization-step Kind to record, and true when a block was found. It is
// the permissive ingestion fallback BEFORE a transport normalization error:
// a conversational wrapper is transport noise, never grounds to reject a
// recoverable artifact.
func recoverArtifactBlock(raw string) (block, kind string, ok bool) {
	if m := searchReplaceBlockRe.FindStringSubmatch(raw); m != nil {
		return strings.TrimSpace(m[0]), "extract_search_replace_block", true
	}
	if m := rawHTMLBlockRe.FindStringSubmatch(raw); m != nil {
		return strings.TrimSpace(m[1]), "extract_raw_html_block", true
	}
	return "", "", false
}

// isFenceLine reports whether a trimmed line is a bare markdown fence marker
// ("```" or "```lang") as opposed to payload content that merely begins with
// backticks. It backs the last-resort raw-content extraction in Process.
func isFenceLine(trimmed string) bool {
	if !strings.HasPrefix(trimmed, backtick) {
		return false
	}
	rest := strings.TrimPrefix(trimmed, backtick)
	if rest == "" {
		return true // a bare closing fence
	}
	// An opening fence carries a language tag with no inline content
	// ("```json"); anything with whitespace is payload content.
	return !strings.ContainsAny(rest, " \t")
}

// extractTransportContent is the LAST-RESORT transport extraction: it drops
// every line that is a bare markdown fence marker from the raw response and
// unescapes transport-escaped quotes, yielding the residual raw content. It is
// used only when NormalizeTransport could not close a badly damaged wrapper.
func extractTransportContent(raw string) string {
	var b strings.Builder
	for _, line := range strings.Split(raw, "\n") {
		if isFenceLine(strings.TrimSpace(line)) {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	out := strings.TrimSpace(b.String())
	if strings.Contains(out, `\"`) && !json.Valid([]byte(out)) {
		out = escapedQuoteRe.ReplaceAllString(out, `"`)
	}
	return out
}
