package verifier

import (
	"sort"
	"strings"
)

// maxRemovalIntents caps the removal intents distilled from one objective. An
// objective asking for more distinct removals than this is not expressible as
// a bounded audit contract — the excess is dropped, never invented.
const maxRemovalIntents = 16

// maxRemovalTokenLen bounds one extracted token.
const maxRemovalTokenLen = 64

// IntentSpec is the machine-checkable distillation of the user objective the
// DAG decomposed. It is intentionally SMALL: only claims that admit a
// deterministic audit travel here. Anything fuzzier stays prose in
// Objective and is surfaced as evidence, never asserted.
type IntentSpec struct {
	// Objective is the bounded original user prompt (audit evidence).
	Objective string
	// Target is the workspace-relative file the audit covers.
	Target string
	// Removals lists every identity the objective EXPLICITLY asked to
	// remove. Each must strictly reduce its occurrence count between the
	// pre- and post-DAG states.
	Removals []RemovalIntent
}

// RemovalIntent is one explicit removal request.
type RemovalIntent struct {
	// Token is the identity requested for removal ("sidebar", "hero").
	Token string
	// Kind resolves ambiguity: KindID ("#hero"), KindClass (".sidebar") or
	// KindAny for bare quoted tokens (match id, class or tag).
	Kind string
}

// String renders the compact intent line ("remove .sidebar").
func (r RemovalIntent) String() string {
	switch r.Kind {
	case KindID:
		return "remove #" + r.Token
	case KindClass:
		return "remove ." + r.Token
	default:
		return `remove "` + r.Token + `"`
	}
}

// occurrences counts how many times the intent's identity appears in ONE
// document state: definitions plus usage sites (id/class) or elements of
// that tag name. The SAME formula runs on both sides, so the comparison is
// always apples-to-apples.
func (r RemovalIntent) occurrences(defs definitionSet, refs map[defKey][]int, root *DOMNode) int {
	count := 0
	match := func(kind string) bool { return r.Kind == KindAny || r.Kind == kind }
	for k, d := range defs {
		if k.token == r.Token && match(k.kind) {
			count += d.count
			count += len(refs[k])
		}
	}
	if r.Kind == KindTag || r.Kind == KindAny {
		root.Walk(func(n *DOMNode) {
			if n.Tag != DocumentTag && strings.EqualFold(n.Tag, r.Token) {
				count++
			}
		})
	}
	return count
}

// removalVerbs are the word boundaries that mark a sentence as carrying an
// explicit removal request. Matching is lowercase word-boundary based and
// deterministic.
var removalVerbs = []string{
	"remove", "delete", "drop", "strip", "purge",
	"eliminate", "clean up", "get rid of",
}

// ExtractRemovalIntents distills explicit removal requests out of an
// objective. It splits the prompt into sentence-like chunks, keeps chunks
// that contain a removal verb, and extracts bounded identities from them:
//
//	#id tokens      → KindID
//	.class tokens   → KindClass
//	"quoted tokens" → KindAny
//
// The extraction is conservative: it NEVER invents removals for objectives
// without both a removal verb and an extractable identity. Results are
// deduplicated and capped at maxRemovalIntents.
func ExtractRemovalIntents(objective string) []RemovalIntent {
	var out []RemovalIntent
	seen := map[RemovalIntent]bool{}
	add := func(r RemovalIntent) {
		if r.Token == "" || len(r.Token) > maxRemovalTokenLen || seen[r] {
			return
		}
		if len(out) >= maxRemovalIntents {
			return
		}
		seen[r] = true
		out = append(out, r)
	}
	// Sentence splitting deliberately does NOT break on '.': class selectors
	// (.sidebar) live inside removal sentences.
	for _, chunk := range strings.FieldsFunc(objective, func(r rune) bool {
		return r == '\n' || r == ';' || r == '!' || r == '?'
	}) {
		lower := strings.ToLower(chunk)
		if !containsRemovalVerb(lower) {
			continue
		}
		extractFromChunk(chunk, add)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Token < out[j].Token
	})
	return out
}

// containsRemovalVerb reports whether s contains any removal verb on a word
// boundary.
func containsRemovalVerb(s string) bool {
	for _, v := range removalVerbs {
		if containsWord(s, v) {
			return true
		}
	}
	return false
}

// containsWord reports whether needle occurs in haystack on word boundaries.
func containsWord(haystack, needle string) bool {
	for i := 0; ; {
		idx := strings.Index(haystack[i:], needle)
		if idx < 0 {
			return false
		}
		at := i + idx
		before := at == 0 || !isNameChar(rune(haystack[at-1])) && haystack[at-1] != '.'
		end := at + len(needle)
		after := end >= len(haystack) || !isNameChar(rune(haystack[end]))
		if before && after {
			return true
		}
		i = at + 1
	}
}

// extractFromChunk pulls #id, .class and "quoted" identities out of one
// removal-verb chunk, left to right. Quoted tokens become KindAny: the quote
// marks carry no id/class signal, so every definition kind is audited.
func extractFromChunk(chunk string, add func(RemovalIntent)) {
	for i := 0; i < len(chunk); i++ {
		switch c := chunk[i]; c {
		case '#', '.':
			// A selector marker only counts on a token boundary: inside an
			// identifier or path (@index.html, file.go) it is prose, not an
			// identity.
			if i > 0 && isNameChar(rune(chunk[i-1])) {
				continue
			}
			if tok := identAt(chunk, i+1); tok != "" {
				kind := KindID
				if c == '.' {
					kind = KindClass
				}
				add(RemovalIntent{Token: tok, Kind: kind})
				i += len(tok)
			}
		case '"', '\'', '`':
			end := strings.IndexByte(chunk[i+1:], c)
			if end < 0 {
				i = len(chunk) // unterminated quote: nothing more to extract
				break
			}
			tok := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(
				strings.TrimSpace(chunk[i+1:i+1+end]), "#"), ""))
			if tok == "" || len(tok) > maxRemovalTokenLen {
				i += end + 1
				break
			}
			add(RemovalIntent{Token: tok, Kind: KindAny})
			i += end + 1
		}
	}
}

// identAt extracts the identifier starting at index i of s.
func identAt(s string, i int) string {
	if i >= len(s) {
		return ""
	}
	j := i
	for j < len(s) && isNameChar(rune(s[j])) {
		j++
	}
	return s[i:j]
}
