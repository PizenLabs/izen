package execution

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution/ingestion"
)

// TestRepairCandidateAccepted_LeavesForensicAuditTrail verifies that an
// accepted repair leaves a complete forensic audit trail in ExecutionEvidence
// containing both RawOutput and RepairCandidate.Diff, and that the repaired
// payload is used for the mutation.
func TestRepairCandidateAccepted_LeavesForensicAuditTrail(t *testing.T) {
	ingestion.ResetRepairMetrics()
	root := t.TempDir()
	// Original file content is valid; the model's malformed output will be repaired.
	writeTarget(t, root, "index.html", validHTML)
	// Repairable payload: a full-file HTML rewrite missing the closing </html>.
	// Use a near-full-size payload (>80% of original) so the patch engine treats
	// it as a full-content rewrite, not an ambiguous snippet. Single missing tag
	// is within the safety threshold (maxAddedTags=2) and will be auto-accepted.
	malformed := "<!doctype html>\n<html><head><title>t</title></head><body><p>hello</p></body>"
	mock := &mockProvider{responses: []*ai.Response{{Content: malformed, Usage: reproUsage}}}
	x := testExecutor(t, root, mock, events.NewBus(events.DefaultBufferSize))

	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID: "repair-evidence",
		Mode:      "build",
		Prompt:    "rewrite index.html",
		Target:    "index.html",
	})
	if err != nil {
		t.Fatalf("Execute should succeed with accepted repair, got err=%v", err)
	}
	if res.PendingPatchID == "" {
		t.Fatal("expected pending patch (repair accepted and submitted for verification)")
	}
	if res.IngestionTrace == nil {
		t.Fatal("IngestionTrace nil")
	}
	// RawOutput preserved verbatim.
	if res.IngestionTrace.RawOutput != malformed {
		t.Fatalf("RawOutput = %q, want %q", res.IngestionTrace.RawOutput, malformed)
	}
	if res.IngestionTrace.RepairCandidate == nil {
		t.Fatal("RepairCandidate nil — expected accepted repair")
	}
	if res.IngestionTrace.RepairCandidate.Diff == "" {
		t.Fatal("RepairCandidate.Diff empty")
	}
	if !strings.Contains(res.IngestionTrace.RepairCandidate.Diff, "</html>") {
		t.Fatalf("Diff should contain minimal closing tag, got %q", res.IngestionTrace.RepairCandidate.Diff)
	}
	if res.IngestionTrace.RepairCandidate.RuleID != ingestion.RuleHTMLTagBalance {
		t.Fatalf("RuleID = %q, want %q", res.IngestionTrace.RepairCandidate.RuleID, ingestion.RuleHTMLTagBalance)
	}
	// Verify metrics.
	if got := ingestion.RepairGeneratedCount(); got == 0 {
		t.Fatal("RepairGeneratedCount should be >0")
	}
	if got := ingestion.RepairAcceptedCount(); got == 0 {
		t.Fatal("RepairAcceptedCount should be >0")
	}
	// Approve and verify evidence forensic trail.
	apr, err := x.Approve(context.Background(), res.PendingPatchID)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if apr.Evidence == nil {
		t.Fatal("Evidence nil after approve")
	}
	evTrace := apr.Evidence.IngestionTrace()
	if evTrace == nil {
		t.Fatal("Evidence.IngestionTrace nil")
	}
	if evTrace.RawOutput != malformed {
		t.Fatalf("Evidence RawOutput = %q, want %q", evTrace.RawOutput, malformed)
	}
	if evTrace.RepairCandidate == nil || evTrace.RepairCandidate.Diff == "" {
		t.Fatal("Evidence missing RepairCandidate.Diff forensic trail")
	}
	// The repaired payload should have been applied (file changed).
	content := mustRead(t, root, "index.html")
	if !strings.Contains(content, "</html>") {
		t.Fatalf("repaired content not applied to file: %q", content)
	}
}

// TestRepairCandidate_UnrecoverableReturnsNoCandidate ensures genuinely
// unrecoverable syntax errors (an empty generation) return no repair candidate
// and reject immediately. Malformed markdown fences are NOT in this class: they
// are transport artifacts that NormalizeTransport sanitizes away before envelope
// validation, so the executor never raises a terminal syntax error for them.
func TestRepairCandidate_UnrecoverableExecutorRejects(t *testing.T) {
	ingestion.ResetRepairMetrics()
	root := t.TempDir()
	writeTarget(t, root, "index.html", validHTML)
	// Unrecoverable: an empty generation carries no artifact to repair or extract.
	malformed := ""
	mock := &mockProvider{responses: []*ai.Response{{Content: malformed, Usage: reproUsage}}}
	x := testExecutor(t, root, mock, events.NewBus(events.DefaultBufferSize))

	res, err := x.Execute(context.Background(), ExecuteRequest{
		RequestID: "repair-unrecoverable",
		Mode:      "build",
		Prompt:    "rewrite index.html",
		Target:    "index.html",
	})
	if err == nil {
		t.Fatal("expected error for unrecoverable syntax")
	}
	if res.IngestionTrace == nil {
		t.Fatal("IngestionTrace nil")
	}
	if res.IngestionTrace.RepairCandidate != nil {
		t.Fatalf("unrecoverable should have no RepairCandidate, got %+v", res.IngestionTrace.RepairCandidate)
	}
	if res.Evidence == nil || res.Evidence.IngestionTrace() == nil {
		t.Fatal("evidence trace missing")
	}
	if res.Evidence.IngestionTrace().RepairCandidate != nil {
		t.Fatal("evidence should not contain RepairCandidate for unrecoverable")
	}
}
