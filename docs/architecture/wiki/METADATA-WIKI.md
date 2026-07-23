# Cloud Model Metadata Registry & Cost Calculator

> Track every token. Know every cent. From LLM response to TUI status bar in one
> deterministic pipeline.

## Philosophy

The cost calculator is built on a simple discipline:

**If you can't measure it, you can't trust it.**

Every LLM call in Izen flows through a unified token-accounting layer that:

1. Extracts per-provider token metadata from raw API responses (input, output,
   prompt cache read/write)
2. Looks up the exact per-model pricing from a curated static catalog
3. Computes the precise USD cost using per-million-token rates
4. Surfaces the result in the TUI status bar and response footers

This aligns with Izen's core philosophy: **transparency at every layer.** The
user sees not just the response, but the exact cost, token breakdown, and
duration of every request.

---

## Model Metadata Catalog

The catalog (`internal/llm/metadata.go`) is a static `map[string]ModelMetadata`
populated with pre-configured pricing for all supported Cloud LLMs.

### Struct

```go
type ModelMetadata struct {
    ID                 string
    Name               string
    Provider           string
    InputCostPerM      float64   // USD per 1M tokens
    OutputCostPerM     float64   // USD per 1M tokens
    CacheWriteCostPerM float64   // USD per 1M tokens (Anthropic prompt caching)
    CacheReadCostPerM  float64   // USD per 1M tokens (Anthropic prompt caching)
    ContextWindow      int
}
```

### Covered Models

| Provider   | Models                                                                         |
|------------|--------------------------------------------------------------------------------|
| Anthropic  | Claude Sonnet 4, Claude 4, Claude Opus 4, Claude 3.5 Sonnet/Haiku, Claude 3   |
| OpenAI     | GPT-4o, GPT-4o-mini, GPT-4 Turbo, GPT-4, GPT-3.5 Turbo, o1, o1-mini, o3-mini |
| DeepSeek   | deepseek-chat (V3), deepseek-reasoner (R1)                                     |
| Gemini     | gemini-1.5-pro, gemini-1.5-flash                                               |

### Lookup

`GetModelMetadata(modelID string) *ModelMetadata` performs an exact-match lookup
on the catalog map. Returns `nil` for unknown models — the calculator gracefully
falls back to zero cost.

---

## Cost Calculator

The calculator (`internal/llm/cost_calculator.go`) converts raw token counts into
precise USD cost and produces a complete `UsageReport`.

### UsageReport

```go
type UsageReport struct {
    InputTokens      int
    OutputTokens     int
    CacheWriteTokens int
    CacheReadTokens  int
    TotalTokens      int       // computed: input + output + cache write + cache read
    TotalCostUSD     float64   // computed: per-token rates × token counts
    DurationMs       int64
}
```

### CalculateCost

```go
func CalculateCost(modelID string, usage UsageReport) UsageReport
```

The algorithm:

1. **Sum total tokens** — `input + output + cache_write + cache_read`
2. **Ollama short-circuit** — if the model is an Ollama provider, force
   `TotalCostUSD = 0.0` and return immediately
3. **Metadata lookup** — if `GetModelMetadata` returns nil, return with cost
   left at zero
4. **Compute** — apply per-million-token rates for input, output, cache write,
   and cache read, then sum:

```
cost = (input_tokens / 1_000_000) × input_rate
     + (output_tokens / 1_000_000) × output_rate
     + (cache_write_tokens / 1_000_000) × cache_write_rate
     + (cache_read_tokens / 1_000_000) × cache_read_rate
```

5. **Round** — truncate to 8 decimal places via `math.Round`

### OpenRouter Dynamic Cost

When OpenRouter provides a `usage.cost` field in the API response, the provider
calls `CostFromOpenRouter(cost, usage)` to override the locally-computed value:

```go
func CostFromOpenRouter(cost float64, usage UsageReport) UsageReport
```

This ensures OpenRouter's negotiated rate (which may differ from catalog pricing)
takes precedence when available.

---

## Provider Integration

Each provider extracts provider-specific token metadata from the API response and
populates the relevant `LLMResponse` fields. The pipeline is:

```
API Response → Provider Parse → LLMResponse (raw tokens) → CalculateCost → UsageReport
```

### Extended LLMResponse

```go
type LLMResponse struct {
    Content          string
    TokenInput       int
    TokenOutput      int
    CacheWriteTokens int     // prompt cache creation tokens (Anthropic)
    CacheReadTokens  int     // prompt cache read tokens (Anthropic/OpenAI)
    TotalCostUSD     float64 // computed by CalculateCost
    DurationMs       int64   // request duration
}
```

### Anthropic

Anthropic's API returns cache metadata in the `usage` block of both the
`message_start` event (streaming) and the final response object.

| JSON Field                          | LLMResponse Field    |
|-------------------------------------|----------------------|
| `usage.input_tokens`                | `TokenInput`         |
| `usage.output_tokens`               | `TokenOutput`        |
| `usage.cache_creation_input_tokens` | `CacheWriteTokens`   |
| `usage.cache_read_input_tokens`     | `CacheReadTokens`    |

In streaming mode, `message_start` carries input + cache tokens, while
`message_delta` carries output tokens (`cache_creation_input_tokens` and
`cache_read_input_tokens` are only present in `message_start`).

### OpenAI / OpenRouter

OpenAI's API provides cache metadata in `usage.prompt_tokens_details`:

| JSON Field                                    | LLMResponse Field    |
|-----------------------------------------------|----------------------|
| `usage.prompt_tokens`                         | `TokenInput`         |
| `usage.completion_tokens`                     | `TokenOutput`        |
| `usage.prompt_tokens_details.cached_tokens`   | `CacheReadTokens`    |
| `usage.cost` (OpenRouter only)                | `TotalCostUSD`       |

When the base URL contains `"openrouter"`, the provider:
1. Computes a local estimate via `CalculateCost` as a baseline
2. Overrides with `usage.cost` from the API response when `cost > 0`

### Ollama

Ollama local models are always free. Both `GenerateResponse` and
`StreamResponse` set `TotalCostUSD = 0` unconditionally. Token counts are
estimated at ~¼ of character length when the API response lacks usage
metadata.

---

## Display Format

Cost is formatted to **4 decimal places** for the TUI status bar and message
footer logs:

```
✓ done · +128 tok · $0.0012 · 1.4s
```

Display rules:
- **Cloud models**: `$0.0012` (4 decimal places, trimmed trailing zeros optional)
- **Ollama local models**: `$free` (replaces `$0.0000`)
- **Duration**: seconds with one decimal place

---

## File Layout

```
internal/llm/
├── metadata.go            # ModelMetadata struct + static pricing catalog
├── cost_calculator.go     # UsageReport, CalculateCost, CostFromOpenRouter
├── cost_calculator_test.go # 13 tests: pricing, caching, OpenRouter, Ollama
├── provider.go            # LLMResponse (extended with cache/cost fields)
├── anthropic.go           # Cache token extraction from streaming SSE + response
├── openai.go              # Cached tokens + OpenRouter cost from usage metadata
├── ollama.go              # TotalCostUSD forced to 0
├── groq.go                # (unchanged — delegates to OpenAIClient)
├── stream.go              # SSE reader (unchanged)
├── sanitize.go            # (unchanged)
├── registry.go            # (unchanged)
├── registry_test.go       # (unchanged)
└── llm_test.go            # Updated ProviderAdapter signatures
```

---

## Contract Guarantees

| Guarantee | Enforcement |
|---|---|
| **Deterministic cost** | Same tokens + model → same cost, every time |
| **Ollama always free** | Provider short-circuits before metadata lookup |
| **OpenRouter override** | API `usage.cost` takes precedence when > 0 |
| **Graceful fallback** | Unknown models return cost = $0, never error |
| **Cache precision** | Cache write/read tokens tracked separately per provider spec |
| **No floating-point drift** | Rounded to 8 decimal places internally, 4 for display |
| **Catalog is static** | Pricing is compile-time constant; no runtime API calls |
