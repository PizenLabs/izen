package verifier

import (
	"sort"
	"strings"
)

// ── Active-reference scanning ───────────────────────────────────────────────
//
// The audit needs to know where id/class tokens are USED: CSS selectors
// (#id / .cls / .a.b chains), href anchors (#id), url(#id) fragments and
// quoted tokens inside scripts. Definition sites — the elements carrying the
// attribute — are NOT usage sites: an element cannot cite itself, and its
// class attribute would otherwise masquerade as a quoted script lookup.
//
// The heuristics intentionally mirror the planner's Lea reference collector
// so every verdict traces back to the same evidence the decomposition context
// showed the sub-task models.

// ReferenceKind enumerates the audited identity kinds.
const (
	KindID    = "id"
	KindClass = "class"
	KindTag   = "tag"
	KindAny   = "any"
)

// defKey identifies one identity across both compared states.
type defKey struct {
	token string
	kind  string // KindID | KindClass
}

// display renders the CSS-selector shorthand of a key ("#nav", ".card").
func (k defKey) display() string {
	if k.kind == KindID {
		return "#" + k.token
	}
	return "." + k.token
}

// definition is the per-token definition aggregate across one document.
type definition struct {
	count     int
	firstLine int
}

// definitionSet indexes one document's id/class bindings by identity.
type definitionSet map[defKey]*definition

func (m definitionSet) add(token, kind string, line int) {
	k := defKey{token, kind}
	d, ok := m[k]
	if !ok {
		m[k] = &definition{count: 1, firstLine: line}
		return
	}
	d.count++
}

// collectDefinitions walks the tree and aggregates every id/class binding.
func collectDefinitions(root *DOMNode) definitionSet {
	out := definitionSet{}
	root.Walk(func(n *DOMNode) {
		if n.Tag == DocumentTag {
			return
		}
		if n.ID != "" {
			out.add(n.ID, KindID, n.StartLine)
		}
		for _, c := range n.Classes {
			out.add(c, KindClass, n.StartLine)
		}
	})
	return out
}

// defSiteLines records EVERY line on which a token is bound by an element
// (the element's start line carries its id/class attributes). These lines are
// excluded from reference scanning.
func defSiteLines(root *DOMNode) map[defKey]map[int]bool {
	out := map[defKey]map[int]bool{}
	record := func(token, kind string, line int) {
		k := defKey{token, kind}
		if out[k] == nil {
			out[k] = map[int]bool{}
		}
		out[k][line] = true
	}
	root.Walk(func(n *DOMNode) {
		if n.Tag == DocumentTag {
			return
		}
		if n.ID != "" {
			record(n.ID, KindID, n.StartLine)
		}
		for _, c := range n.Classes {
			record(c, KindClass, n.StartLine)
		}
	})
	return out
}

// candidateKeys unions the identities of two states so BOTH sides are scanned
// for the SAME candidate set — a dangling reference to a removed definition
// must still be found in the mutated document even though nothing there
// defines the token anymore.
func candidateKeys(a, b definitionSet) []defKey {
	seen := map[defKey]bool{}
	var out []defKey
	for k := range a {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for k := range b {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].token != out[j].token {
			return out[i].token < out[j].token
		}
		return out[i].kind < out[j].kind
	})
	return out
}

// scanReferences finds the usage sites of every candidate key in the given
// document lines, skipping each token's own definition-site lines.
func scanReferences(lines [][]byte, candidates []defKey, defSites map[defKey]map[int]bool) map[defKey][]int {
	out := map[defKey][]int{}
	if len(lines) == 0 || len(candidates) == 0 {
		return out
	}
	for _, k := range candidates {
		for i := range lines {
			lineNo := i + 1
			if defSites[k][lineNo] {
				continue // never cite the definition itself
			}
			if referencedInLine(string(lines[i]), k.token, k.kind) {
				out[k] = append(out[k], lineNo)
			}
		}
	}
	return out
}

// referencedInLine reports whether the token is consumed on this line by a
// selector, anchor, URL fragment or quoted script lookup.
func referencedInLine(s, token, kind string) bool {
	if !strings.Contains(s, token) {
		return false
	}
	switch kind {
	case KindID:
		// Anchor and SVG-fragment forms: href="#top", url(#top).
		if strings.Contains(s, "\"#"+token) || strings.Contains(s, "'#"+token) ||
			strings.Contains(strings.ToLower(s), "url(#"+token+")") {
			return true
		}
	case KindClass:
		// Class selectors may chain (.card.active): require a '.' boundary
		// that does not belong to the defining tag itself.
		from := 0
		for {
			idx := strings.Index(s[from:], "."+token)
			if idx < 0 {
				break
			}
			at := from + idx
			if at > 0 && !isNameChar(rune(s[at-1])) &&
				s[at-1] != '<' && s[at-1] != '"' && s[at-1] != '\'' {
				return true
			}
			from = at + 1
			if from >= len(s) {
				break
			}
		}
	}
	// Script/template lookups: getElementById("x"), addEventListener hooks,
	// template data bindings — any quoted occurrence counts as a consumer.
	for _, q := range []byte{'"', '\'', '`'} {
		if strings.Contains(s, string(q)+token+string(q)) {
			return true
		}
	}
	return false
}

// isNameChar reports whether r may appear inside a compound identifier
// (letters, digits, dash, underscore, colon).
func isNameChar(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '-' || r == '_' || r == ':':
		return true
	default:
		return false
	}
}
