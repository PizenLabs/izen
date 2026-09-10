# IMPLEMENTATION PLAN: Unified Model Authority Refactor

## Current State Summary

The codebase has **two partially-implemented model resolution systems** that don't connect:

1. **`internal/runtime/authority/resolver.go`** — Has correct `ModelBinding`, `ModelState`, `ModelPolicy`, and `ResolveModel()` but is **not used by any execution path**.
2. **`internal/runtime/authority.go`** — `RuntimeAuthority` uses `map[WorkspaceTarget]ModelRef` (5-slot matrix) — the old design to be removed.
3. **Config cascade** (`ActiveModelName()`) — The actual execution path reads `SessionModel → Provider.DefaultModel → Models.Default` — a hidden fallback chain.
4. **Pipeline Router** — `SyncTiers()` passes empty strings; `FallbackModel()` is dead code (0 callers).
5. **Provider adapters** — `resolveModel()` falls back to `c.model` (hardcoded provider-level default).

## Execution Path (Current)

```
streamCmd → m.cfg.ActiveModelName()
  → Models.SessionModel || Provider.DefaultModel || Models.Default || ""
→ ai.Request{Model: ...}
→ provider.ExecuteStream(ctx, req)
→ adapter.resolveModel(req.Model)  // falls back to c.model
```

## Execution Path (Target)

```
streamCmd → resolveModelForExecution(intent)
  → authority.ResolveModel(intent, ModelState, ModelPolicy)
  → ModelBinding{ProviderID, ModelID, VariantParams}
→ ai.Request{Model: binding.ModelID, Provider: binding.ProviderID}
→ provider.ExecuteStream(ctx, req)
→ adapter receives exact binding — no fallback
```

---

## Phase 1: Core Types & Runtime Authority

### 1.1 Refactor `internal/runtime/authority.go`

**Remove:**
- `assignments map[WorkspaceTarget]ModelRef` — the 5-slot matrix
- `SeedAssignment()`, `PrepareTransition()`, `CommitTransition()` — per-workspace assignment logic
- `EffectiveModel(target)` — per-target lookup

**Replace with:**
```go
type RuntimeAuthority struct {
    mu       sync.RWMutex
    active   authority.ModelBinding  // atomic active binding
    policy   authority.ModelPolicy   // role-based overrides
}
```

**New methods:**
- `Activate(binding authority.ModelBinding)` — atomically sets active binding
- `ActiveBinding() authority.ModelBinding` — returns current active binding
- `SetPolicy(policy authority.ModelPolicy)` — sets role-based policy
- `Policy() authority.ModelPolicy` — returns current policy
- `ResolveForIntent(intent string) (authority.ModelBinding, error)` — delegates to `authority.ResolveModel()`
- `CurrentMode() string` — retained for UI mode tracking (read-only, not a model authority)

**Keep `CurrentMode`/`SetCurrentMode`** — but only for UI mode display, NOT for model resolution.

### 1.2 Update `internal/runtime/authority/resolver.go`

- Add `ErrProviderDisabled` error type for unavailable providers
- `ResolveModel()` logic stays the same (already correct per spec Rules 1-6)
- Add `ValidateBinding()` convenience function for provider adapter validation

### 1.3 Update `internal/core/domain/model_picker.go`

- **Remove** `WorkspaceTarget` type and all 5 constants
- **Remove** `AllWorkspaceTargets` slice
- **Remove** `IsValid()` method
- **Keep** `ModelRef` (used by `ModelTransitionEvent`)
- **Keep** `ModelTransitionEvent` (used by transcript logging)
- **Keep** `ReasoningOption`, `ReasoningCapability`, `ReasoningPolicy`

---

## Phase 2: Config Migration

### 2.1 Refactor `internal/config/config.go`

**Remove from `Config` struct:**
- `DefaultModel string` (legacy)
- `PlanModel string` (legacy)

**Replace `AssignmentsConfig` with:**
```go
type ActiveBindingConfig struct {
    Provider    string `yaml:"provider"`
    Model       string `yaml:"model"`
    Variant     string `yaml:"variant,omitempty"`
}

type BindingsConfig struct {
    Active    ActiveBindingConfig             `yaml:"active"`
    Policy    map[string]ActiveBindingConfig  `yaml:"policy,omitempty"` // role → binding
}
```

**Add migration:**
```go
func (c *Config) MigrateLegacyAssignments() bool {
    // Migrate 5-slot → single active binding
    // First non-empty assignment becomes Active
    // Preserve as policy overrides if role-specific
}
```

**Replace `PersistAssignment()`:**
```go
func PersistActiveBinding(provider, model, variant string) error {
    cfg := GetGlobalConfig()
    cfg.Bindings.Active = ActiveBindingConfig{Provider: provider, Model: model, Variant: variant}
    return Save(cfg)
}
```

**Replace `ActiveModelName()`:**
```go
func (c *Config) ActiveModelName() string {
    if c.Bindings.Active.Model != "" {
        return c.Bindings.Active.Model
    }
    return "" // zero fallback
}
```

**Replace `ActiveProviderName()`:**
```go
func (c *Config) ActiveProviderName() string {
    if c.Bindings.Active.Provider != "" {
        return c.Bindings.Active.Provider
    }
    return ""
}
```

### 2.2 Update `internal/config/resolver_test.go`

Update all tests to use new `BindingsConfig` structure.

---

## Phase 3: UI Model Refactor

### 3.1 Update `internal/ui/model.go`

**Remove:**
- `sessionModel string` field — no longer needed
- `modelRuntime *appruntime.RuntimeAuthority` — replaced by unified authority

**Add:**
- `modelAuthority *authority.RuntimeAuthority` — the single authority

**Replace `getActiveModelName()`:**
```go
func (m *model) getActiveModelName() string {
    if m.modelAuthority != nil {
        if b := m.modelAuthority.ActiveBinding(); b.ModelID != "" {
            return string(b.ModelID)
        }
    }
    return m.cfg.ActiveModelName()
}
```

**Replace `getActiveProviderName()`:**
```go
func (m *model) getActiveProviderName() string {
    if m.modelAuthority != nil {
        if b := m.modelAuthority.ActiveBinding(); b.ProviderID != "" {
            return string(b.ProviderID)
        }
    }
    return m.cfg.ActiveProviderName()
}
```

**Replace `ensureModelRuntime()`:**
```go
func (m *model) ensureModelAuthority() *authority.RuntimeAuthority {
    if m.modelAuthority == nil {
        m.modelAuthority = authority.NewRuntimeAuthority()
        // Bootstrap from persisted config
        if m.cfg != nil && m.cfg.Bindings.Active.Model != "" {
            m.modelAuthority.Activate(authority.ModelBinding{
                ProviderID: authority.ProviderID(m.cfg.Bindings.Active.Provider),
                ModelID:    authority.ModelID(m.cfg.Bindings.Active.Model),
                VariantParams: authority.VariantOption(m.cfg.Bindings.Active.Variant),
            })
        }
    }
    return m.modelAuthority
}
```

### 3.2 Update `internal/ui/model_picker_wiring.go`

**Replace `commitModelAssignment()`:**
```go
func (m *model) commitModelAssignment(msg model_picker.ModelAssignmentRequestedMsg) tea.Cmd {
    auth := m.ensureModelAuthority()
    binding := authority.ModelBinding{
        ProviderID: authority.ProviderID(msg.Provider),
        ModelID:    authority.ModelID(msg.ModelID),
    }
    // Validate binding
    if err := authority.ValidateBinding(binding); err != nil {
        m.push(roleError, fmt.Sprintf("[✗] Model assignment rejected: %s", err.Error()))
        return nil
    }
    // Persist first (I3)
    if err := config.PersistActiveBinding(msg.Provider, msg.ModelID, string(msg.Policy.Reasoning)); err != nil {
        m.push(roleError, fmt.Sprintf("[✗] Model assignment persist failed: %s", err.Error()))
        return nil
    }
    // Commit to runtime (I4)
    auth.Activate(binding)
    // Sync pipeline
    m.syncPipelineTiers()
    // Provider switch if needed
    var followCmd tea.Cmd
    if msg.Provider != "" {
        followCmd = m.switchProviderIfNeeded(msg.Provider)
    }
    m.push(roleSystem, fmt.Sprintf("✓ Model set to %s/%s", msg.Provider, msg.ModelID))
    m.refreshViewportContent()
    m.gotoBottomIfAllowed()
    m.showModelPicker = false
    m.ti.Focus()
    if followCmd != nil {
        return tea.Batch(followCmd, model_picker.CloseModalCmd())
    }
    return model_picker.CloseModalCmd()
}
```

**Replace `applyPickerActivation()`:**
Same pattern — use `auth.Activate()` + `config.PersistActiveBinding()`.

**Replace `syncPipelineTiers()`:**
```go
func (m *model) syncPipelineTiers() {
    if m.modelAuthority == nil {
        return
    }
    var eng *pipeline.Engine
    // ... resolve engine ...
    if eng == nil {
        return
    }
    eng.Router().SyncTiers(func(i pipeline.Intent) (string, string) {
        b, err := m.modelAuthority.ResolveForIntent(pipeline.IntentToMode(i))
        if err != nil {
            return "", ""
        }
        return string(b.ModelID), string(b.ProviderID)
    })
}
```

### 3.3 Update `internal/ui/widgets/model_picker/types.go`

- **Remove** `WorkspaceTarget` type and all constants (already deprecated)
- **Remove** `AllWorkspaceTargets`
- **Remove** `Target` field from `ModelAssignmentRequestedMsg` (or make it empty/deprecated)

### 3.4 Update `internal/ui/update_init.go`

- Remove `sessionModel` references from `getActiveModelName()`
- Update `ensureModelRuntime()` → `ensureModelAuthority()`

---

## Phase 4: Pipeline Router

### 4.1 Update `internal/engine/pipeline/router.go`

- **Remove** `FallbackModel()` method — dead code (0 callers)
- **Remove** `fallback string` field from `Router`
- **Remove** `WithFallbackModel()` option
- **Update** `SyncTiers()` — the resolve function now returns real values from authority
- **Update** `RouteFor()` — no fallback chain; if model is empty, return error profile

### 4.2 Update `internal/engine/pipeline/router_test.go`

Update tests to remove fallback model test cases.

---

## Phase 5: Provider Adapters

### 5.1 `internal/providers/openrouter.go`

Already strict — no changes needed. `resolveModel()` returns error on empty model.

### 5.2 `internal/providers/ollama.go`

Already strict — no changes needed.

### 5.3 `internal/llm/anthropic.go`

**Remove** fallback in `resolveModel()`:
```go
func (c *AnthropicClient) resolveModel(override string) string {
    if override != "" {
        return override
    }
    return "" // was: return c.model
}
```

Or better: make `resolveModel` return error when override is empty, matching OpenRouter's pattern.

### 5.4 `internal/llm/openai.go`

Same as anthropic — remove `c.model` fallback.

### 5.5 `internal/llm/ollama.go`

Same — remove `c.model` fallback.

### 5.6 `internal/llm/groq.go`

Delegates to OpenAI — fix propagates.

---

## Phase 6: Execution Path

### 6.1 `internal/execution/executor.go`

**Update `resolveModel()`** to use authority:
```go
func (x *RuntimeExecutor) resolveModel(req ExecuteRequest) (string, error) {
    // Model MUST come from req.Model — no fallback
    model := strings.TrimSpace(req.Model)
    if model == "" {
        return "", fmt.Errorf("%w [%s]", ErrUnassignedTargetModel, req.Mode)
    }
    // Provider validation
    p := x.provider
    if p == nil {
        return "", fmt.Errorf("executor: no provider configured")
    }
    if !modelBelongsTo(p.Name(), model) {
        return "", fmt.Errorf("%w: model %q does not belong to provider %q",
            ErrProviderModelMismatch, model, p.Name())
    }
    return model, nil
}
```

### 6.2 `internal/ui/stream.go`

**Update `streamCmd()`** to resolve model through authority:
```go
// Before building ai.Request:
var modelID string
var providerName string
if m.modelAuthority != nil {
    b, err := m.modelAuthority.ResolveForIntent("conversation")
    if err == nil {
        modelID = string(b.ModelID)
        providerName = string(b.ProviderID)
    }
}
if modelID == "" {
    modelID = m.cfg.ActiveModelName()
}
req := ai.Request{
    Model:     modelID,
    Messages:  msgs,
    Stream:    true,
    System:    systemPrompt,
    MaxTokens: maxTokens,
}
```

---

## Phase 7: Negative Architecture Tests

### 7.1 Create `test/integration/model_authority_negative_test.go`

```go
// Test that no production code path creates model identifiers independently
// of the Model Resolution authority.

func TestNoDefaultModelFallback(t *testing.T) {
    // Grep for: defaultModel, fallbackModel, WorkspaceTarget model authority,
    // IntentTier model authority, provider-local default execution model,
    // UI-created model identifiers
    // Assert: zero matches in production code (excluding tests/fixtures/docs)
}

func TestNoDuplicateModelAuthority(t *testing.T) {
    // Verify that RuntimeAuthority is the only source of active model state
    // No other struct holds a "current model" field
}

func TestResolveModelIsSolePath(t *testing.T) {
    // Verify all execution paths go through ResolveModel or equivalent
}
```

---

## Phase 8: Integration Tests

### 8.1 Update `test/integration/model_authority_test.go`

Rewrite all tests (A-H) to use the new `RuntimeAuthority` directly:

- **Test A** — Unified Active Model: Set one binding, resolve all intents
- **Test B** — Explicit Policy Override: Set policy for plan role, verify override
- **Test C** — Invalid Provider Binding: Verify `ErrProviderModelMismatch`
- **Test D** — Conversation Path: Verify `ResolveForIntent("ask")` returns exact binding
- **Test E** — Provider Switch: Verify no implicit model carryover
- **Test F** — Credential Security: Verify no API keys in logs/serialization
- **Test G** — Dynamic Catalog: Verify live provider data, not static lists
- **Test H** — Capability Truth: Verify supported/unsupported/unknown states

---

## Phase 9: Cleanup

### 9.1 Remove dead code
- `FallbackModel()` in router
- `WithFallbackModel()` option
- `DefaultModel`/`PlanModel` legacy config fields
- `AssignmentsConfig` (5-slot)
- `PersistAssignment()` (old)
- `WorkspaceTarget` from `model_picker/types.go`
- `AllWorkspaceTargets` from both packages
- `sessionModel` from UI model
- Provider `resolveModel()` fallbacks to `c.model`

### 9.2 Update all callers
- `syncPipelineTiers()` → resolve through authority
- `getActiveModelName()` → use authority binding
- `getActiveProviderName()` → use authority binding
- `renderRuntimeStatus()` → read from authority
- `renderStartupBanner()` → read from authority

---

## File Change Summary

| File | Action |
|------|--------|
| `internal/runtime/authority.go` | Major refactor: 5-slot → single binding |
| `internal/runtime/authority/resolver.go` | Minor: add `ValidateBinding`, `ErrProviderDisabled` |
| `internal/core/domain/model_picker.go` | Remove `WorkspaceTarget` + 5 constants |
| `internal/config/config.go` | Replace `AssignmentsConfig` → `BindingsConfig` |
| `internal/ui/model.go` | Replace `sessionModel` + `modelRuntime` → `modelAuthority` |
| `internal/ui/model_picker_wiring.go` | Refactor commit/activation to use authority |
| `internal/ui/update_init.go` | Update bootstrap/seeding |
| `internal/ui/widgets/model_picker/types.go` | Remove deprecated `WorkspaceTarget` |
| `internal/engine/pipeline/router.go` | Remove `FallbackModel`, update `SyncTiers` |
| `internal/llm/anthropic.go` | Remove `c.model` fallback |
| `internal/llm/openai.go` | Remove `c.model` fallback |
| `internal/llm/ollama.go` | Remove `c.model` fallback |
| `internal/execution/executor.go` | Clean up `resolveModel` |
| `internal/ui/stream.go` | Resolve model through authority |
| `test/integration/model_authority_test.go` | Rewrite all tests |
| `test/integration/model_authority_negative_test.go` | New: architecture enforcement tests |

---

## Risk Assessment

1. **Config migration** — Legacy `AssignmentsConfig` must migrate gracefully. The `MigrateLegacyAssignments()` function handles this.
2. **UI session model** — `sessionModel` is used in 11 places. Each must be replaced with `modelAuthority.ActiveBinding()`.
3. **Pipeline sync** — `SyncTiers()` currently passes empty strings. The new implementation resolves through authority.
4. **Provider adapter contracts** — Removing `c.model` fallback means every path MUST provide model in request. This is enforced by `resolveModel()` validation.

## Dependencies

- Phase 1 (core types) must complete before Phase 2 (config)
- Phase 2 (config) must complete before Phase 3 (UI)
- Phase 3 (UI) must complete before Phase 6 (execution)
- Phase 4 (router) and Phase 5 (providers) are independent of each other
- Phase 7-8 (tests) come after all production code changes
- Phase 9 (cleanup) is last
