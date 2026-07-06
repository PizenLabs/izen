package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/config"
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

	if err := state.EnsureRuntimeBinaries(); err != nil {
		fmt.Fprintf(os.Stderr, "izen: runtime binary error: %v\n", err)
		os.Exit(1)
	}

	// ---- Local context boundary enforcement ----
	root := targetDir

	localCfg, _ := config.LoadLocalConfig(root)
	if !state.HasLocalState(root) {
		reader := bufio.NewReader(os.Stdin)
		fmt.Print("Initialize Izen architecture for this repository? (y/n): ")
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "y" && answer != "yes" {
			fmt.Println("Aborted. Run 'izen' again when ready.")
			os.Exit(0)
		}

		if err := state.InitLocalState(root); err != nil {
			fmt.Fprintf(os.Stderr, "izen: warning: local state init: %v\n", err)
		}

		defaultName := os.Getenv("USER")
		if defaultName == "" {
			defaultName = "developer"
		}
		fmt.Printf("What should I call you? (default: %s): ", defaultName)
		nameAnswer, _ := reader.ReadString('\n')
		nameAnswer = strings.TrimSpace(nameAnswer)
		if nameAnswer == "" {
			nameAnswer = defaultName
		}

		localCfg = &config.LocalConfig{Username: nameAnswer}
		if err := config.SaveLocalConfig(root, localCfg); err != nil {
			fmt.Fprintf(os.Stderr, "izen: warning: saving local config: %v\n", err)
		}

		fmt.Printf("✨ Welcome aboard, @%s! Configuration saved.\n", nameAnswer)
		time.Sleep(500 * time.Millisecond)
		fmt.Print("\033[H\033[2J")
	} else {
		if err := state.InitLocalState(root); err != nil {
			fmt.Fprintf(os.Stderr, "izen: warning: local state init: %v\n", err)
		}
	}

	if err := state.MigrateLegacyFiles(root); err != nil {
		fmt.Fprintf(os.Stderr, "izen: migration warning: %v\n", err)
	}

	_ = state.CheckVersion(root, Version)

	if localCfg != nil && localCfg.Username != "" {
		cfg.Username = localCfg.Username
	}

	// ---- Phase 3: TUI boot routing ----
	if isRollbackMode {
		ui.RunRollbackEngine(cfg, root, localCfg)
	} else {
		ui.RunMainDashboard(cfg, root, localCfg)
	}
}
