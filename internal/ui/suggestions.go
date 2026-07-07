package ui

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/internal/modes"
)

func (m *model) dismissSuggestions() {
	m.showSuggestions = false
	m.suggestionType = ""
	m.suggestions = nil
	m.suggestionIdx = 0
	m.syncAutocompleteFromSuggestions()
	m.recalcViewportHeight()
}

func (m *model) updateSuggestions() {
	current := m.input.String()
	if current == "" {
		m.dismissSuggestions()
		return
	}
	if strings.HasPrefix(current, "$") {
		m.showSuggestions = true
		m.suggestionType = "$"
		m.suggestions = m.filterDollarCommands(current[1:])
		m.suggestionIdx = 0
		if len(m.suggestions) == 1 && "$"+m.suggestions[0] == current {
			m.showSuggestions = false
		}
		m.syncAutocompleteFromSuggestions()
		if m.autocompleteActive {
			m.recalcViewportHeight()
		}
		return
	}

	if strings.HasPrefix(current, "/") {
		m.showSuggestions = true
		m.suggestionType = "/"
		m.suggestions = m.filterCommands(current[1:])
		m.suggestionIdx = 0
		if len(m.suggestions) == 1 && m.suggestions[0] == current {
			m.showSuggestions = false
		}
		m.syncAutocompleteFromSuggestions()
		if m.autocompleteActive {
			m.recalcViewportHeight()
		}
		return
	}
	atIdx := strings.LastIndex(current, "@")
	if atIdx >= 0 {
		prefix := current[atIdx+1:]
		if !strings.Contains(prefix, " ") {
			m.showSuggestions = true
			m.suggestionType = "@"
			m.suggestions = filterFilesRecursive(prefix)
			m.suggestionIdx = 0
			if len(m.suggestions) == 1 && m.suggestions[0] == prefix {
				m.showSuggestions = false
			}
			m.syncAutocompleteFromSuggestions()
			if m.autocompleteActive {
				m.recalcViewportHeight()
			}
			return
		}
	}
	m.dismissSuggestions()
}

// syncAutocompleteFromSuggestions bridges the old suggestion system to the new
// Prompt Sandwich autocomplete state so the dropdown renderer can read from
// autocompleteActive / autocompleteItems / autocompleteIdx directly.
func (m *model) syncAutocompleteFromSuggestions() {
	m.autocompleteActive = m.showSuggestions
	m.autocompleteType = m.suggestionType
	m.autocompleteItems = m.suggestions
	m.autocompleteIdx = m.suggestionIdx
}

// dismissAutocomplete cleanly closes the dropdown and clears both state systems.
// Restores viewport height that was reserved for the dropdown.
func (m *model) dismissAutocomplete() {
	m.autocompleteActive = false
	m.autocompleteType = ""
	m.autocompleteItems = nil
	m.autocompleteIdx = 0
	m.dismissSuggestions()
	m.recalcViewportHeight()
}

// navigateAutocomplete moves the dropdown highlight by dir (+1 or -1).
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

// completeAutocomplete replaces the input buffer with the highlighted item,
// using cursor-aware backward scanning to find the trigger (@ or /).
// For @-files: prepends @ to the selected path. For /-commands: uses the
// selection as-is (already contains /). Preserves text after cursor.
func (m *model) completeAutocomplete() {
	if !m.autocompleteActive || len(m.autocompleteItems) == 0 {
		return
	}
	sel := m.autocompleteItems[m.autocompleteIdx]
	val := m.ti.Value()
	cursorIdx := m.ti.Position()

	triggerIdx := -1
	var activeTrigger byte
	for i := cursorIdx - 1; i >= 0; i-- {
		if val[i] == '@' || val[i] == '/' || val[i] == '$' {
			triggerIdx = i
			activeTrigger = val[i]
			break
		}
	}
	if triggerIdx < 0 {
		return
	}

	var selectedToken string
	switch activeTrigger {
	case '@':
		selectedToken = "@" + sel
		m.pendingFileRefs = append(m.pendingFileRefs, sel)
		m.attachedFiles = append(m.attachedFiles, sel)
	case '$':
		selectedToken = "$" + sel
	default:
		selectedToken = sel
	}

	beforeTrigger := val[:triggerIdx]
	afterCursor := val[cursorIdx:]
	newVal := beforeTrigger + selectedToken + " " + afterCursor
	m.ti.SetValue(newVal)
	m.ti.SetCursor(len(beforeTrigger + selectedToken + " "))

	m.autocompleteActive = false
	m.syncInputFromTI()
	m.recalcViewportHeight()
}

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

func (m *model) filterCommands(prefix string) []string {
	var result []string
	matches := func(cmd string) bool {
		if prefix == "" {
			return true
		}
		cmdName := strings.TrimPrefix(cmd, "/")
		return strings.HasPrefix(cmdName, prefix) || fuzzyMatch(prefix, cmdName)
	}
	currentMode := m.resolver.Current()
	for _, c := range coreModes {
		if matches(c) {
			result = append(result, c)
		}
	}
	for _, c := range utilityCommands[currentMode] {
		// Strictly hide build-only commands unless in build mode.
		if (c == "/undo" || c == "/commit" || c == "/checkpoint") && currentMode != modes.ModeBuild {
			continue
		}
		if matches(c) {
			result = append(result, c)
		}
	}
	for _, c := range globalCommands {
		if matches(c) {
			result = append(result, c)
		}
	}
	return result
}

func (m *model) filterDollarCommands(prefix string) []string {
	mode := m.resolver.Current()
	var candidates []string
	switch mode {
	case modes.ModeReview:
		candidates = []string{"test", "run", "fix"}
	case modes.ModeInvestigate:
		candidates = []string{"env", "trace", "diagnose"}
	default:
		return nil
	}
	if prefix == "" {
		return candidates
	}
	var result []string
	for _, c := range candidates {
		if strings.HasPrefix(c, prefix) {
			result = append(result, c)
		}
	}
	return result
}

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
