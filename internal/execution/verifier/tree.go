// Package verifier implements the POST-DAG GLOBAL STRUCTURAL VERIFIER.
//
// Sub-tasks execute under strict region isolation: each unit's artifact is
// gated against its own window only (Boundaries 3–5). No per-unit gate can
// see regressions that exist only in the AGGREGATE — st-1 removing a CSS
// rule while st-4 keeps the elements that rule styled passes every unit
// boundary and still breaks the document.
//
// VerifyGlobalObjective closes that gap. After the last sub-task lands (and
// before the DAG may claim completion) it audits the WHOLE mutated document
// against the pre-DAG baseline and the machine-checkable intent:
//
//   - syntax remains valid: the mutated document still parses cleanly;
//   - no orphaned references were INTRODUCED: a token whose definition was
//     removed must lose its consumers too, and vice versa;
//   - requested removals actually reduced dead nodes: every removal the
//     objective explicitly asked for strictly reduces token occurrences.
//
// A failed audit overrides the DAG lifecycle to OBJECTIVE_UNRESOLVED and
// routes the decision back to awaiting_human — a false DAG success is
// architecturally impossible.
package verifier

import (
	"github.com/PizenLabs/izen/internal/execution/planner"
)

// DocumentTag is the synthetic identity of the audit-tree root. Every parsed
// document becomes one root node whose children are the scanned top-level
// elements, so callers always handle exactly ONE tree per document state.
const DocumentTag = "#document"

// DOMNode is one node of the verifier's view of a document topology. It
// carries the same structural identity the planner's Lea scanner produces
// (tag, id, classes, inclusive line span) linked by POINTERS instead of flat
// slice indexes, plus the document-level context the global audit needs —
// held only on the synthesized root.
type DOMNode struct {
	// Tag is the lower-cased element name ("section", "style") or the
	// component name for non-HTML scans. The root is DocumentTag.
	Tag string
	// ID is the element's id attribute ("" when absent).
	ID string
	// Classes lists the element's class tokens in declaration order.
	Classes []string
	// StartLine / EndLine bound the node inclusively (1-indexed).
	StartLine int
	EndLine   int
	// Children lists the contained nodes in document order.
	Children []*DOMNode

	// Format is the canonical format label of the scanned document ("html",
	// "jsx", "go_template"). Root-only.
	Format string
	// Scanned reports whether the format carries a Lea structural scanner.
	// A false value degrades the audit to pure structural sanity checks —
	// never a failure verdict on an unscannable format. Root-only.
	Scanned bool
	// Malformed reports that parsing recovered from broken structure
	// (mismatched or unclosed tags). On the MUTATED side this fails the
	// audit outright. Root-only.
	Malformed bool
	// Lines holds the raw document source split into newline-terminated
	// lines. It is the evidence base for reference scanning. Root-only.
	Lines [][]byte
}

// Parse builds the audit tree of ONE document state: it runs the read-only
// Lea structural scan for the target's format and attaches the raw source
// lines to the synthesized root. Formats without a scanner yield an
// unscanned root (Scanned=false); the audit then fail-opens.
func Parse(target string, source []byte) *DOMNode {
	root := &DOMNode{Tag: DocumentTag, Format: leaFormat(target)}
	rep := planner.LeaStructuralScan(target, source)
	if rep == nil {
		return root
	}
	root.Scanned = true
	root.Malformed = rep.LowConfidence
	root.Lines = splitLines(source)
	for i := range rep.Nodes {
		if rep.Nodes[i].Parent < 0 {
			root.Children = append(root.Children, buildSubtree(&rep.Nodes[i], rep))
		}
	}
	return root
}

// buildSubtree converts one flat scanned node and its descendants into the
// pointer-linked audit representation.
func buildSubtree(n *planner.DOMNode, rep *planner.LeaScanReport) *DOMNode {
	out := &DOMNode{
		Tag:       n.Tag,
		ID:        n.ID,
		Classes:   append([]string(nil), n.Classes...),
		StartLine: n.StartLine,
		EndLine:   n.EndLine,
	}
	for _, c := range n.Children {
		if c >= 0 && c < len(rep.Nodes) {
			out.Children = append(out.Children, buildSubtree(&rep.Nodes[c], rep))
		}
	}
	return out
}

// Walk visits the node and every descendant in document order.
func (n *DOMNode) Walk(fn func(node *DOMNode)) {
	if n == nil || fn == nil {
		return
	}
	fn(n)
	for _, c := range n.Children {
		c.Walk(fn)
	}
}

// leaFormat mirrors the planner's format detection for the unscanned-root
// label (best effort; the planner remains the authority).
func leaFormat(target string) string {
	switch ext(target) {
	case ".html", ".htm", ".xhtml":
		return "html"
	case ".jsx", ".tsx":
		return "jsx"
	case ".gohtml", ".tmpl", ".gotmpl", ".gotemplate":
		return "go_template"
	default:
		return ""
	}
}

func ext(target string) string {
	for i := len(target) - 1; i >= 0; i-- {
		if target[i] == '.' {
			return target[i:]
		}
	}
	return ""
}

// splitLines splits source into newline-terminated lines (the planner's
// convention: the \n stays attached).
func splitLines(source []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range source {
		if b == '\n' {
			out = append(out, source[start:i+1])
			start = i + 1
		}
	}
	if start < len(source) {
		out = append(out, source[start:])
	}
	return out
}
