package execution

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ── P0 Execution Core — Typed Artifact Errors ────────────────────────────────
//
// These sentinels are the typed contract-rejection taxonomy spoken at the
// artifact boundary. Callers MUST branch on them with errors.Is; every
// rejection maps to a single canonical failure subtype and therefore to a
// single recovery decision.
//
//	ErrFormatRejected    raw bytes carry no valid artifact contract
//	ErrAmbiguousAnchor   SEARCH anchor matches zero or multiple regions
//	ErrScopeViolation    patch escapes its declared target / region
var (
	ErrFormatRejected  = errors.New("artifact: format rejected — no valid contract")
	ErrAmbiguousAnchor = errors.New("artifact: ambiguous anchor — SEARCH matches zero or multiple regions")
	ErrScopeViolation  = errors.New("artifact: scope violation — patch escapes authorized boundary")
)

// BoundedPatch is the validated, scope-bounded mutation artifact produced by
// an ArtifactValidator. It is the ONLY shape the execution loops may apply:
// a single target file, a verbatim SEARCH anchor and its replacement, plus the
// canonical normalized bytes that will be written on commit.
type BoundedPatch struct {
	Target  string
	Search  string
	Replace string
	Content []byte
	Raw     []byte
}

// ArtifactValidator is the explicit boundary between raw model output and the
// execution authority. Implementations MUST return typed errors
// (ErrFormatRejected / ErrAmbiguousAnchor / ErrScopeViolation) so the
// execution loops can fail closed without string parsing.
//
// The interface is intentionally narrow: P1 Patch Normalization will be added
// as a decorating NormalizingValidator without touching internal/executor
// execution loops.
type ArtifactValidator interface {
	ValidateArtifact(raw []byte, target string) (*BoundedPatch, error)
}

// MutationBoundary is the strict workspace-integrity assertion surface.
// After ANY rollback the caller MUST recompute the tree digest and compare it
// against the base digest that was captured before the first mutation. Any
// mismatch halts execution with a critical DigestMismatchError.
type MutationBoundary interface {
	AssertWorkspaceIntegrity(baseDigest string) error
}

// DigestMismatchError is returned when post-rollback workspace state does not
// match base_tree_digest. It is a critical system diagnostic — execution must
// halt immediately and never report success.
type DigestMismatchError struct {
	Expected string
	Actual   string
	Targets  []string
}

func (e *DigestMismatchError) Error() string {
	return fmt.Sprintf("boundary: digest mismatch — expected %s got %s (targets=%v)", e.Expected, e.Actual, e.Targets)
}

// DefaultArtifactValidator is the production ArtifactValidator. It validates
// against the established patch protocol (SEARCH/REPLACE, unified diff) using
// the existing parser helpers plus the V3 content gate for language syntax.
// It returns the typed sentinels above so callers never branch on strings.
type DefaultArtifactValidator struct {
	pipeline *V3ArtifactPipeline
}

// NewDefaultArtifactValidator returns a validator wired to the shared V3
// pipeline (HTML/JSON/Go) and the bounded-patch extraction helpers.
func NewDefaultArtifactValidator() *DefaultArtifactValidator {
	return &DefaultArtifactValidator{pipeline: NewV3ArtifactPipeline()}
}

// ValidateArtifact validates raw against target and returns a bounded patch or
// a typed rejection. The validation order is strict: format → scope → anchor →
// syntax.
func (v *DefaultArtifactValidator) ValidateArtifact(raw []byte, target string) (*BoundedPatch, error) {
	if v == nil {
		return nil, fmt.Errorf("%w: validator not configured", ErrFormatRejected)
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, fmt.Errorf("%w: empty artifact", ErrFormatRejected)
	}
	// Scope guard: absolute or traversal paths are never valid artifacts.
	cleaned := filepath.Clean(strings.TrimSpace(target))
	if target == "" {
		return nil, fmt.Errorf("%w: empty target", ErrScopeViolation)
	}
	if filepath.IsAbs(target) || strings.Contains(cleaned, "..") || cleaned == "." || cleaned == "/" {
		return nil, fmt.Errorf("%w: target %q escapes workspace", ErrScopeViolation, target)
	}
	// Scope guard: bounded patches must not claim to touch files outside their
	// declared target (multi-target bleed). The raw multi-file diff parser
	// would reveal it; we reject injected headers here.
	if strings.Contains(trimmed, "--- a/") && !strings.Contains(trimmed, "--- a/"+filepath.Base(target)) && !strings.Contains(trimmed, "--- a/"+target) {
		// Conservative: if raw contains any foreign file header not matching
		// target, treat as scope violation rather than silent bleed.
		// However only enforce when raw clearly contains multiple headers;
		// single foreign header is also a violation.
		foreign := false
		for _, line := range strings.Split(trimmed, "\n") {
			if strings.HasPrefix(line, "--- a/") {
				p := strings.TrimSpace(strings.TrimPrefix(line, "--- a/"))
				if p != target && filepath.Base(p) != filepath.Base(target) {
					foreign = true
					break
				}
			}
		}
		if foreign {
			return nil, fmt.Errorf("%w: artifact targets foreign file", ErrScopeViolation)
		}
	}
	// Format gate: raw must contain a recognizable patch structure OR be a
	// valid full-file artifact for a registered language (e.g. HTML/JSON/Go).
	// Unregistered languages with no markers are strictly rejected as format.
	hasSearch := strings.Contains(trimmed, "<<<<<<< SEARCH")
	hasDiff := strings.Contains(trimmed, "@@")
	hasFileCreate := strings.Contains(trimmed, "<<<<<<< FILE_CREATE")
	if !hasSearch && !hasDiff && !hasFileCreate {
		tag := ValidatorTagForPath(target)
		if tag == "" {
			return nil, fmt.Errorf("%w: missing SEARCH/REPLACE or unified diff markers", ErrFormatRejected)
		}
		// Registered language: allow full-file content, validate syntax.
		// Empty payload already handled; validate the raw as full-file.
		if v.pipeline != nil {
			if gate := v.pipeline.ValidateContent(target, []byte(trimmed), 0); !gate.Passed {
				return nil, fmt.Errorf("%w: %w", ErrFormatRejected, gate.Error)
			}
		}
		bp := BoundedPatch{Target: target, Content: []byte(trimmed), Raw: raw}
		return &bp, nil
	}
	// Ambiguous anchor detection: SEARCH block with empty search or duplicate
	// anchors that would match multiple regions.
	if hasSearch {
		blocks := ParseSearchReplaceBlocks(trimmed)
		if len(blocks) == 0 {
			return nil, fmt.Errorf("%w: malformed SEARCH block", ErrFormatRejected)
		}
		for _, b := range blocks {
			if strings.TrimSpace(b.search) == "" {
				return nil, fmt.Errorf("%w: empty SEARCH anchor", ErrAmbiguousAnchor)
			}
		}
		// If SEARCH text appears to be ambiguous (e.g., single common token),
		// the caller would need original content to count matches. Since this
		// validator is intentionally original-free, we flag the trivial 1-char
		// anchor as ambiguous.
		for _, b := range blocks {
			if len(strings.TrimSpace(b.search)) < 2 {
				return nil, fmt.Errorf("%w: anchor too short to be unambiguous", ErrAmbiguousAnchor)
			}
		}
		if len(blocks) > 1 {
			seen := make(map[string]bool, len(blocks))
			for _, b := range blocks {
				if seen[b.search] {
					return nil, fmt.Errorf("%w: duplicate SEARCH anchor %q", ErrAmbiguousAnchor, b.search)
				}
				seen[b.search] = true
			}
		}
	}
	// Build bounded patch: prefer SEARCH/REPLACE extraction when present;
	// otherwise treat the raw as the unified-diff payload.
	var bp BoundedPatch
	bp.Target = target
	bp.Raw = raw
	if hasSearch {
		blocks := ParseSearchReplaceBlocks(trimmed)
		// Use first block's search/replace as canonical window; additional
		// blocks are preserved in Content for ApplySearchReplaceBlocks.
		if len(blocks) > 0 {
			bp.Search = blocks[0].search
			bp.Replace = blocks[0].replace
		}
		bp.Content = []byte(trimmed)
	} else {
		bp.Content = []byte(trimmed)
	}
	// Language syntax gate when pipeline is available: run the V3 validator
	// over the resolved content for known languages. For bounded patches we
	// validate the REPLACE text; for diffs we skip syntax until after apply
	// preview (the diff itself is not syntax). We conservatively validate
	// only when a file extension maps to a known validator and the content
	// looks like full-file text.
	if v.pipeline != nil && ValidatorTagForPath(target) != "" && len(bp.Content) > 0 && !hasDiff {
		// For SEARCH patches, validate the replacement payload, not the
		// protocol wrapper.
		payload := bp.Replace
		if payload == "" {
			payload = string(bp.Content)
		}
		if payload != "" {
			if gate := v.pipeline.ValidateContent(target, []byte(payload), 0); !gate.Passed {
				return nil, fmt.Errorf("%w: %w", ErrFormatRejected, gate.Error)
			}
		}
	}
	return &bp, nil
}

// Ensure interfaces are satisfied at compile time.
var _ ArtifactValidator = (*DefaultArtifactValidator)(nil)
