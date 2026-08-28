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
	// A SEARCH/REPLACE block is a PATCH ARTIFACT, not a document: its
	// structural integrity is enforced by the patch parser / anchor resolver,
	// never by document tag balancing. Unbalanced HTML snippets inside the
	// block (e.g. a SEARCH window carrying only an opening <div>) are
	// legitimate patch content and must not trip the envelope check — a
	// conversational-wrapper SEARCH/REPLACE payload recovered by the transport
	// layer reaches the artifact parser on Attempt 1, never a retry.
	if isPatchArtifact(normalized) {
		if len(steps) > 0 {
			return ClassTransportNormalized
		}
		return ClassValidPayload
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

// isPatchArtifact reports whether a normalized payload is a structured patch
// artifact (a SEARCH/REPLACE block) rather than a document. Patch artifacts
// are exempt from document-level envelope balancing.
func isPatchArtifact(s string) bool {
	return strings.Contains(s, "<<<<<<< SEARCH")
}

func hasUnterminatedStructuralTag(s string) bool {
	if countMatch(scriptOpen, s) != countMatch(scriptClose, s) {
		return true
	}
	if countMatch(styleOpen, s) != countMatch(styleClose, s) {
		return true
	}
	if hasUnbalancedGenericHTMLTag(s) {
		return true
	}
	return false
}

// hasUnbalancedGenericHTMLTag reports whether the payload contains an unclosed
// non-void HTML element (e.g. <div> without </div>). It is the generic
// envelope-integrity check that surfaces recoverable AST/HTML errors for the
// candidate-based repair path. Valid balanced markup returns false.
func hasUnbalancedGenericHTMLTag(s string) bool {
	if !strings.Contains(s, "<") || !strings.Contains(s, ">") {
		return false
	}
	// Fast-path: no alphabetic tag present.
	if !isHTMLLikeClassify(s) {
		return false
	}
	unclosed := detectUnclosedTagsClassify(s)
	return len(unclosed) > 0
}

func isHTMLLikeClassify(s string) bool {
	lower := strings.ToLower(s)
	tagRe := regexp.MustCompile(`</?[a-zA-Z][a-zA-Z0-9]*[\s>/]`)
	return tagRe.MatchString(lower)
}

// detectUnclosedTagsClassify is a local copy of the stack balancer for use
// inside Classify to avoid importing the full repair heuristic. It mirrors
// repair.detectUnclosedTags without the confidence/diff machinery.
func detectUnclosedTagsClassify(s string) []string {
	type frame struct{ tag string }
	var stack []frame
	inComment := false
	rawText := ""
	for i := 0; i < len(s); {
		if inComment {
			if end := strings.Index(s[i:], "-->"); end >= 0 {
				inComment = false
				i += end + 3
				continue
			}
			break
		}
		if rawText != "" {
			j := indexFoldClassify(s, "</"+rawText, i)
			if j < 0 {
				break
			}
			gt := strings.IndexByte(s[j:], '>')
			if gt < 0 {
				break
			}
			if len(stack) > 0 && stack[len(stack)-1].tag == rawText {
				stack = stack[:len(stack)-1]
			}
			rawText = ""
			i = j + gt + 1
			continue
		}
		lt := strings.IndexByte(s[i:], '<')
		if lt < 0 {
			break
		}
		i += lt
		rest := s[i:]
		switch {
		case strings.HasPrefix(rest, "<!--"):
			if end := strings.Index(rest, "-->"); end >= 0 {
				i += end + 3
			} else {
				inComment = true
				i = len(s)
			}
			continue
		case strings.HasPrefix(strings.ToLower(rest), "<!doctype"):
			gt := strings.IndexByte(rest, '>')
			if gt < 0 {
				i = len(s)
			} else {
				i += gt + 1
			}
			continue
		case strings.HasPrefix(rest, "<?"):
			if end := strings.Index(rest, "?>"); end >= 0 {
				i += end + 2
			} else {
				i = len(s)
			}
			continue
		}
		gt := strings.IndexByte(rest, '>')
		if gt < 0 {
			break
		}
		interior := strings.TrimSpace(rest[1:gt])
		selfClosing := strings.HasSuffix(interior, "/")
		interior = strings.TrimSpace(strings.TrimRight(interior, "/"))
		closing := strings.HasPrefix(interior, "/")
		name := strings.ToLower(tagNameClassify(strings.TrimPrefix(interior, "/")))
		if name == "" {
			i += gt + 1
			continue
		}
		switch {
		case closing:
			found := -1
			for k := len(stack) - 1; k >= 0; k-- {
				if stack[k].tag == name {
					found = k
					break
				}
			}
			if found >= 0 {
				stack = stack[:found]
				if rawText == name {
					rawText = ""
				}
			}
		case selfClosing || voidElementsClassify[name]:
			// no stack push
		default:
			stack = append(stack, frame{tag: name})
			if rawTextElementsClassify[name] {
				rawText = name
			}
		}
		i += gt + 1
	}
	out := make([]string, 0, len(stack))
	for _, f := range stack {
		out = append(out, f.tag)
	}
	return out
}

var voidElementsClassify = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true,
	"hr": true, "img": true, "input": true, "link": true, "meta": true,
	"param": true, "source": true, "track": true, "wbr": true,
}

var rawTextElementsClassify = map[string]bool{
	"script": true, "style": true, "textarea": true, "title": true,
}

func tagNameClassify(tag string) string {
	tag = strings.TrimSpace(tag)
	for i, r := range tag {
		if r == ' ' || r == '\t' || r == '\n' || r == '/' {
			return tag[:i]
		}
	}
	return tag
}

func indexFoldClassify(s, sub string, from int) int {
	if from >= len(s) {
		return -1
	}
	j := strings.Index(strings.ToLower(s[from:]), strings.ToLower(sub))
	if j < 0 {
		return -1
	}
	return from + j
}

func countMatch(re *regexp.Regexp, s string) int {
	return len(re.FindAllString(s, -1))
}
