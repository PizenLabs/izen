package ingestion

import (
	"errors"
	"strings"
	"testing"
)

// Test 1: Malformed HTML generates a RepairCandidate containing the minimal diff.
func TestRepairCandidate_MalformedHTMLGeneratesMinimalDiff(t *testing.T) {
	ResetRepairMetrics()
	// Missing closing tags: <div><p>hello is unclosed.
	raw := "<div><p>hello"
	trace, err := Process(raw)
	if err == nil {
		t.Fatalf("Process expected ErrSyntaxInvalid for malformed HTML, got nil")
	}
	if !errors.Is(err, ErrSyntaxInvalid) {
		t.Fatalf("error = %v, want ErrSyntaxInvalid", err)
	}
	if trace == nil || trace.RepairCandidate == nil {
		t.Fatalf("expected RepairCandidate for malformed HTML, got nil")
	}
	cand := trace.RepairCandidate
	if cand.RuleID != RuleHTMLTagBalance {
		t.Fatalf("RuleID = %q, want %q", cand.RuleID, RuleHTMLTagBalance)
	}
	if cand.ProposedPayload == "" {
		t.Fatal("ProposedPayload empty")
	}
	if cand.Diff == "" {
		t.Fatal("Diff empty — expected minimal diff")
	}
	// Diff must contain the injected closings.
	if !strings.Contains(cand.Diff, "</p>") || !strings.Contains(cand.Diff, "</div>") {
		t.Fatalf("Diff should contain minimal closing tags, got %q", cand.Diff)
	}
	// Proposed payload must be balanced.
	if !IsASTValid(cand.ProposedPayload) {
		t.Fatalf("ProposedPayload should be AST valid, got %q", cand.ProposedPayload)
	}
	// Original must remain unmodified in RawOutput for forensics.
	if trace.RawOutput != raw {
		t.Fatalf("RawOutput not preserved: got %q want %q", trace.RawOutput, raw)
	}
	// Safety threshold should pass for this minimal repair.
	if !WithinSafetyThreshold(trace.NormalizedPayload, cand) {
		t.Fatalf("WithinSafetyThreshold should be true for minimal diff")
	}
	if got := RepairGeneratedCount(); got != 1 {
		t.Fatalf("RepairGeneratedCount = %d, want 1", got)
	}
}

// Test 1b: script/style unclosed generates a candidate (proposed) but is
// rejected by the safety threshold — raw-text repairs require explicit
// verification and are never auto-accepted.
func TestRepairCandidate_ScriptUnclosedGeneratesCandidate(t *testing.T) {
	ResetRepairMetrics()
	raw := "<html><body><script>\n  console.log('x');\n</body></html>"
	trace, err := Process(raw)
	if err == nil {
		t.Fatalf("expected ErrSyntaxInvalid for unterminated <script>")
	}
	if !errors.Is(err, ErrSyntaxInvalid) {
		t.Fatalf("error = %v, want ErrSyntaxInvalid", err)
	}
	if trace.RepairCandidate == nil {
		t.Fatalf("expected RepairCandidate for unterminated <script> (proposed)")
	}
	if !strings.Contains(trace.RepairCandidate.Diff, "</script>") {
		t.Fatalf("Diff should contain </script>, got %q", trace.RepairCandidate.Diff)
	}
	if !IsASTValid(trace.RepairCandidate.ProposedPayload) {
		t.Fatalf("ProposedPayload should be valid after repair")
	}
	// Safety threshold must reject raw-text repairs.
	if WithinSafetyThreshold(trace.NormalizedPayload, trace.RepairCandidate) {
		t.Fatalf("WithinSafetyThreshold should be false for raw-text (script) repairs — they are not auto-accepted")
	}
}

// Test 2: Unrecoverable syntax errors return no repair candidate and reject immediately.
func TestRepairCandidate_UnrecoverableNoCandidate(t *testing.T) {
	ResetRepairMetrics()
	tests := []string{
		"",                        // empty payload
		"   \n  ",                 // whitespace only
		"```\nunterminated fence", // residual fence
		"```html\nhello\n",        // residual fence with language hint
	}
	for _, raw := range tests {
		trace, err := Process(raw)
		if err == nil {
			t.Fatalf("Process(%q) expected error, got nil", raw)
		}
		if !errors.Is(err, ErrSyntaxInvalid) {
			t.Fatalf("Process(%q) error = %v, want ErrSyntaxInvalid", raw, err)
		}
		if trace.RepairCandidate != nil {
			t.Fatalf("Process(%q) should return no RepairCandidate for unrecoverable error, got %+v", raw, trace.RepairCandidate)
		}
	}
	// Excessive unclosed tags beyond safety threshold also yields no candidate
	// (suppressed as unrecoverable proposal).
	var b strings.Builder
	for i := 0; i < 10; i++ {
		b.WriteString("<div>")
	}
	b.WriteString("hello")
	rawExcessive := b.String()
	trace, err := Process(rawExcessive)
	if err == nil {
		t.Fatalf("Process excessive tags expected error, got nil")
	}
	if trace.RepairCandidate != nil {
		// Proposal suppressed for excessive tags — should be nil.
		t.Fatalf("excessive tags should not produce a RepairCandidate, got %+v", trace.RepairCandidate)
	}
	if got := RepairAcceptedCount(); got != 0 {
		t.Fatalf("RepairAcceptedCount = %d, want 0 for unrecoverable inputs", got)
	}
}

// Test safety threshold rejects excessive synthetic content.
func TestRepairCandidate_SafetyThresholdRejectsLargeDiff(t *testing.T) {
	// Craft a payload with many unclosed tags beyond maxAddedTags.
	var b strings.Builder
	for i := 0; i < 10; i++ {
		b.WriteString("<div>")
	}
	b.WriteString("hello")
	raw := b.String()
	cand := ProposeRepair(raw)
	if cand == nil {
		// Proposing is already suppressed for excessive tags (> maxAddedTags+3)
		return
	}
	// If a candidate was still produced, safety threshold must reject it.
	tracePayload := raw // normalized payload
	if WithinSafetyThreshold(tracePayload, cand) {
		t.Fatalf("WithinSafetyThreshold should be false for excessive diff, cand diff=%q", cand.Diff)
	}
	if IsASTValid(cand.ProposedPayload) && WithinSafetyThreshold(tracePayload, cand) {
		t.Fatal("excessive repair should not be both AST valid and within threshold")
	}
}

// Test that repair is not silent: RawOutput preserved and NormalizedPayload unchanged on error path.
func TestRepairCandidate_NoSilentMutation(t *testing.T) {
	raw := "<div><span>hello"
	trace, err := Process(raw)
	if err == nil {
		t.Fatal("expected error")
	}
	if trace.RawOutput != raw {
		t.Fatalf("RawOutput mutated silently: got %q want %q", trace.RawOutput, raw)
	}
	if trace.NormalizedPayload != raw && trace.NormalizedPayload != strings.TrimSpace(raw) {
		// NormalizedPayload should still be the original normalized payload, NOT the repaired one.
		// The repaired payload lives only in RepairCandidate.ProposedPayload until L1 verification.
		if trace.RepairCandidate != nil && trace.NormalizedPayload == trace.RepairCandidate.ProposedPayload {
			t.Fatalf("NormalizedPayload silently mutated to repaired payload — must remain original until accepted")
		}
	}
	if trace.RepairCandidate != nil && trace.RepairCandidate.ProposedPayload == raw {
		t.Fatalf("ProposedPayload should differ from original")
	}
}
