package problem

import (
	"sort"
	"strings"

	"github.com/PizenLabs/izen/internal/problemsurface"
	"github.com/PizenLabs/izen/internal/understanding"
)

// StepKind is the kind of problem-solving step. It is proposal-level,
// never an execution directive.
type StepKind string

const (
	StepInvestigate StepKind = "INVESTIGATE"
	StepAnalyze     StepKind = "ANALYZE"
	StepExperiment  StepKind = "EXPERIMENT"
	StepMutate      StepKind = "MUTATE"
	StepVerify      StepKind = "VERIFY"
	StepObserve     StepKind = "OBSERVE"
)

func (k StepKind) String() string { return string(k) }
func (k StepKind) Valid() bool {
	switch k {
	case StepInvestigate, StepAnalyze, StepExperiment, StepMutate, StepVerify, StepObserve:
		return true
	}
	return false
}

// ProblemStep is one bounded, evidence-backed reasoning/engineering step.
type ProblemStep struct {
	ID         string   `json:"id"`
	Kind       StepKind `json:"kind"`
	References []string `json:"references,omitempty"`
	Rationale  string   `json:"rationale"`
	Evidence   []string `json:"evidence,omitempty"`
	DependsOn  []string `json:"depends_on,omitempty"`
}

// PlanStatus is the planning outcome.
type PlanStatus string

const (
	StatusReady      PlanStatus = "READY"
	StatusPartial    PlanStatus = "PARTIAL"
	StatusUnresolved PlanStatus = "UNRESOLVED"
)

// ProblemSolvingPlan is the bounded reasoning/engineering proposal.
// It is pure, deterministic, read-only, and carries no authorization,
// execution, retry, or continuation semantics.
type ProblemSolvingPlan struct {
	IntentSummary       string        `json:"intent_summary"`
	UnderstandingDigest string        `json:"understanding_digest"`
	SurfaceDigest       string        `json:"surface_digest"`
	Status              PlanStatus    `json:"status"`
	Steps               []ProblemStep `json:"steps,omitempty"`
	Evidence            []string      `json:"evidence,omitempty"`
	UnresolvedReason    string        `json:"unresolved_reason,omitempty"`
}

// Derive builds the ProblemSolvingPlan for intent + understanding +
// problem surface. It is the only entry point that translates a
// human intent into bounded problem-solving steps without forcing
// every task into a mutation operation.
//
//   - Investigation intents (race, deadlock, leak, latency, flaky, etc.)
//     yield INVESTIGATE/ANALYZE/OBSERVE steps, not CREATE/MODIFY.
//   - Mutation intents yield MUTATE steps that correspond to the
//     evidence-backed mutation surface.
//   - The plan never mints authorization and never schedules execution.
func Derive(intent string, u understanding.ProjectUnderstanding, ps problemsurface.ProblemSurface) ProblemSolvingPlan {
	plan := ProblemSolvingPlan{
		IntentSummary:       truncateIntent(intent),
		UnderstandingDigest: u.Digest,
		SurfaceDigest:       ps.UnderstandingDigest,
		Evidence:            append([]string(nil), ps.Evidence...),
	}
	if !u.Valid() {
		plan.Status = StatusUnresolved
		plan.UnresolvedReason = "project understanding is unavailable or stale; refusing to fabricate problem plan"
		return plan
	}
	if ps.UnderstandingDigest != "" && u.Digest != "" && ps.UnderstandingDigest != u.Digest {
		plan.Status = StatusUnresolved
		plan.UnresolvedReason = "problem surface derived from stale understanding; re-derive first"
		return plan
	}
	if ps.Status == problemsurface.StatusUnresolved {
		plan.Status = StatusUnresolved
		if ps.UnresolvedReason != "" {
			plan.UnresolvedReason = ps.UnresolvedReason
		} else {
			plan.UnresolvedReason = "problem surface is UNRESOLVED; refusing to invent problem steps"
		}
		return plan
	}
	if len(ps.References) == 0 {
		plan.Status = StatusUnresolved
		plan.UnresolvedReason = "problem surface has no references; refusing to fabricate steps"
		return plan
	}

	lower := strings.ToLower(strings.TrimSpace(intent))
	kind := classifyProblemKind(lower)

	// Build steps: for MUTATE, one step per reference group (bounded);
	// for INVESTIGATE/ANALYZE/OBSERVE, one step that references the
	// problem-relevant evidence (investigation does not imply mutation).
	var steps []ProblemStep
	switch kind {
	case StepMutate:
		// One MUTATE step per distinct path group (extension/dir), bounded.
		refs := sortedRefs(ps.References)
		// For small surfaces, single step; for larger, split by group.
		if len(refs) <= 2 {
			steps = []ProblemStep{{
				ID:         "step-01",
				Kind:       StepMutate,
				References: refs,
				Rationale:  "bounded mutation proposal for evidence-backed references",
				Evidence:   provenanceFor(refs, ps.References),
			}}
		} else {
			// Split into two steps conservatively (first half / second half)
			mid := (len(refs) + 1) / 2
			steps = []ProblemStep{
				{ID: "step-01", Kind: StepMutate, References: refs[:mid], Rationale: "bounded mutation proposal — group A", Evidence: provenanceFor(refs[:mid], ps.References)},
				{ID: "step-02", Kind: StepMutate, References: refs[mid:], Rationale: "bounded mutation proposal — group B", Evidence: provenanceFor(refs[mid:], ps.References), DependsOn: []string{"step-01"}},
			}
		}
	case StepInvestigate, StepAnalyze, StepObserve, StepExperiment, StepVerify:
		refs := sortedRefs(ps.References)
		if len(refs) > 4 {
			refs = refs[:4]
		}
		steps = []ProblemStep{{
			ID:         "step-01",
			Kind:       kind,
			References: refs,
			Rationale:  string(kind) + " evidence-backed references for investigation/verification",
			Evidence:   provenanceFor(refs, ps.References),
		}}
	default:
		// Generic: choose INVESTIGATE for broad intents without clear mutation verb
		refs := sortedRefs(ps.References)
		if len(refs) > 4 {
			refs = refs[:4]
		}
		steps = []ProblemStep{{
			ID:         "step-01",
			Kind:       StepInvestigate,
			References: refs,
			Rationale:  "bounded investigation proposal",
			Evidence:   provenanceFor(refs, ps.References),
		}}
	}

	if len(steps) == 0 {
		plan.Status = StatusUnresolved
		plan.UnresolvedReason = "could not derive problem steps from surface"
		return plan
	}

	// Deduplicate steps refs already sorted.
	for i := range steps {
		sort.Strings(steps[i].References)
		steps[i].Evidence = uniqueSorted(steps[i].Evidence)
	}

	switch ps.Status {
	case problemsurface.StatusPartial:
		plan.Status = StatusPartial
	default:
		plan.Status = StatusReady
	}
	plan.Steps = steps
	return plan
}

func classifyProblemKind(lower string) StepKind {
	if containsAny(lower, []string{"investigate", "race", "deadlock", "leak", "flaky", "latency", "profile", "benchmark", "bottleneck", "contention", "starvation", "memory", "goroutine", "channel", "mutex", "lock"}) {
		return StepInvestigate
	}
	if containsAny(lower, []string{"analyze", "analysis", "root cause", "diagnose", "triage"}) {
		return StepAnalyze
	}
	if containsAny(lower, []string{"experiment", "repro", "benchmark", "measure", "test hypothesis"}) {
		return StepExperiment
	}
	if containsAny(lower, []string{"verify", "validate", "confirm fix", "check", "test", "ci"}) {
		// If mutation verbs also present, prefer MUTATE→VERIFY workflow, but single-step VERIFY here.
		if containsAny(lower, []string{"create", "add", "modify", "refactor", "fix", "change"}) {
			return StepMutate
		}
		return StepVerify
	}
	if containsAny(lower, []string{"observe", "monitor", "trace", "log", "metric"}) {
		return StepObserve
	}
	// Mutation verbs
	if containsAny(lower, []string{"create", "add", "modify", "change", "update", "fix", "refactor", "rename", "delete", "remove", "redesign", "overhaul", "restructure"}) {
		return StepMutate
	}
	// Default: investigation for unknown/ambiguous (do not force mutation)
	return StepInvestigate
}

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}

func sortedRefs(refs []problemsurface.Reference) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.Path)
	}
	sort.Strings(out)
	return out
}

func provenanceFor(refs []string, all []problemsurface.Reference) []string {
	idx := map[string][]string{}
	for _, r := range all {
		idx[r.Path] = r.Evidence
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range refs {
		for _, e := range idx[p] {
			if !seen[e] {
				seen[e] = true
				out = append(out, e)
			}
		}
	}
	sort.Strings(out)
	return out
}

func uniqueSorted(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func truncateIntent(s string) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if len(s) > 160 {
		return s[:157] + "..."
	}
	return s
}
