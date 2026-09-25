package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	dprovider "github.com/PizenLabs/izen/internal/core/domain/provider"
	"github.com/PizenLabs/izen/internal/llm"
	"github.com/PizenLabs/izen/internal/protocol"
	oregistry "github.com/PizenLabs/izen/internal/provider/registry"
)

// ErrOpenRouterAuth is returned when OpenRouter authentication fails (HTTP 401
// or missing API key). The UI layer detects this sentinel error via errors.Is
// and displays a clear actionable banner instead of a raw HTTP status message.
var ErrOpenRouterAuth = errors.New("openrouter: authorization failed (HTTP 401): invalid or missing OPENROUTER_API_KEY — check your environment variables or run: export OPENROUTER_API_KEY=<your_key>")

// DefaultOpenRouterModel is retained for backward compatibility with legacy
// configuration references but is NEVER used as an implicit fallback during
// live invocation. Every invocation MUST carry an explicit ModelBinding.
const DefaultOpenRouterModel = "anthropic/claude-3.5-sonnet"

// ErrOpenRouterModelIncompatible classifies a WIRE-level refusal: the provider
// answered a dispatched request with an agentic-harness HTTP 403. It is never
// produced locally — Izen no longer pre-flight-guards models (see
// promoteAgenticWireContract), so a model that is selectable always reaches
// the provider with a promoted contract. Callers must not switch models on it:
// a wire-policy requirement is satisfied by Dynamic Contract Promotion, and a
// genuine permission refusal is a configuration problem the user must see.
var ErrOpenRouterModelIncompatible = errors.New("openrouter: model unavailable for Izen's current OpenRouter execution path")

// promoteAgenticWireContract performs the adaptive runtime promotion for
// agentic-harness models: when the target model requires a tool schema on the
// wire and the request carries none (e.g. a DirectCompletion /ask request), it
// elevates the interaction contract to ToolEnabledCompletion and attaches
// IZEN's authentic read-only tools. Standard models are untouched so
// DirectCompletion keeps its lower token overhead.
//
// It is a no-op when tools are already present or the existing contract is
// already tool-bearing.
func promoteAgenticWireContract(req ai.Request, model string) ai.Request {
	if len(req.Tools) > 0 {
		return req
	}
	if !oregistry.RequiresAgenticHarnessWire("openrouter", model) {
		return req
	}
	switch req.InteractionContract {
	case "", protocol.DirectCompletion:
	default:
		// Tool-enabled and agentic loops already carry tools; structured-
		// completion keeps its schema contract (can't carry both).
		return req
	}
	descriptor := protocol.Describe(protocol.ToolEnabledCompletion)
	req.InteractionContract = descriptor.Contract
	req.Contract = &descriptor
	req.Tools = ai.ReadOnlyTools()
	return req
}

// asCompatibilityError classifies a non-OK inference response for a dispatched
// model. Three outcomes, in order:
//
//  1. the provider's agentic-harness ROUTING GATE refused the request
//     (ErrOpenRouterAgenticGate) — an access policy on the provider side, kept
//     distinct from a model incompatibility so the UI can explain it precisely,
//  2. an agentic-harness HTTP 403 compatibility refusal
//     (ErrOpenRouterModelIncompatible),
//  3. every other status keeps the existing ProviderError behavior.
//
// No outcome reverts the model: the caller keeps the user's binding.
func asCompatibilityError(model string, statusCode int, respBody []byte) error {
	if gateErr := asAgenticGateError(model, statusCode, respBody); gateErr != nil {
		return gateErr
	}
	pe := NewProviderError("openrouter", statusCode, respBody)
	if pe.IsModelCompatibility() {
		return fmt.Errorf("%w: %s", ErrOpenRouterModelIncompatible, pe.Error())
	}
	return pe
}

// ErrUnassignedTargetModel is returned when an OpenRouter request carries no
// explicit model ID. The worker MUST reject locally before dispatch.
var ErrUnassignedTargetModel = errors.New("openrouter: no model assigned to target node")

// defaultOpenRouterMaxTokens is the output limit applied when a request
// carries no explicit MaxTokens. 4096 keeps long code-generation answers
// (e.g. /ask technical prompts) clear of the completion ceiling
// (finish_reason "length") instead of relying on provider defaults
// (often ~1500-2048 tokens).
const defaultOpenRouterMaxTokens = 4096

// maxOpenRouterMaxTokens is the hard ceiling for an explicit MaxTokens
// budget: larger requests are clamped, never sent unconstrained.
const maxOpenRouterMaxTokens = 8192

// constrainedOpenRouterMaxTokens is the enforced output budget for
// constrained/free-tier models (max_output <= 1024). 980 leaves headroom
// below a 1024 ceiling so the completion never hits finish_reason="length".
const constrainedOpenRouterMaxTokens = 980

// openRouterUserAgent identifies Izen truthfully to the provider. It is a
// stable constant (not a build-stamped version) so provider-side attribution
// groups every Izen request under one client identity.
const openRouterUserAgent = "izen/0.2 (agentic coding harness; +https://github.com/PizenLabs/izen)"

// isOpenRouterFreeTierModel reports whether a model ID is an OpenRouter
// free-tier model (":free" suffix, case-insensitive). Free-tier models are
// treated as constrained (max_output <= 1024) even when the caller does not
// pass an explicit output cap.
func isOpenRouterFreeTierModel(model string) bool {
	m := strings.TrimSpace(model)
	if len(m) < 5 {
		return false
	}
	return strings.EqualFold(m[len(m)-5:], ":free")
}

// openRouterMaxRateLimitRetries bounds how many times a request answered with
// HTTP 429 (Too Many Requests / rate limit) is retried before the error is
// surfaced to the caller. Each retry waits longer (exponential backoff), so the
// total backoff window across all retries stays small while giving a
// rate-limited free-tier model room to recover.
const openRouterMaxRateLimitRetries = 3

// openRouterRateLimitBackoffBase is the base delay unit for exponential backoff
// on HTTP 429 responses: retries wait 1s, 2s, 4s. It is a package-level variable
// (not a const) so tests can shrink it and keep the suite fast.
var openRouterRateLimitBackoffBase = time.Second

// openRouterModelIDRe matches OpenRouter's vendor/model-id schema: a non-empty
// vendor component, a single "/", and a non-empty model component. Vendors
// carry hyphens and digits (meta-llama, gpt-4o) and models may carry
// ":free"-style variants. An ID without a vendor prefix (e.g. Ollama's
// "local-id:7b") is rejected by the API with HTTP 400 "not a valid model
// ID" and must be mapped before dispatch.
var openRouterModelIDRe = regexp.MustCompile(`^[^/\s]+/[^/\s]+$`)

// SanitizeModelForOpenRouter validates a model ID against OpenRouter's
// vendor/model schema. It returns the trimmed model when it matches
// vendor/model, otherwise "". The fallback argument is retained for
// backward compatibility with callers that probe both values but is NOT used
// as an implicit substitution during live invocation — invalid IDs are
// rejected rather than remapped.
func SanitizeModelForOpenRouter(model, fallback string) string {
	for _, candidate := range []string{model, fallback} {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" && openRouterModelIDRe.MatchString(candidate) {
			return candidate
		}
	}
	return ""
}

// resolveModel returns the effective model ID for a request. The model ID is
// passed verbatim — no hardcoded fallback substitution. An empty model is
// rejected locally with ErrUnassignedTargetModel before any HTTP request.
// An invalid vendor/model format is rejected with a deterministic error.
// The provider's configured p.model is NOT used as a silent fallback; every
// invocation MUST carry an explicit model binding from the Workspace Target.
func (p *OpenRouterProvider) resolveModel(reqModel string) (string, error) {
	model := strings.TrimSpace(reqModel)
	if model == "" {
		return "", fmt.Errorf("%w", ErrUnassignedTargetModel)
	}
	if !openRouterModelIDRe.MatchString(model) {
		return "", fmt.Errorf("openrouter: invalid model ID %q: must match vendor/model", model)
	}
	return model, nil
}

// ReasoningSentinel is a zero-width marker embedded in the stream output to
// distinguish reasoning content from message content. The UI layer detects
// these markers and routes reasoning into a separate collapsible buffer.
const ReasoningSentinel = "\x00RSNG\x00"

// ToolCallSentinel is a zero-width marker embedded in the stream output to
// distinguish tool call delta JSON from message content. The UI layer detects
// these markers and routes them into the ToolCallBuffer for live code preview.
const ToolCallSentinel = "\x00TCLL\x00"

type OpenRouterProvider struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client

	// toolRunner is the execution-pipeline read-only tool runner. When set,
	// agentic-harness models run the adaptive read-only tool loop so a
	// tool_calls response is executed and the final answer is produced
	// in-process. It is optional: without it the provider still promotes the
	// wire contract (no 403) but cannot execute tool calls itself.
	toolRunner ai.ToolRunner
}

// SetToolRunner injects the execution-pipeline read-only tool runner used by
// the adaptive tool loop for agentic-harness models.
func (p *OpenRouterProvider) SetToolRunner(runner ai.ToolRunner) {
	if p == nil {
		return
	}
	p.toolRunner = runner
}

func NewOpenRouterProvider(apiKey, model, baseURL string) *OpenRouterProvider {
	if baseURL == "" {
		baseURL = "https://openrouter.ai/api/v1"
	}
	// Strict TTFT watchdog: the transport carries explicit per-phase socket
	// timeouts (dial 5s, TLS handshake 5s, response headers 10s) so OS-level
	// I/O unblocks with a phase-identifiable error instead of hanging, and
	// the 10s header bound sits strictly inside the 15s request context to
	// avoid channel-select drift to 27s on blocked http.Body.Read.
	return &OpenRouterProvider{
		apiKey:  apiKey,
		model:   model,
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Transport: StrictTransport(CloudResponseHeaderTimeout)},
	}
}

func (p *OpenRouterProvider) closeIdleConnections() {
	if p.client == nil || p.client.Transport == nil {
		return
	}
	if t, ok := p.client.Transport.(*http.Transport); ok {
		t.CloseIdleConnections()
	}
}

func (p *OpenRouterProvider) Name() string {
	return "openrouter"
}

// resolveAPIKey returns the effective API key for a request. Strict
// precedence: the explicitly configured key (saved in ~/.izen/config.yml and
// injected at cold-boot construction time) always wins over the shell
// environment variable. Env is only a fallback when no configured key exists
// (picking up runtime .env changes for unconfigured providers).
func (p *OpenRouterProvider) resolveAPIKey() string {
	if key := strings.TrimSpace(p.apiKey); key != "" {
		return key
	}
	if envKey := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")); envKey != "" {
		return envKey
	}
	return ""
}

// Execute performs one logical OpenRouter invocation. Agentic-harness models
// run the adaptive read-only tool loop when a runner is available; every other
// model runs a single request. The wire contract is promoted before dispatch in
// both cases, so an agentic model never receives a toolless request.
func (p *OpenRouterProvider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	model, err := p.resolveModel(req.Model)
	if err != nil {
		return nil, err
	}
	req = promoteAgenticWireContract(req, model)
	if p.toolRunner != nil && oregistry.RequiresAgenticHarnessWire("openrouter", model) {
		return ai.RunReadOnlyToolLoop(ctx, p.executeOnce, p.toolRunner, req, ai.ToolLoopOptions{})
	}
	return p.executeOnce(ctx, req)
}

// executeOnce performs exactly one HTTP chat-completion invocation. It is the
// single-shot primitive the read-only tool loop drives.
func (p *OpenRouterProvider) executeOnce(ctx context.Context, req ai.Request) (*ai.Response, error) {
	requestStarted := time.Now()
	model, err := p.resolveModel(req.Model)
	if err != nil {
		return nil, err
	}

	key := p.resolveAPIKey()
	if key == "" {
		return nil, fmt.Errorf("%w: api key is empty — set OPENROUTER_API_KEY or configure api_key in provider config", ErrOpenRouterAuth)
	}

	prepared, plan, err := PrepareContractRequest("openrouter", req)
	if err != nil {
		return nil, err
	}
	req = prepared
	msgs := p.buildMessages(req)

	body := p.buildRequest(model, msgs, req, false)

	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	resp, stats, err := p.doChatRequest(reqCtx, key, body, false)
	if err != nil {
		return nil, err
	}
	defer func() {
		// The decoder has consumed the response envelope. Close directly:
		// draining an already-terminal HTTP body can block when a gateway
		// keeps the SSE connection open after the response.
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusUnauthorized {
		respBody, _ := io.ReadAll(resp.Body)
		pe := NewProviderError("openrouter", resp.StatusCode, respBody)
		return nil, fmt.Errorf("%w: %s", ErrOpenRouterAuth, pe.Error())
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, asCompatibilityError(model, resp.StatusCode, respBody)
	}

	var openaiResp openrouterResponse
	if err := json.NewDecoder(resp.Body).Decode(&openaiResp); err != nil {
		return nil, fmt.Errorf("openrouter: decode: %w", err)
	}

	if len(openaiResp.Choices) == 0 {
		return nil, fmt.Errorf("openrouter: no choices")
	}

	content := ""
	var toolCalls []ai.ToolCall
	if openaiResp.Choices[0].Message != nil {
		msg := openaiResp.Choices[0].Message
		content = firstUsableContent(msg.Content, msg.Reasoning, msg.ReasoningContent)
		if openaiResp.Choices[0].FinishReason == "tool_calls" && len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				toolCalls = append(toolCalls, ai.ToolCall{
					ID:   tc.ID,
					Type: tc.Type,
					Function: ai.ToolCallFunction{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				})
			}
		}
	}

	tokenIn := 0
	tokenOut := 0
	var usage ai.ProviderUsage
	usage.RequestStartedAt = requestStarted
	if openaiResp.Usage != nil {
		tokenIn = openaiResp.Usage.PromptTokens
		tokenOut = openaiResp.Usage.CompletionTokens
		usage = openaiResp.Usage.ProviderUsage()
	}
	usage.CompletedAt = time.Now()
	usage.FinishReason = openaiResp.Choices[0].FinishReason
	if usage.FirstTokenAt.IsZero() {
		usage.FirstTokenAt = usage.CompletedAt
	}
	usage.HTTPAttempts = stats.attempts
	usage.RateLimitedRetries = stats.rateLimitedRetries

	response := &ai.Response{
		Content:     content,
		TokenInput:  tokenIn,
		TokenOutput: tokenOut,
		ToolCalls:   toolCalls,
		Usage:       usage,
	}
	StampResponseMetadata(response, "openrouter", model, plan, openaiResp.Choices[0].FinishReason)
	if response.Truncated {
		// Preserve the provider buffer and usage for telemetry, but fail
		// before any caller can feed the partial bytes to a structural parser.
		return response, ai.NewOutputTruncated("openrouter", "length")
	}
	return response, nil
}

func (p *OpenRouterProvider) ExecuteStream(ctx context.Context, req ai.Request) (io.ReadCloser, error) {
	model, err := p.resolveModel(req.Model)
	if err != nil {
		return nil, err
	}
	// Dynamic Contract Promotion: an agentic-harness model is executed
	// natively — the contract is promoted to ToolEnabledCompletion and
	// IZEN's authentic read-only tools are attached before dispatch. There is
	// no local pre-flight guard and no model reversion on this path.
	req = promoteAgenticWireContract(req, model)
	// Agentic-harness models: execute the bounded read-only tool loop
	// in-process and surface the final answer as a one-shot stream. This keeps
	// /ask working for models whose provider requires a tool schema.
	if p.toolRunner != nil && oregistry.RequiresAgenticHarnessWire("openrouter", model) {
		resp, loopErr := ai.RunReadOnlyToolLoop(ctx, p.executeOnce, p.toolRunner, req, ai.ToolLoopOptions{})
		if loopErr != nil {
			return nil, loopErr
		}
		content := ""
		if resp != nil {
			content = resp.Content
		}
		return newSynthesizedStream(content), nil
	}
	requestStarted := time.Now()
	key := p.resolveAPIKey()
	if key == "" {
		return nil, fmt.Errorf("%w: api key is empty — set OPENROUTER_API_KEY or configure api_key in provider config", ErrOpenRouterAuth)
	}

	prepared, plan, err := PrepareContractRequest("openrouter", req)
	if err != nil {
		return nil, err
	}
	req = prepared
	msgs := p.buildMessages(req)

	body := p.buildRequest(model, msgs, req, true)

	reqCtx, cancel := context.WithCancel(ctx)
	resp, stats, err := p.doChatRequest(reqCtx, key, body, true)
	if err != nil {
		cancel()
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized {
		respBody, _ := io.ReadAll(resp.Body)
		cancel()
		_ = resp.Body.Close()
		pe := NewProviderError("openrouter", resp.StatusCode, respBody)
		return nil, fmt.Errorf("%w: %s", ErrOpenRouterAuth, pe.Error())
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		cancel()
		_ = resp.Body.Close()
		return nil, asCompatibilityError(model, resp.StatusCode, respBody)
	}

	sr := &openrouterSSEReader{
		body:           resp.Body,
		cancel:         cancel,
		closeTransport: p.closeIdleConnections,
	}
	sr.usage.markRequestStarted(requestStarted)
	// Phase 6.4.4 Optimistic Prompt Token Invariant: commit the estimated
	// prompt count BEFORE entering the SSE chunk read loop so prompt cost
	// is never lost on early stream cancellation. Authoritative usage
	// chunks replace this estimate verbatim when they arrive.
	sr.usage.recordPromptEstimate(EstimatePromptTokensForRequest(req.System, req.Messages))
	sr.usage.recordTransport(stats.attempts, stats.rateLimitedRetries)
	return &OpenRouterStreamResult{ReadCloser: sr, sr: sr, metadata: newResponseMetadata("openrouter", model, plan)}, nil
}

func (p *OpenRouterProvider) buildMessages(req ai.Request) []openrouterMessage {
	if prepared, _, err := PrepareContractRequest("openrouter", req); err == nil {
		req = prepared
	}
	msgs := make([]openrouterMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, openrouterMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		content := sanitizeContent(m.Content)
		om := openrouterMessage{Role: m.Role, Content: content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			typ := tc.Type
			if typ == "" {
				typ = "function"
			}
			om.ToolCalls = append(om.ToolCalls, openrouterToolCall{
				ID:       tc.ID,
				Type:     typ,
				Function: openrouterToolCallFunc{Name: tc.Function.Name, Arguments: tc.Function.Arguments},
			})
		}
		msgs = append(msgs, om)
	}
	return cleanMessages(msgs)
}

// cleanMessages validates and sanitizes a message sequence before it is
// sent to the OpenRouter API. It prevents HTTP 400 responses caused by
// empty content, consecutive same-role messages, or structural violations.
// Rules applied in order:
//  1. Drop messages with empty content (after sanitization).
//  2. Merge consecutive messages with the same role by joining with "\n".
//  3. Strip leading assistant messages (no response without a prior user prompt).
//  4. Ensure the final message is not a system message (remove trailing system messages).
//  5. Sliding window: keep at most the last 30 messages to prevent unbounded
//     token growth across long sessions.
func cleanMessages(msgs []openrouterMessage) []openrouterMessage {
	// Step 1: drop empty content, but never a tool-call or tool-result
	// envelope — the assistant tool_calls block may legitimately carry empty
	// content and must survive for the tool loop to be valid.
	filtered := msgs[:0]
	for _, m := range msgs {
		if m.Content != "" || len(m.ToolCalls) > 0 || m.ToolCallID != "" {
			filtered = append(filtered, m)
		}
	}
	msgs = filtered

	// Step 2: merge consecutive same-role messages. Tool-call and tool-result
	// envelopes are never merged (each is an atomic wire record).
	merged := make([]openrouterMessage, 0, len(msgs))
	for i, m := range msgs {
		mergeable := m.Role != "system" && len(m.ToolCalls) == 0 && m.ToolCallID == "" &&
			i > 0 && len(msgs[i-1].ToolCalls) == 0 && msgs[i-1].ToolCallID == "" &&
			m.Role == msgs[i-1].Role
		if mergeable {
			last := &merged[len(merged)-1]
			last.Content += "\n" + m.Content
			continue
		}
		merged = append(merged, m)
	}
	msgs = merged

	// Step 3: strip leading assistant messages.
	for len(msgs) > 0 && msgs[0].Role == "assistant" {
		msgs = msgs[1:]
	}

	// Step 4: strip trailing system messages, but preserve at least one
	// message (the head system prompt at index 0) so the IZEN identity and
	// user name context is never dropped.
	for len(msgs) > 1 && msgs[len(msgs)-1].Role == "system" {
		msgs = msgs[:len(msgs)-1]
	}

	// Step 5: sliding window truncation.
	// Keep system + user + assistant messages bounded so the payload never
	// explodes across long sessions. Preserve system message at index 0.
	const maxMessages = 30
	if len(msgs) > maxMessages {
		head := 1
		tail := msgs[head:]
		if len(tail) > maxMessages {
			tail = tail[len(tail)-maxMessages:]
		}
		msgs = append(msgs[:head], tail...)
	}

	return msgs
}

type openrouterMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ToolCalls is set on an assistant message that requested tool calls;
	// ToolCallID binds a "tool" result message to its originating call.
	ToolCalls  []openrouterToolCall `json:"tool_calls,omitempty"`
	ToolCallID string               `json:"tool_call_id,omitempty"`
}

type openrouterRequest struct {
	Model               string              `json:"model"`
	Messages            []openrouterMessage `json:"messages"`
	MaxTokens           int                 `json:"max_tokens,omitempty"`
	MaxCompletionTokens int                 `json:"max_completion_tokens,omitempty"`
	Temperature         float64             `json:"temperature,omitempty"`
	Stop                []string            `json:"stop,omitempty"`
	Stream              bool                `json:"stream,omitempty"`
	StreamOptions       *streamOptions      `json:"stream_options,omitempty"`
	ResponseFormat      *ai.ResponseFormat  `json:"response_format,omitempty"`
	Tools               []json.RawMessage   `json:"tools,omitempty"`
	// Reasoning carries OpenRouter's provider-agnostic reasoning control. It
	// is injected from the dynamically resolved effort directive; a nil value
	// omits the field entirely.
	Reasoning *openrouterReasoning `json:"reasoning,omitempty"`
	// ExtraParams carries arbitrary provider-native JSON fields merged
	// directly into the HTTP POST body (generic passthrough).
	ExtraParams map[string]any `json:"-"`
}

// MarshalJSON merges ExtraParams into the top-level object. Native keys win.
func (r openrouterRequest) MarshalJSON() ([]byte, error) {
	type alias openrouterRequest
	return marshalWithExtra(alias(r), r.ExtraParams)
}

// openrouterReasoning is OpenRouter's reasoning control payload: an optional
// qualitative effort (low/medium/high), an optional max_tokens reasoning cap,
// and an optional on/off switch. OpenRouter relays whichever field is set to
// the underlying provider's native mechanism.
type openrouterReasoning struct {
	Effort    string `json:"effort,omitempty"`
	MaxTokens int    `json:"max_tokens,omitempty"`
	Enabled   *bool  `json:"enabled,omitempty"`
}

// defaultReasoningMaxTokens caps the hidden reasoning channel when reasoning
// is enabled without an explicit CoT cap or thinking budget. Without a cap a
// reasoning model can spend the entire shared output budget on hidden
// chain-of-thought and emit zero visible content (truncating the answer at
// finish_reason "length"), so the uncapped case defaults here and the maximum
// token budget goes to the actual response output.
const defaultReasoningMaxTokens = 1024

// reasoningFor builds the OpenRouter reasoning payload from the resolved
// effort directive. The qualitative effort maps to reasoning.effort; the CoT
// cap and budget map to reasoning.max_tokens. Disabled maps to
// reasoning.enabled=false — the only control reliably honored by models whose
// gateways ignore the CoT cap (they otherwise spend the whole output budget in
// the hidden reasoning channel). A nil request reasoning config yields nil
// (field omitted, the pre-existing behavior).
func reasoningFor(req ai.Request) *openrouterReasoning {
	if req.Reasoning == nil {
		return nil
	}
	if req.Reasoning.Disabled {
		enabled := false
		return &openrouterReasoning{Enabled: &enabled}
	}
	r := &openrouterReasoning{Effort: req.Reasoning.Level}
	if r.Effort == "default" {
		// Provider-factory fallback: omit the effort key entirely.
		r.Effort = ""
	}
	switch {
	case req.Reasoning.CoTLimit > 0:
		r.MaxTokens = req.Reasoning.CoTLimit
	case req.Reasoning.BudgetTokens > 0:
		r.MaxTokens = req.Reasoning.BudgetTokens
	default:
		// Enabled reasoning without an explicit cap: bound the hidden channel
		// so it cannot consume the whole output budget.
		if r.Effort != "" {
			r.MaxTokens = defaultReasoningMaxTokens
		}
	}
	if r.Effort == "" && r.MaxTokens == 0 {
		return nil
	}
	return r
}

// openRouterModelSupportsReasoning reports whether the target model accepts
// OpenRouter's reasoning control. It delegates to the strict explicit
// whitelist in llm.ModelSupportsEffortWithProvider — only verified
// reasoning families (openai/o1*, openai/o3*, anthropic/claude-3-7-sonnet*,
// deepseek/deepseek-r1*) return true. All other models (qwen2.5,
// aion-labs/aion-3.0, gemma, etc.) return false so the reasoning payload is
// never injected and the gateway never rejects with HTTP 400.
func openRouterModelSupportsReasoning(model string) bool {
	return llm.ModelSupportsEffortWithProvider("openrouter", model)
}

// buildRequest assembles the OpenRouter chat-completion payload for a request.
// The reasoning control is injected only when the target model supports the
// reasoning schema (see openRouterModelSupportsReasoning), so a non-reasoning
// model never receives a payload the gateway rejects with HTTP 400.
// It also enforces provider-specific token contracts via TokenManager:
// OpenAI => reasoning_effort + max_completion_tokens, Anthropic => max_tokens = budget+4096.
func (p *OpenRouterProvider) buildRequest(model string, msgs []openrouterMessage, req ai.Request, stream bool) openrouterRequest {
	if prepared, plan, err := PrepareContractRequest("openrouter", req); err == nil {
		req = prepared
		if !plan.NativeSchema && plan.InlineConstraint != "" && !messagesContainSchemaMarker(msgs) && !strings.Contains(req.System, StructuredOutputPromptMarker) {
			msgs = appendInlineConstraintToMessages(msgs, plan.InlineConstraint)
		}
	}
	body := openrouterRequest{
		Model:          model,
		Messages:       msgs,
		MaxTokens:      req.MaxTokens,
		Temperature:    req.Temperature,
		Stop:           req.Stop,
		Stream:         stream,
		ResponseFormat: req.ResponseFormat,
		Reasoning:      reasoningFor(req),
	}
	// Default output limit — never send unconstrained max_tokens. The default
	// is 4096 so long code-generation answers complete without hitting the
	// completion ceiling (finish_reason "length"); explicit larger budgets
	// are honored up to the 8192 hard cap. Callers with tighter budgets
	// (bounded-patch mutation, read-only plans) pass their own smaller
	// MaxTokens, which is preserved verbatim below the cap.
	//
	// Constrained Output Budget Invariant: OpenRouter free-tier models
	// (":free" suffix, max_output <= 1024) MUST force
	// max_tokens = min(requested, 980) so the completion never hits
	// finish_reason="length" / OUTPUT_EXHAUSTED.
	if isOpenRouterFreeTierModel(model) {
		if body.MaxTokens <= 0 || body.MaxTokens > constrainedOpenRouterMaxTokens {
			body.MaxTokens = constrainedOpenRouterMaxTokens
		}
	} else {
		if body.MaxTokens == 0 {
			body.MaxTokens = defaultOpenRouterMaxTokens
		}
		if body.MaxTokens > maxOpenRouterMaxTokens {
			body.MaxTokens = maxOpenRouterMaxTokens
		}
	}
	if stream {
		body.StreamOptions = &streamOptions{IncludeUsage: true}
	}
	if !openRouterModelSupportsReasoning(model) {
		body.Reasoning = nil
	} else if body.Reasoning != nil {
		// Enforce token contracts via TokenManager
		tm := llm.NewTokenManager()
		effort := body.Reasoning.Effort
		// For openrouter vendor-prefixed IDs, infer effective provider
		payload := tm.BuildPayload("openrouter", model, effort)
		lowerModel := strings.ToLower(model)
		switch {
		case strings.HasPrefix(lowerModel, "openai/o1") || strings.HasPrefix(lowerModel, "openai/o3"):
			// OpenAI: reasoning_effort + max_completion_tokens
			if payload.ReasoningEffort != "" {
				body.Reasoning.Effort = payload.ReasoningEffort
			}
			body.Reasoning.MaxTokens = 0
			if payload.MaxCompletionTokens > 0 {
				body.MaxCompletionTokens = payload.MaxCompletionTokens
				// Also keep MaxTokens for compatibility; some gateways map it
				if body.MaxTokens == 0 || body.MaxTokens < payload.MaxCompletionTokens {
					body.MaxTokens = payload.MaxCompletionTokens
				}
			}
		case strings.HasPrefix(lowerModel, "anthropic/claude-3-7"):
			// Anthropic: thinking.budget_tokens + max_tokens = budget+4096
			body.Reasoning.Effort = ""
			if payload.ThinkingBudget > 0 {
				body.Reasoning.MaxTokens = payload.ThinkingBudget
			}
			if payload.MaxTokens > 0 {
				body.MaxTokens = payload.MaxTokens
				body.MaxCompletionTokens = 0
			}
		default:
			// Generic: apply thinking budget if present
			if payload.ThinkingBudget > 0 {
				body.Reasoning.MaxTokens = payload.ThinkingBudget
			}
			if payload.MaxTokens > 0 && body.MaxTokens == 0 {
				body.MaxTokens = payload.MaxTokens
			}
		}
		// Ensure Anthropic max_tokens > budget_tokens
		if strings.HasPrefix(lowerModel, "anthropic/") && body.Reasoning != nil && body.Reasoning.MaxTokens > 0 {
			if body.MaxTokens <= body.Reasoning.MaxTokens {
				body.MaxTokens = body.Reasoning.MaxTokens + 4096
			}
		}
	}
	// INVARIANT 1: ZERO-TOOL PAYLOAD ON CASUAL — if the system prompt is the
	// minimal casual contract, tools MUST be omitted entirely (not even an empty
	// array). Defensive: even if caller erroneously sets Tools, drop them.
	//
	// EXCEPTION (adaptive runtime): an agentic-harness model rejects a toolless
	// request with HTTP 403 regardless of the prompt profile, so the read-only
	// tool schema must survive even on a casual turn.
	agenticWire := oregistry.RequiresAgenticHarnessWire("openrouter", model)
	if isCasualSystemPrompt(req.System) && !agenticWire {
		body.Tools = nil
	} else if len(req.Tools) > 0 {
		rawTools := make([]json.RawMessage, 0, len(req.Tools))
		for _, t := range req.Tools {
			data, err := json.Marshal(t)
			if err == nil {
				rawTools = append(rawTools, data)
			}
		}
		body.Tools = rawTools
	}
	body.ExtraParams = req.ExtraParams
	return body
}

// isCasualSystemPrompt reports whether system is the minimal casual prompt.
// It checks for the tiny contract without the full MODE contracts so a casual
// greeting never carries tool schemas.
func messagesContainSchemaMarker(msgs []openrouterMessage) bool {
	for _, message := range msgs {
		if strings.Contains(message.Content, StructuredOutputPromptMarker) {
			return true
		}
	}
	return false
}

func appendInlineConstraintToMessages(msgs []openrouterMessage, constraint string) []openrouterMessage {
	if len(msgs) == 0 {
		return []openrouterMessage{{Role: "system", Content: constraint}}
	}
	out := append([]openrouterMessage(nil), msgs...)
	last := len(out) - 1
	out[last].Content = strings.TrimSpace(out[last].Content) + "\n\n" + constraint
	return out
}

func isCasualSystemPrompt(system string) bool {
	if system == "" {
		return false
	}
	// Minimal contract is "You are IZEN, a fast CLI coding companion..." without MODE.
	if strings.Contains(system, "fast CLI coding companion") && !strings.Contains(system, "MODE:") {
		return true
	}
	return false
}

// chatRequestStats carries the transport forensics of one logical invocation
// (Phase 7 P5): attempts is the total number of HTTP round-trips (1 + every
// retry), rateLimitedRetries how many of those were 429 rate-limit retries.
type chatRequestStats struct {
	attempts           int
	rateLimitedRetries int
}

// doChatRequest POSTs a chat-completion payload to the OpenRouter gateway and
// returns the HTTP response (the caller owns the body) plus the transport
// forensics of the call. When the payload
// carried a reasoning control and the gateway rejects it with HTTP 400, the
// reasoning field is stripped and the request is retried exactly once: some
// OpenRouter models do not accept the reasoning schema, and OpenRouter bills
// tokens at the gateway before the stream fails. Stripping reasoning lets the
// turn complete without reasoning rather than failing the whole request.
//
// HTTP 429 (Too Many Requests) responses are handled gracefully with a retry
// loop before the error is thrown: the Retry-After header is honored when
// present, otherwise the request is retried with exponential backoff
// (1s -> 2s -> 4s) up to openRouterMaxRateLimitRetries times. The wait between
// retries is interruptible by the request context so a cancelled or
// deadline-exceeded context surfaces promptly instead of sleeping out the full
// backoff window. Both the non-streaming Execute path and the streaming
// ExecuteStream path route through this function, so rate-limited free-tier
// builds recover instead of aborting on the first 429. Every retry is a
// transport attempt of the SAME logical invocation — recovered 429s never
// double the invocation count, and their responses carry no billed tokens.
//
// Strict TTFT watchdog: each HTTP attempt is wrapped in context.WithTimeout
// (15s) directly on the Request and the transport enforces a 10s response-
// header bound, so the OS-level I/O unblocks with a phase-identifiable error
// instead of drifting to 27s via blocked http.Body.Read select.
func (p *OpenRouterProvider) doChatRequest(ctx context.Context, key string, body openrouterRequest, stream bool) (*http.Response, chatRequestStats, error) {
	var stats chatRequestStats
	attempt := func(b openrouterRequest) (*http.Response, error) {
		stats.attempts++
		payload, err := json.Marshal(b)
		if err != nil {
			return nil, fmt.Errorf("openrouter: marshal: %w", err)
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("openrouter: new request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+key)
		if stream {
			httpReq.Header.Set("Accept", "text/event-stream")
		}
		// App-attribution headers (documented, optional, never
		// authorization): HTTP-Referer identifies the app for rankings,
		// X-Title/X-OpenRouter-Title sets its display name. No Categories
		// header is sent: OpenRouter silently drops unrecognized category
		// values and attribution headers never grant model eligibility.
		httpReq.Header.Set("HTTP-Referer", "https://pizenlabs.github.io/izen314")
		httpReq.Header.Set("X-OpenRouter-Title", "izen")
		httpReq.Header.Set("X-Title", "izen")
		// Truthful client identification. Go's implicit default
		// ("Go-http-client/1.1") hides the caller from provider-side app
		// attribution and diagnostics; Izen names itself instead. Izen NEVER
		// borrows another product's User-Agent: OpenRouter's agentic-harness
		// routing gate is a User-Agent allowlist (see agentic_gate.go), and
		// impersonating a registered harness to pass it would be a lie about
		// who is calling.
		httpReq.Header.Set("User-Agent", openRouterUserAgent)
		resp, err := p.client.Do(httpReq)
		if err != nil && resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return resp, err
	}

	resp, err := attempt(body)
	if err != nil {
		return nil, stats, fmt.Errorf("openrouter: do: %w", err)
	}
	if resp.StatusCode == http.StatusBadRequest && body.Reasoning != nil {
		// A non-reasoning model rejected the reasoning schema. Discard the
		// rejected response, strip the reasoning payload and retry once.
		_ = resp.Body.Close()
		body.Reasoning = nil
		resp, err = attempt(body)
		if err != nil {
			return nil, stats, fmt.Errorf("openrouter: do: %w", err)
		}
	}

	// Rate-limited (429): retry with backoff instead of aborting the turn.
	for retries := 0; resp.StatusCode == http.StatusTooManyRequests && retries < openRouterMaxRateLimitRetries; retries++ {
		delay, ok := retryAfterDelay(resp)
		if !ok {
			delay = openRouterRateLimitBackoff(retries)
		}
		_ = resp.Body.Close()
		if !waitRateLimitRetry(ctx, delay) {
			return nil, stats, fmt.Errorf("openrouter: rate limited (429): retry aborted: %w", ctx.Err())
		}
		resp, err = attempt(body)
		if err != nil {
			return nil, stats, fmt.Errorf("openrouter: do: %w", err)
		}
		stats.rateLimitedRetries++
	}
	return resp, stats, nil
}

// retryAfterDelay parses the HTTP Retry-After header from a response into the
// delay to wait before retrying. Retry-After carries either a number of seconds
// or an HTTP-date; both forms are honored. ok=false when the header is absent
// or unparsable, so callers fall back to exponential backoff.
func retryAfterDelay(resp *http.Response) (time.Duration, bool) {
	if resp == nil {
		return 0, false
	}
	v := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d, true
		}
		return 0, true
	}
	return 0, false
}

// openRouterRateLimitBackoff returns the exponential backoff delay for the
// zero-based retry index: 1s for retry 0, 2s for retry 1, 4s for retry 2.
func openRouterRateLimitBackoff(retry int) time.Duration {
	if retry < 0 {
		retry = 0
	}
	if retry > 10 {
		retry = 10
	}
	return time.Duration(1<<retry) * openRouterRateLimitBackoffBase
}

// waitRateLimitRetry blocks for delay, aborting early when ctx is cancelled.
// It reports false when the context was cancelled before the delay elapsed, so
// the caller can surface the cancellation instead of sleeping out the backoff.
func waitRateLimitRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

type openrouterResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []openrouterChoice `json:"choices"`
	Usage   *openrouterUsage   `json:"usage,omitempty"`
}

type openrouterChoice struct {
	Index        int              `json:"index"`
	Message      *openrouterMsg   `json:"message,omitempty"`
	Delta        *openrouterDelta `json:"delta,omitempty"`
	FinishReason string           `json:"finish_reason"`
}

type openrouterMsg struct {
	Role             string               `json:"role"`
	Content          string               `json:"content"`
	Reasoning        string               `json:"reasoning,omitempty"`
	ReasoningContent string               `json:"reasoning_content,omitempty"`
	ToolCalls        []openrouterToolCall `json:"tool_calls,omitempty"`
}

type openrouterToolCall struct {
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Function openrouterToolCallFunc `json:"function"`
}

type openrouterToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openrouterDelta struct {
	Role             string                `json:"role,omitempty"`
	Content          string                `json:"content,omitempty"`
	ReasoningContent string                `json:"reasoning_content,omitempty"`
	Reasoning        string                `json:"reasoning,omitempty"`
	ToolCalls        []openrouterToolDelta `json:"tool_calls,omitempty"`
}

type openrouterToolDelta struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function,omitempty"`
}

type openrouterUsage struct {
	PromptTokens     int                       `json:"prompt_tokens"`
	CompletionTokens int                       `json:"completion_tokens"`
	TotalTokens      int                       `json:"total_tokens"`
	PromptDetails    *openrouterUsageDetails   `json:"prompt_tokens_details,omitempty"`
	CompletionDetail *openrouterCompletionInfo `json:"completion_tokens_details,omitempty"`
}

// openrouterUsageDetails carries the input-token split OpenRouter exposes
// through the OpenAI-compatible prompt_tokens_details object.
type openrouterUsageDetails struct {
	CachedTokens    int `json:"cached_tokens,omitempty"`
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

// openrouterCompletionInfo carries the output-token split for reasoning.
type openrouterCompletionInfo struct {
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
}

// ProviderUsage converts the parsed OpenRouter usage object into the
// authoritative ai.ProviderUsage contract. Known is always true for a parsed
// object: OpenRouter reports a usage object on every non-streaming response.
func (u *openrouterUsage) ProviderUsage() ai.ProviderUsage {
	out := ai.ProviderUsage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		Known:            true,
	}
	if u.PromptDetails != nil {
		out.CachedTokens = u.PromptDetails.CachedTokens
		if out.ReasoningTokens == 0 {
			out.ReasoningTokens = u.PromptDetails.ReasoningTokens
		}
	}
	if u.CompletionDetail != nil {
		out.ReasoningTokens += u.CompletionDetail.ReasoningTokens
	}
	if out.TotalTokens == 0 {
		out.TotalTokens = out.PromptTokens + out.CompletionTokens + out.ReasoningTokens
	}
	return out
}

// synthesizedStream presents already-complete content as an io.ReadCloser. It
// is used by the adaptive tool loop (ExecuteStream) so the caller-visible
// streaming contract is preserved once the model has produced its final answer.
type synthesizedStream struct {
	*strings.Reader
}

func (s *synthesizedStream) Close() error { return nil }

func newSynthesizedStream(content string) io.ReadCloser {
	return &synthesizedStream{Reader: strings.NewReader(content)}
}

type OpenRouterStreamResult struct {
	io.ReadCloser
	sr       *openrouterSSEReader
	metadata ai.ResponseMetadata
}

func (r *OpenRouterStreamResult) Usage() ai.ProviderUsage {
	if r.sr != nil {
		return normalizeUsageMetadata(r.sr.usage.Usage())
	}
	return ai.ProviderUsage{}
}

// FinishReason returns the terminal finish_reason observed on the stream
// ("stop", "length", "tool_calls", ...). It reports "" when the stream ended
// before any finish_reason chunk was seen. Consumers use it to distinguish a
// natural completion ("stop") from a response truncated by the completion
// ceiling ("length").
func (r *OpenRouterStreamResult) FinishReason() string {
	if r.sr != nil {
		return NormalizeFinishReason(r.sr.finishReason)
	}
	return ""
}

// ResponseMetadata returns the standardized contract/finish-reason wrapper for
// this stream.
func (r *OpenRouterStreamResult) ResponseMetadata() ai.ResponseMetadata {
	if r == nil {
		return ai.ResponseMetadata{}
	}
	return streamResponseMetadata(r.metadata, r.Usage(), r.FinishReason())
}

// TruncationError exposes the typed output-ceiling signal without changing
// io.Reader's EOF contract for consumers that still need to drain the
// already-emitted bytes.
func (r *OpenRouterStreamResult) TruncationError() error {
	if r != nil && isOutputLength(r.FinishReason()) {
		return ai.NewOutputTruncated("openrouter", r.FinishReason())
	}
	return nil
}

// Err is a short compatibility spelling for TruncationError.
func (r *OpenRouterStreamResult) Err() error { return r.TruncationError() }

// thinkTagSplitter is a stateful inline <think>...</think> extractor for
// OpenRouter models that return their thinking blocks inside delta.content
// instead of a dedicated reasoning field. It rewrites contiguous thinking
// segments into ReasoningSentinel-wrapped runs so EVERY downstream consumer —
// including ones that never ran the stream classifier — receives reasoning on
// the sentinel channel and visible content untouched. Partial markers that
// straddle a chunk boundary ("<thi", "ink>") are held back in a residue buffer
// until enough bytes arrive, so a marker is consumed exactly once regardless
// of how the gateway fragments the SSE deltas.
type thinkTagSplitter struct {
	inThink bool
	residue []byte
}

func (t *thinkTagSplitter) write(chunk []byte) []byte {
	if len(chunk) == 0 {
		return nil
	}
	data := chunk
	if len(t.residue) > 0 {
		data = append(append([]byte(nil), t.residue...), chunk...)
		t.residue = nil
	}
	var out []byte
	for len(data) > 0 {
		if t.inThink {
			idx := bytes.Index(data, []byte("</think>"))
			if idx < 0 {
				// Still inside the thinking block: emit what is provably not a
				// partial closing marker, hold back the ambiguous tail.
				keep := longestPartialSuffix(data, "</think>")
				emit := len(data) - keep
				out = append(out, data[:emit]...)
				t.residue = append(t.residue[:0], data[emit:]...)
				return out
			}
			out = append(out, data[:idx]...)
			out = append(out, ReasoningSentinel...)
			t.inThink = false
			data = data[idx+len("</think>"):]
			continue
		}
		idx := bytes.Index(data, []byte("<think>"))
		if idx < 0 {
			keep := longestPartialSuffix(data, "<think>")
			emit := len(data) - keep
			out = append(out, data[:emit]...)
			t.residue = append(t.residue[:0], data[emit:]...)
			return out
		}
		out = append(out, data[:idx]...)
		out = append(out, ReasoningSentinel...)
		t.inThink = true
		data = data[idx+len("<think>"):]
	}
	return out
}

// takeResidue flushes bytes held back for a possibly-partial marker when the
// stream terminates: no more bytes can complete the marker, so the tail is
// delivered verbatim (classified by the state at hold time).
func (t *thinkTagSplitter) takeResidue() []byte {
	tail := t.residue
	t.residue = nil
	if t.inThink && len(tail) > 0 {
		wrapped := make([]byte, 0, len(ReasoningSentinel)+len(tail)+len(ReasoningSentinel))
		wrapped = append(wrapped, ReasoningSentinel...)
		wrapped = append(wrapped, tail...)
		wrapped = append(wrapped, ReasoningSentinel...)
		return wrapped
	}
	return tail
}

// longestPartialSuffix returns the length of the longest proper suffix of data
// that is a prefix of marker — the number of trailing bytes that MIGHT be the
// beginning of marker arriving in the next chunk.
func longestPartialSuffix(data []byte, marker string) int {
	max := len(marker) - 1
	if len(data) < max {
		max = len(data)
	}
	for k := max; k > 0; k-- {
		if bytes.HasSuffix(data, []byte(marker[:k])) {
			return k
		}
	}
	return 0
}

type openrouterSSEReader struct {
	cancel         context.CancelFunc
	body           io.ReadCloser
	reader         *bufio.Reader
	closed         bool
	closeOnce      sync.Once
	finalUsage     *openrouterUsage
	closeTransport func()

	// think splits inline <think>…</think> blocks out of delta.content into
	// sentinel-wrapped reasoning runs (see thinkTagSplitter).
	think thinkTagSplitter

	// usage tracks cumulative token accounting: the authoritative provider
	// usage when a usage chunk arrives, plus a character-count estimate when
	// the stream is interrupted (context deadline) before that chunk.
	usage streamUsageTracker

	// finishReason records the terminal finish_reason chunk observed on the
	// stream ("stop", "length", "tool_calls", ...). It is surfaced to callers
	// via OpenRouterStreamResult.FinishReason() so consumers can detect
	// responses truncated by the completion ceiling (finish_reason: "length")
	// instead of assuming the response ended naturally.
	finishReason string

	// pending holds bytes produced by a parsed SSE event that did not fit
	// into the caller's buffer on a previous Read() call. Read() must never
	// silently drop bytes just because len(p) was smaller than one logical
	// unit (a sentinel-wrapped reasoning/content/tool-call chunk) — doing so
	// previously truncated large reasoning bursts and tool-call argument
	// JSON mid-stream, dropping the closing sentinel along with the tail of
	// the data. That desynced the sentinel parser downstream (an opened-but-
	// never-closed \x00RSNG\x00 block), which caused partially-streamed
	// reasoning text to leak into the visible answer instead of staying in
	// the Thinking Panel. Buffering the remainder here and draining it on
	// the next Read() call restores normal io.Reader semantics regardless
	// of the caller's buffer size.
	pending []byte

	// lifecycle enforces the Stream Terminal Invariant (Phase 6.4.1).
	lifecycle *dprovider.StreamLifecycle
	idleStop  func()
	startOnce sync.Once
}

func (s *openrouterSSEReader) armIdle() {
	s.startOnce.Do(func() {
		if s.lifecycle == nil {
			s.lifecycle = dprovider.NewStreamLifecycle()
		}
		lc := s.lifecycle
		s.idleStop = dprovider.ArmIdleDeadline(s.cancel, lc.TokensEmitted, lc.IsClosed)
	})
}

func (s *openrouterSSEReader) stopIdle() {
	if s.idleStop != nil {
		stop := s.idleStop
		s.idleStop = nil
		stop()
	}
}

// drainTrailingUsage implements the Phase 6.4.2 Bounded Usage Drain
// (Telemetry Accuracy Invariant): after a terminal finish_reason, the
// gateway frequently delivers the authoritative usage object as a trailing
// usage-only SSE event (choices: []). Closing instantly would drop those
// billed tokens from TaskState.TokenUsage and the UI footer. This polls
// for one trailing event for up to dprovider.UsageDrainTimeout (50ms) and
// records any usage found. Single-threaded and race-free: the stream's
// only reader polls s.reader.Buffered() — no goroutine ever touches the
// bufio reader concurrently. Best-effort: timeout, [DONE], EOF, or a parse
// failure simply ends the drain and the terminal close proceeds.
func (s *openrouterSSEReader) drainTrailingUsage() {
	if s.reader == nil {
		return
	}
	deadline := time.Now().Add(dprovider.UsageDrainTimeout)
	for {
		if n := s.reader.Buffered(); n > 0 {
			// Only consume a COMPLETE buffered line: ReadString blocks
			// for '\n', so a partial line with no further data would hang
			// the drain past its bound. Peek never blocks for buffered
			// bytes; an incomplete line keeps polling until the deadline.
			// Blank separator lines (SSE events end with "\n\n") are
			// skipped, not terminal: the usage event follows them.
			peeked, err := s.reader.Peek(n)
			if err != nil {
				return
			}
			if !bytes.Contains(peeked, []byte{'\n'}) {
				if !time.Now().Before(deadline) {
					return
				}
				time.Sleep(2 * time.Millisecond)
				continue
			}
			line, err := s.reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				continue
			}
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				return
			}
			var chunk openrouterResponse
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				return
			}
			if chunk.Usage != nil {
				s.finalUsage = chunk.Usage
				s.usage.recordUsageFull(chunk.Usage.ProviderUsage())
			}
			return
		}
		if !time.Now().Before(deadline) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// closeTerminalOnFinish records a terminal finish_reason and tears the
// channel down immediately (Stream Terminal Invariant): cancel context,
// close the body, mark closed, and complete usage. Pending bytes are preserved
// in s.pending; callers flush them before returning EOF.
func (s *openrouterSSEReader) closeTerminalOnFinish(reason string) {
	s.finishReason = reason
	s.usage.markCompleted(time.Now(), reason)
	if s.lifecycle != nil {
		s.lifecycle.MarkClosed()
	}
	s.stopIdle()
	// A terminal finish_reason is authoritative. Do not drain the HTTP body:
	// OpenRouter may keep the connection open after the terminal event, and a
	// drain would wait for the request context instead of returning control to
	// the caller.
	s.closed = true
	s.closeOnce.Do(func() {
		_ = closeSSERequest(s.cancel, s.body, s.closeTransport)
	})
}

func (s *openrouterSSEReader) Read(p []byte) (int, error) {
	// Drain any bytes left over from a previous parsed event before doing
	// any new work. This is what makes Read() safe to call with any buffer
	// size without losing data.
	if len(s.pending) > 0 {
		n := copy(p, s.pending)
		s.pending = s.pending[n:]
		return n, nil
	}

	if s.closed {
		return 0, io.EOF
	}

	if s.reader == nil {
		s.reader = bufio.NewReader(s.body)
	}
	s.armIdle()

	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			// A cutoff before [DONE]/finish_reason is an interrupted stream,
			// not a reason to leave the HTTP body parked until the parent
			// context expires. Close it synchronously and finalize telemetry.
			// ReadString may return a final unterminated line; preserve it
			// when it is an explicit [DONE] sentinel.
			trimmed := strings.TrimSpace(line)
			if trimmed == "data: [DONE]" {
				s.closed = true
				if s.lifecycle != nil {
					s.lifecycle.MarkClosed()
				}
				s.stopIdle()
				s.usage.markCompleted(time.Now(), s.finishReason)
				s.closeOnce.Do(func() { _ = closeSSERequest(s.cancel, s.body, s.closeTransport) })
				return 0, io.EOF
			}
			s.usage.markInterrupted()
			if s.lifecycle != nil {
				s.lifecycle.MarkClosed()
			}
			s.stopIdle()
			s.closed = true
			s.closeOnce.Do(func() { _ = closeSSERequest(s.cancel, s.body, s.closeTransport) })
			return 0, err
		}
		line = strings.TrimRight(line, "\r\n")

		if line == "" {
			continue
		}

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		if data == "[DONE]" {
			if tail := s.think.takeResidue(); len(tail) > 0 {
				s.pending = append(s.pending, tail...)
			}
			s.closed = true
			if s.lifecycle != nil {
				s.lifecycle.MarkClosed()
			}
			s.stopIdle()
			s.usage.markCompleted(time.Now(), s.finishReason)
			// [DONE] is a semantic terminal event. Close the response body
			// immediately and never wait for a server-side EOF: some gateways
			// deliberately keep the HTTP stream open after the sentinel.
			s.closeOnce.Do(func() {
				_ = closeSSERequest(s.cancel, s.body, s.closeTransport)
			})
			if len(s.pending) > 0 {
				n := copy(p, s.pending)
				s.pending = s.pending[n:]
				return n, nil
			}
			return 0, io.EOF
		}

		var chunk openrouterResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		if chunk.Usage != nil {
			s.finalUsage = chunk.Usage
			s.usage.recordUsageFull(chunk.Usage.ProviderUsage())
		}

		// Telemetry Accuracy Invariant (Phase 6.4.2): usage-only chunks
		// (len(choices) == 0 with a usage object — including a trailing
		// usage event that follows finish_reason) MUST be captured into
		// the usage tracker before the channel is torn down. The usage
		// above is already recorded; skipping the choice logic preserves
		// it downstream via Usage().
		if dprovider.IsUsageOnlyChunk(len(chunk.Choices), chunk.Usage != nil) {
			continue
		}

		if len(chunk.Choices) == 0 {
			continue
		}

		// Stream Terminal Invariant (Phase 6.4.1): finish_reason != ""
		// closes the output channel immediately — never wait for [DONE].
		// A terminal chunk may still carry content/tool deltas, so those are
		// emitted below before the transport is closed.
		terminal := dprovider.ShouldCloseOnFinishReason(chunk.Choices[0].FinishReason)
		if chunk.Choices[0].Delta != nil {
			delta := chunk.Choices[0].Delta
			// Reasoning content: emit wrapped in sentinel so the UI can
			// separate it from the message content buffer. Any bytes that
			// don't fit in p are held in s.pending and drained on the next
			// Read() call — never dropped, and never split without keeping
			// the remainder for delivery (see s.pending doc comment).
			// Some models/gateways report reasoning under "reasoning" instead
			// of "reasoning_content"; both are routed identically. Inline
			// <think>…</think> blocks inside delta.content are extracted into
			// the same sentinel channel by s.think.
			var out []byte
			reasoningText := delta.ReasoningContent
			if reasoningText == "" {
				reasoningText = delta.Reasoning
			}
			if reasoningText != "" {
				s.usage.recordReasoning(len(reasoningText))
				if s.lifecycle != nil {
					s.lifecycle.NoteTokens(len(reasoningText))
				}
				out = append(out, ReasoningSentinel...)
				out = append(out, reasoningText...)
				out = append(out, ReasoningSentinel...)
			}
			if delta.Content != "" {
				s.usage.recordOutput(len(delta.Content))
				if s.lifecycle != nil {
					s.lifecycle.NoteTokens(len(delta.Content))
				}
				out = append(out, s.think.write([]byte(delta.Content))...)
			}
			if len(out) > 0 {
				if terminal {
					if tail := s.think.takeResidue(); len(tail) > 0 {
						s.pending = append(s.pending, tail...)
					}
					s.drainTrailingUsage()
					s.closeTerminalOnFinish(chunk.Choices[0].FinishReason)
				}
				s.stopIdle()
				n := copy(p, out)
				if n < len(out) {
					s.pending = append(s.pending, out[n:]...)
				}
				return n, nil
			}
			if len(delta.ToolCalls) > 0 {
				// Concatenate all tool-call deltas for this event into one
				// pending buffer rather than returning after the first
				// truncated chunk and silently discarding the rest — a
				// dropped tail here corrupts the tool-call JSON and causes
				// the downstream json.Unmarshal in the consumer to fail
				// silently, losing the whole tool call (and, with it, any
				// file mutation it carried).
				var all []byte
				for _, tc := range delta.ToolCalls {
					tcData, err := json.Marshal(tc)
					if err != nil {
						continue
					}
					all = append(all, ToolCallSentinel...)
					all = append(all, tcData...)
					all = append(all, ToolCallSentinel...)
				}
				if len(all) > 0 {
					s.usage.recordOutput(len(all))
					if s.lifecycle != nil {
						s.lifecycle.NoteTokens(len(all))
					}
					if terminal {
						if tail := s.think.takeResidue(); len(tail) > 0 {
							s.pending = append(s.pending, tail...)
						}
						s.drainTrailingUsage()
						s.closeTerminalOnFinish(chunk.Choices[0].FinishReason)
					}
					s.stopIdle()
					n := copy(p, all)
					if n < len(all) {
						s.pending = append(s.pending, all[n:]...)
					}
					return n, nil
				}
			}
		}

		if dprovider.ShouldCloseOnFinishReason(chunk.Choices[0].FinishReason) {
			if tail := s.think.takeResidue(); len(tail) > 0 {
				s.pending = append(s.pending, tail...)
			}
			s.drainTrailingUsage()
			s.closeTerminalOnFinish(chunk.Choices[0].FinishReason)
			if len(s.pending) > 0 {
				n := copy(p, s.pending)
				s.pending = s.pending[n:]
				return n, nil
			}
			return 0, io.EOF
		}
	}
}

func (s *openrouterSSEReader) Close() error {
	s.closed = true
	if s.lifecycle != nil {
		s.lifecycle.MarkClosed()
	}
	s.stopIdle()
	var err error
	s.closeOnce.Do(func() {
		err = closeSSERequest(s.cancel, s.body, s.closeTransport)
	})
	return err
}
