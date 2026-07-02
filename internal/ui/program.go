package ui

import (
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/git"
	"github.com/PizenLabs/izen/internal/graph"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/modes/plan"
	"github.com/PizenLabs/izen/internal/session"
)

// NewProgram initializes the active model state context and instantiates the runner engine.
func NewProgram(cfg *config.Config, sess *session.Session, mgr *ai.Manager) *tea.Program {
	eng := git.NewEngine(".")

	var provider ai.Provider
	if defaultP, _ := mgr.Default(); defaultP != nil {
		provider = defaultP
	}

	graphEng := graph.NewEngine(".")
	g, _, _ := graphEng.BuildOrLoad()

	ti := textinput.New()
	ti.Prompt = ""
	ti.CharLimit = 0
	ti.Focus()

	planStore := plan.NewPlanStore()
	execEng := execution.NewEngine(".", cfg, sess)
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
	}
	m.resolver.Set(sess.Mode)
	m.loadHistory()
	m.historyIndex = len(m.history)

	// Switch to WithMouseAllMotion to enable real-time custom drag selection maps
	// and trigger "Release-to-Copy" automation cleanly.
	opts := []tea.ProgramOption{
		tea.WithAltScreen(),
		tea.WithMouseAllMotion(),
	}

	return tea.NewProgram(m, opts...)
}
