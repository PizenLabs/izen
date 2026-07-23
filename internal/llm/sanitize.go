package llm

import (
	"strings"
)

func SanitizeOutput(s string) string {
	s = strings.TrimSpace(s)
	lines := strings.Split(s, "\n")

	if len(lines) >= 2 && strings.HasPrefix(lines[0], "```") && strings.HasPrefix(lines[len(lines)-1], "```") {
		lines = lines[1 : len(lines)-1]
	} else if len(lines) >= 2 && strings.HasPrefix(lines[0], "```") && lines[len(lines)-1] == "```" {
		lines = lines[1 : len(lines)-1]
	}

	result := strings.Join(lines, "\n")
	result = strings.TrimSpace(result)
	return result
}

func SanitizeOutputWithLang(s string) (string, string) {
	s = strings.TrimSpace(s)
	lines := strings.Split(s, "\n")

	lang := ""

	if len(lines) >= 2 && strings.HasPrefix(lines[0], "```") {
		first := strings.TrimSpace(lines[0][3:])
		if first != "" && !strings.Contains(first, " ") {
			lang = first
		}
		last := strings.TrimSpace(lines[len(lines)-1])
		if last == "```" || strings.HasPrefix(last, "```") {
			lines = lines[1 : len(lines)-1]
		} else {
			lines = lines[1:]
		}
	}

	result := strings.Join(lines, "\n")
	result = strings.TrimSpace(result)
	return result, lang
}
