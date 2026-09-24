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

// ErrInvalidContract is returned when a descriptor is unknown or internally
// inconsistent.  It is intentionally distinct from an authorization failure:
// callers can reject malformed metadata without treating it as a model refusal.
var ErrInvalidContract = errors.New("protocol: invalid interaction contract descriptor")

// ErrAuthorityCeilingExceeded is returned when a staged operation asks for a
// capability above the active descriptor's ceiling.  The canonical executor
// authorization boundary still decides whether an otherwise permitted
// operation is admitted.
var ErrAuthorityCeilingExceeded = errors.New("protocol: operation exceeds interaction contract authority ceiling")

// ErrContractSchema is the provider-neutral structural-output failure used by
// synthesis boundaries before they commit parsed tasks.
var ErrContractSchema = errors.New("protocol: interaction contract output schema violation")

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

// ContractViolationError records the exact contract boundary that rejected a
// proposed operation.  It carries enough evidence for a terminal diagnostic
// without exposing provider wire details.
type ContractViolationError struct {
	Contract  InteractionContract
	Operation string
	Reason    string
}

func (e *ContractViolationError) Error() string {
	if e == nil {
		return ""
	}
	contract := e.Contract
	if contract == "" {
		contract = "unknown"
	}
	operation := e.Operation
	if operation == "" {
		operation = "unknown"
	}
	reason := e.Reason
	if reason == "" {
		reason = "operation is outside the declared capability ceiling"
	}
	return fmt.Sprintf("%s: contract=%s operation=%s: %s", ErrAuthorityCeilingExceeded, contract, operation, reason)
}

func (e *ContractViolationError) Unwrap() error { return ErrAuthorityCeilingExceeded }

// IsOutputTruncated reports whether err is (or wraps) the canonical output
// truncation signal.
func IsOutputTruncated(err error) bool { return errors.Is(err, ErrOutputTruncated) }

// IsOutputTruncatedReason reports whether a provider-native finish reason
// denotes an output ceiling.
func IsOutputTruncatedReason(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "length", "max_tokens", "max tokens", "max_output_tokens", "max output tokens", "max_output_token", "max-output-tokens", "max-output-token", "max output token", "truncated", "token_limit", "output_limit", "max_output":
		return true
	default:
		return false
	}
}

// NormalizeFinishReason returns the canonical "length" label for every
// provider-specific output-ceiling spelling. Non-truncation labels are left
// untouched for provider-specific diagnostics.
func NormalizeFinishReason(reason string) string {
	if IsOutputTruncatedReason(reason) {
		return "length"
	}
	return reason
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

// Capability is a semantic capability a turn may use while proposing an
// action.  It is a ceiling, never an authorization grant: the execution
// admission boundary remains the only component that can grant a real
// capability to an operation.
type Capability string

const (
	CapabilityRead        Capability = "read"
	CapabilityAnalyze     Capability = "analyze"
	CapabilityPropose     Capability = "propose"
	CapabilityMutate      Capability = "mutate"
	CapabilityVerify      Capability = "verify"
	CapabilityTool        Capability = "tool"
	CapabilityShell       Capability = "shell"
	CapabilityDestructive Capability = "destructive"
)

// ContractCapability is a descriptive alias used by integrations that keep
// the protocol vocabulary in their public API.
type ContractCapability = Capability

// AuthorityCeiling is the highest semantic authority a contract permits.  It
// deliberately describes a ceiling rather than a grant.  A contract can lower
// the ceiling for a step, while the canonical authorization engine still has
// to admit the concrete request before anything is executed.
type AuthorityCeiling string

const (
	AuthorityReadOnly AuthorityCeiling = "read_only"
	AuthorityPropose  AuthorityCeiling = "propose"
	AuthorityExecute  AuthorityCeiling = "execute"

	// Verbose aliases make the ceiling's role explicit at call sites.
	AuthorityCeilingReadOnly AuthorityCeiling = AuthorityReadOnly
	AuthorityCeilingPropose  AuthorityCeiling = AuthorityPropose
	AuthorityCeilingExecute  AuthorityCeiling = AuthorityExecute
)

// Descriptive aliases for callers that use the shorter vocabulary.
type AuthorityLevel = AuthorityCeiling

// OutputSchema identifies the structural contract expected from a model turn.
// It is provider-neutral and intentionally small: the plan package owns the
// concrete schema text, while the protocol owns the boundary kind and its
// validation marker.
type OutputSchema string

const (
	SchemaText       OutputSchema = "text"
	SchemaJSON       OutputSchema = "json"
	SchemaPlanJSON   OutputSchema = "plan_json"
	SchemaTaskBlocks OutputSchema = "task_blocks"
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

	// AuthorityCeiling and the capability sets below are descriptive
	// constraints on proposals and dispatch.  They are intentionally kept
	// separate from execution.AdmittedCapabilities: a descriptor can only
	// lower what a step may ask for, never grant authority to act.
	AuthorityCeiling      AuthorityCeiling `json:"authority_ceiling,omitempty"`
	AllowedCapabilities   []Capability     `json:"allowed_capabilities,omitempty"`
	ForbiddenCapabilities []Capability     `json:"forbidden_capabilities,omitempty"`
	// OutputSchema names the structural shape expected from this turn.  A
	// concrete plan schema is supplied by the plan package; the protocol does
	// not embed provider or prompt formatting.
	OutputSchema OutputSchema `json:"output_schema,omitempty"`
	// StructuralOutputSchema is an optional provider-neutral JSON Schema
	// document carried by a descriptor.  It is deliberately a string so the
	// descriptor remains easy to persist in a ledger and so adapters can parse
	// and validate it at their wire boundary.  Schema is retained as the
	// historical identifier/text field; when both are present this field is the
	// concrete document and wins.
	StructuralOutputSchema string `json:"structural_output_schema,omitempty"`
	// SchemaVersion is an optional stable label for consumers that persist a
	// descriptor in an execution ledger.
	SchemaVersion string `json:"schema_version,omitempty"`
	// Schema is an optional concrete schema identifier or text supplied by an
	// embedding boundary.  Protocol defaults use the stable OutputSchema kind
	// plus SchemaVersion; callers may carry a provider-neutral schema document
	// when they need one.
	Schema string `json:"schema,omitempty"`
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

	AuthorityCeiling      AuthorityCeiling
	AllowedCapabilities   []Capability
	ForbiddenCapabilities []Capability
	OutputSchema          OutputSchema
	// StructuralOutputSchema optionally carries a concrete JSON Schema
	// document.  It is kept separate from Schema so schema identifiers and
	// persisted prompt text remain backwards compatible.
	StructuralOutputSchema string
	SchemaVersion          string
	Schema                 string
}

// ModelMetadata is the small amount of provider-neutral model information the
// protocol needs when selecting a prompt profile.  Provider adapters may fill
// it from a catalog, while the planning engine can still fall back to the
// model-name policy when metadata is unavailable.
type ModelMetadata struct {
	ID                       string `json:"id,omitempty"`
	Name                     string `json:"name,omitempty"`
	ContextWindow            int    `json:"context_window,omitempty"`
	MaxOutputTokens          int    `json:"max_output_tokens,omitempty"`
	SupportsStructuredOutput bool   `json:"supports_structured_output,omitempty"`
	SupportsTools            bool   `json:"supports_tools,omitempty"`
	Constrained              bool   `json:"constrained,omitempty"`
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
		return ContractDescriptor{}, fmt.Errorf("%w: unknown interaction contract %q", ErrInvalidContract, contract)
	}

	defaults := defaultContractPolicy(contract)
	promptProfile := opts.PromptProfile
	if promptProfile == "" {
		promptProfile = PromptProfileFull
	}
	if opts.Constrained && opts.PromptProfile == "" {
		promptProfile = PromptProfileCompact
	}
	contextProfile := defaults.context
	structured := contract == StructuredCompletion
	tools := contract == ToolEnabledCompletion || contract == AgenticLoop
	if contract == DirectCompletion {
		contextProfile = ContextObjective
	}
	if contract == ToolEnabledCompletion {
		contextProfile = ContextTarget
	}
	authority := opts.AuthorityCeiling
	if authority == "" {
		authority = defaults.authority
	}
	allowed := append([]Capability(nil), opts.AllowedCapabilities...)
	if len(allowed) == 0 {
		allowed = append([]Capability(nil), defaults.allowed...)
	}
	// An explicit capability set is a complete declaration for this step, not
	// an additive request.  This lets a caller lower (or deliberately widen,
	// subject to the separate authorization boundary) a mode's semantic
	// ceiling without inheriting a contradictory default deny.  When the set
	// explicitly opts into a capability that the contract normally forbids,
	// that normal default is not copied as a deny; an explicit Forbidden
	// entry below still wins.
	forbidden := []Capability(nil)
	if len(opts.AllowedCapabilities) == 0 {
		forbidden = append([]Capability(nil), defaults.forbidden...)
	} else {
		for _, capability := range defaults.forbidden {
			if !containsCapability(opts.AllowedCapabilities, capability) {
				forbidden = append(forbidden, capability)
			}
		}
	}
	for _, capability := range opts.ForbiddenCapabilities {
		if !containsCapability(forbidden, capability) {
			forbidden = append(forbidden, capability)
		}
	}
	outputSchema := opts.OutputSchema
	if outputSchema == "" {
		outputSchema = defaults.output
	}
	// A concrete structural document is an explicit request for JSON output
	// unless the caller also selected a different known shape.  Keeping the
	// inference here makes descriptors assembled from persisted metadata behave
	// the same as descriptors assembled through DescriptorOptions.
	if outputSchema == defaults.output && defaults.output == SchemaText &&
		(strings.TrimSpace(opts.StructuralOutputSchema) != "" || looksLikeJSONDocument(opts.Schema)) {
		outputSchema = SchemaJSON
	}
	if !validAuthorityCeiling(authority) {
		return ContractDescriptor{}, fmt.Errorf("%w: unknown authority ceiling %q", ErrInvalidContract, authority)
	}
	if !validOutputSchema(outputSchema) {
		return ContractDescriptor{}, fmt.Errorf("%w: unknown output schema %q", ErrInvalidContract, outputSchema)
	}
	if opts.MaxOutputTokens < 0 || opts.MaxTasks < 0 {
		return ContractDescriptor{}, fmt.Errorf("%w: output/task ceilings cannot be negative", ErrInvalidContract)
	}
	for _, capability := range opts.AllowedCapabilities {
		if !validCapability(capability) {
			return ContractDescriptor{}, fmt.Errorf("%w: unknown capability %q", ErrInvalidContract, capability)
		}
	}
	for _, capability := range opts.ForbiddenCapabilities {
		if !validCapability(capability) {
			return ContractDescriptor{}, fmt.Errorf("%w: unknown capability %q", ErrInvalidContract, capability)
		}
	}
	return ContractDescriptor{
		Contract:               contract,
		Kind:                   contract,
		StructuredOutput:       structured,
		Tools:                  tools,
		Streaming:              opts.Streaming,
		Reasoning:              opts.Reasoning,
		PromptProfile:          promptProfile,
		ContextProfile:         contextProfile,
		Archetype:              opts.Archetype,
		MaxOutputTokens:        opts.MaxOutputTokens,
		MaxTasks:               opts.MaxTasks,
		ConstrainedModel:       opts.Constrained,
		AuthorityCeiling:       authority,
		AllowedCapabilities:    allowed,
		ForbiddenCapabilities:  forbidden,
		OutputSchema:           outputSchema,
		StructuralOutputSchema: strings.TrimSpace(opts.StructuralOutputSchema),
		SchemaVersion:          opts.SchemaVersion,
		Schema:                 opts.Schema,
	}, nil
}

// Describe returns the default descriptor for a contract.
func Describe(contract InteractionContract) ContractDescriptor {
	d, _ := NewContractDescriptor(contract, DescriptorOptions{})
	return d
}

type contractPolicy struct {
	authority AuthorityCeiling
	allowed   []Capability
	forbidden []Capability
	context   ContextProfile
	output    OutputSchema
}

// defaultContractPolicy is the deny-by-default semantic boundary for each
// interaction kind.  It is intentionally conservative for shell and
// destructive work: those capabilities require an explicit descriptor
// override, and even then the execution authorization boundary remains in
// force.
func defaultContractPolicy(contract InteractionContract) contractPolicy {
	switch contract {
	case DirectCompletion:
		return contractPolicy{
			authority: AuthorityReadOnly,
			allowed:   []Capability{CapabilityRead, CapabilityAnalyze},
			forbidden: []Capability{CapabilityPropose, CapabilityMutate, CapabilityShell, CapabilityDestructive},
			context:   ContextObjective,
			output:    SchemaText,
		}
	case StructuredCompletion:
		return contractPolicy{
			authority: AuthorityPropose,
			allowed:   []Capability{CapabilityRead, CapabilityAnalyze, CapabilityPropose},
			forbidden: []Capability{CapabilityMutate, CapabilityShell, CapabilityDestructive},
			context:   ContextRepository,
			output:    SchemaPlanJSON,
		}
	case ToolEnabledCompletion:
		return contractPolicy{
			authority: AuthorityPropose,
			allowed:   []Capability{CapabilityRead, CapabilityAnalyze, CapabilityPropose, CapabilityTool},
			forbidden: []Capability{CapabilityMutate, CapabilityShell, CapabilityDestructive},
			context:   ContextTarget,
			output:    SchemaJSON,
		}
	case AgenticLoop:
		return contractPolicy{
			authority: AuthorityExecute,
			allowed:   []Capability{CapabilityRead, CapabilityAnalyze, CapabilityPropose, CapabilityMutate, CapabilityVerify},
			forbidden: []Capability{CapabilityShell, CapabilityDestructive},
			context:   ContextRepository,
			output:    SchemaText,
		}
	default:
		return contractPolicy{authority: AuthorityReadOnly, context: ContextObjective, output: SchemaText}
	}
}

func containsCapability(capabilities []Capability, wanted Capability) bool {
	for _, capability := range capabilities {
		if capability == wanted {
			return true
		}
	}
	return false
}

func validAuthorityCeiling(authority AuthorityCeiling) bool {
	switch authority {
	case AuthorityReadOnly, AuthorityPropose, AuthorityExecute:
		return true
	default:
		return false
	}
}

func validCapability(capability Capability) bool {
	switch capability {
	case CapabilityRead, CapabilityAnalyze, CapabilityPropose, CapabilityMutate, CapabilityVerify, CapabilityTool, CapabilityShell, CapabilityDestructive:
		return true
	default:
		return false
	}
}

func validOutputSchema(schema OutputSchema) bool {
	switch schema {
	case SchemaText, SchemaJSON, SchemaPlanJSON, SchemaTaskBlocks:
		return true
	default:
		return false
	}
}

// Normalize returns a defensive, complete descriptor.  It is used at trust
// boundaries where a descriptor may have been decoded from JSON or assembled
// by a caller using a struct literal.  Unknown contracts and inconsistent
// identities fail closed rather than being silently upgraded.
func (d ContractDescriptor) Normalize() (ContractDescriptor, error) {
	contract := d.Contract
	if contract == "" {
		contract = d.Kind
	}
	if !contract.Valid() {
		return ContractDescriptor{}, fmt.Errorf("%w: unknown interaction contract %q", ErrInvalidContract, contract)
	}
	if d.Contract != "" && d.Kind != "" && d.Contract != d.Kind {
		return ContractDescriptor{}, fmt.Errorf("%w: contract identity mismatch: contract=%q kind=%q", ErrInvalidContract, d.Contract, d.Kind)
	}
	normalized, err := NewContractDescriptor(contract, DescriptorOptions{
		Streaming:              d.Streaming,
		Reasoning:              d.Reasoning,
		PromptProfile:          d.PromptProfile,
		Archetype:              d.Archetype,
		MaxOutputTokens:        d.MaxOutputTokens,
		MaxTasks:               d.MaxTasks,
		Constrained:            d.ConstrainedModel,
		AuthorityCeiling:       d.AuthorityCeiling,
		AllowedCapabilities:    d.AllowedCapabilities,
		ForbiddenCapabilities:  d.ForbiddenCapabilities,
		OutputSchema:           d.OutputSchema,
		StructuralOutputSchema: d.StructuralOutputSchema,
		SchemaVersion:          d.SchemaVersion,
		Schema:                 d.Schema,
	})
	if err != nil {
		return ContractDescriptor{}, err
	}
	// Tools is a declared boolean, not merely a derived convenience bit.  A
	// caller that explicitly carries false on a tool-capable contract must be
	// able to lower that ceiling; Describe/NewContractDescriptor still produce
	// true for the normal defaults.
	if !d.Tools && (contract == ToolEnabledCompletion || contract == AgenticLoop) {
		normalized.Tools = false
		if !containsCapability(normalized.ForbiddenCapabilities, CapabilityTool) {
			normalized.ForbiddenCapabilities = append(normalized.ForbiddenCapabilities, CapabilityTool)
		}
	}
	if d.Tools && (contract == ToolEnabledCompletion || contract == AgenticLoop) {
		normalized.Tools = true
	}
	return normalized, nil
}

// Validate checks descriptor identity and policy consistency without
// returning a replacement value.
func (d ContractDescriptor) Validate() error {
	_, err := d.Normalize()
	return err
}

// Clone returns a descriptor whose mutable slices do not alias the receiver.
func (d ContractDescriptor) Clone() ContractDescriptor {
	out := d
	out.AllowedCapabilities = append([]Capability(nil), d.AllowedCapabilities...)
	out.ForbiddenCapabilities = append([]Capability(nil), d.ForbiddenCapabilities...)
	return out
}

func (d ContractDescriptor) RequiresStructuredOutput() bool { return d.StructuredOutput }
func (d ContractDescriptor) AllowsTools() bool              { return d.Tools }
func (d ContractDescriptor) RequiresStreaming() bool        { return d.Streaming }
func (d ContractDescriptor) IsCompact() bool {
	return d.PromptProfile == PromptProfileCompact || d.PromptProfile == PromptProfileMinimal
}
func (d ContractDescriptor) IsConstrained() bool { return d.ConstrainedModel }

// AllowsCapability reports whether the descriptor permits a capability at
// its declared authority ceiling.  The check is a ceiling check only; it is
// not an authorization decision.
func (d ContractDescriptor) AllowsCapability(capability Capability) bool {
	if capability == CapabilityTool && (!d.Tools || (d.Contract != ToolEnabledCompletion && d.Contract != AgenticLoop)) {
		return false
	}
	if containsCapability(d.ForbiddenCapabilities, capability) {
		return false
	}
	if !containsCapability(d.AllowedCapabilities, capability) {
		return false
	}
	switch d.AuthorityCeiling {
	case AuthorityReadOnly:
		return capability == CapabilityRead || capability == CapabilityAnalyze
	case AuthorityPropose:
		return capability == CapabilityRead || capability == CapabilityAnalyze || capability == CapabilityPropose || capability == CapabilityTool
	case AuthorityExecute:
		return true
	default:
		return false
	}
}

// CapabilityAllowed is a compatibility spelling for callers that prefer a
// verb-style method.
func (d ContractDescriptor) CapabilityAllowed(capability Capability) bool {
	return d.AllowsCapability(capability)
}

// CapabilitySet returns a defensive copy of the declared allowed capabilities.
func (d ContractDescriptor) CapabilitySet() []Capability {
	return append([]Capability(nil), d.AllowedCapabilities...)
}

// AllowsTaskType reports whether a task may be included in a structured plan
// proposal.  This is intentionally separate from AllowsOperation: a plan may
// propose a mutation, while a read-only execution contract may not dispatch
// one.
func (d ContractDescriptor) AllowsTaskType(taskType string) bool {
	switch normalizeOperation(taskType) {
	case OperationRead, OperationVerify:
		return d.AllowsCapability(CapabilityRead) || d.AllowsCapability(CapabilityVerify)
	case OperationFileMutate, OperationFileEdit, OperationGitAction:
		return d.AllowsCapability(CapabilityPropose) || d.AllowsCapability(CapabilityMutate)
	case OperationShell:
		// Structured planning may carry a shell command as an untrusted
		// proposal even though dispatch remains forbidden.  An agentic
		// execution contract, however, must explicitly carry the shell
		// capability; its default deny must not be bypassed by a proposal bit.
		if d.Contract == StructuredCompletion || d.Contract == ToolEnabledCompletion {
			return d.AllowsCapability(CapabilityPropose)
		}
		return d.AllowsCapability(CapabilityShell)
	case OperationTool:
		return d.AllowsCapability(CapabilityTool) || d.AllowsCapability(CapabilityPropose)
	default:
		return false
	}
}

// AllowsProposalTaskType is an explicit alias for AllowsTaskType.
func (d ContractDescriptor) AllowsProposalTaskType(taskType string) bool {
	return d.AllowsTaskType(taskType)
}

// Operation names the semantic operation carried by a staged task.  It is kept
// in protocol so adapters can validate a task boundary without importing the
// plan or autonomy packages.
type Operation string

const (
	OperationRead        Operation = "read"
	OperationVerify      Operation = "verify"
	OperationFileMutate  Operation = "FILE_MUTATE"
	OperationFileEdit    Operation = "FILE_EDIT"
	OperationShell       Operation = "SHELL_EXEC"
	OperationGitAction   Operation = "GIT_ACTION"
	OperationTool        Operation = "TOOL_CALL"
	OperationDestructive Operation = "DESTRUCTIVE"

	OperationReadOnly Operation = OperationRead
	OperationExecute  Operation = OperationFileMutate
)

func normalizeOperation(operation string) Operation {
	switch strings.ToUpper(strings.TrimSpace(operation)) {
	case "READ", "READ_ONLY", "READONLY":
		return OperationRead
	case "VERIFY", "VERIFICATION":
		return OperationVerify
	case "FILE_MUTATE", "ATOMIC_REPLACE", "DIFF_PATCH", "MUTATE", "MUTATION":
		return OperationFileMutate
	case "FILE_EDIT", "EDIT":
		return OperationFileEdit
	case "SHELL_EXEC", "SHELL", "COMMAND", "EXEC":
		return OperationShell
	case "GIT_ACTION", "GIT", "COMMIT":
		return OperationGitAction
	case "TOOL_CALL", "TOOL", "NATIVE_TOOL":
		return OperationTool
	case "DESTRUCTIVE", "DESTROY", "DELETE", "DELETE_FILE", "REMOVE", "REMOVE_FILE", "DROP", "WIPE":
		return OperationDestructive
	default:
		return Operation(strings.ToUpper(strings.TrimSpace(operation)))
	}
}

// NormalizeOperation exposes the provider-neutral operation mapping to the
// planning and autonomy dispatch boundaries.
func NormalizeOperation(operation string) Operation { return normalizeOperation(operation) }

// AllowsOperation reports whether a concrete operation may cross the semantic
// dispatch boundary under this descriptor.  It is deliberately stricter than
// AllowsTaskType: proposals are not authority.
func (d ContractDescriptor) AllowsOperation(operation string) bool {
	op := normalizeOperation(operation)
	switch op {
	case OperationRead:
		return d.AllowsCapability(CapabilityRead)
	case OperationVerify:
		return d.AllowsCapability(CapabilityVerify) || d.AllowsCapability(CapabilityRead)
	case OperationFileMutate, OperationFileEdit, OperationGitAction:
		return d.AllowsCapability(CapabilityMutate)
	case OperationShell:
		return d.AllowsCapability(CapabilityShell)
	case OperationTool:
		return d.AllowsCapability(CapabilityTool)
	case OperationDestructive:
		return d.AllowsCapability(CapabilityDestructive)
	default:
		return false
	}
}

// PlanDescriptor derives the descriptor used by /plan.  It centralizes the
// small/free-model decision so prompt construction does not append several
// overlapping negative instruction blocks.
func PlanDescriptor(model string, archetype Archetype, maxOutputTokens, maxTasks int) (ContractDescriptor, error) {
	return PlanDescriptorWithMetadata(model, ModelMetadata{ID: model}, archetype, maxOutputTokens, maxTasks)
}

// PlanDescriptorWithMetadata is the model-metadata-aware form of
// PlanDescriptor.  Explicit metadata wins over name heuristics for the
// constrained/output-budget decisions, while the semantic contract remains
// selected by the plan workspace (StructuredCompletion) rather than by a
// provider capability bit.
func PlanDescriptorWithMetadata(model string, metadata ModelMetadata, archetype Archetype, maxOutputTokens, maxTasks int) (ContractDescriptor, error) {
	if metadata.ID != "" && !strings.EqualFold(strings.TrimSpace(metadata.ID), strings.TrimSpace(model)) {
		metadata = ModelMetadata{}
	}
	constrained := IsConstrainedModel(model)
	if strings.TrimSpace(metadata.ID) != "" {
		// An identified metadata record is authoritative, including an
		// explicit false for Constrained.  The name heuristic is only the
		// compatibility fallback when no catalog fact is available.
		constrained = metadata.Constrained
	} else if metadata.Constrained {
		constrained = true
	}
	if metadata.MaxOutputTokens > 0 && (maxOutputTokens <= 0 || metadata.MaxOutputTokens < maxOutputTokens) {
		maxOutputTokens = metadata.MaxOutputTokens
	}
	opts := DescriptorOptions{
		Streaming:       true,
		Archetype:       archetype,
		MaxOutputTokens: maxOutputTokens,
		MaxTasks:        maxTasks,
		Constrained:     constrained,
		OutputSchema:    SchemaPlanJSON,
		SchemaVersion:   "plan.atomic_tasks.v1",
		Schema:          "izen.plan.atomic_tasks.v1",
	}
	if constrained || archetype == ArchetypeVanillaWeb {
		opts.PromptProfile = PromptProfileCompact
	}
	return NewContractDescriptor(StructuredCompletion, opts)
}

// PlanDescriptorForModel is a descriptive alias used by protocol-oriented
// callers.
func PlanDescriptorForModel(model string, metadata ModelMetadata, archetype Archetype, maxOutputTokens, maxTasks int) (ContractDescriptor, error) {
	return PlanDescriptorWithMetadata(model, metadata, archetype, maxOutputTokens, maxTasks)
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
	if d.Archetype == ArchetypeVanillaWeb {
		b.WriteString("Use FILE_MUTATE for an existing frontend source file.\n")
		b.WriteString("Workspace VANILLA_WEB: target existing .html, .css, or .js files.\n")
	} else {
		b.WriteString("Use SHELL_EXEC for an exact runnable dependency or verification command.\n")
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
	// A required mutation capability is an explicit agentic-loop request in
	// autonomy/build execution contexts.  It must not be lost merely because
	// the raw objective does not contain a word such as "build" or "mutate".
	if hasCapability(requiredCapabilities, "mutate") ||
		hasCapability(requiredCapabilities, "mutation") ||
		hasCapability(requiredCapabilities, "shell") ||
		hasCapability(requiredCapabilities, "destructive") {
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
	case strings.Contains(lower, "build"), strings.Contains(lower, "mutat"):
		return AgenticLoop
	case modeLower != "autonomy" && strings.Contains(lower, "autonom"):
		return AgenticLoop
	default:
		return DirectCompletion
	}
}
