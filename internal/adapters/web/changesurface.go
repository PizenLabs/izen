package web

import (
	"sort"
	"strings"

	"github.com/PizenLabs/izen/internal/changesurface"
	"github.com/PizenLabs/izen/internal/understanding"
)

// Candidate expands the web-specific keyword mapping onto the generic
// ChangeSurface. Core must not import this; callers that need web-aware
// surface should invoke this helper after obtaining the generic surface
// or directly derive via DeriveSurface.
func DeriveSurface(intent string, explicitTargets []string, u understanding.ProjectUnderstanding) changesurface.ChangeSurface {
	// Start from the domain-neutral surface.
	base := changesurface.Derive(intent, explicitTargets, u)
	// Derive web surface from generic understanding.
	ws := DeriveFromUnderstanding(u)
	if !ws.Present {
		return base
	}

	// Web keyword expansion: only adds candidates that are already
	// evidence-backed (present in ws).
	lower := strings.ToLower(intent)
	extra := webCandidatesForIntent(lower, ws)
	if len(extra) == 0 {
		return base
	}

	// Merge extra into base, preserving no-invented-targets: only paths
	// present in ws (already evidence-backed) are admitted.
	known := webKnownPaths(ws)
	var toAdd []changesurface.Candidate
	for _, c := range extra {
		if known[c.Path] {
			toAdd = append(toAdd, c)
		}
	}
	if len(toAdd) == 0 {
		return base
	}

	// Deduplicate and determine status.
	merged := append([]changesurface.Candidate(nil), base.Candidates...)
	merged = append(merged, toAdd...)
	merged = dedupe(merged)

	status := base.Status
	if hasDirect(merged) {
		status = changesurface.StatusResolved
	} else if len(merged) > 0 {
		if base.Status == changesurface.StatusUnresolved {
			status = changesurface.StatusPartial
		}
	}

	// Build evidence set.
	evidenceSet := map[string]bool{}
	for _, e := range base.Evidence {
		evidenceSet[e] = true
	}
	for _, c := range toAdd {
		for _, e := range c.Evidence {
			evidenceSet[e] = true
		}
	}
	var evidence []string
	for e := range evidenceSet {
		evidence = append(evidence, e)
	}
	sort.Strings(evidence)

	return changesurface.ChangeSurface{
		Status:              status,
		Candidates:          merged,
		Evidence:            evidence,
		UnderstandingDigest: base.UnderstandingDigest,
		IntentSummary:       base.IntentSummary,
		UnresolvedReason:    base.UnresolvedReason,
	}
}

func webKnownPaths(ws StaticWebSurface) map[string]bool {
	m := map[string]bool{}
	for _, f := range ws.Entrypoints {
		m[f] = true
	}
	for _, f := range ws.HTML {
		m[f] = true
	}
	for _, f := range ws.CSS {
		m[f] = true
	}
	for _, f := range ws.Scripts {
		m[f] = true
	}
	for _, d := range ws.AssetDirs {
		m[d] = true
		m[strings.TrimSuffix(d, "/")] = true
	}
	return m
}

func webCandidatesForIntent(lower string, ws StaticWebSurface) []changesurface.Candidate {
	var out []changesurface.Candidate
	containsAny := func(words []string) bool {
		for _, w := range words {
			if strings.Contains(lower, w) {
				return true
			}
		}
		return false
	}

	if containsAny([]string{"homepage", "home page", "landing", "index", "portfolio", "website", "site", "page"}) {
		// DIRECT entrypoints + RELATED assets
		for _, p := range ws.Entrypoints {
			out = append(out, changesurface.Candidate{Path: p, Certainty: changesurface.CertaintyDirect, Reason: "intent references the web surface; entrypoint and linked assets are structurally relevant", Evidence: []string{"structure:" + p}})
		}
		for _, p := range ws.CSS {
			out = append(out, changesurface.Candidate{Path: p, Certainty: changesurface.CertaintyRelated, Reason: "intent references the web surface; entrypoint and linked assets are structurally relevant", Evidence: []string{"structure:" + p}})
		}
		for _, p := range ws.Scripts {
			out = append(out, changesurface.Candidate{Path: p, Certainty: changesurface.CertaintyRelated, Reason: "intent references the web surface; entrypoint and linked assets are structurally relevant", Evidence: []string{"structure:" + p}})
		}
		for _, d := range ws.AssetDirs {
			out = append(out, changesurface.Candidate{Path: d, Certainty: changesurface.CertaintyRelated, Reason: "intent references the web surface; entrypoint and linked assets are structurally relevant", Evidence: []string{"structure:" + d + "/"}})
		}
		return out
	}
	if containsAny([]string{"style", "css", "theme", "look", "design", "redesign", "layout"}) {
		for _, f := range ws.CSS {
			out = append(out, changesurface.Candidate{Path: f, Certainty: changesurface.CertaintyDirect, Reason: "intent references styling; stylesheet is structurally relevant", Evidence: []string{"structure:" + f}})
		}
		for _, f := range ws.Entrypoints {
			out = append(out, changesurface.Candidate{Path: f, Certainty: changesurface.CertaintyRelated, Reason: "stylesheet is linked from the HTML entrypoint", Evidence: []string{"structure:" + f}})
		}
	}
	if containsAny([]string{"script", "javascript", "js", "behavior", "interactive", "logic"}) {
		for _, f := range ws.Scripts {
			out = append(out, changesurface.Candidate{Path: f, Certainty: changesurface.CertaintyDirect, Reason: "intent references behavior; script is structurally relevant", Evidence: []string{"structure:" + f}})
		}
	}
	if containsAny([]string{"asset", "image", "images", "static", "media", "font"}) {
		for _, d := range ws.AssetDirs {
			out = append(out, changesurface.Candidate{Path: d, Certainty: changesurface.CertaintyDirect, Reason: "intent references assets; asset directory is structurally relevant", Evidence: []string{"structure:" + d + "/"}})
		}
	}
	return out
}

func dedupe(in []changesurface.Candidate) []changesurface.Candidate {
	seen := map[string]bool{}
	var out []changesurface.Candidate
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

func hasDirect(cands []changesurface.Candidate) bool {
	for _, c := range cands {
		if c.Certainty == changesurface.CertaintyDirect {
			return true
		}
	}
	return false
}
