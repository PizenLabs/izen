package execution

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/events"
)

// ── RAW CLAIM PROPAGATION (detection ≠ classification) ──────────────────────

// TestExtractNoOpClaimPropagatesRawClaim pins the contract that detection
// propagates the RAW model claim (sentinel + bounded prose) without forcing
// any terminal classification.
func TestExtractNoOpClaimPropagatesRawClaim(t *testing.T) {
	t.Run("bare_sentinel", func(t *testing.T) {
		claim, ok := ExtractNoOpClaim("NO_CHANGES_REQUIRED")
		if !ok || claim.Sentinel != NoOpSentinel || claim.Prose != "" {
			t.Fatalf("claim = %+v ok=%v, want bare sentinel", claim, ok)
		}
	})
	t.Run("prose_wrapped_claim_keeps_conversational_context", func(t *testing.T) {
		raw := "The assigned slice already matches.\nNO_CHANGES_REQUIRED\nThanks!"
		claim, ok := ExtractNoOpClaim(raw)
		if !ok {
			t.Fatal("prose-wrapped sentinel not detected")
		}
		if !strings.Contains(claim.Prose, "already matches") || !strings.Contains(claim.Prose, "Thanks") {
			t.Fatalf("raw claim prose lost: %+v", claim)
		}
	})
	t.Run("fenced_sentinel", func(t *testing.T) {
		if _, ok := ExtractNoOpClaim("```\nNO_CHANGES_REQUIRED\n```"); !ok {
			t.Fatal("fenced sentinel not detected")
		}
	})
	t.Run("code_shaped_response_is_never_a_claim", func(t *testing.T) {
		if _, ok := ExtractNoOpClaim("<<<<<<< SEARCH\na\n=======\nb\n>>>>>>>"); ok {
			t.Fatal("a real artifact was swallowed as a no-op claim")
		}
	})
	t.Run("prose_is_bounded", func(t *testing.T) {
		raw := strings.Repeat("word ", 100) + "\nNO_CHANGES_REQUIRED"
		claim, ok := ExtractNoOpClaim(raw)
		if !ok {
			t.Fatal("sentinel not detected")
		}
		if len(claim.Prose) > maxNoOpClaimProse {
			t.Fatalf("claim prose unbounded: %d chars", len(claim.Prose))
		}
	})
}

// TestIsNoOpBoundedPatchResponseDelegatesToClaimExtraction proves the binary
// predicate stays consistent with the structured extraction.
func TestIsNoOpBoundedPatchResponseDelegatesToClaimExtraction(t *testing.T) {
	for _, raw := range []string{
		"NO_CHANGES_REQUIRED",
		"  NO_CHANGES_REQUIRED \n",
		"```\nNO_CHANGES_REQUIRED\n```",
		"NO_CHANGES_REQUIRED.",
		"The assigned slice already matches.\nNO_CHANGES_REQUIRED",
	} {
		if !IsNoOpBoundedPatchResponse(raw) {
			t.Errorf("IsNoOpBoundedPatchResponse(%q) = false, want true", raw)
		}
	}
	for _, raw := range []string{
		"I could not find anything to change in these lines.",
		"<<<<<<< SEARCH\na\n=======\nb\n>>>>>>>",
		"@@ -1 +1 @@\n-a\n+b",
		"",
	} {
		if IsNoOpBoundedPatchResponse(raw) {
			t.Errorf("IsNoOpBoundedPatchResponse(%q) = true, want false", raw)
		}
	}
}

// ── DETERMINISTIC CLASSIFICATION MATRIX ─────────────────────────────────────

// TestClassifyNoOpClaimMatrix covers the tri-state decision matrix.
func TestClassifyNoOpClaimMatrix(t *testing.T) {
	cases := []struct {
		name    string
		obj     string
		slice   string
		want    NoOpVerdict
		reasons []string // substrings expected in the rationale
	}{
		{
			name:  "removal payload still present verbatim",
			obj:   `remove the section "Legacy Marker"`,
			slice: "top\n// Legacy Marker\nbottom\n",
			want:  NoOpObjectiveUnresolved,
		},
		{
			name:  "removal payload absent — claim confirmed",
			obj:   `remove the section "Ghost Payload"`,
			slice: "top\n// unrelated\nbottom\n",
			want:  NoOpObjectiveSatisfied,
		},
		{
			name:  "normalized-only match stays below threshold",
			obj:   `remove the section "Header Nav"`,
			slice: "top\nheader  nav\nbottom\n",
			want:  NoOpNoSafeMutation,
		},
		{
			name:  "partial match across payloads stays below threshold",
			obj:   `remove the section "Alpha One" and delete the section "Beta Two"`,
			slice: "alpha one only\n",
			want:  NoOpNoSafeMutation,
		},
		{
			name:  "dedup objective with duplicated runs remains unresolved",
			obj:   "remove duplicated handler blocks",
			slice: "// process kind A\n\treturn x\n// process kind A\n\treturn x\n",
			want:  NoOpObjectiveUnresolved,
		},
		{
			name:  "dedup objective with no duplicates confirms the claim",
			obj:   "strip redundant duplicate lines",
			slice: "// unique one\n// unique two\n",
			want:  NoOpObjectiveSatisfied,
		},
		{
			name:  "generic objective honors the uncontradicted claim",
			obj:   "restyle every row of this section",
			slice: "<div class=\"row\">x</div>\n",
			want:  NoOpObjectiveSatisfied,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyNoOpClaim(tc.obj, tc.slice)
			if got.Verdict != tc.want {
				t.Fatalf("verdict = %s (%s), want %s", got.Verdict, got.Reason, tc.want)
			}
			if got.Reason == "" {
				t.Fatal("classification must carry a deterministic rationale")
			}
		})
	}
}

// TestVerdictToOutcomeVocabulary pins the verdict → canonical outcome mapping
// and the parse/display round-trip of the three sub-states.
func TestVerdictToOutcomeVocabulary(t *testing.T) {
	cases := map[NoOpVerdict]MutationOutcome{
		NoOpObjectiveSatisfied:  OutcomeNoOpObjectiveSatisfied,
		NoOpNoSafeMutation:      OutcomeNoOpNoSafeMutation,
		NoOpObjectiveUnresolved: OutcomeNoOpObjectiveUnresolved,
	}
	for verdict, want := range cases {
		if got := VerdictToOutcome(verdict); got != want {
			t.Errorf("VerdictToOutcome(%s) = %s, want %s", verdict, got, want)
		}
		if back := ParseMutationOutcome(string(want)); back != want {
			t.Errorf("ParseMutationOutcome(%q) = %s, want %s", want, back, want)
		}
		if want.Display() == string(want) {
			t.Errorf("%s must carry a human Display label", want)
		}
	}
	if !OutcomeNoOpObjectiveSatisfied.IsNoOpFamily() ||
		!OutcomeNoOpNoSafeMutation.IsNoOpFamily() ||
		!OutcomeNoOpObjectiveUnresolved.IsNoOpFamily() {
		t.Error("all three sub-states must belong to the no-op family")
	}
	if OutcomeChanged.IsNoOpFamily() {
		t.Error("changed must not belong to the no-op family")
	}
}

// TestEvidenceOutcomeForNoOpSubStates pins the coarse evidence vocabulary:
// satisfied seals COMMITTED, below-threshold seals REQUIRES_REVIEW (never
// committed), unresolved seals FAILED.
func TestEvidenceOutcomeForNoOpSubStates(t *testing.T) {
	if got := evidenceOutcomeFor(OutcomeNoOpObjectiveSatisfied, nil); got != EvidenceCommitted {
		t.Errorf("satisfied → %s, want COMMITTED", got)
	}
	if got := evidenceOutcomeFor(OutcomeNoOpNoSafeMutation, nil); got != EvidenceRequiresReview {
		t.Errorf("no_safe_mutation → %s, want REQUIRES_REVIEW", got)
	}
	if got := evidenceOutcomeFor(OutcomeNoOpObjectiveUnresolved, errors.New("contradicted")); got != EvidenceFailed {
		t.Errorf("unresolved → %s, want FAILED", got)
	}
	if !EvidenceRequiresReview.Terminal() {
		t.Error("REQUIRES_REVIEW must be a terminal evidence state")
	}
	ev := SealFromScalars(SealEvidenceScalars{ContractID: "c-noop", AttemptID: 1, Outcome: string(EvidenceRequiresReview)})
	if ev == nil || ev.Outcome() != EvidenceRequiresReview {
		t.Fatalf("SealFromScalars rejected REQUIRES_REVIEW: %+v", ev)
	}
	if ev.Authoritative() {
		t.Fatal("requires-review evidence must NEVER be authoritative")
	}
}

// ── EXECUTOR INTEGRATION (single execution, real pipeline) ──────────────────

// sentinelExecutor builds a minimal runtime over a single sentinel response
// with the search_replace contract forced.
func sentinelExecutor(t *testing.T, root, content string) (*RuntimeExecutor, *mockProvider) {
	t.Helper()
	writeTarget(t, root, "note.txt", content)
	mock := &mockProvider{responses: []*ai.Response{{
		Content: NoOpSentinel,
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 50, CompletionTokens: 4, FinishReason: "stop"},
	}}}
	return phase4Executor(t, root, mock, events.NewBus(events.DefaultBufferSize)), mock
}

// TestExecutorNoOpClaimContradictedByStructureEscalates drives the full
// pipeline: the model claims NO_CHANGES_REQUIRED while the targeted content is
// still present verbatim. The execution must converge to
// no_op_objective_unresolved with ErrNoOpObjectiveUnresolved and FAILED
// evidence — never a success.
func TestExecutorNoOpClaimContradictedByStructureEscalates(t *testing.T) {
	root := t.TempDir()
	content := "top\n// Legacy Marker\nbottom\n"
	x, _ := sentinelExecutor(t, root, content)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode:            "autonomy",
		Prompt:          `remove the section "Legacy Marker"`,
		Targets:         []string{"note.txt"},
		Strategy:        searchReplaceProfile(),
		MaxOutputTokens: 1024,
	})
	if err == nil {
		t.Fatal("a contradicted no-op claim must not succeed")
	}
	if !errors.Is(err, ErrNoOpObjectiveUnresolved) {
		t.Fatalf("error = %v, want ErrNoOpObjectiveUnresolved", err)
	}
	if res == nil || res.Proof.Outcome != OutcomeNoOpObjectiveUnresolved {
		t.Fatalf("proof outcome = %v, want %s", res.Proof.Outcome, OutcomeNoOpObjectiveUnresolved)
	}
	if res.Evidence == nil || res.Evidence.Outcome() != EvidenceFailed {
		t.Fatalf("evidence outcome = %v, want FAILED", res.Evidence)
	}
	foundSignal := false
	for _, d := range res.Diagnostics {
		if d.Subtype == SignalNoOpObjectiveUnresolved {
			foundSignal = true
		}
	}
	if !foundSignal {
		t.Error("diagnostics must carry the NO_OP_OBJECTIVE_UNRESOLVED signal")
	}
	if got := mustRead(t, root, "note.txt"); got != content {
		t.Fatalf("filesystem changed on an escalated no-op: %q", got)
	}
}

// TestExecutorNoOpClaimBelowThresholdRequiresReview proves the terminal
// WARNING path: candidate edits detected below the safety threshold seal as
// REQUIRES_REVIEW with a nil error and no filesystem touch.
func TestExecutorNoOpClaimBelowThresholdRequiresReview(t *testing.T) {
	root := t.TempDir()
	// "Legacy Marker" appears only after normalization (case + spacing drift):
	// an approximate match — below the safety threshold by construction.
	content := "top\nlegacy  marker\nbottom\n"
	x, _ := sentinelExecutor(t, root, content)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode:            "autonomy",
		Prompt:          `remove the section "Legacy Marker"`,
		Targets:         []string{"note.txt"},
		Strategy:        searchReplaceProfile(),
		MaxOutputTokens: 1024,
	})
	if err != nil {
		t.Fatalf("a below-threshold hold is a terminal warning, not an error: %v", err)
	}
	if res == nil || res.Proof.Outcome != OutcomeNoOpNoSafeMutation {
		t.Fatalf("proof outcome = %v, want %s", res.Proof.Outcome, OutcomeNoOpNoSafeMutation)
	}
	if res.Evidence == nil || res.Evidence.Outcome() != EvidenceRequiresReview {
		t.Fatalf("evidence outcome = %v, want REQUIRES_REVIEW", res.Evidence)
	}
	if res.Evidence.Authoritative() {
		t.Fatal("requires-review evidence projected as authoritative success")
	}
	foundSignal := false
	for _, d := range res.Diagnostics {
		if d.Subtype == SignalNoOpRequiresReview {
			foundSignal = true
		}
	}
	if !foundSignal {
		t.Error("diagnostics must carry the NO_OP_REQUIRES_REVIEW signal")
	}
	if res.PendingPatchID != "" {
		t.Fatal("review hold staged a patch at the approval gate")
	}
	if got := mustRead(t, root, "note.txt"); got != content {
		t.Fatalf("filesystem changed on a review hold: %q", got)
	}
}

// TestExecutorNoOpClaimConfirmedByStructureSatisfies proves the terminal
// SUCCESS path keeps its structural backing: a removal objective whose payload
// is genuinely absent converges to no_op_objective_satisfied and COMMITTED
// evidence.
func TestExecutorNoOpClaimConfirmedByStructureSatisfies(t *testing.T) {
	root := t.TempDir()
	content := "top\n// unrelated content\nbottom\n"
	x, _ := sentinelExecutor(t, root, content)

	res, err := x.Execute(context.Background(), ExecuteRequest{
		Mode:            "autonomy",
		Prompt:          `remove the section "Vanished Section"`,
		Targets:         []string{"note.txt"},
		Strategy:        searchReplaceProfile(),
		MaxOutputTokens: 1024,
	})
	if err != nil {
		t.Fatalf("confirmed no-op must succeed: %v", err)
	}
	if res.Proof.Outcome != OutcomeNoOpObjectiveSatisfied {
		t.Fatalf("proof outcome = %s, want %s", res.Proof.Outcome, OutcomeNoOpObjectiveSatisfied)
	}
	if res.Evidence == nil || res.Evidence.Outcome() != EvidenceCommitted {
		t.Fatalf("evidence outcome = %v, want COMMITTED", res.Evidence)
	}
}
