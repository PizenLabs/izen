package proposal

import (
	"strings"
	"testing"
	"time"
)

func baseContract() Contract {
	return Contract{
		ID:        "p1",
		Objective: "restructure the module",
		Strategies: []Strategy{
			{
				ID:          "incremental",
				Title:       "Incremental",
				Summary:     "change one module at a time",
				Recommended: true,
				Provenance:  ProvenanceModel,
				Targets:     []string{"a.go", "b.go"},
				Acceptance: []AcceptanceCriterion{
					{ID: "c1", Statement: "a.go compiles", Verification: "v1", Provenance: ProvenanceModel},
				},
				Verification: []VerificationMethod{
					{ID: "v1", Kind: VerifySource, Method: "go build", Automated: true},
				},
			},
			{
				ID:         "coordinated",
				Title:      "Coordinated",
				Summary:    "one atomic change",
				Provenance: ProvenanceModel,
				Targets:    []string{"a.go", "b.go", "c.go"},
			},
		},
		Recommended: "incremental",
	}
}

func TestContractValidate(t *testing.T) {
	c := baseContract()
	if err := c.Validate(); err != nil {
		t.Fatalf("minimal contract should validate: %v", err)
	}

	bad := baseContract()
	bad.Recommended = "ghost"
	if err := bad.Validate(); err == nil {
		t.Fatal("a recommendation naming an unknown strategy must fail validation")
	}

	dup := baseContract()
	dup.Strategies = append(dup.Strategies, dup.Strategies[0])
	if err := dup.Validate(); err == nil {
		t.Fatal("duplicate strategy ids must fail validation")
	}

	noObjective := baseContract()
	noObjective.Objective = "  "
	if err := noObjective.Validate(); err == nil {
		t.Fatal("an objective is required")
	}
}

func TestContractValidateQuestions(t *testing.T) {
	c := baseContract()
	c.Questions = []Question{{
		ID:       "scope",
		Prompt:   "how wide?",
		Kind:     QuestionSingle,
		Required: true,
		Options: []Option{
			{ID: "minimal", Label: "Minimal", Provenance: ProvenanceModel},
			{ID: "broad", Label: "Broad", Provenance: ProvenanceModel},
		},
		Recommended: "minimal",
	}}
	if err := c.Validate(); err != nil {
		t.Fatalf("valid question rejected: %v", err)
	}

	bad := c
	bad.Questions = []Question{{
		ID: "scope", Prompt: "how wide?", Kind: QuestionSingle,
		Options: []Option{{ID: "minimal"}}, Recommended: "ghost",
	}}
	if err := bad.Validate(); err == nil {
		t.Fatal("recommendation naming an unknown option must fail")
	}

	empty := c
	empty.Questions = []Question{{ID: "q", Prompt: "p", Kind: QuestionSingle}}
	if err := empty.Validate(); err == nil {
		t.Fatal("a single-choice question with no options and no free-form must fail")
	}
}

func TestSelectInteractionAdapts(t *testing.T) {
	cases := []struct {
		name string
		in   Signal
		want Interaction
	}{
		{"read-only", Signal{Objective: "explain x", Mutating: false}, InteractionDirect},
		{"ambiguity", Signal{Mutating: true, Ambiguity: true}, InteractionClarify},
		{"unresolved targeted", Signal{Mutating: true, Unresolved: true}, InteractionClarify},
		{"consequential", Signal{Mutating: true, Consequential: 2}, InteractionInvestigate},
		{"project-level", Signal{Mutating: true, ProjectLevel: true, DerivedTargets: []string{"a"}}, InteractionInvestigate},
		{"bounded explicit", Signal{Mutating: true, HasExplicitTarget: true, AutonomyPermitted: true}, InteractionDirect},
		{"default", Signal{Mutating: true}, InteractionDirect},
	}
	for _, tc := range cases {
		got := SelectInteraction(tc.in)
		if got.Interaction != tc.want {
			t.Errorf("%s: interaction = %s, want %s (%v)", tc.name, got.Interaction, tc.want, got.Reasons)
		}
		if len(got.Reasons) == 0 {
			t.Errorf("%s: every selection must justify itself", tc.name)
		}
	}
}

// TestSelectInteractionIsNotAFixedQuestionnaire pins the mission's core
// requirement: the interaction differs by the actual decision requirement, not
// by a constant.
func TestSelectInteractionIsNotAFixedQuestionnaire(t *testing.T) {
	simple := SelectInteraction(Signal{Mutating: true, HasExplicitTarget: true, AutonomyPermitted: true})
	complex := SelectInteraction(Signal{Mutating: true, Consequential: 3})
	if simple.Interaction == complex.Interaction {
		t.Fatalf("simple and complex objectives selected the same interaction %s", simple.Interaction)
	}
	if simple.RequireAnswer || !complex.RequireAnswer {
		t.Fatalf("interaction requirement did not track the decision requirement: simple=%+v complex=%+v", simple, complex)
	}
}

func TestConsequentialQuestions(t *testing.T) {
	formal := []Question{{ID: "a", Required: true, Options: []Option{{ID: "only"}}}}
	if n := ConsequentialQuestions(formal); n != 0 {
		t.Fatalf("a one-option formality counted as consequential: %d", n)
	}
	material := []Question{
		{ID: "a", Required: true, Options: []Option{{ID: "x"}, {ID: "y"}}},
		{ID: "b", Required: false, Options: []Option{{ID: "x"}, {ID: "y"}}},
		{ID: "c", Required: true, AllowFreeForm: true},
	}
	if n := ConsequentialQuestions(material); n != 2 {
		t.Fatalf("consequential count = %d, want 2", n)
	}
}

func TestStateRevisionAndInvalidation(t *testing.T) {
	c := baseContract()
	c.Strategies[0].Dependencies = []Dependency{
		{ID: "d1", From: "strategy:incremental", To: "phase:p1", Kind: "invalidates"},
	}
	c.Strategies[0].Phases = []Phase{{ID: "p1", Title: "phase one"}}
	c.Questions = []Question{{
		ID: "approach", Prompt: "which approach?", Kind: QuestionSingle, Required: true,
		Options: []Option{
			{ID: "incremental", Label: "Incremental"},
			{ID: "coordinated", Label: "Coordinated"},
		},
		Affects: []string{"strategy:incremental", "targets"},
	}}
	state, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision() != 1 {
		t.Fatalf("initial revision = %d, want 1", state.Revision())
	}

	// A model recommendation can never satisfy a question.
	if _, err := state.Apply(Decision{QuestionID: "approach", OptionID: "incremental", Provenance: ProvenanceModel}); err == nil {
		t.Fatal("a model recommendation satisfied a human question")
	}

	change, err := state.Apply(Decision{QuestionID: "approach", OptionID: "incremental", Provenance: ProvenanceHuman, At: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if change.NoOp || change.Revision != 2 {
		t.Fatalf("change = %+v, want revision 2", change)
	}
	if !contains(change.Invalidated, "strategy:incremental") || !contains(change.Invalidated, "targets") {
		t.Fatalf("change did not invalidate the affected elements: %v", change.Invalidated)
	}
	// Transitive invalidation through the dependency graph.
	if !contains(change.Invalidated, "phase:p1") {
		t.Fatalf("dependency-graph invalidation missing: %v", change.Invalidated)
	}
	if !change.ScopeChanged {
		t.Fatal("a decision affecting targets must report a scope change")
	}

	// Re-stating the same answer is a no-op.
	again, err := state.Apply(Decision{QuestionID: "approach", OptionID: "incremental", Provenance: ProvenanceHuman})
	if err != nil {
		t.Fatal(err)
	}
	if !again.NoOp || again.Revision != 2 {
		t.Fatalf("re-stating the same answer was not a no-op: %+v", again)
	}

	// Changing the answer is a new revision.
	changed, err := state.Apply(Decision{QuestionID: "approach", OptionID: "coordinated", Provenance: ProvenanceHuman})
	if err != nil {
		t.Fatal(err)
	}
	if changed.NoOp || changed.Revision != 3 {
		t.Fatalf("a changed answer must advance the revision: %+v", changed)
	}
	if changed.Previous == nil || changed.Previous.OptionID != "incremental" {
		t.Fatalf("the previous decision was not recorded: %+v", changed.Previous)
	}
}

func TestStateRejectsUnknownOption(t *testing.T) {
	c := baseContract()
	c.Questions = []Question{{
		ID: "approach", Prompt: "which?", Kind: QuestionSingle, Required: true,
		Options: []Option{{ID: "a", Label: "A"}},
	}}
	state, _ := New(c)
	if _, err := state.Apply(Decision{QuestionID: "approach", OptionID: "ghost", Provenance: ProvenanceHuman}); err == nil {
		t.Fatal("an unknown option must be refused")
	}
	if _, err := state.Apply(Decision{QuestionID: "nope", OptionID: "a", Provenance: ProvenanceHuman}); err == nil {
		t.Fatal("an unknown question must be refused")
	}
}

func TestCompileRequiresCompletion(t *testing.T) {
	c := baseContract()
	c.Questions = []Question{{
		ID: "approach", Prompt: "which?", Kind: QuestionSingle, Required: true,
		Options: []Option{{ID: "incremental", Label: "I"}, {ID: "coordinated", Label: "C"}},
	}}
	state, _ := New(c)
	if _, err := state.Compile(); err == nil {
		t.Fatal("an incomplete proposal compiled")
	}

	if _, err := state.Apply(Decision{QuestionID: "approach", OptionID: "coordinated", Provenance: ProvenanceHuman}); err != nil {
		t.Fatal(err)
	}
	compiled, err := state.Compile()
	if err != nil {
		t.Fatalf("complete proposal did not compile: %v", err)
	}
	if compiled.StrategyID != "coordinated" {
		t.Fatalf("strategy = %q, want the chosen one", compiled.StrategyID)
	}
	if len(compiled.Targets) != 3 {
		t.Fatalf("compiled targets = %v, want the strategy's three", compiled.Targets)
	}
	if len(compiled.Constraints) != 1 || !strings.Contains(compiled.Constraints[0], "approach") {
		t.Fatalf("accepted decisions were not compiled into constraints: %v", compiled.Constraints)
	}
	if compiled.Fingerprint == "" {
		t.Fatal("compiled contract carries no fingerprint")
	}
}

func TestCompilePhasesCarryTargetsAndAcceptance(t *testing.T) {
	c := baseContract()
	c.Strategies[0].Phases = []Phase{
		{ID: "phase-1", Title: "prepare", Order: 1, Targets: []string{"prep.go"}, Boundary: "checkpoint"},
		{ID: "phase-2", Title: "apply", Order: 2, Targets: []string{"apply.go"}},
	}
	c.Strategies[0].Acceptance = []AcceptanceCriterion{
		{ID: "a1", Statement: "prep is complete", Verification: "v1", Provenance: ProvenanceModel},
	}
	c.Strategies[0].Verification = []VerificationMethod{
		{ID: "v1", Kind: VerifySource, Method: "build", Automated: true},
	}
	state, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := state.Compile()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"a.go", "b.go", "prep.go", "apply.go"} {
		if !contains(compiled.Targets, want) {
			t.Errorf("phase/strategy target %q missing from the compiled scope: %v", want, compiled.Targets)
		}
	}
	if len(compiled.Acceptance) != 1 || compiled.Acceptance[0].ID != "a1" {
		t.Fatalf("acceptance criteria were not compiled: %+v", compiled.Acceptance)
	}
	if len(compiled.Verification) != 1 || compiled.Verification[0].Kind != VerifySource {
		t.Fatalf("verification methods were not compiled: %+v", compiled.Verification)
	}
}

func TestCompileRenderedRequirement(t *testing.T) {
	c := baseContract()
	c.Strategies[0].Verification = []VerificationMethod{
		{ID: "v1", Kind: VerifyRendered, Method: "render and check the composed output", Automated: true},
	}
	state, _ := New(c)
	compiled, err := state.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if !compiled.RequiresRenderedVerification() {
		t.Fatal("the compiled contract did not carry the rendered requirement")
	}
}

func TestPolicyDefaultNeverMasksAsHuman(t *testing.T) {
	c := baseContract()
	c.Questions = []Question{{
		ID: "approach", Prompt: "which?", Kind: QuestionSingle, Required: true,
		Options: []Option{{ID: "incremental", Label: "I"}, {ID: "coordinated", Label: "C"}},
		Default: "incremental",
	}}
	state, _ := New(c)
	if err := state.Complete(); err == nil {
		t.Fatal("an unanswered question must block completion")
	}
	change, err := state.ApplyPolicy("approach")
	if err != nil {
		t.Fatal(err)
	}
	if change.NoOp {
		t.Fatal("policy default was a no-op")
	}
	// The recorded decision must NOT be a human decision.
	cc := state.Contract()
	d := cc.DecisionFor("approach")
	if d == nil || d.Provenance != ProvenancePolicy {
		t.Fatalf("policy default recorded as %+v", d)
	}
	if err := state.Complete(); err != nil {
		t.Fatalf("a declared policy default must satisfy the question: %v", err)
	}
}

func TestPolicyDefaultRequiresDeclaredDefault(t *testing.T) {
	c := baseContract()
	c.Questions = []Question{{
		ID: "approach", Prompt: "which?", Kind: QuestionSingle, Required: true,
		Options: []Option{{ID: "a", Label: "A"}},
	}}
	state, _ := New(c)
	if _, err := state.ApplyPolicy("approach"); err == nil {
		t.Fatal("a question with no declared default must not be policy-answered")
	}
}

func TestSnapshotRestoreAndTamper(t *testing.T) {
	c := baseContract()
	c.Questions = []Question{{
		ID: "approach", Prompt: "which?", Kind: QuestionSingle, Required: true,
		Options: []Option{{ID: "incremental", Label: "I"}, {ID: "coordinated", Label: "C"}},
	}}
	state, _ := New(c)
	if _, err := state.Apply(Decision{QuestionID: "approach", OptionID: "incremental", Provenance: ProvenanceHuman}); err != nil {
		t.Fatal(err)
	}
	snap := state.Snapshot()
	restored, err := Restore(snap)
	if err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	if restored.Revision() != state.Revision() {
		t.Fatalf("restored revision = %d, want %d", restored.Revision(), state.Revision())
	}
	if rc := restored.Contract(); true {
		d := rc.DecisionFor("approach")
		if d == nil || d.OptionID != "incremental" {
			t.Fatalf("restored decisions lost: %+v", d)
		}
	}

	// A tampered snapshot must be refused rather than silently trusted.
	snap.Fingerprint = "deadbeefdeadbeef"
	if _, err := Restore(snap); err == nil {
		t.Fatal("a fingerprint mismatch must be refused")
	}
}

func TestRestoreRefusesNonHumanDecision(t *testing.T) {
	c := baseContract()
	c.Questions = []Question{{
		ID: "approach", Prompt: "which?", Kind: QuestionSingle, Required: true,
		Options: []Option{{ID: "a", Label: "A"}},
	}}
	snap := Snapshot{Contract: c}
	snap.Contract.Decisions = []Decision{{QuestionID: "approach", OptionID: "a", Provenance: ProvenanceModel}}
	snap.Fingerprint = snap.Contract.Fingerprint()
	if _, err := Restore(snap); err == nil {
		t.Fatal("a model decision in a snapshot must be refused on restore")
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
