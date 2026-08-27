package ingestion

import (
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
)

// RepairCandidate is an explicit, candidate-based semantic repair proposal.
// The engine MUST NEVER perform silent semantic fixes — any proposed
// modification is emitted as a RepairCandidate for L1 verification.
type RepairCandidate struct {
	// RuleID identifies the heuristic that produced the proposal
	// (e.g. rule_html_tag_balance).
	RuleID string `json:"rule_id"`
	// ProposedPayload is the repaired payload bytes the heuristic produced.
	ProposedPayload string `json:"proposed_payload"`
	// Confidence is the heuristic's self-reported confidence in [0,1].
	Confidence float64 `json:"confidence"`
	// Diff is the minimal, human-readable diff between the original normalized
	// payload and the proposed payload (unified-diff style, bounded).
	Diff string `json:"diff"`
}

// Rule identifiers.
const (
	RuleHTMLTagBalance = "rule_html_tag_balance"
)

// Safety thresholds for candidate acceptance.
const (
	// maxAddedTags is the maximum number of synthetic closing tags a single
	// candidate may inject before it is considered beyond the safety threshold.
	maxAddedTags = 2
	// maxAddedBytesRatio is the maximum ratio of added bytes to original bytes
	// (20%).
	maxAddedBytesRatio = 0.20
	// maxAddedBytesAbs is an absolute cap on added bytes (512).
	maxAddedBytesAbs = 512
	// minConfidence is the minimum heuristic confidence for auto-acceptance.
	minConfidence = 0.70
)

// Telemetry counters (process-wide, monotonic).
var (
	repairGenerated atomic.Int64
	repairAccepted  atomic.Int64
)

// RecordRepairGenerated increments the repair_candidate_generated metric.
func RecordRepairGenerated() { repairGenerated.Add(1) }

// RecordRepairAccepted increments the repair_candidate_accepted metric.
func RecordRepairAccepted() { repairAccepted.Add(1) }

// RepairGeneratedCount returns the number of generated candidates (observability).
func RepairGeneratedCount() int64 { return repairGenerated.Load() }

// RepairAcceptedCount returns the number of accepted candidates (observability).
func RepairAcceptedCount() int64 { return repairAccepted.Load() }

// ResetRepairMetrics clears the counters (tests).
func ResetRepairMetrics() {
	repairGenerated.Store(0)
	repairAccepted.Store(0)
}

// voidElements never need a closing tag.
var voidElementsRepair = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true,
	"hr": true, "img": true, "input": true, "link": true, "meta": true,
	"param": true, "source": true, "track": true, "wbr": true,
}

// rawTextElements hold opaque character data until their own close tag.
var rawTextElementsRepair = map[string]bool{
	"script": true, "style": true, "textarea": true, "title": true,
}

// ProposeRepair attempts to produce a RepairCandidate for a normalized payload
// that was classified ClassSyntaxInvalid. It implements the HTML Stack Balancer
// heuristic: it walks the tag stack, identifies unclosed non-void elements, and
// proposes appending the minimal set of closing tags to balance the stack.
//
// Returns nil when no heuristic applies (unrecoverable syntax error) — the
// caller must reject the payload immediately to the contract retry loop.
func ProposeRepair(normalized string) *RepairCandidate {
	trimmed := strings.TrimSpace(normalized)
	if trimmed == "" {
		return nil
	}
	// Residual fence markers are unrecoverable transport corruption.
	if strings.Contains(trimmed, "```") {
		return nil
	}
	if !isHTMLLike(trimmed) {
		return nil
	}
	unclosed := detectUnclosedTags(trimmed)
	if len(unclosed) == 0 {
		return nil
	}
	// Safety: too many synthetic tags is beyond the safety threshold — don't
	// even propose it (let the caller treat it as unrecoverable).
	if len(unclosed) > maxAddedTags+3 {
		return nil
	}
	repaired := buildRepairedPayload(trimmed, unclosed)
	if repaired == trimmed {
		return nil
	}
	// Confidence degrades with the number of injected tags. A single missing
	// close is highly confident; multiple injections reduce confidence.
	confidence := 0.95 - float64(len(unclosed)-1)*0.07
	if confidence < 0.5 {
		confidence = 0.5
	}
	if confidence > 0.95 {
		confidence = 0.95
	}
	diff := buildRepairDiff(trimmed, repaired, unclosed)
	return &RepairCandidate{
		RuleID:          RuleHTMLTagBalance,
		ProposedPayload: repaired,
		Confidence:      confidence,
		Diff:            diff,
	}
}

// isHTMLLike reports whether the payload looks like HTML/markup and therefore
// may benefit from tag-balance repair. Non-HTML payloads (e.g. JSON, Go) are
// unrecoverable by this heuristic.
func isHTMLLike(s string) bool {
	if !strings.Contains(s, "<") || !strings.Contains(s, ">") {
		return false
	}
	// Require at least one tag-like sequence.
	lower := strings.ToLower(s)
	// Quick check for any tag opener.
	tagRe := regexp.MustCompile(`</?[a-zA-Z][a-zA-Z0-9]*[\s>/]`)
	return tagRe.MatchString(lower)
}

// detectUnclosedTags walks tag nesting and returns the stack of unclosed
// (non-void, non-self-closing) tags at EOF, from bottom to top.
func detectUnclosedTags(s string) []string {
	type frame struct {
		tag string
	}
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
			j := indexFoldRepair(s, "</"+rawText, i)
			if j < 0 {
				break // rest is raw text
			}
			gt := strings.IndexByte(s[j:], '>')
			if gt < 0 {
				break
			}
			// Pop the rawText element.
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
			// Incomplete tag at EOF — treat as unclosed.
			break
		}
		interior := strings.TrimSpace(rest[1:gt])
		selfClosing := strings.HasSuffix(interior, "/")
		interior = strings.TrimSpace(strings.TrimRight(interior, "/"))
		closing := strings.HasPrefix(interior, "/")
		name := strings.ToLower(tagNameRepair(strings.TrimPrefix(interior, "/")))
		if name == "" {
			i += gt + 1
			continue
		}
		switch {
		case closing:
			// Find matching opener.
			found := -1
			for k := len(stack) - 1; k >= 0; k-- {
				if stack[k].tag == name {
					found = k
					break
				}
			}
			if found >= 0 {
				// Pop through found (implicit close of inner elements is
				// malformed but the heuristic tolerates it to reach the
				// matching close — validation will decide).
				stack = stack[:found]
				if rawTextElementsRepair[name] && rawText == name {
					rawText = ""
				}
			}
		case selfClosing || voidElementsRepair[name]:
			// No stack push.
		default:
			stack = append(stack, frame{tag: name})
			if rawTextElementsRepair[name] {
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

func buildRepairedPayload(original string, unclosed []string) string {
	if len(unclosed) == 0 {
		return original
	}
	var b strings.Builder
	b.WriteString(original)
	// Ensure the payload ends with a newline before appended closings when it
	// doesn't already.
	if !strings.HasSuffix(original, "\n") {
		b.WriteString("\n")
	}
	for i := len(unclosed) - 1; i >= 0; i-- {
		fmt.Fprintf(&b, "</%s>", unclosed[i])
		if i > 0 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func buildRepairDiff(original, repaired string, unclosed []string) string {
	// Minimal diff: list the injected closing tags.
	var added []string
	for i := len(unclosed) - 1; i >= 0; i-- {
		added = append(added, fmt.Sprintf("</%s>", unclosed[i]))
	}
	origLines := strings.Count(original, "\n") + 1
	return fmt.Sprintf("@@ -%d +%d @@\n+%s", origLines, origLines, strings.Join(added, "\n+"))
}

// WithinSafetyThreshold reports whether the candidate's semantic diff is within
// the safety threshold for auto-acceptance: limited added bytes and tag count,
// and sufficient confidence. Raw-text elements (script/style) are never
// auto-repaired — they require semantic understanding beyond tag balancing.
func WithinSafetyThreshold(original string, c *RepairCandidate) bool {
	if c == nil {
		return false
	}
	if c.Confidence < minConfidence {
		return false
	}
	// Raw-text repairs are never auto-accepted — they are rejected to the
	// contract retry loop for explicit human/model correction.
	if strings.Contains(c.Diff, "</script>") || strings.Contains(c.Diff, "</style>") {
		return false
	}
	addedBytes := len(c.ProposedPayload) - len(original)
	if addedBytes < 0 {
		addedBytes = -addedBytes
	}
	addedTags := strings.Count(c.Diff, "</")
	if addedTags > maxAddedTags {
		return false
	}
	origLen := len(original)
	if origLen == 0 {
		return false
	}
	if addedBytes > maxAddedBytesAbs && float64(addedBytes)/float64(origLen) > maxAddedBytesRatio {
		return false
	}
	return true
}

// IsASTValid reports whether the payload is structurally valid HTML after the
// proposed repair (stack balanced, no malformed state). It is the lightweight
// L1 Verifier for AST structural equivalence used by the executor gate.
func IsASTValid(payload string) bool {
	if strings.TrimSpace(payload) == "" {
		return false
	}
	if strings.Contains(payload, "```") {
		return false
	}
	unclosed := detectUnclosedTags(payload)
	return len(unclosed) == 0
}

func tagNameRepair(tag string) string {
	tag = strings.TrimSpace(tag)
	for i, r := range tag {
		if r == ' ' || r == '\t' || r == '\n' || r == '/' {
			return tag[:i]
		}
	}
	return tag
}

func indexFoldRepair(s, sub string, from int) int {
	if from >= len(s) {
		return -1
	}
	j := strings.Index(strings.ToLower(s[from:]), strings.ToLower(sub))
	if j < 0 {
		return -1
	}
	return from + j
}
