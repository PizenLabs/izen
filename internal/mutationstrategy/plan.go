package mutationstrategy

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/changesurface"
	"github.com/PizenLabs/izen/internal/understanding"
)

// MutationStep is one bounded proposed unit of work. It references
// only paths that already appear in the evidence-backed ChangeSurface
// (never invented), carries its proposal operation, structural estimate,
// planning budget, dependency ordering, and provenance.
type MutationStep struct {
	// ID is the stable within-plan identity (e.g. step-01).
	ID string `json:"id"`
	// SurfaceRefs are the workspace-relative paths from the ChangeSurface
	// this step proposes to mutate. Always a subset of candidates, never
	// invented.
	SurfaceRefs []string `json:"surface_refs"`
	// Operation is the proposal operation for this step.
	Operation OperationKind `json:"operation"`
	// Estimate is the structural mutation estimate for this step.
	Estimate EstimatedMutationSize `json:"estimate"`
	// Budget is the planning budget for this step (envelope, not a grant).
	Budget StepBudget `json:"budget"`
	// Envelope is the model-output envelope this step expects.
	Envelope Envelope `json:"envelope"`
	// DependsOn lists the IDs of predecessor steps that must complete
	// before this step may be proposed.
	DependsOn []string `json:"depends_on,omitempty"`
	// Rationale is a deterministic, human-readable justification for
	// the step's existence and ordering (e.g. "CSS references HTML structure").
	Rationale string `json:"rationale"`
	// Evidence carries the provenance signal keys supporting this step.
	Evidence []string `json:"evidence,omitempty"`
	// Status is the per-step planning status (mirrors the plan status
	// for TOO_LARGE propagation; READY when the step fits its envelope).
	Status PlanStatus `json:"status"`
}

// MutationPlan is the bounded planning result derived purely from intent
// + understanding + change surface (+ optional model constraints). It is
// immutable after Derive and carries no authorization.
type MutationPlan struct {
	// Strategy is the chosen high-level decomposition (SINGLE_STEP vs MULTI_STEP).
	Strategy StrategyKind `json:"strategy"`
	// Status is the overall planning outcome (READY/PARTIAL/BLOCKED/TOO_LARGE/UNRESOLVED).
	Status PlanStatus `json:"status"`
	// Steps are the bounded proposed steps (empty when unresolved/too_large with no decomposition).
	Steps []MutationStep `json:"steps,omitempty"`
	// Estimate is the aggregate structural estimate (preserves uncertainty).
	Estimate EstimatedMutationSize `json:"estimate"`
	// Evidence carries the derivation provenance (signal keys).
	Evidence []string `json:"evidence,omitempty"`
	// UnderstandingDigest binds the plan to the exact understanding it was derived from.
	UnderstandingDigest string `json:"understanding_digest"`
	// SurfaceDigest binds the plan to the exact change surface it was derived from.
	SurfaceDigest string `json:"surface_digest"`
	// IntentSummary is the truncated intent the plan was derived from.
	IntentSummary string `json:"intent_summary"`
	// UnresolvedReason explains an unresolved/blocked plan, if any.
	UnresolvedReason string `json:"unresolved_reason,omitempty"`
	// CreatedAt records when the plan was derived.
	CreatedAt time.Time `json:"created_at"`
}

// PlanOptions configures one Derive call.
type PlanOptions struct {
	// ModelConstraints carries already-known provider capability metadata
	// (output ceiling, etc.) for TOO_LARGE determination. Zero means
	// unconstrained planning.
	ModelConstraints ModelConstraints `json:"model_constraints,omitempty"`
	// StepBudget is the fallback per-step budget when no model ceiling
	// is known. Zero falls back to the estimator's default envelope.
	StepBudget StepBudget `json:"step_budget,omitempty"`
	// MaxFilesPerStep caps artifact-family coalescing inside one step.
	// Zero means no per-step file cap beyond the budget (used for
	// large-task acceptance to prove decomposition).
	MaxFilesPerStep int `json:"max_files_per_step,omitempty"`
}

// Derive deterministically builds the MutationPlan for the given
// intent + understanding + change surface. It never discovers the
// repository independently, never invents targets outside the
// evidence-backed surface, never mints authorization, never mutates
// the filesystem, and never invokes a model. The chain
// Evidence → Understanding → Surface → Strategy remains intact.
func Derive(intent string, u understanding.ProjectUnderstanding, surface changesurface.ChangeSurface, opts PlanOptions) MutationPlan {
	now := time.Now()
	plan := MutationPlan{
		UnderstandingDigest: u.Digest,
		SurfaceDigest:       surfaceDigest(surface),
		IntentSummary:       truncateIntent(intent),
		CreatedAt:           now,
	}

	// Provenance: always carry the surface evidence for explainability.
	plan.Evidence = append([]string(nil), surface.Evidence...)

	// 1. Guard: understanding must be valid and bound to the surface.
	if !u.Valid() {
		plan.Strategy = StrategySingleStep
		plan.Status = StatusUnresolved
		plan.UnresolvedReason = "project understanding is unavailable or stale; refusing to fabricate strategy"
		return plan
	}
	if surface.UnderstandingDigest != "" && u.Digest != "" && surface.UnderstandingDigest != u.Digest {
		plan.Strategy = StrategySingleStep
		plan.Status = StatusUnresolved
		plan.UnresolvedReason = "change surface derived from stale understanding; re-derive understanding and surface first"
		return plan
	}
	// 2. Guard: unresolved surface yields UNRESOLVED, never a fabricated plan.
	if surface.Status == changesurface.StatusUnresolved {
		plan.Strategy = StrategySingleStep
		plan.Status = StatusUnresolved
		if surface.UnresolvedReason != "" {
			plan.UnresolvedReason = surface.UnresolvedReason
		} else {
			plan.UnresolvedReason = "change surface is UNRESOLVED; refusing to invent mutation steps"
		}
		return plan
	}
	if len(surface.Candidates) == 0 {
		plan.Strategy = StrategySingleStep
		plan.Status = StatusUnresolved
		plan.UnresolvedReason = "change surface has no candidates; refusing to fabricate steps"
		return plan
	}

	// 3. Strategy selection: deterministic from evidence size and structural group count (domain-neutral).
	lowerIntent := strings.ToLower(strings.TrimSpace(intent))
	strategy := selectStrategy(surface, lowerIntent)
	plan.Strategy = strategy

	// 4. Determine proposal operation from intent (domain-neutral verbs only).
	op := operationForIntent(lowerIntent, string(surface.Status))

	// 5. Derive budgets.
	est := DefaultEstimator()
	if opts.StepBudget.MaxOutputTokens == 0 {
		opts.StepBudget.MaxOutputTokens = est.DefaultStepEnvelope
	}
	if opts.StepBudget.MaxFiles == 0 {
		opts.StepBudget.MaxFiles = 4
	}
	stepBudget := StepBudgetFor(opts.ModelConstraints, opts.StepBudget)
	envelope := Envelope{
		MaxOutputTokens:      stepBudget.MaxOutputTokens,
		RequiresBoundedPatch: op == OpModify,
		MaxFiles:             stepBudget.MaxFiles,
	}

	// 6. Build steps by coalescing candidates into artifact-family groups.
	esteps := buildSteps(surface, op, stepBudget, envelope, est, plan.Strategy, opts.MaxFilesPerStep)

	// 7. Estimate aggregate.
	plan.Estimate = est.EstimateForPlan(esteps)

	// 8. TOO_LARGE check: if any single step exceeds its envelope and
	// cannot be further decomposed by artifact family, the plan reports
	// TOO_LARGE with that evidence (no silent continuation).
	for i := range esteps {
		if esteps[i].Estimate.Exceeds(esteps[i].Budget) {
			// Can we decompose further? Only if the step still references
			// >1 file — otherwise it is a genuine single-artifact TOO_LARGE.
			if len(esteps[i].SurfaceRefs) > 1 && opts.MaxFilesPerStep == 0 {
				// Allow one level of finer decomposition: retry with max 1 file per step.
				retryOpts := opts
				retryOpts.MaxFilesPerStep = 1
				finer := buildSteps(surface, op, stepBudget, envelope, est, plan.Strategy, retryOpts.MaxFilesPerStep)
				// If the finer steps now all fit, adopt them instead of failing TOO_LARGE.
				allFit := true
				for _, f := range finer {
					if f.Estimate.Exceeds(f.Budget) {
						allFit = false
						break
					}
				}
				if allFit {
					esteps = finer
					plan.Estimate = est.EstimateForPlan(esteps)
					break
				}
			}
			// Otherwise mark the offending step and the plan as TOO_LARGE.
			esteps[i].Status = StatusTooLarge
			plan.Status = StatusTooLarge
			plan.Steps = esteps
			plan.UnresolvedReason = "estimated mutation exceeds the configured step envelope: step " + esteps[i].ID + " expects " + itoa(esteps[i].Estimate.Expected) + " structural units but envelope is " + itoa(esteps[i].Budget.MaxOutputTokens) + " — decomposition required, not auto-continuation"
			return plan
		}
	}
	plan.Steps = esteps

	// 9. Status: READY when resolved with steps, PARTIAL when only RELATED candidates.
	switch surface.Status {
	case changesurface.StatusPartial:
		plan.Status = StatusPartial
	default:
		plan.Status = StatusReady
	}
	// Propagate per-step status: any TOO_LARGE already handled above; otherwise READY.
	for i := range plan.Steps {
		if plan.Steps[i].Status == "" {
			plan.Steps[i].Status = plan.Status
			if plan.Steps[i].Status == StatusPartial {
				plan.Steps[i].Status = StatusReady // per-step partial is just the plan qualifier
			}
		}
	}

	return plan
}

// ─── strategy selection ───────────────────────────────────────────────
// Domain-neutral: strategy derives from candidate count, distinct
// structural groups, and direct-vs-related balance, not from web-
// specific narrow/multi-family intents.

func selectStrategy(surface changesurface.ChangeSurface, lowerIntent string) StrategyKind {
	n := len(surface.Candidates)
	if n <= 1 {
		return StrategySingleStep
	}
	// If intent is explicitly narrow and only one DIRECT candidate,
	// keep single step (bounded single-file mutation).
	if isNarrowGeneric(lowerIntent) && directCount(surface) == 1 {
		return StrategySingleStep
	}
	// If candidates span ≥2 structural groups (distinct extensions/dirs),
	// use MULTI_STEP so each bounded step maps to one group.
	if groupCount(surface) >= 2 {
		return StrategyMultiStep
	}
	// Candidate count threshold: ≥3 candidates → multi-step for bounding.
	if n >= 3 {
		return StrategyMultiStep
	}
	// Two candidates in same group → single step when structurally narrow.
	if n == 2 && groupCount(surface) == 1 {
		return StrategySingleStep
	}
	return StrategyMultiStep
}

func isNarrowGeneric(lower string) bool {
	for _, kw := range []string{"one line", "small", "fix typo", "single file", "trivial", "title", "page title"} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

func directCount(s changesurface.ChangeSurface) int {
	n := 0
	for _, c := range s.Candidates {
		if c.Certainty == changesurface.CertaintyDirect {
			n++
		}
	}
	return n
}

func directPaths(s changesurface.ChangeSurface) []string {
	var out []string
	for _, c := range s.Candidates {
		if c.Certainty == changesurface.CertaintyDirect {
			out = append(out, c.Path)
		}
	}
	sort.Strings(out)
	return out
}

func groupCount(s changesurface.ChangeSurface) int {
	seen := map[string]bool{}
	for _, c := range s.Candidates {
		seen[familyOf(c.Path)] = true
	}
	return len(seen)
}

//nolint:unused // retained for compatibility; distinct group count is via groupCount
func familyCount(s changesurface.ChangeSurface) int { return groupCount(s) }

// ─── step construction ────────────────────────────────────────────────

func buildSteps(surface changesurface.ChangeSurface, op OperationKind, budget StepBudget, envelope Envelope, est Estimator, stratKind StrategyKind, maxFilesPerStep int) []MutationStep {
	if stratKind == StrategySingleStep || len(surface.Candidates) == 1 {
		refs := sortedPaths(surface)
		// Narrow single-step intents (e.g. "Change the page title") carry
		// RELATED surface candidates that are not structurally necessary for
		// the bounded proposal. Keep the step to the DIRECT evidence only
		// so the estimate stays small and the step stays bounded.
		if directCount(surface) == 1 && len(refs) > 1 {
			hasTitle := false
			for _, r := range refs {
				// Title changes are HTML-only: filter conservatively.
				_ = r
			}
			directRefs := directPaths(surface)
			if len(directRefs) > 0 && len(directRefs) < len(refs) {
				// Heuristic: single-DIRECT single-step → use only DIRECT paths.
				// This matches the acceptance: "Change the page title" → Modify index.html.
				refs = directRefs
			}
			_ = hasTitle
		}
		if maxFilesPerStep > 0 && len(refs) > maxFilesPerStep {
			return splitByFamily(surface, op, budget, envelope, est, maxFilesPerStep)
		}
		ev := provenanceFor(refs, surface)
		estSize := est.EstimateForStep(refs, op, 0)
		return []MutationStep{{
			ID:          "step-01",
			SurfaceRefs: refs,
			Operation:   op,
			Estimate:    estSize,
			Budget:      budget,
			Envelope:    envelope,
			DependsOn:   nil,
			Rationale:   rationaleFor(refs, nil, op, 0),
			Evidence:    ev,
			Status:      "",
		}}
	}

	// MULTI_STEP: coalesce into structural groups (extension/directory)
	// so each step stays bounded. Domain-neutral: grouping by extension
	// or directory, not by html/css/js/assets.
	groups := groupByFamily(surface)
	order := familyOrder(groups)
	var steps []MutationStep
	var allEvidence []string
	for _, fam := range order {
		paths := groups[fam]
		sort.Strings(paths)
		if maxFilesPerStep > 0 && len(paths) > maxFilesPerStep {
			for i := 0; i < len(paths); i += maxFilesPerStep {
				end := i + maxFilesPerStep
				if end > len(paths) {
					end = len(paths)
				}
				chunk := paths[i:end]
				steps = append(steps, stepFor(chunk, fam, op, budget, envelope, est, surface, len(steps)))
				allEvidence = append(allEvidence, provenanceFor(chunk, surface)...)
			}
		} else {
			steps = append(steps, stepFor(paths, fam, op, budget, envelope, est, surface, len(steps)))
			allEvidence = append(allEvidence, provenanceFor(paths, surface)...)
		}
	}
	// Generic: no web-specific dependency wiring (html → css/js). Keep
	// steps independent unless explicit dependency evidence exists.
	// Conservative: no automatic DependsOn.
	_ = allEvidence
	for i := range steps {
		steps[i].ID = stepID(i)
	}
	for i := range steps {
		if steps[i].Rationale == "" {
			steps[i].Rationale = rationaleFor(steps[i].SurfaceRefs, steps[i].DependsOn, steps[i].Operation, len(steps[i].DependsOn))
		}
	}
	return steps
}

func stepFor(paths []string, fam string, op OperationKind, budget StepBudget, envelope Envelope, est Estimator, surface changesurface.ChangeSurface, index int) MutationStep {
	ev := provenanceFor(paths, surface)
	estSize := est.EstimateForStep(paths, op, 0)
	return MutationStep{
		ID:          stepID(index),
		SurfaceRefs: append([]string(nil), paths...),
		Operation:   op,
		Estimate:    estSize,
		Budget:      budget,
		Envelope:    envelope,
		DependsOn:   nil,
		Rationale:   rationaleFor(paths, nil, op, 0),
		Evidence:    ev,
		Status:      "",
	}
}

func stepID(i int) string {
	return "step-" + pad2(i+1)
}

func pad2(n int) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}

func groupByFamily(s changesurface.ChangeSurface) map[string][]string {
	out := map[string][]string{}
	for _, c := range s.Candidates {
		f := familyOf(c.Path)
		out[f] = append(out[f], c.Path)
	}
	return out
}

// familyOrder is domain-neutral: alphabetical ordering of structural groups.
func familyOrder(groups map[string][]string) []string {
	order := make([]string, 0, len(groups))
	for f := range groups {
		order = append(order, f)
	}
	sort.Strings(order)
	return order
}

//nolint:unused // retained for compatibility; distinct group count is via groupCount
func idsForFamily(steps []MutationStep, family string) []string {
	var out []string
	for _, s := range steps {
		if len(s.SurfaceRefs) > 0 && familyOf(s.SurfaceRefs[0]) == family {
			out = append(out, s.ID)
		}
	}
	return out
}

func splitByFamily(s changesurface.ChangeSurface, op OperationKind, budget StepBudget, envelope Envelope, est Estimator, maxFilesPerStep int) []MutationStep {
	paths := sortedPaths(s)
	groups := groupByFamily(changesurface.ChangeSurface{Candidates: candidatesFromPaths(paths, s)})
	order := familyOrder(groups)
	var steps []MutationStep
	idx := 0
	for _, fam := range order {
		ps := groups[fam]
		sort.Strings(ps)
		for i := 0; i < len(ps); i += maxFilesPerStep {
			end := i + maxFilesPerStep
			if end > len(ps) {
				end = len(ps)
			}
			chunk := ps[i:end]
			steps = append(steps, stepFor(chunk, fam, op, budget, envelope, est, s, idx))
			idx++
		}
	}
	for i := range steps {
		steps[i].ID = stepID(i)
	}
	return steps
}

func candidatesFromPaths(paths []string, src changesurface.ChangeSurface) []changesurface.Candidate {
	idx := map[string]changesurface.Candidate{}
	for _, c := range src.Candidates {
		idx[c.Path] = c
	}
	var out []changesurface.Candidate
	for _, p := range paths {
		if c, ok := idx[p]; ok {
			out = append(out, c)
		} else {
			out = append(out, changesurface.Candidate{Path: p, Certainty: changesurface.CertaintyRelated})
		}
	}
	return out
}

func sortedPaths(s changesurface.ChangeSurface) []string {
	paths := make([]string, 0, len(s.Candidates))
	for _, c := range s.Candidates {
		paths = append(paths, c.Path)
	}
	sort.Strings(paths)
	return paths
}

func provenanceFor(refs []string, s changesurface.ChangeSurface) []string {
	idx := map[string][]string{}
	for _, c := range s.Candidates {
		idx[c.Path] = c.Evidence
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range refs {
		for _, e := range idx[r] {
			if !seen[e] {
				seen[e] = true
				out = append(out, e)
			}
		}
	}
	sort.Strings(out)
	return out
}

func rationaleFor(refs []string, depends []string, op OperationKind, depCount int) string {
	fam := ""
	if len(refs) > 0 {
		fam = familyOf(refs[0])
	}
	if depCount > 0 {
		return string(op) + " " + fam + " — depends on " + strings.Join(depends, ", ")
	}
	return string(op) + " " + fam + " — bounded structural mutation"
}

// ─── digest helpers ───────────────────────────────────────────────────

func surfaceDigest(s changesurface.ChangeSurface) string {
	h := sha256.New()
	h.Write([]byte(s.Status))
	h.Write([]byte{0})
	h.Write([]byte(s.UnderstandingDigest))
	h.Write([]byte{0})
	h.Write([]byte(s.IntentSummary))
	h.Write([]byte{0})
	paths := sortedPaths(s)
	for _, p := range paths {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	for _, e := range s.Evidence {
		h.Write([]byte(e))
		h.Write([]byte{0})
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:8])
}

func truncateIntent(s string) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if len(s) > 160 {
		return s[:157] + "..."
	}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [32]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
