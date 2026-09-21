package changesurface

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/internal/understanding"
)

// Certainty classifies how strongly a candidate relates to the intent.
// DIRECT means the candidate is explicitly or structurally implicated;
// RELATED means it is plausibly adjacent (same component, referenced
// asset); UNKNOWN marks an unresolved surface with no fabricated target.
type Certainty string

const (
	// CertaintyDirect marks an explicitly or structurally implicated area.
	CertaintyDirect Certainty = "DIRECT"
	// CertaintyRelated marks a plausibly adjacent area.
	CertaintyRelated Certainty = "RELATED"
	// CertaintyUnknown marks an unresolved surface (no candidates).
	CertaintyUnknown Certainty = "UNKNOWN"
)

// String returns the canonical certainty label.
func (c Certainty) String() string { return string(c) }

// Status classifies whether the surface resolved against evidence.
type Status string

const (
	// StatusResolved means intent + understanding identified candidates.
	StatusResolved Status = "RESOLVED"
	// StatusPartial means only weak/adjacent evidence was found.
	StatusPartial Status = "PARTIAL"
	// StatusUnresolved means no evidence-backed relationship exists.
	// The surface is empty by design — targets are never invented.
	StatusUnresolved Status = "UNRESOLVED"
)

// String returns the canonical status label.
func (s Status) String() string { return string(s) }

// Candidate is one repository area plausibly relevant to the intent. It
// carries its provenance and certainty, never an authorization and never
// a mutation operation.
type Candidate struct {
	// Path is the workspace-relative file or directory.
	Path string `json:"path"`
	// Certainty is DIRECT, RELATED, or UNKNOWN.
	Certainty Certainty `json:"certainty"`
	// Reason is the human-readable derivation justification.
	Reason string `json:"reason"`
	// Evidence carries the supporting signal keys.
	Evidence []string `json:"evidence,omitempty"`
}

// ChangeSurface is the canonical derivation of intent + understanding +
// repository evidence into plausibly relevant repository areas. Read-only
// and informational: it cannot authorize mutation and cannot express a
// mutation operation.
type ChangeSurface struct {
	// Status is RESOLVED, PARTIAL, or UNRESOLVED.
	Status Status `json:"status"`
	// Candidates are the plausibly relevant areas (empty when unresolved).
	Candidates []Candidate `json:"candidates,omitempty"`
	// Evidence carries the derivation provenance (signal keys).
	Evidence []string `json:"evidence,omitempty"`
	// UnderstandingDigest binds the surface to the exact understanding it
	// was derived from. A digest mismatch means stale input.
	UnderstandingDigest string `json:"understanding_digest"`
	// IntentSummary is the truncated intent the surface was derived from.
	IntentSummary string `json:"intent_summary"`
	// UnresolvedReason explains an unresolved surface, if any.
	UnresolvedReason string `json:"unresolved_reason,omitempty"`
}

// DigestMatches reports whether the surface was derived from the given
// understanding instance. A mismatch means the understanding moved on and
// the surface must be re-derived, never trusted as current.
func (s ChangeSurface) DigestMatches(u understanding.ProjectUnderstanding) bool {
	return s.UnderstandingDigest != "" && s.UnderstandingDigest == u.Digest
}

// Derive computes the Change Surface for intent against an evidence-backed
// ProjectUnderstanding. explicitTargets are user-referenced concrete
// targets (e.g. @index.html); only targets present in repository evidence
// become DIRECT candidates — anything else is dropped, never invented.
// The derivation is conservative: unknown or ambiguous understanding
// yields an unresolved surface rather than fabricated targets.
func Derive(intent string, explicitTargets []string, u understanding.ProjectUnderstanding) ChangeSurface {
	surface := ChangeSurface{
		UnderstandingDigest: u.Digest,
		IntentSummary:       truncateIntent(intent),
	}
	if !u.Valid() {
		surface.Status = StatusUnresolved
		surface.UnresolvedReason = "project understanding is unavailable or stale; refusing to fabricate targets"
		return surface
	}
	if u.Kind == understanding.KindUnknown {
		surface.Status = StatusUnresolved
		surface.UnresolvedReason = "project understanding is UNKNOWN; refusing to fabricate targets"
		return surface
	}

	known := knownPaths(u)
	var candidates []Candidate
	var provenance []string

	// 1. Explicit user targets backed by repository evidence → DIRECT.
	// Target resolution ("what did the user refer to?") stays distinct:
	// we only admit the target as a surface candidate when the workspace
	// actually contains it.
	for _, raw := range explicitTargets {
		t := strings.TrimSpace(strings.TrimPrefix(raw, "@"))
		t = strings.Trim(t, "\"'")
		if t == "" {
			continue
		}
		if match, ok := lookupKnown(known, t); ok {
			candidates = append(candidates, Candidate{
				Path:      match,
				Certainty: CertaintyDirect,
				Reason:    "explicitly referenced target present in repository evidence",
				Evidence:  []string{"target:" + match},
			})
			provenance = append(provenance, "target:"+match)
		}
	}

	// 2. Intent keywords mapped onto evidenced components → DIRECT/RELATED.
	lower := strings.ToLower(intent)
	for _, kw := range intentKeywords(lower) {
		for _, c := range keywordCandidates(kw, u, known) {
			candidates = append(candidates, c)
			provenance = append(provenance, c.Evidence...)
		}
	}

	candidates = dedupeCandidates(candidates)
	if len(candidates) == 0 {
		// 3. Broad read-oriented intents (review/explore) over an EXISTING
		// project yield a RELATED component-level surface, never a
		// fabricated file target.
		if u.Kind == understanding.KindExisting && isBroadReadIntent(lower) {
			candidates = componentSurface(u, known)
			provenance = append(provenance, "intent:broad-read")
		}
	}

	switch {
	case len(candidates) == 0:
		surface.Status = StatusUnresolved
		surface.UnresolvedReason = "no evidence-backed relationship between intent and repository structure; refusing to fabricate targets"
	case hasDirect(candidates):
		surface.Status = StatusResolved
	default:
		surface.Status = StatusPartial
	}
	surface.Candidates = candidates
	surface.Evidence = uniqueSorted(provenance)
	return surface
}

// ─── helpers ────────────────────────────────────────────────────────────

// knownPaths indexes every evidenced repository path: understanding
// evidence IDs plus the static-web file surface.
func knownPaths(u understanding.ProjectUnderstanding) map[string]bool {
	known := map[string]bool{}
	for _, e := range u.Evidence {
		id := e.ID
		for _, prefix := range []string{"structure:", "manifest:", "config:"} {
			if strings.HasPrefix(id, prefix) {
				p := strings.TrimSuffix(strings.TrimPrefix(id, prefix), "/")
				if p != "" {
					known[p] = true
				}
			}
		}
	}
	if u.StaticWeb != nil {
		for _, f := range u.StaticWeb.Entrypoints {
			known[f] = true
		}
		for _, f := range u.StaticWeb.HTML {
			known[f] = true
		}
		for _, f := range u.StaticWeb.CSS {
			known[f] = true
		}
		for _, f := range u.StaticWeb.Scripts {
			known[f] = true
		}
		for _, d := range u.StaticWeb.AssetDirs {
			known[d] = true
			known[strings.TrimSuffix(d, "/")] = true
		}
	}
	return known
}

// lookupKnown resolves a user-referenced target against evidenced paths:
// exact match first, then basename match (conservative: shortest path
// wins, ambiguous basenames still resolve to one evidenced file — the
// target itself was explicit, so this is resolution, not invention).
func lookupKnown(known map[string]bool, target string) (string, bool) {
	clean := filepath.ToSlash(filepath.Clean(target))
	if known[clean] {
		return clean, true
	}
	base := strings.ToLower(filepath.Base(clean))
	var best string
	for p := range known {
		if strings.ToLower(filepath.Base(p)) == base {
			if best == "" || len(p) < len(best) {
				best = p
			}
		}
	}
	return best, best != ""
}

// keywordRule maps an intent keyword family onto evidenced paths.
type keywordRule struct {
	words []string
	match func(u understanding.ProjectUnderstanding, known map[string]bool) []Candidate
}

func keywordRules() []keywordRule {
	return []keywordRule{
		{
			words: []string{"homepage", "home page", "landing", "index", "portfolio", "website", "site", "page"},
			match: func(u understanding.ProjectUnderstanding, known map[string]bool) []Candidate {
				return staticWebCandidates(u, known, CertaintyDirect, "intent references the web surface; entrypoint and linked assets are structurally relevant")
			},
		},
		{
			words: []string{"style", "css", "theme", "look", "design", "redesign", "layout"},
			match: func(u understanding.ProjectUnderstanding, known map[string]bool) []Candidate {
				var out []Candidate
				if u.StaticWeb != nil {
					for _, f := range u.StaticWeb.CSS {
						out = append(out, Candidate{Path: f, Certainty: CertaintyDirect, Reason: "intent references styling; stylesheet is structurally relevant", Evidence: []string{"structure:" + f}})
					}
					for _, f := range u.StaticWeb.Entrypoints {
						out = append(out, Candidate{Path: f, Certainty: CertaintyRelated, Reason: "stylesheet is linked from the HTML entrypoint", Evidence: []string{"structure:" + f}})
					}
				}
				return out
			},
		},
		{
			words: []string{"script", "javascript", "js", "behavior", "interactive", "logic"},
			match: func(u understanding.ProjectUnderstanding, known map[string]bool) []Candidate {
				var out []Candidate
				if u.StaticWeb != nil {
					for _, f := range u.StaticWeb.Scripts {
						out = append(out, Candidate{Path: f, Certainty: CertaintyDirect, Reason: "intent references behavior; script is structurally relevant", Evidence: []string{"structure:" + f}})
					}
				}
				return out
			},
		},
		{
			words: []string{"asset", "image", "images", "static", "media", "font"},
			match: func(u understanding.ProjectUnderstanding, known map[string]bool) []Candidate {
				var out []Candidate
				if u.StaticWeb != nil {
					for _, d := range u.StaticWeb.AssetDirs {
						out = append(out, Candidate{Path: d, Certainty: CertaintyDirect, Reason: "intent references assets; asset directory is structurally relevant", Evidence: []string{"structure:" + d + "/"}})
					}
				}
				return out
			},
		},
	}
}

// intentKeywords returns the rule indices whose words appear in the intent.
func intentKeywords(lower string) []int {
	var out []int
	for i, r := range keywordRules() {
		for _, w := range r.words {
			if strings.Contains(lower, w) {
				out = append(out, i)
				break
			}
		}
	}
	return out
}

func keywordCandidates(rule int, u understanding.ProjectUnderstanding, known map[string]bool) []Candidate {
	rules := keywordRules()
	if rule < 0 || rule >= len(rules) {
		return nil
	}
	got := rules[rule].match(u, known)
	// Admit only candidates backed by repository evidence.
	var out []Candidate
	for _, c := range got {
		if known[c.Path] || known[strings.TrimSuffix(c.Path, "/")] {
			out = append(out, c)
		}
	}
	return out
}

// staticWebCandidates expands the evidenced static-web surface.
func staticWebCandidates(u understanding.ProjectUnderstanding, known map[string]bool, cert Certainty, reason string) []Candidate {
	if u.StaticWeb == nil {
		return nil
	}
	var out []Candidate
	add := func(paths []string, c Certainty) {
		for _, p := range paths {
			if known[p] {
				out = append(out, Candidate{Path: p, Certainty: c, Reason: reason, Evidence: []string{"structure:" + p}})
			}
		}
	}
	add(u.StaticWeb.Entrypoints, cert)
	add(u.StaticWeb.CSS, CertaintyRelated)
	add(u.StaticWeb.Scripts, CertaintyRelated)
	add(u.StaticWeb.AssetDirs, CertaintyRelated)
	return out
}

// isBroadReadIntent reports read-oriented intents that legitimately widen
// the surface to the component level (still RELATED, never DIRECT files).
func isBroadReadIntent(lower string) bool {
	for _, w := range []string{"review", "explore", "overview", "summarize", "summary", "explain", "audit", "survey", "understand"} {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

// componentSurface returns one RELATED candidate per evidenced component
// path for broad read intents.
func componentSurface(u understanding.ProjectUnderstanding, known map[string]bool) []Candidate {
	var out []Candidate
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] || !known[p] {
			return
		}
		seen[p] = true
		out = append(out, Candidate{Path: p, Certainty: CertaintyRelated, Reason: "broad read-oriented intent over an existing project; component-level surface", Evidence: []string{"intent:broad-read"}})
	}
	if u.StaticWeb != nil {
		for _, p := range u.StaticWeb.Entrypoints {
			add(p)
		}
		for _, p := range u.StaticWeb.CSS {
			add(p)
		}
		for _, p := range u.StaticWeb.Scripts {
			add(p)
		}
		for _, p := range u.StaticWeb.AssetDirs {
			add(p)
		}
	}
	for _, c := range u.Components {
		for _, p := range c.Paths {
			add(p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	if len(out) > 12 {
		out = out[:12]
	}
	return out
}

func hasDirect(cands []Candidate) bool {
	for _, c := range cands {
		if c.Certainty == CertaintyDirect {
			return true
		}
	}
	return false
}

func dedupeCandidates(in []Candidate) []Candidate {
	seen := map[string]bool{}
	var out []Candidate
	for _, c := range in {
		key := c.Path + "\x00" + string(c.Certainty)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Certainty != out[j].Certainty {
			return out[i].Certainty < out[j].Certainty // DIRECT < RELATED
		}
		return out[i].Path < out[j].Path
	})
	return out
}

func uniqueSorted(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func truncateIntent(s string) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if len(s) > 160 {
		return s[:157] + "..."
	}
	return s
}
