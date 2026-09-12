package role

import (
	"fmt"
	"sort"
	"strings"
)

// Core operational roles managed by Izen.
const (
	RoleDefault = "default"
	RolePlan    = "plan"
	RoleSmol    = "smol"
	RoleVision  = "vision"
	RoleAdviser = "adviser"
)

// ValidRoles is the closed set of resolvable role names.
var ValidRoles = []string{RoleDefault, RolePlan, RoleSmol, RoleVision, RoleAdviser}

// AdviserMinContext is the preferred minimum context window for the adviser
// role (code review & security audit).
const AdviserMinContext = 128000

// smolPatterns match fast/low-latency model IDs (case-insensitive substring).
var smolPatterns = []string{
	"smol", "mini", "flash", "haiku", "nano",
	"7b", "8b", "3b", "1b", "lite", "turbo",
}

// ResolveRoleModel resolves the model for roleName from provider-agnostic
// inputs:
//
//  1. Exact role binding in bindings[roleName] (matched against models by
//     exact ID, then case-insensitive ID, then Name). A bound ID absent
//     from models is an error — the engine must not silently substitute.
//  2. Heuristic capability fallback when no explicit model is bound:
//     plan → thinking model; smol → fast model; vision → vision model
//     (required); adviser → largest context ≥128k preferred; default →
//     tool-capable model, else first.
//
// An empty model list is always an error. A nil bindings map is treated as
// "no explicit bindings". Callers outside internal/domain translate concrete
// config/registry records into these primitives via the adapter layer.
func ResolveRoleModel(roleName string, bindings map[string]string, models []ModelDescriptor) (ModelDescriptor, error) {
	if len(models) == 0 {
		return ModelDescriptor{}, fmt.Errorf("role %q: model list is empty", roleName)
	}

	// Step 1: exact role binding.
	if bound, ok := bindings[roleName]; ok && strings.TrimSpace(bound) != "" {
		if m, found := lookupModel(models, strings.TrimSpace(bound)); found {
			return EnrichDescriptor(m), nil
		}
		return ModelDescriptor{}, fmt.Errorf("role %q: bound model %q not found in registry", roleName, bound)
	}

	// Step 2: heuristic fallback.
	switch roleName {
	case RolePlan:
		if m, ok := firstThinking(models); ok {
			return EnrichDescriptor(m), nil
		}
		// No thinking model available: fall back to a general model rather
		// than failing — plan must always resolve when the model list is
		// non-empty.
		return EnrichDescriptor(models[0]), nil
	case RoleSmol:
		return EnrichDescriptor(firstSmol(models)), nil
	case RoleVision:
		if m, ok := firstWithCap(models, CapVision); ok {
			return EnrichDescriptor(m), nil
		}
		return ModelDescriptor{}, fmt.Errorf("role %q: no vision-capable model in registry", roleName)
	case RoleAdviser:
		return EnrichDescriptor(largestContext(models)), nil
	case RoleDefault, "":
		if m, ok := firstWithCap(models, CapTools); ok {
			return EnrichDescriptor(m), nil
		}
		return EnrichDescriptor(models[0]), nil
	default:
		return ModelDescriptor{}, fmt.Errorf("unknown role %q (valid: %s)", roleName, strings.Join(ValidRoles, ", "))
	}
}

// IsValidRole reports whether name is one of the 5 core roles.
func IsValidRole(name string) bool {
	for _, r := range ValidRoles {
		if name == r {
			return true
		}
	}
	return false
}

// lookupModel finds a descriptor by exact ID, case-insensitive ID, then Name.
func lookupModel(models []ModelDescriptor, id string) (ModelDescriptor, bool) {
	for _, m := range models {
		if m.ID == id {
			return m, true
		}
	}
	for _, m := range models {
		if strings.EqualFold(m.ID, id) {
			return m, true
		}
	}
	for _, m := range models {
		if m.Name == id || strings.EqualFold(m.Name, id) {
			return m, true
		}
	}
	return ModelDescriptor{}, false
}

// firstThinking returns the first model flagged thinking by stored fields or
// the classifier.
func firstThinking(models []ModelDescriptor) (ModelDescriptor, bool) {
	for _, m := range models {
		if EffectiveIsThinking(m) || HasCapability(EffectiveCapabilities(m), CapThinking) {
			return m, true
		}
	}
	return ModelDescriptor{}, false
}

// firstWithCap returns the first model with the given effective capability.
func firstWithCap(models []ModelDescriptor, cap ModelCapability) (ModelDescriptor, bool) {
	for _, m := range models {
		if HasCapability(EffectiveCapabilities(m), cap) {
			return m, true
		}
	}
	return ModelDescriptor{}, false
}

// firstSmol returns the first fast/low-latency model, else the first model.
func firstSmol(models []ModelDescriptor) ModelDescriptor {
	lower := make([]string, len(models))
	for i, m := range models {
		lower[i] = strings.ToLower(m.ID + " " + m.Name)
	}
	for i, l := range lower {
		for _, p := range smolPatterns {
			if strings.Contains(l, p) {
				return models[i]
			}
		}
	}
	return models[0]
}

// largestContext returns the model with the largest context window,
// preferring ≥AdviserMinContext but always resolving when non-empty.
func largestContext(models []ModelDescriptor) ModelDescriptor {
	sorted := append([]ModelDescriptor(nil), models...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].ContextWindow > sorted[j].ContextWindow
	})
	for _, m := range sorted {
		if m.ContextWindow >= AdviserMinContext {
			return m
		}
	}
	return sorted[0]
}
