package adapter

import (
	"errors"
	"fmt"
)

// ErrIncompatibleProviderSwitch is returned when the active model is not
// compatible with a new active provider. The engine must never combine
// newProvider + oldModel silently.
var ErrIncompatibleProviderSwitch = errors.New("adapter: incompatible provider switch: active model does not belong to new provider")

// ErrEmptyModelBinding is returned when a ModelBinding carries an empty ModelID.
var ErrEmptyModelBinding = errors.New("adapter: model binding has empty model id")

// ProviderSwitchGuard validates provider/model compatibility when switching
// providers. If the new provider is incompatible with the current active model,
// the guard marks the active model invalid and demands explicit activation.
func ProviderSwitchGuard(newProvider, currentModel string) error {
	if newProvider == "" {
		return fmt.Errorf("%w: empty provider", ErrIncompatibleProviderSwitch)
	}
	if currentModel == "" {
		return fmt.Errorf("%w: empty active model", ErrIncompatibleProviderSwitch)
	}
	if !IsCompatibleProviderModel(newProvider, currentModel) {
		return fmt.Errorf("%w: model %q is incompatible with provider %q", ErrIncompatibleProviderSwitch, currentModel, newProvider)
	}
	return nil
}

// IsCompatibleProviderModel checks whether a model ID is compatible with a
// provider. It uses the same schema rules enforced by the execution boundary.
func IsCompatibleProviderModel(providerName, model string) bool {
	switch providerName {
	case "ollama":
		// Local models must not carry a vendor prefix (OpenRouter-style).
		return !containsSlashVendorPrefix(model)
	case "openrouter":
		// OpenRouter requires vendor/model schema.
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
