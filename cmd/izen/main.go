package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/PizenLabs/izen/internal/compact"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/debug"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/infrastructure/capabilities"
	"github.com/PizenLabs/izen/internal/language"
	"github.com/PizenLabs/izen/internal/lea"
	"github.com/PizenLabs/izen/internal/pkg/lock"
	"github.com/PizenLabs/izen/internal/planner"
	"github.com/PizenLabs/izen/internal/project"
	"github.com/PizenLabs/izen/internal/prompt"
	"github.com/PizenLabs/izen/internal/retrieval"
	compose "github.com/PizenLabs/izen/internal/runtime/compose"
	"github.com/PizenLabs/izen/internal/runtime/gate"
	"github.com/PizenLabs/izen/internal/runtime/harness"
	"github.com/PizenLabs/izen/internal/runtime/orchestrator"
	"github.com/PizenLabs/izen/internal/runtime/output"
	"github.com/PizenLabs/izen/internal/runtime/preflight"
	"github.com/PizenLabs/izen/internal/runtime/ui/decision"
	"github.com/PizenLabs/izen/internal/state"
	"github.com/PizenLabs/izen/internal/ui"
)

// Audit wiring: ensure DI container instantiates new invariants.
// These references guarantee the binary is wired to the new runtime invariants.
var (
	_ = preflight.NewGate
	_ = preflight.EvaluateBudgetGate
	_ = decision.NewSurface
	_ = decision.AnnotateStrategies
	_ = harness.NewExtractorPipeline
	_ = gate.NewPipeline
	_ = orchestrator.NewLoop
)

var Version = "0.2.0-rmah-wired"

func printMinimalistHelp() {
	fmt.Println("izen — human-centered coding intelligence")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  izen                       Start the interactive TUI")
	fmt.Println("  izen [path]                Start TUI at the given project path")
	fmt.Println()
	fmt.Println("Execution (one authoritative runtime):")
	fmt.Println("  izen run [flags] \"<objective>\"   Execute an objective (see `izen run -h`)")
	fmt.Println("  izen prompt [flags] \"<objective>\"  Alias of run (surface $prompt)")
	fmt.Println("  izen orchestrate [flags] \"<objective>\"  Alias of run (surface $orchestrate)")
	fmt.Println("  izen resume [flags]        Continue the recorded interrupted objective")
	fmt.Println("  izen runs                  List durable execution tasks")
	fmt.Println()
	fmt.Println("Inspect and configure:")
	fmt.Println("  izen models [use <p> <m>]  List or set the active provider/model binding")
	fmt.Println("  izen auth status           Show credential availability (never the secret)")
	fmt.Println("  izen config show|get|style Inspect or set configuration")
	fmt.Println("  izen stats [-json]         Token usage from the durable ledger")
	fmt.Println("  izen debug [path]          Inspect engine diagnostics (Lea, governance, output)")
	fmt.Println("  izen compact               Compress prompt overhead in context/memory files")
	fmt.Println("  izen memory optimize       Alias for izen compact")
	fmt.Println("  izen rollback              Review recent file mutations")
	fmt.Println("  izen version               Show version information")
	fmt.Println("  izen help                  Show this help message")
	fmt.Println()
	fmt.Println("Non-interactive execution flags (izen run/resume):")
	fmt.Println("  -dir -target -provider -model -base-url -mode -approve -timeout -json -quiet")
	fmt.Println()
	fmt.Println("Exit codes: 0 success 1 failed 2 usage 3 provider 4 human-required 5 unsubstantiated 6 timeout 7 denied")
	fmt.Println()
	fmt.Println("Interactive Commands (inside TUI):")
	fmt.Println("  /ask /plan /build /investigate /review /help /status /mode /q")
	fmt.Println("  !<cmd>        Run a shell command")
	fmt.Println("  Ctrl+C / Esc  Exit Izen")
}

func main() {
	// ---- Phase 1: Global subcommand dispatch (no local state checks) ----
	if len(os.Args) > 1 {
		arg := os.Args[1]
		switch arg {
		case "version", "-v", "--version":
			fmt.Printf("izen version v%s (PizenLabs)\n", Version)
			os.Exit(0)
		case "help", "--help", "-h":
			printMinimalistHelp()
			os.Exit(0)
		case "auth":
			os.Exit(runAuthCommand(os.Args[2:]))
		case "models", "model":
			os.Exit(runModelsCommand(os.Args[2:]))
		case "stats":
			os.Exit(runStatsCommand(os.Args[2:]))
		case "config":
			os.Exit(runConfigCommand(os.Args[2:]))
		case "compact":
			runCompactCommand(os.Args[2:])
			os.Exit(0)
		case "memory":
			runMemoryCommand(os.Args[2:])
			os.Exit(0)
		case "debug":
			if err := runDebugCommand(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		case "run", "exec":
			os.Exit(runRuntimeCommand(os.Args[2:]))
		case "prompt":
			// `izen prompt "<objective>"` is an alias of `izen run` recorded
			// under the "$prompt" surface. It drives the SAME authoritative
			// runtime: compose.Wire → autonomy Driver → RuntimeExecutor.
			os.Exit(runPromptCommand(os.Args[2:]))
		case "orchestrate":
			os.Exit(runOrchestrateCommand(os.Args[2:]))
		case "resume":
			os.Exit(runResumeCommand(os.Args[2:]))
		case "runs":
			os.Exit(runRunsCommand(os.Args[2:]))
		}
	}

	// ---- Phase 2: Local scope parsing ----
	isRollbackMode := false
	targetDir := "."
	if len(os.Args) > 1 {
		arg := os.Args[1]
		switch arg {
		case "rollback":
			isRollbackMode = true
			targetDir = "."
		default:
			if arg[0] != '-' {
				targetDir = arg
			}
		}
	}

	// ---- Bootstrap common infrastructure ----
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		cfg = config.Default()
	}

	// Non-fatal bootup on unconfigured model state: launch TUI directly
	// into /models picker instead of terminating.
	unconfigured := false
	var bootErr error
	if err := cfg.Validate(); err != nil {
		bootErr = err
		unconfigured = true
	}
	_ = unconfigured // Graceful boot handled by TUI model state check in NewProgramWithApp

	// Inject the configured response style policy into every composed system
	// prompt for the lifetime of this process.
	prompt.SetActiveStyle(cfg.ActiveStylePolicy())

	// ── Silent global ~/.izen/ initialization ─────────────────────────────
	// On the very first run of izen on a machine, ensure ~/.izen/ exists with
	// default global config. This is SILENT — the user is never prompted or
	// interrupted for global setup.
	homeDir, homeErr := os.UserHomeDir()
	if homeErr == nil {
		globalCfgPath := filepath.Join(homeDir, ".izen", "config.yml")
		if _, statErr := os.Stat(globalCfgPath); os.IsNotExist(statErr) {
			_ = state.InitGlobalState()
			_ = config.Save(cfg)
		}
	}

	// ---- Local context boundary enforcement ----
	root := targetDir

	// ---- Project type detection (pure scan, no writes) ----
	// Computed once, before wiring, so the language-aware execution verifier
	// can be wired into the composition root. Detection never writes to disk,
	// so it is safe to run on the onboarding path.
	detection := project.Detect(root)
	var detectedLang language.ID
	if detection.Primary != nil {
		detectedLang = detection.Primary.ID
	}

	// ── COMPOSITION ROOT: INFRASTRUCTURE + APPLICATION LAYERS (RFC v1.0) ──
	// The composition root is the only place that instantiates the concrete
	// Infrastructure adapters and wires the Application layer (domain
	// WorkflowRuntime + command handlers + LedgerBuilder + Runtime facade +
	// the complete engine tree) on top of the shared event bus. The resulting
	// Application is injected into the Presentation layer below, making it the
	// single entry point of the system. Command handlers are registered inside
	// compose.Wire.
	osFile := capabilities.NewOSFile(root)
	shell := capabilities.NewExecShell(30 * time.Second)
	gitCLI := capabilities.NewGitCLI(root)
	patchAdapter := capabilities.NewPatchAdapter(root)
	bus := events.NewBus(events.DefaultBufferSize)

	app, wireErr := compose.Wire(
		compose.WithBus(bus),
		compose.WithAuditDir(filepath.Join(root, ".izen", "audit")),
		compose.WithCapabilities(compose.Capabilities{
			File:  osFile,
			Shell: shell,
			Git:   gitCLI,
			Patch: patchAdapter,
		}),
		compose.WithRoot(root),
		compose.WithConfig(cfg),
		compose.WithLanguageID(detectedLang),
	)
	if wireErr != nil {
		fmt.Fprintf(os.Stderr, "izen: wire application layer: %v\n", wireErr)
		os.Exit(1)
	}

	localCfg, _ := config.LoadLocalConfig(root)

	if err := state.MigrateLegacyFiles(root); err != nil {
		fmt.Fprintf(os.Stderr, "izen: migration warning: %v\n", err)
	}

	_ = state.CheckVersion(root, Version)

	if localCfg != nil && localCfg.Username != "" {
		cfg.Username = localCfg.Username
	}

	// ── Phase 4: inter-process workspace lock ─────────────────────────────
	// Serialize concurrent izen processes on the same workspace so .izen/
	// state files and the active session cannot be corrupted by a second
	// process (second terminal pane, background daemon). The lock is held
	// for the lifetime of the TUI session; nested agent-loop and patch
	// application reuse the self-held lock instead of failing.
	unlockWorkspace, lockErr := lock.TryAcquireWorkspaceLock(root)
	if lockErr != nil {
		if errors.Is(lockErr, lock.ErrWorkspaceLocked) {
			fmt.Fprintf(os.Stderr, "izen: %v — another izen process is active in %s\n", lockErr, root)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "izen: workspace lock: %v\n", lockErr)
		os.Exit(1)
	}
	defer unlockWorkspace()

	// ── AUDIT DURABILITY: blocking synchronous flush on signals + defer ──
	// The audit logger persists every bus envelope to
	// .izen/audit/events.ndjson asynchronously. Session finalization MUST
	// flush it synchronously: the signal handler below performs a blocking
	// Flush on SIGINT/SIGTERM (never swallowed — failures are reported to
	// stderr so a lost audit trail can never masquerade as success), and
	// the defer chain guarantees the same flush runs on every normal exit
	// path before the application tears down.
	stopAuditSignals := installAuditSignalFlush(app)
	defer stopAuditSignals()
	defer func() {
		if err := app.FlushAudit(); err != nil {
			fmt.Fprintf(os.Stderr, "izen: audit flush failed — evidence integrity compromised: %v\n", err)
		}
		app.Close()
		if err := app.AuditCloseErr(); err != nil {
			fmt.Fprintf(os.Stderr, "izen: audit teardown failed: %v\n", err)
		}
	}()

	// ── Gate: missing local config → launch TUI onboarding ─────────────────
	// NEVER write .izen/ or .izen/config.json to disk from main.go before the
	// TUI program runs. If .izen/config.json doesn't exist, launch the TUI
	// directly into the interactive onboarding flow. The TUI handles env var
	// detection, git init, identity setup, and provider selection — and only
	// writes .izen/config.json when the user confirms the setup wizard.
	if _, err := os.Stat(filepath.Join(root, ".izen", "config.json")); os.IsNotExist(err) {
		ui.RunMainDashboardWithApp(cfg, root, localCfg, app, bootErr)
		return
	}

	// ---- Project type report (local config exists) ----
	if detection.Primary != nil {
		primaryLang := detection.Primary.Name
		conf := detection.Confidence
		if _, err := os.Stat(root + "/.izen"); err == nil {
			updateLocalConfig(root, localCfg, detection)
		}
		fmt.Fprintf(os.Stderr, "izen: detected project type: %s (confidence: %.0f%%)\n", primaryLang, conf*100)
		if len(detection.Secondary) > 0 {
			fmt.Fprintf(os.Stderr, "izen: secondary languages:")
			for _, s := range detection.Secondary {
				fmt.Fprintf(os.Stderr, " %s", s.Def.Name)
			}
			fmt.Fprintln(os.Stderr)
		}
	} else {
		fmt.Fprintf(os.Stderr, "izen: warning: could not detect project type (no recognized files)\n")
	}

	// ---- Phase 3: TUI boot routing ----
	if isRollbackMode {
		ui.RunRollbackEngine(cfg, root, localCfg, app, detection)
	} else {
		ui.RunMainDashboardWithApp(cfg, root, localCfg, app, bootErr, detection)
	}
}

// installAuditSignalFlush ties the blocking, synchronous audit flush to OS
// signals (SIGINT, SIGTERM) and returns a stop function for the defer chain.
// On signal it performs a BLOCKING Flush of .izen/audit/events.ndjson before
// the process proceeds to teardown; flush errors are reported to stderr and
// never swallowed. In the TUI path the handler never exits the process
// itself — the TUI owns exit — it only guarantees the evidence is durable.
// A nil application (or nil audit logger) yields a no-op stop.
func installAuditSignalFlush(app *compose.Application) func() {
	if app == nil || app.Audit == nil {
		return func() {}
	}
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case sig := <-sigCh:
			fmt.Fprintf(os.Stderr, "izen: caught %v — flushing audit log synchronously…\n", sig)
			if err := app.FlushAudit(); err != nil {
				fmt.Fprintf(os.Stderr, "izen: audit flush on signal failed — evidence integrity compromised: %v\n", err)
			}
		case <-done:
		}
	}()
	return func() {
		signal.Stop(sigCh)
		select {
		case <-done:
		default:
			close(done)
		}
	}
}

func updateLocalConfig(root string, localCfg *config.LocalConfig, det project.Detection) {
	if localCfg == nil {
		localCfg = &config.LocalConfig{}
	}
	if det.Primary != nil {
		localCfg.DetectedLang = string(det.Primary.ID)
	}
	if len(det.Frameworks) > 0 {
		localCfg.DetectedFw = string(det.Frameworks[0].Def.ID)
	}
	localCfg.LastDetected = time.Now().Format(time.RFC3339)
	_ = config.SaveLocalConfig(root, localCfg)
}

// runConfigCommand implements the `izen config` command group:
//
//	izen config show              print the effective non-secret configuration
//	izen config get <key>         print one effective value
//	izen config style <policy>    set the response style policy
func runConfigCommand(args []string) int {
	usage := func() {
		fmt.Fprintln(os.Stderr, "Usage:")
		fmt.Fprintln(os.Stderr, "  izen config show")
		fmt.Fprintln(os.Stderr, "  izen config get <provider|model|style|username>")
		fmt.Fprintln(os.Stderr, "  izen config style <verbose|balanced|terse|ultra>")
	}
	if len(args) == 0 {
		usage()
		return exitUsage
	}
	cfg, err := config.Load()
	if err != nil {
		cfg = config.Default()
	}
	switch args[0] {
	case "-h", "--help", "help":
		usage()
		return exitOK
	case "show":
		fmt.Printf("provider: %s\n", cfg.ActiveProviderName())
		fmt.Printf("model:    %s\n", cfg.ActiveModelName())
		fmt.Printf("style:    %s\n", cfg.Style)
		fmt.Printf("username: %s\n", cfg.Username)
		return exitOK
	case "get":
		if len(args) < 2 {
			usage()
			return exitUsage
		}
		switch args[1] {
		case "provider":
			fmt.Println(cfg.ActiveProviderName())
		case "model":
			fmt.Println(cfg.ActiveModelName())
		case "style":
			fmt.Println(cfg.Style)
		case "username":
			fmt.Println(cfg.Username)
		default:
			fmt.Fprintf(os.Stderr, "izen config get: unknown key %q\n", args[1])
			return exitUsage
		}
		return exitOK
	case "style":
		if len(args) < 2 {
			usage()
			return exitUsage
		}
		policy, perr := prompt.ParseStylePolicy(args[1])
		if perr != nil {
			fmt.Fprintf(os.Stderr, "izen config style: %v\n", perr)
			return exitUsage
		}
		cfg.Style = string(policy)
		if serr := config.Save(cfg); serr != nil {
			fmt.Fprintf(os.Stderr, "izen config style: save config: %v\n", serr)
			return exitFailed
		}
		prompt.SetActiveStyle(policy)
		fmt.Printf("izen: output style set to %q\n", policy)
		return exitOK
	default:
		usage()
		return exitUsage
	}
}

// runCompactCommand implements `izen compact` / `izen memory optimize`: it
// scans prompt-overhead files, compresses them in place, and reports byte and
// token savings.
func runCompactCommand(args []string) {
	dryRun := false
	var paths []string
	for _, a := range args {
		switch a {
		case "-n", "--dry-run":
			dryRun = true
		case "-h", "--help":
			printCompactHelp()
			os.Exit(0)
		default:
			if strings.HasPrefix(a, "-") {
				fmt.Fprintf(os.Stderr, "izen compact: unknown flag %q\n", a)
				os.Exit(2)
			}
			paths = append(paths, a)
		}
	}
	if len(paths) == 0 {
		paths = []string{"."}
	}

	var targets []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			fmt.Fprintf(os.Stderr, "izen compact: %v\n", err)
			os.Exit(1)
		}
		if info.IsDir() {
			found, err := compact.DiscoverFiles(p)
			if err != nil {
				fmt.Fprintf(os.Stderr, "izen compact: %v\n", err)
				os.Exit(1)
			}
			targets = append(targets, found...)
		} else {
			targets = append(targets, p)
		}
	}

	if len(targets) == 0 {
		fmt.Println("izen compact: no target files found (AGENTS.md, RULES.md, CLAUDE.md, GEMINI.md, README.md, docs/*.md)")
		os.Exit(0)
	}

	totalOrig, totalNew := 0, 0
	for _, t := range targets {
		data, err := os.ReadFile(t)
		if err != nil {
			fmt.Fprintf(os.Stderr, "izen compact: read %s: %v\n", t, err)
			os.Exit(1)
		}
		opt, stats := compact.Optimize(string(data))
		if stats.NewBytes < stats.OriginalBytes {
			if dryRun {
				fmt.Printf("  would compact %s\n", t)
			} else if err := os.WriteFile(t, []byte(opt), 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "izen compact: write %s: %v\n", t, err)
				os.Exit(1)
			}
		}
		fmt.Printf("  %-46s %6d B -> %6d B (%5.1f%%) | %4d -> %4d tokens | %d code block(s) kept\n",
			t, stats.OriginalBytes, stats.NewBytes, stats.SavingsPercent(),
			stats.OriginalTokens, stats.NewTokens, stats.CodeBlocksPreserved)
		totalOrig += stats.OriginalBytes
		totalNew += stats.NewBytes
	}

	if totalOrig > 0 {
		fmt.Printf("  %-46s %6d B -> %6d B (%5.1f%%)  total\n",
			"TOTAL", totalOrig, totalNew, 100*(1-float64(totalNew)/float64(totalOrig)))
	}
}

// runMemoryCommand implements `izen memory optimize`, the alias for compact.
func runMemoryCommand(args []string) {
	if len(args) > 0 && args[0] == "optimize" {
		runCompactCommand(args[1:])
		return
	}
	fmt.Fprintln(os.Stderr, "Usage: izen memory optimize [flags] [path...]")
	os.Exit(1)
}

// runDebugCommand implements `izen debug [path]`: it materializes the on-demand
// Debug Capability report across the Phase 1-4 engines for the given workspace
// (defaulting to the current directory). The inspection is performed on
// request only — the engines carry no continuous instrumentation for it. The
// command is best-effort: an unavailable engine renders its section as "not
// available" rather than failing the whole report.
func runDebugCommand(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "-h", "--help":
			fmt.Println("Usage: izen debug [path]")
			fmt.Println()
			fmt.Println("Inspect on-demand diagnostics for the workspace engines:")
			fmt.Println("  Lea structural engine   index duration, files, symbols, edges, cache version")
			fmt.Println("  Context Governance      allocated budget vs retrieved/dropped chunks")
			fmt.Println("  Output Pipeline         compression ratio, token bytes saved, .logs/ pointers")
			fmt.Println()
			fmt.Println("No path argument inspects the current directory.")
			return nil
		}
	}

	root := "."
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		root = args[0]
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// ── Lea structural engine ───────────────────────────────────────────
	eng := lea.NewEngine(root)
	defer func() { _ = eng.Close() }()
	if err := eng.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "izen debug: lea engine: %v\n", err)
		// A stale/invalid cache (or a first run) fails to load; fall back to a
		// full re-index so the report reflects the live workspace.
		if _, ierr := eng.Index(ctx); ierr != nil {
			fmt.Fprintf(os.Stderr, "izen debug: lea re-index: %v\n", ierr)
		}
	}

	// ── Context governance: one real planning pass ─────────────────────
	// The planner is wired with the same adapters the TUI uses (Lea graph,
	// .logs/ tee logs, retrieval file source). The plan is best-effort: an
	// unindexed or empty workspace still yields a valid governance snapshot
	// (allocated budget with zero chunks).
	var plan *planner.ContextPlan
	planEng := planner.New(
		planner.WithGraphSource(planner.NewLeaAdapter(eng)),
		planner.WithLogSource(planner.NewTeeLogAdapter(root)),
		planner.WithFileSource(planner.NewRetrievalFileAdapter(
			retrieval.NewRouter(root, func(string, ...interface{}) {}),
		)),
	)
	if p, err := planEng.Plan(ctx, "explain the architecture of this project"); err == nil {
		plan = p
	}

	// ── Output pipeline: .logs/ inspection ──────────────────────────────
	// The report surfaces the persistent log directory, its files, and — when
	// a log exists — what the semantic compressor would do to the newest one.
	logs := output.InspectWorkspace(root)
	var outDi output.DebugInfo
	if len(logs.LogFiles) > 0 {
		if data, err := os.ReadFile(logs.LogFiles[0]); err == nil {
			outDi = output.New().Process("go test ./...", data).Debug(output.NewTee(root))
		}
	}

	report := debug.NewReport(eng, plan, outDi, logs)
	if err := report.Render(os.Stdout); err != nil {
		return fmt.Errorf("izen debug: render report: %w", err)
	}
	return nil
}

func printCompactHelp() {
	fmt.Println("Usage: izen compact [flags] [path...]")
	fmt.Println("       izen memory optimize [flags] [path...]")
	fmt.Println()
	fmt.Println("Scan and compress prompt overhead in context/memory files.")
	fmt.Println("Filler prose, comments, and conversational lines are stripped; code")
	fmt.Println("blocks, paths, variables, and command-line flags are preserved byte-for-byte.")
	fmt.Println()
	fmt.Println("Default targets when no path is given: AGENTS.md, RULES.md, CLAUDE.md,")
	fmt.Println("GEMINI.md, README.md, and docs/*.md in the current directory.")
	fmt.Println()
	fmt.Println("Flags:")
	fmt.Println("  -n, --dry-run   Preview savings without writing files")
	fmt.Println("  -h, --help      Show this help")
}
