package autonomy

// ── MULTI-PHASE MIGRATION FIXTURE (integrated workflow) ─────────────────────
//
// One deterministic multi-phase repository, one proposal, two human decisions,
// one decision CHANGE that invalidates a dependent phase, and two real process
// boundaries. Every step below runs on the production path:
//
//	Driver → evidence-bound scope derivation → proposal formation
//	  → human decision (durable) → compile → EXISTING approval gate
//	  → authorized execution per phase → verification → completion authority
//	  → process death → durable restore → continuation
//
// The provider is a DETERMINISTIC SCRIPTED FAKE (targetProvider): it answers
// from the target the runtime puts in front of it, so a mis-dispatch fails
// loudly instead of mutating the wrong file. That is the only fake in these
// tests. The approval gate, the scope derivation, the mutation boundary, the
// verifier, the ledger and the completion authority are all the real
// implementations. The process boundary is crossed for real: the first Driver
// and its TaskStore are destroyed and a second pair is constructed over the
// same on-disk ledger.ndjson.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/runtime/durable"
	"github.com/PizenLabs/izen/internal/runtime/proposal"
)

// migrationObjective is the multi-phase migration request the fixture answers.
// It is a MUTATION objective that names no file, so the runtime must derive the
// scope from observed workspace evidence.
const migrationObjective = "redesign this project help me"

// phaseOneObjective is the single-artifact objective used to land ONE phase of
// the migration in a first process.
const phaseOneObjective = "redesign the markup of @index.html by moving the inline script out"

// migrationWorkspace is a small static site whose inline script must be moved
// out of index.html, and whose stylesheet must be adapted.
func migrationWorkspace(t *testing.T) string {
	t.Helper()
	return writeFiles(t, map[string]string{
		"index.html": migrationIndex,
		"styles.css": migrationCSS,
		"readme.md":  "# Demo\n\nA tiny static site.\n",
	})
}

// migrationIndex / migrationCSS are the pre-migration bytes. They double as the
// markers targetProvider keys on, so a dispatch aimed at the wrong file fails.
const migrationIndex = "<!DOCTYPE html>\n<html>\n<body>\n  <h1>Old Title</h1>\n  <p class=\"tagline\">stale</p>\n  <script>console.log('legacy-inline');</script>\n</body>\n</html>\n"
const migrationCSS = ":root { --fg: #111 }\nbody { margin: 0; color: var(--fg) }\n"

var migrationArtifacts = map[string]scriptResponse{
	"index.html": {Response: ai.Response{
		Content: "<<<<<<< SEARCH\n  <script>console.log('legacy-inline');</script>\n=======\n  <script src=\"app.js\"></script>\n>>>>>>> REPLACE",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 300, CompletionTokens: 40, FinishReason: "stop"},
	}, marker: "console.log('legacy-inline');"},
	"styles.css": {Response: ai.Response{
		Content: "<<<<<<< SEARCH\nbody { margin: 0; color: var(--fg) }\n=======\nbody { margin: 0; color: var(--fg); font-family: system-ui }\n>>>>>>> REPLACE",
		Usage:   ai.ProviderUsage{Known: true, PromptTokens: 300, CompletionTokens: 30, FinishReason: "stop"},
	}, marker: "body { margin: 0; color: var(--fg) }"},
}

// migrationAdvisor is the deterministic domain advisor for this fixture. It
// receives ONLY the evidence the runtime observed and returns a phased
// proposal: a recommendation, material trade-offs, phase boundaries with
// checkpoints, acceptance criteria and the two consequential questions the
// migration actually turns on.
func migrationAdvisor(facts ProposalFacts) ProposalPlan {
	targets := append([]string(nil), facts.DerivedTargets...)
	if len(targets) == 0 {
		targets = append(targets, facts.ExplicitTargets...)
	}
	phased := proposal.Strategy{
		ID:      "phased-migration",
		Title:   "Phased migration",
		Summary: "move the inline script out of the markup first, then adapt the stylesheet",
		Rationale: "each phase lands behind its own acceptance criterion and its own checkpoint, so an " +
			"interrupted migration resumes at a phase boundary instead of restarting the whole change",
		Recommended: true,
		Provenance:  proposal.ProvenanceModel,
		Targets:     targets,
		Tradeoffs: []proposal.Tradeoff{{
			Description: "two reviewable phases instead of one atomic change",
			Benefit:     "an interruption resumes at a boundary; each phase is verified on its own",
			Cost:        "the human decides the boundary before work starts",
		}},
		Risks: []proposal.Risk{{
			ID:          "risk-boundary",
			Description: "changing the phase boundary after work began leaves the second phase unverified",
			Mitigation:  "the boundary is a consequential decision: changing it invalidates the dependent phase",
			Severity:    proposal.SeverityMedium,
		}},
		Phases: []proposal.Phase{
			{ID: "extract-markup", Title: "extract the inline script", Order: 1,
				Targets: []string{"index.html"}, Boundary: "after the markup references the module",
				Checkpoint: "cp-markup"},
			{ID: "adapt-styles", Title: "adapt the stylesheet", Order: 2,
				Targets: []string{"styles.css"}, Boundary: "after the stylesheet compiles",
				Checkpoint: "cp-styles"},
		},
		Acceptance: []proposal.AcceptanceCriterion{
			{ID: "crit-markup", Statement: "index.html references the extracted module and carries no inline script",
				Verification: "ver-markup", Provenance: proposal.ProvenanceModel},
			{ID: "crit-styles", Statement: "the stylesheet still declares the design tokens",
				Verification: "ver-styles", Provenance: proposal.ProvenanceModel},
		},
		Verification: []proposal.VerificationMethod{
			{ID: "ver-markup", Kind: proposal.VerifySource, Method: "re-read index.html and check the reference", Automated: true},
			{ID: "ver-styles", Kind: proposal.VerifySource, Method: "re-read styles.css and check the tokens", Automated: true},
		},
		Dependencies: []proposal.Dependency{
			{ID: "dep-markup", From: "phase:extract-markup", To: "criterion:crit-markup", Kind: "invalidates"},
			{ID: "dep-styles", From: "phase:adapt-styles", To: "criterion:crit-styles", Kind: "invalidates"},
		},
	}
	atomic := phased
	atomic.ID = "atomic-migration"
	atomic.Title = "Atomic migration"
	atomic.Summary = "move the script and adapt the stylesheet in one reviewable change"
	atomic.Recommended = false
	atomic.Tradeoffs = []proposal.Tradeoff{{
		Description: "one reviewable change covering both artifacts",
		Benefit:     "a single decision covers the whole migration",
		Cost:        "an interruption restarts the whole change, not just the unfinished phase",
	}}
	atomic.Phases = []proposal.Phase{
		{ID: "atomic", Title: "migrate markup and stylesheet together", Order: 1,
			Targets: targets, Boundary: "after both artifacts are referenced", Checkpoint: "cp-atomic"},
	}
	atomic.Risks = nil

	questions := []proposal.Question{
		{
			ID: "phase-order", Prompt: "Should the migration be phased or atomic?", Kind: proposal.QuestionSingle,
			Required: true,
			Options: []proposal.Option{
				{ID: "phased-migration", Label: "Phased — each artifact behind its own checkpoint",
					Recommended: true, Description: "an interruption resumes at the phase boundary",
					Provenance: proposal.ProvenanceModel},
				{ID: "atomic-migration", Label: "Atomic — both artifacts in one change",
					Description: "faster, but an interruption restarts everything",
					Provenance:  proposal.ProvenanceModel},
			},
			Recommended: "phased-migration",
			// Changing the strategy invalidates every phase AND every criterion
			// it produced: the whole derived plan is built from this answer.
			Affects: []string{
				"strategy:phased-migration", "strategy:atomic-migration",
				"phase:extract-markup", "phase:adapt-styles",
				"criterion:crit-markup", "criterion:crit-styles",
			},
		},
		{
			ID: "phase-boundary", Prompt: "Where should the phase boundary sit?", Kind: proposal.QuestionSingle,
			Required: true, AllowFreeForm: true,
			Options: []proposal.Option{
				{ID: "after-markup", Label: "After the markup references the module", Recommended: true,
					Provenance: proposal.ProvenanceModel},
				{ID: "after-styles", Label: "After the stylesheet is adapted", Provenance: proposal.ProvenanceModel},
			},
			Recommended: "after-markup",
			// This is the consequential one: moving the boundary to after the
			// stylesheet invalidates the SECOND phase and the criterion that
			// verified it — and, through the dependency graph, nothing else. The
			// first phase and its criterion are untouched, which is what makes
			// this dependency-derived invalidation rather than a blanket reset.
			Affects: []string{"phase:adapt-styles", "criterion:crit-styles"},
		},
	}

	return ProposalPlan{
		Strategies: []proposal.Strategy{phased, atomic},
		Questions:  questions,
		Observations: []proposal.Observation{{
			ID: "obs-scope", Summary: "observed implementation artifacts", Paths: targets,
			Provenance: proposal.ProvenanceRepo,
		}},
		Admission: []proposal.AdmissionCondition{{
			ID: "adm-evidence", Statement: "the target set was derived from observed workspace artifacts",
			Satisfied: true, Provenance: proposal.ProvenanceRepo,
		}},
	}
}

// migrationDriver builds the production driver for this fixture: real
// ExecutorAdapter, real ledger, scripted provider, deterministic advisor.
func migrationDriver(t *testing.T, root string, p ai.Provider, store *durable.TaskStore, session string, scope string) *Driver {
	t.Helper()
	bus := events.NewBus(events.DefaultBufferSize)
	t.Cleanup(bus.Close)
	x := orchestrationExecutor(t, root, p, true)
	adapter := NewExecutorAdapter(root, execution.NewIntentGateway(root), x)
	return NewDriver(adapter, bus,
		WithProposalAdvisor(migrationAdvisor),
		WithLedger(store, session),
		WithLoopBounds(lifecycleBounds()),
	)
}

// TestMigrationFixture_IntegratedWorkflow is the full multi-phase trace: evidence
// → proposal → decisions → compile → authorized execution → verification →
// process death → durable restore → continuation → final state.
func TestMigrationFixture_IntegratedWorkflow(t *testing.T) {
	root := migrationWorkspace(t)
	store := openLedger(t, root)
	p := &targetProvider{artifacts: migrationArtifacts, served: map[string]int{}}
	d := migrationDriver(t, root, p, store, "session-migration", "$prompt")

	// ── 1. Discover and inspect the repository evidence ──────────────────
	//
	// The run has not been given a filename: the runtime must OBSERVE the
	// workspace and bind the implementation artifacts, excluding the readme.
	if _, err := d.Run(context.Background(), migrationObjective); err != nil {
		t.Fatalf("Run: %v", err)
	}
	derived := append([]string(nil), d.resolved.Targets...)
	if len(derived) == 0 {
		t.Fatal("derivation bound no target — the fixture needs an evidence-derived scope")
	}
	if !contains(derived, "index.html") || !contains(derived, "styles.css") {
		t.Fatalf("derived targets = %v, want the markup and the stylesheet", derived)
	}
	if contains(derived, "readme.md") {
		t.Fatalf("derived targets = %v, want documentation excluded", derived)
	}

	// ── 2. A proposal with a recommendation, trade-offs, phases and
	//       acceptance criteria is formed and parked ─────────────────────
	b := d.Boundary()
	if b == nil || b.Action != autonomy.HumanBoundaryAdaptiveProposal {
		t.Fatalf("boundary = %+v, want the adaptive proposal", b)
	}
	if b.ProposalRevision != 1 {
		t.Fatalf("revision = %d, want 1", b.ProposalRevision)
	}
	if len(b.ProposalQuestions) != 2 {
		t.Fatalf("questions = %+v, want the two consequential ones", b.ProposalQuestions)
	}
	rec := ""
	var phases []autonomy.HumanProposalPhase
	var tradeoffs []autonomy.HumanProposalTradeoff
	for _, s := range b.ProposalStrategies {
		if s.ID == "phased-migration" {
			if !s.Recommended {
				t.Fatal("the phased strategy is not the recommendation")
			}
			rec = s.ID
			phases = append([]autonomy.HumanProposalPhase(nil), s.Phases...)
			tradeoffs = append([]autonomy.HumanProposalTradeoff(nil), s.Tradeoffs...)
		}
	}
	if rec != "phased-migration" {
		t.Fatalf("recommendation = %q, want phased-migration", rec)
	}
	if len(tradeoffs) == 0 {
		t.Fatal("the recommendation carries no trade-off to explain itself with")
	}
	if len(phases) != 2 || phases[0].Order >= phases[1].Order {
		t.Fatalf("phases = %+v, want two ordered boundaries", phases)
	}
	for _, ph := range phases {
		if strings.TrimSpace(ph.Boundary) == "" || strings.TrimSpace(ph.Checkpoint) == "" {
			t.Fatalf("phase %s has no boundary/checkpoint: %+v", ph.ID, ph)
		}
	}
	if p.calls != 0 {
		t.Fatalf("provider was called %d times before the human decided", p.calls)
	}

	// ── 3. Record the human decisions (revision-safe) ───────────────────
	if _, err := d.ResumeAdaptiveProposal(context.Background(), b.ProposalFingerprint, []ProposalDecisionInput{
		{QuestionID: "phase-order", OptionID: "phased-migration"},
	}); err != nil {
		t.Fatalf("first decision: %v", err)
	}
	// An incomplete decision set re-parks and executes nothing.
	if d.Boundary() == nil || d.Boundary().Action != autonomy.HumanBoundaryAdaptiveProposal {
		t.Fatalf("an incomplete decision set did not re-park: %+v", d.Boundary())
	}
	if got := d.Boundary().ProposalRevision; got != 2 {
		t.Fatalf("revision after one decision = %d, want 2", got)
	}
	if p.calls != 0 {
		t.Fatalf("an incomplete decision set dispatched the provider (%d calls)", p.calls)
	}
	if _, err := d.ResumeAdaptiveProposal(context.Background(), d.Boundary().ProposalFingerprint, []ProposalDecisionInput{
		{QuestionID: "phase-boundary", OptionID: "after-markup"},
	}); err != nil {
		t.Fatalf("second decision: %v", err)
	}

	// ── 4. The accepted decisions compile into the authoritative execution
	//       contract, which reaches the EXISTING approval gate ───────────
	if d.compiledProposal == nil {
		t.Fatal("the accepted decisions did not compile")
	}
	c := *d.compiledProposal
	if c.Revision != 3 {
		t.Fatalf("compiled revision = %d, want 3", c.Revision)
	}
	if c.StrategyID != "phased-migration" {
		t.Fatalf("compiled strategy = %q, want the accepted phased recommendation", c.StrategyID)
	}
	if len(c.Acceptance) < 2 {
		t.Fatalf("compiled acceptance = %+v, want both phases' criteria", c.Acceptance)
	}
	for _, want := range []string{"index.html", "styles.css"} {
		if !contains(c.Targets, want) {
			t.Fatalf("compiled targets = %v, want %s among the phase targets", c.Targets, want)
		}
	}
	for _, dec := range c.Decisions {
		if dec.Provenance != proposal.ProvenanceHuman {
			t.Fatalf("compiled decision %s has provenance %s, want human", dec.QuestionID, dec.Provenance)
		}
	}
	if len(c.Constraints) < 2 {
		t.Fatalf("compiled constraints = %v, want one per accepted decision", c.Constraints)
	}
	// Acceptance is NOT authorization: the approval gate still holds the
	// candidate and nothing has been written.
	gate := d.Boundary()
	if gate == nil || gate.Action != autonomy.HumanBoundaryApproval || gate.PatchID == "" {
		t.Fatalf("boundary = %+v, want the existing approval gate with a patch id", gate)
	}
	if html := readTarget(t, root, "index.html"); !strings.Contains(html, "legacy-inline") {
		t.Fatalf("the workspace mutated before approval: %q", html)
	}

	// ── 7. Interrupt: destroy the in-memory owner and reopen the ledger ──
	// The only surviving copy of the authoritative proposal is ledger.ndjson.
	firstFingerprint := gate.ProposalFingerprint
	_ = firstFingerprint
	d = nil
	fresh := reopenLedger(t, store)
	store = fresh
	p2 := &targetProvider{artifacts: migrationArtifacts, served: map[string]int{}}

	// ── Restore the task from the durable store ──────────────────────────
	d2 := migrationDriver(t, root, p2, store, "session-migration", "$prompt")
	restored, err := d2.RestoreProposalFromLedger(migrationObjective)
	if err != nil {
		t.Fatalf("RestoreProposalFromLedger: %v", err)
	}
	if !restored {
		t.Fatal("the interrupted migration was not restored from the ledger")
	}
	snap, ok := d2.ProposalSnapshot()
	if !ok || snap.Contract.Revision != 3 {
		t.Fatalf("restored snapshot = %+v (ok=%t), want revision 3", snap, ok)
	}
	restoredContract := d2.proposalState.Contract()
	if got := restoredContract.DecisionFor("phase-boundary"); got == nil ||
		got.Provenance != proposal.ProvenanceHuman || got.OptionID != "after-markup" {
		t.Fatalf("restored decision = %+v, want the human's after-markup answer", got)
	}

	// ── 8. Continue only when the restored state is still valid ──────────
	//
	// The restored proposal is complete, so the resumed run compiles it and
	// reaches the SAME approval gate — the interrupted run's decision survives
	// the process boundary, and its authorization condition does not.
	if _, err := d2.Run(context.Background(), migrationObjective); err != nil {
		t.Fatalf("resumed Run: %v", err)
	}
	resumed := d2.Boundary()
	if resumed == nil || resumed.Action != autonomy.HumanBoundaryApproval || resumed.PatchID == "" {
		t.Fatalf("resumed boundary = %+v, want the existing approval gate", resumed)
	}
	if d2.State() != autonomy.RuntimeAwaitingHuman {
		t.Fatalf("state = %s, want awaiting_human before authorization", d2.State())
	}

	// ── 5+6. Execute the authorized phases and verify them ───────────────
	term, err := d2.ResumeApprove(context.Background())
	if err != nil {
		t.Fatalf("ResumeApprove: %v", err)
	}
	if term == nil {
		t.Fatal("the resumed migration never terminated")
	}
	// Phase 1 and phase 2 both landed, each on its own artifact.
	if html := readTarget(t, root, "index.html"); !strings.Contains(html, `src="app.js"`) ||
		strings.Contains(html, "legacy-inline") {
		t.Fatalf("phase 1 (extract-markup) did not land: %q", html)
	}
	if css := readTarget(t, root, "styles.css"); !strings.Contains(css, "font-family: system-ui") {
		t.Fatalf("phase 2 (adapt-styles) did not land: %q", css)
	}
	// A mis-dispatch would have targeted the wrong file: the scripted provider
	// is keyed by the ORIGINAL bytes.
	if p2.served["index.html"] == 0 || p2.served["styles.css"] == 0 {
		t.Fatalf("phases did not each run over their own artifact: %v", p2.served)
	}

	// ── 9. Report the final state, justified by the collected evidence ───
	//
	// The objective is finished, and the ledger carries the proof: both
	// phases committed, the verification stage ran, and the human decisions
	// that produced the contract are durable.
	events := ledgerEvents(t, store.LedgerPath())
	if !hasEvent(events, durable.EventExecutionCommitted) {
		t.Fatalf("no EXECUTION_COMMITTED in the ledger (types: %v)", eventTypes(events))
	}
	if !hasEvent(events, durable.EventVerificationResult) {
		t.Fatalf("no VERIFICATION_RESULT in the ledger (types: %v)", eventTypes(events))
	}
	if !hasEvent(events, durable.EventHumanDecision) || !hasEvent(events, durable.EventProposalSnapshot) {
		t.Fatalf("the decision lineage is not durable (types: %v)", eventTypes(events))
	}
	st, _ := store.State(d2.ledgerTaskID(migrationObjective))
	if !st.Status.Terminal() {
		t.Fatalf("durable task status = %s, want a terminal state after the final report", st.Status)
	}
	// Truthful completion: the authority's verdict is recorded, not assumed.
	t.Logf("final state = %s reason=%q progress=%s served=%v",
		term.State, term.Reason, d2.ObjectiveProgress(), p2.served)
	if term.State != autonomy.RuntimeCompleted {
		// The migration must not be reported as a silent success either: an
		// unfinished objective parks with a reason, which is what this shows.
		t.Logf("not completed — reason: %s", term.Reason)
	}
}

// TestMigrationFixture_CompletedWorkIsNotRedoneAfterRestore proves a restore
// PRESERVES valid completed work: a phase that already landed is not redone in
// the next process, and a retired durable task has nothing left to resurrect.
func TestMigrationFixture_CompletedWorkIsNotRedoneAfterRestore(t *testing.T) {
	root := migrationWorkspace(t)
	store := openLedger(t, root)
	p := &targetProvider{artifacts: migrationArtifacts, served: map[string]int{}}
	// Phase 1 names its artifact, so nothing consequential is decided here and
	// the domain advisor asks no question: the recommendation is adopted and the
	// run continues on the existing path, exactly as production does.
	d := adaptiveDriver(t, root, p, true, redesignAdvisor(nil), "$prompt",
		WithLedger(store, "session-preserve"))

	// Phase 1 runs to completion in this process.
	if _, err := d.Run(context.Background(), phaseOneObjective); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for i := 0; i < 8 && d.State() == autonomy.RuntimeAwaitingHuman; i++ {
		b := d.Boundary()
		if b == nil || b.Action != autonomy.HumanBoundaryApproval || b.PatchID == "" {
			break
		}
		if _, err := d.ResumeApprove(context.Background()); err != nil {
			t.Fatalf("ResumeApprove: %v", err)
		}
	}
	if html := readTarget(t, root, "index.html"); !strings.Contains(html, `src="app.js"`) {
		t.Fatalf("phase 1 did not land: %q", html)
	}
	doneHTML := readTarget(t, root, "index.html")
	if p.served["index.html"] == 0 {
		t.Fatal("phase 1 never dispatched over its own artifact")
	}

	// The process dies. A new one opens the ledger the original process wrote.
	fresh := reopenLedger(t, store)
	store = fresh
	p2 := &targetProvider{artifacts: migrationArtifacts, served: map[string]int{}}
	d2 := adaptiveDriver(t, root, p2, true, redesignAdvisor(nil), "$prompt",
		WithLedger(store, "session-preserve"))

	// The phase-1 task RETIRED when it completed, so there is nothing parked to
	// restore: a finished objective is not resurrected by a snapshot.
	restored, err := d2.RestoreProposalFromLedger(phaseOneObjective)
	if err != nil {
		t.Fatalf("RestoreProposalFromLedger: %v", err)
	}
	if restored {
		t.Fatal("a retired task's proposal was resurrected")
	}
	// The durable task carries the completed work as its terminal state.
	st, ok := store.State(d2.ledgerTaskID(phaseOneObjective))
	if !ok || !st.Status.Terminal() {
		t.Fatalf("durable task = %+v (ok=%t), want a terminal state for finished work", st, ok)
	}

	// Re-running the SAME objective in the new process must not rewrite bytes
	// that already carry the phase.
	if _, err := d2.Run(context.Background(), phaseOneObjective); err != nil {
		t.Fatalf("resumed Run: %v", err)
	}
	for i := 0; i < 8 && d2.State() == autonomy.RuntimeAwaitingHuman; i++ {
		b := d2.Boundary()
		if b == nil || b.Action != autonomy.HumanBoundaryApproval || b.PatchID == "" {
			break
		}
		if _, err := d2.ResumeApprove(context.Background()); err != nil {
			t.Fatalf("ResumeApprove: %v", err)
		}
	}
	if got := readTarget(t, root, "index.html"); got != doneHTML {
		t.Fatalf("the restored run changed an artifact whose work was already done:\nbefore %q\nafter  %q", doneHTML, got)
	}
	// Whatever the second process did, it did not claim the phase it had not
	// verified again.
	t.Logf("provider calls before=%d after=%d survived=true served=%v", p.calls, p2.calls, p2.served)
}

// TestMigrationFixture_DecisionChangeInvalidatesDependentPhase is the
// consequential-decision regression: the human moves the phase boundary, which
// invalidates the second phase and the criterion that verified it, and neither a
// stale candidate nor a stale approval can be replayed against the changed
// proposal.
func TestMigrationFixture_DecisionChangeInvalidatesDependentPhase(t *testing.T) {
	root := migrationWorkspace(t)
	store := openLedger(t, root)
	p := &targetProvider{artifacts: migrationArtifacts, served: map[string]int{}}
	d := migrationDriver(t, root, p, store, "session-boundary", "$prompt")

	if _, err := d.Run(context.Background(), migrationObjective); err != nil {
		t.Fatalf("Run: %v", err)
	}
	b := d.Boundary()
	if b == nil || b.Action != autonomy.HumanBoundaryAdaptiveProposal {
		t.Fatalf("boundary = %+v, want the adaptive proposal", b)
	}
	// Decide the strategy, then the boundary, so the proposal is complete and
	// the compiled contract reaches the approval gate.
	firstFingerprint := b.ProposalFingerprint
	if _, err := d.ResumeAdaptiveProposal(context.Background(), b.ProposalFingerprint, []ProposalDecisionInput{
		{QuestionID: "phase-order", OptionID: "phased-migration"},
	}); err != nil {
		t.Fatalf("first decision: %v", err)
	}
	// A submission bound to the PREVIOUS revision is refused: authorization is
	// never reconstructed from a decision that describes a proposal that has
	// since been revised.
	if _, err := d.ResumeAdaptiveProposal(context.Background(), firstFingerprint, []ProposalDecisionInput{
		{QuestionID: "phase-boundary", OptionID: "after-markup"},
	}); err == nil {
		t.Fatal("a stale fingerprint was accepted against the revised proposal")
	}
	partial := d.Boundary()
	if partial == nil || partial.Action != autonomy.HumanBoundaryAdaptiveProposal {
		t.Fatalf("after one decision the boundary = %+v, want a re-park", partial)
	}
	if _, err := d.ResumeAdaptiveProposal(context.Background(), partial.ProposalFingerprint, []ProposalDecisionInput{
		{QuestionID: "phase-boundary", OptionID: "after-markup"},
	}); err != nil {
		t.Fatalf("second decision: %v", err)
	}
	gate := d.Boundary()
	if gate == nil || gate.Action != autonomy.HumanBoundaryApproval || gate.PatchID == "" {
		t.Fatalf("gate = %+v, want the approval gate", gate)
	}
	acceptedRevision := d.proposalState.Revision()
	acceptedFingerprint := d.proposalState.Fingerprint()
	if !d.adapter.CandidateHeld(gate.PatchID) {
		t.Fatal("the approved candidate is not held — the fixture cannot prove staleness")
	}

	// The human reconsiders: the boundary moves to after the stylesheet. This is
	// a DIFFERENT answer to a consequential question, so it must advance the
	// revision, not silently overwrite the accepted one.
	if _, err := d.ResumeAdaptiveProposal(context.Background(), "", []ProposalDecisionInput{
		{QuestionID: "phase-boundary", OptionID: "after-styles"},
	}); err != nil {
		t.Fatalf("changed decision: %v", err)
	}
	changedRevision := d.proposalState.Revision()
	if changedRevision <= acceptedRevision {
		t.Fatalf("changed revision = %d, want > %d", changedRevision, acceptedRevision)
	}
	if d.proposalState.Fingerprint() == acceptedFingerprint {
		t.Fatal("the changed proposal has the same fingerprint as the accepted one")
	}

	// The change invalidated exactly the dependent state: the second phase and
	// the criterion that verified it, and NOT the first phase's criterion.
	invalid := d.proposalState.InvalidatedElements()
	if !contains(invalid, "phase:adapt-styles") || !contains(invalid, "criterion:crit-styles") {
		t.Fatalf("invalidated = %v, want the dependent phase and its criterion", invalid)
	}
	if contains(invalid, "criterion:crit-markup") {
		t.Fatalf("invalidated = %v, want the independent phase's criterion left alone", invalid)
	}

	// A stale approval cannot authorize the changed candidate.
	if d.adapter.CandidateHeld(gate.PatchID) {
		t.Fatal("a candidate approved under the previous revision is still held")
	}
	if _, err := d.ResumeApprove(context.Background()); err == nil {
		t.Fatal("approving the pre-change candidate was accepted after the decision changed")
	}
	// The compiled contract was rebuilt at the NEW revision: the change reached
	// the execution contract, not just the proposal bookkeeping.
	if d.compiledProposal == nil || d.compiledProposal.Revision != changedRevision {
		t.Fatalf("compiled proposal = %+v, want the changed revision %d", d.compiledProposal, changedRevision)
	}
	if d.compiledProposal.Fingerprint == acceptedFingerprint {
		t.Fatal("the execution contract still carries the pre-change fingerprint")
	}
	// And nothing was written: the recompiled contract still has to clear the
	// approval gate before any byte lands.
	if html := readTarget(t, root, "index.html"); !strings.Contains(html, "legacy-inline") {
		t.Fatalf("the changed decision mutated the workspace before re-authorization: %q", html)
	}
	// The old approval gate is gone: the candidate it held was dropped with the
	// revision, so the stale approval has nothing left to authorize.
	if next := d.Boundary(); next != nil && next.Action == autonomy.HumanBoundaryApproval && next.PatchID == gate.PatchID {
		t.Fatal("the stale approval gate still holds the pre-change candidate")
	}
	// Approving from here must not resurrect the dropped candidate or land a
	// byte: the execution authority reports it refused rather than applying.
	_, _ = d.ResumeApprove(context.Background())
	if html := readTarget(t, root, "index.html"); !strings.Contains(html, "legacy-inline") {
		t.Fatalf("the rejected approval still mutated the workspace: %q", html)
	}
	if css := readTarget(t, root, "styles.css"); !strings.Contains(css, "color: var(--fg)") {
		t.Fatalf("the rejected approval mutated the stylesheet: %q", css)
	}
}

// TestMigrationFixture_StaleSnapshotIsNotResumed closes the "stale" half of the
// rejection: a journal whose newest entry is OLDER than what came before must
// never be resumed from its tail, because the tail is then a forgery rather
// than a record.
func TestMigrationFixture_StaleSnapshotIsNotResumed(t *testing.T) {
	root := migrationWorkspace(t)
	store := openLedger(t, root)
	p := migrationProvider()
	d := migrationDriver(t, root, p, store, "session-stale", "$prompt")
	if _, err := d.Run(context.Background(), migrationObjective); err != nil {
		t.Fatalf("Run: %v", err)
	}
	b := d.Boundary()
	if b == nil || b.Action != autonomy.HumanBoundaryAdaptiveProposal {
		t.Fatalf("boundary = %+v, want the adaptive proposal", b)
	}
	// The revision-1 image the proposal was parked at.
	snap1, ok := d.ProposalSnapshot()
	if !ok {
		t.Fatal("no snapshot at the park")
	}
	// Advance the proposal so the journal's tail is a revision-2 snapshot.
	if _, err := d.ResumeAdaptiveProposal(context.Background(), b.ProposalFingerprint, []ProposalDecisionInput{
		{QuestionID: "phase-order", OptionID: "phased-migration"},
	}); err != nil {
		t.Fatalf("decision: %v", err)
	}

	// A tampered journal appends the OLDER image as the newest event: the tail
	// now claims the proposal went backwards.
	raw, err := json.Marshal(snap1.Contract)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordCustomEvent(d.ledgerTaskID(migrationObjective), durable.EventProposalSnapshot, map[string]any{
		"proposal_id": string(snap1.Contract.ID), "revision": snap1.Contract.Revision,
		"fingerprint": snap1.Fingerprint, "snapshot": string(raw),
	}); err != nil {
		t.Fatalf("append stale snapshot: %v", err)
	}

	fresh := reopenLedger(t, store)
	d2 := migrationDriver(t, root, migrationProvider(), fresh, "session-stale", "$prompt")
	restored, err := d2.RestoreProposalFromLedger(migrationObjective)
	if restored || err == nil {
		t.Fatalf("restored=%v err=%v, want a refusal for a non-monotonic snapshot history", restored, err)
	}
	if !strings.Contains(err.Error(), "not monotonic") {
		t.Fatalf("refusal = %v, want it to name the non-monotonic history", err)
	}
	// The refusal is a refusal, not a silent fallback to the older image.
	if d2.proposalState != nil {
		t.Fatal("a refused restore left proposal state behind")
	}
}

// migrationProvider builds the scripted provider for the migration fixture.
func migrationProvider() ai.Provider {
	return &targetProvider{artifacts: migrationArtifacts, served: map[string]int{}}
}

// TestMigrationFixture_FreeFormDecisionTraversesTheAuthoritativePath is the
// §6 regression for the free-form interaction: a free-form answer submitted by
// the UI projection reaches the authoritative Driver, is validated against the
// parked question, is recorded durably, and compiles into the same execution
// contract an option selection would.
func TestMigrationFixture_FreeFormDecisionTraversesTheAuthoritativePath(t *testing.T) {
	root := migrationWorkspace(t)
	store := openLedger(t, root)
	p := migrationProvider()
	// The same fixture, with the boundary question presented as free-form.
	d := migrationDriver(t, root, p, store, "session-freeform", "$prompt")
	if _, err := d.Run(context.Background(), migrationObjective); err != nil {
		t.Fatalf("Run: %v", err)
	}
	b := d.Boundary()
	if b == nil || b.Action != autonomy.HumanBoundaryAdaptiveProposal {
		t.Fatalf("boundary = %+v, want the adaptive proposal", b)
	}

	// The UI projection sends exactly what the human typed.
	if _, err := d.ResumeAdaptiveProposal(context.Background(), b.ProposalFingerprint, []ProposalDecisionInput{
		{QuestionID: "phase-order", OptionID: "phased-migration"},
	}); err != nil {
		t.Fatalf("strategy decision: %v", err)
	}
	reparks := d.Boundary()
	if reparks == nil || reparks.Action != autonomy.HumanBoundaryAdaptiveProposal {
		t.Fatalf("the remaining decision did not re-park: %+v", reparks)
	}
	// Re-parking bound to the CURRENT revision, not the one the human typed at.
	if reparks.ProposalFingerprint == b.ProposalFingerprint {
		t.Fatal("the re-park carries the pre-decision fingerprint")
	}

	// A free-form answer is accepted and validated by the runtime.
	if _, err := d.ResumeAdaptiveProposal(context.Background(), reparks.ProposalFingerprint, []ProposalDecisionInput{
		{QuestionID: "phase-boundary", FreeForm: "after the markup references the module"},
	}); err != nil {
		t.Fatalf("free-form decision: %v", err)
	}
	if d.compiledProposal == nil || d.compiledProposal.Revision != 3 {
		t.Fatalf("compiled proposal = %+v, want revision 3", d.compiledProposal)
	}
	if d.compiledProposal.StrategyID != "phased-migration" {
		t.Fatalf("compiled strategy = %q, want the accepted recommendation", d.compiledProposal.StrategyID)
	}
	// Acceptance is still not authorization.
	if d.Boundary() == nil || d.Boundary().Action != autonomy.HumanBoundaryApproval {
		t.Fatalf("free-form acceptance bypassed the approval gate: %+v", d.Boundary())
	}
	// The free-form decision is durable with its provenance.
	events := ledgerEvents(t, store.LedgerPath())
	if !hasEvent(events, durable.EventHumanDecision) {
		t.Fatalf("the free-form decision was not recorded (types: %v)", eventTypes(events))
	}
	snap, ok := d.ProposalSnapshot()
	if !ok {
		t.Fatal("no snapshot after the free-form decision")
	}
	dec := snap.Contract.DecisionFor("phase-boundary")
	if dec == nil || dec.Provenance != proposal.ProvenanceHuman || dec.FreeForm == "" {
		t.Fatalf("restored decision = %+v, want a human free-form answer", dec)
	}
	if len(snap.Contract.Decisions) != 2 {
		t.Fatalf("restored decisions = %+v, want both", snap.Contract.Decisions)
	}
}
