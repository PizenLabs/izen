package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/internal/config"
)

// authUsage describes the `izen auth` command group.
const authUsage = `Usage:
  izen auth status    Show credential availability per provider (never the secret)
  izen auth login     Explain how to authenticate

izen never prints a stored credential. "login" is performed by the interactive
onboarding/TUI model picker or by setting the provider's environment variable.
`

// runAuthCommand implements `izen auth`.
func runAuthCommand(args []string) int {
	jsonOut := false
	var rest []string
	for _, a := range args {
		switch a {
		case "-h", "--help":
			fmt.Print(authUsage)
			return exitOK
		case "-json", "--json":
			jsonOut = true
		default:
			rest = append(rest, a)
		}
	}
	if len(rest) == 0 {
		fmt.Print(authUsage)
		return exitUsage
	}
	switch rest[0] {
	case "status":
		return authStatus(jsonOut)
	case "login":
		fmt.Println("izen: headless login is intentionally not implemented — no credential is")
		fmt.Println("written outside the interactive onboarding flow. To authenticate, either:")
		fmt.Println("  - run `izen` and use the /models picker, or")
		fmt.Println("  - export the provider's API key, e.g. OPENROUTER_API_KEY=...")
		return exitOK
	default:
		fmt.Fprintf(os.Stderr, "izen auth: unknown subcommand %q\n", rest[0])
		fmt.Fprint(os.Stderr, authUsage)
		return exitUsage
	}
}

func authStatus(jsonOut bool) int {
	cfg, err := config.Load()
	if err != nil {
		cfg = config.Default()
	}
	type row struct {
		Provider string `json:"provider"`
		Source   string `json:"credential_source,omitempty"`
		Present  bool   `json:"present"`
		EnvVar   string `json:"env_var,omitempty"`
	}
	names := make([]string, 0, len(config.ValidProviderNames))
	for name := range config.ValidProviderNames {
		names = append(names, name)
	}
	sort.Strings(names)
	rows := make([]row, 0, len(names))
	for _, name := range names {
		src := config.CredentialSourceFor(cfg, name)
		rows = append(rows, row{Provider: name, Source: src, Present: src != "", EnvVar: config.EnvVarForProvider(name)})
	}
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(map[string]any{
			"active_provider": cfg.ActiveProviderName(),
			"active_model":    cfg.ActiveModelName(),
			"providers":       rows,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "izen auth: encode: %v\n", err)
			return exitFailed
		}
		return exitOK
	}
	fmt.Printf("active: provider=%s model=%s\n", cfg.ActiveProviderName(), cfg.ActiveModelName())
	for _, r := range rows {
		state := "missing"
		if r.Present {
			state = r.Source
		}
		fmt.Printf("  %-12s %-8s (env %s)\n", r.Provider, state, envOrDash(r.EnvVar))
	}
	return exitOK
}

func envOrDash(env string) string {
	if strings.TrimSpace(env) == "" {
		return "-"
	}
	return env
}
