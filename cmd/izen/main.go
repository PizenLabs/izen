package main

import (
	"fmt"
	"os"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/lynx"
	"github.com/PizenLabs/izen/internal/providers"
	"github.com/PizenLabs/izen/internal/retrieval"
	"github.com/PizenLabs/izen/internal/session"
	"github.com/PizenLabs/izen/internal/ui"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		cfg = config.Default()
	}

	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "izen: config error: %v\n", err)
		os.Exit(1)
	}

	sess, err := session.Load()
	if err != nil {
		sess = session.New()
	}

	mgr := ai.NewManager()
	defaultProvider := cfg.ActiveProviderName()
	provCfg, ok := cfg.AI.Providers[defaultProvider]
	if ok {
		p := providers.NewOllamaProvider(provCfg.BaseURL, provCfg.APIKey, provCfg.DefaultModel)
		mgr.Register(defaultProvider, p)
		mgr.SetDefault(defaultProvider)
	}

	if cfg.Lynx.Enabled {
		root := "."
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
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running Izen: %v\n", err)
		os.Exit(1)
	}
}
