// Package commands contains small, presentation-independent command seams.
// The status command lives here so workspace inspection and its Bubble Tea
// dispatch can be tested without importing the full UI model.
package commands

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/workspace"
)

// Name is the canonical standalone status command.
const Name = "/status"

// Input is the runtime-owned portion of a status request. Root is normally
// the active workspace root; the remaining fields are cheap snapshots already
// held by the UI/application and therefore require no discovery I/O.
type Input struct {
	Root       string
	Generation uint64
	Indexer    workspace.IndexerStatus
	Session    workspace.SessionStatus
	Engine     workspace.EngineStatus
}

// ResultMsg is emitted after the bounded status probe completes. Status is
// always populated on a best-effort basis; Err is reserved for an unexpected
// collector failure and is safe for the UI to render as a compact error.
type ResultMsg struct {
	Status     workspace.Status
	Err        error
	Generation uint64
}

var _ tea.Msg = ResultMsg{}

// Collect performs the status probe synchronously with the caller's context.
// The workspace collector applies its own strict inspection deadline, so this
// function is safe for direct/headless callers as well as tests.
func Collect(ctx context.Context, input Input) workspace.Status {
	return workspace.CollectStatus(ctx, input.Root, workspace.StatusInputs{
		Indexer: input.Indexer,
		Session: input.Session,
		Engine:  input.Engine,
	})
}

// StatusCmd returns a Bubble Tea command that performs the same bounded probe
// off the UI event loop and emits a ResultMsg. Git work is never performed by
// the command's caller thread.
func StatusCmd(input Input) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), workspace.InspectionTimeout)
		defer cancel()
		return ResultMsg{Status: Collect(ctx, input), Generation: input.Generation}
	}
}

// Run is a concise alias for StatusCmd.
func Run(input Input) tea.Cmd { return StatusCmd(input) }

// PolicyGateForMode projects the mode policy surface into the small vocabulary
// shown by /status. This is an observation only; it does not grant authority.
func PolicyGateForMode(mode string) string {
	mode = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(mode)), "/")
	switch mode {
	case "ask", "plan", "review":
		return "Read-Only"
	case "build", "investigate":
		return "Interactive"
	default:
		return "Unknown"
	}
}
