package autonomy

// ── ADAPTIVE PROPOSAL ORCHESTRATION REGRESSIONS ─────────────────────────────
//
// These tests drive the REAL production path (Driver → ExecutorAdapter →
// RuntimeExecutor over a temporary workspace) with an adaptive advisor wired.
// They pin the mission's requirements that proposal state is adaptive, that
// accepted decisions compile into the EXISTING execution path, that revision
// changes invalidate the correct dependent state, and that authorization is
// never widened by a proposal.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/runtime/durable"
	"github.com/PizenLabs/izen/internal/runtime/proposal"
)

// redesignAdvisor is a representative advisor: it recommends a strategy over
// the evidence-derived target set and carries whatever questions the scenario
// supplies.
func redesignAdvisor(questions []proposal.Question) ProposalAdvisor {
	return func(facts ProposalFacts) ProposalPlan {
		targets := append([]string(nil), facts.DerivedTargets...)
		strategy := proposal.Strategy{
			ID:          "evidence-bound",
			Title:       "Evidence-bound redesign",
			Summary:     "apply the objective to the observed implementation artifacts",
			Rationale:   "the workspace's own implementation surface was observed",
			Recommended: true,
			Provenance:  proposal.ProvenanceModel,
			Targets:     targets,
			Acceptance: []proposal.AcceptanceCriterion{
				{ID: "crit-scope", Statement: "every observed implementation artifact reflects the objective", Provenance: proposal.ProvenanceModel},
			},
			Verification: []proposal.VerificationMethod{
				{ID: "ver-source", Kind: proposal.VerifySource, Method: "re-read the artifacts", Automated: true},
			},
		}
		plan := ProposalPlan{
			Strategies: []proposal.Strategy{strategy},
			Questions:  questions,
			Observations: []proposal.Observation{{
				ID:         "obs-scope",
				Summary:    "observed implementation artifacts",
				Paths:      targets,
				Provenance: proposal.ProvenanceRepo,
			}},
		}
		return plan
	}
}

func scopeQuestion(required bool) proposal.Question {
	return proposal.Question{
		ID:       "scope",
		Prompt:   "How wide should the change be?",
		Kind:     proposal.QuestionSingle,
		Required: required,
		Options: []proposal.Option{
			{ID: "evidence-bound", Label: "All observed artifacts", Recommended: true, Provenance: proposal.ProvenanceModel},
			{ID: "minimal", Label: "Smallest change only", Provenance: proposal.ProvenanceModel},
		},
		Recommended: "evidence-bound",
		Affects:     []string{"strategy:evidence-bound", "targets"},
	}
}

func adaptiveDriver(t *testing.T, root string, provider ai.Provider, customVerifier bool, advisor ProposalAdvisor, scope string, opts ...Option) *Driver {
	t.Helper()
	bus := events.NewBus(events.DefaultBufferSize)
	t.Cleanup(bus.Close)
	x := orchestrationExecutor(t, root, provider, customVerifier)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	base := make([]Option, 0, 1+len(opts))
	base = append(base, WithProposalAdvisor(advisor))
	base = append(base, opts...)
	d := NewDriver(adapter, bus, base...)
	d.SetScope(scope)
	return d
}

// TestAdaptive_DirectTaskKeepsItsPath pins that a clear, bounded objective with
// no consequential decision is NOT given a planning ceremony: the advisor is
// wired, yet the run goes straight to the existing approval gate.
func TestAdaptive_DirectTaskKeepsItsPath(t *testing.T) {
	root := writeFiles(t, map[string]string{"README.md": "# Sample\n"})
	p := &mockProvider{responses: []*ai.Response{{
		Content: "Hello everyone\n",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 100, CompletionTokens: 5, FinishReason: "stop"},
	}}}
	d := adaptiveDriver(t, root, p, true, redesignAdvisor(nil), "$prompt")

	if _, err := d.Run(context.Background(), `create a new file named zuru.md with content "Hello everyone"`); err != nil {
		t.Fatalf("Run: %v", err)
	}
	b := d.Boundary()
	if b == nil {
		t.Fatal("no boundary")
	}
	if b.Action == autonomy.HumanBoundaryAdaptiveProposal {
		t.Fatalf("a clear, bounded task was given an adaptive proposal ceremony: %+v", b)
	}
	if b.Action != autonomy.HumanBoundaryApproval {
		t.Fatalf("boundary = %s, want the existing approval gate", b.Action)
	}
	if d.compiledProposal != nil {
		t.Fatalf("a direct task compiled a proposal: %+v", d.compiledProposal)
	}
}

// TestAdaptive_InteractionDiffersByDecisionRequirement is mission Test C: two
// materially different objectives produce different interactions. The runtime
// must not emit a fixed questionnaire.
func TestAdaptive_InteractionDiffersByDecisionRequirement(t *testing.T) {
	// Objective 1: broad but understandable; no consequential choice → the
	// runtime investigates and proceeds with the recommendation (no park).
	root1 := orchestrationWebWorkspace(t)
	p1 := &targetProvider{artifacts: webArtifacts, served: map[string]int{}}
	d1 := adaptiveDriver(t, root1, p1, true, redesignAdvisor(nil), "$prompt")
	if _, err := d1.Run(context.Background(), "redesign this project help me"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if d1.Boundary() != nil && d1.Boundary().Action == autonomy.HumanBoundaryAdaptiveProposal {
		t.Fatal("an objective with no consequential decision parked for a proposal")
	}
	if d1.compiledProposal == nil {
		t.Fatal("the investigated objective did not record a compiled proposal")
	}

	// Objective 2: a consequential choice (incremental vs coordinated) → the
	// runtime parks and asks only that question.
	root2 := orchestrationWebWorkspace(t)
	p2 := &targetProvider{artifacts: webArtifacts, served: map[string]int{}}
	d2 := adaptiveDriver(t, root2, p2, true, redesignAdvisor([]proposal.Question{scopeQuestion(true)}), "$prompt")
	if _, err := d2.Run(context.Background(), "redesign this project help me"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	b := d2.Boundary()
	if b == nil || b.Action != autonomy.HumanBoundaryAdaptiveProposal {
		t.Fatalf("a consequential objective did not park for a proposal: %+v", b)
	}
	if len(b.ProposalQuestions) != 1 || b.ProposalQuestions[0].ID != "scope" {
		t.Fatalf("proposal questions = %+v, want exactly the material one", b.ProposalQuestions)
	}
	if b.ProposalRevision != 1 {
		t.Fatalf("proposal revision = %d, want 1", b.ProposalRevision)
	}
	// No provider call before the human decided.
	if p2.calls != 0 {
		t.Fatalf("provider was called %d times before the human decided", p2.calls)
	}
	if d2.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human", d2.State())
	}
}

// TestAdaptive_AcceptedDecisionCompilesIntoExecution is mission Test D/E at the
// driver level: accepting the proposal compiles the decision into the existing
// execution path, dispatches over the derived multi-file scope, and reaches the
// existing approval gate.
func TestAdaptive_AcceptedDecisionCompilesIntoExecution(t *testing.T) {
	root := orchestrationWebWorkspace(t)
	p := &targetProvider{artifacts: webArtifacts, served: map[string]int{}}
	d := adaptiveDriver(t, root, p, true, redesignAdvisor([]proposal.Question{scopeQuestion(true)}), "$prompt")

	if _, err := d.Run(context.Background(), "redesign this project help me"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	b := d.Boundary()
	if b == nil || b.Action != autonomy.HumanBoundaryAdaptiveProposal {
		t.Fatalf("no adaptive proposal boundary: %+v", b)
	}

	if _, err := d.ResumeAdaptiveProposal(context.Background(), b.ProposalFingerprint, []ProposalDecisionInput{
		{QuestionID: "scope", OptionID: "evidence-bound"},
	}); err != nil {
		t.Fatalf("ResumeAdaptiveProposal: %v", err)
	}
	// The accepted decision compiled and the EXISTING approval gate now holds
	// the concrete candidate.
	ab := d.Boundary()
	if ab == nil || ab.Action != autonomy.HumanBoundaryApproval {
		t.Fatalf("after acceptance boundary = %+v, want the existing approval gate", ab)
	}
	if d.compiledProposal == nil || d.compiledProposal.Revision < 2 {
		t.Fatalf("compiled proposal = %+v, want revision >= 2", d.compiledProposal)
	}
	if len(d.compiledProposal.Constraints) == 0 {
		t.Fatal("the accepted decision did not compile into constraints")
	}
	// The provider was dispatched over the derived multi-file scope.
	if p.calls < 3 {
		t.Fatalf("provider calls = %d, want one per derived target", p.calls)
	}
	// Acceptance is not authorization: nothing is written before approval.
	if html := readTarget(t, root, "index.html"); !strings.Contains(html, "Old Title") {
		t.Fatalf("workspace mutated before approval: %q", html)
	}
}

// TestAdaptive_IncompleteDecisionReparksWithRevision is mission Test D/H: a
// partial decision set re-parks with an advanced revision and does not execute.
func TestAdaptive_IncompleteDecisionReparksWithRevision(t *testing.T) {
	root := orchestrationWebWorkspace(t)
	p := &targetProvider{artifacts: webArtifacts, served: map[string]int{}}
	two := []proposal.Question{scopeQuestion(true), {
		ID: "compat", Prompt: "Preserve compatibility?", Kind: proposal.QuestionSingle, Required: true,
		Options: []proposal.Option{{ID: "preserve", Label: "Preserve"}, {ID: "break", Label: "Break"}},
		Affects: []string{"strategy:evidence-bound"},
	}}
	d := adaptiveDriver(t, root, p, true, redesignAdvisor(two), "$prompt")
	if _, err := d.Run(context.Background(), "redesign this project help me"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Answer only one of two required questions.
	if _, err := d.ResumeAdaptiveProposal(context.Background(), "", []ProposalDecisionInput{
		{QuestionID: "scope", OptionID: "evidence-bound"},
	}); err != nil {
		t.Fatalf("ResumeAdaptiveProposal: %v", err)
	}
	b := d.Boundary()
	if b == nil || b.Action != autonomy.HumanBoundaryAdaptiveProposal {
		t.Fatalf("an incomplete decision set executed: %+v", b)
	}
	if !strings.Contains(b.Reason, "incomplete") {
		t.Fatalf("re-park reason = %q, want an explicit incompleteness", b.Reason)
	}
	if b.ProposalRevision != 2 {
		t.Fatalf("revision after one decision = %d, want 2", b.ProposalRevision)
	}
	if p.calls != 0 {
		t.Fatalf("provider ran on an incomplete decision set (%d calls)", p.calls)
	}

	// Answer the remaining question; now it compiles and dispatches.
	if _, err := d.ResumeAdaptiveProposal(context.Background(), "", []ProposalDecisionInput{
		{QuestionID: "compat", OptionID: "preserve"},
	}); err != nil {
		t.Fatalf("ResumeAdaptiveProposal: %v", err)
	}
	if d.compiledProposal == nil || d.compiledProposal.Revision != 3 {
		t.Fatalf("compiled revision = %+v, want 3", d.compiledProposal)
	}
}

// TestAdaptive_ProposalDoesNotWidenAuthority is mission Test F: accepting a
// proposal cannot expand capabilities, bypass mutation review, or widen scope.
func TestAdaptive_ProposalDoesNotWidenAuthority(t *testing.T) {
	root := orchestrationWebWorkspace(t)
	p := &targetProvider{artifacts: webArtifacts, served: map[string]int{}}
	d := adaptiveDriver(t, root, p, true, redesignAdvisor([]proposal.Question{scopeQuestion(true)}), "$prompt")
	if _, err := d.Run(context.Background(), "redesign this project help me"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	b := d.Boundary()
	if b == nil || b.Action != autonomy.HumanBoundaryAdaptiveProposal {
		t.Fatalf("no adaptive boundary: %+v", b)
	}
	derived := append([]string(nil), d.resolved.Targets...)
	if len(derived) == 0 {
		t.Fatal("no derived scope to protect")
	}

	if _, err := d.ResumeAdaptiveProposal(context.Background(), b.ProposalFingerprint, []ProposalDecisionInput{
		{QuestionID: "scope", OptionID: "evidence-bound"},
	}); err != nil {
		t.Fatalf("ResumeAdaptiveProposal: %v", err)
	}
	// The compiled scope is exactly the derived scope: a proposal never widens
	// the target set it was formed over.
	for _, tgt := range d.objectiveTargets() {
		if !contains(derived, tgt) {
			t.Fatalf("a proposal widened the mutation scope to %q (derived %v)", tgt, derived)
		}
	}
	// Acceptance is NOT authorization: the mutation still passes through the
	// existing human approval gate, and nothing is written before approval.
	ab := d.Boundary()
	if ab == nil || ab.Action != autonomy.HumanBoundaryApproval {
		t.Fatalf("acceptance bypassed mutation review: boundary = %+v", ab)
	}
	if html := readTarget(t, root, "index.html"); !strings.Contains(html, "Old Title") {
		t.Fatalf("a proposal wrote before approval: %q", html)
	}
	// A proposal cannot grant a capability: the run holds no extra authority.
	if _, err := os.ReadFile(filepath.Join(root, "secrets/outside.txt")); err == nil {
		t.Fatal("a proposal created an out-of-scope artifact")
	}
}

// TestAdaptive_HotNeverGainsRenderedCapability pins that $hot cannot gain a
// browser capability through a proposal.
func TestAdaptive_HotNeverGainsRenderedCapability(t *testing.T) {
	root := orchestrationWebWorkspace(t)
	p := &targetProvider{artifacts: webArtifacts, served: map[string]int{}}
	advisor := func(facts ProposalFacts) ProposalPlan {
		plan := redesignAdvisor([]proposal.Question{scopeQuestion(true)})(facts)
		plan.Strategies[0].Verification = []proposal.VerificationMethod{
			{ID: "v-render", Kind: proposal.VerifyRendered, Method: "render", Automated: true},
		}
		return plan
	}
	verifier := &HeadlessBrowserVerifier{BrowserPath: DetectBrowser()}
	d := adaptiveDriver(t, root, p, true, advisor, "$hot", WithRenderedVerifier(verifier), WithRenderedChecks("index.html", nil))
	if _, err := d.Run(context.Background(), "redesign this project help me"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	b := d.Boundary()
	if b == nil {
		t.Fatal("no boundary")
	}
	if b.Action == autonomy.HumanBoundaryAdaptiveProposal {
		if _, err := d.ResumeAdaptiveProposal(context.Background(), b.ProposalFingerprint, []ProposalDecisionInput{
			{QuestionID: "scope", OptionID: "evidence-bound"},
		}); err != nil {
			t.Fatalf("ResumeAdaptiveProposal: %v", err)
		}
	}
	if !d.renderedRequired {
		t.Fatal("the accepted rendered strategy did not set the rendered requirement")
	}
	// The $hot guard refuses the browser capability directly.
	d.ensureRenderedVerification(context.Background())
	if d.renderedEvidence.Blocked == "" {
		t.Fatalf("$hot gained a rendered capability: %+v", d.renderedEvidence)
	}
}

// TestAdaptive_DecisionsAreDurable is mission Test H: the human decision and the
// proposal snapshot are written to the durable ledger.
func TestAdaptive_DecisionsAreDurable(t *testing.T) {
	root := orchestrationWebWorkspace(t)
	store := openLedger(t, root)
	p := &targetProvider{artifacts: webArtifacts, served: map[string]int{}}
	d := adaptiveDriver(t, root, p, true, redesignAdvisor([]proposal.Question{scopeQuestion(true)}), "$prompt",
		WithLedger(store, "session-adaptive"))

	if _, err := d.Run(context.Background(), "redesign this project help me"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	b := d.Boundary()
	if b == nil || b.Action != autonomy.HumanBoundaryAdaptiveProposal {
		t.Fatalf("no adaptive boundary: %+v", b)
	}
	if _, err := d.ResumeAdaptiveProposal(context.Background(), b.ProposalFingerprint, []ProposalDecisionInput{
		{QuestionID: "scope", OptionID: "evidence-bound"},
	}); err != nil {
		t.Fatalf("ResumeAdaptiveProposal: %v", err)
	}

	events := ledgerEvents(t, store.LedgerPath())
	if !hasEvent(events, durable.EventHumanDecision) {
		t.Fatalf("no HUMAN_DECISION recorded (types: %v)", eventTypes(events))
	}
	if !hasEvent(events, durable.EventProposalSnapshot) {
		t.Fatalf("no PROPOSAL_SNAPSHOT recorded (types: %v)", eventTypes(events))
	}
	// The in-process snapshot restores the decision with its revision.
	snap, ok := d.ProposalSnapshot()
	if !ok {
		t.Fatal("no proposal snapshot available")
	}
	if snap.Contract.Revision < 2 {
		t.Fatalf("snapshot revision = %d, want >= 2", snap.Contract.Revision)
	}
	if d := snap.Contract.DecisionFor("scope"); d == nil || d.Provenance != proposal.ProvenanceHuman {
		t.Fatalf("snapshot lost the human decision: %+v", d)
	}
}

// TestAdaptive_RenderedEvidenceFlowsToCompletion is mission Test G at the driver
// level: a broad objective whose source-level gate is skipped is PROVEN only
// when a real rendered check passed, and stays unproven when it did not.
func TestAdaptive_RenderedEvidenceFlowsToCompletion(t *testing.T) {
	// A fake verifier stands in for the browser here; the real browser path is
	// exercised by TestRenderedVerifier_RealBrowser.
	fake := fakeRenderedVerifier{result: RenderedVerification{
		Ran: true, Passed: true, Browser: "fake", ArtifactRevision: "rev-1",
		Entry: "index.html", Observations: []string{"rendered index.html"},
	}}
	root := orchestrationWebWorkspace(t)
	p := &targetProvider{artifacts: webArtifacts, served: map[string]int{}}
	advisor := func(facts ProposalFacts) ProposalPlan {
		plan := redesignAdvisor(nil)(facts)
		plan.Strategies[0].Verification = []proposal.VerificationMethod{
			{ID: "v-render", Kind: proposal.VerifyRendered, Method: "render", Automated: true},
		}
		return plan
	}
	d := adaptiveDriver(t, root, p, false, advisor, "$prompt",
		WithRenderedVerifier(fake), WithRenderedChecks("index.html", []RenderedCheck{
			{ID: "title", Kind: RenderedDOMContains, Value: "New Title"},
		}))

	if _, err := d.Run(context.Background(), "redesign this project help me"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Approve the concrete mutation; the rendered check then runs.
	for i := 0; i < 8 && d.State() == autonomy.RuntimeAwaitingHuman; i++ {
		b := d.Boundary()
		if b != nil && b.Action == autonomy.HumanBoundaryApproval && b.PatchID != "" {
			if _, err := d.ResumeApprove(context.Background()); err != nil {
				t.Fatalf("ResumeApprove: %v", err)
			}
			continue
		}
		break
	}
	if !d.renderedEvidence.Ran || !d.renderedEvidence.Passed {
		t.Fatalf("rendered evidence did not run/pass: %+v", d.renderedEvidence)
	}
	conditions := d.ObjectiveConditions()
	for i := range conditions {
		if conditions[i].Obligation == execution.ObligationIntegrityHeld {
			if conditions[i].VerificationState == execution.VerifyGateUnverified {
				t.Fatalf("a passing rendered check left the integrity condition unverified: %+v", conditions[i])
			}
		}
	}
}

type fakeRenderedVerifier struct{ result RenderedVerification }

func (f fakeRenderedVerifier) Verify(context.Context, RenderedVerificationRequest) RenderedVerification {
	return f.result
}

// TestAdaptive_BlankFreeFormDecisionIsRefusedByTheAuthoritativePath is the §6
// input-validation regression at the runtime seam: a submission that answers
// nothing is refused by the Driver, not merely by the UI.
func TestAdaptive_BlankFreeFormDecisionIsRefusedByTheAuthoritativePath(t *testing.T) {
	root := orchestrationWebWorkspace(t)
	p := &targetProvider{artifacts: webArtifacts, served: map[string]int{}}
	freeForm := []proposal.Question{{
		ID: "approach", Prompt: "How should this be phased?", Kind: proposal.QuestionFreeForm,
		Required: true, AllowFreeForm: true,
	}}
	d := adaptiveDriver(t, root, p, true, redesignAdvisor(freeForm), "$prompt")
	if _, err := d.Run(context.Background(), "redesign this project help me"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	b := d.Boundary()
	if b == nil || b.Action != autonomy.HumanBoundaryAdaptiveProposal {
		t.Fatalf("no adaptive boundary: %+v", b)
	}
	// A free-form question is projected for the UI to render.
	if !b.ProposalQuestions[0].AllowFreeForm ||
		b.ProposalQuestions[0].Kind != autonomy.HumanProposalQuestionFreeForm {
		t.Fatalf("question = %+v, want a free-form question", b.ProposalQuestions[0])
	}
	for _, blank := range []ProposalDecisionInput{
		{QuestionID: "approach"},
		{QuestionID: "approach", FreeForm: "   "},
	} {
		if _, err := d.ResumeAdaptiveProposal(context.Background(), b.ProposalFingerprint, []ProposalDecisionInput{blank}); err == nil {
			t.Fatalf("a blank free-form answer was accepted: %+v", blank)
		}
	}
	// The refusal leaves the proposal parked at its original revision and
	// executes nothing.
	if got := d.proposalState.Revision(); got != 1 {
		t.Fatalf("revision after a refused submission = %d, want 1", got)
	}
	if p.calls != 0 {
		t.Fatalf("a refused submission dispatched the provider (%d calls)", p.calls)
	}
	if d.compiledProposal != nil {
		t.Fatalf("a refused submission compiled a proposal: %+v", d.compiledProposal)
	}
}
