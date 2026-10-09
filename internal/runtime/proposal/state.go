package proposal

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Change describes the consequence of recording one decision. It is the
// revision-safe record of WHAT changed and WHAT it invalidated, so a caller can
// drop exactly the derived state that depends on the changed decision instead of
// invalidating everything.
type Change struct {
	// QuestionID is the question the decision answered.
	QuestionID string `json:"question_id"`
	// Revision is the proposal revision AFTER the decision.
	Revision int `json:"revision"`
	// Previous is the decision that was replaced, or nil for a first answer.
	Previous *Decision `json:"previous,omitempty"`
	// Invalidated names every element the change invalidated, in deterministic
	// order. It is derived from the question's Affects set plus the contract's
	// dependency graph, not from a blanket rule.
	Invalidated []string `json:"invalidated,omitempty"`
	// ScopeChanged reports that the accepted decisions changed the target set,
	// so the caller must re-bind targets through the canonical authority.
	ScopeChanged bool `json:"scope_changed"`
	// NoOp reports that the decision restated the existing answer; nothing was
	// invalidated and the revision did not advance.
	NoOp bool `json:"no_op"`
}

// CompletionError is returned when the proposal is not ready to execute.
type CompletionError struct {
	Unanswered  []string
	Unsatisfied []string
}

// Error implements error.
func (e CompletionError) Error() string {
	var parts []string
	if len(e.Unanswered) > 0 {
		parts = append(parts, "unanswered required question(s): "+strings.Join(e.Unanswered, ", "))
	}
	if len(e.Unsatisfied) > 0 {
		parts = append(parts, "unsatisfied admission condition(s): "+strings.Join(e.Unsatisfied, ", "))
	}
	return "proposal: not executable — " + strings.Join(parts, "; ")
}

// State is the revision-safe MUTABLE holder of one proposal. It owns the
// proposal's revision counter and its decision set. It is the ONLY place a
// proposal is advanced; the Driver holds the State and never edits a Contract by
// hand.
type State struct {
	mu       sync.Mutex
	contract Contract
	// invalid holds the element IDs invalidated by the latest revision change.
	invalid map[string]bool
}

// New builds a State from a validated contract.
func New(c Contract) (*State, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.Revision <= 0 {
		c.Revision = 1
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	return &State{contract: c, invalid: map[string]bool{}}, nil
}

// Revision returns the current proposal revision.
func (s *State) Revision() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.contract.Revision
}

// Contract returns a deep-enough copy of the current contract so a caller
// cannot mutate the State's internal value.
func (s *State) Contract() Contract {
	if s == nil {
		return Contract{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneContract(s.contract)
}

// Fingerprint returns the content identity at the current revision.
func (s *State) Fingerprint() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.contract.Fingerprint()
}

// Invalidated returns whether the latest revision change invalidated an element.
func (s *State) Invalidated(element string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.invalid[element]
}

// InvalidatedElements returns the invalidated set in deterministic order.
func (s *State) InvalidatedElements() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return sortedKeys(s.invalid)
}

// ApplyPolicy records a decision the runtime is entitled to make from reliable
// evidence or existing policy, WITHOUT claiming it was a human decision. The
// provenance is ProvenancePolicy and it is only accepted for a question that
// declares a Default. A policy answer never masquerades as a human decision.
func (s *State) ApplyPolicy(questionID string) (Change, error) {
	if s == nil {
		return Change{}, ValidationError{Reason: "nil proposal state"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.contract.Question(questionID)
	if q == nil {
		return Change{}, ValidationError{Field: "policy", Reason: "unknown question " + questionID}
	}
	if strings.TrimSpace(q.Default) == "" {
		return Change{}, ValidationError{Field: "policy", Reason: "question " + questionID + " declares no default; it requires a human decision"}
	}
	d := Decision{
		QuestionID: questionID,
		OptionID:   q.Default,
		Provenance: ProvenancePolicy,
		At:         time.Now().UTC(),
	}
	return s.applyLocked(d)
}

// Apply records one human decision against the current revision, invalidates
// the dependent derived state, and returns the change.
//
// A MODEL recommendation is rejected: only ProvenanceHuman may satisfy a
// question. A decision with no provenance is rejected for the same reason.
func (s *State) Apply(d Decision) (Change, error) {
	if s == nil {
		return Change{}, ValidationError{Reason: "nil proposal state"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if d.Provenance != ProvenanceHuman {
		return Change{}, ValidationError{Field: "decision " + d.QuestionID,
			Reason: "a proposal decision must be ProvenanceHuman; a model recommendation is not a human decision"}
	}
	return s.applyLocked(d)
}

// applyLocked is the shared revision transaction. The caller must hold the lock
// and must have already validated the decision's provenance.
func (s *State) applyLocked(d Decision) (Change, error) {
	q := s.contract.Question(d.QuestionID)
	if q == nil {
		return Change{}, ValidationError{Field: "decision", Reason: "unknown question " + d.QuestionID}
	}
	if d.Provenance != ProvenanceHuman && d.Provenance != ProvenancePolicy {
		return Change{}, ValidationError{Field: "decision " + d.QuestionID,
			Reason: "a proposal decision must be a human decision or a question default"}
	}
	if err := validateAnswer(*q, d); err != nil {
		return Change{}, err
	}

	previous := s.contract.DecisionFor(d.QuestionID)
	if previous != nil {
		prevCopy := *previous
		previous = &prevCopy
	}
	if previous != nil && sameAnswer(*previous, d) {
		return Change{QuestionID: d.QuestionID, Revision: s.contract.Revision, Previous: previous, NoOp: true}, nil
	}

	// Replace (or append) the decision.
	replaced := false
	for i := range s.contract.Decisions {
		if s.contract.Decisions[i].QuestionID == d.QuestionID {
			s.contract.Decisions[i] = d
			replaced = true
			break
		}
	}
	if !replaced {
		s.contract.Decisions = append(s.contract.Decisions, d)
	}
	s.contract.Revision++

	// Compute the invalidated set: the question's own Affects plus everything
	// the dependency graph marks invalidated by those elements, transitively.
	s.invalid = invalidatedBy(&s.contract, q.Affects)

	// A decision that changes the selected strategy or its targets changes the
	// scope, so the caller must re-bind targets.
	scopeChanged := false
	for _, el := range q.Affects {
		if el == "targets" || strings.HasPrefix(el, "target:") || strings.HasPrefix(el, "strategy:") {
			scopeChanged = true
		}
	}

	change := Change{
		QuestionID:   d.QuestionID,
		Revision:     s.contract.Revision,
		Previous:     previous,
		Invalidated:  sortedKeys(s.invalid),
		ScopeChanged: scopeChanged,
	}
	s.contract.Decisions = sortedDecisions(s.contract.Decisions)
	return change, nil
}

// validateAnswer checks that a decision answers the question it claims to.
func validateAnswer(q Question, d Decision) error {
	opts := map[string]bool{}
	for _, o := range q.Options {
		opts[o.ID] = true
	}
	switch q.Kind {
	case QuestionSingle:
		if d.OptionID == "" && strings.TrimSpace(d.FreeForm) == "" {
			return ValidationError{Field: "decision " + q.ID, Reason: "a single-choice answer needs an option or free-form text"}
		}
		if d.OptionID != "" && len(q.Options) > 0 && !opts[d.OptionID] {
			return ValidationError{Field: "decision " + q.ID, Reason: "unknown option " + d.OptionID}
		}
		if d.OptionID == "" && !q.AllowFreeForm {
			return ValidationError{Field: "decision " + q.ID, Reason: "free-form is not permitted for this question"}
		}
	case QuestionMulti:
		for _, id := range d.OptionIDs {
			if len(q.Options) > 0 && !opts[id] {
				return ValidationError{Field: "decision " + q.ID, Reason: "unknown option " + id}
			}
		}
		if len(d.OptionIDs) == 0 && strings.TrimSpace(d.FreeForm) == "" {
			return ValidationError{Field: "decision " + q.ID, Reason: "a multi-select answer needs at least one option or free-form text"}
		}
	case QuestionFreeForm:
		if strings.TrimSpace(d.FreeForm) == "" && d.OptionID == "" && len(d.OptionIDs) == 0 {
			return ValidationError{Field: "decision " + q.ID, Reason: "a free-form answer needs text"}
		}
	}
	return nil
}

func sameAnswer(a, b Decision) bool {
	if a.OptionID != b.OptionID || a.FreeForm != b.FreeForm {
		return false
	}
	if len(a.OptionIDs) != len(b.OptionIDs) {
		return false
	}
	ac := append([]string(nil), a.OptionIDs...)
	bc := append([]string(nil), b.OptionIDs...)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}

// invalidatedBy computes the transitive invalidation set for a set of changed
// elements, using the contract's dependency graph (`invalidates` edges).
func invalidatedBy(c *Contract, changed []string) map[string]bool {
	invalid := map[string]bool{}
	for _, el := range changed {
		if el != "" {
			invalid[el] = true
		}
	}
	// Adjacency from `invalidates` edges plus the strategy/targets coupling:
	// changing a strategy invalidates its targets and acceptance criteria.
	edges := map[string][]string{}
	for _, s := range c.Strategies {
		for _, dep := range s.Dependencies {
			if strings.EqualFold(dep.Kind, "invalidates") {
				edges[dep.From] = append(edges[dep.From], dep.To)
			}
		}
		if len(s.Targets) > 0 {
			for _, t := range s.Targets {
				edges["strategy:"+s.ID] = append(edges["strategy:"+s.ID], "target:"+t)
			}
		}
		for _, a := range s.Acceptance {
			edges["strategy:"+s.ID] = append(edges["strategy:"+s.ID], "criterion:"+a.ID)
		}
	}
	// Fixpoint over the dependency graph.
	for changedRound := true; changedRound; {
		changedRound = false
		for el := range invalid {
			for _, next := range edges[el] {
				if !invalid[next] {
					invalid[next] = true
					changedRound = true
				}
			}
		}
	}
	return invalid
}

// Complete reports whether every required question is answered and every
// admission condition holds. It is the gate the driver consults before it
// compiles and admits.
func (s *State) Complete() error {
	if s == nil {
		return CompletionError{Unsatisfied: []string{"(no proposal state)"}}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var unanswered, unsatisfied []string
	for _, q := range s.contract.Questions {
		if !q.Required {
			continue
		}
		d := s.contract.DecisionFor(q.ID)
		if !questionAnswered(q, d) {
			unanswered = append(unanswered, q.ID)
		}
	}
	for _, a := range s.contract.Admission {
		if !a.Satisfied {
			unsatisfied = append(unsatisfied, a.ID)
		}
	}
	sort.Strings(unanswered)
	sort.Strings(unsatisfied)
	if len(unanswered) > 0 || len(unsatisfied) > 0 {
		return CompletionError{Unanswered: unanswered, Unsatisfied: unsatisfied}
	}
	return nil
}

// Compile produces the authoritative execution contract the driver consumes.
// It refuses to compile an incomplete proposal, so a runtime can never execute
// a decision set the human has not finished making.
//
// Compile does NOT authorize anything. The compiled value is a description of
// the work; the driver still runs the existing authorization, approval and
// verification machinery on it.
func (s *State) Compile() (ExecutionContract, error) {
	if s == nil {
		return ExecutionContract{}, ValidationError{Reason: "nil proposal state"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.completeLocked(); err != nil {
		return ExecutionContract{}, err
	}
	contract := &s.contract
	strategy := selectedStrategy(contract)

	out := ExecutionContract{
		ProposalID:  contract.ID,
		Revision:    contract.Revision,
		Fingerprint: contract.Fingerprint(),
		Objective:   contract.Objective,
	}
	if strategy != nil {
		out.StrategyID = strategy.ID
		out.Targets = append([]string(nil), strategy.Targets...)
		out.Acceptance = append([]AcceptanceCriterion(nil), strategy.Acceptance...)
		out.Verification = append([]VerificationMethod(nil), strategy.Verification...)
		for _, phase := range strategy.Phases {
			for _, t := range phase.Targets {
				out.Targets = appendUnique(out.Targets, t)
			}
		}
	}
	out.Decisions = append([]Decision(nil), contract.Decisions...)
	out.Constraints = compileConstraints(contract)
	sort.Strings(out.Targets)
	return out, nil
}

// questionAnswered reports whether a question is satisfied: a human decision
// always satisfies it; a policy decision satisfies it ONLY when the question
// declares the default that was applied. A model recommendation satisfies
// nothing.
func questionAnswered(q Question, d *Decision) bool {
	if d == nil {
		return false
	}
	switch d.Provenance {
	case ProvenanceHuman:
		return true
	case ProvenancePolicy:
		return strings.TrimSpace(q.Default) != "" && d.OptionID == q.Default
	default:
		return false
	}
}

// completeLocked is Complete without re-locking.
func (s *State) completeLocked() error {
	var unanswered, unsatisfied []string
	for _, q := range s.contract.Questions {
		if !q.Required {
			continue
		}
		d := s.contract.DecisionFor(q.ID)
		if !questionAnswered(q, d) {
			unanswered = append(unanswered, q.ID)
		}
	}
	for _, a := range s.contract.Admission {
		if !a.Satisfied {
			unsatisfied = append(unsatisfied, a.ID)
		}
	}
	sort.Strings(unanswered)
	sort.Strings(unsatisfied)
	if len(unanswered) > 0 || len(unsatisfied) > 0 {
		return CompletionError{Unanswered: unanswered, Unsatisfied: unsatisfied}
	}
	return nil
}

// selectedStrategy resolves the strategy the accepted decisions choose: a
// decision whose answer names a strategy wins, then the recommendation, then
// the first.
func selectedStrategy(c *Contract) *Strategy {
	if c == nil {
		return nil
	}
	for _, q := range c.Questions {
		d := c.DecisionFor(q.ID)
		if d == nil {
			continue
		}
		if s := c.Strategy(d.OptionID); s != nil {
			return s
		}
		for _, id := range d.OptionIDs {
			if s := c.Strategy(id); s != nil {
				return s
			}
		}
	}
	if s := c.RecommendedStrategy(); s != nil {
		return s
	}
	if len(c.Strategies) > 0 {
		return &c.Strategies[0]
	}
	return nil
}

// compileConstraints renders the accepted human decisions as bounded execution
// constraints. They are FACTS ABOUT THE HUMAN'S CHOICES — never authorization
// and never model output.
func compileConstraints(c *Contract) []string {
	out := make([]string, 0, len(c.Decisions))
	for _, d := range sortedDecisions(c.Decisions) {
		q := c.Question(d.QuestionID)
		label := d.QuestionID
		if q != nil {
			label = q.Prompt
		}
		answer := d.OptionID
		if answer == "" && len(d.OptionIDs) > 0 {
			answer = strings.Join(d.OptionIDs, ",")
		}
		if answer == "" {
			answer = d.FreeForm
		}
		out = append(out, fmt.Sprintf("human decision [%s] %s = %s", d.QuestionID, label, answer))
	}
	return out
}

// Snapshot is the durable, resume-safe image of a proposal.
type Snapshot struct {
	Contract    Contract `json:"contract"`
	Fingerprint string   `json:"fingerprint"`
}

// Snapshot returns the durable image of the current proposal.
func (s *State) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{Contract: cloneContract(s.contract), Fingerprint: s.contract.Fingerprint()}
}

// Restore rebuilds a State from a durable snapshot and verifies that the
// snapshot's content is intact. A snapshot whose fingerprint does not match its
// contents is REFUSED: silently reconstructing authorization from a corrupted
// conversational record is exactly what must not happen after a resume.
func Restore(snap Snapshot) (*State, error) {
	c := snap.Contract
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if snap.Fingerprint != "" && snap.Fingerprint != c.Fingerprint() {
		return nil, ValidationError{Field: "snapshot", Reason: "fingerprint does not match the recorded proposal contents"}
	}
	// Validate every recorded decision against its question.
	for _, d := range c.Decisions {
		q := c.Question(d.QuestionID)
		if q == nil {
			return nil, ValidationError{Field: "snapshot", Reason: "decision names unknown question " + d.QuestionID}
		}
		if d.Provenance != ProvenanceHuman {
			return nil, ValidationError{Field: "snapshot", Reason: "recorded decision for " + d.QuestionID + " is not a human decision"}
		}
		if err := validateAnswer(*q, d); err != nil {
			return nil, err
		}
	}
	if c.Revision <= 0 {
		c.Revision = 1
	}
	return &State{contract: c, invalid: map[string]bool{}}, nil
}

// ExecutionContract is the compiled, authoritative description of the accepted
// work. It is the bridge between the proposal and the runtime's existing
// execution request; it is NOT an authorization.
type ExecutionContract struct {
	ProposalID   ID                    `json:"proposal_id"`
	Revision     int                   `json:"revision"`
	Fingerprint  string                `json:"fingerprint"`
	Objective    string                `json:"objective"`
	StrategyID   string                `json:"strategy_id,omitempty"`
	Targets      []string              `json:"targets,omitempty"`
	Constraints  []string              `json:"constraints,omitempty"`
	Acceptance   []AcceptanceCriterion `json:"acceptance,omitempty"`
	Verification []VerificationMethod  `json:"verification,omitempty"`
	Decisions    []Decision            `json:"decisions,omitempty"`
}

// RequiresRenderedVerification reports whether the accepted strategy demands a
// rendered check.
func (e ExecutionContract) RequiresRenderedVerification() bool {
	for _, v := range e.Verification {
		if v.Kind == VerifyRendered {
			return true
		}
	}
	return false
}

// HasTargets reports whether the compiled contract binds a target set.
func (e ExecutionContract) HasTargets() bool { return len(e.Targets) > 0 }

func cloneContract(c Contract) Contract {
	out := c
	out.Observations = append([]Observation(nil), c.Observations...)
	out.Assumptions = append([]Assumption(nil), c.Assumptions...)
	out.Uncertainties = append([]string(nil), c.Uncertainties...)
	out.Strategies = make([]Strategy, len(c.Strategies))
	for i, s := range c.Strategies {
		s.Tradeoffs = append([]Tradeoff(nil), s.Tradeoffs...)
		s.Risks = append([]Risk(nil), s.Risks...)
		s.Dependencies = append([]Dependency(nil), s.Dependencies...)
		s.Phases = append([]Phase(nil), s.Phases...)
		s.Acceptance = append([]AcceptanceCriterion(nil), s.Acceptance...)
		s.Verification = append([]VerificationMethod(nil), s.Verification...)
		s.Targets = append([]string(nil), s.Targets...)
		s.Steps = append([]string(nil), s.Steps...)
		out.Strategies[i] = s
	}
	out.Questions = make([]Question, len(c.Questions))
	for i, q := range c.Questions {
		q.Options = append([]Option(nil), q.Options...)
		q.RecommendedSet = append([]string(nil), q.RecommendedSet...)
		q.DependsOn = append([]string(nil), q.DependsOn...)
		q.Affects = append([]string(nil), q.Affects...)
		out.Questions[i] = q
	}
	out.Decisions = append([]Decision(nil), c.Decisions...)
	out.Admission = append([]AdmissionCondition(nil), c.Admission...)
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		if v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func appendUnique(list []string, v string) []string {
	for _, existing := range list {
		if existing == v {
			return list
		}
	}
	return append(list, v)
}
