package main

import (
	"fmt"
	"os"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/audit"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/lynx"
	"github.com/PizenLabs/izen/internal/providers"
	"github.com/PizenLabs/izen/internal/retrieval"
	"github.com/PizenLabs/izen/internal/session"
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
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			fmt.Printf("izen version v%s (PizenLabs)\n", Version)
			os.Exit(0)
		case "help", "--help", "-h":
			printMinimalistHelp()
			os.Exit(0)
		}
	}

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

	root := "."
	if !state.HasLocalState(root) {
		if !ui.ConfirmInit("Initialize Izen architecture for this repository?") {
			fmt.Println("Aborted. Run 'izen' again when ready.")
			os.Exit(0)
		}
	}

	if err := state.InitLocalState(root); err != nil {
		fmt.Fprintf(os.Stderr, "izen: warning: local state init: %v\n", err)
	}

	if err := state.MigrateLegacyFiles(root); err != nil {
		fmt.Fprintf(os.Stderr, "izen: migration warning: %v\n", err)
	}

	_ = state.CheckVersion(root, Version)

	sess, err := session.Load()
	if err != nil {
		sess = session.New()
	}

	_ = audit.NewLogger(root)

	mgr := ai.NewManager()
	defaultProvider := cfg.ActiveProviderName()
	provCfg, ok := cfg.AI.Providers[defaultProvider]
	if ok {
		p := providers.NewOllamaProvider(provCfg.BaseURL, provCfg.APIKey, provCfg.DefaultModel)
		mgr.Register(defaultProvider, p)
		mgr.SetDefault(defaultProvider)
	}

	if cfg.Lynx.Enabled {
		lc := lynx.NewController(root, cfg.Lynx.LazyStart)
		retrieval.SetLynxController(lc)

		if err := lc.EnsureStarted(); err != nil {
			fmt.Fprintf(os.Stderr, "izen: lynx warning: %v\n", err)
		}

		if cfg.Lynx.LazyStart {
			lc.StartLazy()
		}

		defer lc.Stop()
	}

	p := ui.NewProgram(cfg, sess, mgr)

	configCh := make(chan bool, 1)
	config.StartConfigWatcher(configCh)
	go func() {
		for range configCh {
			p.Send(config.ConfigChangeMsg{})
		}
	}()

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running Izen: %v\n", err)
		os.Exit(1)
	}
}
