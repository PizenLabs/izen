package problemsurface

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/internal/understanding"
)

// Certainty classifies how strongly a reference relates to the problem.
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

// Reference is one evidence-backed area relevant to understanding/
// investigation/problem solving. It is informational only: no
// authorization, no mutation, no execution semantics.
type Reference struct {
	// Path is the workspace-relative file or directory, or a symbolic
	// reference (e.g. package name, config key) when backed by evidence.
	Path string `json:"path"`
	// Kind is the coarse category ("file", "directory", "symbol",
	// "package", "config", "test", "ci", "log", etc). Domain-neutral;
	// never invents domain-specific objects without evidence.
	Kind string `json:"kind"`
	// Certainty is DIRECT, RELATED, or UNKNOWN.
	Certainty Certainty `json:"certainty"`
	// Reason is the human-readable derivation justification.
	Reason string `json:"reason"`
	// Evidence carries the supporting signal keys.
	Evidence []string `json:"evidence,omitempty"`
}

// ProblemSurface is the domain-neutral, evidence-backed area relevant to
// understanding/investigation/problem solving. It may reference files,
// directories, symbols, packages, tests, configuration, CI artifacts,
// logs, profiles, dependency relationships, runtime observations — but
// only when those references are actually backed by evidence.
//
// ProblemSurface is informational and read-only. It carries no
// authorization, capability, mutation, execution, retry, continuation,
// or model selection semantics.
//
// Invariants:
//   - No invented targets: every reference must be evidence-backed.
//   - Stale understanding → unusable surface (digest binding).
type ProblemSurface struct {
	Status              Status      `json:"status"`
	References          []Reference `json:"references,omitempty"`
	Evidence            []string    `json:"evidence,omitempty"`
	UnderstandingDigest string      `json:"understanding_digest"`
	IntentSummary       string      `json:"intent_summary"`
	UnresolvedReason    string      `json:"unresolved_reason,omitempty"`
}

// DigestMatches reports whether the surface was derived from the given
// understanding instance.
func (s ProblemSurface) DigestMatches(u understanding.ProjectUnderstanding) bool {
	return s.UnderstandingDigest != "" && s.UnderstandingDigest == u.Digest
}

// Derive computes the ProblemSurface for intent against an
// evidence-backed ProjectUnderstanding. ExplicitTargets are user-
// referenced concrete targets (e.g. @cmd/worker); only targets present
// in repository evidence become DIRECT references — anything else is
// dropped, never invented.
func Derive(intent string, explicitTargets []string, u understanding.ProjectUnderstanding) ProblemSurface {
	surface := ProblemSurface{
		UnderstandingDigest: u.Digest,
		IntentSummary:       truncateIntent(intent),
	}
	if !u.Valid() {
		surface.Status = StatusUnresolved
		surface.UnresolvedReason = "project understanding is unavailable or stale; refusing to fabricate references"
		return surface
	}
	if u.Kind == understanding.KindUnknown {
		surface.Status = StatusUnresolved
		surface.UnresolvedReason = "project understanding is UNKNOWN; refusing to fabricate references"
		return surface
	}

	known := knownPaths(u)
	var refs []Reference
	var provenance []string

	// 1. Explicit user targets backed by repository evidence → DIRECT.
	for _, raw := range explicitTargets {
		t := strings.TrimSpace(strings.TrimPrefix(raw, "@"))
		t = strings.Trim(t, "\"'")
		if t == "" {
			continue
		}
		if match, ok := lookupKnown(known, t); ok {
			kind := kindForPath(match, u)
			refs = append(refs, Reference{
				Path:      match,
				Kind:      kind,
				Certainty: CertaintyDirect,
				Reason:    "explicitly referenced target present in repository evidence",
				Evidence:  []string{"target:" + match},
			})
			provenance = append(provenance, "target:"+match)
		}
	}

	// 2. Intent-tokens matched against evidenced paths/components/languages → DIRECT/RELATED.
	lower := strings.ToLower(intent)
	tokens := tokenize(lower)
	for _, tok := range tokens {
		if len(tok) < 3 {
			continue
		}
		for p := range known {
			if strings.Contains(strings.ToLower(p), tok) {
				// Avoid duplicating explicit targets at same certainty
				kind := kindForPath(p, u)
				cert := CertaintyRelated
				// If token is a component name or language, treat as DIRECT for that path
				if isComponentToken(tok, u) || isLanguageToken(tok, u) {
					cert = CertaintyDirect
				}
				refs = append(refs, Reference{
					Path:      p,
					Kind:      kind,
					Certainty: cert,
					Reason:    "intent token '" + tok + "' matches evidence-backed reference " + p,
					Evidence:  []string{"intent:" + tok, "structure:" + p},
				})
				provenance = append(provenance, "intent:"+tok, "structure:"+p)
			}
		}
		// Component name matches
		for _, c := range u.Components {
			if strings.Contains(strings.ToLower(c.Name), tok) {
				for _, p := range c.Paths {
					if known[p] {
						refs = append(refs, Reference{
							Path:      p,
							Kind:      kindForPath(p, u),
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

	refs = dedupeReferences(refs)
	if len(refs) == 0 {
		// 3. Broad intents over EXISTING yield RELATED component-level surface.
		if u.Kind == understanding.KindExisting && isBroadIntent(lower) {
			refs = componentReferences(u, known)
			provenance = append(provenance, "intent:broad")
		}
	}

	switch {
	case len(refs) == 0:
		surface.Status = StatusUnresolved
		surface.UnresolvedReason = "no evidence-backed relationship between intent and repository structure; refusing to fabricate references"
	case hasDirect(refs):
		surface.Status = StatusResolved
	default:
		surface.Status = StatusPartial
	}
	surface.References = refs
	surface.Evidence = uniqueSorted(provenance)
	return surface
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
	// Also index evidence-backed file paths from component evidence where
	// the structure ID may not have been emitted as EvidenceStructure but
	// the component still carries it.
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

func kindForPath(p string, u understanding.ProjectUnderstanding) string {
	low := strings.ToLower(p)
	// Heuristic kind from path + understanding evidence
	if strings.Contains(low, "cmd/") || strings.Contains(low, "internal/") {
		return "package"
	}
	if strings.HasSuffix(low, ".go") || strings.HasSuffix(low, ".rs") || strings.HasSuffix(low, ".py") {
		return "file"
	}
	if strings.HasSuffix(low, "/") || !strings.Contains(filepath.Base(p), ".") {
		// directory-like
		// Check if it's a known component directory
		for _, c := range u.Components {
			for _, cp := range c.Paths {
				if cp == p {
					return "directory"
				}
			}
		}
		return "directory"
	}
	if strings.Contains(low, "test") || strings.Contains(low, "spec") {
		return "test"
	}
	if strings.Contains(low, "config") || strings.HasSuffix(low, ".yml") || strings.HasSuffix(low, ".yaml") || strings.HasSuffix(low, ".json") || strings.HasSuffix(low, ".toml") {
		return "config"
	}
	return "file"
}

func tokenize(lower string) []string {
	// Simple whitespace + punctuation tokenization, filtered to alphanumerics
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
	// Deduplicate
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
	// also check extension tokens
	extMap := map[string]bool{"go": true, "py": true, "rs": true, "js": true, "ts": true, "html": true, "css": true}
	return extMap[strings.ToLower(tok)]
}

func isBroadIntent(lower string) bool {
	for _, w := range []string{"review", "explore", "overview", "summarize", "summary", "explain", "audit", "survey", "understand", "investigate", "analyze", "analysis", "refactor", "redesign", "overhaul", "optimize", "fix", "debug", "profile", "benchmark", "race", "leak", "flaky", "latency", "render", "investigation"} {
		if strings.Contains(lower, w) {
			return true
		}
	}
	// Also broad if intent is long (multiple sentences) suggesting investigation
	if len(strings.Fields(lower)) >= 6 {
		return true
	}
	return false
}

func componentReferences(u understanding.ProjectUnderstanding, known map[string]bool) []Reference {
	var out []Reference
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] || !known[p] {
			return
		}
		seen[p] = true
		out = append(out, Reference{
			Path: p, Kind: kindForPath(p, u), Certainty: CertaintyRelated,
			Reason: "broad intent over an existing project; component-level reference", Evidence: []string{"intent:broad"},
		})
	}
	for _, c := range u.Components {
		for _, p := range c.Paths {
			add(p)
		}
	}
	// Also add known structure evidence paths (bounded)
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

func hasDirect(refs []Reference) bool {
	for _, r := range refs {
		if r.Certainty == CertaintyDirect {
			return true
		}
	}
	return false
}

func dedupeReferences(in []Reference) []Reference {
	seen := map[string]bool{}
	var out []Reference
	for _, r := range in {
		key := r.Path + "\x00" + string(r.Certainty)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
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
