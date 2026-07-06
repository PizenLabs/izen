package ui

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

func (m *model) dismissSuggestions() {
	m.showSuggestions = false
	m.suggestionType = ""
	m.suggestions = nil
	m.suggestionIdx = 0
	m.syncAutocompleteFromSuggestions()
}

func (m *model) updateSuggestions() {
	current := m.input.String()
	if current == "" {
		m.dismissSuggestions()
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
func (m *model) dismissAutocomplete() {
	m.autocompleteActive = false
	m.autocompleteType = ""
	m.autocompleteItems = nil
	m.autocompleteIdx = 0
	m.dismissSuggestions()
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
// appends a trailing space, advances the cursor to the end of the buffer,
// then dismisses the dropdown.
func (m *model) completeAutocomplete() {
	if !m.autocompleteActive || len(m.autocompleteItems) == 0 {
		return
	}
	sel := m.autocompleteItems[m.autocompleteIdx]
	val := m.ti.Value()

	switch m.autocompleteType {
	case "file":
		atIdx := strings.LastIndex(val, "@")
		if atIdx >= 0 {
			newVal := val[:atIdx] + sel + " "
			m.ti.SetValue(newVal)
			m.ti.CursorEnd()
			m.pendingFileRefs = append(m.pendingFileRefs, sel)
			m.attachedFiles = append(m.attachedFiles, sel)
		}
	case "command":
		slashIdx := strings.LastIndex(val, "/")
		if slashIdx >= 0 {
			newVal := val[:slashIdx] + sel + " "
			m.ti.SetValue(newVal)
			m.ti.CursorEnd()
		}
	}

	m.autocompleteActive = false
	m.syncInputFromTI()
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
