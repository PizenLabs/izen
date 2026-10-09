// Package proposal defines IZEN's first-class ADAPTIVE PROPOSAL contract.
//
// ── WHO OWNS WHAT ───────────────────────────────────────────────────────────
//
// This package is PURE DATA AND PURE FUNCTIONS. It owns no workspace, invokes
// no provider, mutates nothing, and cannot authorize anything. It is the typed
// language a proposal is written in and the deterministic rules for:
//
//  1. which interaction an objective should use       (SelectInteraction)
//  2. how a human decision revises the proposal       (State.Apply)
//  3. what the accepted decisions compile into        (State.Compile)
//
// The runtime authority stays where it already is. `State.Compile` produces a
// value the DRIVER consumes; it never reaches the executor, the kernel or the
// filesystem itself. A proposal never grants a capability and never authorizes
// a mutation — accepting a strategy and approving a concrete mutation candidate
// remain two different decisions, owned by two different components.
//
// ── PROVENANCE IS NOT DECORATION ────────────────────────────────────────────
//
// Every fact carries Provenance. The distinction is load-bearing:
//
//	ProvenanceModel    a model recommendation — NEVER a human decision
//	ProvenanceRepo     a fact read from the repository/workspace
//	ProvenanceRuntime  runtime-derived state (scope resolution, digests)
//	ProvenanceHuman    a decision the human actually made
//	ProvenancePolicy   the applicable authorization/verification policy
//
// A model recommendation can be the RECOMMENDATION; only a ProvenanceHuman
// decision can ever satisfy a required question or compile into an execution
// contract.
package proposal

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ID is a stable proposal identity.
type ID string

// Provenance names who authored a fact. It is the boundary that keeps a model
// recommendation from being mistaken for a human decision.
type Provenance string

const (
	// ProvenanceModel marks a model-generated recommendation.
	ProvenanceModel Provenance = "model"
	// ProvenanceRepo marks a repository-derived fact (a file that exists, a
	// discovered artifact kind).
	ProvenanceRepo Provenance = "repository"
	// ProvenanceRuntime marks runtime-derived state (scope resolution, a
	// candidate digest).
	ProvenanceRuntime Provenance = "runtime"
	// ProvenanceHuman marks a decision the human actually made.
	ProvenanceHuman Provenance = "human"
	// ProvenancePolicy marks the applicable authorization/verification policy.
	ProvenancePolicy Provenance = "policy"
)

// Valid reports whether the provenance is a member of the closed vocabulary.
func (p Provenance) Valid() bool {
	switch p {
	case ProvenanceModel, ProvenanceRepo, ProvenanceRuntime, ProvenanceHuman, ProvenancePolicy:
		return true
	default:
		return false
	}
}

// Observation is one piece of evidence the proposal was formed from.
type Observation struct {
	// ID is the stable identity of the observation.
	ID string `json:"id"`
	// Summary is the bounded, human-readable finding.
	Summary string `json:"summary"`
	// Paths are the repository paths the observation is about (optional).
	Paths []string `json:"paths,omitempty"`
	// Provenance records how the observation was produced. A model assertion
	// is not a repository fact.
	Provenance Provenance `json:"provenance"`
}

// Assumption is a statement the proposal takes for granted, together with
// whether the runtime actually validated it.
type Assumption struct {
	ID         string     `json:"id"`
	Statement  string     `json:"statement"`
	Provenance Provenance `json:"provenance"`
	Validated  bool       `json:"validated"`
}

// RiskSeverity classifies a material risk.
type RiskSeverity string

const (
	SeverityLow      RiskSeverity = "low"
	SeverityMedium   RiskSeverity = "medium"
	SeverityHigh     RiskSeverity = "high"
	SeverityCritical RiskSeverity = "critical"
)

// Risk is a material risk of a strategy together with its mitigation.
type Risk struct {
	ID          string       `json:"id"`
	Description string       `json:"description"`
	Mitigation  string       `json:"mitigation,omitempty"`
	Severity    RiskSeverity `json:"severity"`
}

// Tradeoff names what a strategy trades away for what it gains. It exists so a
// recommendation can be explained rather than asserted.
type Tradeoff struct {
	Description string `json:"description"`
	Benefit     string `json:"benefit"`
	Cost        string `json:"cost"`
}

// Dependency is a directed dependency between proposal elements. Kind names the
// dependency ("requires", "affects", "invalidates"). A revision that changes the
// `From` element invalidates every element it `invalidates`.
type Dependency struct {
	ID          string `json:"id"`
	From        string `json:"from"`
	To          string `json:"to"`
	Kind        string `json:"kind"`
	Description string `json:"description,omitempty"`
}

// Phase is an optional execution phase with its boundary and checkpoint.
type Phase struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Order      int      `json:"order"`
	Targets    []string `json:"targets,omitempty"`
	Boundary   string   `json:"boundary,omitempty"`
	Checkpoint string   `json:"checkpoint,omitempty"`
}

// VerificationKind classifies how an acceptance criterion is verified.
type VerificationKind string

const (
	// VerifySource is a deterministic source-level check.
	VerifySource VerificationKind = "source"
	// VerifyRendered is a rendered/browser check of the composed output.
	VerifyRendered VerificationKind = "rendered"
	// VerifyTests is a test-suite/command check.
	VerifyTests VerificationKind = "tests"
	// VerifyBehavioral is a runtime/behavioural observation.
	VerifyBehavioral VerificationKind = "behavioral"
	// VerifyManual is verification that depends on human judgement.
	VerifyManual VerificationKind = "manual"
)

// VerificationMethod names HOW an acceptance criterion is checked.
type VerificationMethod struct {
	ID   string           `json:"id"`
	Kind VerificationKind `json:"kind"`
	// Method is the concrete check description.
	Method    string `json:"method"`
	Automated bool   `json:"automated"`
}

// AcceptanceCriterion is one thing that must hold for the objective to be
// satisfied, bound to the verification method that establishes it.
type AcceptanceCriterion struct {
	ID           string   `json:"id"`
	Statement    string   `json:"statement"`
	Verification string   `json:"verification,omitempty"`
	Targets      []string `json:"targets,omitempty"`
	// Provenance records whether the criterion came from the human or was
	// proposed by the model and accepted.
	Provenance Provenance `json:"provenance"`
}

// Strategy is one viable approach, with its own rationale and material
// consequences.
type Strategy struct {
	ID           string                `json:"id"`
	Title        string                `json:"title"`
	Summary      string                `json:"summary"`
	Steps        []string              `json:"steps,omitempty"`
	Rationale    string                `json:"rationale,omitempty"`
	Recommended  bool                  `json:"recommended"`
	Tradeoffs    []Tradeoff            `json:"tradeoffs,omitempty"`
	Risks        []Risk                `json:"risks,omitempty"`
	Dependencies []Dependency          `json:"dependencies,omitempty"`
	Phases       []Phase               `json:"phases,omitempty"`
	Acceptance   []AcceptanceCriterion `json:"acceptance,omitempty"`
	Verification []VerificationMethod  `json:"verification,omitempty"`
	// Targets is the strategy's proposed target set. A strategy that narrows or
	// widens the scope says so explicitly rather than relying on a question.
	Targets    []string   `json:"targets,omitempty"`
	Provenance Provenance `json:"provenance"`
}

// QuestionKind is the interaction shape of a question.
type QuestionKind string

const (
	// QuestionSingle is a single-choice question.
	QuestionSingle QuestionKind = "single"
	// QuestionMulti is a multi-select question.
	QuestionMulti QuestionKind = "multi"
	// QuestionFreeForm accepts free-form input, optionally alongside options.
	QuestionFreeForm QuestionKind = "free_form"
)

// Valid reports whether the question kind is a member of the closed vocabulary.
func (k QuestionKind) Valid() bool {
	switch k {
	case QuestionSingle, QuestionMulti, QuestionFreeForm:
		return true
	default:
		return false
	}
}

// Option is one selectable choice for a question.
type Option struct {
	ID          string     `json:"id"`
	Label       string     `json:"label"`
	Description string     `json:"description,omitempty"`
	Recommended bool       `json:"recommended"`
	Provenance  Provenance `json:"provenance"`
}

// Question is one decision the human must make for the proposal to be
// executable. Questions are NOT a fixed questionnaire: an objective with no
// consequential choices carries none.
type Question struct {
	ID     string       `json:"id"`
	Prompt string       `json:"prompt"`
	Kind   QuestionKind `json:"kind"`
	// Options is the predefined choice set. It may be empty for free-form.
	Options []Option `json:"options,omitempty"`
	// AllowFreeForm permits free-form input even when options exist.
	AllowFreeForm bool `json:"allow_free_form"`
	// Required marks a question whose answer is needed before execution.
	Required bool `json:"required"`
	// Recommended names the recommended option ID (single) — a model/runtime
	// recommendation, never a human decision.
	Recommended string `json:"recommended,omitempty"`
	// RecommendedSet names the recommended option IDs for a multi-select.
	RecommendedSet []string `json:"recommended_set,omitempty"`
	// DependsOn names question IDs that must be answered before this one.
	DependsOn []string `json:"depends_on,omitempty"`
	// Affects names the proposal elements (targets, phases, criteria IDs) this
	// question's answer changes. It drives revision invalidation.
	Affects []string `json:"affects,omitempty"`
	// Default is the option ID applied when the runtime may decide from
	// reliable evidence or policy without asking (optional).
	Default string `json:"default,omitempty"`
}

// Decision is one recorded human decision.
type Decision struct {
	QuestionID string     `json:"question_id"`
	OptionID   string     `json:"option_id,omitempty"`
	OptionIDs  []string   `json:"option_ids,omitempty"`
	FreeForm   string     `json:"free_form,omitempty"`
	Provenance Provenance `json:"provenance"`
	At         time.Time  `json:"at"`
	// Revision is the proposal revision the decision was recorded against.
	Revision int `json:"revision"`
}

// AdmissionCondition is a condition that must hold before execution is
// admitted. Satisfied is a runtime/policy fact, never a model claim.
type AdmissionCondition struct {
	ID         string     `json:"id"`
	Statement  string     `json:"statement"`
	Satisfied  bool       `json:"satisfied"`
	Provenance Provenance `json:"provenance"`
}

// Contract is the typed proposal. It is deliberately proportional: an objective
// fills only the fields it actually needs.
type Contract struct {
	ID        ID     `json:"id"`
	Revision  int    `json:"revision"`
	Objective string `json:"objective"`

	Observations  []Observation `json:"observations,omitempty"`
	Assumptions   []Assumption  `json:"assumptions,omitempty"`
	Uncertainties []string      `json:"uncertainties,omitempty"`

	Strategies  []Strategy `json:"strategies,omitempty"`
	Recommended string     `json:"recommended,omitempty"`

	Questions []Question `json:"questions,omitempty"`
	Decisions []Decision `json:"decisions,omitempty"`

	Admission []AdmissionCondition `json:"admission,omitempty"`

	CreatedAt time.Time `json:"created_at,omitempty"`
}

// Strategy returns the strategy with the given ID, or nil.
func (c *Contract) Strategy(id string) *Strategy {
	if c == nil {
		return nil
	}
	for i := range c.Strategies {
		if c.Strategies[i].ID == id {
			return &c.Strategies[i]
		}
	}
	return nil
}

// Question returns the question with the given ID, or nil.
func (c *Contract) Question(id string) *Question {
	if c == nil {
		return nil
	}
	for i := range c.Questions {
		if c.Questions[i].ID == id {
			return &c.Questions[i]
		}
	}
	return nil
}

// RecommendedStrategy returns the recommended strategy, or nil.
func (c *Contract) RecommendedStrategy() *Strategy {
	if c == nil {
		return nil
	}
	if c.Recommended != "" {
		if s := c.Strategy(c.Recommended); s != nil {
			return s
		}
	}
	for i := range c.Strategies {
		if c.Strategies[i].Recommended {
			return &c.Strategies[i]
		}
	}
	return nil
}

// DecisionFor returns the recorded decision for a question, or nil.
func (c *Contract) DecisionFor(questionID string) *Decision {
	if c == nil {
		return nil
	}
	for i := range c.Decisions {
		if c.Decisions[i].QuestionID == questionID {
			return &c.Decisions[i]
		}
	}
	return nil
}

// ── Validation ──────────────────────────────────────────────────────────────

// ValidationError is a bounded, actionable contract failure.
type ValidationError struct {
	Field  string
	Reason string
}

// Error implements error.
func (e ValidationError) Error() string {
	if e.Field == "" {
		return "proposal: " + e.Reason
	}
	return "proposal: " + e.Field + ": " + e.Reason
}

// Validate checks the contract's structural invariants. It is proportional: a
// minimal proposal (objective + one strategy) validates; a proposal with
// questions must describe them correctly.
func (c *Contract) Validate() error {
	if c == nil {
		return ValidationError{Reason: "nil contract"}
	}
	if strings.TrimSpace(c.Objective) == "" {
		return ValidationError{Field: "objective", Reason: "an objective is required"}
	}
	ids := map[string]bool{}
	for _, s := range c.Strategies {
		if strings.TrimSpace(s.ID) == "" {
			return ValidationError{Field: "strategies", Reason: "every strategy needs an id"}
		}
		if ids["strategy:"+s.ID] {
			return ValidationError{Field: "strategies", Reason: "duplicate strategy id " + s.ID}
		}
		ids["strategy:"+s.ID] = true
		if strings.TrimSpace(s.Title) == "" {
			return ValidationError{Field: "strategy " + s.ID, Reason: "a strategy needs a title"}
		}
		if !s.Provenance.Valid() {
			return ValidationError{Field: "strategy " + s.ID, Reason: "unknown provenance"}
		}
	}
	if c.Recommended != "" && c.Strategy(c.Recommended) == nil {
		return ValidationError{Field: "recommended", Reason: "names unknown strategy " + c.Recommended}
	}
	qids := map[string]bool{}
	for _, q := range c.Questions {
		if strings.TrimSpace(q.ID) == "" {
			return ValidationError{Field: "questions", Reason: "every question needs an id"}
		}
		if qids[q.ID] {
			return ValidationError{Field: "questions", Reason: "duplicate question id " + q.ID}
		}
		qids[q.ID] = true
		if !q.Kind.Valid() {
			return ValidationError{Field: "question " + q.ID, Reason: "unknown kind " + string(q.Kind)}
		}
		if strings.TrimSpace(q.Prompt) == "" {
			return ValidationError{Field: "question " + q.ID, Reason: "a question needs a prompt"}
		}
		optIDs := map[string]bool{}
		for _, o := range q.Options {
			if strings.TrimSpace(o.ID) == "" {
				return ValidationError{Field: "question " + q.ID, Reason: "every option needs an id"}
			}
			if optIDs[o.ID] {
				return ValidationError{Field: "question " + q.ID, Reason: "duplicate option id " + o.ID}
			}
			optIDs[o.ID] = true
		}
		if q.Kind == QuestionSingle && len(q.Options) == 0 && !q.AllowFreeForm {
			return ValidationError{Field: "question " + q.ID, Reason: "a single-choice question needs options or free-form"}
		}
		if q.Kind == QuestionMulti && len(q.Options) == 0 && !q.AllowFreeForm {
			return ValidationError{Field: "question " + q.ID, Reason: "a multi-select question needs options or free-form"}
		}
		if q.Recommended != "" && len(q.Options) > 0 && !optIDs[q.Recommended] {
			return ValidationError{Field: "question " + q.ID, Reason: "recommendation names unknown option " + q.Recommended}
		}
		for _, dep := range q.DependsOn {
			if !qids[dep] && c.Question(dep) == nil {
				return ValidationError{Field: "question " + q.ID, Reason: "depends on unknown question " + dep}
			}
		}
	}
	return nil
}

// Fingerprint is the content identity of the contract at its current revision.
// It is revision-safe: any change to the objective, strategies, questions or
// decisions changes the fingerprint, so a stale approval can never be replayed
// against a revised proposal.
func (c *Contract) Fingerprint() string {
	if c == nil {
		return ""
	}
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "id=%s\nrev=%d\nobj=%s\n", c.ID, c.Revision, strings.TrimSpace(c.Objective))
	if s := c.RecommendedStrategy(); s != nil {
		_, _ = fmt.Fprintf(h, "rec=%s\n", s.ID)
	}
	for _, s := range c.Strategies {
		_, _ = fmt.Fprintf(h, "strategy=%s;%s\n", s.ID, s.Title)
	}
	for _, q := range c.Questions {
		_, _ = fmt.Fprintf(h, "q=%s;%s;%s\n", q.ID, q.Kind, q.Prompt)
	}
	for _, d := range sortedDecisions(c.Decisions) {
		_, _ = fmt.Fprintf(h, "d=%s;%s;%s;%s\n", d.QuestionID, d.OptionID, strings.Join(d.OptionIDs, ","), d.FreeForm)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func sortedDecisions(in []Decision) []Decision {
	out := append([]Decision(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].QuestionID < out[j].QuestionID })
	return out
}
