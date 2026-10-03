package execution

// ── The requirement pass: a proposal surface, never an authority ─────────────
//
// The pass is the ONLY place the model is asked what the objective requires. It
// is bounded, read-only and structurally incapable of granting completion — the
// worst a non-compliant model can do is return nothing. These tests pin that
// property at the wire boundary, where a malformed payload is cheapest to refuse.

import (
	"strings"
	"testing"
)

func TestRequirementPass_ParseBareArray(t *testing.T) {
	got, err := ParseProposedRequirements([]byte(
		`[{"id":"r1","requirement":"restructure index.html"},{"id":"r2","requirement":"restyle styles.css"}]`))
	if err != nil {
		t.Fatalf("ParseProposedRequirements: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("parsed %d requirements, want 2", len(got))
	}
	if got[0].ID != "r1" || got[0].Requirement != "restructure index.html" {
		t.Fatalf("requirement 0 = %+v", got[0])
	}
}

func TestRequirementPass_ParseEnvelopeAndFence(t *testing.T) {
	for _, payload := range []string{
		`{"requirements":[{"id":"r1","requirement":"update the greeting in main.go"}]}`,
		"```json\n" + `{"requirements":[{"id":"r1","requirement":"update the greeting in main.go"}]}` + "\n```",
		"```\n" + `[{"id":"r1","requirement":"update the greeting in main.go"}]` + "\n```",
	} {
		got, err := ParseProposedRequirements([]byte(payload))
		if err != nil {
			t.Fatalf("payload %q: %v", payload, err)
		}
		if len(got) != 1 || got[0].Requirement != "update the greeting in main.go" {
			t.Fatalf("payload %q parsed to %+v", payload, got)
		}
	}
}

func TestRequirementPass_RefusesUnusablePayloads(t *testing.T) {
	// Each of these must be an ERROR, not a silent empty ledger: an empty ledger
	// reads as "the objective needs nothing", which is the opposite of what a
	// failed derivation means.
	for name, payload := range map[string]string{
		"empty":        "",
		"whitespace":   "   \n\t ",
		"truncated":    `[{"id":"r1","requirement":"restructure ind`,
		"prose":        "Sure! Here are the requirements you asked for: ...",
		"blank text":   `[{"id":"r1","requirement":"   "}]`,
		"not an array": `"r1"`,
	} {
		if _, err := ParseProposedRequirements([]byte(payload)); err == nil {
			t.Errorf("%s: a malformed payload was accepted as a ledger", name)
		}
	}
}

func TestRequirementPass_AssignsIdentityWhenAbsent(t *testing.T) {
	got, err := ParseProposedRequirements([]byte(`[{"requirement":"update VERSION"}]`))
	if err != nil {
		t.Fatalf("ParseProposedRequirements: %v", err)
	}
	if got[0].ID == "" {
		t.Fatal("a proposal without an id must still receive a deterministic identity")
	}
	if got[0].ID != "r1" {
		t.Fatalf("id = %q, want r1 (positional identity must be deterministic)", got[0].ID)
	}
}

func TestRequirementPass_BoundsRunawayLedgers(t *testing.T) {
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < 200; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"requirement":"restyle styles.css"}`)
	}
	b.WriteByte(']')
	got, err := ParseProposedRequirements([]byte(b.String()))
	if err != nil {
		t.Fatalf("ParseProposedRequirements: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("a long-but-valid ledger was refused entirely")
	}
	if len(got) > 24 {
		t.Fatalf("ledger of %d requirements was not bounded; a model that ignores the schema must not manufacture an unbounded obligation set", len(got))
	}
}

// TestRequirementPass_ProposalsCarryNoAuthority pins the structural property the
// whole design rests on: a proposal has no status field, so nothing on the wire
// can assert its own admission.
func TestRequirementPass_ProposalsCarryNoAuthority(t *testing.T) {
	// A payload that tries to grant itself admission must simply be parsed as
	// proposals; the extra keys are ignored and the requirement still has to
	// clear the runtime's own gate.
	got, err := ParseProposedRequirements([]byte(
		`[{"id":"r1","requirement":"restyle styles.css","status":"ADMITTED","satisfied":true}]`))
	if err != nil {
		t.Fatalf("ParseProposedRequirements: %v", err)
	}
	admitted := DeriveObjectiveContract(ObjectiveDerivation{
		Request: "restyle styles.css",
		Kind:    TaskPatch,
		Scope:   []string{"styles.css"},
		Proposals: []DerivedRequirement{{
			ID: got[0].ID, Text: got[0].Requirement, Origin: OriginModel,
		}},
	}).AdmittedRequirements()
	if len(admitted) != 1 || admitted[0].Status != RequirementAdmitted {
		t.Fatalf("the runtime gate did not decide admission: %+v", admitted)
	}
	// And with an empty scope the SAME proposal is refused, proving the admission
	// came from the runtime's grounding rule and not from the wire.
	refused := DeriveObjectiveContract(ObjectiveDerivation{
		Request:   "restyle styles.css",
		Kind:      TaskPatch,
		Scope:     nil,
		Proposals: admitted,
	}).AdmittedRequirements()
	if len(refused) != 0 {
		t.Fatalf("a proposal with no groundable scope was admitted: %+v", refused)
	}
}
