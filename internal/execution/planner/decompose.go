package planner

import (
	"bytes"
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

// Decompose partitions ONE infeasible objective into a validated ExecutionDAG
// using the SYNTACTIC pipeline only: it selects the structural or block
// decomposer for the target's format, splits the source at natural boundaries,
// slices any section that still exceeds the strict sub-task budget into
// contiguous line-range windows (FallbackLineSlicer), greedily groups adjacent
// sections under the budget, and stages the plan against baseDigest.
//
// Decompose is the low_semantic_confidence fallback path of DecomposeTarget;
// new call sites should prefer DecomposeTarget, which tries Lea's semantic
// units first and records LowSemanticConfidence when it must fall back here.
//
// The returned DAG satisfies every Validate() invariant and every sub-task
// passes the Boundary-2 preflight guard individually. When even single lines
// cannot fit the budget the error is ErrNotDecomposable — fail-closed, with no
// partial plan returned.
func Decompose(objective, target string, source []byte, baseDigest string, maxOutputTokens int) (*ExecutionDAG, error) {
	if len(strings.TrimSpace(string(source))) == 0 {
		return nil, ErrEmptySource
	}
	budget := SubTaskBudget(maxOutputTokens)
	if budget <= 0 {
		return nil, fmt.Errorf("%w: max_output=%d leaves no per-sub-task budget", ErrNotDecomposable, maxOutputTokens)
	}
	return stageSyntacticDAG(objective, target, source, baseDigest, maxOutputTokens, budget)
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

// ── DecomposeTarget: semantic-first partitioning ────────────────────────────

// DecomposeTarget partitions ONE infeasible objective over ONE target into a
// validated ExecutionDAG, preferring SEMANTIC UNITS over line ranges.
//
// The strategy ladder:
//
//  1. SEMANTIC SPLITTING — when the target carries a Lea structural scanner
//     (HTML / JSX / Go templates), LeaStructuralScan parses the document
//     read-only and yields units that are structural nodes ("<head>
//     metadata", "<section#hero> hero") plus targeted findings. Units are
//     refined along descendant node boundaries until every piece fits the
//     strict sub-task budget.
//
//  2. SYNTACTIC FALLBACK — only when the scan is unavailable for the format,
//     recovers from malformed structure (LowConfidence) or partitions nothing
//     (< minSemanticUnits units) does the plan fall back to the syntactic
//     splitters; the staged DAG records LowSemanticConfidence=true so callers
//     know the units are line ranges, not semantic nodes.
//
// Both paths share the same downstream machinery: oversize sections explode
// into contiguous budget-bounded line windows (FallbackLineSlicer), adjacent
// sections group under the budget, and the plan stages against baseDigest.
// The returned DAG satisfies every Validate() invariant and every sub-task
// passes the Boundary-2 preflight guard individually. When even single lines
// cannot fit the budget the error is ErrNotDecomposable — fail-closed, with
// no partial plan returned.
func DecomposeTarget(objective, target string, source []byte, baseDigest string, maxOutputTokens int) (*ExecutionDAG, error) {
	if len(strings.TrimSpace(string(source))) == 0 {
		return nil, ErrEmptySource
	}
	// ── STRICT PREFLIGHT BASELINE SYNTAX GUARD (invariant) ───────────────
	// DAG decomposition is STRICTLY FORBIDDEN when the target's baseline AST is
	// corrupt. A broken document must never be sliced into semantic-structural
	// or line-window sub-tasks: doing so would ask the model to reason over an
	// unparseable structure and can only yield a regression. The caller must
	// divert to the Zero-Token DecisionSurface barrier instead.
	if err := execution.ValidateDocumentSyntax(target, source); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrDecompositionForbiddenCorruptAST, err)
	}
	budget := SubTaskBudget(maxOutputTokens)
	if budget <= 0 {
		return nil, fmt.Errorf("%w: max_output=%d leaves no per-sub-task budget", ErrNotDecomposable, maxOutputTokens)
	}

	if scan := LeaStructuralScan(target, source); scan != nil {
		if !scan.LowConfidence && len(scan.Units) >= minSemanticUnits {
			return stageSemanticDAG(objective, target, source, baseDigest, maxOutputTokens, budget, scan)
		}
		// low_semantic_confidence: syntax parsing failed or partitioned
		// nothing — retain syntactic splitting as the fallback and record why.
		dag, err := stageSyntacticDAG(objective, target, source, baseDigest, maxOutputTokens, budget)
		if err != nil {
			return nil, err
		}
		dag.LowSemanticConfidence = true
		return dag, nil
	}
	// No Lea scanner for this format: the syntactic splitter IS the strategy.
	return stageSyntacticDAG(objective, target, source, baseDigest, maxOutputTokens, budget)
}

// stageSemanticDAG builds the DAG from Lea's semantic units: refine oversize
// units along descendant node boundaries, group what fits together, and stage
// each group as one sub-task whose description names structural identities.
func stageSemanticDAG(objective, target string, source []byte, baseDigest string, maxOutputTokens, budget int, scan *LeaScanReport) (*ExecutionDAG, error) {
	lines := splitKeepNewline(source)
	sections := refineOversizeUnits(lines, scan.Units, scan.Nodes, budget)
	var err error
	// Fail-safe: a unit with no internal structure at all still cannot join a
	// plan oversized — the line-slicing fallback remains the last resort.
	sections, err = explodeOversizeSections(lines, sections, budget)
	if err != nil {
		return nil, err
	}
	dag := NewExecutionDAG(objective, target, SplitSemantic, baseDigest, maxOutputTokens)
	if err := stageSections(dag, lines, sections); err != nil {
		return nil, err
	}
	if err := dag.Validate(); err != nil {
		return nil, err
	}
	return dag, nil
}

// StageSemanticSections stages a MANIFEST-SCOPED validated ExecutionDAG from
// explicit semantic sections. The caller (autonomy.Pass 1 manifest hook) has
// already pruned the Lea scan's units down to exactly the AST/Semantic nodes
// the MutationManifest marks for modification; this function only stages them
// under the strict 0.7 sub-task ceiling, applying the line-window fallback to
// any individual section that still exceeds the budget.
//
// The returned DAG is ManifestScoped=true: Validate() does not require its
// regions to cover the whole file, because unmodified sections were pruned
// before dispatch — no sub-task is ever created solely to evaluate to
// no_op_objective_satisfied over an untouched block.
func StageSemanticSections(objective, target string, source []byte, baseDigest string, maxOutputTokens int, sections []Section) (*ExecutionDAG, error) {
	if len(sections) == 0 {
		return nil, ErrEmptySource
	}
	// ── STRICT PREFLIGHT BASELINE SYNTAX GUARD (invariant) ───────────────
	// Manifest-scoped decomposition is equally forbidden on a corrupt baseline:
	// pruning the manifest surface cannot repair a broken AST, so no sub-task
	// may be staged against it.
	if err := execution.ValidateDocumentSyntax(target, source); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrDecompositionForbiddenCorruptAST, err)
	}
	budget := SubTaskBudget(maxOutputTokens)
	if budget <= 0 {
		return nil, fmt.Errorf("%w: max_output=%d leaves no per-sub-task budget", ErrNotDecomposable, maxOutputTokens)
	}
	lines := splitKeepNewline(source)
	dag := NewExecutionDAG(objective, target, SplitSemantic, baseDigest, maxOutputTokens)
	dag.ManifestScoped = true
	for _, s := range sections {
		if regionTokensInLines(lines, s.Region) > budget {
			slices, err := FallbackLineSlicer(joinLines(lines), s, budget)
			if err != nil {
				return nil, err
			}
			for _, sl := range slices {
				if err := addScopedSubTask(dag, lines, sl); err != nil {
					return nil, err
				}
			}
			continue
		}
		if err := addScopedSubTask(dag, lines, s); err != nil {
			return nil, err
		}
	}
	if err := dag.Validate(); err != nil {
		return nil, err
	}
	return dag, nil
}

// addScopedSubTask appends one manifest-scoped section as the next sub-task,
// carrying its semantic label and a measured token estimate under the same
// accounting as Boundary 2.
func addScopedSubTask(dag *ExecutionDAG, lines [][]byte, s Section) error {
	est := regionTokensInLines(lines, s.Region)
	if est <= 0 {
		est = 1 // a non-empty window always carries at least one token
	}
	return dag.AddTask(SubTask{
		ID:              fmt.Sprintf("st-%d", len(dag.SubTasks)+1),
		Index:           len(dag.SubTasks) + 1,
		Kind:            dag.Kind,
		Target:          dag.Target,
		Description:     describeGroup([]Section{s}),
		Region:          s.Region,
		EstimatedTokens: est,
	})
}

// stageSyntacticDAG is the pre-existing syntactic pipeline (structural or
// block splitter + line-slice fallback), factored out of Decompose so both
// entry points share it verbatim.
func stageSyntacticDAG(objective, target string, source []byte, baseDigest string, maxOutputTokens, budget int) (*ExecutionDAG, error) {
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
	lines := splitKeepNewline(source)
	sections, err = explodeOversizeSections(lines, sections, budget)
	if err != nil {
		return nil, err
	}
	dag := NewExecutionDAG(objective, target, kindOf(d), baseDigest, maxOutputTokens)
	if err := stageSections(dag, lines, sections); err != nil {
		return nil, err
	}
	if err := dag.Validate(); err != nil {
		return nil, err
	}
	return dag, nil
}

// stageSections groups adjacent sections under the budget and appends one
// sub-task per group to the DAG.
func stageSections(dag *ExecutionDAG, lines [][]byte, sections []Section) error {
	groups, err := groupSections(sections, func(r Region) int { return regionTokensInLines(lines, r) }, dag.Budget())
	if err != nil {
		return err
	}
	for i, g := range groups {
		region := Region{StartLine: g[0].Region.StartLine, EndLine: g[len(g)-1].Region.EndLine}
		est := regionTokensInLines(lines, region)
		if est <= 0 {
			est = 1 // a non-empty window always carries at least one token
		}
		st := SubTask{
			ID:              fmt.Sprintf("st-%d", i+1),
			Index:           i + 1,
			Kind:            subTaskKind(dag.Kind, g),
			Target:          dag.Target,
			Description:     describeGroup(g),
			Region:          region,
			EstimatedTokens: est,
		}
		if err := dag.AddTask(st); err != nil {
			return err
		}
	}
	return nil
}

// refineOversizeUnits replaces every semantic unit whose own estimate exceeds
// the budget with pieces cut at DESCENDANT NODE BOUNDARIES first: candidate
// cut lines are the start lines of all scanned nodes strictly inside the
// unit's region, so pieces remain structural regions rather than arbitrary
// byte windows. Pieces still too large pass through untouched — the caller's
// explodeOversizeSections applies the final line-window fallback.
func refineOversizeUnits(lines [][]byte, units []Section, nodes []DOMNode, budget int) []Section {
	cutAt := make(map[int]string, len(nodes)) // start line -> unit label
	for _, n := range nodes {
		if n.StartLine >= 1 && n.Tag != "" {
			cutAt[n.StartLine] = nodeUnitLabel(n)
		}
	}
	var out []Section
	for _, u := range units {
		if regionTokensInLines(lines, u.Region) <= budget {
			out = append(out, u)
			continue
		}
		out = append(out, splitUnitAtNodes(lines, u, cutAt, budget)...)
	}
	return out
}

// splitUnitAtNodes cuts one oversize unit at descendant node start lines,
// greedily accumulating structural pieces while they fit the budget. A piece
// whose own first inter-node span already exceeds the budget is emitted as
// far as it can grow (down to a single line) and the remainder continues from
// the next boundary; any residual oversize piece passes through untouched —
// the caller's explodeOversizeSections applies the final line-window fallback.
func splitUnitAtNodes(lines [][]byte, u Section, cutAt map[int]string, budget int) []Section {
	var bounds []int
	for line := u.Region.StartLine + 1; line <= u.Region.EndLine; line++ {
		if _, ok := cutAt[line]; ok {
			bounds = append(bounds, line)
		}
	}
	if len(bounds) == 0 {
		return []Section{u} // no internal structure: leave for the line fallback
	}
	base := strings.TrimSuffix(truncateLabel(u.Label), "…")
	var out []Section
	segStart := u.Region.StartLine
	k := 0 // next unconsumed boundary index
	for segStart <= u.Region.EndLine {
		// Grow to the furthest boundary keeping the segment within budget.
		bestEnd, bestK := segStart, k
		for j := k; j < len(bounds); j++ {
			candEnd := bounds[j] - 1 // segment ends right before the node opens
			if candEnd < segStart || candEnd > u.Region.EndLine {
				continue
			}
			tok := regionTokensInLines(lines, Region{StartLine: segStart, EndLine: candEnd})
			if tok > budget && candEnd > segStart {
				break // this boundary would overflow: keep the last fit
			}
			bestEnd, bestK = candEnd, j+1
		}
		end := bestEnd
		if end > u.Region.EndLine {
			end = u.Region.EndLine
		}
		label := u.Label
		if childLabel, ok := cutAt[end+1]; ok && segStart != u.Region.StartLine && end+1 <= u.Region.EndLine {
			label = childLabel // piece opens exactly at a known child boundary
		} else if segStart != u.Region.StartLine {
			label = base + " (continued)"
		}
		out = append(out, Section{Region: Region{StartLine: segStart, EndLine: end}, Label: label})
		segStart = end + 1
		k = bestK
	}
	return out
}

// groupSections merges ADJACENT sections into budget-fitted groups. The
// merged REGION is always re-estimated as a whole (never the sum of its
// parts): floor division makes token estimates superadditive, so a merged
// region can cost more than the sum of its sections and must be measured
// directly against the ceiling. Oversize sections were already exploded into
// line windows by explodeOversizeSections upstream; the indivisible-section
// abort here remains as the fail-closed defense in depth — no oversized unit
// ever joins a plan.
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

// ── Secondary fallback: fine-grained line slicing ───────────────────────────

// FallbackLineSlicer partitions ONE oversize section into contiguous
// budget-bounded LINE windows. It walks clean \n breaks and greedily
// accumulates lines until admitting one more would push the window's token
// estimate past safeChunkLimit (the strict sub-task ceiling,
// max_output × 0.7); the accumulated range is then emitted as a Section bound
// to its explicit [StartLine, EndLine] interval. Token estimates use the
// SAME accounting as Boundary 2 (bytes/4 × FullRewriteTokenMultiplier), so
// every emitted window passes EvaluatePreflight individually BY CONSTRUCTION.
//
// A single line whose own estimate already exceeds safeChunkLimit is truly
// indivisible: the error is fail-closed ErrNotDecomposable.
func FallbackLineSlicer(source []byte, s Section, safeChunkLimit int) ([]Section, error) {
	lines := splitKeepNewline(SliceLines(source, s.Region))
	if len(lines) == 0 || s.Region.StartLine < 1 || s.Region.EndLine < s.Region.StartLine {
		return nil, fmt.Errorf("%w: section %q carries no sliceable content", ErrNotDecomposable, truncateLabel(s.Label))
	}
	base := s.Region.StartLine
	var out []Section
	winStart := 0 // index into lines: first line of the open window
	winBytes := 0 // bytes of the open window incl. per-line newlines
	emit := func(last int) {
		r := Region{StartLine: base + winStart, EndLine: base + last}
		out = append(out, Section{
			Region:       r,
			Label:        fmt.Sprintf("%s [%s]", truncateLabel(s.Label), r),
			BoundedLines: true,
		})
	}
	for i, line := range lines {
		n := len(line) + 1
		if winBytes > 0 && regionTokens(winBytes+n) > safeChunkLimit {
			emit(i - 1)
			winStart, winBytes = i, 0
		}
		if est := regionTokens(n); est > safeChunkLimit {
			return nil, fmt.Errorf("%w: %q line %d alone estimates ~%d tokens but the ceiling is %d — no finer split exists",
				ErrNotDecomposable, truncateLabel(s.Label), base+i, est, safeChunkLimit)
		}
		winBytes += n
	}
	emit(len(lines) - 1)
	return out, nil
}

// explodeOversizeSections replaces every section whose own token estimate
// exceeds safeChunkLimit with the contiguous line-range windows produced by
// FallbackLineSlicer. Sections already within budget pass through untouched;
// ordering and contiguity are preserved, so the union still covers the whole
// source. Any failure is fail-closed: no oversized unit survives.
func explodeOversizeSections(lines [][]byte, sections []Section, safeChunkLimit int) ([]Section, error) {
	var out []Section
	for _, s := range sections {
		if regionTokensInLines(lines, s.Region) <= safeChunkLimit {
			out = append(out, s)
			continue
		}
		slices, err := FallbackLineSlicer(joinLines(lines), s, safeChunkLimit)
		if err != nil {
			return nil, err
		}
		out = append(out, slices...)
	}
	return out, nil
}

// joinLines reassembles a line table into newline-terminated source bytes.
// It is only called for sections that genuinely need line-window slicing;
// the hot estimate paths never reassemble.
func joinLines(lines [][]byte) []byte {
	var b bytes.Buffer
	for _, l := range lines {
		b.Write(l)
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// regionTokensInLines applies the canonical Boundary-2 accounting to a region
// measured directly against a pre-split line table — the linear-time
// equivalent of EstimateRegionTokens(source, r) without re-splitting source
// per call.
func regionTokensInLines(lines [][]byte, r Region) int {
	if r.StartLine < 1 {
		r.StartLine = 1
	}
	if r.EndLine > len(lines) {
		r.EndLine = len(lines)
	}
	if r.StartLine > r.EndLine {
		return 0
	}
	n := 0
	for i := r.StartLine - 1; i < r.EndLine; i++ {
		n += len(lines[i]) + 1
	}
	return regionTokens(n)
}

// regionTokens applies the canonical Boundary-2 accounting to a raw byte
// count: bytes/4 × FullRewriteTokenMultiplier. It mirrors
// EstimateRegionTokens for byte counts that have not (yet) become a Region.
func regionTokens(n int) int {
	return EstimateTokens(n) * execution.FullRewriteTokenMultiplier
}

// subTaskKind selects the mutation contract label of one grouped unit: any
// group containing a line-sliced fragment is bound to the explicit line
// intervals it was cut into, so it carries SEARCH_REPLACE_BOUNDED_LINES.
func subTaskKind(defaultKind SplitKind, group []Section) SplitKind {
	for _, s := range group {
		if s.BoundedLines {
			return SplitBoundedLines
		}
	}
	return defaultKind
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
