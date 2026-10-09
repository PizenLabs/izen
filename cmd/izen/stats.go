package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/internal/runtime/durable"
)

// statsUsage describes `izen stats`, the durable usage report.
const statsUsage = `Usage: izen stats [-dir <path>] [-json]

Report token usage from the durable execution ledger
(.izen/runtime/ledger.ndjson). Every figure is the runtime's own always-flushed
cumulative accounting; nothing is estimated here or read from model prose.

Flags:
  -dir <path>   Workspace root (default ".")
  -json         Machine-readable output
`

type taskUsage struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	Intent       string `json:"intent"`
	PromptTokens int    `json:"prompt_tokens"`
	OutputTokens int    `json:"completion_tokens"`
	TotalTokens  int    `json:"total_tokens"`
	Turns        int    `json:"turns"`
	Estimated    bool   `json:"estimated"`
}

// runStatsCommand implements `izen stats`.
func runStatsCommand(args []string) int {
	dir := "."
	jsonOut := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			fmt.Print(statsUsage)
			return exitOK
		case "-json", "--json":
			jsonOut = true
		case "-dir":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "izen stats: -dir requires a path")
				return exitUsage
			}
			i++
			dir = args[i]
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintf(os.Stderr, "izen stats: unknown flag %q\n", args[i])
				return exitUsage
			}
			fmt.Fprintf(os.Stderr, "izen stats: unexpected argument %q\n", args[i])
			return exitUsage
		}
	}

	rows, err := readTaskUsage(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "izen stats: %v\n", err)
		return exitFailed
	}

	var total taskUsage
	for _, r := range rows {
		total.PromptTokens += r.PromptTokens
		total.OutputTokens += r.OutputTokens
		total.TotalTokens += r.TotalTokens
		total.Turns += r.Turns
		total.Estimated = total.Estimated || r.Estimated
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(map[string]any{"tasks": rows, "total": total}); err != nil {
			fmt.Fprintf(os.Stderr, "izen stats: encode: %v\n", err)
			return exitFailed
		}
		return exitOK
	}

	if len(rows) == 0 {
		fmt.Println("izen stats: no durable tasks recorded")
		return exitOK
	}
	for _, r := range rows {
		fmt.Printf("%-24s %-12s ↑%d ↓%d turns=%d %s\n",
			r.ID, r.Status, r.PromptTokens, r.OutputTokens, r.Turns, oneLine(r.Intent))
	}
	fmt.Printf("%-24s %-12s ↑%d ↓%d turns=%d\n", "TOTAL", "", total.PromptTokens, total.OutputTokens, total.Turns)
	return exitOK
}

// readTaskUsage materializes the durable token usage for every task.
func readTaskUsage(dir string) ([]taskUsage, error) {
	store := durable.NewTaskStore(dir)
	if err := store.Open(); err != nil {
		return nil, err
	}
	defer func() { _ = store.Close() }()

	ids := make([]string, 0, len(store.TaskScopes()))
	for id := range store.TaskScopes() {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	out := make([]taskUsage, 0, len(ids))
	for _, id := range ids {
		st, ok := store.State(id)
		if !ok {
			continue
		}
		out = append(out, taskUsage{
			ID:           st.ID,
			Status:       string(st.Status),
			Intent:       st.Intent,
			PromptTokens: st.TokenUsage.PromptTokens,
			OutputTokens: st.TokenUsage.CompletionTokens,
			TotalTokens:  st.TokenUsage.TotalTokens,
			Turns:        st.TokenUsage.Turns,
			Estimated:    st.TokenUsage.Estimated,
		})
	}
	return out, nil
}

// oneLine trims an intent to a single short line for tabular output.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 60 {
		return s[:57] + "..."
	}
	return s
}
