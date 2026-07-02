package ui

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

func (m *model) dismissSuggestions() {
	prevHeight := m.suggestionPaletteHeight()

	m.showSuggestions = false
	m.suggestionType = ""
	m.suggestions = nil
	m.suggestionIdx = 0

	if m.vpReady && prevHeight != m.suggestionPaletteHeight() {
		m.rebuildViewport()
	}
}

func (m *model) updateSuggestions() {
	prevHeight := m.suggestionPaletteHeight()

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
		if m.vpReady && prevHeight != m.suggestionPaletteHeight() {
			m.rebuildViewport()
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
			if m.vpReady && prevHeight != m.suggestionPaletteHeight() {
				m.rebuildViewport()
			}
			return
		}
	}
	m.dismissSuggestions()
}

func (m *model) filterCommands(prefix string) []string {
	var result []string
	matches := func(cmd string) bool {
		return prefix == "" || strings.HasPrefix(cmd, "/"+prefix)
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
			return nil
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

		rel := path
		if strings.HasPrefix(rel, "./") {
			rel = rel[2:]
		}

		if prefix == "" || strings.HasPrefix(rel, prefix) || strings.Contains(strings.ToLower(rel), strings.ToLower(prefix)) {
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

func (m *model) renderSuggestions(width int) string {
	maxVisible := 6
	items := m.suggestions
	if len(items) > maxVisible {
		items = items[:maxVisible]
	}

	var inner strings.Builder

	if m.suggestionType == "@" {
		maxBase := 0
		for _, s := range items {
			if n := len(filepath.Base(s)); n > maxBase {
				maxBase = n
			}
		}
		gap := 2
		if maxBase < 10 {
			maxBase = 10
		}

		inner.WriteString(paletteSectionStyle.Render("files"))
		inner.WriteString("\n")
		for i, s := range items {
			base := filepath.Base(s)
			dir := s
			if filepath.Dir(s) == "." {
				dir = ""
			}
			padded := base + strings.Repeat(" ", maxBase-len(base)+gap)

			if i == m.suggestionIdx {
				inner.WriteString(paletteSelectedStyle.Render("❯ " + padded))
				if dir != "" {
					inner.WriteString(paletteSelectedPath.Render(dir))
				}
			} else {
				inner.WriteString(paletteItemStyle.Render("  " + padded))
				if dir != "" {
					inner.WriteString(palettePathStyle.Render(dir))
				}
			}
			inner.WriteString("\n")
		}
	} else {
		inner.WriteString(paletteSectionStyle.Render("commands"))
		inner.WriteString("\n")

		prevCat := ""
		for i, s := range items {
			cat := cmdCategory(s)
			if cat != prevCat {
				prevCat = cat
				var label string
				switch cat {
				case "core":
					label = "modes"
				case "utility":
					label = m.resolver.Current().String()
				case "global":
					label = "global"
				}
				inner.WriteString(paletteSectionStyle.Render("  " + label))
				inner.WriteString("\n")
			}

			baseStyle := paletteItemStyle
			if cat == "core" {
				baseStyle = paletteCoreItemStyle
			}
			if i == m.suggestionIdx {
				inner.WriteString(paletteSelectedStyle.Render(" ❯ " + s))
			} else {
				inner.WriteString(baseStyle.Render("   " + s))
			}
			inner.WriteString("\n")
		}
	}

	inner.WriteString(paletteHintStyle.Render("tab · enter · esc"))

	boxWidth := 48
	if width < boxWidth+4 {
		boxWidth = width - 4
	}
	return paletteBoxStyle.Width(boxWidth).Render(inner.String())
}
