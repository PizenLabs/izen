package command

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/session"
)

// CommandRouter defines the interface for routing commands to native handlers
type CommandRouter interface {
	Route(input string) (handled bool, cmd tea.Cmd)
}

// NewCommandRouter creates a new command router with all handlers
func NewCommandRouter(sess *session.Session, resolver *modes.Resolver) *Router {
	return &Router{
		sess:     sess,
		resolver: resolver,
	}
}

// Router implements CommandRouter
type Router struct {
	sess     *session.Session
	resolver *modes.Resolver
}

// Route processes input and returns whether it was handled by a native command
func (r *Router) Route(input string) (bool, tea.Cmd) {
	input = strings.TrimSpace(input)
	if input == "" {
		return false, nil
	}

	// Handle bare commands (without slash) that should bypass LLM
	switch input {
	case "run":
		return r.handleRun()
	case "apply":
		return r.handleApply()
	case "build":
		return r.handleBuild()
	case "clear":
		return r.handleClear()
	case "undo":
		return r.handleUndo()
	case "mode":
		return r.handleModeBare()
	}

	// Handle slash commands that should bypass LLM
	if strings.HasPrefix(input, "/") {
		parts := strings.Fields(input)
		if len(parts) == 0 {
			return false, nil
		}

		cmd := parts[0]
		switch cmd {
		case "/run":
			return r.handleRun()
		case "/apply":
			return r.handleApply()
		case "/build":
			return r.handleBuild()
		case "/clear":
			return r.handleClear()
		case "/undo":
			return r.handleUndo()
		case "/mode":
			if len(parts) >= 2 {
				return r.handleModeWithArg(parts[1])
			}
			return r.handleModeBare()
		}
	}

	// Not handled by router, pass to LLM
	return false, nil
}

// Native command handlers - these execute immediately without LLM involvement

func (r *Router) handleRun() (bool, tea.Cmd) {
	// Execute a shell command or internal action
	r.sess.AddMessage("system", "Executing run command...", 1)
	// TODO: Implement actual run logic based on current context
	return true, nil
}

func (r *Router) handleApply() (bool, tea.Cmd) {
	// Apply staged changes
	r.sess.AddMessage("system", "Applying staged changes...", 1)
	// TODO: Implement actual apply logic
	return true, nil
}

func (r *Router) handleBuild() (bool, tea.Cmd) {
	// Trigger build process
	r.sess.AddMessage("system", "Starting build process...", 1)
	// TODO: Implement actual build logic
	return true, nil
}

func (r *Router) handleClear() (bool, tea.Cmd) {
	// Clear conversation + restore banner
	r.sess.AddMessage("system", "Clearing conversation...", 1)
	return true, nil
}

func (r *Router) handleUndo() (bool, tea.Cmd) {
	// Undo last operation
	r.sess.AddMessage("system", "Undoing last operation...", 1)
	// TODO: Implement actual undo logic
	return true, nil
}

func (r *Router) handleModeBare() (bool, tea.Cmd) {
	// Show current mode or help
	current := r.resolver.Current()
	r.sess.AddMessage("system", fmt.Sprintf("Current mode: %s", current), 1)
	return true, nil
}

func (r *Router) handleModeWithArg(modeStr string) (bool, tea.Cmd) {
	// Set mode to specified value
	mode, ok := modes.Parse(modeStr)
	if !ok {
		r.sess.AddMessage("error", fmt.Sprintf("Invalid mode: %s", modeStr), 1)
		return true, nil
	}
	r.sess.SetMode(mode)
	_ = r.sess.Save()
	r.sess.AddMessage("system", fmt.Sprintf("Switched to %s mode", mode), 1)
	return true, nil
}
