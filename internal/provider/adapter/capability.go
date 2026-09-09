// Package adapter maps provider-native reasoning capability
// representations for Izen's model registry. It carries no UI dependencies:
// pure domain data consumed by the application service and (in a later
// phase) the TUI model picker.
package adapter

// ReasoningMode is the provider-native reasoning capability representation.
type ReasoningMode string

const (
	// ReasoningModeNone marks models with no reasoning support.
	ReasoningModeNone ReasoningMode = "none"
	// ReasoningModeEnumStandard marks enum-graded reasoning (low, medium, high).
	ReasoningModeEnumStandard ReasoningMode = "enum_standard"
	// ReasoningModeEnumExtended marks extended enum-graded reasoning
	// (low, medium, high, xhigh, max).
	ReasoningModeEnumExtended ReasoningMode = "enum_extended"
	// ReasoningModeToggleAuto marks off/auto/on toggled reasoning.
	ReasoningModeToggleAuto ReasoningMode = "toggle_auto"
	// ReasoningModeFixed marks locked chain-of-thought models (DeepSeek R1).
	ReasoningModeFixed ReasoningMode = "fixed"
)

// Standard reasoning option sets per mode.
var (
	// StandardOptions are the enum_standard grades.
	StandardOptions = []string{"low", "medium", "high"}
	// ExtendedOptions are the enum_extended grades.
	ExtendedOptions = []string{"low", "medium", "high", "xhigh", "max"}
	// ToggleAutoOptions are the toggle_auto states.
	ToggleAutoOptions = []string{"off", "auto", "on"}
)

// ReasoningSelection is a validated reasoning choice for a role binding:
// an option grade plus an optional token budget.
type ReasoningSelection struct {
	Option string `json:"option"`
	Budget *int   `json:"budget,omitempty"`
}

// ProviderCapability describes what a provider/model supports.
type ProviderCapability struct {
	SupportsTools    bool          `json:"supports_tools"`
	SupportsVision   bool          `json:"supports_vision"`
	ReasoningMode    ReasoningMode `json:"reasoning_mode"`
	AvailableOptions []string      `json:"available_options"`
	MinBudget        int           `json:"min_budget"`
	MaxBudget        int           `json:"max_budget"`
}

// OptionsForMode returns the canonical option list for a reasoning mode.
func OptionsForMode(mode ReasoningMode) []string {
	switch mode {
	case ReasoningModeEnumStandard:
		return append([]string(nil), StandardOptions...)
	case ReasoningModeEnumExtended:
		return append([]string(nil), ExtendedOptions...)
	case ReasoningModeToggleAuto:
		return append([]string(nil), ToggleAutoOptions...)
	default:
		return []string{}
	}
}

// IsValidOption reports whether option is legal for mode. ReasoningModeNone
// and ReasoningModeFixed accept no caller-selected option.
func IsValidOption(mode ReasoningMode, option string) bool {
	for _, o := range OptionsForMode(mode) {
		if o == option {
			return true
		}
	}
	return false
}

// CapabilityForProvider returns the default capability record for a known
// provider name. Unknown providers get a conservative tools-only record.
func CapabilityForProvider(name string) ProviderCapability {
	switch name {
	case "anthropic":
		return ProviderCapability{
			SupportsTools:    true,
			SupportsVision:   true,
			ReasoningMode:    ReasoningModeToggleAuto,
			AvailableOptions: OptionsForMode(ReasoningModeToggleAuto),
			MinBudget:        1024,
			MaxBudget:        32000,
		}
	case "openai":
		return ProviderCapability{
			SupportsTools:    true,
			SupportsVision:   true,
			ReasoningMode:    ReasoningModeEnumStandard,
			AvailableOptions: OptionsForMode(ReasoningModeEnumStandard),
			MinBudget:        0,
			MaxBudget:        0,
		}
	case "gemini", "google":
		return ProviderCapability{
			SupportsTools:    true,
			SupportsVision:   true,
			ReasoningMode:    ReasoningModeToggleAuto,
			AvailableOptions: OptionsForMode(ReasoningModeToggleAuto),
			MinBudget:        0,
			MaxBudget:        24576,
		}
	case "deepseek":
		return ProviderCapability{
			SupportsTools:    true,
			SupportsVision:   false,
			ReasoningMode:    ReasoningModeFixed,
			AvailableOptions: []string{},
			MinBudget:        0,
			MaxBudget:        0,
		}
	case "groq", "openrouter", "ollama":
		return ProviderCapability{
			SupportsTools:    true,
			SupportsVision:   false,
			ReasoningMode:    ReasoningModeEnumExtended,
			AvailableOptions: OptionsForMode(ReasoningModeEnumExtended),
			MinBudget:        0,
			MaxBudget:        0,
		}
	default:
		return ProviderCapability{
			SupportsTools:    true,
			SupportsVision:   false,
			ReasoningMode:    ReasoningModeNone,
			AvailableOptions: []string{},
			MinBudget:        0,
			MaxBudget:        0,
		}
	}
}
