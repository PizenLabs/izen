package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/lea"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/investigate"
	"github.com/PizenLabs/izen/internal/presentation"
	"github.com/PizenLabs/izen/internal/project"
	"github.com/PizenLabs/izen/internal/retrieval"
	appruntime "github.com/PizenLabs/izen/internal/runtime"
	compose "github.com/PizenLabs/izen/internal/runtime/compose"
	"github.com/PizenLabs/izen/internal/state"
	"github.com/PizenLabs/izen/pkg/tui/components/shimmer"
	"github.com/PizenLabs/izen/pkg/tui/tips"
)

// NewProgramWithApp initializes the model bound to the externally wired
// Application layer (RFC v1.0 section 1). The shared bus in app is the single
// event bus every engine publishes onto, and app.Runtime is the single entry
// point the presentation layer drives. Every engine, orchestrator, capability
// set and adapter is consumed read-only from app — this package never
// instantiates engines.
func NewProgramWithApp(root string, cfg *config.Config, localCfg *config.LocalConfig, app *compose.Application, det ...project.Detection) *tea.Program {
	detection := project.Detection{}
	if len(det) > 0 {
		detection = det[0]
	}

	userName := resolveUsername(root, localCfg)

	// The display username may be more specific than the process-level fallback
	// wired into the plan engine (local config / git identity take precedence).
	// Push the resolved value down so plans are attributed correctly.
	if app != nil && app.PlanEngine != nil {
		app.PlanEngine.SetUserName(userName)
	}

	var provider ai.Provider
	if app != nil {
		provider = app.Provider()
	}

	ti := textinput.New()
	ti.Prompt = ""
	ti.CharLimit = 0
	ti.Focus()

	// ── EVENT BUS ──────────────────────────────────────────────────────────
	// The single in-process event bus wired by the Application layer. The mode
	// engines publish domain events headlessly (never calling UI routines
	// directly); the UI subscribes below and acts purely as a projection of
	// the event stream.
	eventBus := app.Bus

	// ── PRESENTATION BRIDGE ───────────────────────────────────────────────
	presenter := presentation.New(app.Runtime)

	// ── STRICT LOCAL-FIRST ONBOARDING DETECTOR ────────────────────────
	// The init gate MUST be driven exclusively by the CURRENT local repo.
	// A global ~/.izen/config.yml from a previous workspace is NEVER used to
	// bypass onboarding for a brand-new repo — it is read only as a read-only
	// source of pre-filled default form values (username + provider).
	//
	//   Branch 1 (Local Active): .izen/config.json exists locally
	//       -> initStage = initComplete, enter workspace.
	//   Branch 2 (Local Missing): .izen/config.json does NOT exist locally
	//       -> initStage = initConfirm, trigger interactive TUI setup.
	//       (regardless of whether a global ~/.izen/config.yml exists)
	//
	// localCfg is loaded from .izen/config.json but is an empty struct when the
	// file is absent, so we verify disk presence directly to decide the gate.
	localCfgPath := filepath.Join(root, ".izen", "config.json")
	_, localCfgErr := os.Stat(localCfgPath)
	localActive := localCfgErr == nil

	// Read-only pre-population: global footprint used ONLY to seed form
	// defaults — never to advance initStage past initConfirm.
	var globalUsername string
	var globalProvider string
	if home, homeErr := os.UserHomeDir(); homeErr == nil && home != "" {
		if g, gErr := config.Load(); gErr == nil {
			if g.Username != "" {
				globalUsername = g.Username
			}
			if g.AI.DefaultProvider != "" {
				globalProvider = g.AI.DefaultProvider
			}
		}
	}

	initStage := initComplete
	if !localActive {
		// .izen/config.json is missing. Do NOT let leftover .izen/ directory
		// state (state.HasLocalState) promote this workspace past onboarding:
		// isProjectInitialized() requires config.json to render the workspace,
		// so any initStage other than initNone here deadlocks into a frozen
		// welcome header. Route to the wizard instead.
		initStage = initNone
	}
	if !localActive {
		// Always start at the welcome screen (initNone) when .izen/ does not
		// exist. The welcome screen handles git detection and routes to the
		// correct sub-stage (initGitCheck, initIdentity) when the user presses
		// Enter. This ensures the onboarding flow is never bypassed.
		initStage = initNone
	}

	// ── DEFERRED GRAPH LOAD ─────────────────────────────────────────────
	// The file-centric graph view must not be loaded before the onboarding
	// detector runs, because indexing creates .izen/ and would cause a false
	// positive in HasLocalState, bypassing the TUI onboarding flow.
	// The graph is indexed in a background goroutine below.
	g := lea.NewFileGraph(root)

	// ── Explicit mode registry (deterministic bootstrap) ──────────────
	// Modes are registered here, in one place, instead of via implicit
	// init() self-registration. This makes wiring testable and lets external
	// (plugin / MCP) modes register themselves without touching package state.
	reg := NewRegistry()
	reg.Register(modes.ModeAsk, askView{})
	reg.Register(modes.ModePlan, planView{})
	reg.Register(modes.ModeBuild, buildView{})
	reg.Register(modes.ModeInvestigate, investigateView{})
	reg.Register(modes.ModeReview, reviewView{})

	m := &model{
		cfg:                 cfg,
		runtimeCtx:          app.RuntimeCtx,
		workflowSM:          app.WorkflowSM,
		workflowRT:          app.Workflow,
		authEngine:          app.Auth,
		mutationBudget:      app.Budget,
		microBudget:         app.MicroBudget,
		caps:                app.Caps,
		sess:                app.Session(),
		provider:            provider,
		mgr:                 app.Manager(),
		gitEng:              app.Git,
		graph:               g,
		leaEng:              app.Lea,
		extractorRegistry:   retrieval.NewPolyglotRegistry(),
		resolver:            modes.NewResolver(),
		attachedFiles:       make([]string, 0),
		execEng:             app.Execution,
		planStore:           app.PlanStore,
		planEngine:          app.PlanEngine,
		executor:            app.Executor,
		gateway:             app.Gateway,
		microkernel:         app.Microkernel,
		intentCompiler:      app.IntentCompiler,
		ledger:              NewContextLedger(),
		ti:                  ti,
		showBanner:          true,
		IsCloudModel:        cfg.ActiveProviderName() != "ollama",
		ContextLimit:        128000,
		userName:            userName,
		workspaceRoot:       root,
		detection:           detection,
		projectContext:      projectContextFor(detection),
		repoConfig:          repoConfigFor(root),
		initStage:           initStage,
		initProviderIdx:     0,
		initProviderFilter:  "",
		initPrefillUsername: globalUsername,
		initPrefillProvider: globalProvider,
		viewRegistry:        reg,
		logStore:            NewLogStore(),
		bus:                 eventBus,
		rt:                  app.Runtime,
		pres:                presenter,
		orch:                app.Orchestrator,
		autonomy:            app.Autonomy,
		pipelineEngine:      app.Pipeline,
		patchEngine:         app.Patch,
		viewState:           presentation.NewWorkflowViewState(),
		toolCallBuffer:      execution.NewToolCallBuffer(root),
		thinkingPanel:       NewThinkingPanel(),
		liveCodePreview:     NewLiveCodePreview(),
		shimmerAnim:         shimmer.New(""),
		tipProvider:         tips.Default(),
		currentEffort:       EffortAuto,
	}
	if initStage == initIdentity {
		m.initIdentityInput = textinput.New()
		m.initIdentityInput.Prompt = ""
		m.initIdentityInput.CharLimit = 64
		m.initIdentityInput.Placeholder = "username"
		if globalUsername != "" {
			m.userName = globalUsername
		}
		m.initIdentityInput.SetValue(m.userName)
		m.initIdentityInput.Focus()
	}

	// ── INDEXING STATUS ──────────────────────────────────────────
	// Default is "indexing" when the project is initialized but
	// the graph is being built in the background. On a fresh project
	// (initNone), no indexing is needed yet.
	if initStage == initComplete {
		m.indexingStatus = "indexing"
	}

	m.resolver.Set(app.Session().Mode)
	m.loadHistory()
	m.historyIndex = len(m.history)

	// ── WIRE ACTIVITY LOGGERS ────────────────────────────────────────────
	// The retrieval/execution activity sinks are routed through the event bus
	// (never a direct model callback): each line is published as an
	// EventActivity domain event and projected by handleDomainEvent on the UI
	// goroutine. This guarantees the model is only ever mutated from the
	// Bubble Tea event loop.
	activityFn := func(format string, args ...interface{}) {
		eventBus.Publish(events.NewActivity(fmt.Sprintf(format, args...)))
	}
	retrieval.SetActivityLogger(activityFn)
	execution.SetActivityLogger(activityFn)

	// ── WIRE TYPED EVENT LOGGERS ─────────────────────────────────────────
	// The typed engine I/O events (bytes read, lines patched, hits, elapsed)
	// are wrapped as EventEngineTelemetry on the bus and projected into the
	// ActivityTree by handleDomainEvent. No direct UI calls from engines.
	eventFn := func(ev interface{}) {
		eventBus.Publish(events.NewEngineTelemetry(ev))
	}
	retrieval.SetEventLogger(eventFn)
	execution.SetEventLogger(eventFn)

	// ── REDIRECT /investigate ENGINE LOG SINKS ───────────────────────────
	// The investigate orchestrator's forensic/dispatch sinks are also routed
	// through the bus so progress surfaces as projected viewport lines instead
	// of frame-corrupting raw output or direct model mutation. The engine
	// already dispatches a single terminal investigateResultMsg on completion.
	investigate.SetForensicLog(activityFn)
	investigate.SetDispatchLog(activityFn)

	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())

	// ── FORCE-EXIT TERMINAL RESTORE ──────────────────────────────────────
	// The model needs the owning program to restore the terminal before a hard
	// double-Ctrl+C exit (status 130).
	m.program = p

	// ── FACT-ONLY CONTROL BRIDGE ─────────────────────────────────────
	// The adaptive control loop's telemetry bus publishes fact-only facts
	// (control.iteration + control.node_observed). The UI subscribes via
	// ListenControlEvents (armed in Init, once the program runs) and projects
	// the raw Dynamic IR facts as a live execution tree — never reconstructing
	// or mutating engine state.
	m.controlFactSend = p.Send

	// ── BACKGROUND LEA INDEXING ─────────────────────────────────────────
	// Boot the Phase 3 structural engine in the background so the TUI never
	// blocks on the (potentially large) full index. It is gated on completed
	// onboarding because the engine persists its cache under .izen/ — running
	// it during the init flow would create a false positive in HasLocalState
	// and silently bypass the setup wizard. Once indexed, /arch and the
	// context planner read straight from the Lea graph. The graphBuiltMsg
	// drives the model's indexingStatus and populates the file-centric graph.
	if initStage == initComplete && state.HasLocalState(root) && app.Lea != nil {
		go func() {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := app.Lea.Start(ctx); err != nil {
				p.Send(graphBuiltMsg{err: err})
				return
			}
			p.Send(graphBuiltMsg{graph: app.Lea.FileGraph()})
		}()
	}

	// ── PRESENTATION EVENT PROJECTION SUBSCRIPTION ────────────────────────
	// The Application layer translates the engine domain events into
	// UI-ready PresentationEvents and forwards them here. The view updates
	// strictly from those payloads (presentationEventMsg); the direct domain
	// subscriptions below are kept ONLY for the rich engine event types the
	// translator intentionally leaves to the UI (engine telemetry, reasoning
	// streams, intent disambiguation, and approval prompts).
	m.presSink = presentation.NewEventSink(p, app.Runtime, func(ev appruntime.PresentationEvent) tea.Msg {
		return presentationEventMsg{ev: ev}
	})
	app.Runtime.Start()

	// ── EVENT BUS PROJECTION SUBSCRIPTION ───────────────────────────────
	// The UI is a pure projection: every domain event published by the headless
	// engines is forwarded into the Bubble Tea event loop as a domainEventMsg
	// and rendered by the model's handleDomainEvent. p.Send is safe from any
	// goroutine, so a bus dispatch goroutine can bridge into the UI safely.
	// Subscriptions happen after the program exists because nothing runs before
	// p.Run(); any event published before then is simply dropped (non-blocking).
	//
	// EventPhaseChanged and EventApprovalRequested additionally drive the
	// derived UI-state projection (presentation.WorkflowViewState): the
	// AwaitingApproval/Processing presentation states are a pure function of
	// the canonical workflow state, never independent UI flags.
	for _, typ := range []string{
		events.EventPatchAttempted,
		events.EventEngineTelemetry,
		events.EventReasoningStream,
		events.EventIntentClassified,
		events.EventPhaseChanged,
		events.EventApprovalRequested,
// ── CANONICAL RUNTIME EXECUTION LIFECYCLE (RuntimeExecutor) ──
		// The runtime owns every execution; the UI renders its lifecycle purely
		// as a projection of these events.
		events.EventExecutionStarted,
		events.EventStrategySelected,
		events.EventTargetResolved,
		events.EventContextPrepared,
		events.EventModelInvoked,
		events.EventProviderWaiting,
		events.EventProviderFirstToken,
		events.EventProviderStreamDelta,
		events.EventProviderUsageUpdate,
		events.EventReasoningTelemetry,
		events.EventProviderResponse,
		events.EventArtifactProduced,
		events.EventMutationStarted,
		events.EventMutationCompleted,
		events.EventVerificationCompleted,
		events.EventExecutionFinished,
		events.EventApprovalRequired,
		// Autonomy decision runtime events: every gate (auto_continue /
		// ask_user / block / direct_response), capability grant, loop step and
		// context compilation is projected as an activity line so the operator
		// observes exactly when the runtime thinks, asks, switches, loops, acts
		// and stops.
		events.EventAutonomyDecision,
		events.EventCapabilityGranted,
		events.EventLoopTransition,
		events.EventContextCompiled,
	} {
		eventBus.Subscribe(typ, func(ev events.DomainEvent) {
			p.Send(domainEventMsg{ev: ev})
		})
	}

	return p
}

// resolveUsername resolves the user's display name with the following
// precedence:
//  1. Local config (.izen/config.json or TUI init state).
//  2. Git user name (git config user.name).
//  3. Environment variable ($USER).
//  4. OS user lookup (os/user.Current).
//  5. Fallback to "developer".
func resolveUsername(root string, localCfg *config.LocalConfig) string {
	if localCfg != nil && localCfg.Username != "" {
		return localCfg.Username
	}

	gitName := gitUsername(root)
	if gitName != "" {
		return gitName
	}

	u := os.Getenv("USER")
	if u != "" {
		return u
	}

	if currentUser, err := user.Current(); err == nil && currentUser.Username != "" {
		return currentUser.Username
	}

	return "developer"
}

func gitUsername(root string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "config", "user.name")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	val := strings.TrimSpace(string(out))
	if val != "" {
		return val
	}
	return ""
}

func runProgram(p *tea.Program, root string, initStage initStage) {
	configCh := make(chan bool, 1)
	config.StartConfigWatcher(configCh)
	go func() {
		for range configCh {
			p.Send(config.ConfigChangeMsg{})
		}
	}()

	// ── ROOT SIGINT/SIGTERM CANCELLATION BRIDGE ──────────────────────────
	// First signal: graceful cancellation of the active operation. Second
	// signal while a cancellation is in progress: hard exit with status 130.
	// The application must never require tmux kill-pane to recover.
	stopSignals := installRootSignalBridge(p)

	_, runErr := p.Run()
	stopSignals()
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "Error running Izen: %v\n", runErr)
		os.Exit(1)
	}
}

// RunMainDashboardWithApp is the composition-root entry point: it launches the
// main TUI dashboard bound to the fully-wired Application layer (shared bus +
// Runtime facade + complete engine tree). The composition root (cmd/izen)
// builds app once via compose.Wire and injects it here, satisfying the RFC
// single-entry-point invariant — no engine is ever instantiated in this
// package.
func RunMainDashboardWithApp(cfg *config.Config, root string, localCfg *config.LocalConfig, app *compose.Application, det ...project.Detection) {
	detection := project.Detection{}
	if len(det) > 0 {
		detection = det[0]
	}

	initStage := initComplete
	localCfgPath := filepath.Join(root, ".izen", "config.json")
	_, localCfgErr := os.Stat(localCfgPath)
	localActive := localCfgErr == nil
	if !localActive {
		// config.json is the sole onboarding authority; leftover .izen/ dir
		// state must never promote past onboarding (see NewProgramWithApp).
		initStage = initNone
	}

	p := NewProgramWithApp(root, cfg, localCfg, app, detection)
	runProgram(p, root, initStage)
}

func RunRollbackEngine(cfg *config.Config, root string, localCfg *config.LocalConfig, app *compose.Application, det ...project.Detection) {
	// ── VIRTUAL SNAPSHOT ROLLBACK ────────────────────────────────
	// Roll back any in-flight patches using the execution engine wired by the
	// composition root.
	fmt.Fprintf(os.Stderr, "izen: running rollback engine for %s...\n", root)
	if app == nil || app.Execution == nil {
		fmt.Fprintln(os.Stderr, "izen: rollback unavailable — no execution engine wired.")
		return
	}
	errs := app.Execution.RollbackTransaction()
	if len(errs) > 0 {
		for _, err := range errs {
			fmt.Fprintf(os.Stderr, "izen: rollback error: %v\n", err)
		}
	} else {
		fmt.Fprintf(os.Stderr, "izen: rollback complete — workspace restored to last snapshot.\n")
	}

	detection := project.Detection{}
	if len(det) > 0 {
		detection = det[0]
	}

	initStage := initComplete
	localCfgPath := filepath.Join(root, ".izen", "config.json")
	_, localCfgErr := os.Stat(localCfgPath)
	localActive := localCfgErr == nil
	if !localActive {
		// config.json is the sole onboarding authority; leftover .izen/ dir
		// state must never promote past onboarding (see NewProgramWithApp).
		initStage = initNone
	}

	p := NewProgramWithApp(root, cfg, localCfg, app, detection)
	runProgram(p, root, initStage)
}

// projectContextFor returns a safe ProjectContext from project detection.
// When detection finds no recognized files (Primary is nil), it returns
// a fallback context with Name "generic" and Type "unknown" so the UI
// always has valid data to render.
func projectContextFor(detection project.Detection) *project.ProjectContext {
	if detection.Primary != nil {
		return &project.ProjectContext{
			Name: detection.Primary.Name,
			Type: string(detection.Primary.Category),
		}
	}
	return project.FallbackProjectContext()
}

// repoConfigFor returns a safe RepoConfig for the given root directory.
// It checks git status and provides defaults when git metadata is missing.
func repoConfigFor(root string) *project.RepoConfig {
	isGitRepo := false
	defaultBranch := "main"
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		isGitRepo = true
		if head, err := os.ReadFile(filepath.Join(root, ".git", "HEAD")); err == nil {
			branch := strings.TrimSpace(string(head))
			if strings.HasPrefix(branch, "ref: refs/heads/") {
				defaultBranch = strings.TrimPrefix(branch, "ref: refs/heads/")
			}
		}
	}
	return &project.RepoConfig{
		Root:          root,
		IsGitRepo:     isGitRepo,
		DefaultBranch: defaultBranch,
	}
}
