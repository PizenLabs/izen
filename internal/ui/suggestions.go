package ui

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/pkg/domain/command"
)

func (m *model) dismissSuggestions() {
	m.showSuggestions = false
	m.suggestionType = ""
	m.suggestions = nil
	m.suggestionIdx = 0
	m.syncAutocompleteFromSuggestions()
	m.recalcViewportHeight()
}

// updateSuggestions re-projects the Context Selection menu from the registry
// based on the cursor context. It recognizes markers ANYWHERE in the line: the
// token being edited determines the marker family, and the workspace declared
// in the line (or the active session workspace) gates '$' directives.
func (m *model) updateSuggestions() {
	current := m.input.String()
	if current == "" {
		m.dismissSuggestions()
		return
	}

	cursorIdx := m.ti.Position()
	if cursorIdx > len(current) {
		cursorIdx = len(current)
	}

	ctx := analyzeCursor(current, cursorIdx)
	ctx.ActiveWorkspace = m.activeWorkspaceFor(current)

	var items []Suggestion
	switch ctx.ActiveMarker {
	case command.MarkerAt:
		items = projectAtSuggestions(ctx.PartialTerm, m.gitModifiedPaths())
	case command.MarkerDollar:
		items = projectDollarSuggestions(command.Default(), ctx.ActiveWorkspace, ctx.PartialTerm)
	case command.MarkerSlash:
		items = projectSlashSuggestions(command.Default(), ctx.ActiveWorkspace, ctx.PartialTerm)
	default:
		m.dismissSuggestions()
		return
	}

	if len(items) == 0 {
		m.dismissSuggestions()
		return
	}
	if len(items) == 1 && items[0].Token == string(ctx.ActiveMarker)+ctx.PartialTerm {
		m.dismissSuggestions()
		return
	}

	m.showSuggestions = true
	m.suggestionType = suggestionTypeFor(ctx.ActiveMarker)
	m.suggestions = items
	m.suggestionIdx = 0
	m.syncAutocompleteFromSuggestions()
	if m.autocompleteActive {
		m.recalcViewportHeight()
	}
}

// suggestionTypeFor maps an active marker to the renderer layout selector:
// "scope" renders the two-column file rows, everything else renders grouped
// command sections.
func suggestionTypeFor(marker rune) string {
	switch marker {
	case command.MarkerAt:
		return "scope"
	case command.MarkerDollar:
		return "directive"
	default:
		return "command"
	}
}

// syncAutocompleteFromSuggestions bridges the suggestion state to the Prompt
// Sandwich autocomplete state so the dropdown renderer reads from
// autocompleteActive / autocompleteItems / autocompleteIdx directly.
func (m *model) syncAutocompleteFromSuggestions() {
	m.autocompleteActive = m.showSuggestions
	m.autocompleteType = m.suggestionType
	m.autocompleteItems = m.suggestions
	m.autocompleteIdx = m.suggestionIdx
}

// dismissAutocomplete cleanly closes the dropdown and clears both state
// systems. Restores viewport height that was reserved for the dropdown.
func (m *model) dismissAutocomplete() {
	m.autocompleteActive = false
	m.autocompleteType = ""
	m.autocompleteItems = nil
	m.autocompleteIdx = 0
	m.dismissSuggestions()
	m.recalcViewportHeight()
}

// navigateAutocomplete moves the dropdown highlight by dir (+1 or -1),
// wrapping around the full item list.
func (m *model) navigateAutocomplete(dir int) {
	if !m.autocompleteActive || len(m.autocompleteItems) == 0 {
		return
	}
	total := len(m.autocompleteItems)
	m.autocompleteIdx = (m.autocompleteIdx + dir) % total
	if m.autocompleteIdx < 0 {
		m.autocompleteIdx += total
	}
}

// completeAutocomplete replaces the partial token under the caret with the
// highlighted suggestion's Token (marker included), preserving all text before
// and after the caret. A trailing space is ALWAYS appended so the user can
// keep typing without tokens sticking together ("$fix check in @main" →
// "$fix check in @main.go "). The one exception is a whole-line completion
// committed via Enter (commit=true and the token occupies the entire line): no
// trailing space is added and the method returns true so the caller submits
// the completed command immediately ("/q" → "/quit" executes instead of
// dumping "unknown command: /q").
func (m *model) completeAutocomplete(commit bool) bool {
	if !m.autocompleteActive || len(m.autocompleteItems) == 0 {
		return false
	}
	sel := m.autocompleteItems[m.autocompleteIdx]
	val := m.ti.Value()
	cursorIdx := m.ti.Position()
	if cursorIdx > len(val) {
		cursorIdx = len(val)
	}

	ctx := analyzeCursor(val, cursorIdx)
	if ctx.ActiveMarker == 0 {
		return false
	}
	markerIdx := cursorIdx - len(ctx.PartialTerm) - 1
	if markerIdx < 0 {
		return false
	}

	if ctx.ActiveMarker == command.MarkerAt {
		target := strings.TrimPrefix(sel.Token, "@")
		m.pendingFileRefs = append(m.pendingFileRefs, target)
		m.attachedFiles = append(m.attachedFiles, target)
	}

	beforeTrigger := val[:markerIdx]
	afterCursor := val[cursorIdx:]

	wholeLine := strings.TrimSpace(beforeTrigger) == "" && strings.TrimSpace(afterCursor) == ""
	// Commit-and-execute only when the completed token is a unique command
	// suggestion occupying the entire line ("/q" → "/quit"). Ambiguous prefixes
	// and mid-sentence completions just complete with a trailing space.
	commitLine := commit && wholeLine && m.autocompleteType == "command" && len(m.autocompleteItems) == 1
	sep := " "
	if commitLine {
		sep = ""
	}
	newVal := beforeTrigger + sel.Token + sep + afterCursor
	m.ti.SetValue(newVal)
	m.ti.SetCursor(len(beforeTrigger + sel.Token + sep))

	m.autocompleteActive = false
	m.syncInputFromTI()
	m.recalcViewportHeight()
	return commitLine
}

// fuzzyMatch reports whether the pattern's runes appear, in order, within the
// target (subsequence matching).
func fuzzyMatch(pattern, target string) bool {
	pattern = strings.ToLower(pattern)
	target = strings.ToLower(target)
	pi := 0
	for ti := 0; pi < len(pattern) && ti < len(target); ti++ {
		if pattern[pi] == target[ti] {
			pi++
		}
	}
	return pi == len(pattern)
}

// filterFilesRecursive walks the workspace and returns file paths matching
// the prefix fragment, capped at a small limit and sorted with exact prefix
// matches first.
func filterFilesRecursive(prefix string) []string {
	const limit = 20

	prefix = strings.TrimPrefix(prefix, "./")

	searchDir := "."
	if idx := strings.LastIndex(prefix, "/"); idx >= 0 {
		searchDir = prefix[:idx]
		if searchDir == "" {
			searchDir = "."
		}
	}

	var results []string
	_ = filepath.WalkDir(searchDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if len(results) >= limit {
			return filepath.SkipAll
		}

		name := d.Name()
		if name == "." {
			return nil
		}
		if strings.HasPrefix(name, ".") {
			if d.IsDir() {
				switch name {
				case ".git", ".svn", ".DS_Store", ".izen":
					return filepath.SkipDir
				}
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			switch name {
			case "vendor", "node_modules", "dist", "build", "__pycache__", "target", ".next":
				return filepath.SkipDir
			}
			return nil
		}

		rel := strings.TrimPrefix(path, "./")

		if prefix == "" || strings.HasPrefix(rel, prefix) || strings.Contains(strings.ToLower(rel), strings.ToLower(prefix)) || fuzzyMatch(prefix, rel) {
			results = append(results, rel)
		}
		return nil
	})

	sort.Slice(results, func(i, j int) bool {
		iExact := strings.HasPrefix(results[i], prefix)
		jExact := strings.HasPrefix(results[j], prefix)
		if iExact != jExact {
			return iExact
		}
		return len(results[i]) < len(results[j])
	})

	if len(results) > limit {
		results = results[:limit]
	}
	return results
}
