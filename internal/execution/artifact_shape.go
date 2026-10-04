package execution

// ── ARTIFACT-SHAPE TRUST BOUNDARY ───────────────────────────────────────────
//
// The Phase 14 invariant is that model output is UNTRUSTED: a ComputeResult is
// interpreted, validated and authorized before it can become a mutation. The
// syntax validators enforce "is this well-formed?". They cannot enforce "is this
// a FILE?", because a conversational answer is well-formed in several target
// languages simultaneously:
//
//	a sentence is a syntactically valid HTML body
//	a sentence is a syntactically inert stylesheet
//	a sentence is a syntactically inert script
//
// With only a syntax gate, a model replying "Sure! I have redesigned your
// portfolio page" against an index.html target produced a real, approvable
// patch candidate that would have replaced the page with prose. The approval
// gate would then have presented a mutation derived from a chat reply — the
// ComputeResult → mutation shortcut the whole authority model exists to forbid.
//
// The fix is deliberately small and deliberately structural. A response must
// carry at least ONE token belonging to the target's own language. It does not
// have to be good code, complete, or even correct; it has to BE the artifact
// kind the runtime asked for. Everything above this line stays where it was:
// syntax validation, AST audit, symbol baseline, scope checks.

import (
	"path/filepath"
	"strconv"
	"strings"
)

// targetStructure is the minimal structural signature one language must carry
// for a response to plausibly BE a file of that language.
type targetStructure struct {
	// name is the human-readable language name used in rejection messages.
	name string
	// markers are byte sequences that cannot occur in ordinary prose about the
	// task. At least one must be present.
	markers []string
}

// targetStructures maps an extension onto the structural signature its language
// requires. Entries are deliberately minimal: one unambiguous token per
// language, chosen so that a chat reply cannot satisfy it while a real file
// essentially always does.
//
// A language absent from this table is NOT gated on shape — an unregistered or
// uncommon language must not have its artifacts rejected by a heuristic this
// crude. "Unknown shape" means "shape cannot be judged here", never "rejected".
var targetStructures = map[string]targetStructure{
	".html":  {name: "HTML", markers: []string{"<"}},
	".htm":   {name: "HTML", markers: []string{"<"}},
	".xhtml": {name: "HTML", markers: []string{"<"}},

	".css":  {name: "CSS", markers: []string{"{"}},
	".scss": {name: "CSS", markers: []string{"{"}},
	".sass": {name: "CSS", markers: []string{"{"}},
	".less": {name: "CSS", markers: []string{"{"}},

	".js":  {name: "JavaScript", markers: []string{";", "{", "=>", "function", "const ", "let ", "var "}},
	".mjs": {name: "JavaScript", markers: []string{";", "{", "=>", "function", "const ", "let ", "var "}},
	".cjs": {name: "JavaScript", markers: []string{";", "{", "=>", "function", "const ", "let ", "var "}},
	".jsx": {name: "JavaScript", markers: []string{";", "{", "=>", "function", "const ", "let ", "var "}},
	".ts":  {name: "TypeScript", markers: []string{";", "{", ":", "=>", "function", "const ", "let ", "var ", "interface ", "type "}},
	".tsx": {name: "TypeScript", markers: []string{";", "{", ":", "=>", "function", "const ", "let ", "var ", "interface ", "type "}},

	".go":   {name: "Go", markers: []string{"package ", "func ", ":=", "}"}},
	".py":   {name: "Python", markers: []string{"def ", "class ", "import ", ":\n", "print("}},
	".rb":   {name: "Ruby", markers: []string{"def ", "end", "class ", "module ", "require "}},
	".rs":   {name: "Rust", markers: []string{"fn ", "let ", "use ", "impl ", ";"}},
	".java": {name: "Java", markers: []string{"class ", "public ", "private ", "void ", ";"}},
	".json": {name: "JSON", markers: []string{"{", "["}},
	".sql":  {name: "SQL", markers: []string{"SELECT", "select", "FROM", "from", "CREATE", "create", ";"}},
}

// structureFor returns the structural signature for a target, or ok=false when
// the target's language has no shape rule.
func structureFor(target string) (targetStructure, bool) {
	ext := strings.ToLower(filepath.Ext(target))
	s, ok := targetStructures[ext]
	return s, ok
}

// carriesTargetStructure reports whether content plausibly IS a file of the
// target's language rather than a description of one.
//
// A target with no shape rule always passes: the gate may only REJECT content it
// can positively identify as a non-artifact, never content it merely fails to
// recognise.
func carriesTargetStructure(target, content string) bool {
	shape, ok := structureFor(target)
	if !ok {
		return true
	}
	if strings.TrimSpace(content) == "" {
		return false
	}
	for _, marker := range shape.markers {
		if strings.Contains(content, marker) {
			return true
		}
	}
	return false
}

// targetLanguageName names the target's language for a rejection message, or
// "source" when no shape rule applies.
func targetLanguageName(target string) string {
	if shape, ok := structureFor(target); ok {
		return shape.name
	}
	return "source"
}

// summarizeArtifactRejection renders a bounded, single-line preview of a
// rejected response so a retry can be told WHAT it produced. It never renders a
// whole model response into a log line, and it never renders file content —
// only the shape of the refusal.
func summarizeArtifactRejection(content string) string {
	const maxPreview = 120
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "the response was empty"
	}
	collapsed := strings.Join(strings.Fields(trimmed), " ")
	if len(collapsed) <= maxPreview {
		return strconv.Quote(collapsed)
	}
	return strconv.Quote(collapsed[:maxPreview]) + "…"
}
