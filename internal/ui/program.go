package ui

import (
	"fmt"
	"os"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/audit"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/git"
	"github.com/PizenLabs/izen/internal/graph"
	"github.com/PizenLabs/izen/internal/lynx"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/providers"
	"github.com/PizenLabs/izen/internal/retrieval"
	"github.com/PizenLabs/izen/internal/session"
)

// NewProgram initializes the active model state context and instantiates the runner engine.
func NewProgram(root string, cfg *config.Config, sess *session.Session, mgr *ai.Manager) *tea.Program {
	eng := git.NewEngine(root)

	var provider ai.Provider
	if defaultP, _ := mgr.Default(); defaultP != nil {
		provider = defaultP
	}

	graphEng := graph.NewEngine(root)
	g, _, _ := graphEng.BuildOrLoad()

	ti := textinput.New()
	ti.Prompt = ""
	ti.CharLimit = 0
	ti.Focus()

	planStore := plan.NewPlanStore()
	execEng := execution.NewEngine(root, cfg, sess)
	execEng.SetPlanStore(planStore)

	m := &model{
		cfg:           cfg,
		sess:          sess,
		provider:      provider,
		gitEng:        eng,
		graphEng:      graphEng,
		graph:         g,
		resolver:      modes.NewResolver(),
		attachedFiles: make([]string, 0),
		execEng:       execEng,
		planStore:     planStore,
		ti:            ti,
		showBanner:    true,
		IsCloudModel:  cfg.ActiveProviderName() != "ollama",
		ContextLimit:  128000,
	}
	m.resolver.Set(sess.Mode)
	m.loadHistory()
	m.historyIndex = len(m.history)

	// Print the static welcome header once on startup so the terminal's
	// native scrollback captures it permanently.
	fmt.Println()
	fmt.Println(m.renderStartupBanner(120))
	fmt.Println()

	return tea.NewProgram(m)
}

func bootCommon(root string, cfg *config.Config) (*session.Session, *ai.Manager, *lynx.Controller) {
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

	var lc *lynx.Controller
	if cfg.Lynx.Enabled {
		lc = lynx.NewController(root, cfg.Lynx.LazyStart)
		retrieval.SetLynxController(lc)

		if err := lc.EnsureStarted(); err != nil {
			fmt.Fprintf(os.Stderr, "izen: lynx warning: %v\n", err)
		}

		if cfg.Lynx.LazyStart {
			lc.StartLazy()
		}
	}

	return sess, mgr, lc
}

func runProgram(p *tea.Program) {
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

func RunMainDashboard(cfg *config.Config, root string) {
	sess, mgr, lc := bootCommon(root, cfg)

	if lc != nil {
		defer func() { _ = lc.Stop() }()
	}

	p := NewProgram(root, cfg, sess, mgr)
	runProgram(p)
}

func RunRollbackEngine(cfg *config.Config, root string) {
	sess, mgr, lc := bootCommon(root, cfg)

	fmt.Fprintf(os.Stderr, "izen: rollback engine stub — not yet implemented (root=%s)\n", root)

	if lc != nil {
		defer func() { _ = lc.Stop() }()
	}

	p := NewProgram(root, cfg, sess, mgr)
	runProgram(p)
}
