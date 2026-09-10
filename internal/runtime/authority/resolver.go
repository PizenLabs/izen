package authority

import (
	"errors"
	"fmt"
)

// ProviderID identifies the execution provider (e.g. "ollama", "openrouter").
type ProviderID string

// ModelID identifies the concrete model (e.g. "claude-sonnet-4-20250514").
type ModelID string

// VariantOption selects a reasoning grade or variant within a binding.
type VariantOption string

// IntentRole classifies the semantic role of a user intent.
type IntentRole string

const (
	RoleThinking IntentRole = "thinking"
	RoleCoder    IntentRole = "coder"
	RoleFast     IntentRole = "fast"
)

// Intent maps to the role that selects the policy binding.
func RoleForIntent(name string) IntentRole {
	switch name {
	case "plan", "investigate":
		return RoleThinking
	case "build":
		return RoleCoder
	case "ask", "review", "commit":
		return RoleFast
	default:
		return RoleFast
	}
}

// ModelBinding is the atomic executable binding: NEVER separate ProviderID
// and ModelID. The tuple must always travel together.
type ModelBinding struct {
	ProviderID    ProviderID
	ModelID       ModelID
	VariantParams VariantOption
}

// ModelState is the single runtime model state read by the resolver.
// It carries NO policy authority — only the active binding selected by
// the user/session at this moment.
type ModelState struct {
	ActiveProvider ProviderID
	ActiveModel    ModelID
	ActiveVariant  VariantOption
}

// ModelPolicy is the read-only policy configuration loaded from settings.
// It defines the role-bound bindings (never a bare model or provider).
type ModelPolicy struct {
	Thinking *ModelBinding
	Coder    *ModelBinding
	Fast     *ModelBinding
}

// ErrUnassignedModel signals an empty ModelID in the resolved tuple.
var ErrUnassignedModel = errors.New("authority: unassigned model")

// ErrProviderModelMismatch signals a model that does not belong to the
// provider that will serve it.
var ErrProviderModelMismatch = errors.New("authority: provider/model mismatch")

// ResolveModel implements the pure, stateless Policy Resolver.
// Rules:
//   1. Explicit Policy Override: if policy defines a binding for the role,
//      return that complete tuple.
//   2. Default Active Model: otherwise return the runtime active tuple.
//   3. Tuple Integrity: NEVER combine policy.Model with runtime.ActiveProvider.
//   4. Missing Binding: if ModelID is empty, return ErrUnassignedModel.
//   5. Invalid Model/Provider: validate compatibility; on mismatch return
//      ErrProviderModelMismatch.
func ResolveModel(intent string, runtime ModelState, policy ModelPolicy) (ModelBinding, error) {
	role := RoleForIntent(intent)

	// Rule 1: Explicit Policy Override (complete binding only)
	var binding *ModelBinding
	switch role {
	case RoleThinking:
		binding = policy.Thinking
	case RoleCoder:
		binding = policy.Coder
	case RoleFast:
		binding = policy.Fast
	}

	if binding != nil {
		if binding.ModelID == "" {
			return ModelBinding{}, fmt.Errorf("%w: role %q has empty model", ErrUnassignedModel, role)
		}
		if !modelCompatible(string(binding.ProviderID), string(binding.ModelID)) {
			return ModelBinding{}, fmt.Errorf("%w: model %q does not belong to provider %q",
				ErrProviderModelMismatch, binding.ModelID, binding.ProviderID)
		}
		return *binding, nil
	}

	// Rule 2: Default Active Model (atomic tuple from runtime)
	b := ModelBinding{
		ProviderID:    runtime.ActiveProvider,
		ModelID:       runtime.ActiveModel,
		VariantParams: runtime.ActiveVariant,
	}

	// Rule 4: Missing Binding check
	if b.ModelID == "" {
		return ModelBinding{}, fmt.Errorf("%w [%s]", ErrUnassignedModel, intent)
	}

	// Rule 5: Provider/Model compatibility
	if !modelCompatible(string(b.ProviderID), string(b.ModelID)) {
		return ModelBinding{}, fmt.Errorf("%w: model %q does not belong to provider %q",
			ErrProviderModelMismatch, b.ModelID, b.ProviderID)
	}

	return b, nil
}

// modelCompatible performs the local validation of model/provider pairing.
// It uses the same schema rules enforced by the executor boundary.
func modelCompatible(providerName, model string) bool {
	switch providerName {
	case "ollama":
		// Local models must not carry a vendor prefix (OpenRouter-style)
		return !containsSlashVendorPrefix(model)
	case "openrouter":
		// OpenRouter requires vendor/model schema
		return containsSlashVendorPrefix(model)
	case "":
		return false
	default:
		return model != ""
	}
}

func containsSlashVendorPrefix(s string) bool {
	for i, c := range s {
		if c == '/' {
			return i > 0 && len(s) > i+1
		}
	}
	return false
}
