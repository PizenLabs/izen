# Adaptive Runtime: Contract Promotion, Wire Policy & Role Fallback

This document is the contract for how Izen decides **how** to call a model and
**when** it may switch to a different one. It supersedes the older behavior in
which Izen pre-flight-guarded models and silently reverted the user's selection.

## 1. Nothing is ever reverted implicitly

There is exactly one mechanism that can change which model answers a turn: the
**role fallback chain** the user declares in `~/.izen/config.yml`. No code path
in Izen picks a "replacement" model on its own.

Removed (and covered by negative tests):

- the OpenRouter pre-flight eligibility guard (`guardModelExecutable`),
- the session-boot model reconciliation that reverted a restored binding,
- the `/model` local eligibility rejection,
- every "Reverted to workspace default …" notice in the stream, executor and
  gated-execution paths.

A failure now always surfaces its real cause.

## 2. Dynamic Contract Promotion (execution side)

`registry.RequiresAgenticHarnessWire(provider, model)` is a **positive**
wire-policy registry (`internal/provider/registry/wire_policy.go`): it lists
models whose provider requires an agentic tool schema on the wire. It is not a
blacklist — those models stay discovered, selectable and executable.

At dispatch, `promoteAgenticWireContract` (in `internal/providers/openrouter.go`)
elevates a toolless request to `ToolEnabledCompletion` and binds
`ai.ReadOnlyTools()`. When a read-only tool runner is available the provider also
runs the bounded in-process tool loop, so a `tool_calls` response is executed
and the final answer is produced. The Model Registry TUI surfaces the same truth
with an `[Agentic]` badge and an `Auto-Promote` runtime path.

## 3. VERIFIED LIMIT: OpenRouter's agentic-harness gate is a User-Agent allowlist

Contract promotion is **necessary but not sufficient** for the `:free` Inkling
models. Measured against the live API on 2026-09 with
`thinkingmachines/inkling-small:free`:

| request | result |
|---|---|
| tools present, `User-Agent: Go-http-client/1.1` (Go default) | 403 `Gate Free Endpoints by Agentic Harness` |
| no tools, `User-Agent: izen/0.2.0` | 403 `Gate Free Endpoints by Agentic Harness` |
| tools present, `User-Agent: izen/0.2.0` | 403 `Gate Free Endpoints by Agentic Harness` |
| tools present, `User-Agent: claude-cli/2.0.0` | 200 |
| tools present, `User-Agent: codex_cli_rs/0.1.0` | 200 |
| tools present, `User-Agent: opencode/0.2.0` | 200 |
| tools present, `User-Agent: cursor/0.2.0` / `cline/0.2.0` | 200 |
| tools present, `User-Agent: cli`, `agent`, `izen-cli`, `roo`, `aider` | 403 |

The 403 body carries the machine-readable reason:

```json
{"error":{"metadata":{"failed_routing_step":"Gate Free Endpoints by Agentic Harness"}}}
```

Conclusions, and what Izen does about each:

1. **The gate keys on `User-Agent`, not on the wire contract.** Izen therefore
   cannot pass it by binding tools.
2. **Izen never impersonates another product.** Sending `claude-cli/…` while
   running Izen would be a false claim about who is calling, so it is not done.
   Izen sends a truthful `User-Agent: izen/0.2 (agentic coding harness; …)`
   (`openRouterUserAgent`) instead of Go's opaque default.
3. **The gate is a provider access policy, not a model incompatibility.** It is
   classified as `ErrOpenRouterAgenticGate` (distinct from
   `ErrOpenRouterModelIncompatible`) and rendered as an actionable,
   **reversion-free** notice: the active model is unchanged, and the user is
   pointed at the three real remedies (non-gated model id, a configured
   `roles.<role>.fallback`, or registering Izen at openrouter.ai/apps).
4. **The paid catalog id is not gated.** `thinkingmachines/inkling-small`
   (no `:free`) answers `200` with **no tools at all**, so it is deliberately
   absent from the wire-policy registry and renders as a standard model.

## 4. Role fallback chain (user-configured)

```yaml
roles:
  plan:
    model: "openrouter/thinkingmachines/inkling-small:free"
    fallback: "openrouter/anthropic/claude-3.5-sonnet"
  default:
    fallback: "openrouter/anthropic/claude-3.5-sonnet"
```

- Values are bare model IDs (resolved against the active provider) or
  `provider/model` slugs. A leading segment counts as a provider only when it
  names a provider Izen can execute, because an OpenRouter model ID already
  contains a slash.
- `model` declares the primary the chain belongs to. The chain fires for the
  role of the current turn when that primary matches the active model, or for
  any role whose declared primary matches the failing model, or for an entry
  that declares only a `fallback`.
- A fallback equal to the failed primary is refused (no loop).
- Resolution: `config.RoleChainFor` (pure, tested). Dispatch:
  `ui.executeStreamWithRoleFallback`.

### Trigger matrix

| failure | fallback? |
|---|---|
| timeout / deadline (`context.DeadlineExceeded`, `os.ErrDeadlineExceeded`, `net.Error.Timeout`, stream idle watchdog) | yes |
| transport failure (dial refused, DNS, reset, TLS, truncated stream) | yes |
| HTTP 429 | yes |
| HTTP 5xx | yes |
| HTTP 400 / 401 / 403 / 404 and other 4xx | no — the request itself is wrong |
| agentic-harness routing gate or compatibility refusal | no — promotion territory, never a model switch |
| user cancellation | no |
| local contract / decode failure | no |

The chain is attempted exactly once; a failing fallback surfaces the real error.

### Trace event

```
[fallback] Primary model failed (rate limit (HTTP 429)). Switched to openrouter/anthropic/claude-3.5-sonnet.
```

The switch is reported **before** the fallback is dispatched (pinned to the
stream channel so it always precedes the fallback's first token), and the user's
active binding is never rewritten.

## 5. Model Registry table layout

`internal/ui/widgets/model_picker/view.go` resolves the four columns per render
(`tableColumns`):

- **context / price** — natural width of the widest formatted cell in the
  window, capped (`tableCtxColW`, `tablePriceColW`),
- **badges** — the remaining budget, with the ID minimum reserved first; the
  full vocabulary (`[Agentic] [Thinking] [Vision] [Tools]`) is used when the
  viewport is wider than 100 columns **and** it genuinely fits, otherwise the
  compact vocabulary (`[Ag] [Th] [Vis] [Tl]`) is used,
- **model ID** — everything else, never below `tableIDMinW`, and
  **middle-truncated** so the vendor prefix and the version/variant tail both
  survive (`models/gemini-3.8-pro-ex…extended-thinking`).

A badge cell that cannot fit drops its trailing badges; a capability is never
rendered as a clipped word such as `[Think…`. Rows stay exactly one line tall
and share one visible width at 80x24, 100x30 and 140x40.
