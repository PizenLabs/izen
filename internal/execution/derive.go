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
//
// ── WHY THE VERDICT IS STRUCTURED ───────────────────────────────────────────
//
// The first version of this file returned a NON-EMPTY TARGET LIST for the
// ambiguous case and reported the ambiguity in Reason. The caller then read the
// list as a scope:
//
//	ambiguous evidence → non-empty Targets → ScopeResolved → mutation
//
// A non-empty target list is not a resolved scope. `a.html b.html index.html`
// says the workspace holds three html files; it does not say the objective is
// about all three. So the verdict is now a TYPED FIELD, and it is the field the
// caller branches on:
//
//	UNRESOLVED  no target could be derived
//	UNIQUE      the observed evidence determines the target set
//	AMBIGUOUS   several observed files satisfy the objective and NONE of them is
//	            proven to be the intended one
//
// The three cases are mutually exclusive and a caller can no longer collapse the
// last two. Reason remains, verbatim, for humans; it is no longer load-bearing.

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

// DerivationStatus is the closed, three-valued verdict of one evidence-bound
// derivation pass. It exists because the SIZE of a derived target set is not a
// verdict:
//
//	DerivationUnresolved  nothing could be derived, and nothing was proven
//	DerivationUnique      exactly the observed evidence determines the target set
//	DerivationAmbiguous   the objective matched SEVERAL observed files and the
//	                      evidence does not establish which one(s) it is about
//
// The third value is the one a non-empty target list used to hide. Callers must
// branch on this field (or the predicates below) and never on len(Targets).
type DerivationStatus string

const (
	// DerivationUnresolved: no target was derived. The derivation may have been
	// refused outright (no declared kind, or a proven target already exists) or
	// attempted and found nothing; Derivable distinguishes the two.
	DerivationUnresolved DerivationStatus = "UNRESOLVED"
	// DerivationUnique: the derived target set is deterministic given the
	// observation. This is the ONLY status that may become a mutation scope.
	DerivationUnique DerivationStatus = "UNIQUE"
	// DerivationAmbiguous: evidence exists and candidates exist, but the
	// objective did not establish which candidate(s) it means. The candidates
	// are carried as evidence — never as authority.
	DerivationAmbiguous DerivationStatus = "AMBIGUOUS"
)

// AllDerivationStatuses returns the closed vocabulary. It exists so a test can
// assert the three values are the whole set: a caller branches on this field, so
// adding a fourth meaning is an architectural decision rather than something a
// call site may do locally.
func AllDerivationStatuses() []DerivationStatus {
	return []DerivationStatus{DerivationUnresolved, DerivationUnique, DerivationAmbiguous}
}

// String returns the canonical derivation-status label.
func (s DerivationStatus) String() string { return string(s) }

// KindResolution is the per-declared-kind outcome of one derivation pass. It is
// the structured record of WHERE the ambiguity is: a kind with several observed
// matches is ambiguous, a kind with exactly one is not, and a kind with none is
// simply absent from the objective's scope.
type KindResolution struct {
	// Kind is the canonical artifact-kind label the objective declared.
	Kind string
	// Matches are the observed paths whose extension satisfies the kind, in
	// deterministic order. They are CANDIDATES.
	Matches []string
	// Ambiguous reports that the evidence offers more than one file for this
	// kind and therefore does not establish the intended one.
	Ambiguous bool
}

// Derivation is the complete, evidence-carrying outcome of one derivation pass.
type Derivation struct {
	// Targets is the derived, existing-file set. It is populated for UNIQUE
	// (where it IS the candidate mutation scope) and for AMBIGUOUS (where it is
	// the DISAMBIGUATION CANDIDATE SET and never a scope). Status is the field
	// that distinguishes the two; Targets alone must never be read as a scope.
	Targets []string
	// Kinds are the artifact kinds the objective declared, in canonical order.
	Kinds []string
	// Resolutions is the per-kind breakdown that produced Status.
	Resolutions []KindResolution
	// Status is the typed verdict. A zero Derivation is UNRESOLVED, never
	// UNIQUE: an unconstructed result can never read as a resolved scope.
	Status DerivationStatus
	// Reason explains the verdict in runtime vocabulary, never prompt vocabulary.
	// It is evidence for humans; Status is what callers branch on.
	Reason string
	// Derivable reports whether derivation was ATTEMPTED. A caller can tell
	// "nothing to derive" from "derivation refused", which matter differently.
	Derivable bool
}

// status normalizes the zero value, so an unconstructed or legacy Derivation is
// UNRESOLVED and can never be mistaken for a resolved scope.
func (d Derivation) status() DerivationStatus {
	if d.Status == "" {
		return DerivationUnresolved
	}
	return d.Status
}

// StatusOrUnresolved returns the normalized typed verdict of the derivation.
func (d Derivation) StatusOrUnresolved() DerivationStatus { return d.status() }

// IsUnique reports whether the evidence determines the target set. Only a UNIQUE
// derivation may become a mutation scope.
func (d Derivation) IsUnique() bool { return d.status() == DerivationUnique }

// IsAmbiguous reports whether candidates exist without a proven choice among
// them. An ambiguous derivation MUST NOT produce a resolved scope.
func (d Derivation) IsAmbiguous() bool { return d.status() == DerivationAmbiguous }

// IsUnresolved reports that no target could be derived.
func (d Derivation) IsUnresolved() bool { return !d.IsUnique() && !d.IsAmbiguous() }

// AmbiguousKinds names the declared kinds whose observed matches are ambiguous.
func (d Derivation) AmbiguousKinds() []string {
	var out []string
	for _, r := range d.Resolutions {
		if r.Ambiguous {
			out = append(out, r.Kind)
		}
	}
	return out
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
			Status: DerivationUnresolved,
			Reason: "the caller already proved a target set; derivation never overrides a stated target",
		}
	}
	kinds := DeclareArtifactKinds(req.Prompt)
	if len(kinds) == 0 {
		return Derivation{
			Status: DerivationUnresolved,
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
			Status:    DerivationUnresolved,
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
	var resolutions []KindResolution
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
		sort.Strings(matches)
		ambiguous := len(matches) > 1
		resolutions = append(resolutions, KindResolution{
			Kind:      k,
			Matches:   matches,
			Ambiguous: ambiguous,
		})
		if ambiguous {
			ambiguousKinds = append(ambiguousKinds, k)
		}
		derived = append(derived, matches...)
	}
	sort.Strings(derived)

	// ── AMBIGUOUS ───────────────────────────────────────────────────────
	// The pass found candidates and could not prove which of them the objective
	// is about. The candidate set travels with the verdict as EVIDENCE, and the
	// verdict is AMBIGUOUS so no caller can read the non-empty list as a scope.
	//
	// It deliberately does not narrow: binding the unambiguous kinds and
	// dropping the ambiguous ones would silently rewrite the objective into the
	// subset the runtime happened to be sure about.
	if len(ambiguousKinds) > 0 {
		return Derivation{
			Status:      DerivationAmbiguous,
			Targets:     derived,
			Kinds:       kinds,
			Resolutions: resolutions,
			Derivable:   true,
			Reason: "the objective declares artifact kind(s) " + strings.Join(ambiguousKinds, ",") +
				" and the workspace holds several files for each; the human must name the intended file(s)",
		}
	}
	if len(derived) == 0 {
		return Derivation{
			Status:      DerivationUnresolved,
			Kinds:       kinds,
			Resolutions: resolutions,
			Reason: "the objective declares artifact kind(s) " + strings.Join(kinds, ",") +
				" but the bounded discovery pass observed no file with a matching extension; the runtime will not invent a target",
		}
	}
	// ── UNIQUE ─────────────────────────────────────────────────────────
	// Every declared kind matched at most one observed file, so the evidence
	// determines the target set and the caller may bind it.
	return Derivation{
		Status:      DerivationUnique,
		Targets:     derived,
		Kinds:       kinds,
		Resolutions: resolutions,
		Derivable:   true,
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
