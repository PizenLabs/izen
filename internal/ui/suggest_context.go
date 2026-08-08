package ui

import (
	"sort"
	"strings"

	"github.com/PizenLabs/izen/internal/modes"
	"github.com/PizenLabs/izen/internal/parser"
	"github.com/PizenLabs/izen/pkg/domain/command"
)

// CursorContext is the lexical context around the caret in the prompt input
// line. It is the sole input to the suggest engine: which marker family is
// being edited, what fragment is typed so far, and which workspace gates the
// projection.
type CursorContext struct {
	// ActiveMarker is the marker rune introducing the token being edited:
	// '/', '$', '@', or 0 when the caret sits in a plain word fragment.
	ActiveMarker rune
	// PartialTerm is the current word fragment being typed, marker excluded
	// ("" when the caret sits immediately after a fresh marker).
	PartialTerm string
	// ActiveWorkspace is the workspace gating directive suggestions. It is
	// derived from a '/workspace' token declared in the input line when
	// present, else the active session workspace.
	ActiveWorkspace command.WorkspaceType
}

// analyzeCursor extracts the CursorContext for the input line at cursorIdx (a
// byte offset into input). Markers are recognized ANYWHERE in the line: the
// token being edited is the token that reaches the caret, determined by
// lexing the prefix with the Phase 2 lexer. A marker immediately before the
// caret wins over earlier ones, so "/build$" with the caret after '$' yields
// ActiveMarker '$'. When the caret sits past a completed token (whitespace
// between token and caret) or inside a plain word, no marker is active.
func analyzeCursor(input string, cursorIdx int) CursorContext {
	if cursorIdx > len(input) {
		cursorIdx = len(input)
	}
	if cursorIdx < 0 {
		cursorIdx = 0
	}
	toks := parser.Tokenize(input[:cursorIdx])
	last := toks[len(toks)-1]
	if last.Kind == parser.TokenEOF {
		if len(toks) < 2 {
			return CursorContext{}
		}
		last = toks[len(toks)-2]
	}
	if last.Kind != parser.TokenCommand || tokenByteEnd(last) != cursorIdx {
		return CursorContext{}
	}
	return CursorContext{
		ActiveMarker: last.Marker,
		PartialTerm:  last.Name,
	}
}

// tokenByteEnd returns the byte offset just past the token in the input.
func tokenByteEnd(tok parser.Token) int {
	switch tok.Kind {
	case parser.TokenCommand:
		return tok.Pos.Offset + 1 + len(tok.Name)
	case parser.TokenWord:
		return tok.Pos.Offset + len(tok.Text)
	default:
		return tok.Pos.Offset
	}
}

// workspaceForMode maps the session mode to its command-domain workspace.
func workspaceForMode(m modes.Mode) command.WorkspaceType {
	switch m {
	case modes.ModeAsk:
		return command.WorkspaceAsk
	case modes.ModeInvestigate:
		return command.WorkspaceInvestigate
	case modes.ModePlan:
		return command.WorkspacePlan
	case modes.ModeBuild:
		return command.WorkspaceBuild
	case modes.ModeReview:
		return command.WorkspaceReview
	default:
		return command.WorkspaceAsk
	}
}

// modeForWorkspace maps a command-domain workspace back to the session mode.
// It is the inverse of workspaceForMode and drives the workspace transition
// the deterministic parser pipeline performs when an intent declares a
// /workspace marker different from the active session mode.
func modeForWorkspace(ws command.WorkspaceType) modes.Mode {
	switch ws {
	case command.WorkspaceAsk:
		return modes.ModeAsk
	case command.WorkspaceInvestigate:
		return modes.ModeInvestigate
	case command.WorkspacePlan:
		return modes.ModePlan
	case command.WorkspaceBuild:
		return modes.ModeBuild
	case command.WorkspaceReview:
		return modes.ModeReview
	default:
		return modes.ModeAsk
	}
}

// workspaceFromName maps a canonical workspace name back to its type.
func workspaceFromName(name string) (command.WorkspaceType, bool) {
	for _, ws := range []command.WorkspaceType{
		command.WorkspaceAsk,
		command.WorkspaceInvestigate,
		command.WorkspacePlan,
		command.WorkspaceBuild,
		command.WorkspaceReview,
	} {
		if ws.String() == name {
			return ws, true
		}
	}
	return 0, false
}

// workspaceFromInput extracts the workspace declared by a '/' workspace token
// anywhere in the input line (order-independent, matching the parser's
// grammar). It reports false when the line declares none. Global commands
// (/help) do not select a workspace.
func workspaceFromInput(input string, reg *command.Registry) (command.WorkspaceType, bool) {
	if reg == nil {
		reg = command.Default()
	}
	var found command.WorkspaceType
	var ok bool
	for _, tok := range parser.Tokenize(input) {
		if tok.Kind != parser.TokenCommand || tok.Marker != command.MarkerSlash {
			continue
		}
		d, exists := reg.Lookup(command.MarkerSlash, tok.Name)
		if !exists || d.Kind != command.KindWorkspace {
			continue
		}
		if ws, exists := workspaceFromName(tok.Name); exists {
			found = ws
			ok = true
		}
	}
	return found, ok
}

// activeWorkspaceFor resolves the effective workspace for the input line: a
// '/workspace' token declared in the line wins; otherwise the session's
// active workspace applies.
func (m *model) activeWorkspaceFor(input string) command.WorkspaceType {
	if ws, ok := workspaceFromInput(input, command.Default()); ok {
		return ws
	}
	if m != nil && m.resolver != nil {
		return workspaceForMode(m.resolver.Current())
	}
	return command.WorkspaceAsk
}

// SuggestionKind classifies a projected suggestion for menu grouping.
type SuggestionKind int

const (
	// SuggestionWorkspace is a '/' workflow context (/build).
	SuggestionWorkspace SuggestionKind = iota
	// SuggestionGlobal is a '/' global command (/help).
	SuggestionGlobal
	// SuggestionDirective is a '$' capability ($hot).
	SuggestionDirective
	// SuggestionScope is an '@' target (@internal/auth.go).
	SuggestionScope
)

// Suggestion is one projected option in the Context Selection menu. Token is
// the literal text (marker included) inserted into the prompt when selected.
type Suggestion struct {
	// Token is the text inserted at the caret on selection, e.g. "/build" or
	// "$fix". It always carries its marker.
	Token string
	// Label is the display text shown in the menu row.
	Label string
	// Detail is an optional dimmed annotation rendered beside the label.
	Detail string
	// Kind groups the item in the menu.
	Kind SuggestionKind
	// Category is the DirectiveCategory label for $ suggestions; empty
	// otherwise.
	Category string
	// Descriptor is the underlying registry metadata; nil for scopes.
	Descriptor *command.CommandDescriptor
}

// matchPartial reports whether a name matches the typed fragment: a prefix
// match or a subsequence (fuzzy) match. An empty fragment matches everything.
func matchPartial(partial, name string) bool {
	if partial == "" {
		return true
	}
	if strings.HasPrefix(name, partial) {
		return true
	}
	return fuzzyMatch(partial, name)
}

// projectSlashSuggestions projects the '/' surface — workspace contexts and
// global commands — straight from the registry. Workspace switchers are always
// available (context switching stays open in every workspace); global commands
// are filtered through the active workspace's permission set, so mutation
// commands like /undo, /commit, and /checkpoint vanish in read-only
// workspaces. Workspaces sort first, then global commands, each alphabetically.
func projectSlashSuggestions(reg *command.Registry, ws command.WorkspaceType, partial string) []Suggestion {
	if reg == nil {
		reg = command.Default()
	}
	perms := ws.Permissions()
	var workspaces, globals []Suggestion
	for _, d := range reg.All() {
		if d.Marker != command.MarkerSlash {
			continue
		}
		if !matchPartial(strings.ToLower(partial), d.Name) {
			continue
		}
		if d.Kind == command.KindGlobal && !perms.Contains(d.RequiredPerms) {
			continue
		}
		dd := d
		s := Suggestion{
			Token:      "/" + d.Name,
			Label:      "/" + d.Name,
			Detail:     d.Description,
			Descriptor: &dd,
		}
		switch d.Kind {
		case command.KindWorkspace:
			s.Kind = SuggestionWorkspace
			workspaces = append(workspaces, s)
		case command.KindGlobal:
			s.Kind = SuggestionGlobal
			globals = append(globals, s)
		}
	}
	sort.Slice(workspaces, func(i, j int) bool { return workspaces[i].Label < workspaces[j].Label })
	sort.Slice(globals, func(i, j int) bool { return globals[i].Label < globals[j].Label })
	return append(workspaces, globals...)
}

// projectDollarSuggestions projects the '$' surface for a workspace: only the
// directives whose RequiredPerms are a subset of the workspace's permission
// set (registry.GetAllowedDirectives). Base directives are rendered without
// flags, e.g. "$fix" — never "fix --apply".
func projectDollarSuggestions(reg *command.Registry, ws command.WorkspaceType, partial string) []Suggestion {
	if reg == nil {
		reg = command.Default()
	}
	var out []Suggestion
	for _, d := range reg.GetAllowedDirectives(ws) {
		if !matchPartial(strings.ToLower(partial), d.Name) {
			continue
		}
		dd := d
		out = append(out, Suggestion{
			Token:      "$" + d.Name,
			Label:      "$" + d.Name,
			Detail:     d.Description,
			Kind:       SuggestionDirective,
			Category:   d.Category,
			Descriptor: &dd,
		})
	}
	return out
}

// scopeSuggestLimit caps the combined '@' projection so the menu never floods.
const scopeSuggestLimit = 20

// projectAtSuggestions projects the '@' surface: git-modified files first
// (from the repo status), then a filesystem walk of the workspace, matched
// against the partial path fragment.
func projectAtSuggestions(partial string, modified []string) []Suggestion {
	seen := map[string]bool{}
	var out []Suggestion
	for _, path := range modified {
		path = strings.TrimPrefix(path, "./")
		if path == "" || seen[path] || !matchPartial(strings.ToLower(partial), path) {
			continue
		}
		seen[path] = true
		out = append(out, Suggestion{
			Token:  "@" + path,
			Label:  path,
			Detail: "modified",
			Kind:   SuggestionScope,
		})
	}
	for _, path := range filterFilesRecursive(partial) {
		if seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, Suggestion{
			Token: "@" + path,
			Label: path,
			Kind:  SuggestionScope,
		})
	}
	if len(out) > scopeSuggestLimit {
		out = out[:scopeSuggestLimit]
	}
	return out
}

// gitModifiedPaths returns the worktree-modified file paths from the git
// engine, or nil when git state is unavailable.
func (m *model) gitModifiedPaths() []string {
	if m == nil || m.gitEng == nil {
		return nil
	}
	entries, err := m.gitEng.Status()
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Path != "" {
			out = append(out, e.Path)
		}
	}
	return out
}

// SuggestionSection is a titled cluster of suggestions in the dropdown.
type SuggestionSection struct {
	Title string
	Items []Suggestion
}

// directiveCategoryOrder is the canonical display order for '$' groups:
// mutation, validation, observation, activation.
var directiveCategoryOrder = []string{
	command.CategoryMutation.String(),
	command.CategoryValidation.String(),
	command.CategoryObservation.String(),
	command.CategoryActivation.String(),
}

// buildSuggestionSections partitions the visible suggestions into titled
// sections. '/' items split into workspace contexts and global commands; '$'
// items split by DirectiveCategory in the canonical order.
func buildSuggestionSections(items []Suggestion) []SuggestionSection {
	var workspaces, globals []Suggestion
	cats := map[string][]Suggestion{}
	var extra []string
	for _, it := range items {
		switch it.Kind {
		case SuggestionWorkspace:
			workspaces = append(workspaces, it)
		case SuggestionGlobal:
			globals = append(globals, it)
		case SuggestionDirective:
			if _, ok := cats[it.Category]; !ok {
				if !orderedCategory(it.Category) {
					extra = append(extra, it.Category)
				}
			}
			cats[it.Category] = append(cats[it.Category], it)
		}
	}
	var sections []SuggestionSection
	if len(workspaces) > 0 {
		sections = append(sections, SuggestionSection{Title: "WORKSPACE CONTEXTS", Items: workspaces})
	}
	if len(globals) > 0 {
		sections = append(sections, SuggestionSection{Title: "GLOBAL COMMANDS", Items: globals})
	}
	for _, cat := range directiveCategoryOrder {
		if items, ok := cats[cat]; ok && len(items) > 0 {
			sections = append(sections, SuggestionSection{Title: strings.ToUpper(cat), Items: items})
		}
	}
	for _, cat := range extra {
		if items, ok := cats[cat]; ok && len(items) > 0 {
			sections = append(sections, SuggestionSection{Title: strings.ToUpper(cat), Items: items})
		}
	}
	return sections
}

func orderedCategory(cat string) bool {
	for _, c := range directiveCategoryOrder {
		if c == cat {
			return true
		}
	}
	return false
}
