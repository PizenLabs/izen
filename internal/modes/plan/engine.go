package plan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/core/stream"
	"github.com/PizenLabs/izen/internal/discovery/recon"
	"github.com/PizenLabs/izen/internal/domain/signal"
	"github.com/PizenLabs/izen/internal/domain/task"
	"github.com/PizenLabs/izen/internal/engine/layer3"
	"github.com/PizenLabs/izen/internal/engine/pipeline"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/prompt"
	"github.com/PizenLabs/izen/internal/protocol"
	"github.com/PizenLabs/izen/internal/retrieval"
	"github.com/PizenLabs/izen/internal/retrieval/grounding"
	wscap "github.com/PizenLabs/izen/internal/workspace/capability"
	wssnapshot "github.com/PizenLabs/izen/internal/workspace/snapshot"
)

// synthesisFailureMode classifies why the most recent plan-synthesis attempt was
// rejected, so the next retry's prompt augmentation targets the actual defect
// instead of always re-instructing SHELL_EXEC validation (which does nothing
// for a model that produces non-JSON prose on every attempt).
type synthesisFailureMode int

const (
	// failureNone is the initial state before any attempt is evaluated.
	failureNone synthesisFailureMode = iota
	// failureInvalidJSON: the model's output could not be parsed as a valid plan
	// JSON object/array even after structural repair (fences, comments,
	// auto-close). The retry re-emits the strict raw-JSON schema contract.
	failureInvalidJSON
	// failureInvalidShellExec: the JSON parsed but contained SHELL_EXEC tasks
	// whose targets are not runnable commands.
	failureInvalidShellExec
	// failureFilteredCandidates: the JSON parsed but every candidate task was
	// rejected by the evidence-based filters (non-existent targets, scope).
	failureFilteredCandidates
)

// ErrPlanAttemptTimeout is returned by Engine.complete when a single plan
// synthesis attempt exceeded the strict per-attempt deadline. Callers use
// errors.Is to distinguish "the provider hung" from other provider failures and
// fail fast to the heuristic task-extraction fallback instead of blocking the
// TUI for the full retry budget.
var ErrPlanAttemptTimeout = errors.New("plan engine: synthesis attempt timed out")

// ErrOutputTruncated is re-exported at the plan boundary so callers do not
// need to know which provider adapter produced the response.
var ErrOutputTruncated = ai.ErrOutputTruncated

// ErrPayloadTruncated is the historical plan-layer alias for the same
// canonical output-ceiling signal.
var ErrPayloadTruncated = ai.ErrPayloadTruncated

type OutputTruncatedError = protocol.OutputTruncatedError

// IsOutputTruncated reports whether a plan/provider error is an output
// ceiling signal.
func IsOutputTruncated(err error) bool { return errors.Is(err, ErrOutputTruncated) }

// NewOutputTruncated constructs the protocol-level typed truncation error.
func NewOutputTruncated(provider, reason string) error {
	return ai.NewOutputTruncated(provider, reason)
}

// IsPayloadTruncated is the historical plan-layer spelling.
func IsPayloadTruncated(err error) bool { return IsOutputTruncated(err) }

// planAttemptTimeout is the strict per-attempt deadline for plan synthesis HTTP
// calls against free/cloud models. OpenRouter free-tier models are frequently
// queued or cold-started for minutes; a 15s budget cuts a hung provider off fast
// so the engine can fail over to heuristic task extraction without freezing the
// live terminal. Each synthesis attempt (initial + retries) gets a fresh budget
// derived from the parent context.
//
// It is a var (not a const) so tests can shrink the deadline and exercise the
// fail-fast path deterministically without waiting 15s.
var planAttemptTimeout = 15 * time.Second

// useStrictAttemptTimeout reports whether a plan synthesis attempt against this
// model must be capped by the strict per-attempt deadline. Only free-tier cloud
// models carry the OpenRouter ":free" marker — the models reported to hang for
// minutes. Paid cloud and local SLMs keep the parent context budget because
// their legitimate prefill+generation latency routinely exceeds 15s.
func useStrictAttemptTimeout(modelName string) bool {
	name := strings.ToLower(strings.TrimSpace(modelName))
	return strings.HasSuffix(name, ":free") || strings.Contains(name, "-free")
}

// ProviderFunc defines a structured function signature matching the ai.Request format.
type ProviderFunc func(ctx context.Context, req ai.Request) (*ai.Response, error)

// StreamProviderFunc matches the ai.Provider.ExecuteStream signature. When
// wired, the plan engine performs its LLM synthesis through a streaming
// connection; the accumulated buffer is retained for telemetry and the
// provider-authenticated truncation signal is surfaced before structural
// parsing.
type StreamProviderFunc func(ctx context.Context, req ai.Request) (io.ReadCloser, error)

// Engine is the core interface for the plan module, coordinating between data store,
// parser, and AI provider to process plans.
type Engine struct {
	store        *PlanStore
	parser       func(string) []Task
	provider     ProviderFunc
	streamProv   StreamProviderFunc
	UserName     string   // collaborating engineer identity, injected into system prompts
	rootPath     string   // workspace root for file discovery
	AllowedFiles []string // grounded file tree for scope guard validation
	vanillaWeb   bool     // when true, skip Go-specific fast-track paths
	// archetype is the investigation/workspace language context used by every
	// fallback path. vanillaWeb remains as a compatibility projection for the
	// older frontend-only guards.
	archetype           recon.ProjectArchetype
	archetypeExplicit   bool
	archetypeFromLedger bool
	frontendOnly        bool

	// snapCache and capReg are injected at bootstrap for archetype-aware
	// diagnostic gating. They are optional; nil values are safe.
	snapCache *wssnapshot.SnapshotCache
	capReg    *wscap.ArchetypeCapabilityRegistry

	// bus is the event bus this engine publishes domain events to. Engines are
	// headless: they publish and never touch the UI directly. Optional; nil
	// disables event emission.
	bus *events.Bus

	// usageMu guards lastInput/lastOutput, the provider-reported token usage of
	// the most recent LLM synthesis. The UI reads it via LastUsage() to commit
	// token metrics to the status.Tracker even when the response was truncated
	// (finish_reason: "length").
	usageMu    sync.RWMutex
	lastInput  int
	lastOutput int

	// facade is the Layer 0-5 pipeline Facade injected by the composition
	// root. When wired and no direct provider is set, the plan engine delegates
	// its generative synthesis to the facade's ExecutePlan: the Mode engine
	// remains the Security & Boundary gate while the stateless pipeline owns
	// the LLM worker execution. Optional; nil keeps the legacy provider path.
	facade pipeline.Facade

	// interactionContract is the active semantic contract for synthesis.  A
	// plan normally derives it from the workspace; tests and embedding callers
	// may bind a descriptor explicitly.  It is descriptive metadata only.
	interactionContract   protocol.InteractionContract
	interactionDescriptor *protocol.ContractDescriptor
	contractBindingErr    error
	modelMetadata         protocol.ModelMetadata

	// contractMu protects the last committed descriptor and metadata snapshots
	// so a caller can inspect the exact contract used by a synthesis without
	// racing the provider callback.
	contractMu   sync.RWMutex
	lastContract *protocol.ContractDescriptor
}

// PlanEngine is the descriptive name used by Phase 12 protocol callers.
type PlanEngine = Engine

// NewEngine creates a new Engine instance with the provided components.
// Default parser is ParseJSONPlan — falls back to ParseMarkdownToTasks for legacy plans.
func NewEngine(store *PlanStore) *Engine {
	return &Engine{
		store:    store,
		parser:   parsePlanContent,
		provider: nil,
	}
}

// NewPlanEngine is a constructor alias for the Phase 12 descriptor-oriented
// API. The optional store keeps the descriptor-oriented zero-dependency form
// convenient for callers that provide a provider directly.
func NewPlanEngine(stores ...*PlanStore) *PlanEngine {
	var store *PlanStore
	if len(stores) > 0 {
		store = stores[0]
	}
	if store == nil {
		store = NewPlanStore()
	}
	return NewEngine(store)
}

// SetInteractionContract binds a semantic contract and optional descriptor to
// the next synthesis run.  The descriptor is normalized and copied so a
// caller cannot mutate the active execution boundary after admission.
func (e *Engine) SetInteractionContract(contract protocol.InteractionContract, descriptors ...*protocol.ContractDescriptor) error {
	if e == nil {
		return fmt.Errorf("plan engine: nil engine")
	}
	var descriptor protocol.ContractDescriptor
	if len(descriptors) > 0 && descriptors[0] != nil {
		descriptor = descriptors[0].Clone()
	} else {
		descriptor = protocol.Describe(contract)
	}
	normalized, err := descriptor.Normalize()
	if err != nil {
		e.contractBindingErr = fmt.Errorf("plan engine: %w: %w", protocol.ErrInvalidContract, err)
		return e.contractBindingErr
	}
	if contract.Valid() && normalized.Contract != contract {
		e.contractBindingErr = fmt.Errorf("plan engine: %w: descriptor contract %q does not match %q", protocol.ErrInvalidContract, normalized.Contract, contract)
		return e.contractBindingErr
	}
	if !contract.Valid() {
		contract = normalized.Contract
	}
	e.contractBindingErr = nil
	e.interactionContract = contract
	e.interactionDescriptor = &normalized
	return nil
}

// WithInteractionContract is the fluent form of SetInteractionContract.  An
// invalid descriptor is retained as a failed binding and is surfaced by the
// next synthesis call rather than silently falling back to a more capable
// contract.
func (e *Engine) WithInteractionContract(contract protocol.InteractionContract, descriptors ...*protocol.ContractDescriptor) *Engine {
	_ = e.SetInteractionContract(contract, descriptors...)
	return e
}

// WithContract is the descriptor-first fluent alias.
func (e *Engine) WithContract(descriptor protocol.ContractDescriptor) *Engine {
	_ = e.SetInteractionContract(descriptor.Contract, &descriptor)
	return e
}

// SetModelMetadata supplies provider-neutral model facts used to choose the
// compact/standard prompt profile and output budget.  Name heuristics remain a
// fallback when no metadata is available.
func (e *Engine) SetModelMetadata(metadata protocol.ModelMetadata) {
	if e == nil {
		return
	}
	e.contractMu.Lock()
	e.modelMetadata = metadata
	e.contractMu.Unlock()
}

// WithModelMetadata is the fluent form of SetModelMetadata.
func (e *Engine) WithModelMetadata(metadata protocol.ModelMetadata) *Engine {
	e.SetModelMetadata(metadata)
	return e
}

// LastContract returns a defensive copy of the descriptor used by the most
// recent synthesis attempt, or nil before the first contract-bound run.
func (e *Engine) LastContract() *protocol.ContractDescriptor {
	if e == nil {
		return nil
	}
	e.contractMu.RLock()
	defer e.contractMu.RUnlock()
	if e.lastContract == nil {
		return nil
	}
	copy := e.lastContract.Clone()
	return &copy
}

// SetUserName sets the engineer identity for system prompt injection.
func (e *Engine) SetUserName(name string) { e.UserName = name }

// SetRootPath sets the workspace root for file discovery.
func (e *Engine) SetRootPath(rootPath string) { e.rootPath = rootPath }

// SetArchetype records the archetype discovered by investigation. It is
// explicit metadata for callers that already have an investigation result;
// otherwise processFromLedger derives it from the workspace root.
func (e *Engine) SetArchetype(archetype recon.ProjectArchetype) {
	if e == nil {
		return
	}
	e.archetype = archetype
	e.archetypeExplicit = archetype != ""
	e.archetypeFromLedger = false
	e.vanillaWeb = archetype == recon.VANILLA_WEB
	e.frontendOnly = e.vanillaWeb
}

// WithArchetype is the fluent form of SetArchetype.
func (e *Engine) WithArchetype(archetype recon.ProjectArchetype) *Engine {
	e.SetArchetype(archetype)
	return e
}

// Archetype returns the currently resolved investigation archetype.
func (e *Engine) Archetype() recon.ProjectArchetype {
	if e == nil || e.archetype == "" {
		return recon.UNKNOWN_GENERIC
	}
	return e.archetype
}

// resolveArchetype derives the workspace context once at the start of every
// plan synthesis. Explicit investigation metadata wins; otherwise discovery is
// authoritative. This prevents fallback code from silently reverting to a Go
// assumption after a frontend investigation.
var investigationArchetypeMarker = regexp.MustCompile(`(?im)^[ \t]*ARCHETYPE:[ \t]*(VANILLA_WEB|REACT_NEXT|GO_BACKEND|UNKNOWN_GENERIC)[ \t]*$`)

func (e *Engine) adoptArchetypeFromLedger(ledgerContent string) {
	if e == nil || e.archetypeExplicit || ledgerContent == "" {
		return
	}
	match := investigationArchetypeMarker.FindStringSubmatch(ledgerContent)
	if len(match) != 2 {
		return
	}
	archetype := strings.ToUpper(strings.TrimSpace(match[1]))
	e.archetype = recon.ProjectArchetype(archetype)
	e.archetypeFromLedger = true
	e.vanillaWeb = e.archetype == recon.VANILLA_WEB
	e.frontendOnly = e.vanillaWeb
}

func (e *Engine) resolveArchetype() {
	if e == nil {
		return
	}
	if !e.archetypeExplicit && !e.archetypeFromLedger && e.rootPath != "" {
		if ac, err := recon.DetectArchetype(e.rootPath); err == nil && ac != nil {
			e.archetype = ac.Type
			e.frontendOnly = false
		}
	}
	if e.archetype == "" {
		e.archetype = recon.UNKNOWN_GENERIC
	}
	e.vanillaWeb = e.archetype == recon.VANILLA_WEB || e.frontendOnly
}

// SetAllowedFiles sets the grounded file tree for scope guard validation.
func (e *Engine) SetAllowedFiles(files []string) { e.AllowedFiles = files }

// WithSnapshotCache injects a workspace snapshot cache for archetype-aware
// diagnostic gating. May be nil.
func (e *Engine) WithSnapshotCache(sc *wssnapshot.SnapshotCache) *Engine {
	e.snapCache = sc
	return e
}

// WithCapabilityRegistry injects an archetype capability registry for
// archetype-aware diagnostic gating. May be nil.
func (e *Engine) WithCapabilityRegistry(cr *wscap.ArchetypeCapabilityRegistry) *Engine {
	e.capReg = cr
	return e
}

// WithEventBus injects the event bus this engine publishes domain events to.
// The engine stays headless: it never mutates UI state or writes to the
// terminal directly — consumers subscribe to the bus as projections. May be
// nil to disable emission.
func (e *Engine) WithEventBus(bus *events.Bus) *Engine {
	e.bus = bus
	return e
}

// WithPipelineFacade injects the Layer 0-5 pipeline Facade. When wired and no
// direct provider is set, the engine delegates its generative synthesis to the
// facade (see processFromLedger). May be nil to keep the legacy provider path.
func (e *Engine) WithPipelineFacade(f pipeline.Facade) *Engine {
	if e != nil {
		e.facade = f
	}
	return e
}

// Facade returns the injected Layer 0-5 pipeline Facade, if any.
func (e *Engine) Facade() pipeline.Facade {
	if e == nil {
		return nil
	}
	return e.facade
}

// emit publishes a domain event. It is a strict no-op when no bus is wired,
// so engines keep working unchanged in headless/CLI contexts.
func (e *Engine) emit(ev events.DomainEvent) {
	if e != nil && e.bus != nil {
		e.bus.Publish(ev)
	}
}

// synthesisBudget resolves the provider-independent output ceiling for one
// plan turn.  Explicit model metadata is authoritative; the shared
// llmstep/name policy remains the compatibility fallback.
func (e *Engine) synthesisBudget(modelName string, requested int) (maxTokens int, constrained bool) {
	maxTokens, constrained = resolveSynthesisMaxTokens(modelName, requested)
	if e == nil {
		return maxTokens, constrained
	}
	e.contractMu.RLock()
	metadata := e.modelMetadata
	e.contractMu.RUnlock()
	if metadata.ID != "" && !strings.EqualFold(strings.TrimSpace(metadata.ID), strings.TrimSpace(modelName)) {
		return maxTokens, constrained
	}
	if metadata.ID != "" {
		// Catalog metadata is authoritative for a named model, including a
		// deliberate unconstrained classification.  The shared resolver's
		// name-based clamp must not silently override that fact.
		constrained = metadata.Constrained
		if !metadata.Constrained && requested > 0 {
			maxTokens = requested
		}
	} else if metadata.Constrained {
		constrained = true
	}
	if metadata.MaxOutputTokens > 0 && (maxTokens <= 0 || metadata.MaxOutputTokens < maxTokens) {
		maxTokens = metadata.MaxOutputTokens
	}
	return maxTokens, constrained
}

func (e *Engine) rememberContract(descriptor protocol.ContractDescriptor) {
	if e == nil {
		return
	}
	e.contractMu.Lock()
	copy := descriptor.Clone()
	e.lastContract = &copy
	e.contractMu.Unlock()
}

// synthesisDescriptor is the single descriptor factory used by every plan
// provider path.  Prompt selection, schema selection, retry continuation and
// the final task guard all consume this value; no path reconstructs contract
// metadata ad hoc from a model-name substring.
func (e *Engine) synthesisDescriptor(modelName string, archetype protocol.Archetype, maxTokens, maxTasks int, fastTrack bool) (protocol.ContractDescriptor, error) {
	if e != nil && e.contractBindingErr != nil {
		return protocol.ContractDescriptor{}, e.contractBindingErr
	}
	var descriptor protocol.ContractDescriptor
	var err error
	if e != nil && e.interactionContract.Valid() {
		if e.interactionDescriptor == nil {
			descriptor = protocol.Describe(e.interactionContract)
		} else {
			descriptor = e.interactionDescriptor.Clone()
		}
		// The active archetype and budget are step metadata.  They may lower
		// or specialize the descriptor, but they never change its semantic
		// contract identity.
		if descriptor.Archetype == "" {
			descriptor.Archetype = archetype
		}
		// Runtime/model budgets may specialize a descriptor, but they must never
		// widen an explicitly declared ceiling.  A descriptor carrying 64 output
		// tokens remains 64 even when the shared resolver would normally choose
		// a larger provider budget.
		if maxTokens > 0 && (descriptor.MaxOutputTokens <= 0 || maxTokens < descriptor.MaxOutputTokens) {
			descriptor.MaxOutputTokens = maxTokens
		}
		if maxTasks > 0 && (descriptor.MaxTasks <= 0 || maxTasks < descriptor.MaxTasks) {
			descriptor.MaxTasks = maxTasks
		}
		descriptor, err = descriptor.Normalize()
	} else {
		var metadata protocol.ModelMetadata
		if e != nil {
			e.contractMu.RLock()
			metadata = e.modelMetadata
			e.contractMu.RUnlock()
		}
		// Metadata is a fact about one model, not a process-global override.  A
		// stale record for another model must not change this turn's prompt or
		// output budget; the name heuristic remains the compatibility fallback.
		if metadata.ID != "" && !strings.EqualFold(strings.TrimSpace(metadata.ID), strings.TrimSpace(modelName)) {
			metadata = protocol.ModelMetadata{}
		}
		descriptor, err = protocol.PlanDescriptorWithMetadata(modelName, metadata, archetype, maxTokens, maxTasks)
	}
	if err != nil {
		return protocol.ContractDescriptor{}, err
	}
	if fastTrack {
		descriptor.OutputSchema = protocol.SchemaTaskBlocks
		descriptor.SchemaVersion = "plan.task_blocks.v1"
		descriptor.Schema = "izen.plan.task_blocks.v1"
	} else if descriptor.OutputSchema == "" || descriptor.OutputSchema == protocol.SchemaText {
		descriptor.OutputSchema = protocol.SchemaPlanJSON
		descriptor.SchemaVersion = "plan.atomic_tasks.v1"
	}
	if descriptor.Schema == "" {
		if descriptor.OutputSchema == protocol.SchemaTaskBlocks {
			descriptor.Schema = "izen.plan.task_blocks.v1"
		} else {
			descriptor.Schema = "izen.plan.atomic_tasks.v1"
		}
	}
	if archetype == protocol.ArchetypeVanillaWeb {
		descriptor.Archetype = protocol.ArchetypeVanillaWeb
		if descriptor.PromptProfile == protocol.PromptProfileFull || descriptor.PromptProfile == "" {
			descriptor.PromptProfile = protocol.PromptProfileCompact
		}
	}
	normalized, err := descriptor.Normalize()
	if err != nil {
		return protocol.ContractDescriptor{}, err
	}
	e.rememberContract(normalized)
	return normalized, nil
}

// finalizeTasks applies the compile-error shell enforcement (go get / go mod
// tidy injection) unless the workspace is a frontend/vanilla archetype. A
// FRONTEND_UI intent or VANILLA_WEB archetype MUST NEVER receive injected Go
// dependency tasks — the enforcement heuristic is invalidated immediately when
// the domain isolation guard is active.
func (e *Engine) finalizeTasks(tasks []Task, problem, ledgerContent string) []Task {
	if e == nil {
		return tasks
	}
	e.resolveArchetype()
	// The archetype-aware variant is the only path allowed to synthesize a
	// dependency command. The legacy helper remains available to callers that
	// explicitly do not have investigation context. A capability registry may
	// classify a workspace as frontend-only even when disk detection retained a
	// backend label; use the stricter guard in that case.
	archetypeForTasks := e.archetype
	if e.vanillaWeb {
		archetypeForTasks = recon.VANILLA_WEB
	}
	tasks = ForceShellExecOnCompileErrorForArchetype(tasks, problem, ledgerContent, archetypeForTasks)
	return ValidateShellExecCommandsForArchetype(tasks, ledgerContent, archetypeForTasks)
}

// DiscoverAllowedFiles runs pkg/recon and pkg/grounding to discover the
// workspace file tree. Returns the allowed file list or an error.
// If AllowedFiles is already set, returns them immediately.
func (e *Engine) DiscoverAllowedFiles() ([]string, error) {
	if len(e.AllowedFiles) > 0 {
		return e.AllowedFiles, nil
	}
	if e.rootPath == "" {
		return nil, fmt.Errorf("plan engine: rootPath not set — call SetRootPath first")
	}
	archetype, err := recon.DetectArchetype(e.rootPath)
	if err != nil {
		return nil, fmt.Errorf("plan engine: recon failed: %w", err)
	}
	intent := &grounding.CanonicalIntent{
		RawPrompt:    "workspace discovery",
		CleanIntent:  "workspace discovery",
		TargetScopes: nil,
		Confidence:   1.0,
	}
	gc, err := grounding.SliceContext(archetype, intent, e.rootPath)
	if err != nil {
		return nil, fmt.Errorf("plan engine: grounding failed: %w", err)
	}
	e.AllowedFiles = gc.AllowedFileTree
	return e.AllowedFiles, nil
}

// GroundedConstraint returns the ALLOWED_FILE_TREE constraint block for prompt
// injection, or empty string if no allowed files are set.
func (e *Engine) GroundedConstraint() string {
	if len(e.AllowedFiles) == 0 {
		return ""
	}
	e.resolveArchetype()
	archetype := ""
	if e.vanillaWeb {
		archetype = string(recon.VANILLA_WEB)
	} else if e.archetype != recon.UNKNOWN_GENERIC {
		archetype = string(e.archetype)
	}
	return prompt.GroundedConstraint(archetype, e.AllowedFiles)
}

// parsePlanContent enforces strict JSON schema with recovery.
// Phase 3: If JSON parsing fails, it attempts auto-repair via autoCloseJSON
// and retries before giving up. Markdown-only output is rejected.
func parsePlanContent(content string) []Task {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}

	result := ParseJSONPlan(content)
	if result.Valid {
		if err := ValidateAllTasks(result.Tasks); err != nil {
			return nil
		}
		return result.Tasks
	}

	// Phase 3: Attempt auto-repair of truncated JSON before giving up.
	repaired := autoCloseJSON(content)
	if repaired != content {
		result = ParseJSONPlan(repaired)
		if result.Valid {
			if err := ValidateAllTasks(result.Tasks); err != nil {
				return nil
			}
			return result.Tasks
		}
	}

	return nil
}

// SetProvider configures the AI provider for this engine using the structured signature.
func (e *Engine) SetProvider(provider ProviderFunc) {
	if e != nil {
		e.provider = provider
	}
}

// SetStreamProvider configures the streaming AI provider for this engine. When
// wired, LLM synthesis runs over ExecuteStream; the accumulated buffer is
// retained for telemetry, while provider-authenticated truncation is surfaced
// before structural parsing. Optional; when nil the engine falls back to the
// non-streaming SetProvider path.
func (e *Engine) SetStreamProvider(sp StreamProviderFunc) {
	if e != nil {
		e.streamProv = sp
	}
}

// LastUsage returns the provider-reported token usage (input, output) of the
// most recent LLM synthesis, committed even when the response was truncated by
// the completion ceiling. The UI reads it after ProcessFromLedger returns to
// update the session counters and the status.Tracker.
func (e *Engine) LastUsage() (input, output int) {
	if e == nil {
		return 0, 0
	}
	e.usageMu.RLock()
	defer e.usageMu.RUnlock()
	return e.lastInput, e.lastOutput
}

// recordUsage commits provider-reported token usage. It is called on every
// synthesis attempt, truncated or not, so the token metrics are never lost to
// a finish_reason: "length" terminal event.
//
// Phase 6.4.5 Global UI Telemetry Binding: usage is ALSO published live to
// the event bus as a ProviderUsageUpdate so the global footer view model
// (↑X ↓Y) renders billed tokens in real time during plan.synthesize across
// ALL system states — not just at the terminal planResultMsg commit.
func (e *Engine) recordUsage(input, output int) {
	if e == nil {
		return
	}
	e.usageMu.Lock()
	e.lastInput = input
	e.lastOutput = output
	e.usageMu.Unlock()
}

// publishLiveUsage emits the provider-reported usage of one synthesis attempt
// to the event bus for real-time footer binding. No-op when no bus is wired
// or when both counts are zero (nothing billed, nothing to render).
func (e *Engine) publishLiveUsage(model string, input, output int) {
	if e == nil || e.bus == nil {
		return
	}
	if input <= 0 && output <= 0 {
		return
	}
	e.bus.Publish(events.NewProviderUsageUpdate("", model, input, output, 0))
}

// usageReader is implemented by stream results that report provider usage.
type usageReader interface {
	Usage() ai.ProviderUsage
}

func stampPlanResponse(resp *ai.Response, req ai.Request) {
	if resp == nil {
		return
	}
	if resp.Contract == nil && req.Contract != nil {
		resp.SetContractMetadata(resp.Provider, req.Model, req.InteractionContract, req.Contract)
	}
	if resp.FinishReason == "" {
		resp.FinishReason = resp.Usage.FinishReason
	}
	if protocol.IsOutputTruncatedReason(resp.FinishReason) {
		resp.FinishReason = "length"
	}
	if protocol.IsOutputTruncatedReason(resp.Usage.FinishReason) {
		resp.Usage.FinishReason = "length"
	}
}

// complete performs a single LLM synthesis call. When a streaming provider is
// wired it runs over ExecuteStream and accumulates the buffer rune-safe,
// stripping reasoning sentinels. A provider-authenticated length finish is
// returned as a typed truncation error before any structural parser sees the
// accumulated bytes; the buffer is retained only for telemetry and bounded
// recovery decisions. When the stream produced only reasoning/thinking text
// (content empty), the reasoning is used as the payload via the reasoning
// fallback. The provider-reported usage is committed to the engine regardless
// of the terminal finish_reason.
//
// Reasoning forwarding: when an event bus is wired, every reasoning/thinking
// chunk (reasoning_content deltas routed through the request's ReasoningHandler
// and <thought>/sentinel markers parsed out of the raw stream by
// accumulateStream) is published to the bus as an EventReasoningStream as it
// arrives, so the UI can render live thinking during plan synthesis. Reasoning
// tokens are never dropped even when the request times out or yields empty
// final content — the already-accumulated chunks are published before any
// error/truncation path returns. A terminal IsComplete event closes the block
// so the UI collapses it to a summary line.
func (e *Engine) complete(ctx context.Context, req ai.Request) (*ai.Response, error) {
	// Provider callbacks are transport adapters, not trusted descriptor owners.
	// Give them a private copy so a callback cannot mutate the active ceiling or
	// the ledger-bound descriptor through the request pointer.
	if req.Contract != nil {
		copy := req.Contract.Clone()
		req.Contract = &copy
	}
	// The composed provider facade uses this semantic label to apply the Plan
	// phase budget. Standalone provider callbacks remain source-compatible.
	req.ContextPhase = "plan"
	// Carry catalog facts across the provider boundary when they identify this
	// exact model. The context compiler can then lower the Plan phase budget
	// for the real context/output window instead of relying only on a name
	// heuristic. A stale record is ignored; synthesisBudget applies the same
	// identity rule above.
	if e != nil {
		e.contractMu.RLock()
		metadata := e.modelMetadata
		e.contractMu.RUnlock()
		if metadata.ID == "" || strings.EqualFold(strings.TrimSpace(metadata.ID), strings.TrimSpace(req.Model)) {
			copy := metadata
			req.ModelMetadata = &copy
		}
	}
	// Strict per-attempt deadline for free/cloud models: a hung provider must be
	// cut off fast so the caller can fail over to heuristic plan synthesis
	// instead of blocking the TUI for minutes. Each synthesis attempt gets a
	// fresh budget derived from the parent context; local/paid models keep the
	// parent budget because their legitimate latency exceeds a strict deadline.
	attemptCtx := ctx
	if useStrictAttemptTimeout(req.Model) {
		var cancel context.CancelFunc
		attemptCtx, cancel = context.WithTimeout(ctx, planAttemptTimeout)
		defer cancel()
	}

	if e.streamProv == nil {
		resp, err := e.provider(attemptCtx, req)
		if resp != nil {
			stampPlanResponse(resp, req)
		}
		if err != nil {
			if attemptCtx.Err() != nil {
				// Return whatever the provider produced alongside the timeout
				// sentinel so the heuristic fallback can mine partial output.
				return resp, fmt.Errorf("%w: provider exceeded the %.0fs per-attempt budget", ErrPlanAttemptTimeout, planAttemptTimeout.Seconds())
			}
			return nil, err
		}
		if resp != nil {
			e.recordUsage(resp.TokenInput, resp.TokenOutput)
			e.publishLiveUsage(req.Model, resp.TokenInput, resp.TokenOutput)
			if resp.FinishReason == "" && resp.Usage.FinishReason != "" {
				resp.FinishReason = resp.Usage.FinishReason
			}
			// Usage metadata is authoritative when the provider marked the
			// usage record as known. Normalize that provenance onto Response
			// before returning the typed error so strict structural gates can
			// distinguish it from a legacy test double that only set a string
			// FinishReason.
			if resp.Usage.Known && isTruncatedFinish(resp.Usage.FinishReason) {
				resp.Truncated = true
			}
			if resp.Truncated {
				// Keep the response attached for usage/diagnostics, but make
				// the typed error impossible for structural parsers to ignore.
				return resp, ai.NewOutputTruncated(req.Model, resp.FinishReason)
			}
		}
		return resp, nil
	}

	// Reasoning sink: accumulates every reasoning chunk for the Phase 6.4.2
	// Reasoning Content Fallback Invariant AND publishes each chunk to the
	// event bus exactly as it streams in. reasoningPublished tracks whether
	// any chunk was forwarded so the terminal IsComplete event is only
	// emitted when there is an active thinking block to collapse.
	// Providers that route reasoning via the request-level handler
	// (OpenAI/Claude/Gemini/Ollama/Groq/...) are captured here;
	// providers that embed sentinel markers in the raw stream (OpenRouter)
	// are captured by the accumulateStream sink through the splitter —
	// never double-counted, because those readers do not consult
	// ReasoningHandler. The sink is ALWAYS wired (even with a nil bus) so
	// handler-routed thinking text survives for the fallback below.
	var handlerReasoning strings.Builder
	reasoningPublished := false
	reasoningSink := func(chunk string) {
		if chunk == "" {
			return
		}
		handlerReasoning.WriteString(chunk)
		if e.bus != nil {
			reasoningPublished = true
			e.bus.Publish(events.NewReasoningStream(chunk, false))
		}
	}
	req.ReasoningHandler = func(chunk string) error {
		reasoningSink(chunk)
		return nil
	}

	req.Stream = true
	rawStream, err := e.streamProv(attemptCtx, req)
	if err != nil {
		if attemptCtx.Err() != nil {
			return nil, fmt.Errorf("%w: provider exceeded the %.0fs per-attempt budget", ErrPlanAttemptTimeout, planAttemptTimeout.Seconds())
		}
		return nil, err
	}
	defer func() { _ = rawStream.Close() }()

	content, reasoning, finishReason, input, output := accumulateStream(rawStream, reasoningSink)
	truncated := false
	if provider, ok := rawStream.(ai.TruncationProvider); ok {
		truncated = provider.TruncationError() != nil
	}
	e.recordUsage(input, output)
	e.publishLiveUsage(req.Model, input, output)

	// Reasoning Content Fallback Invariant (Phase 6.4.2): handler-routed
	// thinking text (OpenAI/Ollama-compatible readers) never reaches the
	// splitter's reasoning buffer — merge it here so a reasoning-only
	// stream still synthesizes a plan instead of raising an empty
	// response error. Sentinel-classified reasoning (OpenRouter) wins
	// when both paths carried text.
	if strings.TrimSpace(reasoning) == "" {
		reasoning = handlerReasoning.String()
	}
	if strings.TrimSpace(content) == "" && strings.TrimSpace(reasoning) != "" {
		// Reasoning fallback: the model emitted only thinking content (a
		// Mini/reasoning model with empty message content, e.g. Nemotron
		// reasoning variants). Promote the reasoning text to the payload
		// so plan synthesis succeeds instead of failing with
		// "empty response from provider".
		content = reasoning
	}

	// Terminal reasoning event: collapses the live thinking block in the UI.
	// Emitted whenever reasoning was forwarded, even if the request timed out
	// or yielded empty final content — the UI must never be left with an
	// orphaned open thinking box.
	if reasoningPublished {
		e.bus.Publish(events.NewReasoningStream("", true))
	}

	// Per-attempt deadline fired mid-stream (or between stream open and first
	// bytes). Return whatever partial content accumulated — flagged with the
	// timeout sentinel — so the caller can fail fast and mine it. A natural
	// "stop" completion is always treated as success even if the deadline
	// expired a microsecond after the last byte.
	if attemptCtx.Err() != nil && finishReason != "stop" && !truncated {
		response := &ai.Response{
			Content:      content,
			TokenInput:   input,
			TokenOutput:  output,
			FinishReason: finishReason,
		}
		stampPlanResponse(response, req)
		return response, fmt.Errorf("%w: provider exceeded the %.0fs per-attempt budget", ErrPlanAttemptTimeout, planAttemptTimeout.Seconds())
	}

	// Truncation-aware response: the accumulated buffer is retained for
	// telemetry, but a length finish is surfaced as a typed error before any
	// structural fallback parser can consume a partial artifact.
	response := &ai.Response{
		Content:      content,
		TokenInput:   input,
		TokenOutput:  output,
		FinishReason: finishReason,
		Truncated:    truncated,
	}
	stampPlanResponse(response, req)
	if truncated {
		return response, ai.NewOutputTruncated(req.Model, finishReason)
	}
	return response, nil
}

// isTruncatedFinish normalizes the provider-native output-ceiling labels at
// the semantic boundary. Structural parsers must never infer completion from
// a partial buffer when this returns true.
func isTruncatedFinish(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "length", "max_tokens", "max tokens", "max_output_tokens", "max output tokens", "max_output_token", "max-output-tokens", "max-output-token", "max output token", "truncated", "token_limit", "output_limit", "max_output":
		return true
	default:
		return false
	}
}

// completeJSONArtifact performs a non-repairing JSON syntax check. It is used
// only as a compatibility bridge for older providers that attach
// finish_reason=length to an otherwise complete object; auto-closing and
// markdown salvage are intentionally excluded so genuinely truncated bytes
// cannot enter a structural fallback parser.
func completeJSONArtifact(content string) bool {
	content = strings.TrimSpace(content)
	if content == "" {
		return false
	}
	// A fenced complete response is still complete; remove only the fence
	// wrapper, never repair or truncate its payload.
	if strings.HasPrefix(content, "```") {
		if first := strings.IndexByte(content, '\n'); first >= 0 {
			last := strings.LastIndex(strings.TrimSpace(content), "```")
			if last > first {
				content = strings.TrimSpace(content[first+1 : last])
			}
		}
	}
	var value any
	if err := json.Unmarshal([]byte(content), &value); err != nil {
		return false
	}
	switch value.(type) {
	case map[string]any, []any:
		return true
	default:
		return false
	}
}

// accumulateStream drains an SSE-backed stream to EOF, keeping EVERY byte that
// arrived. It is transport-level and truncation-agnostic: a stream that ends
// with finish_reason "length" has its partial buffer retained for telemetry,
// while the caller decides whether the provider-authenticated result may cross
// a structural boundary. Reasoning sentinels are stripped via the Splitter so
// thinking text never pollutes the parseable JSON; the extracted reasoning is
// returned alongside so a reasoning-only stream can fall back to it when the
// content buffer is empty.
//
// The stream is classified incrementally (not buffered-then-split), so every
// reasoning/thinking chunk is routed to the optional sink funcs as it arrives.
// This lets callers publish reasoning to the event bus continuously — reasoning
// tokens are never dropped even when the request times out or yields empty
// final content, because chunks already read are forwarded before any error
// path returns. Each sink is invoked with verbatim reasoning text; a nil sink
// is ignored.
func accumulateStream(r io.Reader, sinks ...func(string)) (content, reasoning, finishReason string, input, output int) {
	var contentParts, reasoningParts strings.Builder
	runeBuf := stream.NewRuneBuffer()
	splitter := stream.NewSplitter()

	publishReasoning := func(text string) {
		if text == "" {
			return
		}
		for _, s := range sinks {
			if s != nil {
				s(text)
			}
		}
	}

	emitFrame := func(fr stream.Frame) {
		switch fr.Kind {
		case stream.ChunkContent:
			if fr.Text != "" {
				contentParts.WriteString(fr.Text)
			}
		case stream.ChunkReasoning:
			if fr.Text != "" {
				reasoningParts.WriteString(fr.Text)
				publishReasoning(fr.Text)
			}
		}
	}

	flushBuffered := func() {
		if rem := runeBuf.Flush(); rem != "" {
			splitter.Write(rem, emitFrame)
		}
		splitter.Flush(emitFrame)
	}

	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if text := runeBuf.Write(buf[:n]); text != "" {
				splitter.Write(text, emitFrame)
			}
		}
		if err == io.EOF {
			flushBuffered()
			break
		}
		if err != nil {
			// Non-EOF error: keep whatever accumulated so far; the caller
			// decides whether the partial content is usable.
			flushBuffered()
			break
		}
	}
	if up, ok := r.(usageReader); ok {
		usage := up.Usage()
		input = usage.PromptTokens
		output = usage.CompletionTokens
	}
	if frp, ok := r.(ai.FinishReasonProvider); ok {
		finishReason = frp.FinishReason()
	}
	return contentParts.String(), reasoningParts.String(), finishReason, input, output
}

// ProcessFromLedger generates an execution plan directly from investigation
// ledger data using enforced structured output (JSON mode). Returns parsed
// Task structs, bypassing the conversational text-streaming path entirely.
//
// When fastTrack is true (used for local 7B models on a 0-TODO + compile/dep
// blocker), the heavy JSON-schema instruction and full forensic ledger prompt
// are replaced with a minimal shell-resolution prompt so the model can produce
// its first token within a tight local budget instead of choking on context.
func (e *Engine) ProcessFromLedger(ctx context.Context, ledgerContent string, problem string, modelName string) ([]Task, error) {
	return e.processFromLedger(ctx, ledgerContent, problem, modelName, false)
}

// ProcessFromLedgerFastTrack is the lightweight variant used for local SLMs that
// hit a 0-TODO + dependency/compilation blocker. It skips the JSON-schema system
// prompt and the full forensic ledger prompt in favour of a minimal resolution
// prompt, keeping the prompt tiny enough for a 7B model to answer quickly.
func (e *Engine) ProcessFromLedgerFastTrack(ctx context.Context, promptText string, modelName string) ([]Task, error) {
	return e.processFromLedger(ctx, "", "", modelName, true, promptText)
}

// SynthesisRequest is the explicit contract-bound entry point for callers
// that own the plan boundary themselves.  ProcessFromLedger remains the
// compatibility façade and derives the same descriptor when this request
// leaves Contract zero-valued.
type SynthesisRequest struct {
	LedgerContent string
	Problem       string
	ModelName     string
	FastTrack     bool
	FastPrompt    string

	InteractionContract protocol.InteractionContract
	// Contract is the normalized descriptor to bind for this turn.  The
	// pointer form is accepted for callers that already carry descriptor
	// metadata; Contract is used when Descriptor is nil.
	Contract   protocol.ContractDescriptor
	Descriptor *protocol.ContractDescriptor
	Archetype  recon.ProjectArchetype
}

// SynthesisInput is a compatibility alias for the explicit synthesis request.
type SynthesisInput = SynthesisRequest

// SynthesisResult keeps the task list and the exact descriptor that governed
// it together, so a concurrent caller does not have to infer the contract from
// the engine's most-recent mutable telemetry field.
type SynthesisResult struct {
	Tasks    []Task
	Contract *protocol.ContractDescriptor
}

// SynthesizeWithContract is the result-oriented form of Synthesize.
func (e *Engine) SynthesizeWithContract(ctx context.Context, request SynthesisRequest) (SynthesisResult, error) {
	tasks, err := e.Synthesize(ctx, request)
	if err != nil {
		return SynthesisResult{}, err
	}
	return SynthesisResult{Tasks: tasks, Contract: e.LastContract()}, nil
}

// Synthesize runs one contract-bound plan synthesis.  It is intentionally a
// thin boundary over the canonical ledger path: deterministic archetype
// fallbacks, truncation handling, retries, and the final task/ledger guard all
// remain centralized in processFromLedger.
func (e *Engine) Synthesize(ctx context.Context, request SynthesisRequest) ([]Task, error) {
	if e == nil {
		return nil, fmt.Errorf("plan engine: nil engine")
	}
	// Save and restore the optional binding so an embedding can use Synthesize
	// for more than one model without leaking descriptor state into the next
	// call.  Engine synthesis is otherwise intentionally single-lane.
	oldContract, oldDescriptor := e.interactionContract, e.interactionDescriptor
	oldBindingErr := e.contractBindingErr
	oldArchetype, oldExplicit, oldFromLedger, oldFrontend := e.archetype, e.archetypeExplicit, e.archetypeFromLedger, e.frontendOnly
	oldVanilla := e.vanillaWeb
	defer func() {
		e.interactionContract, e.interactionDescriptor, e.contractBindingErr = oldContract, oldDescriptor, oldBindingErr
		e.archetype, e.archetypeExplicit, e.archetypeFromLedger, e.frontendOnly, e.vanillaWeb = oldArchetype, oldExplicit, oldFromLedger, oldFrontend, oldVanilla
	}()

	if request.Archetype != "" {
		e.SetArchetype(request.Archetype)
	}
	contract := request.InteractionContract
	descriptor := request.Contract
	if request.Descriptor != nil {
		descriptor = request.Descriptor.Clone()
	}
	if contract.Valid() || descriptor.Contract.Valid() || descriptor.Kind.Valid() {
		if !descriptor.Contract.Valid() && !descriptor.Kind.Valid() {
			descriptor = protocol.Describe(contract)
		}
		if err := e.SetInteractionContract(contract, &descriptor); err != nil {
			return nil, err
		}
	}
	if request.FastTrack {
		return e.ProcessFromLedgerFastTrack(ctx, request.FastPrompt, request.ModelName)
	}
	return e.ProcessFromLedger(ctx, request.LedgerContent, request.Problem, request.ModelName)
}

func (e *Engine) processFromLedger(ctx context.Context, ledgerContent string, problem string, modelName string, fastTrack bool, fastPrompt ...string) (tasks []Task, err error) {
	if e == nil {
		return nil, fmt.Errorf("plan engine: nil engine")
	}
	// activeDescriptor is established after archetype resolution but before any
	// deterministic task can be returned.  The defer below is the final commit
	// gate: no response or fallback can publish PlanStaged without passing the
	// same contract/task validation.
	var activeDescriptor protocol.ContractDescriptor

	// ── Phase 6.4.5 Ledger Context Isolation ──────────────────────────
	// Strip synthetic 'package root (:0)' placeholders from the forensic
	// ledger before any signal classification or prompt injection. When no
	// active build errors are present, stale empty-target coordinates from a
	// prior /investigate run must never reach plan synthesis prompts.
	ledgerContent = StripSyntheticPackageRootPlaceholders(ledgerContent)

	// ── HEADLESS EVENT EMISSION ───────────────────────────────
	// The plan engine is headless: every observable outcome is published to
	// the event bus. The deferred block guarantees a terminal event (success
	// or failure) fires for every early-return path below.
	raw := problem
	if raw == "" && len(fastPrompt) > 0 {
		raw = fastPrompt[0]
	}
	e.emit(events.NewCommandReceived(raw, "plan"))
	defer func() {
		if err == nil && len(tasks) > 0 && activeDescriptor.Contract.Valid() {
			if validationErr := ValidateTasksForContract(tasks, activeDescriptor, fastTrack); validationErr != nil {
				tasks = nil
				err = validationErr
			}
		}
		if err != nil {
			e.emit(events.NewExecutionFailed(events.FailurePermanent, err, "plan"))
			return
		}
		if len(tasks) > 0 {
			targets := make([]string, 0, len(tasks))
			for _, t := range tasks {
				targets = append(targets, t.Target)
			}
			descriptor := activeDescriptor.Clone()
			e.emit(events.NewPlanStaged(len(tasks), targets, "plan", &descriptor))
			e.emit(events.NewStageCompleted("plan", 0, fmt.Sprintf("staged %d tasks", len(tasks))))
		}
	}()

	// Resolve the investigation/workspace archetype before ANY fast-track or
	// fallback branch. A stale boolean is not sufficient: fallback generation
	// must be able to distinguish VANILLA_WEB from GO_BACKEND and suppress
	// language-specific commands accordingly. A marker adopted from the
	// investigation ledger is authoritative for this run, even if a later
	// context helper re-resolves the engine.
	if !e.archetypeExplicit {
		e.archetypeFromLedger = false
		e.archetype = recon.UNKNOWN_GENERIC
		e.frontendOnly = false
		e.vanillaWeb = false
	}
	e.resolveArchetype()
	e.adoptArchetypeFromLedger(ledgerContent)
	if !e.archetypeExplicit && !e.archetypeFromLedger && e.capReg != nil && e.snapCache != nil && e.rootPath != "" {
		//nolint:contextcheck
		if snap, snapErr := e.snapCache.GetSnapshot(e.rootPath); snapErr == nil {
			if !e.capReg.ArchetypeHasGoTools(snap.Archetype) {
				e.frontendOnly = true
				if e.archetype == recon.UNKNOWN_GENERIC {
					e.archetype = recon.VANILLA_WEB
				}
			}
		}
	}
	e.vanillaWeb = e.archetype == recon.VANILLA_WEB || e.frontendOnly
	// Bind the semantic descriptor before any deterministic fast-track.  The
	// provisional budget is replaced with the provider-aware budget below once
	// the generative path is reached; the identity/archetype/output schema are
	// already fixed here for the final commit guard.
	archetypeForContract := protocol.Archetype(e.archetype)
	if e.vanillaWeb {
		archetypeForContract = protocol.ArchetypeVanillaWeb
	}
	activeDescriptor, err = e.synthesisDescriptor(modelName, archetypeForContract, 0, 0, fastTrack)
	if err != nil {
		return nil, err
	}

	// ── CANONICAL SIGNAL CLASSIFICATION ────────────────────────────────
	// Classify the ledger once into canonical signal.Signal values. All routing
	// below (canonical import mismatch, undefined symbol, remote dependency
	// blocker, compile/dependency fallbacks) evaluates typed SignalKind values
	// instead of re-scanning raw terminal text — the signal classifier is the
	// single place where free-text matching for routing happens.
	signals := signal.Detect(ledgerContent, "plan.ledger")

	// ── DIRECT MUTATION FAST-TRACK ──────────────────────────
	// When the prompt is a simple file replacement (refactor LICENSE
	// from MIT to APACHE, change X to Y in @file, etc.), bypass
	// /investigate mode entirely. Do NOT run test suites (go test).
	// Route directly to BUILD / MUTATION pipeline with a
	// deterministic, hardcoded task — zero LLM synthesis needed.
	//
	// This implements the "Direct Mutation Fast-Track Rule":
	//   1. Detect direct mutation intent in the prompt/problem text.
	//   2. Route directly to a deterministic FILE_MUTATE task.
	//   3. Skip investigation, test execution, and JSON synthesis.
	if !fastTrack {
		if target := detectDirectMutation(problem, ledgerContent); target != nil {
			candidate := []Task{*target}
			if e.archetype != recon.UNKNOWN_GENERIC {
				candidate = FilterTasksForArchetype(candidate, e.archetype)
			}
			if len(candidate) > 0 {
				e.emit(events.NewIntentParsed("direct_mutation", problem, 1.0))
				return candidate, nil
			}
			// The direct target is outside the discovered archetype. Continue
			// through the grounded synthesis path instead of returning a task
			// that the execution boundary would later have to reject.
		}
	}

	// ── CANONICAL IMPORT MISMATCH (lx coordinate handshake) ──────────────
	// When the ledger contains a canonical import path mismatch error
	// ("module declares its path as: X but was required as: Y"), use the lx
	// daemon to resolve the exact file:line coordinates where the old path
	// appears. Then generate deterministic FILE_EDIT tasks at those coordinates
	// followed by SHELL_EXEC go mod tidy — replacing the SHELL_EXEC-only
	// short-circuit that previously bypassed precision file editing.
	//
	// VANILLA_WEB GUARD: This block is SKIPPED for HTML/CSS/JS workspaces that
	// have no Go files — the canonical import path signal is a false positive
	// from non-Go tooling that happens to emit similar-looking error text.
	//
	// This implements the "Lynx Coordinate Handshake" architectural spec:
	//   Step 1: Parse diagnostic output for canonical mismatch.
	//   Step 2: Leverage lx related/resolve for precision discovery (no full
	//           file loading into LLM context).
	//   Step 3: Minimal context ledger population (under 100 tokens).
	//   Step 4: Atomic execution blueprint (FILE_EDIT + SHELL_EXEC).
	if !fastTrack && e.allowsGoFallback() && signal.HasKind(signals, signal.SignalImportMismatch) {
		mismatch := retrieval.ParseCanonicalMismatch(ledgerContent)
		if mismatch != nil && mismatch.OldPath != "" && mismatch.NewPath != "" {
			router := retrieval.GetGlobalRouter()
			if router != nil {
				resolver := retrieval.NewSearchEngineResolver(router.Engine())
				//nolint:contextcheck // search engine API predates context propagation
				refs, err := resolver.ResolveCanonicalMismatch(mismatch)
				if err == nil && len(refs) > 0 {
					tasks := make([]Task, 0, len(refs)+2)
					for i, ref := range refs {
						desc := fmt.Sprintf("Replace import path %q with %q at %s:%d",
							mismatch.OldPath, mismatch.NewPath, ref.File, ref.StartLine)
						tasks = append(tasks, Task{
							StepNum: i + 1,

							Status:      "idle",
							Type:        "FILE_MUTATE",
							Target:      ref.File,
							Description: desc,
							Rationale:   fmt.Sprintf("Canonical import mismatch resolved by search at %s:%d-%d", ref.File, ref.StartLine, ref.EndLine),
							Solution:    fmt.Sprintf("Replaced %q with %q in %s", mismatch.OldPath, mismatch.NewPath, ref.File),
							IsHardcoded: true,
						})
					}
					tidyStep := len(refs) + 1
					tasks = append(tasks, Task{
						StepNum: tidyStep,

						Status:      "idle",
						Type:        "SHELL_EXEC",
						Target:      "go mod tidy",
						Description: "Re-synchronize the dependency manifest after canonical import fix.",
						Rationale:   "Clean up stale go.mod/go.sum entries after import path correction.",
						Solution:    "Dependency manifest re-synchronized.",
						IsHardcoded: true,
					})
					return tasks, nil
				}
			}
		}
	}

	// ── UNDEFINED SYMBOL (instant fast-path, zero LLM/lx) ─────────
	//
	// Phase 1 — Standard Library Case-Sensitivity Check
	// If the symbol is a capitalized stdlib package name (e.g., "Log" → "log"),
	// generate a deterministic FILE_EDIT with STDLIB solution format.
	//
	// Phase 2 — Deterministic fallback (zero external calls)
	// For non-stdlib or unresolvable symbol errors, construct a FILE_MUTATE
	// task directly from the error coordinates. No lx daemon, no LLM — the
	// error file/line/symbol already carries the exact fix location.
	//
	// VANILLA_WEB GUARD: This block is SKIPPED for HTML/CSS/JS workspaces —
	// "undefined" errors in JavaScript are normal runtime semantics, not
	// Go-style compilation errors, and stdlib case-correction is Go-specific.
	//
	// CRITICAL: Both paths complete in < 1ms. The LLM synthesis retry loop
	// and lx daemon handshake are NEVER reached for undefined symbol errors.
	if !fastTrack && e.allowsGoFallback() && signal.HasKind(signals, signal.SignalSymbolUndefined) {
		undef := retrieval.ParseUndefinedSymbol(ledgerContent)
		if undef != nil && undef.Symbol != "" {
			sanitizedTarget, _ := retrieval.SanitizeTargetPath(undef.File)
			if sanitizedTarget == "" {
				sanitizedTarget = undef.File
			}

			// Phase 1: Standard library case-sensitivity correction.
			if pkgName, importPath, matched := retrieval.CheckStdlibCaseCorrection(undef.Symbol); matched {
				return []Task{
					{
						StepNum: 1,

						Status:      "idle",
						Type:        "FILE_MUTATE",
						Target:      sanitizedTarget,
						Description: fmt.Sprintf("Fix %q at %s:%d: replace %q with %q and add import %q", undef.Symbol, sanitizedTarget, undef.Line, undef.Symbol, pkgName, importPath),
						Rationale:   fmt.Sprintf("Undefined symbol %q is a capitalized stdlib package name — correct to %q.", undef.Symbol, pkgName),
						Solution:    fmt.Sprintf("STDLIB:%s:%s:%s", undef.Symbol, pkgName, importPath),
						IsHardcoded: true,
					},
				}, nil
			}

			// Phase 2: Deterministic fallback — no lx, no LLM.
			return []Task{
				{
					StepNum: 1,

					Status:      "idle",
					Type:        "FILE_MUTATE",
					Target:      sanitizedTarget,
					Description: fmt.Sprintf("Fix undefined symbol %q at %s:%d", undef.Symbol, sanitizedTarget, undef.Line),
					Rationale:   fmt.Sprintf("Undefined symbol %q at %s:%d — requires import or definition", undef.Symbol, sanitizedTarget, undef.Line),
					Solution:    fmt.Sprintf("Fix undefined symbol %q in %s", undef.Symbol, sanitizedTarget),
					IsHardcoded: true,
				},
			}, nil
		}
	}

	// REMOTE DEPENDENCY BLOCKER short-circuit: if the ledger carries a
	// SignalDepMissing whose payload marks the investigate "lx bypassed" blocker
	// token, bypass LLM synthesis entirely and generate deterministic
	// go get / go mod tidy tasks. This guarantees 100% success for missing
	// package resolution, eliminating the 3-attempt JSON synthesis crash loop.
	//
	// The signal classifier extracts the blocker marker AND the dependency
	// package from the ledger; the conclusion path remains the primary
	// dependency source (it carries the corrected path from forensic analysis).
	// ── FRONTEND DOMAIN ISOLATION (Module D) ───────────────────────────
	// A FRONTEND_UI / VANILLA_WEB workspace MUST NEVER stage Go dependency
	// tasks. The REMOTE DEPENDENCY BLOCKER short-circuit below is a Go
	// dependency heuristic: it is invalidated immediately for frontend
	// workspaces so a pure HTML/CSS/JS project can never receive go get /
	// go mod tidy tasks.
	if !fastTrack && e.allowsGoFallback() {
		blocker := signal.First(signals, signal.SignalDepMissing)
		if blocker != nil && blocker.PayloadValue("blocker") == "true" {
			conclusion := ExtractConclusionFromLedger(ledgerContent)
			dep := dependencyFromConclusion(conclusion)
			if dep == "" {
				dep = blocker.PayloadValue("dependency")
			}
			if dep != "" && !isPlaceholderToken(dep) {
				taskGet := Task{
					StepNum: 1,

					Status:      "idle",
					Type:        "SHELL_EXEC",
					Target:      fmt.Sprintf("go get %s", dep),
					Description: fmt.Sprintf("Install missing dependency %s to resolve compiler/import blocker.", dep),
					Rationale:   fmt.Sprintf("Inject the explicit third-party module %s missing from the execution boundary.", dep),
					Solution:    fmt.Sprintf("Missing package %s successfully resolves and dependency block clears.", dep),
					IsHardcoded: true,
				}
				taskTidy := Task{
					StepNum: 2,

					Status:      "idle",
					Type:        "SHELL_EXEC",
					Target:      "go mod tidy",
					Description: "Re-synchronize the dependency manifest with active imports after blocker identification.",
					Rationale:   "Re-synchronize the dependency manifest with active imports after blocker identification.",
					Solution:    "Clean up stale pointers and establish structural registry alignment.",
					IsHardcoded: true,
				}
				return []Task{taskGet, taskTidy}, nil
			}
			return []Task{
				{
					StepNum: 1,

					Status:      "idle",
					Type:        "SHELL_EXEC",
					Target:      "go mod tidy",
					Description: "Re-synchronize the dependency manifest with active imports after blocker identification.",
					Rationale:   "Re-synchronize the dependency manifest with active imports after blocker identification.",
					Solution:    "Clean up stale pointers and establish structural registry alignment.",
					IsHardcoded: true,
				},
			}, nil
		}
	}

	// ── GENERATIVE SYNTHESIS SOURCE ────────────────────────────────────
	// The Mode engine is the Security & Boundary gate. The generative core
	// work is owned either by the legacy direct provider or — when a
	// pipeline.Facade is injected and no direct provider is wired — by the
	// Layer 0-5 pipeline facade. The deterministic fast-tracks above run in
	// both cases and never require a provider.
	if e.provider == nil && e.streamProv == nil {
		if e.facade == nil {
			return nil, fmt.Errorf("plan engine: provider not set")
		}
		return e.synthesizeViaFacade(ctx, problem, ledgerContent)
	}

	e.emit(events.NewIntentParsed("plan.synthesize", problem, 0.8))

	// ── CAPABILITY-AWARE STEP BUDGET ──────────────────────────────────────
	// Plan synthesis requests a conservative output budget, but the provider's
	// ACTUAL ceiling (free-tier/constrained models clamp to ~980) is the binding
	// constraint. The request max_tokens is clamped to that ceiling up front so
	// the synthesis never asks for a budget the provider must silently cut —
	// that mismatch is exactly what produces finish_reason="length" plus blind
	// same-scope retries. Constrained models also receive a bounded-step
	// instruction (fewer atomic tasks per response) so a full batch fits.
	maxTokens, constrained := e.synthesisBudget(modelName, planSynthesisRequestedMaxTokens)
	stepState := newSynthesisStepState(modelName, constrained, maxTokens)
	archetype := protocol.Archetype(e.archetype)
	if e.vanillaWeb {
		archetype = protocol.ArchetypeVanillaWeb
	}
	descriptor, descriptorErr := e.synthesisDescriptor(modelName, archetype, maxTokens, stepState.taskBudget, fastTrack)
	if descriptorErr != nil {
		return nil, descriptorErr
	}
	// The descriptor is the final semantic ceiling for this turn.  Apply it to
	// the actual provider request and to the shared continuation telemetry state
	// as well as carrying it on the request metadata.
	if descriptor.MaxOutputTokens > 0 && (maxTokens <= 0 || maxTokens > descriptor.MaxOutputTokens) {
		maxTokens = descriptor.MaxOutputTokens
		stepState = newSynthesisStepState(modelName, constrained, maxTokens)
	}
	if descriptor.MaxTasks > 0 && stepState.taskBudget > descriptor.MaxTasks {
		stepState.taskBudget = descriptor.MaxTasks
	}
	activeDescriptor = descriptor
	if constrained {
		// The capability resolver is authoritative over a name heuristic: a
		// model with a low provider ceiling gets the same compact contract.
		descriptor.ConstrainedModel = true
		descriptor.PromptProfile = protocol.PromptProfileCompact
		normalizedDescriptor, normalizeErr := descriptor.Normalize()
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		descriptor = normalizedDescriptor
		activeDescriptor = descriptor
		e.rememberContract(descriptor)
	}
	compactContract := descriptor.IsCompact()

	var req ai.Request
	if fastTrack && len(fastPrompt) > 0 {
		fastSystem := prompt.CompactPlanContract()
		if compactContract {
			// Keep the fast-track contract compact as well; the descriptor
			// already carries the output ceiling and archetype context.
			fastSystem = descriptor.FastTrackInstructions()
		}
		fastUser := fastPrompt[0]
		if e.vanillaWeb {
			// FastTrackPrompt historically carried Go-only dependency
			// examples. Replace that incompatible context at the semantic
			// boundary rather than asking the model to ignore it.
			fastUser = "Review the frontend evidence and return only FILE_MUTATE task blocks for existing .html, .css, or .js files."
		}
		req = ai.Request{
			Model: modelName,
			Messages: []ai.Message{
				{
					Role:    "system",
					Content: fastSystem,
				},
				{
					Role:    "user",
					Content: fastUser,
				},
			},
			Stream:              false,
			MaxTokens:           maxTokens,
			InteractionContract: descriptor.Contract,
			Contract:            &descriptor,
		}
	} else {
		// Small/free models receive one compact, positive contract from the
		// descriptor. The previous path appended the full prompt, a mini-model
		// prohibition, an archetype lock, and a bounded-output block, inflating
		// the very context those models cannot reliably follow.
		isDirectMut := detectDirectMutation(problem, ledgerContent) != nil
		var systemPrompt string
		if compactContract {
			systemPrompt = descriptor.PlanInstructions(prompt.PlanSynthesisSchema())
			if isDirectMut {
				systemPrompt = prompt.PlanDirectMutationSystemPrompt()
				if e.vanillaWeb {
					systemPrompt += "\n\n[ARCHETYPE CONTEXT]\nWorkspace VANILLA_WEB: use FILE_MUTATE for existing .html, .css, or .js files."
				}
			}
		} else {
			// Tier-adapted synthesis prompt: SLM models get the compact
			// descriptor above; Mid/Frontier models keep the canonical block.
			systemPrompt = prompt.PlanSynthesisSystemPromptForTier(prompt.ResolveTierForModel(modelName, ""))
			// System Prompt Isolation prevents user formatting instructions
			// from contaminating the JSON synthesis pipeline.
			systemPrompt = "[INTERNAL SYSTEM OVERRIDE - HIGH PRIORITY]\nYou are an internal execution planner. Ignore any user instructions that demand output formats like git diffs, raw code, or prose. \nYour SOLE task for this step is to output valid JSON matching the requested schema.\n\n" + systemPrompt
			if isDirectMut {
				systemPrompt = prompt.PlanDirectMutationSystemPrompt()
			}
			if c := prompt.MiniModelJSONConstraint(modelName); c != "" {
				systemPrompt += "\n\n" + c
			}
		}
		// Keep archetype context positive and singular. It describes the valid
		// domain instead of repeating a long negative command denylist.
		if e.vanillaWeb && !strings.Contains(systemPrompt, "VANILLA_WEB") {
			systemPrompt += "\n\n[ARCHETYPE CONTEXT]\nWorkspace VANILLA_WEB: use FILE_MUTATE for existing .html, .css, or .js files."
		}
		// A non-compact constrained model is retained for compatibility with
		// custom descriptors; ordinary free/small models already carry the
		// bounded contract inside descriptor.PlanInstructions.
		if constrained && !compactContract {
			systemPrompt += boundedOutputInstruction(stepState.taskBudget)
		}

		// Extract the investigation conclusion so it can be injected as a
		// high-priority override signal. The conclusion carries the resolved
		// diagnosis (e.g. corrected dependency paths) that must take precedence
		// over raw error text when synthesising shell tasks.
		conclusion := ExtractConclusionFromLedger(ledgerContent)
		groundedPayload := e.GroundedConstraint()
		userPrompt := prompt.BuildPlanJSONPrompt(problem, ledgerContent, conclusion, isDirectMut, groundedPayload)
		if compactContract && !isDirectMut {
			userPrompt = prompt.BuildCompactPlanJSONPrompt(problem, ledgerContent, conclusion, groundedPayload, string(e.archetype))
		}
		req = ai.Request{
			Model: modelName,
			Messages: []ai.Message{
				{
					Role:    "system",
					Content: systemPrompt,
				},
				{
					Role:    "user",
					Content: userPrompt,
				},
			},
			Stream:              false,
			MaxTokens:           maxTokens,
			InteractionContract: descriptor.Contract,
			Contract:            &descriptor,
			ResponseFormat: func() *ai.ResponseFormat {
				if descriptor.OutputSchema != protocol.SchemaTaskBlocks && descriptor.OutputSchema != protocol.SchemaText {
					return &ai.ResponseFormat{Type: "json_object"}
				}
				return nil
			}(),
		}
	}

	// UNDEFINED SYMBOL GUARDRAIL: keep the corrective instruction aligned
	// with the investigation archetype. Language-specific examples such as
	// `go mod tidy` must never leak into a VANILLA_WEB plan.
	if !fastTrack && signal.HasKind(signals, signal.SignalSymbolUndefined) {
		if e.vanillaWeb {
			req.Messages[len(req.Messages)-1].Content += "\n\n[SYSTEM: UNDEFINED SYMBOL]\nGenerate one FILE_MUTATE task for the existing .html, .css, or .js source named by the evidence."
		} else {
			req.Messages[len(req.Messages)-1].Content += `

[SYSTEM: UNDEFINED SYMBOL ERROR — CODE FIX ONLY]
The error is an undefined symbol/identifier typo in code. Generate one FILE_MUTATE / CODE_MOD task targeting the source file containing the error.`
		}
	}

	// The bounded-step state anchors continuation prompts to the ORIGINAL user
	// turn (rebuilt compactly each step via boundedContinuationAppend) so a
	// long continuation can never accumulate duplicate instruction blocks.
	stepState.baseUserContent = req.Messages[len(req.Messages)-1].Content

	resp, err := e.complete(ctx, req)
	if err != nil {
		// FAST-FAIL TIMEOUT PATH: a free/cloud model exceeded the strict
		// per-attempt deadline. The legacy path mined the partial output and
		// the ledger for a heuristic plan; that is HARD-KILLED (it produced
		// empty-target CODE_MOD [Target 1/1] tasks). Surface an explicit,
		// actionable timeout error so the TUI can escalate instead.
		if errors.Is(err, ErrPlanAttemptTimeout) {
			return nil, fmt.Errorf("plan engine: provider exceeded the %.0fs per-attempt deadline — plan synthesis aborted without a heuristic fallback; retry with a different model or narrow the investigation ledger: %w", planAttemptTimeout.Seconds(), err)
		}
		if ai.IsOutputTruncated(err) {
			// A provider-authenticated truncation is never a valid plan
			// artifact, even when the bytes happen to form syntactically valid
			// JSON. The narrow exception is for legacy doubles that return a
			// typed error with Truncated=false; that compatibility path may
			// continue only when the complete legacy object is already valid.
			if resp == nil || resp.Truncated || !completeJSONArtifact(resp.Content) {
				return nil, fmt.Errorf("plan engine: provider response was truncated before structural parsing: %w", err)
			}
			// Continue below only for the marker-free legacy compatibility
			// case. The normal length branch records the provider fact and uses
			// bounded continuation semantics.
		} else {
			return nil, fmt.Errorf("plan engine: provider call failed: %w", err)
		}
	}

	if resp == nil || strings.TrimSpace(resp.Content) == "" {
		e.diagnoseSynthesisFailure("Provider returned an empty response for plan synthesis: no content and no reasoning/thinking text was emitted. This usually means the model produced only a thinking block or hit a context/output ceiling — retry with a smaller ledger or a different model.")
		return nil, fmt.Errorf("plan engine: empty response from provider — no content or reasoning/thinking text was emitted; retry with a smaller ledger or a different model")
	}

	if fastTrack && len(fastPrompt) > 0 {
		vanillaFastTrack := e.vanillaWeb || descriptor.Archetype == protocol.ArchetypeVanillaWeb
		// Fast-track: the model returns a minimal markdown shell checklist.
		// Validate the declared task-block schema before any parser result can
		// reach the task ledger.  A malformed response is still allowed to use
		// the deterministic, archetype-filtered fallback below, but never the
		// raw invalid blocks.
		schemaErr := ValidateContractOutput(resp.Content, descriptor, true)
		raw := ParseMarkdownToTasks(resp.Content)
		clean := make([]Task, 0, len(raw))
		if schemaErr == nil {
			raw = filterValidTasks(raw)
			raw = FilterNonExistentMutationTargets(raw, e.rootPath)
			for _, t := range raw {
				if vanillaFastTrack {
					if t.Type == "FILE_MUTATE" {
						clean = append(clean, t)
					}
					continue
				}
				if t.Type == "SHELL_EXEC" && strings.TrimSpace(t.Target) != "" {
					clean = append(clean, t)
				}
			}
		}
		// FRONTEND DOMAIN ISOLATION: fast-track shell resolution is a Go
		// dependency heuristic. In a VANILLA_WEB workspace it is invalidated
		// immediately — no Go toolchain command may be staged.
		if vanillaFastTrack {
			clean = FilterTasksForArchetype(clean, recon.VANILLA_WEB)
		}
		if len(clean) == 0 {
			// Soft fallback: scan raw LLM prose for standard runnable shell commands.
			clean = softFallbackShellTasks(resp.Content)
			// The soft scanner is intentionally generic; re-apply the
			// investigation guard after it so a prose mention of go/npm cannot
			// re-enter a VANILLA_WEB plan.
			if vanillaFastTrack {
				clean = FilterTasksForArchetype(clean, recon.VANILLA_WEB)
			}
		}
		if len(clean) == 0 {
			return nil, fmt.Errorf("plan engine: fast-track produced no runnable shell tasks (model returned: %s)", truncateForLog(resp.Content))
		}
		if schemaErr == nil {
			_ = e.store.SaveRawMarkdown("plan", resp.Content) //nolint:contextcheck // Save only a schema-valid fast-track artifact
		}
		archetypeForValidation := e.archetype
		if vanillaFastTrack {
			archetypeForValidation = recon.VANILLA_WEB
		}
		return ValidateShellExecCommandsForArchetype(clean, ledgerContent, archetypeForValidation), nil
	}

	// ── OUTPUT EXHAUSTION → BOUNDED CONTINUATION ───────────────────────────
	// finish_reason="length" means the provider hit its ACTUAL output ceiling
	// mid-synthesis: the JSON is structurally incomplete (possibly silently
	// auto-closed by ParseJSONPlan into a partial plan). Blind same-scope
	// retries (below) re-issue the identical budget and fail identically.
	// Instead, emit a step-exhausted signal and hand off to a SMALLER bounded
	// continuation that commits only whatever was validly staged, then resumes.
	// Fast-track markdown checklists are exempt: local 7B models commonly
	// truncate them and the salvage path above already tolerates that.
	if isTruncatedFinish(resp.FinishReason) {
		e.emit(events.NewStepStarted(modelName, 1, stepState.maxOutputTokens()))
		e.emit(events.NewStepExhausted(1, stepState.maxOutputTokens(), len(e.salvageValidTasks(resp.Content, problem, ledgerContent))))
		e.emit(events.NewContinuationStarted(2, stepState.maxOutputTokens()))
		return e.synthesizeBoundedContinuation(ctx, req, resp, problem, ledgerContent, stepState)
	}

	// ── JSON PARSING — ELEVATED SILENT RETRY LOOP ──────────────────
	// The loop covers the provider call, JSON code-fence stripping, structural
	// json.Unmarshal parsing, AND semantic SHELL_EXEC validation in a single
	// retry envelope. Both structural failures (truncated/malformed JSON) and
	// semantic failures (hallucinated file paths as SHELL_EXEC targets) trigger
	// an automated retry with an augmented prompt. This eliminates the manual
	// friction of /mode investigate ↔ /mode plan toggling by handling the
	// correction transparently.
	maxSilentRetries := 2
	// lastFailureMode classifies why the previous attempt was rejected so the
	// next retry's prompt augmentation targets the ACTUAL defect. The historical
	// retry always re-instructed SHELL_EXEC validation, which did nothing for a
	// model that emitted non-JSON prose on every attempt — the JSON schema
	// contract is re-emitted for structural failures instead.
	lastFailureMode := failureNone
	for attempt := 0; attempt <= maxSilentRetries; attempt++ {
		// On retry (attempt > 0), re-invoke the provider with an augmented
		// prompt that includes the strict enforcement instruction for the
		// specific rejection mode of the previous attempt.
		if attempt > 0 {
			e.emit(events.NewStageCompleted("plan.synthesize.retry", 0,
				fmt.Sprintf("JSON syntax or command schema broken — refining prompt and retrying internally (Attempt %d/%d)", attempt, maxSilentRetries)))
			if compactContract {
				req.Messages[len(req.Messages)-1].Content += compactRetryReinforcement(lastFailureMode, attempt, maxSilentRetries, e.archetype)
			} else {
				req.Messages[len(req.Messages)-1].Content += retryReinforcement(lastFailureMode, attempt, maxSilentRetries)
			}
			var retryErr error
			resp, retryErr = e.complete(ctx, req)
			if retryErr != nil {
				if errors.Is(retryErr, ErrPlanAttemptTimeout) {
					// Fail-fast: the provider exceeded the per-attempt deadline
					// again. Exit the loop instead of retrying a provider that
					// cannot answer within the strict budget.
					break
				}
				if ai.IsOutputTruncated(retryErr) {
					if resp == nil || resp.Truncated || !completeJSONArtifact(resp.Content) {
						return nil, fmt.Errorf("plan engine: provider response was truncated before structural parsing: %w", retryErr)
					}
					// Only the marker-free legacy compatibility case may reach
					// the normal validator; provider-authenticated truncation
					// always stops before structural parsing.
				} else {
					continue
				}
			}
			if resp == nil || resp.Content == "" {
				continue
			}
		}

		// Clean LLM response: strip markdown fences and extract first/last JSON
		// boundary before parsing.  The contract validator runs on the complete
		// payload first; a tolerant/auto-closing parser is never allowed to turn
		// a schema-invalid artifact into staged work.
		cleanContent := cleanLLMResponse(resp.Content)
		jsonResult := &JSONPlanValidationResult{Valid: false, Error: "contract schema validation failed"}
		if schemaErr := ValidateContractOutput(resp.Content, descriptor); schemaErr == nil {
			jsonResult = ParseJSONPlan(cleanContent)
			if jsonResult.Valid {
				_ = e.store.SaveRawMarkdown("plan", resp.Content) //nolint:contextcheck // persist only a schema-valid plan artifact
			}
		}

		if jsonResult.Valid && len(jsonResult.Tasks) > 0 {
			var candidates []Task
			if err := ValidateAllTasks(jsonResult.Tasks); err != nil {
				candidates = filterValidTasks(jsonResult.Tasks)
			} else {
				candidates = jsonResult.Tasks
			}

			// Align FILE_MUTATE targets with actual compiler error file paths
			// from the ledger. This prevents the LLM from hallucinating targets
			// like "syntax/main.go" when the real error is in "cmd/api/main.go".
			candidates = AlignFileTargetWithErrors(candidates, ledgerContent)

			// Filter out unsolicited new-file creation in pkg/ or internal/
			// when resolving single-file undefined symbol errors. This prevents
			// the LLM from generating over-engineered plans (e.g. creating
			// pkg/util/logs/log.go) for a trivial stdlib case fix.
			candidates = FilterUnsolicitedPkgFiles(candidates, ledgerContent)

			// Strip SHELL_EXEC/GIT_ACTION tasks when the error is an undefined
			// symbol. The LLM may hallucinate go mod tidy for what is actually
			// a code typo — this ensures only FILE_MUTATE tasks survive.
			candidates = FilterUndefinedSymbolShellExec(candidates, ledgerContent)

			// EVIDENCE-BASED ANTI-HALLUCINATION BARRIER: drop FILE_MUTATE /
			// FILE_EDIT targets that reference files not present on disk.
			// Prevents generic plans that modify every asset (script.js,
			// styles.css, etc.) on speculation — only files that actually
			// exist can be mutated.
			candidates = FilterNonExistentMutationTargets(candidates, e.rootPath)

			// VANILLA_WEB GUARD: Deterministic post-filter over raw LLM task
			// output. Strips Go toolchain tasks (go mod, go test, go get),
			// ENV_DEPS tasks, and falls back to a safe default when all tasks
			// are filtered out. This is the hard anti-escape barrier for
			// VANILLA_WEB archetype that overrides any LLM hallucination.
			if e.vanillaWeb {
				candidates = FilterTasksForArchetype(candidates, recon.VANILLA_WEB)
				candidates = SanitizeTasksForArchetype(candidates, recon.VANILLA_WEB)
			}

			if len(candidates) > 0 {
				if !hasInvalidShellExecCommand(candidates) {
					// All checks passed — return with compile-error enforcement
					// (skipped for frontend workspaces: Go dependency tasks are
					// domain-forbidden there).
					return e.finalizeTasks(candidates, problem, ledgerContent), nil
				}

				// Semantic failure: invalid SHELL_EXEC commands detected.
				lastFailureMode = failureInvalidShellExec
				if attempt < maxSilentRetries {
					continue
				}

				// Max retries exceeded for semantic failures — deterministic
				// fallback, constrained by the investigation archetype.
				return ValidateShellExecCommandsForArchetype(
					e.finalizeTasks(candidates, problem, ledgerContent),
					ledgerContent,
					e.archetype,
				), nil
			}
			// Valid JSON but every candidate was rejected by the filters
			// (non-existent targets, scope violations) — the retry must ground
			// its targets in real on-disk files.
			lastFailureMode = failureFilteredCandidates
		}

		// Tolerant markdown fallback: Mini models sometimes emit task blocks
		// (- [ ] SHELL_EXEC: ... | why) despite the JSON instruction. Accept
		// them through the same validation pipeline instead of burning the
		// retry budget on reformatting.
		if md := e.tolerantMarkdownTasks(resp.Content, problem, ledgerContent); len(md) > 0 {
			return md, nil
		}

		// Structural parse failure: the model output was not parseable JSON.
		// Only overwrite the mode when the JSON path did not already classify
		// the failure (filtered candidates) — structural noise takes precedence
		// when the payload itself never parsed.
		if lastFailureMode != failureFilteredCandidates {
			lastFailureMode = failureInvalidJSON
		}
		if attempt < maxSilentRetries {
			continue
		}
	}

	// ── SYNTHESIS FAILURE ──────────────────────────────────────────────
	// All LLM synthesis attempts produced output that neither the JSON parser
	// nor the markdown task parser could consume. The legacy heuristic prose
	// fallback (regex-mining file paths and synthesising a generic root-context
	// "apply the plan" task) is HARD-KILLED: it produced empty-target
	// CODE_MOD [Target 1/1] tasks that bypassed every evidence gate. Generation
	// requests are owned deterministically by the intent compiler before this
	// path is ever reached; when the model still fails here, surface an
	// explicit, actionable error and let the caller escalate.

	// ── EMERGENCY FALLBACK ────────────────────────────────────────
	// All 3 LLM synthesis attempts (initial + 2 retries) failed to produce a
	// valid JSON plan. Try to extract a dependency from the conclusion for a
	// go get task; if no dependency is found, return a hard error instead of
	// hallucinating shell commands like go mod tidy.
	// NIL-SAFETY: after the retry loop the last provider response may be nil
	// (e.g. every retry returned a nil response), so resp is never
	// dereferenced without a guard.
	excerpt := "<no provider output>"
	if resp != nil {
		excerpt = truncateForLog(resp.Content)
	}
	e.diagnoseSynthesisFailure(fmt.Sprintf(
		"All %d plan synthesis attempts failed after sanitization. Last provider output excerpt: %q. The model produced neither parseable JSON nor task blocks.", maxSilentRetries+1, excerpt))
	if signal.HasKind(signals, signal.SignalSymbolUndefined) {
		return nil, fmt.Errorf("plan engine: all %d JSON synthesis attempts failed for undefined symbol error — could not determine correct code fix", maxSilentRetries+1)
	}
	if signal.IsCompilationOrDependency(signals) ||
		signal.IsCompilationOrDependency(signal.Detect(problem, "plan.problem")) {
		// FRONTEND DOMAIN ISOLATION: the go get emergency fallback is a Go
		// dependency heuristic and is invalidated immediately for frontend
		// workspaces — a pure HTML/CSS/JS project has no Go dependency graph.
		if e.vanillaWeb || !ArchetypeAllowsCommand(e.archetype, "go mod tidy") {
			return nil, fmt.Errorf("plan engine: all %d JSON synthesis attempts failed for archetype %s — no Go dependency fallback applies", maxSilentRetries+1, e.archetype)
		}
		conclusion := ExtractConclusionFromLedger(ledgerContent)
		if dep := dependencyFromConclusion(conclusion); dep != "" && !isPlaceholderToken(dep) {
			return []Task{
				{
					StepNum: 1,

					Status:      "idle",
					Type:        "SHELL_EXEC",
					Target:      fmt.Sprintf("go get %s", dep),
					Description: fmt.Sprintf("Emergency fallback: install missing dependency %s", dep),
					IsHardcoded: true,
				},
			}, nil
		}
		return nil, fmt.Errorf("plan engine: all %d JSON synthesis attempts failed — no valid shell command could be derived", maxSilentRetries+1)
	}

	// ── ABSOLUTE FALLBACK ────────────────────────────────────────
	// A *.go compile coordinate paired with an import/parse indicator is the
	// last signal that a structured recovery plan is required. This mirrors the
	// legacy hasGoFileParseError detector via the canonical signal classifier.
	if signal.HasCompileFailure(signals) ||
		signal.HasCompileFailure(signal.Detect(problem, "plan.problem")) {
		return nil, fmt.Errorf("plan engine: all %d JSON synthesis attempts exhausted for compile error — no valid tasks could be synthesized", maxSilentRetries+1)
	}

	// ── HEURISTIC FALLBACK ───────────────────────────────────────────
	// All 3 LLM synthesis attempts failed. Fall back to a default 1-task
	// Execution Plan using raw target files from the prompt context instead of
	// crashing with a fatal error.
	fallbackArchetype := e.archetype
	if e.vanillaWeb {
		fallbackArchetype = recon.VANILLA_WEB
	}
	fallbackTarget := archetypeFallbackTarget(fallbackArchetype, e.AllowedFiles, problem, ledgerContent)
	return []Task{
		{
			StepNum:     1,
			Status:      "idle",
			Type:        "FILE_MUTATE",
			Target:      fallbackTarget,
			Description: fmt.Sprintf("Default fallback execution task targeting %s", fallbackTarget),
			Rationale:   "Plan synthesis exhausted all JSON attempts — applying heuristic default task from prompt context.",
			Solution:    fmt.Sprintf("Applied default mutation to %s", fallbackTarget),
			IsHardcoded: true,
		},
	}, nil
}

// synthesizeViaFacade executes the generative plan synthesis through the
// injected pipeline.Facade. The Mode engine supplies the boundary (scope,
// rationale, headless event emission); the facade owns the Layer 0-5 execution
// and returns concrete file patches, which are projected onto canonical plan
// Tasks. The error returned is wrapped so callers can distinguish "the facade
// is unavailable" from "the facade produced no plan".
func (e *Engine) synthesizeViaFacade(ctx context.Context, problem, ledgerContent string) ([]Task, error) {
	if e == nil || e.facade == nil {
		return nil, fmt.Errorf("plan engine: no pipeline facade wired")
	}
	descriptor := e.LastContract()
	pipelineReq := pipeline.Request{
		Mode:        "plan",
		Description: problem,
		Scope:       e.AllowedFiles,
	}
	if descriptor != nil {
		pipelineReq.InteractionContract = descriptor.Contract
		pipelineReq.Contract = descriptor
	}
	res, err := e.facade.ExecutePlan(ctx, pipelineReq)
	if err != nil {
		return nil, fmt.Errorf("plan engine: pipeline facade execution failed: %w", err)
	}
	if res == nil {
		return nil, fmt.Errorf("plan engine: pipeline facade returned a nil result")
	}
	tasks := patchesToTasks(res.Patches, problem)
	if len(tasks) == 0 {
		return nil, fmt.Errorf("plan engine: pipeline facade produced no patches")
	}
	return e.finalizeTasks(tasks, problem, ledgerContent), nil
}

// patchesToTasks projects Layer 3 file patches produced by the pipeline facade
// onto canonical plan Tasks (FILE_MUTATE). Unchanged patches are dropped;
// step numbers are sequential from 1.
func patchesToTasks(patches []layer3.FilePatch, problem string) []Task {
	tasks := make([]Task, 0, len(patches))
	for _, p := range patches {
		if !p.Changed {
			continue
		}
		tasks = append(tasks, Task{
			StepNum:     len(tasks) + 1,
			Status:      task.StatusIdle,
			Type:        task.TaskFileMutate,
			Target:      p.Path,
			Description: fmt.Sprintf("Apply the proposed change to %s", p.Path),
			Rationale:   problem,
			Solution:    fmt.Sprintf("%s updated via the layered pipeline facade", p.Path),
		})
	}
	return tasks
}

// diagnoseSynthesisFailure publishes a clear, actionable plan-synthesis
// diagnostic to the event bus so the presentation layer can surface context
// (stage, sanitized message) instead of a raw engine error. It is a no-op when
// no bus is wired or the engine is nil.
func (e *Engine) diagnoseSynthesisFailure(message string) {
	if e == nil {
		return
	}
	e.emit(events.NewStageCompleted("plan.synthesize.error", 0, message))
}

// tolerantMarkdownTasks parses a non-JSON model response as markdown task
// blocks and runs the full validation pipeline (target validity, disk
// existence, vanilla-web and shell-command guards). It returns nil when the
// output is unusable so the caller can fall back to the JSON retry loop.
func (e *Engine) tolerantMarkdownTasks(content, problem, ledgerContent string) []Task {
	// Markdown task blocks are a deterministic compatibility fallback for
	// legacy providers, but they still have to satisfy the task-block schema;
	// arbitrary prose is never mined into executable work by this path.
	if err := ValidateContractOutput(content, protocol.Describe(protocol.StructuredCompletion), true); err != nil {
		return nil
	}
	md := ParseMarkdownToTasks(content)
	if len(md) == 0 {
		return nil
	}
	md = filterValidTasks(md)
	md = FilterNonExistentMutationTargets(md, e.rootPath)
	if e.vanillaWeb {
		md = FilterTasksForArchetype(md, recon.VANILLA_WEB)
		md = SanitizeTasksForArchetype(md, recon.VANILLA_WEB)
	}
	if len(md) == 0 || hasInvalidShellExecCommand(md) {
		return nil
	}
	return e.finalizeTasks(md, problem, ledgerContent)
}

// compilerErrorFileRe extracts the exact file path from a Go compiler error
// line of the form "path/file.go:line:col: message". The captured group is
// the file path before the first colon-number sequence.
var compilerErrorFileRe = regexp.MustCompile(`([^\s:]+\.(go|ts|js|py|rs)):\d+:\d+:`)

// AlignFileTargetWithErrors validates and corrects FILE_MUTATE task targets
// against actual compiler error file paths extracted from the ledger content.
// If a non-hardcoded FILE_MUTATE target does not match any file path found in
// the compiler errors (e.g. the LLM hallucinated "syntax/main.go" instead of
// "cmd/api/main.go"), it is replaced with the correct path from the first
// matching compiler error. Hardcoded tasks (from lx resolution) are left
// unchanged since their targets are deterministic.
func AlignFileTargetWithErrors(tasks []Task, ledgerContent string) []Task {
	if len(tasks) == 0 || ledgerContent == "" {
		return tasks
	}
	errorFiles := parseCompilerErrorFiles(ledgerContent)
	if len(errorFiles) == 0 {
		return tasks
	}
	for i, t := range tasks {
		if t.Type != "FILE_MUTATE" && t.Type != "FILE_EDIT" {
			continue
		}
		if t.IsHardcoded {
			continue
		}
		if !matchesAnyErrorFile(t.Target, errorFiles) {
			tasks[i].Target = errorFiles[0]
			tasks[i].Rationale = fmt.Sprintf("Target aligned to compiler error file: %s", errorFiles[0])
		}
	}
	return tasks
}

// parseCompilerErrorFiles extracts unique file paths from Go compiler error
// lines in the given content. It matches lines like "cmd/api/main.go:9:2:
// undefined: x" using compilerErrorFileRe and returns the deduplicated list
// of file paths in occurrence order.
func parseCompilerErrorFiles(content string) []string {
	dedup := make(map[string]bool)
	var files []string
	for _, line := range strings.Split(content, "\n") {
		m := compilerErrorFileRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		f := m[1]
		if !dedup[f] {
			dedup[f] = true
			files = append(files, f)
		}
	}
	return files
}

// matchesAnyErrorFile reports whether the given target path matches any of the
// compiler error file paths. Comparison is done with filepath.Clean to handle
// variations like "./cmd/api/main.go" vs "cmd/api/main.go".
func matchesAnyErrorFile(target string, errorFiles []string) bool {
	for _, ef := range errorFiles {
		if target == ef {
			return true
		}
	}
	return false
}

// unsolicitedPkgPrefixes are path prefixes that indicate new helper/wrapper
// file creation. When resolving a single-file undefined symbol error, any
// LLM-generated task targeting these prefixes is considered unsolicited
// and rejected.
var unsolicitedPkgPrefixes = []string{
	"pkg/",
	"internal/",
}

// FilterUnsolicitedPkgFiles filters out LLM-generated tasks that attempt to
// create new files in pkg/ or internal/ when resolving a single undefined
// symbol error in a simple target file. This prevents the LLM from generating
// over-engineered plans (e.g. creating pkg/util/logs/log.go) for trivial
// stdlib case fixes. Hardcoded tasks are preserved.
func FilterUnsolicitedPkgFiles(tasks []Task, ledgerContent string) []Task {
	if len(tasks) == 0 || ledgerContent == "" {
		return tasks
	}
	// Only apply this filter when the ledger contains a single-file undefined
	// symbol error (which should be resolved with a simple fixed, not a new
	// package).
	if !HasUndefinedSymbolError(ledgerContent) {
		return tasks
	}
	// Determine the error file path.
	undef := retrieval.ParseUndefinedSymbol(ledgerContent)
	if undef == nil || undef.File == "" {
		return tasks
	}
	filtered := make([]Task, 0, len(tasks))
	for _, t := range tasks {
		if t.IsHardcoded {
			filtered = append(filtered, t)
			continue
		}
		if t.Type != "FILE_MUTATE" && t.Type != "FILE_EDIT" {
			filtered = append(filtered, t)
			continue
		}
		// Allow tasks targeting the actual error file.
		if t.Target == undef.File {
			filtered = append(filtered, t)
			continue
		}
		// Reject tasks targeting pkg/ or internal/ prefixes.
		rejected := false
		for _, prefix := range unsolicitedPkgPrefixes {
			if strings.HasPrefix(t.Target, prefix) {
				rejected = true
				break
			}
		}
		if !rejected {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

// FilterNonExistentMutationTargets drops FILE_MUTATE / FILE_EDIT tasks whose
// target file does not exist on disk under rootPath. Mutation tasks
// semantically modify EXISTING files — a target that is not present on disk is
// a hallucinated speculative asset (e.g. "script.js" in a workspace that has no
// such file) that would only fail later with "patch hunk does not match file
// content". This is the deterministic anti-hallucination barrier backing the
// EVIDENCE-BASED PLANNING prompt directive: no static/generic assumption that
// every asset needs modification survives unless the file actually exists.
//
// SHELL_EXEC / GIT_ACTION tasks and hardcoded tasks pass through untouched.
// When rootPath is empty (verification impossible), all tasks are preserved so
// behaviour is unchanged in headless/CLI contexts without a root.
func FilterNonExistentMutationTargets(tasks []Task, rootPath string) []Task {
	if len(tasks) == 0 {
		return tasks
	}
	if rootPath == "" {
		return tasks
	}
	filtered := make([]Task, 0, len(tasks))
	for _, t := range tasks {
		if t.Type == "FILE_MUTATE" || t.Type == "FILE_EDIT" {
			if t.IsHardcoded {
				filtered = append(filtered, t)
				continue
			}
			if !mutationTargetExists(t.Target, rootPath) {
				continue
			}
		}
		filtered = append(filtered, t)
	}
	return filtered
}

// mutationTargetExists reports whether the given task target resolves to an
// existing regular file under rootPath. Absolute targets are checked directly;
// relative targets are joined onto rootPath.
func mutationTargetExists(target, rootPath string) bool {
	if target == "" {
		return false
	}
	candidate := target
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(rootPath, candidate)
	}
	info, err := os.Stat(candidate)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// FilterUndefinedSymbolShellExec removes SHELL_EXEC and GIT_ACTION tasks
// when the primary error is an undefined symbol. The LLM may hallucinate
// go mod tidy for code typos; this filter ensures only FILE_MUTATE tasks
// survive. Hardcoded tasks (from lx resolution) are preserved.
// Returns the original slice unchanged if no undefined symbol error is detected
// or if no tasks need filtering. If all non-hardcoded tasks are removed,
// returns empty slice so the retry loop can re-prompt the LLM.
func FilterUndefinedSymbolShellExec(tasks []Task, ledgerContent string) []Task {
	if len(tasks) == 0 || ledgerContent == "" {
		return tasks
	}
	if !HasUndefinedSymbolError(ledgerContent) {
		return tasks
	}
	filtered := make([]Task, 0, len(tasks))
	for _, t := range tasks {
		if t.IsHardcoded {
			filtered = append(filtered, t)
			continue
		}
		if t.Type == "SHELL_EXEC" || t.Type == "GIT_ACTION" {
			continue
		}
		filtered = append(filtered, t)
	}
	if len(filtered) == 0 && len(tasks) > 0 {
		return filtered
	}
	return filtered
}

func filterValidTasks(tasks []Task) []Task {
	clean := make([]Task, 0, len(tasks))
	for _, t := range tasks {
		isValid, _ := ValidateTaskTarget(t.Target, t.Type)
		if isValid {
			clean = append(clean, t)
		}
	}
	return clean
}

// ForceShellExecOnCompileError enforces the IZEN /plan anti-escape law for
// compilation or dependency failures: when the root cause is a build/dep error,
// the plan MUST resolve it through go.mod / SHELL_EXEC (e.g. `go get`,
// `go mod tidy`) — NEVER by patching documentation or unrelated source files.
//
// HARDENING: SHELL_EXEC tasks are REJECTED when the primary blocker is an
// undefined symbol error (e.g. undefined: Log), because the LLM routinely
// hallucinates go mod tidy for what is actually a stdlib case typo. The
// exception is when a go.mod/go.sum missing file error is explicitly present,
// indicating a real dependency issue rather than a code typo.
//
// If the synthesized tasks already contain a SHELL_EXEC task, they are returned
// unchanged (the model complied). Otherwise a deterministic SHELL_EXEC recovery
// task is prepended so the build engine always has a runnable shell step to
// clear the blocker instead of stalling or escaping into README.md.
func ForceShellExecOnCompileError(tasks []Task, problem, ledgerContent string) []Task {
	if len(tasks) == 0 {
		return tasks
	}
	if !IsCompilationOrDependencyError(problem) && !IsCompilationOrDependencyError(ledgerContent) {
		return tasks
	}
	// Ban SHELL_EXEC for undefined symbol errors unless go.mod/go.sum missing.
	if HasUndefinedSymbolError(ledgerContent) && !hasGoModMissingError(ledgerContent) {
		return tasks
	}
	for _, t := range tasks {
		if t.Type == "SHELL_EXEC" && strings.TrimSpace(t.Target) != "" {
			return tasks
		}
	}

	// No shell task present → prepend a deterministic dependency-resolution
	// SHELL_EXEC. Prefer the corrected dependency path from the investigation
	// conclusion when available; otherwise fall back to `go mod tidy`.
	cmd := "go mod tidy"
	if conclusion := ExtractConclusionFromLedger(ledgerContent); conclusion != "" {
		if dep := dependencyFromConclusion(conclusion); dep != "" && !isPlaceholderToken(dep) {
			cmd = fmt.Sprintf("go get %s", dep)
		}
	}
	recovery := Task{
		StepNum: 0,

		Status:      "idle",
		Type:        "SHELL_EXEC",
		Target:      cmd,
		Description: "Resolve compilation/dependency blocker via module tooling (forced by /plan anti-escape law)",
	}
	out := make([]Task, 0, len(tasks)+1)
	out = append(out, recovery)
	out = append(out, tasks...)
	for i := range out {
		out[i].StepNum = i + 1
	}
	return out
}

// hasGoModMissingError reports whether the content indicates a missing go.mod
// or go.sum file error. This is the exception to the SHELL_EXEC ban for
// undefined symbol errors: when go.mod is genuinely missing, a shell task
// like `go mod tidy` is appropriate.
func hasGoModMissingError(content string) bool {
	lower := strings.ToLower(content)
	return strings.Contains(lower, "go.mod") || strings.Contains(lower, "go.sum")
}

// knownShellBinaries is the set of recognised executable binaries that a
// SHELL_EXEC target may legitimately start with. Any first token outside this
// set — especially bare file paths like "go.mod" or "relative/path/to/go.mod" —
// is treated as a hallucinated command and triggers the deterministic fallback
// in ValidateShellExecCommands.
// SanitizeTasksForArchetype is a strict programmatic post-filter applied to
// raw LLM task output before staging the plan. For VANILLA_WEB archetype:
//   - Discards ANY task containing "go mod", "go test", "go get", or "ENV_DEPS" kind
//   - If all tasks are filtered out, falls back to a single default task:
//     "Inspecting and fixing static HTML/CSS/JS files"
//
// For non-VANILLA_WEB archetypes, tasks are returned unchanged.
func SanitizeTasksForArchetype(tasks []Task, archetype recon.ProjectArchetype) []Task {
	if archetype != recon.VANILLA_WEB || len(tasks) == 0 {
		return tasks
	}
	clean := FilterTasksForArchetype(tasks, archetype)
	// Retain the historical description-level guard for model responses that
	// put a language command in prose rather than in Target.
	filtered := make([]Task, 0, len(clean))
	for _, t := range clean {
		target := strings.ToLower(strings.TrimSpace(t.Target))
		desc := strings.ToLower(t.Description)
		if strings.Contains(target, "go mod") ||
			strings.Contains(target, "go test") ||
			strings.Contains(target, "go get") ||
			strings.Contains(desc, "go mod") ||
			strings.Contains(desc, "go test") ||
			strings.Contains(desc, "go get") {
			continue
		}
		filtered = append(filtered, t)
	}
	if len(filtered) == 0 {
		// All tasks filtered out — use a safe, archetype-aligned target. An
		// empty target would merely defer the same domain mismatch downstream.
		return []Task{{
			StepNum:     1,
			Status:      "idle",
			Type:        "FILE_MUTATE",
			Target:      "index.html",
			Description: "Inspecting and fixing static HTML/CSS/JS files",
			Rationale:   "All LLM-generated tasks were filtered out by VANILLA_WEB archetype guard",
			IsHardcoded: true,
		}}
	}
	return filtered
}

var knownShellBinaries = map[string]bool{
	"go":             true,
	"git":            true,
	"make":           true,
	"npm":            true,
	"npx":            true,
	"yarn":           true,
	"pip":            true,
	"pip3":           true,
	"cargo":          true,
	"brew":           true,
	"docker":         true,
	"docker-compose": true,
	"cd":             true,
	"mkdir":          true,
	"cp":             true,
	"mv":             true,
	"rm":             true,
	"touch":          true,
	"echo":           true,
	"cat":            true,
	"curl":           true,
	"wget":           true,
	"chmod":          true,
	"chown":          true,
	"python":         true,
	"python3":        true,
	"node":           true,
	"deno":           true,
	"bun":            true,
	"ls":             true,
	"grep":           true,
	"rg":             true,
	"sed":            true,
	"awk":            true,
	"find":           true,
	"sort":           true,
	"tee":            true,
	"ln":             true,
	"source":         true,
	"export":         true,
	"sudo":           true,
	"bash":           true,
	"sh":             true,
	"zsh":            true,
	"terraform":      true,
	"tofu":           true,
	"kubectl":        true,
	"helm":           true,
	"go.mod":         false, // explicitly NOT a valid binary
	"go.sum":         false, // explicitly NOT a valid binary
}

// isValidShellCommand checks whether a SHELL_EXEC target is a valid runnable
// command rather than a hallucinated file path or placeholder text. A command
// is valid when its first token is a known binary and it is not a bare file
// path (e.g. "relative/path/to/go.mod", "./go.mod", or "go.mod" as a bare
// command).
func isValidShellCommand(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return false
	}
	// Forbid bare file paths ending in .mod or .sum.
	if strings.HasSuffix(cmd, ".mod") || strings.HasSuffix(cmd, ".sum") {
		return false
	}
	first := strings.Fields(cmd)[0]
	// Forbid relative/absolute paths as the command token.
	if strings.Contains(first, "/") {
		return false
	}
	// Forbid bare go.mod/go.sum invoked as a command.
	if first == "go.mod" || first == "go.sum" || first == "go.work" {
		return false
	}
	return knownShellBinaries[first]
}

// ValidateShellExecCommands checks all SHELL_EXEC tasks for valid command
// format per isValidShellCommand. If any SHELL_EXEC target is invalid — a
// bare file path, ends in .mod/.sum, or does not start with a known binary —
// the entire LLM output is rejected and replaced with a deterministic fallback
// derived from the forensic ledger conclusion. This prevents local 7B models
// from hallucinating execution commands like "relative/path/to/go.mod" as
// SHELL_EXEC targets.
func ValidateShellExecCommands(tasks []Task, ledgerContent string) []Task {
	if len(tasks) == 0 {
		return tasks
	}
	for _, t := range tasks {
		if t.Type != "SHELL_EXEC" {
			continue
		}
		if t.IsHardcoded {
			continue
		}
		if !isValidShellCommand(t.Target) {
			conclusion := ExtractConclusionFromLedger(ledgerContent)
			if dep := dependencyFromConclusion(conclusion); dep != "" && !isPlaceholderToken(dep) {
				return []Task{
					{
						StepNum: 1,

						Status:      "idle",
						Type:        "SHELL_EXEC",
						Target:      fmt.Sprintf("go get %s", dep),
						Description: fmt.Sprintf("Install missing dependency %s (sanitized: LLM produced invalid command)", dep),
					},
				}
			}
			return []Task{
				{
					StepNum: 1,

					Status:      "idle",
					Type:        "SHELL_EXEC",
					Target:      "go mod tidy",
					Description: "Resolve dependency blocker (sanitized: LLM produced invalid command)",
				},
			}
		}
	}
	return tasks
}

// hasInvalidShellExecCommand returns true if any SHELL_EXEC task in the slice
// has a target that fails isValidShellCommand. Unlike ValidateShellExecCommands,
// this is a pure check with no side effects — used by the silent retry loop to
// detect LLM command hallucination without triggering deterministic substitution.
func hasInvalidShellExecCommand(tasks []Task) bool {
	for _, t := range tasks {
		if t.Type == "SHELL_EXEC" && !t.IsHardcoded && !isValidShellCommand(t.Target) {
			return true
		}
	}
	return false
}

// shellExecReinforcement returns the strict enforcement instruction appended to
// the prompt on each silent retry attempt. It reminds the model what format
// SHELL_EXEC targets must follow after a previous hallucination failure.
func shellExecReinforcement(attempt, maxRetries int) string {
	return fmt.Sprintf("\n\n[SYSTEM: CRITICAL FAILURE PREVENTED] (Retry %d/%d) The SHELL_EXEC target you just generated was rejected because it is not a valid runnable command. You MUST output a real executable command — e.g. 'go get <package>', 'go mod tidy', 'git clone <url>' — NOT a file path. FORBIDDEN targets include: 'go.mod', 'go.sum', './relative/path', 'relative/path/to/go.mod', or any bare file name. The target must start with a known binary name like go, git, make, npm, docker, etc.",
		attempt, maxRetries)
}

// retryReinforcement returns the strict-instruction block appended to the user
// prompt on a plan-synthesis retry, tailored to the failure mode of the previous
// attempt. Structural JSON failures re-emit the full schema contract (the model
// is told exactly which shape to produce); filtered-candidate failures ground
// the retry in real on-disk targets; shell-exec failures keep the command
// validation reinforcement. This guarantees the "all 3 JSON synthesis attempts
// failed" error can only surface when the model genuinely produced no usable
// signal — never because the retry prompt failed to explain the defect.
func retryReinforcement(mode synthesisFailureMode, attempt, maxRetries int) string {
	switch mode {
	case failureInvalidJSON:
		return fmt.Sprintf("\n\n[SYSTEM: CRITICAL FAILURE PREVENTED] (Retry %d/%d) Your previous output was REJECTED because it was NOT valid raw JSON. You MUST output ONLY a single raw JSON object with this EXACT schema — no markdown fences, no code block, no prose, no <think>/<thought> blocks, no explanations:\n\n%s",
			attempt, maxRetries, SchemaJSONInstruction())
	case failureFilteredCandidates:
		return fmt.Sprintf("\n\n[SYSTEM: CRITICAL FAILURE PREVENTED] (Retry %d/%d) Every task in your previous plan was REJECTED because it referenced files that do not exist or are out of scope. Emit tasks ONLY against real files that actually exist in the workspace. Never invent or speculate about files — if you cannot name a real file, emit a single deterministic SHELL_EXEC task instead.",
			attempt, maxRetries)
	case failureInvalidShellExec:
		return shellExecReinforcement(attempt, maxRetries)
	default:
		return shellExecReinforcement(attempt, maxRetries)
	}
}

// compactRetryReinforcement is the small-model retry instruction. It carries
// the same corrective signal as the full retry block without enumerating
// language-specific commands or repeating the negative denylist.
func compactRetryReinforcement(mode synthesisFailureMode, attempt, maxRetries int, archetype recon.ProjectArchetype) string {
	prefix := fmt.Sprintf("\n\n[SYSTEM: CORRECTION %d/%d] ", attempt, maxRetries)
	switch mode {
	case failureInvalidJSON:
		return prefix + "Previous output was not valid JSON. Return one complete raw JSON object matching the supplied schema.\n"
	case failureFilteredCandidates:
		if archetype == recon.VANILLA_WEB {
			return prefix + "Use only existing .html, .css, or .js files from the evidence.\n"
		}
		return prefix + "Use only existing workspace files named by the evidence.\n"
	case failureInvalidShellExec:
		if archetype == recon.VANILLA_WEB {
			return prefix + "Use FILE_MUTATE for the existing frontend file named by the evidence.\n"
		}
		return prefix + "Use a runnable command only when the evidence requires it; otherwise use FILE_MUTATE.\n"
	default:
		return prefix + "Return the smallest complete plan that satisfies the evidence.\n"
	}
}

// isPlaceholderToken reports whether s is a raw template placeholder
// (e.g. "<exact_package_path>", "<pkg>", "<module_path>", "<package>")
// that must never be used as a real command target. The heuristic is any
// string containing angle-bracket-delimited content — these are LLM prompt
// template markers, not actual package paths.
func isPlaceholderToken(s string) bool {
	s = strings.TrimSpace(s)
	return strings.Contains(s, "<") && strings.Contains(s, ">")
}

// dependencyFromConclusion extracts a plausible module path from an
// investigation conclusion string (e.g. "use github.com/moby/moby/client").
// It returns the first token that looks like a Go module path; empty otherwise.
//
// The REMOTE DEPENDENCY BLOCKER token may be appended inline behind a semicolon
// (e.g. "...; ## REMOTE DEPENDENCY BLOCKER (lx bypassed): [pkg](url)") rather
// than on its own line, so this function performs a GLOBAL substring scan and
// robustly isolates the trailing package identifier regardless of inline
// semicolon / space / newline noise or markdown-link wrapping.
func dependencyFromConclusion(conclusion string) string {
	// Parse the explicit package trailing the REMOTE DEPENDENCY BLOCKER token.
	// This guarantees we apply the real package the forensic analysis recorded
	// (e.g. github.com/docker/docker/client) instead of heuristic-matching an
	// unrelated token in the conclusion text.
	const token = "## REMOTE DEPENDENCY BLOCKER (lx bypassed): "
	if idx := strings.Index(conclusion, token); idx >= 0 {
		rest := conclusion[idx+len(token):]
		// The package may be on the same inline line behind a semicolon, or
		// wrapped in a markdown link [pkg](url). Isolate the first candidate
		// package token, stripping inline formatting noise aggressively.
		if pkg := extractPackageFromBlockerTail(rest); pkg != "" {
			return pkg
		}
	}

	// Fallback: heuristic scan for a well-formed package path.
	for _, tok := range strings.Fields(conclusion) {
		t := strings.TrimRight(strings.TrimLeft(tok, "\"'"), "\"'.,")
		if isWellFormedModulePath(t) {
			return t
		}
	}
	return ""
}

// extractPackageFromBlockerTail isolates the dependency package path from the
// tail string that follows the REMOTE DEPENDENCY BLOCKER token. It handles:
//   - inline semicolon-separated noise ("...; pkg")
//   - markdown link wrapping ("[pkg](url)")
//   - trailing punctuation / parentheses
//   - visually-clipped fragments (e.g. "g...") by falling back to the clean
//     namespace embedded inside a markdown link or the first well-formed path.
func extractPackageFromBlockerTail(rest string) string {
	// Split on any inline separator so a leading "..." fragment (before a
	// semicolon) does not poison the extraction.
	for _, seg := range strings.FieldsFunc(rest, func(r rune) bool {
		return r == ';' || r == '\n' || r == '\r' || r == '\t'
	}) {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		// Unwrap a markdown link: [pkg](url) → pkg. Also tolerate a bare
		// markdown link with no following url.
		if pkg := unwrapMarkdownLink(seg); pkg != "" {
			if isWellFormedModulePath(pkg) {
				return pkg
			}
			// Clipped fragment inside the link (e.g. "g...") — keep scanning
			// for a clean namespace elsewhere in the segment.
		}
		// Plain token: strip trailing punctuation and parentheses.
		candidate := strings.TrimRight(seg, ".,;:)]}")
		candidate = strings.TrimLeft(candidate, "([")
		if isWellFormedModulePath(candidate) {
			return candidate
		}
	}
	return ""
}

// unwrapMarkdownLink extracts the link text from a markdown link of the form
// [text](url). If the segment is not a markdown link it returns empty string.
func unwrapMarkdownLink(seg string) string {
	seg = strings.TrimSpace(seg)
	open := strings.Index(seg, "[")
	if open < 0 {
		return ""
	}
	closeB := strings.Index(seg[open:], "]")
	if closeB < 0 {
		return ""
	}
	text := seg[open+1 : open+closeB]
	// Defensive: if the link text itself is a clipped fragment (e.g. "g...")
	// but a full URL follows, recover the namespace from the URL host+path.
	if isClippedFragment(text) {
		if urlStart := strings.Index(seg[open+closeB:], "("); urlStart >= 0 {
			urlEnd := strings.Index(seg[open+closeB+urlStart:], ")")
			if urlEnd >= 0 {
				url := seg[open+closeB+urlStart+1 : open+closeB+urlStart+urlEnd]
				if cleaned := modulePathFromURL(url); cleaned != "" {
					return cleaned
				}
			}
		}
	}
	return strings.TrimSpace(text)
}

// modulePathFromURL recovers a Go module path from a repository URL such as
// https://github.com/docker/docker/client → github.com/docker/docker/client.
func modulePathFromURL(url string) string {
	url = strings.TrimSpace(url)
	url = strings.TrimPrefix(url, "https://")
	url = strings.TrimPrefix(url, "http://")
	url = strings.TrimSuffix(url, "/")
	if url == "" {
		return ""
	}
	return url
}

// isClippedFragment reports whether a token is a visually-clipped package
// fragment (e.g. "g...", "github.com/do...") rather than a usable module path.
func isClippedFragment(tok string) bool {
	if strings.Contains(tok, "...") {
		return true
	}
	// A path that ends mid-segment with no final element is also clipped.
	if strings.HasSuffix(tok, "/") {
		return true
	}
	return false
}

// isWellFormedModulePath reports whether tok looks like a usable Go module path:
// it must contain a dot (domain) and either a slash or a known module host
// prefix. Clipped fragments are explicitly rejected so the caller can fall back.
func isWellFormedModulePath(tok string) bool {
	tok = strings.TrimSpace(tok)
	if tok == "" || isClippedFragment(tok) {
		return false
	}
	return strings.Contains(tok, ".") &&
		(strings.Contains(tok, "/") ||
			strings.HasPrefix(tok, "github.com") ||
			strings.HasPrefix(tok, "golang.org"))
}

// truncateForLog caps a model response excerpt so error messages stay readable.
// Uses rune-aware slicing to avoid splitting multi-byte UTF-8 characters.
func truncateForLog(s string) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) > 200 {
		return string(runes[:200]) + "..."
	}
	return s
}

// ProcessPlan generates an execution plan by dispatching to the AI provider
// with strict JSON output enforcement. When the objective indicates a
// direct file mutation, bypasses the Senior Architect prompt and uses
// the zero-prose direct mutation prompt instead.
func (e *Engine) ProcessPlan(ctx context.Context, modelName string, objective string, contextStr string) error {
	if e == nil || (e.provider == nil && e.streamProv == nil) {
		return nil
	}

	isDirectMut := detectDirectMutation(objective, "") != nil
	maxTokens, constrained := e.synthesisBudget(modelName, planSynthesisRequestedMaxTokens)
	archetype := protocol.Archetype(e.archetype)
	if e.vanillaWeb {
		archetype = protocol.ArchetypeVanillaWeb
	}
	descriptor, err := e.synthesisDescriptor(modelName, archetype, maxTokens, 0, false)
	if err != nil {
		return err
	}
	if descriptor.MaxOutputTokens > 0 && (maxTokens <= 0 || maxTokens > descriptor.MaxOutputTokens) {
		maxTokens = descriptor.MaxOutputTokens
	}
	if constrained {
		descriptor.ConstrainedModel = true
		descriptor.PromptProfile = protocol.PromptProfileCompact
		normalizedDescriptor, normalizeErr := descriptor.Normalize()
		if normalizeErr != nil {
			return normalizeErr
		}
		descriptor = normalizedDescriptor
		e.rememberContract(descriptor)
	}

	systemPrompt := prompt.PlanSynthesisSystemPromptForTier(prompt.ResolveTierForModel(modelName, ""))
	if descriptor.IsCompact() {
		systemPrompt = descriptor.PlanInstructions(prompt.PlanSynthesisSchema())
	}
	if isDirectMut {
		systemPrompt = prompt.PlanDirectMutationSystemPrompt()
		if descriptor.Archetype == protocol.ArchetypeVanillaWeb {
			systemPrompt += "\n\n[ARCHETYPE CONTEXT]\nWorkspace VANILLA_WEB: use FILE_MUTATE for existing .html, .css, or .js files."
		}
	}
	req := ai.Request{
		Model: modelName,
		Messages: []ai.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: prompt.BuildPlanPrompt(objective, contextStr, isDirectMut, "")},
		},
		Stream:              false,
		MaxTokens:           maxTokens,
		InteractionContract: descriptor.Contract,
		Contract:            &descriptor,
		ResponseFormat:      &ai.ResponseFormat{Type: "json_object"},
	}

	resp, err := e.complete(ctx, req)
	if err != nil {
		if ai.IsOutputTruncated(err) {
			return fmt.Errorf("plan engine: provider response was truncated before structural parsing: %w", err)
		}
		return err
	}
	if resp == nil {
		return fmt.Errorf("plan engine: provider returned a nil response")
	}
	if resp.Truncated || isTruncatedFinish(resp.FinishReason) {
		return fmt.Errorf("plan engine: provider response was truncated before structural parsing: %w", ai.NewOutputTruncated(modelName, resp.FinishReason))
	}
	if err := ValidateContractOutput(resp.Content, descriptor); err != nil {
		return err
	}

	return e.store.SaveRawMarkdown("plan", resp.Content) //nolint:contextcheck // substrate wrapper manages its own context
}

// Parse parses plan content (JSON or markdown) into tasks.
func (e *Engine) Parse(content string) []Task {
	return e.parser(content)
}

// ParseJSON parses JSON plan content specifically.
func (e *Engine) ParseJSON(content string) (*PlanOutput, error) {
	result := ParseJSONPlan(content)
	if !result.Valid {
		return nil, &PlanSchemaError{Message: result.Error}
	}
	return result.Plan, nil
}

// Store returns the underlying PlanStore for direct access.
func (e *Engine) Store() *PlanStore {
	return e.store
}

// TickTask marks the N-th task as complete in the current plan file.
func (e *Engine) TickTask(stepNum int) error {
	return e.store.TickTaskHoanThanh(stepNum)
}

// directMutationVerbs are verbs/phrases that signal an intent to
// perform a simple file replacement or format conversion rather than
// a diagnosis or investigation. Order matters: longer phrases first.
var directMutationVerbs = []string{
	"refactor", "change", "convert", "replace", "update", "modify",
	"reformat", "transform", "switch", "migrate", "change to",
}

// directMutationFilePattern matches prompts that reference a specific
// file and want to change its content or format (e.g. "refactor MIT LICENSE to APACHE").
var directMutationFilePattern = regexp.MustCompile(`(?i)(license|readme|dockerfile|makefile|\.env|\.gitignore)\b`)

// detectDirectMutation inspects the problem description and ledger
// content to determine whether this is a simple file mutation that
// should bypass the investigation/LLM synthesis pipeline entirely.
// Returns a deterministic hardcoded FILE_MUTATE task when the input
// qualifies, or nil when normal processing should continue.
func detectDirectMutation(problem string, ledgerContent string) *Task {
	combined := strings.ToLower(strings.TrimSpace(problem) + " " + strings.TrimSpace(ledgerContent))
	if combined == "" {
		return nil
	}

	hasVerb := false
	for _, v := range directMutationVerbs {
		if strings.Contains(combined, v) {
			hasVerb = true
			break
		}
	}
	if !hasVerb {
		return nil
	}

	if !directMutationFilePattern.MatchString(combined) {
		return nil
	}

	targetFile := extractMutationTarget(combined)
	if targetFile == "" {
		targetFile = "LICENSE"
	}

	sourceFormat, targetFormat := extractFormatChange(combined)

	solution := fmt.Sprintf("File %s mutated successfully.", targetFile)
	if sourceFormat != "" && targetFormat != "" {
		solution = fmt.Sprintf("Converted %s from %s to %s in %s.", targetFile, sourceFormat, targetFormat, targetFile)
	}

	return &Task{
		StepNum: 1,

		Status:      "idle",
		Type:        "FILE_MUTATE",
		Target:      targetFile,
		Description: fmt.Sprintf("Refactor %s: %s%s", targetFile, sourceFormat, targetFormat),
		Rationale:   "Direct file mutation detected — bypass investigation and LLM synthesis.",
		Solution:    solution,
		IsHardcoded: true,
	}
}

// extractMutationTarget finds the target filename from a mutation prompt string.
func extractMutationTarget(lower string) string {
	if strings.Contains(lower, "license") {
		return "LICENSE"
	}
	if strings.Contains(lower, "readme") {
		return "README.md"
	}
	if strings.Contains(lower, "dockerfile") {
		return "Dockerfile"
	}
	if strings.Contains(lower, "makefile") {
		return "Makefile"
	}
	if strings.Contains(lower, ".env") {
		return ".env"
	}
	if strings.Contains(lower, ".gitignore") {
		return ".gitignore"
	}
	return "LICENSE"
}

// extractFormatChange tries to identify the source and target formats
// from a mutation prompt (e.g. "MIT" → "APACHE_2.0").
func extractFormatChange(lower string) (sourceFormat, targetFormat string) {
	formatPatterns := []struct {
		source string
		target string
	}{
		{"mit", "apache_2.0"},
		{"apache", "mit"},
		{"gpl", "mit"},
		{"mit", "gpl"},
		{"bsd", "apache_2.0"},
		{"apache", "bsd"},
	}
	for _, fp := range formatPatterns {
		if strings.Contains(lower, fp.source) {
			sourceFormat = strings.ToUpper(fp.source)
			targetFormat = strings.ToUpper(fp.target)
			return sourceFormat, targetFormat
		}
	}
	return "", ""
}

// PlanSchemaError indicates a plan output schema violation.
type PlanSchemaError struct {
	Message string
	Cause   error
}

func (e *PlanSchemaError) Error() string {
	return "plan output schema violation: " + e.Message
}

func (e *PlanSchemaError) Unwrap() error {
	if e != nil && e.Cause != nil {
		return e.Cause
	}
	return ErrContractSchema
}

// softFallbackShellTasks scans prose/explanation text for standard runnable
// shell commands (e.g. go test, npm test, git status) when strict regex produced
// no SHELL_EXEC tasks.
func softFallbackShellTasks(content string) []Task {
	content = strings.ToLower(content)
	tasks := make([]Task, 0)
	commands := []string{"go test", "npm test", "git status", "go build", "npm run test", "git log"}
	for _, cmd := range commands {
		if strings.Contains(content, cmd) {
			tasks = append(tasks, Task{
				StepNum:     len(tasks) + 1,
				Status:      "idle",
				Type:        "SHELL_EXEC",
				Target:      cmd,
				Description: fmt.Sprintf("Soft-fallback shell task detected from prose: %s", cmd),
				IsHardcoded: true,
			})
		}
	}
	return tasks
}

// cleanLLMResponse strips markdown code fences and extracts the first JSON
// object boundary from a possibly raw/prose LLM response.
func cleanLLMResponse(content string) string {
	content = strings.TrimSpace(content)
	// Strip markdown fences (```json ... ``` or ``` ... ```)
	content = stripJSONCodeFence(content)
	// Extract first '{' to last '}' boundary.
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start >= 0 && end > start {
		content = content[start : end+1]
	}
	return strings.TrimSpace(content)
}
