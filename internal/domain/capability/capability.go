// Package capability (capability.go) is the unified domain definition for
// authorization state and capability contracts.
//
// It consolidates two legacy concepts that shared a name but not a meaning:
//   - the bitmask Capability + CapabilitySet from internal/core/capability
//     (runtime authorization: what the workspace may do, with scope rules), and
//   - the CapabilityID / ValidationResult / Capability contract from
//     internal/capability (decoupled validation: what a capability is, how it
//     presents itself to models, how it validates artifacts).
//
// The bitmask keeps the canonical name Capability (its callers in ui/ are the
// majority). The validation contract is named CapabilityContract to avoid a
// redeclaration in this package; the legacy internal/capability package
// aliases Capability = CapabilityContract during migration so existing
// callers observe zero behavior change.
//
// The file depends only on the standard library and has zero imports of
// internal/ui, internal/runtime, internal/kernel, or internal/orchestrator.
package capability

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// ── Bitmask authorization (canonical home of core/capability) ─────────────

// Capability represents a single permission bit for runtime authorization.
type Capability int

const (
	CapabilityRead Capability = 1 << iota
	CapabilityWrite
	CapabilityExecute
	CapabilityTest
	CapabilityPatch
	CapabilityCheckpoint
	CapabilityRollback
)

func (c Capability) String() string {
	switch c {
	case CapabilityRead:
		return "read"
	case CapabilityWrite:
		return "write"
	case CapabilityExecute:
		return "execute"
	case CapabilityTest:
		return "test"
	case CapabilityPatch:
		return "patch"
	case CapabilityCheckpoint:
		return "checkpoint"
	case CapabilityRollback:
		return "rollback"
	default:
		return fmt.Sprintf("capability(%d)", int(c))
	}
}

// ScopeRule binds a Capability to zero or more file-path globs or command
// prefixes. An empty Patterns slice means the capability applies globally
// (no scope restriction).
type ScopeRule struct {
	Capability Capability
	Patterns   []string
}

// MatchFile reports whether path matches at least one pattern in the rule.
// A rule with no patterns always matches.
func (r *ScopeRule) MatchFile(path string) bool {
	if len(r.Patterns) == 0 {
		return true
	}
	for _, p := range r.Patterns {
		if matched, _ := filepath.Match(p, path); matched {
			return true
		}
		if strings.Contains(path, p) {
			return true
		}
	}
	return false
}

// MatchCommand reports whether cmd starts with at least one pattern in the
// rule (prefix match). A rule with no patterns always matches.
func (r *ScopeRule) MatchCommand(cmd string) bool {
	if len(r.Patterns) == 0 {
		return true
	}
	lower := strings.ToLower(strings.TrimSpace(cmd))
	for _, p := range r.Patterns {
		if strings.HasPrefix(lower, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

// CapabilitySet manages a set of active capabilities with optional scope
// restrictions.
type CapabilitySet struct {
	granted map[Capability][]ScopeRule
}

// NewCapabilitySet returns an empty CapabilitySet.
func NewCapabilitySet() *CapabilitySet {
	return &CapabilitySet{
		granted: make(map[Capability][]ScopeRule),
	}
}

// Grant adds a capability with optional scope rules. A nil or empty rules
// slice grants the capability globally.
func (cs *CapabilitySet) Grant(c Capability, rules ...ScopeRule) {
	if len(rules) == 0 {
		rules = []ScopeRule{{Capability: c}}
	}
	cs.granted[c] = append(cs.granted[c], rules...)
}

// Deny explicitly removes a capability and all its scope rules.
func (cs *CapabilitySet) Deny(c Capability) {
	delete(cs.granted, c)
}

// Has reports whether the capability is granted (globally or scoped).
func (cs *CapabilitySet) Has(c Capability) bool {
	_, ok := cs.granted[c]
	return ok
}

// CanMutateFile reports whether the given file path is covered by a granted
// CapabilityWrite or CapabilityPatch rule.
func (cs *CapabilitySet) CanMutateFile(path string) bool {
	for _, cap := range []Capability{CapabilityWrite, CapabilityPatch} {
		rules, ok := cs.granted[cap]
		if !ok {
			continue
		}
		for _, r := range rules {
			if r.MatchFile(path) {
				return true
			}
		}
	}
	return false
}

// CanExecuteCommand reports whether the given command is covered by a granted
// CapabilityExecute rule.
func (cs *CapabilitySet) CanExecuteCommand(cmd string) bool {
	rules, ok := cs.granted[CapabilityExecute]
	if !ok {
		return false
	}
	for _, r := range rules {
		if r.MatchCommand(cmd) {
			return true
		}
	}
	return false
}

// CanRead reports whether CapabilityRead is granted.
func (cs *CapabilitySet) CanRead() bool { return cs.Has(CapabilityRead) }

// CanWrite reports whether CapabilityWrite is granted.
func (cs *CapabilitySet) CanWrite() bool { return cs.Has(CapabilityWrite) }

// CanTest reports whether CapabilityTest is granted.
func (cs *CapabilitySet) CanTest() bool { return cs.Has(CapabilityTest) }

// CanPatch reports whether CapabilityPatch is granted.
func (cs *CapabilitySet) CanPatch() bool { return cs.Has(CapabilityPatch) }

// CanCheckpoint reports whether CapabilityCheckpoint is granted.
func (cs *CapabilitySet) CanCheckpoint() bool { return cs.Has(CapabilityCheckpoint) }

// CanRollback reports whether CapabilityRollback is granted.
func (cs *CapabilitySet) CanRollback() bool { return cs.Has(CapabilityRollback) }

// GrantFromSet copies all capabilities and scope rules from another set.
func (cs *CapabilitySet) GrantFromSet(src *CapabilitySet) {
	for cap, rules := range src.granted {
		cs.granted[cap] = append(cs.granted[cap], rules...)
	}
}

// ── Decoupled validation contract (canonical home of capability) ───────────

// CapabilityID uniquely identifies a capability within a Registry.
type CapabilityID string

// String returns the machine-readable id label.
func (c CapabilityID) String() string { return string(c) }

// ValidationResult is the outcome of a capability validating an artifact.
// It is deliberately evidence-shaped: a boolean verdict plus the exact
// reasons behind it, never a floating confidence score.
type ValidationResult struct {
	// Passed reports whether validation succeeded.
	Passed bool
	// Reasons lists the human-readable justifications for the verdict.
	Reasons []string
}

// Pass returns a passing ValidationResult. Optional reasons document why.
func Pass(reasons ...string) ValidationResult {
	return ValidationResult{Passed: true, Reasons: append([]string(nil), reasons...)}
}

// Fail returns a failing ValidationResult carrying the rejection reasons.
func Fail(reasons ...string) ValidationResult {
	return ValidationResult{Passed: false, Reasons: append([]string(nil), reasons...)}
}

// Failed reports whether validation rejected the artifact.
func (v ValidationResult) Failed() bool { return !v.Passed }

// CapabilityContract is the single-source-of-truth contract for one
// workspace capability. Implementations are decoupled: each capability
// defines its own prompt representation and its own validation logic.
//
// It is named Contract (rather than Capability) because the bitmask
// authorization above owns the Capability name in this package. The legacy
// internal/capability package aliases Capability = CapabilityContract so its
// callers keep compiling unchanged.
type CapabilityContract interface {
	// ID returns the unique identifier of the capability.
	ID() CapabilityID
	// Description summarizes what the capability provides.
	Description() string
	// PromptRepresentation renders the capability as instructions for a
	// model. modelTier selects between e.g. "small" and "full" verbosity.
	PromptRepresentation(modelTier string) string
	// Validate checks an artifact's data against this capability's rules and
	// returns a deterministic ValidationResult.
	Validate(ctx context.Context, data []byte) ValidationResult
	// RuntimeRequirements lists the tools/services the capability needs at
	// runtime (e.g. "go toolchain", "docker").
	RuntimeRequirements() []string
}
