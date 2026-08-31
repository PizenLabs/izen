// Package compose is the Application-layer composition helper (RFC v1.0
// section 2). It is the SINGLE composition root of the system: it wires the
// domain WorkflowRuntime, the projection LedgerBuilder, the EventTranslator,
// the thin Runtime facade, AND every engine, orchestrator, capability set and
// infrastructure adapter into one authoritative dependency tree
// (Application). No other package instantiates engines or re-wires the
// dependency graph; the presentation layer consumes the fully-wired
// Application directly.
//
// It is the only package that both imports the runtime package and its
// handlers (avoiding an import cycle); the composition root (cmd/izen) calls
// Wire and injects the resulting Application into the presentation layer.
package compose

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/contextcompiler"
	"github.com/PizenLabs/izen/internal/control"
	"github.com/PizenLabs/izen/internal/core/artifact"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/capability"
	coreRuntime "github.com/PizenLabs/izen/internal/core/runtime"
	coreWorkflow "github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/domain/policy"
	"github.com/PizenLabs/izen/internal/domain/ports"
	"github.com/PizenLabs/izen/internal/domain/workflow"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/events/audit"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/execution/preflight"
	"github.com/PizenLabs/izen/internal/git"
	"github.com/PizenLabs/izen/internal/knowledge"
	"github.com/PizenLabs/izen/internal/language"
	"github.com/PizenLabs/izen/internal/lea"
	"github.com/PizenLabs/izen/internal/loop"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/orchestrator"
	"github.com/PizenLabs/izen/internal/patch"
	"github.com/PizenLabs/izen/internal/prompt"
	"github.com/PizenLabs/izen/internal/providers"
	"github.com/PizenLabs/izen/internal/retrieval"
	"github.com/PizenLabs/izen/internal/runtime"
	runtimeAutonomy "github.com/PizenLabs/izen/internal/runtime/autonomy"
	"github.com/PizenLabs/izen/internal/runtime/handlers"
	"github.com/PizenLabs/izen/internal/session"
	compaction "github.com/PizenLabs/izen/internal/session/compaction"
	izentelemetry "github.com/PizenLabs/izen/internal/telemetry"
	wscap "github.com/PizenLabs/izen/internal/workspace/capability"
	wssnapshot "github.com/PizenLabs/izen/internal/workspace/snapshot"
	"github.com/PizenLabs/izen/pkg/engine/layer1"
	"github.com/PizenLabs/izen/pkg/engine/layer3"
	"github.com/PizenLabs/izen/pkg/engine/pipeline"
	"github.com/PizenLabs/izen/pkg/engine/telemetry"
)

// Capabilities bundles the Infrastructure adapters as Domain ports. The
// composition root instantiates the concrete adapters (OSFile, ExecShell,
// GitCLI, PatchAdapter) and injects them here so the Application layer never
// depends on concrete infrastructure.
type Capabilities struct {
	File  ports.FilePort
	Shell ports.ShellPort
	Git   ports.GitPort
	Patch ports.PatchPort
}

// RuntimeInputs carries the process-level inputs the engine tree is wired
// from — the workspace root, the resolved config, the process session, the AI
// provider manager, the display username, and the detected primary language.
// These are read-only after Wire returns.
type RuntimeInputs struct {
	// Root is the workspace root directory every engine operates on.
	Root string
	// Config is the resolved global configuration. When nil, config.Default()
	// is used.
	Config *config.Config
	// Session is the process conversation session. When nil, session.Load()
	// is called (falling back to a fresh session).
	Session *session.Session
	// Manager is the AI provider manager. When nil, it is built from Config.
	Manager *ai.Manager
	// Username is the display username attributed to plans.
	Username string
	// LanguageID is the detected primary language, used to select the
	// language-aware execution verifier.
	LanguageID language.ID
}

// Application is the fully wired Application layer of the system: the domain
// WorkflowRuntime, the ContextLedger projection, the LedgerBuilder, the
// append-only event audit logger, the Runtime facade with every command
// handler registered, and the complete engine tree (layered pipeline, plan,
// execution, patch, authorization, intent router, orchestrator) on the shared
// event bus. It is the single authoritative dependency graph of the process.
type Application struct {
	Bus      *events.Bus
	Workflow workflow.WorkflowRuntime
	Ledger   *runtime.ContextLedger
	Builder  *runtime.LedgerBuilder
	Runtime  *runtime.Runtime
	Audit    *audit.AuditLogger

	// auditDir is the workspace-relative audit log directory wired via
	// WithAuditDir. Empty disables auditing.
	auditDir string

	// Approver resolves patch approvals for the approval command handlers.
	// Defaults to handlers.NewInMemoryApprover when not supplied.
	Approver handlers.PatchApprover
	// Capabilities carries the injected Infrastructure adapters (read-only
	// record for the composition root; not consumed by handlers).
	Capabilities Capabilities

	// Inputs carries the process-level inputs the engine tree was wired from.
	// Read-only after Wire returns.
	Inputs RuntimeInputs

	// ── Engine tree (Sprint 3) ────────────────────────────────────────
	// Every engine below is constructed here, in Wire, and shares the single
	// event bus (Bus). The presentation layer consumes them read-only.
	RuntimeCtx   *coreRuntime.RuntimeContext
	WorkflowSM   *coreWorkflow.WorkflowStateMachine
	Orchestrator *orchestrator.Orchestrator
	Pipeline     *pipeline.Engine
	PlanStore    *plan.PlanStore
	PlanEngine   *plan.Engine
	Execution    *execution.Engine
	// Executor is the RuntimeExecutor boundary: the single execution authority
	// that owns provider invocation, patch creation, the mutation lifecycle and
	// verification on the migrated paths. The UI submits ExecuteRequests and
	// approves/rejects via this boundary — it never calls a provider or a
	// PatchManager directly on those paths.
	Executor *execution.RuntimeExecutor
	// Sessions is the dual-slot session authority (crash-resilient
	// active-pointer + two-tier locking + INV-SESSION-14 recovery). It is the
	// single owner of the process session record; Inputs.Session mirrors its
	// active session. Wired whenever a workspace root is provided.
	Sessions *session.Manager
	// ── Phase 2 decoupled context subsystems ───────────────────────────
	// Compaction is the async Session Compaction authority: it runs on a
	// background goroutine (never the main execution loop) and sinks
	// generational compact contexts into the session manager.
	Compaction *compaction.Runner
	// Knowledge is the granular Project Knowledge store (INV-SESSION-15:
	// independently addressable chunks, never a monolithic project summary).
	Knowledge *knowledge.Store
	// Promotion is the Knowledge Lifecycle engine (candidate → validated →
	// evaluated → promoted → deprecated/tombstoned).
	Promotion *knowledge.PromotionEngine
	// Compiler is the Context Compilation runtime engine: it decides what
	// context is injected into the LLM prompt under a strict token budget and
	// re-compiles only when underlying state mutates.
	Compiler *contextcompiler.Compiler
	// Gateway is the unified IntentGateway: the single entry point every user
	// action (bare text, $prompt, $hot) crosses to produce an ExecuteRequest
	// with an unconditional Strategy.Select profile. The UI never routes by
	// mode or triggers hidden executions on the migrated paths.
	Gateway        *execution.IntentGateway
	Patch          *patch.Engine
	Auth           *authorization.AuthorizationEngine
	Microkernel    *plan.MicrokernelPlanner
	IntentCompiler *plan.IntentCompilerPlanner
	Git            *git.Engine
	Lea            *lea.Engine
	Caps           *capability.CapabilitySet
	Budget         *budget.MutationBudget
	MicroBudget    *budget.MicroBudget
	Policy         *policy.PolicyEngine
	Artifacts      *artifact.Store
	SnapCache      *wssnapshot.SnapshotCache
	CapRegistry    *wscap.ArchetypeCapabilityRegistry

	// ── Autonomy (decision runtime) ───────────────────────────────────
	// The human-authorized autonomous runtime sits ABOVE the execution modes:
	// it classifies intent independently from workspace selection, evaluates
	// the autonomy decision model (auto_continue / ask_user / block / direct),
	// records session capability grants, and drives the autonomous loop. Every
	// decision is published as a canonical event on the shared bus.
	Autonomy *autonomy.Engine

	// ── Autonomous runtime loop (Phase 5) ──────────────────────────────
	// Autonomous is the real bounded loop: it drives Observe → Decide →
	// Execute → Interpret over the single RuntimeExecutor authority (through
	// the composition-boundary adapter) and publishes every transition as a
	// canonical loop.transition event. It is a consumer of the executor — never
	// a second execution authority. Headless runs and the UI both consume it.
	Autonomous *runtimeAutonomy.Driver

	// provider is the resolved default AI provider from Manager. It is nil
	// when no provider is configured.
	provider ai.Provider

	// telemetryAdapter bridges the layered pipeline's telemetry bus onto the
	// unified domain event bus. It lives for the Application lifetime and is
	// closed by Close.
	telemetryAdapter *telemetry.TelemetryAdapter
}

// Option configures the Application during wiring.
type Option func(*Application)

// WithBus overrides the shared domain event bus. A nil bus (or no option)
// creates a fresh one.
func WithBus(bus *events.Bus) Option {
	return func(a *Application) {
		if bus != nil {
			a.Bus = bus
		}
	}
}

// WithCapabilities injects the Infrastructure adapters as domain ports.
func WithCapabilities(caps Capabilities) Option {
	return func(a *Application) {
		a.Capabilities = caps
	}
}

// WithApprover injects a custom PatchApprover for the approval handlers.
func WithApprover(approver handlers.PatchApprover) Option {
	return func(a *Application) {
		if approver != nil {
			a.Approver = approver
		}
	}
}

// WithAuditDir wires an append-only event audit logger rooted at dir. Every
// events.Envelope published on the shared bus is appended to
// dir/events.ndjson. An empty dir disables auditing (the Application.Audit
// field stays nil).
func WithAuditDir(dir string) Option {
	return func(a *Application) {
		if dir != "" {
			a.auditDir = dir
		}
	}
}

// WithRuntimeInputs wires the process-level runtime inputs the engine tree is
// built from. Calling the finer-grained options after this one overrides the
// corresponding fields.
func WithRuntimeInputs(in RuntimeInputs) Option {
	return func(a *Application) {
		if in.Root != "" {
			a.Inputs.Root = in.Root
		}
		if in.Config != nil {
			a.Inputs.Config = in.Config
		}
		if in.Session != nil {
			a.Inputs.Session = in.Session
		}
		if in.Manager != nil {
			a.Inputs.Manager = in.Manager
		}
		if in.Username != "" {
			a.Inputs.Username = in.Username
		}
		if in.LanguageID != "" {
			a.Inputs.LanguageID = in.LanguageID
		}
	}
}

// WithRoot wires the workspace root directory.
func WithRoot(root string) Option {
	return func(a *Application) {
		a.Inputs.Root = root
	}
}

// WithConfig wires the resolved global configuration.
func WithConfig(cfg *config.Config) Option {
	return func(a *Application) {
		if cfg != nil {
			a.Inputs.Config = cfg
		}
	}
}

// WithSession wires an explicit process session (overrides session.Load()).
func WithSession(sess *session.Session) Option {
	return func(a *Application) {
		if sess != nil {
			a.Inputs.Session = sess
		}
	}
}

// WithManager wires an explicit AI provider manager (overrides building one
// from Config).
func WithManager(mgr *ai.Manager) Option {
	return func(a *Application) {
		if mgr != nil {
			a.Inputs.Manager = mgr
		}
	}
}

// WithProvider overrides the default provider resolved from Manager.
func WithProvider(p ai.Provider) Option {
	return func(a *Application) {
		a.provider = p
	}
}

// WithUsername wires the display username attributed to plans.
func WithUsername(name string) Option {
	return func(a *Application) {
		if name != "" {
			a.Inputs.Username = name
		}
	}
}

// WithLanguageID wires the detected primary language so the execution engine
// can select a language-aware verifier.
func WithLanguageID(id language.ID) Option {
	return func(a *Application) {
		a.Inputs.LanguageID = id
	}
}

// Session returns the process conversation session the engine tree was wired
// from. Never nil after Wire.
func (a *Application) Session() *session.Session {
	if a == nil {
		return nil
	}
	return a.Inputs.Session
}

// SessionManager returns the dual-slot session authority wired by Wire. It is
// nil only when no workspace root was provided (harness mode).
func (a *Application) SessionManager() *session.Manager {
	if a == nil {
		return nil
	}
	return a.Sessions
}

// CompactionRunner returns the async Session Compaction runner (nil when no
// workspace root is wired). It runs on a background goroutine; Submit never
// blocks the main loop.
func (a *Application) CompactionRunner() *compaction.Runner {
	if a == nil {
		return nil
	}
	return a.Compaction
}

// KnowledgeStore returns the granular Project Knowledge store (INV-SESSION-15).
func (a *Application) KnowledgeStore() *knowledge.Store {
	if a == nil {
		return nil
	}
	return a.Knowledge
}

// PromotionEngine returns the Knowledge Lifecycle engine.
func (a *Application) PromotionEngine() *knowledge.PromotionEngine {
	if a == nil {
		return nil
	}
	return a.Promotion
}

// ContextCompiler returns the Context Compilation runtime engine.
func (a *Application) ContextCompiler() *contextcompiler.Compiler {
	if a == nil {
		return nil
	}
	return a.Compiler
}

// Manager returns the AI provider manager the engine tree was wired from.
// Never nil after Wire.
func (a *Application) Manager() *ai.Manager {
	if a == nil {
		return nil
	}
	return a.Inputs.Manager
}

// Provider returns the resolved default AI provider, or nil when no provider
// is configured.
func (a *Application) Provider() ai.Provider {
	if a == nil {
		return nil
	}
	return a.provider
}

// LanguageID returns the detected primary language the execution engine was
// wired with.
func (a *Application) LanguageID() language.ID {
	if a == nil {
		return ""
	}
	return a.Inputs.LanguageID
}

// Wire builds the Application: domain runtime, dispatcher, handlers, ledger
// projection, the Runtime facade, and the complete engine tree — all bound to
// the shared event bus. It is the sole place the application dependency graph
// is assembled.
func Wire(opts ...Option) (*Application, error) {
	a := &Application{}
	for _, opt := range opts {
		opt(a)
	}
	if a.Bus == nil {
		a.Bus = events.NewBus(events.DefaultBufferSize)
	}
	if a.Approver == nil {
		// No in-memory approval default: production approval is routed through
		// the RuntimeExecutor (a.Executor), which applies real mutations. The
		// injected approver is a harness/tests seam only.
		a.Approver = nil
	}
	if a.Inputs.Config == nil {
		a.Inputs.Config = config.Default()
	}

	// ── PROCESS BOOTSTRAP: session + AI provider manager ──────────────
	// When a workspace root is wired, the dual-slot SessionManager is the
	// single session authority: it recovers the active pointer (crash
	// protocol), loads the active session through the INV-SESSION-14 ladder,
	// and mirrors it into Inputs.Session so the whole engine tree and the
	// presentation layer share the manager's canonical record. The boundary
	// hook drains the RuntimeExecutor at every session switch — execution
	// state never leaves that single authority. Without a root (harness
	// mode), the legacy single-file session path is kept.
	if a.Inputs.Session == nil {
		if a.Inputs.Root != "" {
			sm := session.NewManager(a.Inputs.Root,
				session.WithWorkspaceGuard(newGitWorkspaceGuard(func() *git.Engine { return a.Git })),
				session.WithBoundaryHook(session.BoundaryHookFunc(func(ctx context.Context, prev, next session.SlotID) error {
					if a.Executor == nil {
						return nil
					}
					errs := a.Executor.RejectAllPending(ctx, "session boundary: active "+string(prev)+" -> "+string(next))
					if len(errs) > 0 {
						return fmt.Errorf("session boundary drain: %w", errs[0])
					}
					return nil
				})))
			if err := sm.Open(context.Background()); err != nil {
				return nil, fmt.Errorf("wire: open session manager: %w", err)
			}
			a.Sessions = sm
			a.Inputs.Session = sm.Session()
		} else {
			sess, err := session.Load()
			if err != nil {
				sess = session.New()
			}
			a.Inputs.Session = sess
		}
	}
	if a.Inputs.Manager == nil {
		mgr := ai.NewManager()
		registerProviders(a.Inputs.Config, mgr)
		mgr.SetDefault(a.Inputs.Config.ActiveProviderName())
		a.Inputs.Manager = mgr
	}
	if a.Inputs.Username == "" {
		a.Inputs.Username = resolveUsername()
	}

	// ── PHASE 2 DECOUPLED CONTEXT SUBSYSTEMS ───────────────────────────
	// Three independent authorities with absolute separation:
	//   Compaction  — single-session continuity (async, non-blocking).
	//   Knowledge   — durable cross-session knowledge (granular, INV-SESSION-15).
	//   Compiler    — LLM prompt context under a strict token budget.
	// Each is wired independently; none shares state with the others or with
	// the RuntimeExecutor execution engine.
	if a.Sessions != nil {
		runner := compaction.NewRunner(compaction.DefaultPolicy(),
			func(ctx context.Context, j compaction.Job, cc *session.CompactContext) error {
				if a.Sessions == nil {
					return nil
				}
				return a.Sessions.SetCompactContext(ctx, j.Slot, cc)
			})
		runner.Start()
		a.Compaction = runner
	}
	a.Knowledge = knowledge.NewStore(a.Inputs.Root)
	a.Promotion = knowledge.NewPromotionEngine(a.Knowledge, knowledge.DefaultPolicy())
	a.Compiler = contextcompiler.New()
	if a.provider == nil {
		if def, ok := a.Inputs.Manager.Default(); ok {
			a.provider = def
		}
	}

	// ── RETRIEVAL GLOBAL ROUTER ───────────────────────────────────────
	// The search router auto-detects lx in PATH and is registered globally so
	// the context planner can govern file reads. Skipped when no workspace
	// root is wired (harnesses/tests).
	if a.Inputs.Root != "" {
		retrieval.SetGlobalRouter(retrieval.NewRouter(a.Inputs.Root, func(format string, args ...interface{}) {
			fmt.Fprintf(os.Stderr, format+"\n", args...)
		}))
	}

	// ── APPEND-ONLY EVENT AUDIT ────────────────────────────────────────
	// The audit logger subscribes to the shared bus and persists every
	// events.Envelope (bridged telemetry, canonical signals) to
	// <auditDir>/events.ndjson asynchronously. It is a pure projection:
	// non-blocking end to end, so disk I/O never stalls the pipeline or the
	// TUI.
	if a.auditDir != "" {
		logger, err := audit.NewLogger(a.auditDir, a.Bus)
		if err != nil {
			return nil, fmt.Errorf("wire: audit logger: %w", err)
		}
		// INV-SESSION-10: stamp every persisted audit record with the active
		// session id so mutation traces, token usage and tool invocations map
		// strictly to their originating session.
		logger.SetSessionResolver(func() string {
			if a.Sessions != nil {
				return a.Sessions.ActiveSessionID()
			}
			return ""
		})
		if err := logger.Start(); err != nil {
			return nil, fmt.Errorf("wire: start audit logger: %w", err)
		}
		a.Audit = logger
	}

	wf := workflow.NewWorkflowRuntime()
	a.Workflow = wf

	// ── RUNTIME EXECUTOR (authority boundary) ───────────────────────────
	// The RuntimeExecutor is the execution authority the presentation layer
	// submits requests to on the migrated paths ($prompt targeted mutation,
	// $hot). It owns provider invocation, patch creation, the mutation
	// lifecycle and verification, and emits the canonical execution lifecycle
	// events onto the shared bus. The approval command handlers route through
	// it, so approving a patch applies a REAL mutation.
	a.Executor = execution.NewRuntimeExecutor(a.Inputs.Root, a.Inputs.Config, a.provider, a.Bus, a.Inputs.LanguageID)
	a.Gateway = execution.NewIntentGateway(a.Inputs.Root)
	// INV-SESSION-10: resolve the originating session for every execution at
	// admission so the proof, terminal evidence and lifecycle events correlate
	// with the active session — including autonomous/headless submissions.
	if a.Sessions != nil {
		a.Executor.SetSessionResolver(func() string {
			return a.Sessions.ActiveSessionID()
		})
	}

	// ── BACKGROUND PREFLIGHT + SYNC BARRIER (prompt admission invariant) ──
	// Decouple structural discovery and budget estimation from the UI critical
	// path: submit_prompt emits PromptAdmitted in <10ms, BackgroundPreflight runs
	// off the UI goroutine, and the autonomous loop gates observing→deciding on
	// the PreflightSyncBarrier (10s timeout → PREFLIGHT_TIMEOUT).
	preflightState := preflight.NewObservationState()
	loopBarrier := loop.NewBarrier(a.Bus)
	preflightWorker := preflight.New(preflight.Config{
		Root:     a.Inputs.Root,
		Bus:      a.Bus,
		State:    preflightState,
		Barrier:  loopBarrier.Inner(),
		Recorder: izentelemetry.Default(),
	})

	dispatcher := runtime.NewCommandDispatcher()
	hs := handlers.New(handlers.HandlerDeps{
		Workflow:        wf,
		Bus:             a.Bus,
		Approver:        a.Approver,
		Executor:        a.Executor,
		PreflightWorker: preflightWorker,
		Telemetry:       izentelemetry.Default(),
	})
	if err := hs.Register(dispatcher); err != nil {
		return nil, err
	}

	builder := runtime.NewLedgerBuilder(a.Bus)
	builder.Start()
	a.Builder = builder
	a.Ledger = builder.Ledger()

	a.Runtime = runtime.NewRuntime(dispatcher, runtime.WithEventBus(a.Bus))

	// ── ENGINE TREE ───────────────────────────────────────────────────
	// Every engine below is constructed here and bound to the single shared
	// event bus (a.Bus), so projections consume one canonical stream.
	root := a.Inputs.Root
	cfg := a.Inputs.Config
	sess := a.Inputs.Session
	provider := a.provider

	a.Git = git.NewEngine(root)
	a.Lea = lea.NewEngine(root)

	// ── LAYERED PIPELINE ENGINE (LAYERS 0-5) ─────────────────────────────
	// The central Pipeline Engine wires the foundation layers onto the
	// orchestrator: Layer 0 knowledge resolution, Layer 1 capability
	// detection, Layer 2 governed context, Layer 3 policy guard + stateless
	// worker, Layer 4 validation DAG and Layer 5 telemetry. Its intent-based
	// model router picks the model tier per mode (/plan and /investigate
	// route to reasoning models under strict budgets; /build routes to fast
	// coding models; /ask routes to a minimal read-only policy).
	pipeRouter := pipeline.NewRouter(
		pipeline.WithModel(pipeline.IntentReasoning, cfg.ResolveTierModel("reasoning")),
		pipeline.WithModel(pipeline.IntentExecution, cfg.ResolveTierModel("execution")),
		pipeline.WithModel(pipeline.IntentInformational, cfg.ResolveTierModel("informational")),
		pipeline.WithProvider(pipeline.IntentReasoning, cfg.ResolveTierProvider("reasoning")),
		pipeline.WithProvider(pipeline.IntentExecution, cfg.ResolveTierProvider("execution")),
		pipeline.WithProvider(pipeline.IntentInformational, cfg.ResolveTierProvider("informational")),
		pipeline.WithFallbackModel(cfg.ResolveTierModel("execution")),
	)
	// Layer 5 telemetry is observed on a dedicated telemetry EventBus; a
	// TelemetryAdapter bridges every event onto the unified domain event bus
	// as standardized events.Envelope values so projections consume one
	// stream.
	telemetryBus := telemetry.NewEventBus(telemetry.DefaultBufferSize)
	pipeOpts := []pipeline.Option{
		pipeline.WithEventBus(telemetryBus),
		pipeline.WithRouter(pipeRouter),
	}
	if provider != nil {
		pipeOpts = append(pipeOpts, pipeline.WithClient(pipelineClient(provider)))
	}
	a.Pipeline = pipeline.NewEngine(root, a.Lea, pipeOpts...)
	a.telemetryAdapter = telemetry.NewTelemetryAdapter(telemetryBus, a.Bus, "telemetry.pipeline")
	_ = a.telemetryAdapter.Start()

	// Inject the Layer 1 workspace-capability header into every composed LLM
	// system prompt so the model is told exactly which toolchain commands
	// exist (and which do not), eliminating tech-stack hallucinations. The
	// detection is a single cheap directory scan; a failure simply leaves the
	// header empty and the composed prompts unchanged. The detected graph is
	// kept for the PolicyEngine's physical-facts surface below.
	var wsGraph *layer1.Graph
	if g, derr := layer1.Detect(root); derr == nil {
		wsGraph = g
		prompt.SetWorkspaceCapabilities(pipeline.CapabilityHeader(g))
	}

	a.PlanStore = plan.NewPlanStore()
	a.PlanEngine = plan.NewEngine(a.PlanStore)
	a.PlanEngine.SetUserName(a.Inputs.Username)
	if provider != nil {
		a.PlanEngine.SetProvider(provider.Execute)
		a.PlanEngine.SetStreamProvider(provider.ExecuteStream)
	}

	if a.Inputs.LanguageID != "" {
		a.Execution = execution.NewEngine(root, cfg, sess, a.Inputs.LanguageID)
	} else {
		a.Execution = execution.NewEngine(root, cfg, sess)
	}
	a.Execution.SetPlanStore(a.PlanStore)

	// ── CONTROL PLANE: capability set, artifact store, mutation budget ──
	a.Caps = capability.NewCapabilitySet()
	a.Caps.Grant(capability.CapabilityRead)
	a.Caps.Grant(capability.CapabilityWrite)
	a.Caps.Grant(capability.CapabilityExecute)
	a.Caps.Grant(capability.CapabilityTest)
	a.Caps.Grant(capability.CapabilityPatch)
	a.Caps.Grant(capability.CapabilityCheckpoint)
	a.Caps.Grant(capability.CapabilityRollback)
	a.Artifacts = artifact.NewStore(root)
	a.Budget = budget.NewBudget(
		100,            // max files
		5000,           // max diff lines
		1_000_000,      // max tokens
		10,             // max attempts
		30*time.Second, // max duration per step
		5,              // max concurrent commands
	)
	mb := budget.DefaultMicroBudget()
	a.MicroBudget = &mb
	a.SnapCache = wssnapshot.NewSnapshotCache()
	a.CapRegistry = wscap.NewArchetypeCapabilityRegistry()
	if root != "" {
		_, _ = a.SnapCache.GetSnapshot(root) // prime the cache
	}
	a.RuntimeCtx = coreRuntime.NewWithSnapRegistry(
		a.Artifacts, a.Caps, a.Budget, a.SnapCache, a.CapRegistry,
	)

	// Wire snapshot cache and capability registry into the plan engine for
	// archetype-aware diagnostic gating, and the event bus so /plan runs
	// headless and publishes domain events. The layered Pipeline Engine is
	// injected as the pipeline.Facade so the /plan UseCase can delegate its
	// generative synthesis to the Layer 0-5 pipeline when no direct provider
	// is wired.
	a.PlanEngine.WithSnapshotCache(a.SnapCache).WithCapabilityRegistry(a.CapRegistry)
	a.PlanEngine.WithEventBus(a.Bus)
	a.PlanEngine.WithPipelineFacade(a.Pipeline)

	// ── WORKFLOW STATE MACHINE + CHECKPOINT COORDINATOR ────────────────
	a.WorkflowSM = coreWorkflow.NewWorkflowStateMachine()
	wcc := control.NewWorkflowCheckpointManager(a.Execution.Checkpoints, root)
	a.WorkflowSM.WithCheckpointCoordinator(coreWorkflow.NewCheckpointCoordinator(wcc))

	// ── EXECUTION ORCHESTRATOR ───────────────────────────────────────────
	// The orchestrator maps the logical execution phases onto the single
	// WorkflowStateMachine while sharing the persistent RuntimeContext. Mode
	// switches update the active phase dynamically WITHOUT resetting
	// conversation history or workspace artifacts. Phase changes are observed
	// via EventPhaseChanged.
	a.Orchestrator = orchestrator.New(a.WorkflowSM, a.RuntimeCtx).WithEventBus(a.Bus).WithPipeline(a.Pipeline)

	// ── AUTONOMY DECISION RUNTIME ────────────────────────────────────────
	// The autonomy engine is the decision layer above the modes. Its scope is
	// the workspace root (falling back to "repository" when no root is wired,
	// e.g. in harnesses). The mutation risk probe is bound to the execution
	// engine's RiskClassifier so the decision model sees the same risk surface
	// the mutation guards enforce; rollback availability is probed against the
	// checkpoint manager. Both probes degrade gracefully to safe defaults.
	scope := "repository"
	if root != "" {
		scope = root
	}
	autoOpts := []autonomy.Option{
		autonomy.WithEventBus(a.Bus),
		autonomy.WithScope(scope),
	}
	if a.Execution != nil && a.Execution.Risk != nil {
		autoOpts = append(autoOpts, autonomy.WithRiskFunc(func(target string) autonomy.MutationRiskInput {
			res := a.Execution.Risk.ClassifyFileOp(target, true)
			return autonomy.MutationRiskInput{Level: autonomy.ParseRisk(res.Label)}
		}))
	}
	if a.Execution != nil && a.Execution.Checkpoints != nil {
		autoOpts = append(autoOpts, autonomy.WithRollbackFunc(func() bool {
			return len(a.Execution.Checkpoints.List()) > 0
		}))
	}
	a.Autonomy = autonomy.NewEngine(autoOpts...)

	// ── AUTONOMOUS RUNTIME LOOP (Phase 5) ───────────────────────────────
	// The real bounded loop is bound to the RuntimeExecutor through the
	// composition-boundary adapter. The loop is a consumer of the executor:
	// it resolves targets via the IntentGateway, submits ExecuteRequests
	// through the RuntimeExecutor, forwards approvals via Approve/Reject, and
	// publishes loop.transition transitions on the shared bus. The presentation
	// layer only projects those events.
	a.Autonomous = runtimeAutonomy.NewDriver(
		runtimeAutonomy.NewExecutorAdapter(root, a.Gateway, a.Executor),
		a.Bus,
		runtimeAutonomy.WithPreflightBarrier(loopBarrier),
		runtimeAutonomy.WithPreflightState(preflightState),
		// PASS 1 MANIFEST AUTO-HOOK: a preflight-infeasible target issues a
		// lightweight READ-ONLY manifest request before the DAG strategy is
		// determined, so the plan is scoped to the mutation surface and
		// unmodified sections are pruned (never a naive line slicer).
		runtimeAutonomy.WithManifestPass(runtimeAutonomy.ManifestPassForExecutor(a.Executor)),
	)
	// ── AUTONOMY BOUNDARY-TELEMETRY SINK ─────────────────────────────────
	// [boundary2]/[boundary5] diagnostic lines are routed onto the shared
	// event bus as engine.activity events instead of the standard logger:
	// raw stderr writes from background goroutines race Bubble Tea's
	// altscreen renderer and corrupt the live frame. The UI projects them
	// like every other domain event; zero autonomy bytes ever reach stdio.
	if a.Bus != nil {
		runtimeAutonomy.SetDiagnosticLog(func(format string, args ...interface{}) {
			a.Bus.Publish(events.NewActivity(fmt.Sprintf(format, args...)))
		})
	}

	// ── MULTI-TIER PATCH ENGINE ──────────────────────────────────────────
	// The new patch engine replaces the legacy patch application pipeline in
	// the /build flow: Tier 1 (structured diff) -> Tier 2 (SEARCH/REPLACE) ->
	// Tier 3 (whole-file rewrite) -> Tier 4 (human approval). It emits
	// PatchParsed/PatchValidated/PatchRejected/ApprovalRequested on the bus.
	a.Patch = patch.NewEngine().WithEventBus(a.Bus)

	// ── AUTHORIZATION ENGINE ───────────────────────────────────────────────
	// Production AuthorizationEngine wired with a no-op source hash verifier
	// and a checkpoint checker that inspects .izen/checkpoints/ on disk. The
	// unified PolicyEngine is bound to it, so every mutation that passes the
	// operational gates is still adjudicated by the single governance owner.
	a.Policy = policy.NewPolicyEngine(composedCapabilityGraph{ws: wsGraph, caps: a.Caps})
	a.Auth = authorization.NewProductionAuthorizationEngine(root, func() coreWorkflow.WorkflowState {
		return a.WorkflowSM.State()
	}).WithPolicyEngine(a.Policy)

	a.Microkernel = plan.NewMicrokernelPlanner(root)
	a.IntentCompiler = plan.NewIntentCompilerPlanner(root)

	return a, nil
}

// Close tears down the Application: it stops the audit logger, the ledger
// projection, the runtime presentation projection, the telemetry bridge and
// the async compaction runner. Idempotent.
func (a *Application) Close() {
	if a == nil {
		return
	}
	if a.Compaction != nil {
		a.Compaction.Close()
		a.Compaction = nil
	}
	if a.Audit != nil {
		_ = a.Audit.Close()
		a.Audit = nil
	}
	if a.telemetryAdapter != nil {
		a.telemetryAdapter.Stop()
		a.telemetryAdapter = nil
	}
	if a.Builder != nil {
		a.Builder.Close()
	}
	if a.Runtime != nil {
		a.Runtime.Close()
	}
}

// registerProviders registers every configured AI provider onto the manager.
func registerProviders(cfg *config.Config, mgr *ai.Manager) {
	if cfg == nil || mgr == nil {
		return
	}
	if provCfg, ok := cfg.AI.Providers["ollama"]; ok && provCfg.APIKey != "" {
		mgr.Register("ollama", providers.NewOllamaProvider(provCfg.BaseURL, provCfg.APIKey, provCfg.DefaultModel))
	}
	if provCfg, ok := cfg.AI.Providers["openrouter"]; ok && provCfg.APIKey != "" {
		mgr.Register("openrouter", providers.NewOpenRouterProvider(provCfg.APIKey, provCfg.DefaultModel, provCfg.BaseURL))
	}
	if provCfg, ok := cfg.AI.Providers["openai"]; ok && provCfg.APIKey != "" {
		mgr.Register("openai", providers.NewOpenAIProvider(provCfg.APIKey, provCfg.DefaultModel))
	}
	if provCfg, ok := cfg.AI.Providers["anthropic"]; ok && provCfg.APIKey != "" {
		mgr.Register("anthropic", providers.NewClaudeProvider(provCfg.APIKey, provCfg.DefaultModel))
	}
	if provCfg, ok := cfg.AI.Providers["gemini"]; ok && provCfg.APIKey != "" {
		mgr.Register("gemini", providers.NewGeminiProvider(provCfg.APIKey, provCfg.DefaultModel))
	}
	if provCfg, ok := cfg.AI.Providers["groq"]; ok && provCfg.APIKey != "" {
		mgr.Register("groq", providers.NewGroqProvider(provCfg.APIKey, provCfg.DefaultModel, provCfg.BaseURL))
	}
	if provCfg, ok := cfg.AI.Providers["opencode"]; ok && provCfg.APIKey != "" {
		mgr.Register("opencode", providers.NewOpenCodeProvider(provCfg.APIKey, provCfg.DefaultModel, provCfg.BaseURL))
	}
	if provCfg, ok := cfg.AI.Providers["9router"]; ok && provCfg.APIKey != "" {
		mgr.Register("9router", providers.NewNineRouterProvider(provCfg.APIKey, provCfg.DefaultModel, provCfg.BaseURL))
	}
}

// resolveUsername resolves the user's display name with the following
// precedence:
//  1. Environment variable ($USER).
//  2. OS user lookup (os/user.Current).
//  3. Fallback to "developer".
//
// The presentation layer may override this via WithUsername once the local
// config is available.
func resolveUsername() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if currentUser, err := user.Current(); err == nil && currentUser.Username != "" {
		return currentUser.Username
	}
	return "developer"
}

// pipelineClient adapts an ai.Provider onto the pipeline engine's stateless
// WorkerClient contract. The provider's rendered prompt is delivered as the
// user turn; model routing is handled by the pipeline router, not here.
func pipelineClient(p ai.Provider) *pipeline.FuncClient {
	if p == nil {
		return pipeline.NewFuncClient(nil)
	}
	return pipeline.NewFuncClient(func(ctx context.Context, provider, model, prompt string) (string, layer3.TokenUsage, error) {
		resp, err := p.Execute(ctx, ai.Request{
			Messages: []ai.Message{{Role: "user", Content: prompt}},
			Model:    model,
		})
		if err != nil {
			return "", layer3.TokenUsage{}, err
		}
		return resp.Content, layer3.TokenUsage{Input: resp.TokenInput, Output: resp.TokenOutput}, nil
	})
}

// gitWorkspaceGuard is the WorkspaceGuard adapter for the composition root. It
// reports the workspace-relative paths with uncommitted changes (git status)
// that a session switch must surface in the target session's Context Compiler
// view — the Session Manager never executes git itself (INV-SESSION-09).
// resolve is called lazily because the Git engine is wired AFTER the session
// manager in Wire; by switch time it is always set.
type gitWorkspaceGuard struct {
	resolve func() *git.Engine
}

func newGitWorkspaceGuard(resolve func() *git.Engine) *gitWorkspaceGuard {
	return &gitWorkspaceGuard{resolve: resolve}
}

// DirtyFiles implements session.WorkspaceGuard. Only the workspace's own
// changes are reported; .izen/ internal state is never surfaced as workspace
// dirt. A missing/erroring git repo degrades to "no dirty files" so a session
// switch can never be blocked by git absence.
func (g *gitWorkspaceGuard) DirtyFiles(_ context.Context) ([]string, error) {
	if g == nil || g.resolve == nil {
		return nil, nil
	}
	eng := g.resolve()
	if eng == nil {
		return nil, nil
	}
	// A non-git workspace carries no uncommitted changes to guard against.
	if !eng.IsRepo() {
		return nil, nil
	}
	entries, err := eng.Status() //nolint:contextcheck // git.Engine.Status has no ctx parameter; the guard ctx is a switch-lifecycle signal
	if err != nil {
		return nil, err
	}
	var dirty []string
	for _, e := range entries {
		p := e.Path
		if p == "" {
			continue
		}
		if p == ".izen" || strings.HasPrefix(p, ".izen/") {
			continue
		}
		dirty = append(dirty, p)
	}
	return dirty, nil
}

// composedCapabilityGraph is the PolicyEngine's physical-facts surface: the
// Workspace Capability Graph (layer1) provides the tool capabilities, the
// runtime CapabilitySet provides the mutation/execute scope. It answers ONLY
// "what does the system possess?" — it carries zero Allow/Deny or mode
// vocabulary. The PolicyEngine derives every governance verdict from it.
type composedCapabilityGraph struct {
	ws   *layer1.Graph
	caps *capability.CapabilitySet
}

// Supports reports whether the detected workspace exposes the named tool
// capability (build, test, lint, format, container).
func (g composedCapabilityGraph) Supports(cap string) bool {
	return g.ws != nil && g.ws.Supports(layer1.Capability(cap))
}

// Resolve returns the concrete command bound to the named capability.
func (g composedCapabilityGraph) Resolve(cap string) (string, bool) {
	if g.ws == nil {
		return "", false
	}
	return g.ws.Resolve(layer1.Capability(cap))
}

// CanMutateFile reports whether the runtime capability set's mutation scope
// covers the file path.
func (g composedCapabilityGraph) CanMutateFile(path string) bool {
	return g.caps != nil && g.caps.CanMutateFile(path)
}

// CanExecuteCommand reports whether the runtime capability set's execute
// scope covers the command.
func (g composedCapabilityGraph) CanExecuteCommand(cmd string) bool {
	return g.caps != nil && g.caps.CanExecuteCommand(cmd)
}
