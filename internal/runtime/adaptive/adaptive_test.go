package adaptive

import (
	"os"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/runtime/durable"
	"github.com/PizenLabs/izen/internal/runtime/ephemeral"
)

func testStore(t *testing.T, taskID string, scope []string) *durable.TaskStore {
	t.Helper()
	dir := t.TempDir()
	s := durable.NewTaskStore(dir)
	if err := s.Open(); err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.CreateTask(taskID, "fix auth handler", scope); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Checkpoint(taskID, "cp-1"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	return s
}

func ledgerString(t *testing.T, s *durable.TaskStore) string {
	t.Helper()
	data, err := os.ReadFile(s.LedgerPath())
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	return string(data)
}

// Criterion 1: a worker approach failing verification generates a
// NegativeKnowledge event in ledger.ndjson.
func TestVerificationFailureRecordsNegativeKnowledge(t *testing.T) {
	s := testStore(t, "t-neg", []string{"auth/"})
	ledger := NewNegativeLedger()

	// Simulate: verification fails (failed build) for a concrete approach.
	if err := s.RecordVerification("t-neg", "op-1", false, "build failed: undefined sessionModel"); err != nil {
		t.Fatalf("record verification: %v", err)
	}
	nk, err := ledger.Record("t-neg", s, NegativeKnowledge{
		Hypothesis:   "Use sessionModel as universal active-model fallback.",
		WhyRejected:  "Overrides explicit activation on direct_response path.",
		EvidenceRefs: []string{"E-17", "E-21"},
		TargetScope:  []string{"auth/"},
	})
	if err != nil {
		t.Fatalf("record negative: %v", err)
	}
	if nk.Status != StatusActiveNegativeKnowledge {
		t.Fatalf("status=%q want ACTIVE", nk.Status)
	}
	ledgerText := ledgerString(t, s)
	if !strings.Contains(ledgerText, string(durable.EventNegativeKnowledge)) {
		t.Fatal("NEGATIVE_KNOWLEDGE_RECORDED missing from ledger.ndjson")
	}
	if !strings.Contains(ledgerText, "sessionModel") {
		t.Fatal("rejected hypothesis missing from ledger payload")
	}
	// Evidence is mandatory: bare assertion without evidence refs fails.
	if _, err := ledger.Record("t-neg", nil, NegativeKnowledge{Hypothesis: "try again harder"}); err == nil {
		t.Fatal("expected error recording negative knowledge without evidence")
	}
}

// Criterion 2: the replacement worker receives a ResumeContract containing
// the disproven hypothesis under [IMMUTABLE NEGATIVE CONSTRAINTS], and
// proposals attempting it are rejected.
func TestReplacementWorkerReceivesImmutableConstraints(t *testing.T) {
	s := testStore(t, "t-resume", []string{"auth/"})
	ledger := NewNegativeLedger()
	if _, err := ledger.Record("t-resume", s, NegativeKnowledge{
		Hypothesis:   "Use sessionModel as universal active-model fallback.",
		WhyRejected:  "Overrides explicit activation on direct_response path.",
		EvidenceRefs: []string{"E-17", "E-21"},
		TargetScope:  []string{"auth/"},
	}); err != nil {
		t.Fatal(err)
	}
	st, _ := s.State("t-resume")
	capsule := ephemeral.DeriveCapsule(ephemeral.CapsuleSource{
		State:   st,
		Current: ephemeral.StepDefinition{ID: "step-1", Goal: "fix handler"},
		Budget:  ephemeral.BudgetState{RecoveryAttemptsLeft: 2, MaxRecoveryAttempts: 3},
	}, 10)
	contract, err := ephemeral.BuildResumeContract(capsule, "cp-1", ephemeral.FailureVerificationFailure, []string{"read", "edit"})
	if err != nil {
		t.Fatalf("build contract: %v", err)
	}
	ledger.AttachToContract("t-resume", &contract)
	if len(contract.NegativeConstraints) != 1 {
		t.Fatalf("constraints=%d want 1", len(contract.NegativeConstraints))
	}
	prompt := ephemeral.RenderResumePrompt(contract)
	if !strings.Contains(prompt, ephemeral.ImmutableConstraintsHeader) {
		t.Fatal("system prompt missing [IMMUTABLE NEGATIVE CONSTRAINTS]")
	}
	if !strings.Contains(prompt, "Use sessionModel as universal active-model fallback.") {
		t.Fatal("disproven hypothesis missing from system prompt")
	}
	// Inviolability: a worker proposing the rejected approach is rejected.
	if err := ledger.ValidateProposal("t-resume",
		"I will use sessionModel as universal active-model fallback.", []string{"auth/"}); err == nil {
		t.Fatal("expected ValidateProposal to reject known failed approach")
	}
	// A novel approach passes.
	if err := ledger.ValidateProposal("t-resume",
		"Thread the explicit activated model through the direct_response path.", []string{"auth/"}); err != nil {
		t.Fatalf("novel proposal wrongly rejected: %v", err)
	}
}

// Criterion 3: verification failure or unresolved AST symbol increments the
// tier (L0 → L1), whereas an unbacked LLM expansion request is rejected.
// Self-reported confidence NEVER expands context.
func TestEvidencePressureGatesExpansion(t *testing.T) {
	s := testStore(t, "t-tier", []string{"auth/"})
	planner := NewContextPlanner()
	eval := EvidencePressureEvaluator{}

	if got := planner.Tier("t-tier"); got != TierL0 {
		t.Fatalf("initial tier=%s want L0", got)
	}

	// Case A: verification failure → expand L0 → L1 with ledger signal.
	dec := eval.Evaluate(PressureInput{VerificationFailed: true, SelfReportedConfidence: 0.9})
	if !dec.Expand || dec.Signal != SignalVerificationFailure {
		t.Fatalf("verification failure must expand: %+v", dec)
	}
	if err := s.RecordEvidencePressure("t-tier", string(dec.Signal), dec.Reason); err != nil {
		t.Fatal(err)
	}
	if !s.HasEvidencePressure("t-tier", string(SignalVerificationFailure)) {
		t.Fatal("EVIDENCE_PRESSURE event missing from ledger")
	}
	next, expanded, _ := planner.RequestExpansion("t-tier", dec)
	if !expanded || next != TierL1 {
		t.Fatalf("tier=%s expanded=%v want L1/true", next, expanded)
	}
	if err := s.AdvanceContextTier("t-tier", int(next)); err != nil {
		t.Fatal(err)
	}
	if s.ContextTier("t-tier") != 1 {
		t.Fatalf("persisted tier=%d want 1", s.ContextTier("t-tier"))
	}

	// Case B: unresolved AST symbol → expand L1 → L2.
	dec2 := eval.Evaluate(PressureInput{SymbolUnresolved: true})
	if !dec2.Expand || dec2.Signal != SignalUnresolvedSymbol {
		t.Fatalf("unresolved symbol must expand: %+v", dec2)
	}
	next2, expanded2, _ := planner.RequestExpansion("t-tier", dec2)
	if !expanded2 || next2 != TierL2 {
		t.Fatalf("tier=%s expanded=%v want L2/true", next2, expanded2)
	}

	// Case C: unbacked LLM request (confidence 0.2, no signal) → rejected.
	dec3 := eval.Evaluate(PressureInput{SelfReportedConfidence: 0.2, ExpandRequested: true})
	if dec3.Expand {
		t.Fatalf("confidence-only request must not expand: %+v", dec3)
	}
	before := planner.Tier("t-tier")
	after, expanded3, reason := planner.RequestExpansion("t-tier", dec3)
	if expanded3 || after != before {
		t.Fatalf("unbacked expansion granted: tier=%s expanded=%v", after, expanded3)
	}
	if !strings.Contains(strings.ToLower(reason), "confidence") {
		t.Fatalf("rejection reason must cite confidence dominance: %q", reason)
	}

	// Case D: high confidence alone still never expands.
	dec4 := eval.Evaluate(PressureInput{SelfReportedConfidence: 0.99})
	if dec4.Expand {
		t.Fatalf("high confidence must not expand: %+v", dec4)
	}
}

// Criterion 4: RE_PLAN marks invalid negative constraints as STALE so they
// cannot block valid execution under the new target scope.
func TestReplanMarksConstraintsStale(t *testing.T) {
	s := testStore(t, "t-replan", []string{"auth/"})
	ledger := NewNegativeLedger()
	if _, err := ledger.Record("t-replan", s, NegativeKnowledge{
		Hypothesis:   "Patch auth handler in place.",
		WhyRejected:  "Race with session refresh.",
		EvidenceRefs: []string{"E-1"},
		TargetScope:  []string{"auth/"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := len(ledger.ActiveForScope("t-replan", []string{"auth/"})); got != 1 {
		t.Fatalf("active=%d want 1 before replan", got)
	}
	// Structural scope shift: auth/ → billing/. The old constraint's
	// preconditions no longer hold.
	n, err := ledger.MarkStaleOnReplan("t-replan", s, []string{"billing/"}, "RE_PLAN: scope auth/ → billing/")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("staled=%d want 1", n)
	}
	if got := len(ledger.ActiveForScope("t-replan", []string{"billing/"})); got != 0 {
		t.Fatalf("stale constraint still active for new scope: %d", got)
	}
	// Ledger lineage: NEGATIVE_KNOWLEDGE_STALED must exist.
	if !strings.Contains(ledgerString(t, s), string(durable.EventNegativeKnowledgeStale)) {
		t.Fatal("NEGATIVE_KNOWLEDGE_STALED missing from ledger.ndjson")
	}
	// The previously rejected approach is now proposable under the new scope.
	if err := ledger.ValidateProposal("t-replan", "Patch auth handler in place.", []string{"billing/"}); err != nil {
		t.Fatalf("stale constraint blocks new scope: %v", err)
	}
	// Replacement contract under the new scope carries no constraints.
	st, _ := s.State("t-replan")
	st.ActiveTargetScope = []string{"billing/"}
	capsule := ephemeral.DeriveCapsule(ephemeral.CapsuleSource{
		State:   st,
		Current: ephemeral.StepDefinition{ID: "step-2", Goal: "fix billing"},
		Budget:  ephemeral.BudgetState{RecoveryAttemptsLeft: 2, MaxRecoveryAttempts: 3},
	}, 10)
	contract, _ := ephemeral.BuildResumeContract(capsule, "cp-1", ephemeral.FailureTargetConflict, nil)
	ledger.AttachToContract("t-replan", &contract)
	if len(contract.NegativeConstraints) != 0 {
		t.Fatalf("stale constraints leaked into new-scope contract: %+v", contract.NegativeConstraints)
	}
}

// Compaction preserves the faithful minimum and never invalidates ACTIVE
// negatives or the authority graph (it touches neither).
func TestCompactorPreservesFaithfulMinimum(t *testing.T) {
	st := durable.TaskState{
		ID: "t-compact", Intent: "fix auth handler",
		ActiveTargetScope: []string{"auth/"},
		CurrentStepID:     "step-1", LastCheckpointID: "cp-1",
	}
	nk := NegativeKnowledge{
		ID: "NK-0001", Hypothesis: "Use sessionModel as fallback.",
		WhyRejected: "Overrides activation.", EvidenceRefs: []string{"E-17"},
		TargetScope: []string{"auth/"}, Status: StatusActiveNegativeKnowledge,
	}
	stale := NegativeKnowledge{
		ID: "NK-0002", Hypothesis: "Old idea.",
		WhyRejected: "Scope moved.", EvidenceRefs: []string{"E-9"},
		TargetScope: []string{"other/"}, Status: StatusStaleNegativeKnowledge,
	}
	out := Compactor{}.Compact(CompactInput{
		State:     st,
		Objective: "fix auth handler",
		Current:   ephemeral.StepDefinition{ID: "step-1", Goal: "fix handler"},
		Evidence: []ephemeral.EvidenceSummary{
			{Kind: "test", Subject: "auth", Digest: "abc123"},
		},
		Budget:    ephemeral.BudgetState{RecoveryAttemptsLeft: 2, MaxRecoveryAttempts: 3},
		Negatives: []NegativeKnowledge{nk, stale},
		Tier:      TierL1,
	})
	if out.Intent != "fix auth handler" {
		t.Fatalf("intent=%q", out.Intent)
	}
	if len(out.ActiveScope) != 1 || out.ActiveScope[0] != "auth/" {
		t.Fatalf("scope=%v", out.ActiveScope)
	}
	if out.CurrentStepID != "step-1" || out.RemainingBudget != 2 || out.MaxBudget != 3 {
		t.Fatalf("step/budget dropped: %+v", out)
	}
	if len(out.VerifiedEvidenceRefs) != 1 {
		t.Fatalf("evidence refs=%v", out.VerifiedEvidenceRefs)
	}
	if len(out.ActiveNegatives) != 1 || out.ActiveNegatives[0].ID != "NK-0001" {
		t.Fatalf("compaction must carry ACTIVE and drop STALE: %+v", out.ActiveNegatives)
	}
	if out.ContextTier != TierL1 || out.CheckpointID != "cp-1" {
		t.Fatalf("tier/checkpoint dropped: %+v", out)
	}
}

// Ladder token windows match the spec tiers.
func TestLadderTokenWindows(t *testing.T) {
	for tier, wantMax := range map[ContextTier]int{TierL0: 1000, TierL1: 4000, TierL2: 8000, TierL3: 15000, TierL4: 30000} {
		if got := tier.TokenBudget(); got != wantMax {
			t.Fatalf("tier %s budget=%d want %d", tier, got, wantMax)
		}
	}
	if _, ok := NewContextPlanner().Advance("t"); !ok {
		t.Fatal("first advance from L0 must succeed")
	}
	p := NewContextPlanner()
	for i := 0; i < 4; i++ {
		p.Advance("t-max")
	}
	if _, ok := p.Advance("t-max"); ok {
		t.Fatal("advance past L4 must fail")
	}
}
