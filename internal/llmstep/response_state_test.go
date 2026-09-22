package llmstep

import (
	"strings"
	"testing"
)

func TestResponseStateLifecycle(t *testing.T) {
	rs := NewResponseState("explain how X works", "findings / needed adjustments / reason")
	if rs.Status != StatusInProgress {
		t.Fatalf("status = %q, want in_progress", rs.Status)
	}
	if len(rs.PendingTopics) != 1 || !strings.Contains(rs.PendingTopics[0], "explain how X works") {
		t.Fatalf("pending = %v, want the request seeded", rs.PendingTopics)
	}
	if !rs.CanContinue() {
		t.Fatalf("CanContinue = false at in_progress")
	}

	rs.AddAnswered("X is a thing")
	rs.AddFinding("confirmed by the target file")
	rs.AddEvidence("targets/styles.css")
	rs.AdvanceCursor()

	if rs.Status != StatusContinuation {
		t.Fatalf("status = %q after AdvanceCursor, want continuation", rs.Status)
	}
	if rs.ContinuationCursor != 1 {
		t.Fatalf("cursor = %d, want 1", rs.ContinuationCursor)
	}
	if len(rs.AnsweredTopics) != 1 {
		t.Fatalf("answered = %v", rs.AnsweredTopics)
	}

	rs.Complete()
	if rs.Status != StatusComplete {
		t.Fatalf("status = %q after Complete, want complete", rs.Status)
	}
	if len(rs.PendingTopics) != 0 {
		t.Fatalf("pending not cleared after Complete: %v", rs.PendingTopics)
	}
	if rs.CanContinue() {
		t.Fatalf("CanContinue = true after Complete")
	}
}

func TestResponseStateCompactContext(t *testing.T) {
	rs := NewResponseState("request", "text")
	rs.AddAnswered("answer one")
	rs.AdvanceCursor()
	ctx := rs.CompactContext(80, 800)
	for _, want := range []string{"answer one", "continuation #2", "response_format: text"} {
		if !strings.Contains(ctx, want) {
			t.Fatalf("compact context missing %q:\n%s", want, ctx)
		}
	}
	if strings.Contains(ctx, "answer one answer one") {
		t.Fatalf("compact context repeats delivered state:\n%s", ctx)
	}
	if !strings.Contains(ctx, "no transcript") {
		t.Fatalf("compact context does not advertise its transcript-free contract:\n%s", ctx)
	}
}

func TestResponseStateTruncatesLongEntries(t *testing.T) {
	rs := NewResponseState(strings.Repeat("y", 500), "")
	if len(rs.PendingTopics[0]) >= 500 {
		t.Fatalf("pending entry not truncated: %d chars", len(rs.PendingTopics[0]))
	}
}

func TestResponseStateEmptyAddsIgnored(t *testing.T) {
	rs := NewResponseState("", "")
	rs.AddAnswered("  ")
	rs.AddFinding("")
	rs.AddEvidence("\n\n")
	if strings.TrimSpace(strings.Join(rs.AnsweredTopics, "")) != "" {
		t.Fatalf("blank answered topic committed")
	}
	if strings.TrimSpace(strings.Join(rs.ValidatedFindings, "")) != "" {
		t.Fatalf("blank finding committed")
	}
	if strings.TrimSpace(strings.Join(rs.EvidenceRefs, "")) != "" {
		t.Fatalf("blank evidence committed")
	}
}
