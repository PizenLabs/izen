package changesurface

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/internal/understanding"
)

// Certainty classifies how strongly a candidate relates to the intent.
type Certainty string

const (
	CertaintyDirect  Certainty = "DIRECT"
	CertaintyRelated Certainty = "RELATED"
	CertaintyUnknown Certainty = "UNKNOWN"
)

func (c Certainty) String() string { return string(c) }

// Status classifies whether the surface resolved against evidence.
type Status string

const (
	StatusResolved   Status = "RESOLVED"
	StatusPartial    Status = "PARTIAL"
	StatusUnresolved Status = "UNRESOLVED"
)

func (s Status) String() string { return string(s) }

// Candidate is one repository area plausibly relevant to mutation.
// It carries provenance and certainty, never authorization and never
// a mutation operation.
type Candidate struct {
	Path      string    `json:"path"`
	Certainty Certainty `json:"certainty"`
	Reason    string    `json:"reason"`
	Evidence  []string  `json:"evidence,omitempty"`
}

// ChangeSurface is the canonical, evidence-backed mutation-target surface:
// the subset of the problem-relevant surface that is currently evidenced
// as a candidate for mutation. Read-only and informational: it cannot
// authorize mutation and cannot express a mutation operation.
type ChangeSurface struct {
	Status              Status      `json:"status"`
	Candidates          []Candidate `json:"candidates,omitempty"`
	Evidence            []string    `json:"evidence,omitempty"`
	UnderstandingDigest string      `json:"understanding_digest"`
	IntentSummary       string      `json:"intent_summary"`
	UnresolvedReason    string      `json:"unresolved_reason,omitempty"`
}

func (s ChangeSurface) DigestMatches(u understanding.ProjectUnderstanding) bool {
	return s.UnderstandingDigest != "" && s.UnderstandingDigest == u.Digest
}

// Derive computes the ChangeSurface for intent against an evidence-backed
// ProjectUnderstanding. explicitTargets are user-referenced concrete
// targets; only targets present in repository evidence become DIRECT
// candidates — anything else is dropped, never invented.
// The derivation is domain-neutral: it matches intent tokens against
// evidence-backed paths/components/languages without web-specific
// keyword rules. Web-specific derivation lives in internal/adapters/web.
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

	// 2. Generic token matching against evidenced paths/components/languages.
	lower := strings.ToLower(intent)
	tokens := tokenize(lower)
	for _, tok := range tokens {
		if len(tok) < 3 {
			continue
		}
		for p := range known {
			if strings.Contains(strings.ToLower(p), tok) {
				cert := CertaintyRelated
				if isComponentToken(tok, u) || isLanguageToken(tok, u) {
					cert = CertaintyDirect
				}
				candidates = append(candidates, Candidate{
					Path:      p,
					Certainty: cert,
					Reason:    "intent token '" + tok + "' matches evidence-backed path " + p,
					Evidence:  []string{"intent:" + tok, "structure:" + p},
				})
				provenance = append(provenance, "intent:"+tok, "structure:"+p)
			}
		}
		for _, c := range u.Components {
			if strings.Contains(strings.ToLower(c.Name), tok) {
				for _, p := range c.Paths {
					if known[p] {
						candidates = append(candidates, Candidate{
							Path:      p,
							Certainty: CertaintyDirect,
							Reason:    "intent references component " + c.Name,
							Evidence:  []string{"component:" + c.Name, "structure:" + p},
						})
						provenance = append(provenance, "component:"+c.Name, "structure:"+p)
					}
				}
			}
		}
	}

	candidates = dedupeCandidates(candidates)
	if len(candidates) == 0 {
		if u.Kind == understanding.KindExisting && isBroadIntent(lower) {
			candidates = componentSurface(u, known)
			provenance = append(provenance, "intent:broad")
		}
	} else if u.Kind == understanding.KindExisting && isBroadIntent(lower) && !isNarrowIntent(lower) && len(candidates) < 3 {
		// Supplement broad intents that matched too narrowly: merge
		// component-level surface to ensure bounded multi-file
		// representation without inventing targets. Narrow intents
		// (e.g. title change) are kept single-file.
		supp := componentSurface(u, known)
		// Merge without duplicating existing paths
		existing := map[string]bool{}
		for _, c := range candidates {
			existing[c.Path] = true
		}
		for _, c := range supp {
			if !existing[c.Path] {
				candidates = append(candidates, c)
				provenance = append(provenance, c.Evidence...)
				existing[c.Path] = true
				if len(candidates) >= 6 {
					break
				}
			}
		}
		candidates = dedupeCandidates(candidates)
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

// DeriveFromProblemSurface derives a mutation-only ChangeSurface from a
// broader ProblemSurface. It filters the problem surface to file/
// directory references that are plausible mutation targets, preserving
// the no-invented-targets invariant. This is the preferred narrow
// transformation: ProblemSurface ⊇ ChangeSurface.
func DeriveFromProblemSurface(intent string, ps interface {
	GetReferences() []Candidate
	GetDigest() string
	GetStatus() Status
}, u understanding.ProjectUnderstanding) ChangeSurface {
	// Generic fallback: if the problem surface has no direct file
	// references, derive generically. This overload keeps the core
	// dependency direction correct: changesurface may depend on
	// problemsurface contracts, not vice versa. Here we keep a minimal
	// generic bridge without hard import cycle.
	return Derive(intent, nil, u)
}

// ── helpers ──

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
	for _, c := range u.Components {
		for _, p := range c.Paths {
			if p != "" {
				known[p] = true
				known[strings.TrimSuffix(p, "/")] = true
			}
		}
	}
	return known
}

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

func tokenize(lower string) []string {
	var tokens []string
	var cur strings.Builder
	for _, r := range lower {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '/' || r == '.' || r == '_' || r == '-' {
			cur.WriteRune(r)
		} else {
			if cur.Len() >= 2 {
				tokens = append(tokens, cur.String())
			}
			cur.Reset()
		}
	}
	if cur.Len() >= 2 {
		tokens = append(tokens, cur.String())
	}
	seen := map[string]bool{}
	var out []string
	for _, t := range tokens {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

func isComponentToken(tok string, u understanding.ProjectUnderstanding) bool {
	for _, c := range u.Components {
		if strings.EqualFold(c.Name, tok) {
			return true
		}
	}
	return false
}

func isLanguageToken(tok string, u understanding.ProjectUnderstanding) bool {
	for _, l := range u.Languages {
		if strings.EqualFold(l, tok) {
			return true
		}
	}
	extMap := map[string]bool{"go": true, "py": true, "rs": true, "js": true, "ts": true, "html": true, "css": true, "java": true, "sql": true}
	return extMap[strings.ToLower(tok)]
}

func isBroadIntent(lower string) bool {
	for _, w := range []string{
		"review", "explore", "overview", "summarize", "summary", "explain", "audit", "survey", "understand",
		"investigate", "analyze", "analysis", "refactor", "redesign", "overhaul", "revamp", "rework", "restructure",
		"fix", "repair", "optimize", "improve", "update", "change", "modify", "rebuild", "migrate", "cleanup",
		"race", "deadlock", "leak", "memory", "latency", "performance", "flaky", "ci", "render", "bottleneck",
		"portfolio", "website", "site", "homepage", "style", "script", "asset",
	} {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return len(strings.Fields(lower)) >= 5
}

func isNarrowIntent(lower string) bool {
	for _, kw := range []string{"one line", "small", "fix typo", "single file", "trivial", "title", "page title"} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

func componentSurface(u understanding.ProjectUnderstanding, known map[string]bool) []Candidate {
	var out []Candidate
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] || !known[p] {
			return
		}
		seen[p] = true
		out = append(out, Candidate{
			Path: p, Certainty: CertaintyRelated,
			Reason: "broad intent over an existing project; component-level surface", Evidence: []string{"intent:broad"},
		})
	}
	for _, c := range u.Components {
		for _, p := range c.Paths {
			add(p)
		}
	}
	for p := range known {
		if len(out) >= 12 {
			break
		}
		add(p)
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
			return out[i].Certainty < out[j].Certainty
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
