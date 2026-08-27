package verifier

import (
	"fmt"
	"sort"
	"strings"
)

// ── The global objective audit ──────────────────────────────────────────────
//
// VerifyGlobalObjective is the post-DAG gate between "every sub-task applied"
// and "the objective is satisfied". It compares the PRE-DAG baseline tree
// against the POST-DAG mutated tree and asserts the structural invariants no
// per-sub-task boundary can see:
//
//	S1  SYNTAX REMAINS VALID — the mutated document still parses cleanly
//	    (a parse that recovered from broken structure fails the audit).
//	S2  NO ORPHANED REFERENCES WERE INTRODUCED — for every id/class token:
//	      (a) a definition removed by the DAG must not leave consumers
//	          behind (dangling_reference), and
//	      (b) consumers removed by the DAG must not leave a now-unreachable
//	          definition behind when the token existed before (orphaned_
//	          definition — the st-1-removes-CSS-st-4-still-uses regression).
//	    New tokens introduced by the mutation are exempt on both sides.
//	S3  REQUESTED REMOVALS ACTUALLY REDUCED DEAD NODES — every identity the
//	    intent explicitly asked to remove strictly reduces its occurrence
//	    count between the two states.
//
// The audit is deterministic, side-effect free, and FAILS CLOSED: any nil
// input or unverifiable state is an unresolved verdict, never a pass.
type Status string

const (
	// StatusVerified: the mutated document satisfies every structural
	// invariant of the global audit.
	StatusVerified Status = "OBJECTIVE_VERIFIED"
	// StatusUnresolved: the audit found at least one structural violation.
	// Callers must override the DAG lifecycle to OBJECTIVE_UNRESOLVED and
	// route the decision to awaiting_human.
	StatusUnresolved Status = "OBJECTIVE_UNRESOLVED"
)

// Canonical failure codes. They travel verbatim into diagnostics and the
// OBJECTIVE_UNRESOLVED evidence line.
const (
	FailNilTree            = "nil_tree"
	FailSyntaxInvalid      = "syntax_invalid"
	FailTopologyInvalid    = "topology_invalid"
	FailDanglingReference  = "dangling_reference"
	FailOrphanedDefinition = "orphaned_definition"
	FailRemovalNotReduced  = "removal_not_reduced"
)

// Failure is one structural violation with its bounded deterministic detail.
type Failure struct {
	// Code is the canonical failure code.
	Code string
	// Token is the offending identity ("" for document-level failures).
	Token string
	// Detail is the one-line deterministic rationale.
	Detail string
}

// String renders the compact evidence line of one failure.
func (f Failure) String() string {
	if f.Token == "" {
		return fmt.Sprintf("%s: %s", f.Code, f.Detail)
	}
	return fmt.Sprintf("%s %s: %s", f.Code, f.Token, f.Detail)
}

// Stats is the structural fingerprint of one audited document state.
type Stats struct {
	// Nodes counts audited elements (excluding the synthetic root).
	Nodes int
	// Definitions counts id/class bindings across all elements.
	Definitions int
	// References counts usage sites over the union candidate set.
	References int
}

// Verdict is the outcome of one global objective audit.
type Verdict struct {
	// Status is OBJECTIVE_VERIFIED only when Failures is empty.
	Status Status
	// Failures lists every violation in deterministic order
	// (by code, then token, then detail).
	Failures []Failure
	// Base / Mutated are the structural fingerprints of both states.
	Base    Stats
	Mutated Stats
}

// Pass reports whether the audit verified the global objective.
func (v Verdict) Pass() bool {
	return v.Status == StatusVerified && len(v.Failures) == 0
}

// Evidence renders the bounded single-line failure summary carried on
// OBJECTIVE_UNRESOLVED boundaries and plan failure reasons.
func (v Verdict) Evidence() string {
	if len(v.Failures) == 0 {
		return string(StatusVerified)
	}
	parts := make([]string, 0, len(v.Failures))
	for _, f := range v.Failures {
		parts = append(parts, f.String())
	}
	const maxEvidence = 400
	line := strings.Join(parts, "; ")
	if len(line) > maxEvidence {
		line = line[:maxEvidence]
	}
	return line
}

// VerifyGlobalObjective performs the global AST audit comparing the pre-DAG
// baseline tree against the post-DAG mutated tree under the given intent.
// Both trees come from Parse (or an equivalent construction); a nil tree is
// fail-closed. See the package and type documentation for the invariant set.
func VerifyGlobalObjective(baseTree, mutatedTree *DOMNode, intent IntentSpec) Verdict {
	v := Verdict{Status: StatusUnresolved}
	if baseTree == nil || mutatedTree == nil {
		v.Failures = append(v.Failures, Failure{
			Code:   FailNilTree,
			Detail: "global audit requires both the pre- and post-DAG document trees",
		})
		return v
	}

	v.Base = fingerprint(baseTree)
	v.Mutated = fingerprint(mutatedTree)

	// S1 — syntax gate on the MUTATED state. Pre-existing damage in the base
	// is recorded as context but never blamed on this DAG.
	if mutatedTree.Scanned && mutatedTree.Malformed {
		v.Failures = append(v.Failures, Failure{
			Code:   FailSyntaxInvalid,
			Detail: "mutated document fails structural parsing (mismatched or unclosed tags)",
		})
		return v // a broken parse makes every downstream comparison noise
	}
	if detail := topologyFault(mutatedTree); detail != "" {
		v.Failures = append(v.Failures, Failure{Code: FailTopologyInvalid, Detail: detail})
		return v
	}

	defsBase := collectDefinitions(baseTree)
	defsMut := collectDefinitions(mutatedTree)
	candidates := candidateKeys(defsBase, defsMut)
	refsBase := scanReferences(baseTree.Lines, candidates, defSiteLines(baseTree))
	refsMut := scanReferences(mutatedTree.Lines, candidates, defSiteLines(mutatedTree))
	v.Base.References = countRefs(refsBase)
	v.Mutated.References = countRefs(refsMut)

	// S2a — dangling references: a definition the DAG removed must not leave
	// consumers behind in the mutated document.
	for _, k := range candidates {
		dm, definedNow := defsMut[k]
		if definedNow && dm.count > 0 {
			continue
		}
		if sites := refsMut[k]; len(sites) > 0 {
			v.Failures = append(v.Failures, Failure{
				Code:  FailDanglingReference,
				Token: k.display(),
				Detail: fmt.Sprintf("%s was removed but is still referenced at %s",
					k.display(), joinLines(sites)),
			})
		}
	}

	// S2b — orphaned definitions: pre-existing consumers that the DAG removed
	// while their (also pre-existing) definition survived. This is exactly
	// the cross-subtask regression where st-1 deletes a CSS rule that st-4's
	// region still relies on.
	for _, k := range candidates {
		db, existedBefore := defsBase[k]
		dm, definedNow := defsMut[k]
		if !existedBefore || db.count == 0 || !definedNow || dm.count == 0 {
			continue
		}
		if len(refsMut[k]) == 0 && len(refsBase[k]) > 0 {
			v.Failures = append(v.Failures, Failure{
				Code:  FailOrphanedDefinition,
				Token: k.display(),
				Detail: fmt.Sprintf("all references were removed but the definition survives at line %d — styling/hook silently lost",
					dm.firstLine),
			})
		}
	}

	// S3 — requested removals must strictly reduce occurrences. An identity
	// absent from BOTH states is untestable prose (e.g. a quoted phrase on an
	// unscannable format) and never fails the audit.
	for _, r := range intent.Removals {
		before := r.occurrences(defsBase, refsBase, baseTree)
		after := r.occurrences(defsMut, refsMut, mutatedTree)
		if after >= before && before+after > 0 {
			v.Failures = append(v.Failures, Failure{
				Code:   FailRemovalNotReduced,
				Token:  r.String(),
				Detail: fmt.Sprintf("requested removal did not reduce occurrences (%d → %d)", before, after),
			})
		}
	}

	sort.Slice(v.Failures, func(i, j int) bool {
		if v.Failures[i].Code != v.Failures[j].Code {
			return v.Failures[i].Code < v.Failures[j].Code
		}
		if v.Failures[i].Token != v.Failures[j].Token {
			return v.Failures[i].Token < v.Failures[j].Token
		}
		return v.Failures[i].Detail < v.Failures[j].Detail
	})
	if len(v.Failures) == 0 {
		v.Status = StatusVerified
	}
	return v
}

// AuditObjective is the integration entry point: it parses BOTH document
// states for the target's format and runs the global audit.
func AuditObjective(target string, baseSource, mutatedSource []byte, intent IntentSpec) Verdict {
	intent.Target = target
	return VerifyGlobalObjective(Parse(target, baseSource), Parse(target, mutatedSource), intent)
}

// fingerprint computes the structural stats of one document state.
func fingerprint(root *DOMNode) Stats {
	s := Stats{}
	root.Walk(func(n *DOMNode) {
		if n.Tag == DocumentTag {
			return
		}
		s.Nodes++
		if n.ID != "" {
			s.Definitions++
		}
		s.Definitions += len(n.Classes)
	})
	return s
}

// topologyFault returns "" when the tree is structurally sane, else a bounded
// detail naming the first defect: empty tags or inverted/zero spans on a
// scanned tree indicate a corrupt audit input rather than a real document.
func topologyFault(root *DOMNode) string {
	var detail string
	root.Walk(func(n *DOMNode) {
		if detail != "" || n.Tag == DocumentTag {
			return
		}
		switch {
		case strings.TrimSpace(n.Tag) == "":
			detail = fmt.Sprintf("element at line %d carries no tag", n.StartLine)
		case n.StartLine < 1 || n.EndLine < n.StartLine:
			detail = fmt.Sprintf("<%s> has an invalid span (lines %d–%d)", n.Tag, n.StartLine, n.EndLine)
		}
	})
	return detail
}

// countRefs sums usage sites across the reference index.
func countRefs(refs map[defKey][]int) int {
	n := 0
	for _, sites := range refs {
		n += len(sites)
	}
	return n
}

// joinLines renders line numbers compactly ("3, 7, 12").
func joinLines(lines []int) string {
	parts := make([]string, 0, len(lines))
	for _, l := range lines {
		parts = append(parts, fmt.Sprintf("%d", l))
	}
	return strings.Join(parts, ", ")
}
