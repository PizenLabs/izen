package autonomy

// ── CREATION CONTRACTS NEVER DEGRADE INTO AN IMPOSSIBLE PATCH ───────────────
//
// Recovery must not relabel a CREATE into a bounded SEARCH/REPLACE patch. A
// creation target did not exist when the attempt was dispatched, so there is no
// content for a SEARCH block to anchor against: the relabelled attempt can
// never succeed and burns a whole recovery cycle proving it.
//
// The OUTPUT_EXHAUSTED half of this rule already existed; these tests pin the
// SCHEMA_VIOLATION half, which is the shape a small model actually produces
// (a malformed/unterminated creation envelope). Both must re-prompt the SAME
// create contract or halt truthfully — never transition to bounded_patch.

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/autonomy"
)

func creationSchemaViolation() autonomy.Observation {
	return autonomy.Observation{
		Target:        "testfile.md",
		Outcome:       autonomy.OutcomeArtifactRetryableRejected,
		ArtifactShape: "create_file",
		AttemptNum:    1,
		ContractID:    "ct-parent",
		Diagnostic:    "ARTIFACT_INVALID: testfile.md: unterminated creation envelope",
	}
}

func TestDecideRecovery_CreationSchemaViolationRepromptsSameContract(t *testing.T) {
	dec := DecideRecovery(creationSchemaViolation(), autonomy.LoopBounds{MaxRecoveryCycles: 3})
	if dec.Action != autonomy.LoopRepair {
		t.Fatalf("action = %s, want repair (reason=%s)", dec.Action, dec.Reason)
	}
	if strings.Contains(dec.Reason, "BOUNDED_PATCH") || strings.Contains(dec.Reason, "SEARCH/REPLACE") {
		t.Fatalf("a creation schema violation was routed to a bounded patch: %s", dec.Reason)
	}
	if !strings.Contains(dec.Reason, "SAME create contract") {
		t.Fatalf("reason does not name the preserved contract: %s", dec.Reason)
	}
}

func TestTypedRepair_CreationSchemaViolationKeepsContract(t *testing.T) {
	req := autonomy.LoopRequest{
		Prompt:    "Create a file named testfile.md with the content 'Hello World'.",
		Targets:   []string{"testfile.md"},
		Evidence:  "initial evidence",
		RequestID: "run-1",
	}
	next, err := typedRepair(creationSchemaViolation(), req)
	if err != nil {
		t.Fatalf("typedRepair: %v", err)
	}
	if next.RecoveryStrategy == autonomy.StrategyBoundedPatch {
		t.Fatal("a creation schema violation was relabelled as bounded_patch")
	}
	if next.MutationStrategy != "" {
		t.Fatalf("creation repair set a mutation strategy %q; the create contract must be preserved", next.MutationStrategy)
	}
	if next.Evidence == req.Evidence {
		t.Fatal("the schema diagnostic was not carried into the re-prompt evidence")
	}
	if !strings.Contains(next.Evidence, "COMPLETE new file content") {
		t.Fatalf("the re-prompt does not ask for the create artifact: %q", next.Evidence)
	}
	if next.ParentContractID == "" {
		t.Fatal("causal contract lineage (I3) was lost")
	}
	if next.Prompt != req.Prompt {
		t.Fatal("typed repair silently altered the user intent")
	}
}

// TestDecideRecovery_PatchSchemaViolationStillTransitions is the negative half:
// a schema violation on an EXISTING-file patch still takes the bounded-patch
// transition, exactly as before.
func TestDecideRecovery_PatchSchemaViolationStillTransitions(t *testing.T) {
	dec := DecideRecovery(autonomy.Observation{
		Target:        "note.txt",
		Outcome:       autonomy.OutcomeArtifactRetryableRejected,
		ArtifactShape: "replace_block",
		AttemptNum:    1,
		ContractID:    "ct-parent",
		Diagnostic:    "ARTIFACT_INVALID: note.txt: unterminated",
	}, autonomy.LoopBounds{MaxRecoveryCycles: 3})
	if dec.Action != autonomy.LoopRepair {
		t.Fatalf("action = %s, want repair", dec.Action)
	}
	if !strings.Contains(dec.Reason, "BOUNDED_PATCH") {
		t.Fatalf("an existing-file schema violation did not transition to bounded_patch: %s", dec.Reason)
	}
}
