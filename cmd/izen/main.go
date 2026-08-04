package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/compact"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/debug"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/infrastructure/capabilities"
	"github.com/PizenLabs/izen/internal/lea"
	"github.com/PizenLabs/izen/internal/planner"
	"github.com/PizenLabs/izen/internal/project"
	"github.com/PizenLabs/izen/internal/prompt"
	"github.com/PizenLabs/izen/internal/retrieval"
	compose "github.com/PizenLabs/izen/internal/runtime/compose"
	"github.com/PizenLabs/izen/internal/runtime/output"
	"github.com/PizenLabs/izen/internal/state"
	"github.com/PizenLabs/izen/internal/ui"
)

var Version = "0.1.0"

func printMinimalistHelp() {
	fmt.Println("izen — human-centered coding intelligence")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  izen                    Start the interactive TUI")
	fmt.Println("  izen version            Show version information")
	fmt.Println("  izen help               Show this help message")
	fmt.Println("  izen debug [path]       Inspect engine diagnostics (Lea, governance, output)")
	fmt.Println("  izen auth login         Authenticate with a provider")
	fmt.Println("  izen stats              Show usage statistics")
	fmt.Println("  izen config style       Set response style policy (verbose|balanced|terse|ultra)")
	fmt.Println("  izen compact            Compress prompt overhead in context/memory files")
	fmt.Println("  izen memory optimize    Alias for izen compact")
	fmt.Println("  izen rollback           Review recent file mutations")
	fmt.Println("  izen run \"<prompt>\"      Execute a prompt through the v1 runtime state machine")
	fmt.Println("  izen [path]             Start TUI at the given project path")
	fmt.Println()
	fmt.Println("Interactive Commands (inside TUI):")
	fmt.Println("  /ask          Explain, inspect, understand (read-only)")
	fmt.Println("  /plan         Architecture, migrations, refactors (no exec)")
	fmt.Println("  /build        Implement, refactor, write tests (controlled exec)")
	fmt.Println("  /investigate  Debug bugs, failures, regressions")
	fmt.Println("  /review       Audit changes, detect risks")
	fmt.Println("  /help         Show interactive help")
	fmt.Println("  /mode <name>  Switch mode")
	fmt.Println("  /q            Exit Izen")
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
			if len(os.Args) > 2 && os.Args[2] == "login" {
				fmt.Println("Auth login is not yet implemented.")
				os.Exit(0)
			}
			fmt.Println("Usage: izen auth login")
			os.Exit(1)
		case "stats":
			fmt.Println("Stats are not yet implemented.")
			os.Exit(0)
		case "config":
			runConfigCommand(os.Args[2:])
			os.Exit(0)
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
		case "run":
			if err := runRuntimeCommand(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
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

	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "izen: config error: %v\n", err)
		os.Exit(1)
	}

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

	// ── COMPOSITION ROOT: INFRASTRUCTURE + APPLICATION LAYERS (RFC v1.0) ──
	// The composition root is the only place that instantiates the concrete
	// Infrastructure adapters and wires the Application layer (domain
	// WorkflowRuntime + command handlers + LedgerBuilder + Runtime facade)
	// on top of the shared event bus. The resulting Runtime is injected into
	// the Presentation layer below, making it the single entry point of the
	// system. Command handlers are registered inside compose.Wire.
	osFile := capabilities.NewOSFile(root)
	shell := capabilities.NewExecShell(30 * time.Second)
	gitCLI := capabilities.NewGitCLI(root)
	patchAdapter := capabilities.NewPatchAdapter(root)
	bus := events.NewBus(events.DefaultBufferSize)

	app, wireErr := compose.Wire(
		compose.WithBus(bus),
		compose.WithCapabilities(compose.Capabilities{
			File:  osFile,
			Shell: shell,
			Git:   gitCLI,
			Patch: patchAdapter,
		}),
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

	// ── Gate: missing local config → launch TUI onboarding ─────────────────
	// NEVER write .izen/ or .izen/config.json to disk from main.go before the
	// TUI program runs. If .izen/config.json doesn't exist, launch the TUI
	// directly into the interactive onboarding flow. The TUI handles env var
	// detection, git init, identity setup, and provider selection — and only
	// writes .izen/config.json when the user confirms the setup wizard.
	if _, err := os.Stat(filepath.Join(root, ".izen", "config.json")); os.IsNotExist(err) {
		ui.RunMainDashboardWithApp(cfg, root, localCfg, app)
		return
	}

	// ---- Project type detection (local config exists) ----
	detection := project.Detect(root)
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
		ui.RunRollbackEngine(cfg, root, localCfg, detection)
	} else {
		ui.RunMainDashboardWithApp(cfg, root, localCfg, app, detection)
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

// runConfigCommand implements `izen config style <policy>`: it parses the
// policy, persists it to the global config, and activates it for the current
// process.
func runConfigCommand(args []string) {
	usage := func() {
		fmt.Fprintln(os.Stderr, "Usage: izen config style <verbose|balanced|terse|ultra>")
		os.Exit(1)
	}
	if len(args) < 2 || args[0] != "style" {
		usage()
	}

	policy, err := prompt.ParseStylePolicy(args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "izen config style: %v\n", err)
		os.Exit(2)
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "izen config style: load config: %v\n", err)
		os.Exit(1)
	}
	cfg.Style = string(policy)
	if err := config.Save(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "izen config style: save config: %v\n", err)
		os.Exit(1)
	}
	prompt.SetActiveStyle(policy)

	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".izen", "config.yml")
	fmt.Printf("izen: output style set to %q (saved to %s)\n", policy, path)
	fmt.Printf("izen: injected OUTPUT STYLE directive into system prompts:\n%s\n", prompt.StyleDirective(policy))
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
