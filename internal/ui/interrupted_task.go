package ui

import (
	"fmt"
	"strings"
)

// interruptedTask is the unfinished durable task recovered at startup from the
// append-only execution ledger (.izen/runtime/ledger.ndjson).
//
// It is a SURFACE, not a resume loop: the ledger is the record of what the
// previous process actually did, and the only thing the UI does with it is
// tell the human. Re-running the objective is a human decision, and the driver's
// own ledger key means a re-run continues the SAME durable task rather than
// minting a competing one.
type interruptedTask struct {
	id     string
	intent string
	status string
	step   string
}

// surface renders the recovery notice. It names the task identity and its last
// durable state, and says plainly that nothing was resumed — a notice that
// implied an automatic continuation would be a claim the runtime cannot back.
func (t *interruptedTask) surface() string {
	if t == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("[recovered] Unfinished task found in the durable execution ledger")
	fmt.Fprintf(&b, "\n  task:  %s", t.id)
	if step := strings.TrimSpace(t.step); step != "" {
		fmt.Fprintf(&b, "  (step %s)", step)
	}
	if status := strings.TrimSpace(t.status); status != "" {
		fmt.Fprintf(&b, "\n  state: %s", status)
	}
	if intent := strings.TrimSpace(t.intent); intent != "" {
		fmt.Fprintf(&b, "\n  goal:  %s", intent)
	}
	b.WriteString("\n  Nothing was resumed automatically — re-run the objective to continue it.")
	return b.String()
}
