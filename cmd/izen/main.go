package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/project"
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
	fmt.Println("  izen auth login         Authenticate with a provider")
	fmt.Println("  izen stats              Show usage statistics")
	fmt.Println("  izen rollback           Review recent file mutations")
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

	localCfg, _ := config.LoadLocalConfig(root)

	if err := state.MigrateLegacyFiles(root); err != nil {
		fmt.Fprintf(os.Stderr, "izen: migration warning: %v\n", err)
	}

	_ = state.CheckVersion(root, Version)

	if localCfg != nil && localCfg.Username != "" {
		cfg.Username = localCfg.Username
	}

	// ── Gate: local config missing → force-launch TUI init flow ───────────
	// When .izen/config.json does NOT exist, the user MUST go through the
	// interactive init flow. We NEVER write silent defaults for the local
	// scope. Skip project detection and all other busywork.
	if _, err := os.Stat(filepath.Join(root, ".izen", "config.json")); os.IsNotExist(err) {
		ui.RunMainDashboard(cfg, root, localCfg)
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
		ui.RunMainDashboard(cfg, root, localCfg, detection)
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
