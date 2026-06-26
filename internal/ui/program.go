package ui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/execution"
	"github.com/PizenLabs/izen/internal/git"
	"github.com/PizenLabs/izen/internal/graph"
	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/session"
)

func NewProgram(cfg *config.Config, sess *session.Session, mgr *ai.Manager) *tea.Program {
	eng := git.NewEngine(".")

	var provider ai.Provider
	if defaultP, _ := mgr.Default(); defaultP != nil {
		provider = defaultP
	}

	graphEng := graph.NewEngine(".")
	g, _, _ := graphEng.BuildOrLoad()

	m := &model{
		cfg:           cfg,
		sess:          sess,
		provider:      provider,
		gitEng:        eng,
		graphEng:      graphEng,
		graph:         g,
		resolver:      modes.NewResolver(),
		attachedFiles: make([]string, 0),
		execEng:       execution.NewEngine(".", cfg, sess),
	}
	m.resolver.Set(sess.Mode)

	return tea.NewProgram(m)
}
