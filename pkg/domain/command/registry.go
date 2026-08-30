// Package command defines the foundational domain model for Izen's command
// surface: the permission model, the command/workspace taxonomy, and the
// unified CommandRegistry. The registry is the single source of truth consumed
// by the parser and the TUI suggestion engine.
//
// Dependency rule: the package imports only the Go standard library. It never
// imports modes, pipeline, runtime, or UI packages.
package command

import (
	"sort"
	"strings"
	"sync"
)

// descriptorKey identifies a descriptor by its marker rune and lowercased name.
type descriptorKey struct {
	marker rune
	name   string
}

// Registry is the thread-safe, unified command registry. It holds every
// official Izen command, workspace, and directive and answers three questions:
//
//   - which directives a workspace may invoke (GetAllowedDirectives),
//   - whether a marker+name token names a known descriptor (Lookup), and
//   - whether a marker+prefix resolves to exactly one descriptor
//     (LookupPrefix).
//
// Aliases map an alternate marker+name (e.g. "/q" → "/quit", "/?" → "/help")
// onto a canonical descriptor.
type Registry struct {
	mu      sync.RWMutex
	entries map[descriptorKey]CommandDescriptor
	aliases map[descriptorKey]descriptorKey
}

// NewRegistry returns an empty, thread-safe Registry.
func NewRegistry() *Registry {
	return &Registry{
		entries: make(map[descriptorKey]CommandDescriptor),
		aliases: make(map[descriptorKey]descriptorKey),
	}
}

// Register adds or replaces a descriptor. Names are matched case-insensitively.
func (r *Registry) Register(d CommandDescriptor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[descriptorKey{marker: d.Marker, name: strings.ToLower(d.Name)}] = d
}

// RegisterAll registers every descriptor in ds.
func (r *Registry) RegisterAll(ds []CommandDescriptor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range ds {
		r.entries[descriptorKey{marker: d.Marker, name: strings.ToLower(d.Name)}] = d
	}
}

// RegisterAlias maps an alternate marker+name onto an already-registered
// canonical descriptor, e.g. MarkerSlash "q" → canonical "quit". The lookup
// returns the canonical descriptor (so the alias resolves to the same Name,
// Kind, permissions, and description). Registering an alias for an unknown
// canonical name is a no-op. Aliases are never returned by All() or the
// allowed-command queries — they only widen Lookup/LookupPrefix.
func (r *Registry) RegisterAlias(marker rune, alias, canonical string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	canonicalKey := descriptorKey{marker: marker, name: strings.ToLower(canonical)}
	if _, ok := r.entries[canonicalKey]; !ok {
		return
	}
	r.aliases[descriptorKey{marker: marker, name: strings.ToLower(alias)}] = canonicalKey
}

// Lookup returns a copy of the descriptor registered under marker and name.
// Name matching is case-insensitive, and registered aliases resolve to their
// canonical descriptor. It reports false when no descriptor matches.
func (r *Registry) Lookup(marker rune, name string) (*CommandDescriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.lookupLocked(marker, name)
	if !ok {
		return nil, false
	}
	cp := d
	return &cp, true
}

// lookupLocked resolves a marker+name against entries and aliases. Callers
// must hold at least a read lock.
func (r *Registry) lookupLocked(marker rune, name string) (CommandDescriptor, bool) {
	key := descriptorKey{marker: marker, name: strings.ToLower(name)}
	if d, ok := r.entries[key]; ok {
		return d, true
	}
	if canonical, ok := r.aliases[key]; ok {
		d, ok := r.entries[canonical]
		return d, ok
	}
	return CommandDescriptor{}, false
}

// LookupPrefix resolves marker+name to a descriptor when the name names the
// descriptor exactly, is a registered alias, OR is an unambiguous prefix of
// exactly one canonical descriptor ("/q" → "/quit"). Ambiguous prefixes
// ("/u" matching both "undo" and "usage") report false so a wrong guess is
// never silently accepted.
func (r *Registry) LookupPrefix(marker rune, name string) (*CommandDescriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if d, ok := r.lookupLocked(marker, name); ok {
		cp := d
		return &cp, true
	}
	prefix := strings.ToLower(name)
	if prefix == "" {
		return nil, false
	}
	var match *CommandDescriptor
	count := 0
	for _, d := range r.entries {
		if d.Marker != marker {
			continue
		}
		if !strings.HasPrefix(d.Name, prefix) {
			continue
		}
		count++
		if count == 1 {
			dd := d
			match = &dd
		}
	}
	if count == 1 && match != nil {
		cp := *match
		return &cp, true
	}
	return nil, false
}

// GetAllowedDirectives returns every directive ($…) whose RequiredPerms is a
// subset of the workspace's permission set (bitwise subset logic). Directives
// requiring a permission the workspace lacks are excluded. The result is sorted
// by name for deterministic presentation.
func (r *Registry) GetAllowedDirectives(ws WorkspaceType) []CommandDescriptor {
	perms := ws.Permissions()
	r.mu.RLock()
	var out []CommandDescriptor
	for _, d := range r.entries {
		if d.Marker != MarkerDollar {
			continue
		}
		if !perms.Contains(d.RequiredPerms) {
			continue
		}
		out = append(out, d)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// GetAllowedGlobalCommands returns every global command (/undo, /help, …)
// whose RequiredPerms is a subset of the workspace's permission set (bitwise
// subset logic). Mutation-capable globals like /undo, /commit, and
// /checkpoint are excluded from read-only workspaces. The result is sorted by
// name for deterministic presentation.
func (r *Registry) GetAllowedGlobalCommands(ws WorkspaceType) []CommandDescriptor {
	perms := ws.Permissions()
	r.mu.RLock()
	var out []CommandDescriptor
	for _, d := range r.entries {
		if d.Marker != MarkerSlash || d.Kind != KindGlobal {
			continue
		}
		if !perms.Contains(d.RequiredPerms) {
			continue
		}
		out = append(out, d)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// All returns every registered descriptor, sorted by marker then name. Each
// element is a copy, so callers may mutate freely. It is the enumeration the
// TUI suggestion engine uses to project the full command surface without any
// hardcoded lists.
func (r *Registry) All() []CommandDescriptor {
	r.mu.RLock()
	out := make([]CommandDescriptor, 0, len(r.entries))
	for _, d := range r.entries {
		out = append(out, d)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Marker != out[j].Marker {
			return out[i].Marker < out[j].Marker
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// NewDefault builds a fresh Registry pre-loaded with every official Izen
// command, workspace, and directive, plus the canonical alias surface.
func NewDefault() *Registry {
	r := NewRegistry()
	r.RegisterAll(officialCommands())
	r.RegisterAlias(MarkerSlash, "q", "quit")
	r.RegisterAlias(MarkerSlash, "?", "help")
	return r
}

var (
	defaultOnce sync.Once
	defaultReg  *Registry
)

// Default returns the process-wide canonical Registry singleton.
func Default() *Registry {
	defaultOnce.Do(func() { defaultReg = NewDefault() })
	return defaultReg
}

// workspace builds the descriptor for a workflow context.
func workspace(ws WorkspaceType) CommandDescriptor {
	return CommandDescriptor{
		Marker:        MarkerSlash,
		Name:          ws.String(),
		Kind:          KindWorkspace,
		RequiredPerms: ws.Permissions(),
		Description:   ws.description(),
		SupportsChain: true,
	}
}

// directive builds a $ capability descriptor.
func directive(name string, category DirectiveCategory, perms PermissionSet, chain bool, desc string) CommandDescriptor {
	return CommandDescriptor{
		Marker:        MarkerDollar,
		Name:          name,
		Kind:          KindDirective,
		Category:      category.String(),
		RequiredPerms: perms,
		Description:   desc,
		SupportsChain: chain,
	}
}

// officialCommands enumerates every official Izen command, workspace, and
// directive descriptor.
func officialCommands() []CommandDescriptor {
	return []CommandDescriptor{
		// Workspaces (/).
		workspace(WorkspaceAsk),
		workspace(WorkspaceInvestigate),
		workspace(WorkspacePlan),
		workspace(WorkspaceBuild),
		workspace(WorkspaceReview),

		// Global commands (/).
		{Marker: MarkerSlash, Name: "help", Kind: KindGlobal, RequiredPerms: PermissionSet(PermRead), Description: "render the mode and command reference"},
		{Marker: MarkerSlash, Name: "usage", Kind: KindGlobal, RequiredPerms: PermissionSet(PermRead), Description: "display runtime usage, tokens, and provider status"},
		{Marker: MarkerSlash, Name: "model", Kind: KindGlobal, RequiredPerms: PermissionSet(PermRead), Description: "switch or pick the active model/provider"},
		{Marker: MarkerSlash, Name: "provider", Kind: KindGlobal, RequiredPerms: PermissionSet(PermRead), Description: "switch provider (deprecated, use /model)"},
		{Marker: MarkerSlash, Name: "objective", Kind: KindGlobal, RequiredPerms: PermissionSet(PermRead), Description: "create a budget-guarded session objective"},
		{Marker: MarkerSlash, Name: "session", Kind: KindGlobal, RequiredPerms: PermissionSet(PermRead), Description: "list or resume workspace sessions"},
		{Marker: MarkerSlash, Name: "clear", Kind: KindGlobal, RequiredPerms: PermissionSet(PermRead), Description: "reset the workspace session"},
		{Marker: MarkerSlash, Name: "drop", Kind: KindGlobal, RequiredPerms: PermissionSet(PermRead), Description: "detach context files"},
		{Marker: MarkerSlash, Name: "arch", Kind: KindGlobal, RequiredPerms: PermissionSet(PermRead), Description: "explore the workspace architecture"},
		{Marker: MarkerSlash, Name: "explain-decision", Kind: KindGlobal, RequiredPerms: PermissionSet(PermRead), Description: "explain why a tech stack was chosen"},
		{Marker: MarkerSlash, Name: "undo", Kind: KindGlobal, RequiredPerms: PermissionSet(PermWrite), Description: "rollback the most recent runtime mutation"},
		{Marker: MarkerSlash, Name: "commit", Kind: KindGlobal, RequiredPerms: PermissionSet(PermWrite), Description: "persist the runtime timeline into git"},
		{Marker: MarkerSlash, Name: "checkpoint", Kind: KindGlobal, RequiredPerms: PermissionSet(PermWrite), Description: "create or restore git checkpoints"},
		{Marker: MarkerSlash, Name: "quit", Kind: KindGlobal, RequiredPerms: PermissionSet(PermRead), Description: "exit the current session cleanly"},
		{Marker: MarkerSlash, Name: "copy", Kind: KindGlobal, RequiredPerms: PermissionSet(PermRead), Description: "copy the canonical transcript to clipboard"},
		{Marker: MarkerSlash, Name: "copy-mode", Kind: KindGlobal, RequiredPerms: PermissionSet(PermRead), Description: "enter scrollable inspection mode for precise fragment copy (j/k, / search, v/y)"},
		{Marker: MarkerSlash, Name: "inspect", Kind: KindGlobal, RequiredPerms: PermissionSet(PermRead), Description: "alias for /copy-mode"},

		// Directives ($).
		directive("prompt", CategoryActivation, PermissionSet(PermRead), true, "route a raw idea into /ask and refine it"),
		directive("env", CategoryObservation, PermissionSet(PermRead), true, "inspect the workspace environment (go, git, env)"),
		directive("trace", CategoryObservation, PermissionSet(PermAnalyze), true, "collect runtime execution information (race trace)"),
		directive("diagnose", CategoryObservation, PermissionSet(PermAnalyze), true, "produce a one-sentence root-cause diagnosis"),
		directive("log", CategoryObservation, PermissionSet(PermAnalyze), true, "evaluate a shell trace & run the implicit analysis pipeline"),
		directive("run", CategoryValidation, PermissionSet(PermExecute), true, "execute the application (go build)"),
		directive("test", CategoryValidation, PermissionSet(PermExecute), true, "run the project test suite"),
		directive("hot", CategoryMutation, PermissionSet(PermWrite), true, "fast targeted mutation for small localized edits"),
		directive("fix", CategoryMutation, PermissionSet(PermWrite|PermExecute), true, "structured implementation for larger fixes"),
		directive("inspect", CategoryObservation, PermissionSet(PermRead), true, "render the detailed execution telemetry timeline (op, stages, provider)"),
	}
}
