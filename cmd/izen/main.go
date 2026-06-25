package main

import (
	"fmt"
	"os"

	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/session"
	"github.com/PizenLabs/izen/internal/ui"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		cfg = config.Default()
	}

	sess, err := session.Load()
	if err != nil {
		sess = session.New()
	}

	p := ui.NewProgram(cfg, sess)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running Izen: %v\n", err)
		os.Exit(1)
	}
}
