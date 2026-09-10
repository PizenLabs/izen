package capability

// ReasoningSupportState represents the explicit reasoning capability state
// for a model. Unknown capabilities are never collapsed to Unsupported.
type ReasoningSupportState int

const (
	ReasoningUnknown     ReasoningSupportState = iota // Capability metadata absent
	ReasoningUnsupported                              // Provider explicitly reports no reasoning
	ReasoningSupported                                // Provider explicitly reports reasoning
)

// String returns the machine-readable label.
func (s ReasoningSupportState) String() string {
	switch s {
	case ReasoningSupported:
		return "supported"
	case ReasoningUnsupported:
		return "unsupported"
	default:
		return "unknown"
	}
}

// IsExplicit reports whether the state is explicitly declared (Supported or
// Unsupported) rather than Unknown.
func (s ReasoningSupportState) IsExplicit() bool {
	return s == ReasoningSupported || s == ReasoningUnsupported
}

// CapabilityTruth describes the truthful reasoning capability for a model.
// It preserves the distinction between Unknown (absent metadata) and
// Unsupported (explicit denial), and never infers grades.
type CapabilityTruth struct {
	Reasoning    ReasoningSupportState
	Configurable bool
	Options      []string
}

// ToCapabilityTruth builds an explicit truth record from provider-advertised
// evidence. Absent metadata yields Unknown; explicit deny yields Unsupported.
func ToCapabilityTruth(reasoning bool, hasExplicitDenied bool, configurable bool, opts []string) CapabilityTruth {
	switch {
	case hasExplicitDenied:
		return CapabilityTruth{Reasoning: ReasoningUnsupported, Configurable: false, Options: nil}
	case reasoning:
		return CapabilityTruth{Reasoning: ReasoningSupported, Configurable: configurable, Options: opts}
	default:
		return CapabilityTruth{Reasoning: ReasoningUnknown, Configurable: false, Options: nil}
	}
}
