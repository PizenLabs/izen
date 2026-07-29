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
	"github.com/PizenLabs/izen/internal/audit"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/control"
	"github.com/PizenLabs/izen/internal/core/artifact"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/capability"
	"github.com/PizenLabs/izen/internal/core/runtime"
	"github.com/PizenLabs/izen/internal/core/workflow"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/git"
	"github.com/PizenLabs/izen/internal/graph"
	"github.com/PizenLabs/izen/internal/language"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/investigate"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/project"
	"github.com/PizenLabs/izen/internal/providers"
	"github.com/PizenLabs/izen/internal/retrieval"
	"github.com/PizenLabs/izen/internal/session"
	"github.com/PizenLabs/izen/internal/state"
)

// NewProgram initializes the active model state context and instantiates the runner engine.
func NewProgram(root string, cfg *config.Config, sess *session.Session, mgr *ai.Manager, localCfg *config.LocalConfig, det ...project.Detection) *tea.Program {
	detection := project.Detection{}
	if len(det) > 0 {
		detection = det[0]
	}

	userName := resolveUsername(root, localCfg)

	eng := git.NewEngine(root)

	var provider ai.Provider
	if defaultP, _ := mgr.Default(); defaultP != nil {
		provider = defaultP
	}

	graphEng := graph.NewEngine(root)

	ti := textinput.New()
	ti.Prompt = ""
	ti.CharLimit = 0
	ti.Focus()

	planStore := plan.NewPlanStore()
	planEng := plan.NewEngine(planStore)
	planEng.SetUserName(userName)
	if provider != nil {
		planEng.SetProvider(provider.Execute)
	}

	var detectedLang language.ID
	if detection.Primary != nil {
		detectedLang = detection.Primary.ID
	}

	execEng := execution.NewEngine(root, cfg, sess, detectedLang)
	execEng.SetPlanStore(planStore)

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
		// Even if a local .izen/config.json is missing but .izen/ dir state
		// exists, recover into the completed state rather than re-onboarding.
		if state.HasLocalState(root) {
			localActive = true
		}
	}
	if !localActive {
		// Always start at the welcome screen (initNone) when .izen/ does not
		// exist. The welcome screen handles git detection and routes to the
		// correct sub-stage (initGitCheck, initIdentity) when the user presses
		// Enter. This ensures the onboarding flow is never bypassed.
		initStage = initNone
	}

	// ── DEFERRED GRAPH LOAD ─────────────────────────────────────────────
	// Graph cache must not be loaded before the onboarding detector runs,
	// because BuildOrLoad creates .izen/graph/ and would cause a false
	// positive in HasLocalState, bypassing the TUI onboarding flow.
	// The graph is rebuilt incrementally in a background goroutine below.
	g := graph.NewGraph(root)

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

	// ── CONTROL PLANE: RuntimeContext + WorkflowStateMachine ──────────────
	// These are the single source of truth for capability flags, budget
	// counters, artifact lifecycle states, and workflow state. The UI reads
	// them directly and MUST NOT cache or duplicate these values.
	caps := capability.NewCapabilitySet()
	caps.Grant(capability.CapabilityRead)
	caps.Grant(capability.CapabilityWrite)
	caps.Grant(capability.CapabilityExecute)
	caps.Grant(capability.CapabilityTest)
	caps.Grant(capability.CapabilityPatch)
	caps.Grant(capability.CapabilityCheckpoint)
	caps.Grant(capability.CapabilityRollback)
	artStore := artifact.NewStore(root)
	bgt := budget.NewBudget(
		100,            // max files
		5000,           // max diff lines
		1_000_000,      // max tokens
		10,             // max attempts
		30*time.Second, // max duration per step
		5,              // max concurrent commands
	)
	runtimeCtx := runtime.New(artStore, caps, bgt)
	workflowSM := workflow.NewWorkflowStateMachine()
	wcc := control.NewWorkflowCheckpointManager(execEng.Checkpoints, root)
	workflowSM.WithCheckpointCoordinator(workflow.NewCheckpointCoordinator(wcc))

	// ── AUTHORIZATION ENGINE ───────────────────────────────────────────────
	// Production AuthorizationEngine wired with a no-op source hash verifier
	// and a checkpoint checker that inspects .izen/checkpoints/ on disk.
	// The getState closure reads the current workflow state from the SM.
	authEngine := authorization.NewProductionAuthorizationEngine(root, func() workflow.WorkflowState {
		return workflowSM.State()
	})
	mb := budget.DefaultMicroBudget()
	microBudget := &mb

	m := &model{
		cfg:                 cfg,
		runtimeCtx:          runtimeCtx,
		workflowSM:          workflowSM,
		authEngine:          authEngine,
		mutationBudget:      bgt,
		microBudget:         microBudget,
		caps:                caps,
		sess:                sess,
		provider:            provider,
		mgr:                 mgr,
		gitEng:              eng,
		graphEng:            graphEng,
		graph:               g,
		extractorRegistry:   retrieval.NewPolyglotRegistry(),
		resolver:            modes.NewResolver(),
		attachedFiles:       make([]string, 0),
		execEng:             execEng,
		planStore:           planStore,
		planEngine:          planEng,
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
		toolCallBuffer:      execution.NewToolCallBuffer(root),
		thinkingPanel:       NewThinkingPanel(),
		liveCodePreview:     NewLiveCodePreview(),
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

	m.resolver.Set(sess.Mode)
	m.loadHistory()
	m.historyIndex = len(m.history)

	// ── WIRE ACTIVITY LOGGERS ────────────────────────────────────────────
	// The model's logActivity method is injected as the callback for every
	// package that performs internal file system / search / binary actions.
	// This guarantees every ReadFile, Grep/Search, and lx invocation is
	// immediately visible in the chat viewport as a styled system line.
	activityFn := func(format string, args ...interface{}) {
		m.logActivity(format, args...)
	}
	retrieval.SetActivityLogger(activityFn)
	execution.SetActivityLogger(activityFn)

	// ── REDIRECT /investigate ENGINE LOG SINKS ───────────────────────────
	// The investigate orchestrator has two package-level activity sinks:
	// forensicLog (defaults to log.Printf → stderr) and dispatchLog (defaults
	// to fmt.Printf → stdout). Left at their defaults they write RAW TEXT to
	// the terminal while Bubble Tea owns the alt-screen, corrupting the
	// rendered frame — broken ──── separators, misaligned viewport height, and
	// a re-drawn prompt that appears "doubled" as the raw bytes shove the real
	// frame. Route both through the same activityFn used for every other engine
	// package so orchestrator progress surfaces as styled viewport lines
	// instead of frame-corrupting raw output. The engine already dispatches a
	// single terminal investigateResultMsg on completion; these sinks are pure
	// progress telemetry.
	investigate.SetForensicLog(activityFn)
	investigate.SetDispatchLog(activityFn)

	return tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
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

func bootCommon(root string, cfg *config.Config) (*session.Session, *ai.Manager, *retrieval.Router) {
	sess, err := session.Load()
	if err != nil {
		sess = session.New()
	}

	_ = audit.NewLogger(root)

	mgr := ai.NewManager()

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

	defaultProvider := cfg.ActiveProviderName()
	mgr.SetDefault(defaultProvider)

	// Create the search router — auto-detects lx in PATH.
	activityFn := func(format string, args ...interface{}) {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	}
	router := retrieval.NewRouter(root, activityFn)
	retrieval.SetGlobalRouter(router)

	return sess, mgr, router
}

func runProgram(p *tea.Program, root string, graphEng *graph.Engine, initStage initStage) {
	configCh := make(chan bool, 1)
	config.StartConfigWatcher(configCh)
	go func() {
		for range configCh {
			p.Send(config.ConfigChangeMsg{})
		}
	}()

	// ── BACKGROUND INDEXING ─────────────────────────────────────
	// Launch graph building in a background goroutine so the TUI
	// boots instantly. The model tracks indexingStatus and the
	// header renders an indicator. When indexing completes, a
	// graphBuiltMsg or graphIndexingMsg is sent through the
	// Bubble Tea event loop.
	go func() {
		if initStage == initComplete && state.HasLocalState(root) {
			g2, fromCache, err := graphEng.BuildOrLoadIncremental()
			if err != nil {
				p.Send(graphBuiltMsg{err: err})
				return
			}
			if g2 != nil {
				p.Send(graphBuiltMsg{graph: g2})
			}
			_ = fromCache
		}
		p.Send(graphIndexingMsg{indexing: false})
	}()

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running Izen: %v\n", err)
		os.Exit(1)
	}
}

func RunMainDashboard(cfg *config.Config, root string, localCfg *config.LocalConfig, det ...project.Detection) {
	sess, mgr, router := bootCommon(root, cfg)
	_ = router // router is registered globally via SetGlobalRouter

	detection := project.Detection{}
	if len(det) > 0 {
		detection = det[0]
	}

	graphEng := graph.NewEngine(root)
	initStage := initComplete
	localCfgPath := filepath.Join(root, ".izen", "config.json")
	_, localCfgErr := os.Stat(localCfgPath)
	localActive := localCfgErr == nil
	if !localActive {
		if state.HasLocalState(root) {
			localActive = true
		}
	}
	if !localActive {
		initStage = initNone
	}

	p := NewProgram(root, cfg, sess, mgr, localCfg, detection)
	runProgram(p, root, graphEng, initStage)
}

func RunRollbackEngine(cfg *config.Config, root string, localCfg *config.LocalConfig, det ...project.Detection) {
	sess, mgr, router := bootCommon(root, cfg)
	_ = router

	// ── VIRTUAL SNAPSHOT ROLLBACK ────────────────────────────────
	// Create an execution engine and rollback any in-flight patches.
	fmt.Fprintf(os.Stderr, "izen: running rollback engine for %s...\n", root)
	execEng := execution.NewEngine(root, cfg, sess)
	errs := execEng.RollbackTransaction()
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

	graphEng := graph.NewEngine(root)
	initStage := initComplete
	localCfgPath := filepath.Join(root, ".izen", "config.json")
	_, localCfgErr := os.Stat(localCfgPath)
	localActive := localCfgErr == nil
	if !localActive {
		if state.HasLocalState(root) {
			localActive = true
		}
	}
	if !localActive {
		initStage = initNone
	}

	p := NewProgram(root, cfg, sess, mgr, localCfg, detection)
	runProgram(p, root, graphEng, initStage)
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
