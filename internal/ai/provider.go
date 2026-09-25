package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/protocol"
)

// ErrPayloadTruncated is the historical transport-level name for an output
// ceiling.  It aliases the protocol-level sentinel so every provider stack
// agrees on errors.Is identity.
var ErrPayloadTruncated = protocol.ErrOutputTruncated

// ErrOutputTruncated is the provider-neutral name used by the Phase 12
// interaction contract.  Keep both names: older callers and adapters use
// ErrPayloadTruncated while new structural gates use ErrOutputTruncated.
var ErrOutputTruncated = protocol.ErrOutputTruncated

// OutputTruncatedError is the typed carrier accepted by all provider stacks.
type OutputTruncatedError = protocol.OutputTruncatedError

// NewOutputTruncated builds a typed truncation error while preserving the
// provider-native finish reason for diagnostics.
func NewOutputTruncated(provider, finishReason string) error {
	return protocol.NewOutputTruncated(provider, finishReason)
}

// IsOutputTruncated reports whether err carries the canonical truncation
// signal.
func IsOutputTruncated(err error) bool { return errors.Is(err, ErrOutputTruncated) }

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// JSONSchemaFormat is the OpenAI-compatible json_schema envelope. Keeping the
// schema as RawMessage lets a protocol descriptor be forwarded without a
// lossy map[string]any round trip.
type JSONSchemaFormat struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

// ResponseFormat is the provider-neutral structured-output request shape. Type
// may be the legacy "json_object"/"text" value or the OpenAI-compatible
// "json_schema" value. Name, Strict, and Schema are convenience fields used by
// adapters; MarshalJSON emits the provider's nested json_schema envelope.
type ResponseFormat struct {
	Type       string            `json:"type"` // "json_schema", "json_object", or "text"
	Name       string            `json:"-"`
	Strict     bool              `json:"-"`
	Schema     json.RawMessage   `json:"-"`
	JSONSchema *JSONSchemaFormat `json:"-"`
}

// MarshalJSON keeps the old two-field representation byte-for-byte compatible
// while allowing the native JSON Schema representation to be added without a
// second request type.
func (f ResponseFormat) MarshalJSON() ([]byte, error) {
	if f.Type != "json_schema" {
		return json.Marshal(struct {
			Type string `json:"type"`
		}{Type: f.Type})
	}
	format := f.JSONSchema
	if format == nil {
		format = &JSONSchemaFormat{}
	} else {
		copy := *format
		format = &copy
	}
	if format.Name == "" {
		format.Name = f.Name
	}
	if format.Name == "" {
		format.Name = "interaction_contract"
	}
	if !format.Strict {
		format.Strict = f.Strict
	}
	if len(format.Schema) == 0 {
		format.Schema = append(json.RawMessage(nil), f.Schema...)
	}
	if len(format.Schema) == 0 {
		format.Schema = json.RawMessage(`{"type":"object"}`)
	}
	return json.Marshal(struct {
		Type       string            `json:"type"`
		JSONSchema *JSONSchemaFormat `json:"json_schema"`
	}{Type: f.Type, JSONSchema: format})
}

// NewJSONSchemaResponseFormat creates the native OpenAI-compatible structured
// output envelope from a provider-neutral schema document.
func NewJSONSchemaResponseFormat(name string, schema json.RawMessage) *ResponseFormat {
	return &ResponseFormat{
		Type:   "json_schema",
		Name:   name,
		Strict: true,
		Schema: append(json.RawMessage(nil), schema...),
	}
}

// UnmarshalJSON accepts both the compact convenience representation and the
// provider's nested json_schema envelope.
func (f *ResponseFormat) UnmarshalJSON(data []byte) error {
	if f == nil {
		return nil
	}
	var wire struct {
		Type       string            `json:"type"`
		Name       string            `json:"name"`
		Strict     bool              `json:"strict"`
		Schema     json.RawMessage   `json:"schema"`
		JSONSchema *JSONSchemaFormat `json:"json_schema"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	f.Type = wire.Type
	f.Name = wire.Name
	f.Strict = wire.Strict
	f.Schema = append(json.RawMessage(nil), wire.Schema...)
	f.JSONSchema = wire.JSONSchema
	if f.JSONSchema != nil {
		if f.Name == "" {
			f.Name = f.JSONSchema.Name
		}
		f.Strict = f.JSONSchema.Strict
		if len(f.Schema) == 0 {
			f.Schema = append(json.RawMessage(nil), f.JSONSchema.Schema...)
		}
	}
	return nil
}

// SchemaMode lets an embedding boundary explicitly select the adaptive wire
// strategy. Empty/Auto is the production default: use native schema support
// when the adapter knows it, otherwise use the compact prompt fallback.
type SchemaMode string

const (
	SchemaModeAuto   SchemaMode = "auto"
	SchemaModeNative SchemaMode = "native"
	SchemaModePrompt SchemaMode = "prompt"
)

type Request struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
	// InteractionContract is the semantic kind of turn requested by the
	// runtime. It is metadata for the adapter boundary and never grants tool
	// or execution authority.
	InteractionContract protocol.InteractionContract `json:"-"`
	// ContextPhase identifies the semantic phase used by the context budget
	// compiler ("investigate", "plan", or "execute"). It is intentionally a
	// string at the transport boundary so lower-level packages can label a
	// request without importing the compiler implementation.
	ContextPhase string `json:"-"`
	// ContextPolicy is the caller-selected workspace projection policy. An
	// empty value lets the compiler use its conservative repository default;
	// "none" explicitly forbids workspace/session injection.
	ContextPolicy string `json:"-"`
	// ContextPrepared marks a request whose System/Messages projection has
	// already passed through contextcompiler.Compile. Provider middleware
	// must not compile the same request a second time.
	ContextPrepared bool `json:"-"`
	// Contract carries the normalized per-step descriptor when the caller has
	// one. A nil value is valid for legacy requests; adapters may derive a
	// default from InteractionContract without changing dispatch.
	Contract       *protocol.ContractDescriptor `json:"-"`
	System         string                       `json:"-"` // Explicit system prompt (top-level for Anthropic, prepended for OpenAI-compatible)
	MaxTokens      int                          `json:"-"` // 0 = use provider default
	Stop           []string                     `json:"-"` // Optional stop sequences (e.g. [">>>>>>>"])
	Temperature    float64                      `json:"-"` // 0 = use provider default
	ResponseFormat *ResponseFormat              `json:"response_format,omitempty"`
	// SchemaMode optionally overrides the adapter's native-vs-fallback choice.
	// The default is SchemaModeAuto; it never changes the semantic contract.
	SchemaMode SchemaMode `json:"-"`
	// ModelMetadata is an optional provider-catalog fact. An explicit false
	// SupportsStructuredOutput value forces the prompt fallback even when the
	// provider family normally accepts a native schema field.
	ModelMetadata *protocol.ModelMetadata `json:"-"`
	Tools         []ToolDefinition        `json:"-"` // Native LLM function calling tool definitions
	// Reasoning carries the resolved reasoning control (effort level, thinking
	// budget, CoT cap) produced by the decision engine. Providers translate it
	// into their native API payload (reasoning_effort / thinking.budget_tokens /
	// reasoning.{effort,max_tokens}). When nil or zero, no reasoning payload is
	// injected and the provider behaves exactly as before.
	Reasoning *ReasoningConfig `json:"-"`
	// ReasoningHandler receives reasoning/thinking content as it streams in,
	// separated from the main response. Providers call it with verbatim chunks
	// (the same text that reasoning_content / thinking_delta / thought frames
	// carry) so consumers can publish a reasoning stream without ever mixing it
	// into the response pipeline. When nil, reasoning content is silently
	// discarded by the SSE readers.
	ReasoningHandler func(chunk string) error
	// ExtraParams carries arbitrary provider-native JSON fields that are
	// merged directly into the HTTP POST body. It is the generic passthrough
	// for forward-compatibility: callers can send new provider attributes
	// without code changes. Keys colliding with native fields are ignored.
	ExtraParams map[string]any `json:"-"`
}

// ProviderUsage is the AUTHORITATIVE provider-usage contract every invocation
// — streaming and non-streaming alike — must converge on. It is the single
// source of truth for "how many tokens did the provider consume", so the
// renderer never derives token counts from string length or from a local
// token-event counter.
//
// Rules:
//   - Known reports whether the provider actually returned usage metadata.
//     false means "usage unknown" — it must NEVER be rendered as "0 tok".
//   - 0 token counts alongside Known == true mean the provider genuinely
//     reported zero usage (a real zero), never "not yet known".
//   - Prefer provider-reported values verbatim; never fabricate or estimate
//     them when authoritative usage exists.
type ProviderUsage struct {
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`
	CachedTokens     int    `json:"cached_tokens,omitempty"`
	ReasoningTokens  int    `json:"reasoning_tokens,omitempty"`
	FinishReason     string `json:"finish_reason,omitempty"` // "stop", "length", "tool_calls", ...
	// RequestStartedAt is the wall-clock time the provider request began.
	RequestStartedAt time.Time `json:"request_started_at,omitempty"`
	// FirstTokenAt is the wall-clock time the first provider byte arrived.
	// For non-streaming invocations it equals CompletedAt.
	FirstTokenAt time.Time `json:"first_token_at,omitempty"`
	// CompletedAt is the wall-clock time the provider response completed.
	CompletedAt time.Time `json:"completed_at,omitempty"`
	// Known is false when the provider returned no usage metadata at all
	// (usage unknown), true when at least one usage field was reported.
	Known bool `json:"known"`
	// Estimated is true when the token counts were derived from a
	// character-count estimate because the stream was interrupted before the
	// provider's final usage chunk arrived. It is NEVER set when an
	// authoritative usage chunk was observed. Callers must treat estimated
	// counts as approximate, never as provider truth.
	Estimated bool `json:"estimated,omitempty"`
	// HTTPAttempts is the number of transport attempts this single LOGICAL
	// invocation made (1 + every retry). Retry forensics: one model
	// invocation may span multiple HTTP round-trips (429 backoff, 400
	// reasoning-schema retry) — a rate-limited free-tier build that recovers
	// still counts as ONE invocation, and 429 responses carry no billed
	// tokens unless the retried 200 reports usage.
	HTTPAttempts int `json:"http_attempts,omitempty"`
	// RateLimitedRetries is the number of those attempts that were HTTP 429
	// rate-limit retries. Combined with HTTPAttempts it lets consumers
	// distinguish transport retries from a genuinely re-invoked model call.
	RateLimitedRetries int `json:"rate_limited_retries,omitempty"`
}

// EffectiveContract returns a normalized defensive descriptor or derives a
// conservative default from the semantic enum. It never mutates the request;
// malformed or mismatched explicit metadata fails closed as nil.
func (r Request) EffectiveContract() *protocol.ContractDescriptor {
	if r.Contract != nil {
		normalized, err := r.Contract.Clone().Normalize()
		if err != nil || (r.InteractionContract != "" && r.InteractionContract != normalized.Contract) {
			return nil
		}
		return &normalized
	}
	if !r.InteractionContract.Valid() {
		return nil
	}
	d := protocol.Describe(r.InteractionContract)
	return &d
}

// StructuralOutputSchema returns the normalized JSON Schema document carried
// by the request contract. A nil document means the contract is textual or
// otherwise has no JSON structural output.
func (r Request) StructuralOutputSchema() (json.RawMessage, error) {
	descriptor := r.EffectiveContract()
	if descriptor == nil {
		return nil, nil
	}
	return descriptor.JSONSchema()
}

// Empty reports whether the usage record carries no known provider usage.
// This is the "unknown" state and must not render as a literal zero.
func (u ProviderUsage) Empty() bool {
	return !u.Known
}

// Combined returns prompt + completion tokens. Prefer TotalTokens when the
// provider reports a distinct total (some gateways bill cached/reasoning
// tokens that are not part of prompt+completion).
func (u ProviderUsage) Combined() int {
	return u.PromptTokens + u.CompletionTokens
}

// Response is the provider invocation result. TokenInput/TokenOutput are
// retained for backward compatibility and always mirror Usage.PromptTokens /
// Usage.CompletionTokens when Usage.Known is true.
type Response struct {
	Content     string     `json:"content"`
	TokenInput  int        `json:"token_input"`
	TokenOutput int        `json:"token_output"`
	ToolCalls   []ToolCall `json:"tool_calls,omitempty"` // Native LLM function calls from tool_calls finish_reason
	// Provider and Model identify the wire endpoint that produced this result.
	// They are metadata only; callers must not infer authority from them.
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	// InteractionContract and Contract preserve the exact semantic descriptor
	// used to build the request. Providers return a defensive copy so a caller
	// cannot mutate admission state through a response value.
	InteractionContract protocol.InteractionContract `json:"interaction_contract,omitempty"`
	Contract            *protocol.ContractDescriptor `json:"interaction_contract_descriptor,omitempty"`
	// ContractMetadata is a compatibility alias for callers that use the
	// explicit metadata name. It is kept out of JSON to avoid duplicating the
	// canonical descriptor in serialized evidence.
	ContractMetadata *protocol.ContractDescriptor `json:"-"`
	NativeSchema     bool                         `json:"native_schema,omitempty"`
	Schema           json.RawMessage              `json:"schema,omitempty"`
	InlineConstraint string                       `json:"inline_constraint,omitempty"`
	// FinishReason is the provider's terminal finish_reason when one is
	// observable ("stop", "length", "tool_calls", ...). Streaming consumers
	// populate it from the stream's FinishReasonProvider; non-streaming
	// consumers propagate ProviderUsage.FinishReason. A "length" value is the
	// authoritative OUTPUT_EXHAUSTED signal — the response was cut off by the
	// completion ceiling, not finished naturally — so callers can route bounded
	// continuation instead of blind same-scope retry.
	FinishReason string `json:"finish_reason,omitempty"`
	// Truncated is set by a provider adapter when the authoritative provider
	// metadata reported an output ceiling. It is distinct from merely having a
	// FinishReason string, which legacy test doubles may populate without
	// carrying provider truncation provenance.
	Truncated bool `json:"truncated,omitempty"`
	// Usage is the authoritative provider-reported usage of this invocation.
	// Known=false means the provider returned no usage metadata.
	Usage ProviderUsage `json:"usage,omitempty"`
}

// OutputError returns the typed truncation error when the response carries
// output-ceiling metadata. It is safe on nil responses. The explicit
// Truncated bit remains the strongest provenance signal, while the provider
// finish reason and authoritative usage reason are also recognized so callers
// cannot accidentally parse a response whose adapter forgot to set the bit.
func (r *Response) OutputError() error {
	if r == nil {
		return nil
	}
	if !r.Truncated &&
		!protocol.IsOutputTruncatedReason(r.FinishReason) &&
		!protocol.IsOutputTruncatedReason(r.Usage.FinishReason) {
		return nil
	}
	reason := r.FinishReason
	if !protocol.IsOutputTruncatedReason(reason) {
		reason = r.Usage.FinishReason
	}
	if !protocol.IsOutputTruncatedReason(reason) {
		reason = ""
	} else {
		reason = "length"
	}
	return NewOutputTruncated(r.Provider, reason)
}

// ValidateOutputCompletion is the response-boundary form of OutputError.
func ValidateOutputCompletion(resp *Response) error {
	if resp == nil {
		return nil
	}
	return resp.OutputError()
}

// ResponseMetadata is the standardized provider payload metadata wrapper. It
// keeps protocol identity, terminal outcome, and authoritative usage together
// for non-streaming callers and for stream result implementations.
type ResponseMetadata struct {
	Provider            string                       `json:"provider,omitempty"`
	Model               string                       `json:"model,omitempty"`
	InteractionContract protocol.InteractionContract `json:"interaction_contract,omitempty"`
	Contract            *protocol.ContractDescriptor `json:"interaction_contract_descriptor,omitempty"`
	ContractMetadata    *protocol.ContractDescriptor `json:"-"`
	FinishReason        string                       `json:"finish_reason,omitempty"`
	Truncated           bool                         `json:"truncated,omitempty"`
	NativeSchema        bool                         `json:"native_schema,omitempty"`
	Schema              json.RawMessage              `json:"schema,omitempty"`
	InlineConstraint    string                       `json:"inline_constraint,omitempty"`
	Usage               ProviderUsage                `json:"usage,omitempty"`
}

// ProviderPayload and StandardizedResponse are descriptive aliases for
// integrations that name the response wrapper after its transport boundary.
type ProviderPayload = ResponseMetadata
type StandardizedResponse = ResponseMetadata

// Metadata returns a defensive, uniform view of the response envelope.
func (r *Response) Metadata() ResponseMetadata {
	if r == nil {
		return ResponseMetadata{}
	}
	contract := r.Contract
	if contract == nil {
		contract = r.ContractMetadata
	}
	if contract != nil {
		copy := contract.Clone()
		contract = &copy
	}
	usage := r.Usage
	if r.FinishReason != "" && usage.FinishReason == "" {
		usage.FinishReason = r.FinishReason
	}
	if protocol.IsOutputTruncatedReason(usage.FinishReason) {
		usage.FinishReason = "length"
	}
	if r.Truncated && usage.FinishReason == "" {
		usage.FinishReason = "length"
	}
	finishReason := protocol.NormalizeFinishReason(r.FinishReason)
	if finishReason == "" {
		finishReason = usage.FinishReason
	}
	truncated := r.Truncated || protocol.IsOutputTruncatedReason(finishReason)
	return ResponseMetadata{
		Provider:            r.Provider,
		Model:               r.Model,
		InteractionContract: r.InteractionContract,
		Contract:            contract,
		ContractMetadata:    contract,
		FinishReason:        finishReason,
		Truncated:           truncated,
		NativeSchema:        r.NativeSchema,
		Schema:              append(json.RawMessage(nil), r.Schema...),
		InlineConstraint:    r.InlineConstraint,
		Usage:               usage,
	}
}

// ProviderPayload returns the standardized transport wrapper under its
// provider-oriented name.
func (r *Response) ProviderPayload() ResponseMetadata { return r.Metadata() }

// SetContractMetadata stamps the request identity onto a response without
// retaining the caller's mutable descriptor pointer.
func (r *Response) SetContractMetadata(provider, model string, contract protocol.InteractionContract, descriptor *protocol.ContractDescriptor) {
	if r == nil {
		return
	}
	r.Provider = provider
	r.Model = model
	if contract == "" && descriptor != nil {
		contract = descriptor.Contract
	}
	r.InteractionContract = contract
	if descriptor == nil {
		r.Contract = nil
		r.ContractMetadata = nil
		return
	}
	copy := descriptor.Clone()
	r.Contract = &copy
	r.ContractMetadata = &copy
}

type Provider interface {
	Name() string
	Execute(ctx context.Context, req Request) (*Response, error)
	ExecuteStream(ctx context.Context, req Request) (io.ReadCloser, error)
}

// FinishReasonProvider is implemented by stream results that can report the
// provider's terminal finish_reason (e.g. "stop", "length", "tool_calls").
// Consumers use it to detect responses truncated by the API's completion
// ceiling (finish_reason == "length") rather than finished naturally ("stop").
type FinishReasonProvider interface {
	FinishReason() string
}

// TruncationProvider is implemented by stream results that can expose a typed
// output-ceiling error without changing io.Reader's historical EOF behavior.
// Consumers should prefer this over comparing provider-specific strings.
type TruncationProvider interface {
	TruncationError() error
}

// ResponseMetadataProvider is implemented by stream results that can expose
// the same standardized contract/finish-reason wrapper as a non-streaming
// response. The core pipeline uses this metadata after the reader reaches EOF.
type ResponseMetadataProvider interface {
	ResponseMetadata() ResponseMetadata
}

// UsageProvider is implemented by stream results that can report the
// authoritative provider usage (ProviderUsage). Streaming and non-streaming
// executions converge on the same usage contract: a stream result whose reader
// consumed a usage chunk returns Known=true with the provider's exact counts.
type UsageProvider interface {
	Usage() ProviderUsage
}

type ProviderConfig struct {
	APIKey  string
	Model   string
	BaseURL string
}

type Manager struct {
	providers map[string]Provider
	defaultP  string
}

func NewManager() *Manager {
	return &Manager{
		providers: make(map[string]Provider),
	}
}

func (m *Manager) Register(name string, p Provider) {
	m.providers[name] = p
}

func (m *Manager) Get(name string) (Provider, bool) {
	p, ok := m.providers[name]
	return p, ok
}

func (m *Manager) SetDefault(name string) {
	m.defaultP = name
}

func (m *Manager) Default() (Provider, bool) {
	return m.Get(m.defaultP)
}

func (m *Manager) Names() []string {
	names := make([]string, 0, len(m.providers))
	for n := range m.providers {
		names = append(names, n)
	}
	return names
}

type StreamForwarder struct {
	closer  io.ReadCloser
	onChunk func(string)
	onDone  func(string)
	buf     strings.Builder
}

func NewStreamForwarder(closer io.ReadCloser, onChunk func(string), onDone func(string)) *StreamForwarder {
	return &StreamForwarder{
		closer:  closer,
		onChunk: onChunk,
		onDone:  onDone,
	}
}

func (sf *StreamForwarder) Read(p []byte) (int, error) {
	n, err := sf.closer.Read(p)
	if n > 0 {
		chunk := string(p[:n])
		sf.buf.WriteString(chunk)
		if sf.onChunk != nil {
			sf.onChunk(chunk)
		}
	}
	if err == io.EOF {
		if sf.onDone != nil {
			sf.onDone(sf.buf.String())
		}
	}
	return n, err
}

func (sf *StreamForwarder) Close() error {
	return sf.closer.Close()
}
