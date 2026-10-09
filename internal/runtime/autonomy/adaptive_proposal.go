package autonomy

// ── ADAPTIVE PROPOSAL ORCHESTRATION (driver seam) ───────────────────────────
//
// This file wires the pure proposal contract (internal/runtime/proposal) into
// the ONE authoritative execution runtime. It introduces NO parallel executor,
// no second durable task owner and no second completion authority:
//
//	Run → evidence-bound scope → ADAPTIVE PROPOSAL (this file)
//	    → accepted decisions compile into the SAME d.req / resolved scope
//	    → the existing preflight, approval, execution and verification path
//
// The rich proposal lives on the Driver (exactly like DecisionSurface does);
// the HumanBoundary carries only a typed projection for the UI. The UI never
// owns proposal state and never dispatches a mutation: it renders the boundary
// and returns decisions through ResumeAdaptiveProposal.
//
// A proposal NEVER authorizes anything. Accepting a strategy is not approving a
// concrete mutation candidate; that second decision stays with the existing
// approval boundary and its admission gate.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/runtime/durable"
	"github.com/PizenLabs/izen/internal/runtime/proposal"
)

// ProposalAdvisor derives a proposal plan from facts the driver already owns.
// It is PURE: it reads no workspace, invokes no provider, and returns data.
type ProposalAdvisor func(facts ProposalFacts) ProposalPlan

// ProposalFacts is the bounded, evidence-only input to an advisor. Every field
// is a fact the runtime observed; none is a model opinion.
type ProposalFacts struct {
	Objective         string
	Mutating          bool
	ProjectLevel      bool
	ExplicitTargets   []string
	DerivedTargets    []string
	Ambiguous         bool
	Candidates        []string
	ScopeSize         int
	AutonomyPermitted bool
	// RequiresRendered reports that the applicable policy requires a rendered
	// verification of the composed output. It is a policy fact, never a model
	// opinion.
	RequiresRendered bool
}

// ProposalPlan is the data an advisor contributes to a proposal. The driver
// owns identity, revision and compilation; the advisor only describes the work.
type ProposalPlan struct {
	Observations  []proposal.Observation
	Assumptions   []proposal.Assumption
	Uncertainties []string
	Strategies    []proposal.Strategy
	Questions     []proposal.Question
	Admission     []proposal.AdmissionCondition
}

// WithProposalAdvisor wires adaptive proposal orchestration. It is OPT-IN: an
// unwired driver behaves exactly as before, so simple tasks and existing tests
// keep their efficient path. The production composition root wires it.
func WithProposalAdvisor(a ProposalAdvisor) Option {
	return func(d *Driver) {
		if d == nil {
			return
		}
		d.proposalAdvisor = a
	}
}

// WithRenderedVerificationPolicy binds the policy that decides whether an
// objective requires a rendered verification of the composed output. Passing
// nil leaves rendered verification disabled by default.
func WithRenderedVerificationPolicy(policy func(objective string) bool) Option {
	return func(d *Driver) {
		if d != nil {
			d.renderedPolicy = policy
		}
	}
}

// DefaultProposalAdvisor is the domain-neutral production advisor. It
// recommends applying the objective to the evidence-derived implementation
// surface, records the observed evidence, and asks NO question by default: a
// consequential decision only appears when a domain/policy advisor supplies
// one. When policy requires it, the recommended strategy carries a rendered
// verification method.
func DefaultProposalAdvisor(facts ProposalFacts) ProposalPlan {
	targets := append([]string(nil), facts.DerivedTargets...)
	if len(targets) == 0 {
		targets = append(targets, facts.ExplicitTargets...)
	}
	strategy := proposal.Strategy{
		ID:          "evidence-bound",
		Title:       "Evidence-bound change",
		Summary:     "apply the objective to the observed implementation artifacts",
		Rationale:   "the workspace's own implementation surface was observed; no filename was invented",
		Recommended: true,
		Provenance:  proposal.ProvenanceModel,
		Targets:     targets,
		Acceptance: []proposal.AcceptanceCriterion{
			{ID: "crit-objective", Statement: "the objective is reflected in the bound artifacts", Provenance: proposal.ProvenanceModel},
		},
		Verification: []proposal.VerificationMethod{
			{ID: "ver-source", Kind: proposal.VerifySource, Method: "re-read the bound artifacts", Automated: true},
		},
	}
	if facts.RequiresRendered {
		strategy.Verification = append(strategy.Verification, proposal.VerificationMethod{
			ID: "ver-rendered", Kind: proposal.VerifyRendered,
			Method: "render the composed output and evaluate the acceptance checks", Automated: true,
		})
	}
	return ProposalPlan{
		Strategies: []proposal.Strategy{strategy},
		Observations: []proposal.Observation{{
			ID:         "obs-scope",
			Summary:    "observed implementation artifacts",
			Paths:      targets,
			Provenance: proposal.ProvenanceRepo,
		}},
	}
}

// ErrProposalInvalid is returned when a submitted decision does not correspond
// to the parked proposal or its current revision.
var ErrProposalInvalid = errors.New("autonomy: invalid proposal decision")

// ProposalDecisionInput is the decision a human submits for a proposal
// question. It is an alias of the loop-contract transport type so the UI can
// name it without importing the runtime package.
type ProposalDecisionInput = autonomy.ProposalDecision

// maybeAdaptiveProposal selects an interaction and, when the objective demands
// it, forms and parks an adaptive proposal. It returns parked=true only when
// the loop has parked awaiting a human decision.
func (d *Driver) maybeAdaptiveProposal(ctx context.Context, runID uint64) (*autonomy.LoopTermination, bool) {
	if d == nil || d.proposalAdvisor == nil {
		return nil, false
	}
	if d.proposalState != nil {
		// A durable proposal was restored for this run (see
		// proposal_recovery.go). Continue THAT proposal: forming a new one would
		// mint a revision-1 snapshot, overwrite the durable lineage of the
		// decisions the human already made, and ask those questions again.
		return d.continueRestoredProposal(ctx, runID)
	}
	facts := d.proposalFacts()
	plan := d.proposalAdvisor(facts)

	contract := proposal.Contract{
		ID:            proposal.ID(proposalIdentity(d.runRequestID, d.prompt)),
		Objective:     d.prompt,
		Observations:  plan.Observations,
		Assumptions:   plan.Assumptions,
		Uncertainties: plan.Uncertainties,
		Strategies:    plan.Strategies,
		Questions:     plan.Questions,
		Admission:     plan.Admission,
	}
	if rec := contract.RecommendedStrategy(); rec != nil {
		contract.Recommended = rec.ID
	}
	state, err := proposal.New(contract)
	if err != nil {
		// A malformed proposal must never execute. Park with the reason rather
		// than silently falling back to an unreviewed path.
		d.parkProposalFailure(ctx, "proposal contract is invalid: "+err.Error())
		return d.term(), true
	}
	d.proposalState = state
	d.ledgerProposalSnapshot(state.Snapshot())

	selection := proposal.SelectInteraction(proposal.Signal{
		Objective:         d.prompt,
		Mutating:          facts.Mutating,
		HasExplicitTarget: len(facts.ExplicitTargets) > 0,
		DerivedTargets:    facts.DerivedTargets,
		Ambiguity:         facts.Ambiguous,
		Unresolved:        len(facts.DerivedTargets) == 0 && !facts.ProjectLevel,
		ProjectLevel:      facts.ProjectLevel,
		Consequential:     proposal.ConsequentialQuestions(plan.Questions),
		AutonomyPermitted: facts.AutonomyPermitted,
	})
	d.publishProposalFormed(selection)

	if selection.Interaction == proposal.InteractionDirect {
		return nil, false
	}

	// Resolve questions the runtime is entitled to answer from policy, then
	// determine what genuinely requires the human.
	d.applyPolicyDefaults(state)
	c := state.Contract()
	pending := proposal.Unanswered(&c)
	if len(pending) > 0 {
		d.parkAdaptiveProposal(ctx, state, selection, pending)
		return d.term(), true
	}

	// No question requires the human: compile the recommended strategy and
	// continue on the existing execution path. This is "investigate and
	// propose" without an unnecessary interaction — the recommendation is
	// recorded, not a mandatory ceremony.
	compiled, err := state.Compile()
	if err != nil {
		d.parkProposalFailure(ctx, "proposal did not compile: "+err.Error())
		return d.term(), true
	}
	d.applyCompiledProposal(compiled, "adaptive proposal: recommended strategy adopted without human questions")
	d.publishProposalAccepted(state, compiled)
	if d.runID != runID {
		return d.term(), true
	}
	return nil, false
}

// proposalFacts assembles the evidence-only advisor input from the driver's own
// observed state.
func (d *Driver) proposalFacts() ProposalFacts {
	candidates := append([]string(nil), d.scopeDerivation.Candidates...)
	if len(candidates) == 0 {
		candidates = append(candidates, d.resolved.Options...)
	}
	facts := ProposalFacts{
		Objective:         d.prompt,
		Mutating:          d.admissionIntent().IsMutation(),
		ProjectLevel:      d.isProjectLevelObjective(),
		DerivedTargets:    append([]string(nil), d.resolved.Targets...),
		Candidates:        candidates,
		ScopeSize:         len(d.resolved.Targets),
		AutonomyPermitted: !IsHot(d.scope),
	}
	if len(d.statedTargets) > 0 {
		facts.ExplicitTargets = append([]string(nil), d.statedTargets...)
	} else if len(d.resolved.Targets) > 0 {
		// A resolved target is a bound statement for interaction purposes.
		facts.ExplicitTargets = append([]string(nil), d.resolved.Targets...)
	}
	facts.Ambiguous = d.scopeDerivation.IsAmbiguous() || d.resolved.Ambiguous
	if d.renderedPolicy != nil {
		facts.RequiresRendered = d.renderedPolicy(d.prompt)
	}
	return facts
}

// applyPolicyDefaults answers questions the runtime may resolve from policy
// without asking. Only a question that declares a Default is eligible; the
// resulting decision carries ProvenancePolicy, never ProvenanceHuman.
func (d *Driver) applyPolicyDefaults(state *proposal.State) {
	if state == nil {
		return
	}
	for _, q := range state.Contract().Questions {
		if q.Default == "" {
			continue
		}
		if _, err := state.ApplyPolicy(q.ID); err != nil {
			diagnosticf("[proposal] policy default for %s refused: %v", q.ID, err)
		}
	}
}

// parkAdaptiveProposal parks the loop at the adaptive proposal boundary with a
// typed projection of the proposal.
func (d *Driver) parkAdaptiveProposal(ctx context.Context, state *proposal.State, selection proposal.Selection, pending []proposal.Question) {
	c := state.Contract()
	b := d.adaptiveBoundary(state)
	b.Reason = "adaptive proposal: " + selection.Explain() + " — awaiting " +
		strconv.Itoa(len(pending)) + " decision(s)"
	d.loop.AwaitHuman(b)
	d.enrichBoundary()
	d.publish(ctx)
	d.emitAuthorization("proposal", b.Reason, "ADAPTIVE_PROPOSAL", false, false)
	if d.bus != nil {
		d.bus.Publish(events.NewActivity("[proposal] parked for decisions: " +
			strings.Join(questionIDs(pending), ", ") + " (revision " + strconv.Itoa(c.Revision) + ")"))
	}
}

// adaptiveBoundary projects the proposal onto the transport boundary the UI
// renders. It is pure data.
func (d *Driver) adaptiveBoundary(state *proposal.State) autonomy.HumanBoundary {
	c := state.Contract()
	b := autonomy.HumanBoundary{
		ProposalID:          string(c.ID),
		ProposalRevision:    c.Revision,
		ProposalFingerprint: c.Fingerprint(),
		ProposalObjective:   c.Objective,
		ProposalQuestions:   projectQuestions(c.Questions),
		ProposalStrategies:  projectStrategies(c.Strategies, c.Recommended),
	}
	if rec := c.RecommendedStrategy(); rec != nil {
		b.ProposalSummary = rec.Title + " — " + rec.Summary
	}
	b.Targets = append([]string(nil), d.resolved.Targets...)
	b.Action = autonomy.HumanBoundaryAdaptiveProposal
	b.Resumable = true
	return b
}

// projectQuestions maps the proposal questions onto their transport shape.
func projectQuestions(qs []proposal.Question) []autonomy.HumanProposalQuestion {
	out := make([]autonomy.HumanProposalQuestion, 0, len(qs))
	for _, q := range qs {
		opts := make([]autonomy.HumanProposalOption, 0, len(q.Options))
		for _, o := range q.Options {
			opts = append(opts, autonomy.HumanProposalOption{
				ID:          o.ID,
				Label:       o.Label,
				Description: o.Description,
			})
		}
		out = append(out, autonomy.HumanProposalQuestion{
			ID:             q.ID,
			Prompt:         q.Prompt,
			Kind:           autonomy.HumanProposalQuestionKind(q.Kind),
			Options:        opts,
			AllowFreeForm:  q.AllowFreeForm,
			Required:       q.Required,
			Recommended:    q.Recommended,
			RecommendedSet: append([]string(nil), q.RecommendedSet...),
			DependsOn:      append([]string(nil), q.DependsOn...),
			Affects:        append([]string(nil), q.Affects...),
		})
	}
	return out
}

// projectStrategies maps the proposal strategies onto their transport shape.
func projectStrategies(ss []proposal.Strategy, recommended string) []autonomy.HumanProposalStrategy {
	out := make([]autonomy.HumanProposalStrategy, 0, len(ss))
	for _, s := range ss {
		view := autonomy.HumanProposalStrategy{
			ID:          s.ID,
			Title:       s.Title,
			Summary:     s.Summary,
			Rationale:   s.Rationale,
			Recommended: s.ID == recommended || s.Recommended,
		}
		for _, t := range s.Tradeoffs {
			view.Tradeoffs = append(view.Tradeoffs, autonomy.HumanProposalTradeoff{
				Description: t.Description, Benefit: t.Benefit, Cost: t.Cost,
			})
		}
		for _, r := range s.Risks {
			view.Risks = append(view.Risks, autonomy.HumanProposalRisk{
				Description: r.Description, Mitigation: r.Mitigation, Severity: string(r.Severity),
			})
		}
		for _, p := range s.Phases {
			view.Phases = append(view.Phases, autonomy.HumanProposalPhase{
				ID: p.ID, Title: p.Title, Order: p.Order, Boundary: p.Boundary, Checkpoint: p.Checkpoint,
			})
		}
		out = append(out, view)
	}
	return out
}

// ResumeAdaptiveProposal records the human decisions against the parked
// proposal and, when the proposal is complete, compiles them into the
// authoritative execution contract and continues on the EXISTING path.
//
// A submission that does not match the parked proposal revision is refused;
// authorization is never reconstructed from mismatched state.
func (d *Driver) ResumeAdaptiveProposal(ctx context.Context, fingerprint string, decisions []ProposalDecisionInput) (*autonomy.LoopTermination, error) {
	defer d.emitRunSummary()
	if d.proposalState == nil {
		return d.term(), fmt.Errorf("%w: no parked proposal", ErrProposalInvalid)
	}
	if d.loop == nil || d.loop.State() != autonomy.RuntimeAwaitingHuman {
		return d.term(), errors.New("autonomy: resume-adaptive-proposal requires a parked human boundary")
	}
	if b := d.loop.Boundary(); b != nil && b.ProposalFingerprint != "" &&
		fingerprint != "" && b.ProposalFingerprint != fingerprint {
		return d.term(), fmt.Errorf("%w: fingerprint %q does not match the parked proposal %q",
			ErrProposalInvalid, fingerprint, b.ProposalFingerprint)
	}

	anyChange := false
	scopeChanged := false
	for _, in := range decisions {
		if strings.TrimSpace(in.QuestionID) == "" {
			return d.term(), fmt.Errorf("%w: empty question id", ErrProposalInvalid)
		}
		change, err := d.proposalState.Apply(proposal.Decision{
			QuestionID: in.QuestionID,
			OptionID:   in.OptionID,
			OptionIDs:  append([]string(nil), in.OptionIDs...),
			FreeForm:   in.FreeForm,
			Provenance: proposal.ProvenanceHuman,
		})
		if err != nil {
			return d.term(), fmt.Errorf("%w: %w", ErrProposalInvalid, err)
		}
		if !change.NoOp {
			anyChange = true
			scopeChanged = scopeChanged || change.ScopeChanged
		}
		answer := in.OptionID
		if answer == "" && len(in.OptionIDs) > 0 {
			answer = strings.Join(in.OptionIDs, ",")
		}
		if answer == "" {
			answer = in.FreeForm
		}
		d.ledgerHumanDecision("proposal:"+in.QuestionID, answer,
			string(proposal.ID(d.proposalState.Contract().ID)), d.proposalState.Fingerprint())
	}
	d.ledgerProposalSnapshot(d.proposalState.Snapshot())

	// Revision invalidation: a changed decision invalidates the derived state
	// that depended on it. Targets are re-bound through the canonical gateway
	// when the scope changed; held candidates from an earlier revision are
	// dropped so a stale approval cannot be replayed.
	if anyChange {
		d.invalidateProposalDerivedState(scopeChanged)
	}

	if err := d.proposalState.Complete(); err != nil {
		// Not finished: re-park with the remaining questions and an explicit
		// reason. No execution happens on an incomplete decision set.
		b := d.adaptiveBoundary(d.proposalState)
		b.Reason = "adaptive proposal incomplete: " + err.Error()
		d.loop.AwaitHuman(b)
		d.enrichBoundary()
		d.publish(ctx)
		return d.term(), nil
	}

	compiled, err := d.proposalState.Compile()
	if err != nil {
		return d.term(), fmt.Errorf("%w: %w", ErrProposalInvalid, err)
	}
	d.applyCompiledProposal(compiled, "adaptive proposal accepted at revision "+strconv.Itoa(compiled.Revision))
	d.publishProposalAccepted(d.proposalState, compiled)

	d.markHumanGated()
	d.loop.ReleaseHuman("adaptive proposal accepted: revision " + strconv.Itoa(compiled.Revision))
	d.publish(ctx)
	d.runID++
	term, err := d.observeAndRun(ctx, d.runID)
	if term != nil && term.State.IsTerminal() {
		d.runCtx = nil
		d.runCancel = nil
	}
	return term, err
}

// invalidateProposalDerivedState drops the derived state a proposal revision
// change invalidated. It is deliberately targeted: the authoritative objective
// is untouched, and only the derived scope/candidates/context are invalidated.
func (d *Driver) invalidateProposalDerivedState(scopeChanged bool) {
	d.dropStaleDerivedProposalState("adaptive proposal revision changed")
	if scopeChanged {
		d.invalidateDerivedScope("adaptive proposal decision changed the target set")
	}
}

// applyCompiledProposal threads the accepted decisions into the existing
// execution request. It is the ONLY place a compiled proposal touches runtime
// state, and it never bypasses the standard authorization path.
func (d *Driver) applyCompiledProposal(compiled proposal.ExecutionContract, reason string) {
	if d == nil {
		return
	}
	d.compiledProposal = &compiled
	// Target set: re-bound through the canonical gateway, exactly like a human
	// clarification, so a compiled target the workspace does not contain fails
	// closed on the same path as any other unbound destination.
	if compiled.HasTargets() {
		targets := d.bindAuthoritativeTargets(append([]string(nil), compiled.Targets...), reason)
		d.req.Targets = targets
		d.req.Target = firstTarget(targets)
		d.req.WorkspaceDigest = d.adapter.WorkspaceVersion(targets)
		d.capturePreExecutionTargets()
	}
	// Accepted decisions become bounded execution evidence — never authority.
	if len(compiled.Constraints) > 0 {
		block := "ACCEPTED HUMAN DECISIONS (revision " + strconv.Itoa(compiled.Revision) + "):\n" +
			strings.Join(compiled.Constraints, "\n")
		d.req.Evidence = joinEvidence(d.req.Evidence, block)
	}
	// Rendered verification requirement flows to the completion path.
	d.renderedCriteria = append([]proposal.AcceptanceCriterion(nil), compiled.Acceptance...)
	d.renderedRequired = compiled.RequiresRenderedVerification()
}

// publishProposalFormed records the interaction selection as telemetry.
func (d *Driver) publishProposalFormed(selection proposal.Selection) {
	if d == nil || d.bus == nil {
		return
	}
	d.bus.Publish(events.NewActivity("[proposal] interaction=" + selection.Explain()))
}

// publishProposalAccepted records the accepted proposal and its compiled
// contract as telemetry and durable lineage.
func (d *Driver) publishProposalAccepted(state *proposal.State, compiled proposal.ExecutionContract) {
	if d == nil {
		return
	}
	if d.bus != nil {
		d.bus.Publish(events.NewActivity(fmt.Sprintf(
			"[proposal] accepted revision=%d strategy=%s targets=[%s] fingerprint=%s",
			compiled.Revision, compiled.StrategyID, strings.Join(compiled.Targets, ","), compiled.Fingerprint)))
	}
	d.ledgerProposalSnapshot(state.Snapshot())
}

// parkProposalFailure parks the loop with an explicit proposal failure rather
// than executing an invalid proposal.
func (d *Driver) parkProposalFailure(ctx context.Context, reason string) {
	if d.loop == nil {
		return
	}
	b := autonomy.HumanBoundary{Reason: reason}
	autonomy.DeriveBoundaryAction(&b)
	d.loop.AwaitHuman(b)
	d.enrichBoundary()
	d.publish(ctx)
	d.emitAuthorization("deny", reason, "PROPOSAL_INVALID", false, true)
}

// ProposalSnapshot returns the durable image of the current proposal.
func (d *Driver) ProposalSnapshot() (proposal.Snapshot, bool) {
	if d == nil || d.proposalState == nil {
		return proposal.Snapshot{}, false
	}
	return d.proposalState.Snapshot(), true
}

// RestoreProposal restores a proposal from a durable snapshot. It REFUSES a
// snapshot whose recorded decisions do not validate against its questions, so
// authorization is never silently reconstructed from a corrupted record.
func (d *Driver) RestoreProposal(snap proposal.Snapshot) error {
	state, err := proposal.Restore(snap)
	if err != nil {
		return err
	}
	d.proposalState = state
	return nil
}

// ledgerProposalSnapshot appends the proposal snapshot to the durable ledger.
// It is best-effort, exactly like every other ledger write: the loop never
// depends on the journal accepting a line.
func (d *Driver) ledgerProposalSnapshot(snap proposal.Snapshot) {
	if !d.ledgerReady() {
		return
	}
	raw, err := json.Marshal(snap.Contract)
	if err != nil {
		ledgerFail("PROPOSAL_SNAPSHOT marshal: %v", err)
		return
	}
	payload := map[string]any{
		"proposal_id": string(snap.Contract.ID),
		"revision":    snap.Contract.Revision,
		"fingerprint": snap.Fingerprint,
		"snapshot":    string(raw),
	}
	if err := d.ledger.RecordCustomEvent(d.ledgerTask, durable.EventProposalSnapshot, payload); err != nil {
		ledgerFail("PROPOSAL_SNAPSHOT refused for %s: %v", d.ledgerTask, err)
	}
}

// ── small helpers ───────────────────────────────────────────────────────────

func proposalIdentity(runRequestID, objective string) string {
	// Deterministic identity from the run identity and the objective text, so a
	// resumed run of the same objective attaches to the same proposal record.
	return runRequestID + ":" + proposalObjectiveHash(objective)
}

// proposalObjectiveHash is the objective-derived half of a proposal identity.
// Unlike the run-request prefix it does not depend on how many runs this
// particular process happened to start, so it is the part a restarted process
// can still recognise a proposal by.
func proposalObjectiveHash(objective string) string {
	sum := sha256.Sum256([]byte(objective))
	return hex.EncodeToString(sum[:6])
}

func questionIDs(qs []proposal.Question) []string {
	out := make([]string, 0, len(qs))
	for _, q := range qs {
		out = append(out, q.ID)
	}
	return out
}
