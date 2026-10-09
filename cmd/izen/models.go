package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/internal/config"
)

// modelsUsage describes the `izen models` command group.
const modelsUsage = `Usage:
  izen models                              List providers, active binding and models
  izen models use <provider> <model>       Persist the active provider/model binding

The binding is the single source of truth the runtime resolves at admission; an
explicit -provider/-model flag on ` + "`izen run`" + ` overrides it for one command only.

Flags:
  -json   Machine-readable output (list and use)
`

// runModelsCommand implements `izen models`.
func runModelsCommand(args []string) int {
	jsonOut := false
	var rest []string
	for _, a := range args {
		switch a {
		case "-h", "--help":
			fmt.Print(modelsUsage)
			return exitOK
		case "-json", "--json":
			jsonOut = true
		default:
			rest = append(rest, a)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		cfg = config.Default()
	}
	if len(rest) == 0 {
		return listModels(cfg, jsonOut)
	}
	if rest[0] == "use" {
		return useModel(cfg, rest[1:], jsonOut)
	}
	fmt.Fprintf(os.Stderr, "izen models: unknown subcommand %q\n", rest[0])
	fmt.Fprint(os.Stderr, modelsUsage)
	return exitUsage
}

// providerView is one non-secret provider projection.
type providerView struct {
	Name            string `json:"name"`
	Active          bool   `json:"active"`
	HasBaseURL      bool   `json:"has_base_url"`
	BaseURL         string `json:"base_url,omitempty"`
	Credential      string `json:"credential_source,omitempty"`
	HasCredential   bool   `json:"has_credential"`
	ConfiguredModel string `json:"configured_model,omitempty"`
}

func listModels(cfg *config.Config, jsonOut bool) int {
	activeProvider := cfg.ActiveProviderName()
	activeModel := cfg.ActiveModelName()

	names := make([]string, 0, len(config.ValidProviderNames))
	for name := range config.ValidProviderNames {
		names = append(names, name)
	}
	sort.Strings(names)

	views := make([]providerView, 0, len(names))
	for _, name := range names {
		provCfg := cfg.AI.Providers[name]
		src := config.CredentialSourceFor(cfg, name)
		views = append(views, providerView{
			Name:            name,
			Active:          name == activeProvider,
			HasBaseURL:      strings.TrimSpace(provCfg.BaseURL) != "",
			BaseURL:         provCfg.BaseURL,
			Credential:      src,
			HasCredential:   src != "",
			ConfiguredModel: provCfg.DefaultModel,
		})
	}

	if jsonOut {
		out := map[string]any{
			"active_provider": activeProvider,
			"active_model":    activeModel,
			"providers":       views,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			fmt.Fprintf(os.Stderr, "izen models: encode: %v\n", err)
			return exitFailed
		}
		return exitOK
	}

	fmt.Printf("active: provider=%s model=%s\n\n", activeProvider, activeModel)
	for _, v := range views {
		mark := " "
		if v.Active {
			mark = "*"
		}
		cred := "no credentials"
		if v.HasCredential {
			cred = "credentials: " + v.Credential
		}
		base := ""
		if v.BaseURL != "" {
			base = " base_url=" + v.BaseURL
		}
		fmt.Printf(" %s %-12s %-18s%s\n", mark, v.Name, cred, base)
	}
	fmt.Println("\nUse `izen models use <provider> <model>` to set the active binding.")
	return exitOK
}

func useModel(cfg *config.Config, args []string, jsonOut bool) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "Usage: izen models use <provider> <model>")
		return exitUsage
	}
	provider := strings.TrimSpace(args[0])
	model := strings.TrimSpace(args[1])
	if !config.ValidateProviderName(provider) {
		fmt.Fprintf(os.Stderr, "izen models use: unknown provider %q\n", provider)
		return exitUsage
	}
	if model == "" {
		fmt.Fprintln(os.Stderr, "izen models use: model is required")
		return exitUsage
	}
	if err := validateProviderModelBinding(provider, model); err != nil {
		fmt.Fprintf(os.Stderr, "izen models use: %v\n", err)
		return exitUsage
	}
	cfg.Bindings.Active.Provider = provider
	cfg.Bindings.Active.Model = model
	if cfg.AI.Providers == nil {
		cfg.AI.Providers = map[string]config.AIProviderConfig{}
	}
	if _, ok := cfg.AI.Providers[provider]; !ok {
		cfg.AI.Providers[provider] = config.AIProviderConfig{BaseURL: config.WellKnownBaseURL(provider)}
	}
	if err := config.Save(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "izen models use: save config: %v\n", err)
		return exitFailed
	}
	if jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"provider": provider, "model": model})
		return exitOK
	}
	fmt.Printf("izen: active binding set to provider=%s model=%s\n", provider, model)
	return exitOK
}
