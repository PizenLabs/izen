package registry

import (
	"fmt"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/domain/role"
)

// ToRoleDescriptor converts a provider registry record into the
// provider-agnostic domain shape. The domain layer never imports this
// package; the conversion lives here (outer adapter → inner domain).
func ToRoleDescriptor(d ModelDescriptor) role.ModelDescriptor {
	caps := make([]role.ModelCapability, 0, len(d.Capabilities))
	for _, c := range d.Capabilities {
		caps = append(caps, role.ModelCapability(string(c)))
	}
	return role.ModelDescriptor{
		ID:              d.ID,
		Provider:        d.Provider,
		Name:            d.Name,
		ContextWindow:   d.ContextWindow,
		MaxOutputTokens: d.MaxOutputTokens,
		Capabilities:    caps,
		IsThinking:      d.IsThinking,
		InputCostPerM:   d.InputCostPerM,
		OutputCostPerM:  d.OutputCostPerM,
	}
}

// FromRoleDescriptor converts a domain descriptor back into a registry
// record (e.g. for display after role resolution).
func FromRoleDescriptor(d role.ModelDescriptor) ModelDescriptor {
	caps := make([]ModelCapability, 0, len(d.Capabilities))
	for _, c := range d.Capabilities {
		caps = append(caps, ModelCapability(string(c)))
	}
	return ModelDescriptor{
		ID:              d.ID,
		Provider:        d.Provider,
		Name:            d.Name,
		ContextWindow:   d.ContextWindow,
		MaxOutputTokens: d.MaxOutputTokens,
		Capabilities:    caps,
		IsThinking:      d.IsThinking,
		InputCostPerM:   d.InputCostPerM,
		OutputCostPerM:  d.OutputCostPerM,
	}
}

// ToRoleDescriptors maps a slice of registry records to domain shapes.
func ToRoleDescriptors(models []ModelDescriptor) []role.ModelDescriptor {
	out := make([]role.ModelDescriptor, 0, len(models))
	for _, m := range models {
		out = append(out, ToRoleDescriptor(m))
	}
	return out
}

// ResolveRoleModel resolves a role against a live Registry and cascade
// config by translating both into domain primitives first. It is the only
// sanctioned path for outer layers to reach role.ResolveRoleModel.
func ResolveRoleModel(roleName string, cfg *config.CascadeConfig, reg *Registry) (ModelDescriptor, error) {
	if reg == nil {
		return ModelDescriptor{}, fmt.Errorf("role %q: nil model registry", roleName)
	}
	models := ToRoleDescriptors(reg.Snapshot())
	var bindings map[string]string
	if cfg != nil {
		bindings = cfg.Roles
	}
	resolved, err := role.ResolveRoleModel(roleName, bindings, models)
	if err != nil {
		return ModelDescriptor{}, err
	}
	return FromRoleDescriptor(resolved), nil
}

// EffectiveCapabilitiesOf reports the effective domain capabilities for a
// registry record without leaking provider types into the domain.
func EffectiveCapabilitiesOf(d ModelDescriptor) []role.ModelCapability {
	return role.EffectiveCapabilities(ToRoleDescriptor(d))
}

// EffectiveIsThinkingOf reports the effective thinking flag for a record.
func EffectiveIsThinkingOf(d ModelDescriptor) bool {
	return role.EffectiveIsThinking(ToRoleDescriptor(d))
}
