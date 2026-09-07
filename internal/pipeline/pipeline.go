package pipeline

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/PizenLabs/izen/internal/compiler"
	"github.com/PizenLabs/izen/internal/discovery"
	"github.com/PizenLabs/izen/internal/resolver"
)

// Re-export sentinel errors for callers using errors.Is against pipeline package.
var (
	ErrDiscoveryControlField = discovery.ErrDiscoveryControlField
	ErrCreateConflict        = compiler.ErrCreateConflict
	ErrDeleteConflict        = compiler.ErrDeleteConflict
	ErrTargetNotFound        = compiler.ErrTargetNotFound
	ErrAmbiguousIntent       = compiler.ErrAmbiguousIntent
	ErrOperationConflict     = compiler.ErrOperationConflict
	ErrPathTraversal         = resolver.ErrPathTraversal
	ErrOutsideWorkspace      = resolver.ErrOutsideWorkspace
)

// SemanticHypothesis is the pipeline entry hypothesis.
// RawJSON, if non-empty, is parsed via discovery.ParseAndSanitize.
// Otherwise IntentVerb + References are used directly (for direct unit tests).
type SemanticHypothesis struct {
	RawJSON         []byte
	PerceivedIntent string
	IntentVerb      string
	Features        []string
	References      []discovery.ReferenceHypothesis
	ExplicitTargets []string
}

// WorkspaceSnapshot is the observed workspace state at planning time.
// Root is mandatory. ExistingFiles, if non-nil, overrides OS stat for determinism in tests.
type WorkspaceSnapshot struct {
	Root          string
	ExistingFiles map[string]bool // key: workspace-relative or absolute path; value: exists
}

// RunnerPolicy is the authorization context. Execution must not proceed without explicit permission.
type RunnerPolicy struct {
	// AllowDiscovered controls whether discovered references may become executable tasks.
	// Explicit targets are always considered authorized if they pass containment.
	AllowDiscovered bool
	// AllowExplicit controls explicit target authorization (defaults to true if zero-value).
	AllowExplicit *bool
}

// ExecutableTask is the only type allowed to cross the execution boundary.
type ExecutableTask struct {
	Path      string
	Operation compiler.OpType
	Authority compiler.Authority
}

// BuildExecutablePlan implements the 6-stage unidirectional pipeline:
// SemanticIntent -> Reference -> ResolvedTarget -> DerivedOperation -> AuthorizationDecision -> ExecutableTask
// It fails closed on any violation: ErrDiscoveryControlField, PathGuard violation, ErrOperationConflict.
// Zero tasks are returned on error (topological fail-closed).
func BuildExecutablePlan(hypo SemanticHypothesis, ws WorkspaceSnapshot, policy RunnerPolicy) ([]ExecutableTask, error) {
	// Stage 1: SemanticIntent — parse & sanitize
	var verb compiler.IntentVerb
	var refs []discovery.ReferenceHypothesis

	if len(hypo.RawJSON) > 0 {
		p, err := discovery.ParseAndSanitize(hypo.RawJSON)
		if err != nil {
			return nil, err // fail-closed: no resolved targets, no tasks
		}
		verb = compiler.NormalizeVerb(p.IntentVerb)
		refs = p.References
	} else {
		// Direct hypothesis without raw JSON (used by table-driven tests)
		verb = compiler.NormalizeVerb(hypo.IntentVerb)
		refs = hypo.References
	}

	// Validate workspace root
	if ws.Root == "" {
		return nil, fmt.Errorf("pipeline: empty workspace root")
	}
	guard, err := resolver.NewPathGuard(ws.Root)
	if err != nil {
		return nil, err
	}

	// Stage 2 & 3: Reference -> ResolvedTarget (with PathGuard + existence)
	type resolved struct {
		target compiler.ResolvedTarget
		verb   compiler.IntentVerb
	}
	var resolvedTargets []resolved

	// Helper to check existence: use ExistingFiles map if provided, else OS stat on sanitized path.
	existsFn := func(sanitizedAbs string) bool {
		if ws.ExistingFiles != nil {
			// Check exact absolute path
			if v, ok := ws.ExistingFiles[sanitizedAbs]; ok {
				return v
			}
			// Also check relative to root
			rel, err := filepath.Rel(guard.Root(), sanitizedAbs)
			if err == nil {
				if v, ok := ws.ExistingFiles[rel]; ok {
					return v
				}
				if v, ok := ws.ExistingFiles[filepath.ToSlash(rel)]; ok {
					return v
				}
			}
			// Check with forward slashes
			if v, ok := ws.ExistingFiles[filepath.ToSlash(sanitizedAbs)]; ok {
				return v
			}
			// If map provided but key not found, fallback to false (test expects deterministic)
			// However if map is provided for some files, unknown files should be considered not exists
			// To keep behavior predictable, if map non-nil, absence means not exists.
			return false
		}
		// OS check
		_, err := os.Stat(sanitizedAbs)
		return err == nil
	}

	// Explicit user targets (@path) -> AuthorityExplicit
	for _, raw := range hypo.ExplicitTargets {
		sanitized, err := guard.SanitizePath(raw)
		if err != nil {
			return nil, fmt.Errorf("pipeline: explicit target %q: %w", raw, err)
		}
		exists := existsFn(sanitized)
		rt := compiler.ResolvedTarget{
			Path:      sanitized,
			Exists:    exists,
			Authority: compiler.AuthorityExplicit,
		}
		resolvedTargets = append(resolvedTargets, resolved{target: rt, verb: verb})
	}

	// Discovered references -> AuthorityDiscovered
	for _, r := range refs {
		sanitized, err := guard.SanitizePath(r.Path)
		if err != nil {
			// PathGuard violation -> fail closed, no tasks
			return nil, fmt.Errorf("pipeline: discovered reference %q: %w", r.Path, err)
		}
		exists := existsFn(sanitized)
		rt := compiler.ResolvedTarget{
			Path:      sanitized,
			Exists:    exists,
			Authority: compiler.AuthorityDiscovered,
		}
		resolvedTargets = append(resolvedTargets, resolved{target: rt, verb: verb})
	}

	// Stage 4: DerivedOperation — deterministic state machine
	var derived []struct {
		target compiler.ResolvedTarget
		op     compiler.OpType
	}
	for _, rt := range resolvedTargets {
		op, err := compiler.DeriveOperation(rt.verb, rt.target)
		if err != nil {
			// Conflict preservation: do NOT downgrade, do NOT noop — return structured error, zero tasks
			return nil, err
		}
		derived = append(derived, struct {
			target compiler.ResolvedTarget
			op     compiler.OpType
		}{target: rt.target, op: op})
	}

	// Stage 5: AuthorizationDecision — independent of resolution
	// Explicit targets: authorized if AllowExplicit is nil or true
	// Discovered references never grant mutation authority unless policy.AllowDiscovered == true (fail-closed default).
	allowExplicit := true
	if policy.AllowExplicit != nil {
		allowExplicit = *policy.AllowExplicit
	}
	for _, d := range derived {
		switch d.target.Authority {
		case compiler.AuthorityExplicit:
			if !allowExplicit {
				return nil, fmt.Errorf("pipeline: authorization denied for explicit target %s", d.target.Path)
			}
		case compiler.AuthorityDiscovered:
			if !policy.AllowDiscovered {
				return nil, fmt.Errorf("pipeline: authorization denied for discovered target %s: discovered references require explicit authorization", d.target.Path)
			}
		}
	}

	// Stage 6: ExecutableTask — only authorized tasks cross boundary
	// Execution-time confinement note: PathGuard checks at planning time do NOT replace OS-level IO confinement at execution time.
	// Runner must re-validate at execution (TOCTOU protection) — not done here.
	var tasks []ExecutableTask
	for _, d := range derived {
		tasks = append(tasks, ExecutableTask{
			Path:      d.target.Path,
			Operation: d.op,
			Authority: d.target.Authority,
		})
	}

	// Ensure we never return nil slice vs empty slice confusion — return empty slice on success with no targets.
	if tasks == nil {
		tasks = []ExecutableTask{}
	}
	return tasks, nil
}

// Ensure errors.Is works for wrapped sentinel errors from subpackages.
// This helper wraps errors to preserve sentinel via %w; already done above with fmt.Errorf.
var _ = errors.Is
