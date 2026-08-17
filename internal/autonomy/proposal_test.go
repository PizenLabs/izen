package autonomy

import (
	"strings"
	"testing"
)

func TestMutationProposalCompleteness(t *testing.T) {
	base := MutationProposal{
		File:           "a.go",
		Evidence:       []Finding{{Type: "text.lines", Severity: SeverityInfo}},
		EvidenceLedger: "Context Evidence Ledger\nTarget: a.go\nStructural findings: none",
		Reason:         "because the intent requires it",
		Diff:           "--- a/a.go\n+++ b/a.go",
		Risk:           RiskLow,
		Rollback:       true,
	}
	if !base.Complete() {
		t.Fatal("complete proposal must report Complete()")
	}

	cases := []struct {
		name string
		mut  func(MutationProposal) MutationProposal
	}{
		{"missing diff", func(p MutationProposal) MutationProposal { p.Diff = ""; return p }},
		{"missing evidence ledger", func(p MutationProposal) MutationProposal { p.EvidenceLedger = ""; return p }},
		{"missing reason", func(p MutationProposal) MutationProposal { p.Reason = ""; return p }},
		{"unknown risk", func(p MutationProposal) MutationProposal { p.Risk = RiskUnknown; return p }},
		{"no rollback", func(p MutationProposal) MutationProposal { p.Rollback = false; return p }},
	}
	for _, tc := range cases {
		if tc.mut(base).Complete() {
			t.Errorf("proposal with %s must not be Complete()", tc.name)
		}
	}
}

func TestBuildMutationProposalCarriesEvidence(t *testing.T) {
	ctx := CompileContext("a.go", "package a\nfunc X() {}\n")
	p := BuildMutationProposal(MutationProposalInput{
		Context:  ctx,
		Reason:   "reason",
		Diff:     "--- a/a.go\n+++ b/a.go",
		Risk:     RiskLow,
		Rollback: true,
	})
	if p.File != "a.go" {
		t.Errorf("file = %q, want a.go (from context)", p.File)
	}
	if p.EvidenceLedger == "" {
		t.Error("evidence ledger must be compiled from context")
	}
	if !strings.Contains(p.EvidenceLedger, "Context Evidence Ledger") {
		t.Errorf("ledger missing header: %q", p.EvidenceLedger)
	}
	if !p.Complete() {
		t.Error("built proposal must be complete")
	}
}
