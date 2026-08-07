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
// official Izen command, workspace, and directive and answers two questions:
//
//   - which directives a workspace may invoke (GetAllowedDirectives), and
//   - whether a marker+name token names a known descriptor (Lookup).
type Registry struct {
	mu      sync.RWMutex
	entries map[descriptorKey]CommandDescriptor
}

// NewRegistry returns an empty, thread-safe Registry.
func NewRegistry() *Registry {
	return &Registry{entries: make(map[descriptorKey]CommandDescriptor)}
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

// Lookup returns a copy of the descriptor registered under marker and name.
// Name matching is case-insensitive. It reports false when no descriptor
// matches.
func (r *Registry) Lookup(marker rune, name string) (*CommandDescriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.entries[descriptorKey{marker: marker, name: strings.ToLower(name)}]
	if !ok {
		return nil, false
	}
	cp := d
	return &cp, true
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

// NewDefault builds a fresh Registry pre-loaded with every official Izen
// command, workspace, and directive.
func NewDefault() *Registry {
	r := NewRegistry()
	r.RegisterAll(officialCommands())
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

		// Directives ($).
		directive("prompt", CategoryActivation, PermissionSet(PermRead), true, "route a raw idea into /ask and refine it"),
		directive("env", CategoryObservation, PermissionSet(PermRead), true, "inspect the workspace environment (go, git, env)"),
		directive("trace", CategoryObservation, PermissionSet(PermAnalyze), true, "collect runtime execution information (race trace)"),
		directive("diagnose", CategoryObservation, PermissionSet(PermAnalyze), true, "produce a one-sentence root-cause diagnosis"),
		directive("run", CategoryValidation, PermissionSet(PermExecute), true, "execute the application (go build)"),
		directive("test", CategoryValidation, PermissionSet(PermExecute), true, "run the project test suite"),
		directive("hot", CategoryMutation, PermissionSet(PermWrite), true, "fast targeted mutation for small localized edits"),
		directive("fix", CategoryMutation, PermissionSet(PermWrite|PermExecute), true, "structured implementation for larger fixes"),
	}
}
