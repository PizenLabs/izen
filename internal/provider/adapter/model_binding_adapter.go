package adapter

import (
	"github.com/PizenLabs/izen/internal/runtime/authority"
)

// ModelBindingAdapter maps an authority.ModelBinding directly onto provider
// payload injection. It eliminates adapter defaults by consuming the binding
// verbatim and rejecting empty bindings.
func ModelBindingAdapter(binding authority.ModelBinding) (providerID, modelID string, valid bool) {
	if binding.ModelID == "" {
		return "", "", false
	}
	if !IsCompatibleProviderModel(string(binding.ProviderID), string(binding.ModelID)) {
		return "", "", false
	}
	return string(binding.ProviderID), string(binding.ModelID), true
}
