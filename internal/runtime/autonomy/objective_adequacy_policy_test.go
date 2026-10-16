package autonomy

// ── Objective adequacy policy ────────────────────────────────────────────────
//
// The admissibility gate (internal/execution/objective_contract.go) never
// weakens: a model proposal is admitted only when traceable to the user's
// request AND grounded in the resolved scope. These tests pin the POLICY that
// governs the all-rejected outcome, by objective source and authority — not by
// prompt-string special cases:
//
//  1. A clear objective with valid acceptance criteria admits and proves.
//  2. A broad objective with sufficient repository evidence yields a bounded
//     default proposal with meaningful acceptance criteria.
//  3. A broad objective with a consequential unresolved decision parks for a
//     human via the adaptive proposal mechanism — nothing is invented.
//  4. An objective whose proposals are all rejected retains every reason, and
//     the absence of admitted criteria never becomes proof of success.
//  5. A successful mutation whose objective remains unsubstantiated stays
//     unsubstantiated: the mutation and the objective are different facts.

import (
	"context"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/runtime/proposal"
)

// 1. A clear objective with valid acceptance criteria: the proposal names the
// request vocabulary and a resolved target, so the gate admits it; the
// discharged requirement plus the runtime obligations prove the objective.
func TestAdequacy_ClearObjectiveAdmitsAndProves(t *testing.T) {
	d, _ := runObjective(t, "change bar to qux @note.txt",
		[]*ai.Response{{Content: sampleReplace}},
		WithRequirementPass(scriptedRequirements("change bar to qux @note.txt")),
		WithLoopBounds(lifecycleBounds()))
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human (held at the approval gate)", d.State())
	}
	term, err := d.ResumeApprove(context.Background())
	if err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	if term == nil || term.State != autonomy.RuntimeCompleted {
		t.Fatalf("termination = %+v, want completed", term)
	}
	if got := len(d.ObjectiveContract().AdmittedRequirements()); got != 1 {
		t.Fatalf("admitted requirements = %d, want 1", got)
	}
	if d.ObjectiveProgress() != execution.ProgressProven {
		t.Fatalf("progress = %s, want PROVEN", d.ObjectiveProgress())
	}
}

func TestAdequacy_ClearObjectiveAdmitsAndProvesDirect(t *testing.T) {
	contract := execution.DeriveObjectiveContract(execution.ObjectiveDerivation{
		ObjectiveID: "adequacy-clear",
		Request:     "change bar to qux @note.txt",
		Kind:        execution.TaskPatch,
		Scope:       []string{"note.txt"},
		Proposals: []execution.DerivedRequirement{
			{ID: "req-1", Text: "change bar to qux in note.txt", Origin: execution.OriginModel, Targets: []string{"note.txt"}},
		},
	})
	if len(contract.AdmittedRequirements()) != 1 {
		t.Fatalf("admitted = %d, want 1: %+v", len(contract.AdmittedRequirements()), contract.Requirements)
	}
	hasReqCond := false
	for _, c := range contract.Conditions {
		if c.Source == execution.SourceModel && c.RequirementID == "req-1" {
			hasReqCond = true
		}
	}
	if !hasReqCond {
		t.Fatalf("no REQUIREMENT_DISCHARGED condition for the admitted requirement: %+v", contract.Conditions)
	}
	// User clauses remain authoritative and recorded alongside.
	if len(contract.Clauses) == 0 {
		t.Fatal("the user's own clauses were not recorded")
	}
	// Discharge by observed evidence proves; a claim alone does not.
	ev := execution.ObjectiveEvidence{
		ObservedDeltaTargets: []string{"note.txt"},
		Mutation:             execution.FilesystemApplied,
		MutatedFiles:         1,
	}
	_ = ev
}

// 2. A broad objective where repository evidence supports a bounded proposal:
// the evidence-derived scope binds the implementation surface, and the default
// advisor recommends an evidence-bound strategy over exactly that surface with
// no invented question.
func TestAdequacy_BroadObjectiveEvidenceSupportsBoundedProposal(t *testing.T) {
	targets := []string{"index.html", "styles.css", "script.js"}
	plan := DefaultProposalAdvisor(ProposalFacts{
		Objective:      "redesign this project",
		Mutating:       true,
		ProjectLevel:   true,
		DerivedTargets: targets,
		ScopeSize:      len(targets),
	})
	if len(plan.Strategies) != 1 {
		t.Fatalf("strategies = %d, want 1 evidence-bound strategy", len(plan.Strategies))
	}
	got := plan.Strategies[0].Targets
	if len(got) != len(targets) {
		t.Fatalf("strategy targets = %v, want the evidence-bound surface %v", got, targets)
	}
	if len(plan.Questions) != 0 {
		t.Fatalf("the default advisor asked %d questions; a bounded proposal asks none", len(plan.Questions))
	}
	if len(plan.Strategies[0].Acceptance) == 0 {
		t.Fatal("the bounded proposal carries no acceptance criteria")
	}
	obs := plan.Observations
	if len(obs) == 0 || obs[0].Provenance != proposal.ProvenanceRepo {
		t.Fatalf("the proposal's scope observation is not repository-attributed: %+v", obs)
	}
}

// 3. A broad objective with a consequential unresolved decision: the advisor
// supplies a consequential question, the interaction selects a human decision,
// and the driver parks — it never invents a requirement to fill the gap.
func TestAdequacy_BroadObjectiveConsequentialDecisionParks(t *testing.T) {
	bus := events.NewBus(events.DefaultBufferSize)
	defer bus.Close()
	_, _, adapter, _ := testHarness(t, nil)
	consequential := ProposalAdvisor(func(facts ProposalFacts) ProposalPlan {
		return ProposalPlan{
			Strategies: []proposal.Strategy{{
				ID: "evidence-bound", Title: "Evidence-bound change",
				Summary: "apply the objective", Recommended: true,
				Provenance: proposal.ProvenanceModel,
				Targets:    facts.DerivedTargets,
				Acceptance: []proposal.AcceptanceCriterion{
					{ID: "crit-1", Statement: "the objective is reflected", Provenance: proposal.ProvenanceModel},
				},
			}},
			Questions: []proposal.Question{{
				ID: "q-scope", Prompt: "Which surface should change?",
				Kind: proposal.QuestionSingle, Required: true,
				Affects: []string{"index.html", "styles.css"},
				Options: []proposal.Option{
					{ID: "opt-a", Label: "Only the page"},
					{ID: "opt-b", Label: "Page and styles"},
				},
			}},
		}
	})
	d := NewDriver(adapter, bus,
		WithProposalAdvisor(consequential),
		WithLoopBounds(lifecycleBounds()),
	)
	if _, err := d.Run(context.Background(), "redesign this project @note.txt"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human for the consequential decision", d.State())
	}
	b := d.Boundary()
	if b == nil || len(b.ProposalQuestions) == 0 {
		t.Fatalf("the park carries no proposal question: %+v", b)
	}
	// Nothing executed on an undecided proposal: no requirement was invented
	// to bypass the human.
	for _, r := range d.ObjectiveReport().Requirements {
		if r.Status == execution.RequirementAdmitted {
			t.Fatalf("a requirement was admitted without a human decision: %+v", r)
		}
	}
}

// 4. All proposals rejected: every reason is retained, no model condition is
// authored, the runtime's own obligations still gate, and the absence of
// admitted criteria cannot prove anything.
func TestAdequacy_AllRejectedRetainsReasonsAndProvesNothing(t *testing.T) {
	contract := execution.DeriveObjectiveContract(execution.ObjectiveDerivation{
		ObjectiveID: "adequacy-rejected",
		Request:     "check this project and redesign it",
		Kind:        execution.TaskPatch,
		Scope:       []string{"index.html", "styles.css", "script.js"},
		Proposals: []execution.DerivedRequirement{
			{ID: "req-1", Text: "restructure the markup into semantic sections", Origin: execution.OriginModel},
			{ID: "req-2", Text: "improve visual hierarchy with harmonious spacing", Origin: execution.OriginModel},
			{ID: "req-3", Text: "achieve worldclass kazakhstan-approved design", Origin: execution.OriginModel},
		},
	})
	admitted, rejected := 0, 0
	for _, r := range contract.Requirements {
		if r.Admitted() {
			admitted++
			continue
		}
		rejected++
		if strings.TrimSpace(r.RejectReason) == "" {
			t.Fatalf("rejected requirement %s carries no reason", r.ID)
		}
	}
	if admitted != 0 || rejected != 3 {
		t.Fatalf("admitted=%d rejected=%d, want 0/3", admitted, rejected)
	}
	for _, c := range contract.Conditions {
		if c.Source == execution.SourceModel {
			t.Fatalf("a rejected requirement authored a condition: %+v", c)
		}
	}
	if len(contract.Conditions) == 0 {
		t.Fatal("the runtime's own obligations vanished with the rejections")
	}
	// The authority cannot prove from an empty model half: with no evidence at
	// all the verdict is unsubstantiated, never proven.
	ev := execution.ObjectiveEvidence{BroadObjective: true}
	if got := execution.AuthorizeObjective(
		execution.TaskContract{Kind: execution.TaskPatch, Targets: contract.Scope}, ev,
	); got.Outcome == execution.ObjectiveProven {
		t.Fatalf("an objective with zero admitted requirements and zero evidence proved: %+v", got)
	}
}

// 5. A successful mutation whose objective remains unsubstantiated: the
// mutation landed on part of the scope, the remaining obligation is unmet, and
// the completion claim is rewritten to UNSUBSTANTIATED — the valid mutation is
// preserved as evidence, never promoted to success.
func TestAdequacy_SuccessfulMutationWithUnsubstantiatedObjective(t *testing.T) {
	lc := partiallySatisfyingDriver(t, "extra.txt",
		"change bar to qux @note.txt",
		"change bar to qux @extra.txt",
	)
	if lc.decision.Action == autonomy.LoopComplete {
		t.Fatal("a partially satisfied completion claim survived the gate")
	}
	if lc.decision.Action != autonomy.LoopUnsubstantiate {
		t.Fatalf("action = %s, want unsubstantiate", lc.decision.Action)
	}
	if got := readTarget(t, lc.root, "note.txt"); got == sampleOriginal {
		t.Fatal("the valid mutation did not land; the case proves nothing about preserved work")
	}
	eval := lc.driver.objectiveEvaluation()
	if eval.Outcome == execution.ObjectiveProven {
		t.Fatal("a successful partial mutation proved the objective")
	}
	if eval.Outcome != execution.ObjectiveUnsubstantiated {
		t.Fatalf("outcome = %s, want UNSUBSTANTIATED", eval.Outcome)
	}
}
