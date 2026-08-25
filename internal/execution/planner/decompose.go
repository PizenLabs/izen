package planner

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/izen/internal/execution"
)

// ── Token accounting ────────────────────────────────────────────────────────
//
// The planner uses the SAME size heuristic as Boundary 2 and context
// compilation (≈4 bytes per token) and the same generation expansion factor
// (FullRewriteTokenMultiplier): regenerating a region costs its own token size
// multiplied by the full-rewrite factor. A sub-task estimated under this
// accounting and inside the strict 0.7 × max_output ceiling therefore passes
// execution.EvaluatePreflight individually BY CONSTRUCTION.

// EstimateTokens converts a byte count into its conservative token estimate.
func EstimateTokens(n int) int {
	if n <= 0 {
		return 0
	}
	return n / 4
}

// EstimateRegionTokens estimates the generation cost of one region of source:
// region_bytes/4 × FullRewriteTokenMultiplier.
func EstimateRegionTokens(source []byte, r Region) int {
	return EstimateTokens(len(SliceLines(source, r))) * execution.FullRewriteTokenMultiplier
}

// PreflightFeasible reports whether one sub-task passes the Boundary-2
// preflight guard INDIVIDUALLY: the region's own size, evaluated as an
// unbounded rewrite under max_output, must be feasible. This is the invariant
// every staged sub-task is verified against before it may join a plan.
func PreflightFeasible(source []byte, r Region, maxOutputTokens int) bool {
	v := execution.EvaluatePreflight(execution.PreflightRequest{
		ArtifactBounded: false,
		TargetBytes:     len(SliceLines(source, r)),
		MaxOutputTokens: maxOutputTokens,
	})
	return v.Feasible
}

// ── Decomposer registry ─────────────────────────────────────────────────────

// decomposers is the ordered registry consulted by Decompose. Order matters:
// the first Supports match wins. Structural splitters precede block splitters.
var decomposers = []Decomposer{
	ASTDecomposer{},
	BlockDecomposer{},
}

// ForTarget returns the registered decomposer that handles the target, or nil.
func ForTarget(target string) Decomposer {
	for _, d := range decomposers {
		if d.Supports(target) {
			return d
		}
	}
	return nil
}

// Decomposable reports whether any registered decomposer handles the target's
// format. It never inspects content.
func Decomposable(target string) bool {
	return ForTarget(target) != nil
}

// Decompose partitions ONE infeasible objective into a validated ExecutionDAG.
// It selects the structural or block decomposer for the target's format,
// splits the source at natural boundaries, greedily groups adjacent sections
// under the strict sub-task budget, and stages the plan against baseDigest.
//
// The returned DAG satisfies every Validate() invariant and every sub-task
// passes the Boundary-2 preflight guard individually. When even the finest
// split cannot fit the budget the error is ErrNotDecomposable — fail-closed,
// with no partial plan returned.
func Decompose(objective, target string, source []byte, baseDigest string, maxOutputTokens int) (*ExecutionDAG, error) {
	if len(strings.TrimSpace(string(source))) == 0 {
		return nil, ErrEmptySource
	}
	budget := SubTaskBudget(maxOutputTokens)
	if budget <= 0 {
		return nil, fmt.Errorf("%w: max_output=%d leaves no per-sub-task budget", ErrNotDecomposable, maxOutputTokens)
	}
	d := ForTarget(target)
	if d == nil {
		return nil, fmt.Errorf("%w: %s", ErrNoDecomposer, filepath.Ext(target))
	}
	sections, err := d.Split(target, source)
	if err != nil {
		return nil, err
	}
	if len(sections) == 0 {
		return nil, ErrEmptySource
	}

	dag := NewExecutionDAG(objective, target, kindOf(d), baseDigest, maxOutputTokens)
	groups, err := groupSections(sections, func(r Region) int { return EstimateRegionTokens(source, r) }, budget)
	if err != nil {
		return nil, err
	}
	for i, g := range groups {
		region := Region{StartLine: g[0].Region.StartLine, EndLine: g[len(g)-1].Region.EndLine}
		st := SubTask{
			ID:              fmt.Sprintf("st-%d", i+1),
			Index:           i + 1,
			Kind:            dag.Kind,
			Target:          target,
			Description:     describeGroup(g),
			Region:          region,
			EstimatedTokens: EstimateRegionTokens(source, region),
		}
		if err := dag.AddTask(st); err != nil {
			return nil, err
		}
	}
	if err := dag.Validate(); err != nil {
		return nil, err
	}
	return dag, nil
}

// kindOf maps a decomposer onto its canonical SplitKind label.
func kindOf(d Decomposer) SplitKind {
	switch d.(type) {
	case ASTDecomposer:
		return SplitAST
	default:
		return SplitBlock
	}
}

// groupSections merges ADJACENT sections into budget-fitted groups. The
// merged REGION is always re-estimated as a whole (never the sum of its
// parts): floor division makes token estimates superadditive, so a merged
// region can cost more than the sum of its sections and must be measured
// directly against the ceiling. A single indivisible section larger than the
// ceiling aborts with ErrNotDecomposable — no oversized unit ever joins a
// plan.
func groupSections(sections []Section, estimate func(Region) int, budget int) ([][]Section, error) {
	var groups [][]Section
	var cur []Section
	flush := func() {
		if len(cur) > 0 {
			groups = append(groups, cur)
			cur = nil
		}
	}
	for _, s := range sections {
		if len(cur) == 0 {
			if tok := estimate(s.Region); tok > budget {
				return nil, indivisibleError(s, tok, budget)
			}
			cur = append(cur, s)
			continue
		}
		merged := Region{StartLine: cur[0].Region.StartLine, EndLine: s.Region.EndLine}
		if estimate(merged) > budget {
			flush()
			if tok := estimate(s.Region); tok > budget {
				return nil, indivisibleError(s, tok, budget)
			}
			cur = []Section{s}
			continue
		}
		cur = append(cur, s)
	}
	flush()
	if len(groups) == 0 {
		return nil, ErrEmptySource
	}
	if len(groups) > MaxSubTasks {
		return nil, fmt.Errorf("%w: %d sub-tasks would be required (cap %d) — raise max_output or reduce scope",
			ErrNotDecomposable, len(groups), MaxSubTasks)
	}
	return groups, nil
}

// indivisibleError renders the fail-closed error for one oversized section.
func indivisibleError(s Section, tok, budget int) error {
	return fmt.Errorf("%w: %q spans %s (%d lines) and estimates ~%d tokens but the ceiling is %d",
		ErrNotDecomposable, truncateLabel(s.Label), s.Region, s.Region.Lines(), tok, budget)
}

// describeGroup renders the bounded human description of one group of
// sections: up to three labels, then an elision marker.
func describeGroup(group []Section) string {
	labels := make([]string, 0, len(group))
	elided := 0
	for _, s := range group {
		l := truncateLabel(s.Label)
		if len(labels) < 3 {
			labels = append(labels, l)
		} else {
			elided++
		}
	}
	desc := strings.Join(labels, ", ")
	if elided > 0 {
		desc += fmt.Sprintf(", +%d more", elided)
	}
	return desc
}

// truncateLabel bounds one section label to the description budget.
func truncateLabel(label string) string {
	label = strings.TrimSpace(label)
	label = strings.Join(strings.Fields(label), " ")
	if len(label) > 48 {
		return label[:45] + "…"
	}
	if label == "" {
		return "(unnamed)"
	}
	return label
}
