package execution

// ── EVIDENCE-BOUND SCOPE DERIVATION ─────────────────────────────────────────
//
// TargetResolver answers "is the path the caller STATED a file?". It refuses to
// choose from a scan, because choosing from a scan is how "refactor the project"
// silently becomes a write to whichever file the scan returned first (I13).
//
// This file answers a different, narrower question that the portfolio acceptance
// scenario forces into the open:
//
//	"The objective names no file AND it names ARTIFACT KINDS. Which OBSERVED
//	 files satisfy those kinds?"
//
// That is not candidate ranking. Every path this file returns was READ FROM DISK
// by WorkspaceDiscovery and matched by EXTENSION against a kind the objective
// itself declared. Nothing is guessed, nothing is invented, and a workspace with
// no matching file yields nothing at all — which is the negative test that keeps
// this honest:
//
//	a workspace without index.html must NOT cause IZEN to invent index.html
//
// The derived set is then handed to the EXISTING canonical resolution path
// (TargetResolver.resolveExplicit), so a derived target is bound, digested and
// admission-checked exactly like a stated one. This file adds no second mutation
// authority; it only decides which existing files the objective is about.
//
// The one thing it deliberately does NOT do: pick a subset. If the objective
// declares three kinds and the workspace holds two files for one of them, the
// ambiguity is real and is reported, not resolved by preferring the first.

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// artifactKind is one extension family an objective can name. Kinds are
// identified by the LANGUAGE the objective names ("HTML", "CSS", "JavaScript"),
// never by a filename: "HTML" is a statement about file extensions, and mapping
// it to a specific filename is precisely the invention this file refuses.
type artifactKind struct {
	// Label is the canonical, human-readable kind name for evidence records.
	Label string
	// Extensions are the lowercase extensions (with leading dot) that satisfy
	// the kind. A file satisfies the kind when its extension matches one.
	Extensions []string
}

// declaredArtifactKinds maps the language words an objective may use onto the
// extension families that satisfy them. Every entry is a LANGUAGE token: none of
// them is a filename, and none of them can produce a path that does not exist.
//
// The match is on whole tokens. "js" must not fire on "json", and "go" must not
// fire on "google" — substring matching over prose is how a language hint turns
// into a fabricated target.
var declaredArtifactKinds = []artifactKind{
	{Label: "html", Extensions: []string{".html", ".htm"}},
	{Label: "css", Extensions: []string{".css", ".scss", ".sass", ".less"}},
	{Label: "javascript", Extensions: []string{".js", ".mjs", ".cjs", ".jsx"}},
	{Label: "typescript", Extensions: []string{".ts", ".tsx"}},
	{Label: "python", Extensions: []string{".py"}},
	{Label: "go", Extensions: []string{".go"}},
	{Label: "rust", Extensions: []string{".rs"}},
	{Label: "java", Extensions: []string{".java"}},
	{Label: "ruby", Extensions: []string{".rb"}},
	{Label: "sql", Extensions: []string{".sql"}},
	{Label: "markdown", Extensions: []string{".md", ".markdown"}},
}

// kindVocabulary maps each language token onto its artifactKind. The token list
// is closed and explicit so an unexpected word can never reach the matcher.
var kindVocabulary = map[string]string{
	"html": "html", "htm": "html", "markup": "html",
	"css": "css", "stylesheet": "css", "stylesheets": "css",
	"js": "javascript", "javascript": "javascript",
	"ts": "typescript", "typescript": "typescript",
	"py": "python", "python": "python",
	"go": "go", "golang": "go",
	"rs": "rust", "rust": "rust",
	"java": "java", "kotlin": "java",
	"rb": "ruby", "ruby": "ruby",
	"sql": "sql", "database": "sql",
	"md": "markdown", "markdown": "markdown",
}

// DeclareArtifactKinds returns the canonical kind labels an objective declares,
// deduplicated and deterministically ordered. It is exported because the
// evidence record must name WHICH kinds drove a derivation: a derived target
// whose provenance cannot be explained is indistinguishable from a guess.
func DeclareArtifactKinds(prompt string) []string {
	seen := map[string]bool{}
	var out []string
	for _, token := range proseTokens(prompt) {
		kind, ok := kindVocabulary[token]
		if !ok || seen[kind] {
			continue
		}
		seen[kind] = true
		out = append(out, kind)
	}
	sort.Strings(out)
	return out
}

// proseTokens lowercases the objective and splits it into word tokens, dropping
// punctuation so "HTML," "JS." and "(css)" resolve to the same bare token. The
// token is the unit of matching: nothing longer than one word is ever consulted.
func proseTokens(prompt string) []string {
	return strings.FieldsFunc(strings.ToLower(prompt), isTokenSeparator)
}

// isTokenSeparator reports whether r ends the current word token. Anything that
// is not an ASCII letter or digit separates: a path, a URL or an extension can
// therefore never masquerade as a single bare language token.
func isTokenSeparator(r rune) bool {
	return (r < 'a' || r > 'z') && (r < '0' || r > '9')
}

// DerivationRequest is one scope-derivation request: an objective, the observed
// workspace, and whether the caller already proved targets of its own.
type DerivationRequest struct {
	// Prompt is the raw objective. It supplies the declared artifact kinds and
	// nothing else — never a filename.
	Prompt string
	// Profile is the OBSERVED workspace. Derivation reads candidates from here
	// and from nowhere else; it never stats, scans or guesses.
	Profile WorkspaceProfile
	// StatedTargets is the caller's own proven target set. A non-empty set makes
	// derivation a no-op: an explicit target outranks a derived one.
	StatedTargets []string
}

// Derivation is the complete, evidence-carrying outcome of one derivation pass.
type Derivation struct {
	// Targets is the derived, existing-file set. Empty means "no derivation was
	// possible", which is a valid and common outcome — never an error.
	Targets []string
	// Kinds are the artifact kinds the objective declared, in canonical order.
	Kinds []string
	// Reason explains the verdict in runtime vocabulary, never prompt vocabulary.
	Reason string
	// Derivable reports whether derivation was ATTEMPTED. A caller can tell
	// "nothing to derive" from "derivation refused", which matter differently.
	Derivable bool
}

// DeriveScope binds an objective that names no file to the observed workspace
// files that satisfy the artifact kinds the objective itself declared.
//
// It is a PURE function of the request: it reads the observation it is given,
// touches no filesystem, and returns an empty result rather than a fallback. The
// caller decides what an empty result means (disambiguate, block, or park).
func DeriveScope(req DerivationRequest) Derivation {
	if len(nonBlank(req.StatedTargets)) > 0 {
		return Derivation{
			Reason: "the caller already proved a target set; derivation never overrides a stated target",
		}
	}
	kinds := DeclareArtifactKinds(req.Prompt)
	if len(kinds) == 0 {
		return Derivation{
			Reason: "the objective declares no artifact kind, so there is nothing to derive a target from; naming a file is the only way to proceed",
		}
	}
	candidates := req.Profile.CandidatePaths()
	if len(candidates) == 0 {
		// Derivation WAS attempted: the kinds are declared and the scan ran, it
		// just observed nothing. Marking it attempted is what lets a caller tell
		// "I looked and the workspace is empty" apart from "I never looked" —
		// two states that must never be reported the same way.
		return Derivation{
			Kinds:     kinds,
			Derivable: true,
			Reason:    "bounded workspace discovery observed no file at all; the workspace is empty or fully ignored",
		}
	}

	// ── Per-kind resolution ────────────────────────────────────────────
	// Each declared kind is resolved independently. A kind with no match is
	// simply absent from the result: "design a page using HTML and CSS" in a
	// workspace with no stylesheet yet is not an error, it is a smaller scope.
	// A kind with MANY matches is genuinely ambiguous and is reported as such
	// rather than silently narrowed to the first match.
	wanted := map[string]artifactKind{}
	for _, k := range kinds {
		for _, ak := range declaredArtifactKinds {
			if ak.Label == k {
				wanted[k] = ak
			}
		}
	}

	var derived []string
	seen := map[string]bool{}
	var ambiguousKinds []string
	for _, k := range kinds {
		var matches []string
		for _, c := range candidates {
			if !extensionSatisfies(wanted[k], c) {
				continue
			}
			if !seen[c] {
				seen[c] = true
				matches = append(matches, c)
			}
		}
		switch len(matches) {
		case 0:
			// No observed file satisfies this kind. Not fatal on its own.
		case 1:
			derived = append(derived, matches[0])
		default:
			ambiguousKinds = append(ambiguousKinds, k)
			derived = append(derived, matches...)
		}
	}

	if len(ambiguousKinds) > 0 {
		sort.Strings(derived)
		return Derivation{
			Targets:   derived,
			Kinds:     kinds,
			Derivable: true,
			Reason: "the objective declares artifact kind(s) " + strings.Join(ambiguousKinds, ",") +
				" and the workspace holds several files for each; the human must name the intended file(s)",
		}
	}
	if len(derived) == 0 {
		return Derivation{
			Kinds: kinds,
			Reason: "the objective declares artifact kind(s) " + strings.Join(kinds, ",") +
				" but the bounded discovery pass observed no file with a matching extension; the runtime will not invent a target",
		}
	}
	sort.Strings(derived)
	return Derivation{
		Targets:   derived,
		Kinds:     kinds,
		Derivable: true,
		Reason: "derived from observed workspace evidence: kind(s) " + strings.Join(kinds, ",") +
			" matched " + describePaths(derived) + " by extension; every path was read from disk by bounded discovery",
	}
}

// extensionSatisfies reports whether an observed path's extension satisfies the
// artifact kind. The comparison is on the extension ONLY, so a path can never be
// matched because its DIRECTORY or stem happens to contain the kind's name.
func extensionSatisfies(ak artifactKind, candidate string) bool {
	ext := strings.ToLower(path.Ext(candidate))
	for _, want := range ak.Extensions {
		if ext == want {
			return true
		}
	}
	return false
}

// describePaths renders a bounded, human-readable path list for evidence.
func describePaths(paths []string) string {
	const shown = 8
	if len(paths) <= shown {
		return strings.Join(paths, ", ")
	}
	return strings.Join(paths[:shown], ", ") + fmt.Sprintf(" (+%d more)", len(paths)-shown)
}

func nonBlank(items []string) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		if strings.TrimSpace(it) != "" {
			out = append(out, it)
		}
	}
	return out
}
