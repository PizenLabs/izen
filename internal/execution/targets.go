package execution

import (
	"fmt"
	"strings"
)

// ── Deterministic target resolution (Phase 9B) ─────────────────────────────
//
// Target is the smallest deterministic target record. Resolution rules:
//
//  1. Explicit @file targets have highest authority.
//  2. Duplicate targets collapse into one execution node (first appearance
//     wins, order preserved).
//  3. No implicit repository-wide scanning — nothing is invented.
//  4. A target set is never silently expanded.
//  5. If ambiguity remains, the caller stops before any provider generation.
//  6. Target identity is preserved exactly throughout execution.
//
// The UI layer extracts @file scopes from the prompt (via the interaction
// language parser); this package validates and de-duplicates them into the
// deterministic target set the ExecutionGraph consumes.

// TargetRole classifies why a target is in the set.
type TargetRole string

// Target roles.
const (
	// TargetExplicit: the file was named with an @file scope.
	TargetExplicit TargetRole = "explicit"
	// TargetInferred: the target was derived from a deterministic keyword
	// default (e.g. LICENSE, README.md) when no explicit scope existed.
	TargetInferred TargetRole = "inferred"
)

// String returns the canonical role label.
func (r TargetRole) String() string { return string(r) }

// Target is one deterministic mutation target.
type Target struct {
	// Path is the exact, preserved target identity.
	Path string
	// Role records whether the target was explicit or inferred.
	Role TargetRole
	// Exists reports whether the file exists on disk.
	Exists bool
}

// TargetResolution is the outcome of resolving a set of extracted file paths.
type TargetResolution struct {
	// Targets are the de-duplicated, order-preserving targets.
	Targets []Target
	// Ambiguous reports whether the request cannot be safely mapped onto a
	// deterministic target set and must pause before any provider generation.
	Ambiguous bool
	// Reason explains the ambiguity.
	Reason string
}

// templateTargets are the well-known files a $hot may create (new-file
// contract). A non-existent explicit target outside this set is a
// deterministic failure — never an invented file.
var templateTargets = map[string]bool{
	"LICENSE":         true,
	"LICENSE.md":      true,
	"README.md":       true,
	"README":          true,
	"Dockerfile":      true,
	"Makefile":        true,
	"CHANGELOG.md":    true,
	"NOTICE":          true,
	".gitignore":      true,
	".env.example":    true,
	"CONTRIBUTING.md": true,
}

// IsTemplateTarget reports whether path is a well-known new-file template that
// a mutation may create even when the file does not exist yet.
func IsTemplateTarget(path string) bool {
	return templateTargets[path]
}

// ResolveTargetSet converts extracted file paths into the deterministic target
// set. stat reports whether a path exists on disk. Rules:
//
//   - Duplicate paths collapse into one target (first role wins).
//   - Order is first-appearance order — never derived from map iteration.
//   - A non-existent path that is not a well-known template is a
//     deterministic failure (no silent invention).
func ResolveTargetSet(extracted []string, stat func(path string) bool) (TargetResolution, error) {
	var out []Target
	seen := make(map[string]struct{}, len(extracted))
	for _, raw := range extracted {
		path := strings.TrimSpace(raw)
		if path == "" || path == "." || path == "/" {
			continue
		}
		if _, dup := seen[path]; dup {
			continue // duplicate target collapses into one node
		}
		seen[path] = struct{}{}
		exists := false
		if stat != nil {
			exists = stat(path)
		}
		if !exists && !IsTemplateTarget(path) {
			return TargetResolution{}, fmt.Errorf("target does not exist: %s", path)
		}
		out = append(out, Target{Path: path, Role: TargetExplicit, Exists: exists})
	}
	if len(out) == 0 {
		return TargetResolution{
			Ambiguous: true,
			Reason:    "no explicit file target could be resolved",
		}, nil
	}
	return TargetResolution{Targets: out}, nil
}

// CollapseTargets de-duplicates an arbitrary list of (path, role) pairs into
// the deterministic target set. It is used for inferred (keyword-derived)
// targets and any caller that already classified roles.
func CollapseTargets(entries []Target) []Target {
	var out []Target
	seen := make(map[string]struct{}, len(entries))
	for _, t := range entries {
		if t.Path == "" {
			continue
		}
		if _, dup := seen[t.Path]; dup {
			continue
		}
		seen[t.Path] = struct{}{}
		out = append(out, t)
	}
	return out
}
