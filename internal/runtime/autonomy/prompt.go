package autonomy

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/execution/planner"
)

// ── Structural Context Compressor ───────────────────────────────────────────
//
// Sub-task prompts must NEVER carry the full raw source file: a decomposed
// objective exists precisely because the target exceeded one generation's
// budget, and echoing those bytes back as context re-inflates every unit with
// the very payload the decomposition split up — while burying the unit's own
// scope under noise and triggering false NO_CHANGES_REQUIRED claims from
// locally blind models.
//
// CompressedStructuralContext replaces the dump with what a sub-task model
// actually needs to orient itself:
//
//   - DOCUMENT TOPOLOGY — the Lea skeleton tree (tag identities, id/class,
//     line regions) with parent/sibling relations, so "lines 38–76" becomes
//     "<section#hero> hero, between header and footer";
//   - ACTIVE REFERENCES — where the scope's ids/classes are used elsewhere
//     (style rules, anchors, script hooks), the dependency edges a rename or
//     removal must respect;
//   - TARGETED EVIDENCE — the Lea structural findings (unused elements,
//     duplicate selectors, dead code paths) that overlap THIS sub-task's
//     scope, never another unit's.
//
// The context is bounded by construction (MaxCompressedContextBytes ≈ 512
// tokens regardless of document size): the token footprint shrinks from
// O(document) to O(topology).

// Compression budgets. The skeleton is capped per render so a pathological
// document (thousands of sibling nodes) degrades by elision, never by size.
const (
	// MaxCompressedContextBytes bounds the rendered compressed context.
	// ~2048 bytes ≈ 512 tokens — a fixed tax independent of document size.
	MaxCompressedContextBytes = 2048
	// maxSkeletonLines caps the topology tree rendering.
	maxSkeletonLines = 28
	// maxScopeReferences caps the active-reference lines for the scope.
	maxScopeReferences = 6
	// maxScopeFindings caps the targeted evidence lines for the scope.
	maxScopeFindings = 6
)

// CompressedStructuralContext is the bounded topology-first payload injected
// into one sub-task's prompt in place of the raw source file.
type CompressedStructuralContext struct {
	// Target is the workspace-relative document being decomposed.
	Target string
	// TotalLines is the document's line count (size without the bytes).
	TotalLines int
	// ScopeID is the owning sub-task identity ("st-2").
	ScopeID string
	// Scope is the sub-task's assigned inclusive line window.
	Scope planner.Region
	// ScopeLabel is the structural identity of the node covering the scope
	// ("<section#hero> hero") when the topology names one.
	ScopeLabel string
	// Skeleton is the bounded topology tree rendering (one line per node).
	Skeleton []string
	// Relations describe the scope's parent/sibling position.
	Relations []string
	// References describe active id/class usage touching the scope.
	References []string
	// Evidence lists targeted structural findings overlapping the scope.
	Evidence []string
	// Truncated reports that the skeleton was elided under its cap.
	Truncated bool
}

// buildCompressedStructuralContext compresses the read-only Lea scan of the
// CURRENT target bytes down to the sub-task's orientation payload. It returns
// nil when the format has no Lea scanner or produced no topology — the caller
// then proceeds with the plain scoped prompt (graceful degradation, never a
// failure).
func buildCompressedStructuralContext(target string, source []byte, st planner.SubTask) *CompressedStructuralContext {
	scan := planner.LeaStructuralScan(target, source)
	if scan == nil || len(scan.Nodes) == 0 {
		return nil
	}
	c := &CompressedStructuralContext{
		Target:     target,
		TotalLines: scan.TotalLines,
		ScopeID:    st.ID,
		Scope:      st.Region,
		ScopeLabel: scopeNodeLabel(scan, st.Region),
	}
	c.Skeleton, c.Truncated = renderSkeleton(scan, st.Region)
	c.Relations = renderScopeRelations(scan, st.Region)
	c.References = renderScopeReferences(scan, st.Region)
	c.Evidence = renderScopeEvidence(scan, st.Region)
	return c
}

// Render emits the compact prompt block. Output is hard-bounded by
// MaxCompressedContextBytes: the skeleton degrades first (elision marker),
// then relations/references/evidence, never the scope statement.
func (c *CompressedStructuralContext) Render() string {
	if c == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[STRUCTURAL CONTEXT %s — compressed topology of %s (%d lines); the raw file is intentionally NOT included]\n",
		c.ScopeID, c.Target, c.TotalLines)
	fmt.Fprintf(&b, "Assigned scope: %s (%s)\n", c.Scope, c.ScopeLabel)
	if len(c.Skeleton) > 0 {
		b.WriteString("DOCUMENT SKELETON (indentation = nesting):\n")
		for _, l := range c.Skeleton {
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}
	if len(c.Relations) > 0 {
		b.WriteString("PARENT/SIBLING RELATIONS:\n")
		for _, l := range c.Relations {
			b.WriteString("- ")
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}
	if len(c.References) > 0 {
		b.WriteString("ACTIVE REFERENCES (usages your edit must not break):\n")
		for _, l := range c.References {
			b.WriteString("- ")
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}
	if len(c.Evidence) > 0 {
		b.WriteString("TARGETED STRUCTURAL EVIDENCE (this scope):\n")
		for _, l := range c.Evidence {
			b.WriteString("◆ ")
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}
	out := b.String()
	if len(out) > MaxCompressedContextBytes {
		// Hard byte ceiling: truncate at a rune-safe boundary and mark it.
		cut := MaxCompressedContextBytes
		for cut > 0 && !utf8RuneStart(out[cut]) {
			cut--
		}
		out = out[:cut] + "\n…(topology truncated)\n"
	}
	return out
}

// EstimateTokens reports the conservative token footprint of the rendered
// context (bytes/4, the same accounting as Boundary 2).
func (c *CompressedStructuralContext) EstimateTokens() int {
	return len(c.Render()) / 4
}

// scopeNodeLabel finds the most specific topology node covering the scope's
// opening line and renders its structural identity.
func scopeNodeLabel(scan *planner.LeaScanReport, r planner.Region) string {
	best := -1
	for i := range scan.Nodes {
		n := &scan.Nodes[i]
		if n.StartLine <= r.StartLine && r.StartLine <= n.EndLine {
			if best < 0 || deeper(scan.Nodes[i], scan.Nodes[best]) {
				best = i
			}
		}
	}
	if best < 0 {
		return "(no enclosing structural node)"
	}
	return planner.NodeIdentity(scan.Nodes[best])
}

// deeper prefers the deeper node; ties break toward the earlier node.
func deeper(a, b planner.DOMNode) bool {
	if a.Depth != b.Depth {
		return a.Depth > b.Depth
	}
	return false
}

// renderSkeleton draws the topology forest with tree glyphs, marking the
// node(s) overlapping the scope. Long forests degrade under maxSkeletonLines
// with an explicit elision marker so the shape stays truthful.
func renderSkeleton(scan *planner.LeaScanReport, r planner.Region) ([]string, bool) {
	lines := make([]string, 0, len(scan.Nodes))
	var walk func(i int, prefix string)
	walk = func(i int, prefix string) {
		if len(lines) >= maxSkeletonLines {
			return
		}
		n := &scan.Nodes[i]
		marker := ""
		if n.StartLine <= r.EndLine && r.StartLine <= n.EndLine {
			marker = "  ◄ ASSIGNED SCOPE"
		}
		lines = append(lines, fmt.Sprintf("%s%s [%d–%d]%s", prefix, skeletonLabel(*n), n.StartLine, n.EndLine, marker))
		for k, ch := range n.Children {
			childPrefix := prefix + "│  "
			if k == len(n.Children)-1 {
				childPrefix = prefix + "   "
			}
			walk(ch, childPrefix)
		}
	}
	for i := range scan.Nodes {
		if scan.Nodes[i].Parent == -1 {
			walk(i, "")
		}
	}
	truncated := false
	if len(lines) >= maxSkeletonLines && countNodes(scan) > maxSkeletonLines {
		truncated = true
		lines = append(lines[:maxSkeletonLines-1], fmt.Sprintf("… +%d more nodes elided", countNodes(scan)-maxSkeletonLines+1))
	}
	return lines, truncated
}

// countNodes returns the flattened node count.
func countNodes(scan *planner.LeaScanReport) int { return len(scan.Nodes) }

// skeletonLabel renders one node's bounded identity for the tree
// (first class token only, so long class lists cannot bloat the skeleton).
func skeletonLabel(n planner.DOMNode) string {
	sel := n.Tag
	if n.ID != "" {
		return sel + "#" + n.ID
	}
	if len(n.Classes) > 0 {
		return sel + "." + n.Classes[0]
	}
	return sel
}

// renderScopeRelations describes where the scope sits among its siblings.
func renderScopeRelations(scan *planner.LeaScanReport, r planner.Region) []string {
	idx := scopeNodeIndex(scan, r)
	if idx < 0 {
		return nil
	}
	n := &scan.Nodes[idx]
	var out []string
	if n.Parent >= 0 {
		p := &scan.Nodes[n.Parent]
		siblings := p.Children
		pos := -1
		for k, sib := range siblings {
			if sib == idx {
				pos = k
				break
			}
		}
		desc := fmt.Sprintf("%s sits inside %s", planner.NodeIdentity(*n), planner.NodeIdentity(*p))
		switch {
		case pos > 0 && pos < len(siblings)-1:
			desc += fmt.Sprintf(", between %s and %s",
				planner.NodeIdentity(scan.Nodes[siblings[pos-1]]),
				planner.NodeIdentity(scan.Nodes[siblings[pos+1]]))
		case pos > 0:
			desc += fmt.Sprintf(", after %s", planner.NodeIdentity(scan.Nodes[siblings[pos-1]]))
		case pos < len(siblings)-1:
			desc += fmt.Sprintf(", before %s", planner.NodeIdentity(scan.Nodes[siblings[pos+1]]))
		default:
			desc += " as its only child"
		}
		out = append(out, desc)
	}
	return out
}

// renderScopeReferences lists the active usages of the scope node's id/class
// tokens (bounded to maxScopeReferences lines).
func renderScopeReferences(scan *planner.LeaScanReport, r planner.Region) []string {
	idx := scopeNodeIndex(scan, r)
	tokens := map[string]string{} // name -> kind
	if idx >= 0 {
		n := &scan.Nodes[idx]
		if n.ID != "" {
			tokens[n.ID] = "id"
		}
		for _, cls := range n.Classes {
			tokens[cls] = "class"
		}
	}
	if len(tokens) == 0 {
		return nil
	}
	var out []string
	for _, ref := range scan.References {
		if len(out) >= maxScopeReferences {
			break
		}
		kind, ok := tokens[ref.Name]
		if !ok || len(ref.UsedAt) == 0 {
			continue
		}
		out = append(out, fmt.Sprintf("%s%s used at %s",
			refPrefix(kind), ref.Name, joinRefLines(ref.UsedAt)))
	}
	return out
}

// refPrefix renders the selector prefix of a reference kind.
func refPrefix(kind string) string {
	if kind == "id" {
		return "#"
	}
	return "."
}

// joinRefLines bounds a usage-line list to its first few sites.
func joinRefLines(lines []int) string {
	const maxSites = 4
	parts := make([]string, 0, min(len(lines), maxSites))
	for i, l := range lines {
		if i == maxSites {
			parts = append(parts, fmt.Sprintf("+%d more", len(lines)-maxSites))
			break
		}
		parts = append(parts, fmt.Sprintf("line %d", l))
	}
	return strings.Join(parts, ", ")
}

// renderScopeEvidence selects the findings whose regions overlap this
// sub-task's scope (bounded to maxScopeFindings). Findings elsewhere in the
// document belong to other units and are withheld — targeted means targeted.
func renderScopeEvidence(scan *planner.LeaScanReport, r planner.Region) []string {
	var out []string
	for _, f := range scan.Findings {
		if len(out) >= maxScopeFindings {
			break
		}
		if overlaps(f.Region, r) {
			out = append(out, fmt.Sprintf("%s: %s [%s]", f.Kind, f.Detail, f.Region))
		}
	}
	return out
}

// overlaps reports whether two inclusive regions intersect.
func overlaps(a, b planner.Region) bool {
	return a.StartLine <= b.EndLine && b.StartLine <= a.EndLine
}

// scopeNodeIndex locates the deepest node whose span contains the scope's
// opening line (-1 when none does).
func scopeNodeIndex(scan *planner.LeaScanReport, r planner.Region) int {
	best := -1
	for i := range scan.Nodes {
		n := &scan.Nodes[i]
		if n.StartLine <= r.StartLine && r.StartLine <= n.EndLine {
			if best < 0 || deeper(*n, scan.Nodes[best]) {
				best = i
			}
		}
	}
	return best
}

// utf8RuneStart reports whether b begins a UTF-8 rune.
func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }
