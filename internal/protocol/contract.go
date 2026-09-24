// Package protocol contains the provider-neutral description of one model
// interaction.  It deliberately contains no provider wire types and no
// authorization or execution policy: a contract says what kind of interaction
// a step needs, while the existing execution boundary remains the authority
// for admitting and applying anything the model proposes.
package protocol

import (
	"errors"
	"fmt"
	"strings"
)

// ErrOutputTruncated is the canonical, provider-neutral signal that a model
// stopped at its output ceiling.  Provider adapters must attach the observed
// finish reason (normally "length") and callers must stop before attempting
// structural repair or a fallback parser.
//
// It is kept in this small package so both the new ai.Provider stack and the
// legacy llm stack can use the same errors.Is identity without introducing an
// import cycle.
var ErrOutputTruncated = errors.New("model output exceeded max_tokens limit: ErrOutputTruncated")

// OutputTruncatedError is the typed carrier for a provider truncation.  The
// partial response may still be retained for telemetry or an explicitly
// bounded continuation, but it is not a valid artifact for structural
// parsing.
type OutputTruncatedError struct {
	Provider     string
	FinishReason string
}

func (e *OutputTruncatedError) Error() string {
	provider := strings.TrimSpace(e.Provider)
	if provider == "" {
		provider = "provider"
	}
	reason := strings.TrimSpace(e.FinishReason)
	if reason == "" {
		reason = "length"
	}
	return fmt.Sprintf("%s: %s: finish_reason=%s", ErrOutputTruncated, provider, reason)
}

// Unwrap gives callers one stable errors.Is target across providers and the
// historical ErrPayloadTruncated aliases.
func (e *OutputTruncatedError) Unwrap() error { return ErrOutputTruncated }

// NewOutputTruncated constructs a typed truncation error.  Provider-native
// reasons such as max_tokens and MAX_TOKENS are normalized by the adapters
// before calling this helper, but the original value is retained for
// diagnostics.
func NewOutputTruncated(provider, finishReason string) error {
	return &OutputTruncatedError{Provider: provider, FinishReason: finishReason}
}

// IsOutputTruncated reports whether err is (or wraps) the canonical output
// truncation signal.
func IsOutputTruncated(err error) bool { return errors.Is(err, ErrOutputTruncated) }

// IsOutputTruncatedReason reports whether a provider-native finish reason
// denotes an output ceiling.
func IsOutputTruncatedReason(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "length", "max_tokens", "max tokens", "max_output_tokens", "max output tokens", "truncated", "token_limit", "output_limit", "max_output":
		return true
	default:
		return false
	}
}

// InteractionContract is the semantic kind of model interaction required by a
// step.  It is intentionally independent of provider capabilities: a model
// supporting tools does not silently promote a read-only request to a tool
// interaction.
type InteractionContract string

const (
	DirectCompletion      InteractionContract = "direct_completion"
	StructuredCompletion  InteractionContract = "structured_completion"
	ToolEnabledCompletion InteractionContract = "tool_enabled_completion"
	AgenticLoop           InteractionContract = "agentic_loop"

	// Descriptive aliases are useful at call sites that want to make the
	// enum's role explicit without relying on a package prefix.
	ContractDirectCompletion         InteractionContract = DirectCompletion
	ContractStructuredCompletion     InteractionContract = StructuredCompletion
	ContractToolEnabledCompletion    InteractionContract = ToolEnabledCompletion
	ContractAgenticLoop              InteractionContract = AgenticLoop
	InteractionDirectCompletion      InteractionContract = DirectCompletion
	InteractionStructuredCompletion  InteractionContract = StructuredCompletion
	InteractionToolEnabledCompletion InteractionContract = ToolEnabledCompletion
	InteractionAgenticLoop           InteractionContract = AgenticLoop
	ContractDirect                   InteractionContract = DirectCompletion
	ContractStructured               InteractionContract = StructuredCompletion
	ContractTools                    InteractionContract = ToolEnabledCompletion
	ContractAgentic                  InteractionContract = AgenticLoop
)

// InteractionKind is an alias retained for callers that prefer the term
// "kind" for the semantic enum.
type InteractionKind = InteractionContract
type ContractKind = InteractionContract

// Valid reports whether c is one of the four supported semantic contracts.
func (c InteractionContract) Valid() bool {
	switch c {
	case DirectCompletion, StructuredCompletion, ToolEnabledCompletion, AgenticLoop:
		return true
	default:
		return false
	}
}

// String returns the stable wire-independent label.
func (c InteractionContract) String() string { return string(c) }

// Descriptor returns the default descriptor for this contract.
func (c InteractionContract) Descriptor() ContractDescriptor { return Describe(c) }

// WithOptions returns a descriptor with per-step metadata applied.
func (c InteractionContract) WithOptions(opts DescriptorOptions) (ContractDescriptor, error) {
	return NewContractDescriptor(c, opts)
}

// NewInteractionContract is a constructor-style alias for
// NewContractDescriptor.
func NewInteractionContract(contract InteractionContract, options ...DescriptorOptions) (ContractDescriptor, error) {
	return NewContractDescriptor(contract, options...)
}

// Archetype is a small, protocol-level vocabulary for the workspace shape that
// affects context and command compatibility.  It is a string alias rather
// than an import of the discovery package so the protocol boundary remains
// dependency-free.
type Archetype string

const (
	ArchetypeUnknown    Archetype = ""
	ArchetypeVanillaWeb Archetype = "VANILLA_WEB"
	ArchetypeReactNext  Archetype = "REACT_NEXT"
	ArchetypeGoBackend  Archetype = "GO_BACKEND"
	ArchetypeGeneric    Archetype = "UNKNOWN_GENERIC"
)

// PromptProfile selects the amount of instruction text needed by a model.
type PromptProfile string

const (
	PromptProfileFull    PromptProfile = "full"
	PromptProfileCompact PromptProfile = "compact"
	PromptProfileMinimal PromptProfile = "minimal"
)

// ContextProfile describes the semantic scope of context, never a provider
// transport mechanism.
type ContextProfile string

const (
	ContextNone       ContextProfile = "none"
	ContextObjective  ContextProfile = "objective"
	ContextTarget     ContextProfile = "target"
	ContextRepository ContextProfile = "repository"
)

// ContractDescriptor is the per-step metadata carried through the semantic
// runtime to a provider adapter.  It is descriptive only; in particular,
// Tools is a capability ceiling, not an authorization grant.
type ContractDescriptor struct {
	Contract         InteractionContract `json:"contract"`
	Kind             InteractionContract `json:"kind"`
	StructuredOutput bool                `json:"structured_output"`
	Tools            bool                `json:"tools"`
	Streaming        bool                `json:"streaming"`
	Reasoning        bool                `json:"reasoning"`
	PromptProfile    PromptProfile       `json:"prompt_profile"`
	ContextProfile   ContextProfile      `json:"context_profile"`
	Archetype        Archetype           `json:"archetype,omitempty"`
	MaxOutputTokens  int                 `json:"max_output_tokens,omitempty"`
	MaxTasks         int                 `json:"max_tasks,omitempty"`
	ConstrainedModel bool                `json:"constrained_model,omitempty"`
}

// InteractionDescriptor is a descriptive alias used by a few integrations.
type InteractionDescriptor = ContractDescriptor
type InteractionContractDescriptor = ContractDescriptor

// Descriptor and ContractMetadata are compatibility aliases for integrations
// that use the shorter protocol vocabulary.
type Descriptor = ContractDescriptor
type ContractMetadata = ContractDescriptor

// DescriptorOptions controls optional metadata on a descriptor.  Zero values
// are valid and produce the conservative defaults for the contract kind.
type DescriptorOptions struct {
	Streaming       bool
	Reasoning       bool
	PromptProfile   PromptProfile
	Archetype       Archetype
	MaxOutputTokens int
	MaxTasks        int
	Constrained     bool
}

// NewContractDescriptor returns a normalized descriptor for contract. Unknown
// contracts are rejected rather than silently treated as a more capable
// interaction. Omitting options selects the conservative defaults.
func NewContractDescriptor(contract InteractionContract, options ...DescriptorOptions) (ContractDescriptor, error) {
	var opts DescriptorOptions
	if len(options) > 0 {
		opts = options[0]
	}
	if !contract.Valid() {
		return ContractDescriptor{}, fmt.Errorf("protocol: unknown interaction contract %q", contract)
	}
	promptProfile := opts.PromptProfile
	if promptProfile == "" {
		promptProfile = PromptProfileFull
	}
	contextProfile := ContextRepository
	structured := contract == StructuredCompletion
	tools := contract == ToolEnabledCompletion || contract == AgenticLoop
	if opts.Constrained && opts.PromptProfile == "" {
		promptProfile = PromptProfileCompact
	}
	if contract == DirectCompletion {
		contextProfile = ContextObjective
	}
	if contract == ToolEnabledCompletion {
		contextProfile = ContextTarget
	}
	return ContractDescriptor{
		Contract:         contract,
		Kind:             contract,
		StructuredOutput: structured,
		Tools:            tools,
		Streaming:        opts.Streaming,
		Reasoning:        opts.Reasoning,
		PromptProfile:    promptProfile,
		ContextProfile:   contextProfile,
		Archetype:        opts.Archetype,
		MaxOutputTokens:  opts.MaxOutputTokens,
		MaxTasks:         opts.MaxTasks,
		ConstrainedModel: opts.Constrained,
	}, nil
}

// Describe returns the default descriptor for a contract.
func Describe(contract InteractionContract) ContractDescriptor {
	d, _ := NewContractDescriptor(contract, DescriptorOptions{})
	return d
}

func (d ContractDescriptor) RequiresStructuredOutput() bool { return d.StructuredOutput }
func (d ContractDescriptor) AllowsTools() bool              { return d.Tools }
func (d ContractDescriptor) RequiresStreaming() bool        { return d.Streaming }
func (d ContractDescriptor) IsCompact() bool {
	return d.PromptProfile == PromptProfileCompact || d.PromptProfile == PromptProfileMinimal
}
func (d ContractDescriptor) IsConstrained() bool { return d.ConstrainedModel }

// PlanDescriptor derives the descriptor used by /plan.  It centralizes the
// small/free-model decision so prompt construction does not append several
// overlapping negative instruction blocks.
func PlanDescriptor(model string, archetype Archetype, maxOutputTokens, maxTasks int) (ContractDescriptor, error) {
	constrained := IsConstrainedModel(model)
	opts := DescriptorOptions{
		Streaming:       true,
		Archetype:       archetype,
		MaxOutputTokens: maxOutputTokens,
		MaxTasks:        maxTasks,
		Constrained:     constrained,
	}
	if constrained {
		opts.PromptProfile = PromptProfileCompact
	}
	return NewContractDescriptor(StructuredCompletion, opts)
}

// IsSmallModel reports whether model is a small/local/free model for which a
// compact positive instruction contract is preferable.
func IsSmallModel(model string) bool {
	name := strings.ToLower(strings.TrimSpace(model))
	if name == "" {
		return false
	}
	if IsFreeTierModel(name) {
		return true
	}
	markers := []string{"mini", "nano", "flash", "lite", "small", "tiny", "command-r", "command r", "1b", "2b", "3b", "4b", "5b", "6b", "7b", "8b", "9b", "13b", "14b", "20b", "30b", "32b"}
	for _, marker := range markers {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}

// IsFreeTierModel reports the common OpenRouter free-tier suffix.
func IsFreeTierModel(model string) bool {
	name := strings.ToLower(strings.TrimSpace(model))
	return strings.HasSuffix(name, ":free") || strings.Contains(name, "-free")
}

// IsConstrainedModel is the capability-facing form of IsSmallModel.  It is
// intentionally conservative: an unknown model remains on the full contract.
func IsConstrainedModel(model string) bool { return IsSmallModel(model) || IsFreeTierModel(model) }

// PlanInstructions renders a compact, positive-only plan instruction block.  It
// intentionally does not repeat a long list of "forbidden" rules; the schema
// and the workspace evidence are the source of truth.  The bounded-contract
// label is retained for telemetry/tests and to make the output budget explicit.
func (d ContractDescriptor) PlanInstructions(schema string) string {
	var b strings.Builder
	b.WriteString("PLAN CONTRACT\n")
	b.WriteString("Return one JSON object that follows the supplied schema.\n")
	if strings.TrimSpace(schema) != "" {
		b.WriteString("SCHEMA:\n")
		b.WriteString(strings.TrimSpace(schema))
		b.WriteString("\n")
	}
	b.WriteString("Use atomic_tasks with short description, rationale, and solution values.\n")
	if d.MaxTasks > 0 {
		fmt.Fprintf(&b, "Return at most %d atomic_tasks in this response.\n", d.MaxTasks)
	}
	if d.MaxOutputTokens > 0 {
		fmt.Fprintf(&b, "Keep the response within the %d-token output budget.\n", d.MaxOutputTokens)
	}
	if d.Archetype == ArchetypeVanillaWeb {
		b.WriteString("Workspace VANILLA_WEB: use FILE_MUTATE for existing .html, .css, or .js files.\n")
	}
	if d.ConstrainedModel {
		b.WriteString("[SYSTEM: BOUNDED OUTPUT CONTRACT] Keep the JSON compact and finish at the task boundary.\n")
		b.WriteString("Keep internal thinking under 200 tokens and begin the JSON immediately.\n")
	}
	return strings.TrimSpace(b.String())
}

// FastTrackInstructions renders the minimal task-block contract used by the
// local/free fast-track. It is positive-only and keeps the parser's expected
// syntax without importing the full plan schema.
func (d ContractDescriptor) FastTrackInstructions() string {
	var b strings.Builder
	b.WriteString("FAST PLAN CONTRACT\n")
	b.WriteString("Return only task blocks: - [ ] TYPE: target | rationale\n")
	b.WriteString("Use FILE_MUTATE for an existing source file.\n")
	if d.Archetype == ArchetypeVanillaWeb {
		b.WriteString("Workspace VANILLA_WEB: target existing .html, .css, or .js files.\n")
	}
	if d.MaxTasks > 0 {
		fmt.Fprintf(&b, "Return at most %d task blocks.\n", d.MaxTasks)
	}
	if d.ConstrainedModel {
		b.WriteString("Keep each block to one short line and stop at the task boundary.\n")
	}
	return strings.TrimSpace(b.String())
}

// CompactPlanInstructions is a convenience wrapper for callers that have
// already selected a small-model descriptor.
func CompactPlanInstructions(schema string, archetype Archetype, maxTasks int) string {
	return ContractDescriptor{
		Contract:         StructuredCompletion,
		Kind:             StructuredCompletion,
		Archetype:        archetype,
		MaxTasks:         maxTasks,
		ConstrainedModel: true,
	}.PlanInstructions(schema)
}

// hasCapability matches a capability token without requiring callers to use
// one exact spelling. It intentionally treats only semantic capability names
// as hints; provider capability records are not consulted here.
func hasCapability(capabilities []string, name string) bool {
	for _, capability := range capabilities {
		normalized := strings.ToLower(strings.TrimSpace(capability))
		if normalized == name ||
			strings.HasPrefix(normalized, name+":") ||
			strings.HasPrefix(normalized, name+"_") ||
			strings.HasPrefix(normalized, name+"-") ||
			strings.HasPrefix(normalized, "capability."+name) {
			return true
		}
	}
	return false
}

// SelectInteractionContract applies the Phase 12 G2 default matrix.  It is a
// pure helper: provider capability never promotes a read-only request to a
// tool-enabled or agentic contract.
func SelectInteractionContract(intent, mode string, requiredCapabilities ...string) InteractionContract {
	intentLower := strings.ToLower(strings.TrimSpace(intent))
	modeLower := strings.ToLower(strings.TrimSpace(mode))
	// Mode is a ceiling, not a capability source. Read-only modes cannot be
	// promoted to tool/agentic interactions merely because a model supports
	// them or a caller listed tools as a preference. Structured output is the
	// one capability a read-only step may explicitly require.
	switch modeLower {
	case "ask", "review", "investigate":
		if strings.Contains(intentLower, "json") || strings.Contains(intentLower, "schema") || hasCapability(requiredCapabilities, "structured") {
			return StructuredCompletion
		}
		return DirectCompletion
	}
	if modeLower == "plan" {
		return StructuredCompletion
	}
	if modeLower == "build" {
		return AgenticLoop
	}
	if hasCapability(requiredCapabilities, "structured") {
		return StructuredCompletion
	}
	if hasCapability(requiredCapabilities, "tool") || hasCapability(requiredCapabilities, "tools") {
		return ToolEnabledCompletion
	}
	if hasCapability(requiredCapabilities, "agentic") {
		return AgenticLoop
	}
	lower := intentLower + " " + modeLower
	switch {
	case strings.Contains(lower, "plan"), strings.Contains(lower, "schema"), strings.Contains(lower, "json"):
		return StructuredCompletion
	case strings.Contains(lower, "build"), strings.Contains(lower, "mutat"), strings.Contains(lower, "autonom"):
		return AgenticLoop
	default:
		return DirectCompletion
	}
}
