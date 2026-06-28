package ui

import (
	"os"
	"path/filepath"
	"strings"
)

func shortenPath(p string) string {
	home, _ := os.UserHomeDir()
	if strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

func (m *model) expandFileRefs(line string) string {
	fields := strings.Fields(line)
	changed := false
	for i, field := range fields {
		if strings.HasPrefix(field, "@") {
			ref := filepath.Clean(field[1:])
			if ref == "" || ref == "." {
				continue
			}
			if _, err := os.Stat(ref); err == nil {
				fields[i] = ref
				changed = true
				continue
			}
			matches, err := filepath.Glob(ref)
			if err == nil && len(matches) > 0 {
				fields[i] = matches[0]
				changed = true
				continue
			}
			if _, err := os.Stat(field[1:]); err == nil {
				fields[i] = field[1:]
				changed = true
				continue
			}
			m.push(roleSystem, infoStyle.Render("warn: @"+field[1:]+" not found — sending as literal"))
			fields[i] = field[1:]
			changed = true
		}
	}
	if changed {
		return strings.Join(fields, " ")
	}
	return line
}
