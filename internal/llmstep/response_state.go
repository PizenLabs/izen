package llmstep

import (
	"fmt"
	"strings"
)

// ResponseStateStatus is the lifecycle of one conversational response that may
// require more than one bounded step.
type ResponseStateStatus string

const (
	// StatusInProgress: the first bounded step is generating.
	StatusInProgress ResponseStateStatus = "in_progress"
	// StatusContinuation: a bounded step was exhausted and a read-only
	// continuation is under way under the SAME authority (ASK stays read-only).
	StatusContinuation ResponseStateStatus = "continuation"
	// StatusComplete: a step produced a complete answer — the response is
	// closed; no further continuation is scheduled.
	StatusComplete ResponseStateStatus = "complete"
)

// ResponseState is the compact, durable continuation state of a read-only
// (ASK / direct_response) bounded step sequence. It is the ONLY context a
// continuation step rebuilds from: never the full transcript, never the whole
// conversation, never the whole repository. Answered topics are already
// delivered and must not be repeated; pending topics are what remains. The
// continuation cursor advances exactly once per scheduled continuation, and
// the authority, task identity and target scope are STABLE across steps —
// a continuation is never a new authority.
type ResponseState struct {
	AnsweredTopics     []string            `json:"answered_topics,omitempty"`
	PendingTopics      []string            `json:"pending_topics,omitempty"`
	ValidatedFindings  []string            `json:"validated_findings,omitempty"`
	EvidenceRefs       []string            `json:"evidence_refs,omitempty"`
	ResponseFormat     string              `json:"response_format,omitempty"`
	ContinuationCursor int                 `json:"continuation_cursor,omitempty"`
	Status             ResponseStateStatus `json:"status"`
}

// NewResponseState opens the continuation state for one read-only response.
// pending starts as the full request (nothing answered yet); the response
// format carries the conversational contract (never a mutation/patch contract).
func NewResponseState(request, responseFormat string) *ResponseState {
	rs := &ResponseState{
		ResponseFormat: responseFormat,
		Status:         StatusInProgress,
	}
	if r := strings.TrimSpace(request); r != "" {
		rs.PendingTopics = append(rs.PendingTopics, truncateEntry(r, 120))
	}
	return rs
}

// AddAnswered records one validated delivered topic. Delivered topics are
// committed state: a continuation must never repeat them.
func (rs *ResponseState) AddAnswered(topic string) {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return
	}
	rs.AnsweredTopics = append(rs.AnsweredTopics, truncateEntry(topic, 120))
}

// AddFinding records one validated finding from a completed step. Findings
// that did not pass validation are never committed here.
func (rs *ResponseState) AddFinding(finding string) {
	finding = strings.TrimSpace(finding)
	if finding == "" {
		return
	}
	rs.ValidatedFindings = append(rs.ValidatedFindings, truncateEntry(finding, 120))
}

// AddEvidence records one addressable evidence reference (a local file or
// line range) so the continuation may re-read exactly what it already cited,
// never the whole workspace.
func (rs *ResponseState) AddEvidence(ref string) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return
	}
	rs.EvidenceRefs = append(rs.EvidenceRefs, truncateEntry(ref, 80))
}

// SetPending replaces the remaining pending topics with the supplied list.
func (rs *ResponseState) SetPending(topics []string) {
	out := make([]string, 0, len(topics))
	for _, t := range topics {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, truncateEntry(t, 120))
		}
	}
	rs.PendingTopics = out
}

// AdvanceCursor advances the continuation cursor exactly once. It is the proof
// that a continuation step advanced state (per the bounded-step contract):
// authority/task identity/target scope stay stable while the cursor moves.
func (rs *ResponseState) AdvanceCursor() {
	rs.ContinuationCursor++
	rs.Status = StatusContinuation
}

// Complete marks the response complete: no further continuation is scheduled.
func (rs *ResponseState) Complete() {
	rs.Status = StatusComplete
	rs.PendingTopics = nil
}

// CanContinue reports whether the response still has pending work worth a
// continuation step.
func (rs *ResponseState) CanContinue() bool {
	return rs.Status != StatusComplete && (len(rs.PendingTopics) > 0 || rs.Status == StatusInProgress)
}

// CompactContext renders the continuation context as a bounded, transcript-free
// block: answered topics, validated findings, evidence references and the
// pending list. perEntry caps each entry; total caps the whole block. It is
// appended to the continuation user turn in place of the conversation history.
func (rs *ResponseState) CompactContext(perEntry, total int) string {
	if perEntry <= 0 {
		perEntry = 80
	}
	if total <= 0 {
		total = 800
	}
	var b strings.Builder
	b.WriteString("### Response state (compact — no transcript):\n")
	fmt.Fprintf(&b, "- step: continuation #%d, status=%s\n", rs.ContinuationCursor+1, rs.Status)
	if n := len(rs.AnsweredTopics); n > 0 {
		fmt.Fprintf(&b, "- answered_topics (%d): %s\n", n, abbrevAll(rs.AnsweredTopics, perEntry, total))
	}
	if n := len(rs.ValidatedFindings); n > 0 {
		fmt.Fprintf(&b, "- validated_findings (%d): %s\n", n, abbrevAll(rs.ValidatedFindings, perEntry, total))
	}
	if n := len(rs.EvidenceRefs); n > 0 {
		fmt.Fprintf(&b, "- evidence_refs (%d): %s\n", n, abbrevAll(rs.EvidenceRefs, perEntry, total))
	}
	if n := len(rs.PendingTopics); n > 0 {
		fmt.Fprintf(&b, "- pending_topics (%d): %s\n", n, abbrevAll(rs.PendingTopics, perEntry, total))
	}
	if rs.ResponseFormat != "" {
		fmt.Fprintf(&b, "- response_format: %s\n", rs.ResponseFormat)
	}
	out := b.String()
	if len(out) > total+200 {
		out = out[:total+200] + "…\n"
	}
	return out
}

func truncateEntry(s string, limit int) string {
	if n := len(s); n > limit {
		return s[:limit] + "…"
	}
	return s
}
