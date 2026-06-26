package ui

import "strings"

var goKeywords = map[string]bool{
	"func": true, "var": true, "const": true, "type": true, "struct": true,
	"interface": true, "map": true, "chan": true, "go": true, "defer": true,
	"return": true, "if": true, "else": true, "for": true, "range": true,
	"switch": true, "case": true, "default": true, "break": true, "continue": true,
	"package": true, "import": true, "select": true, "nil": true, "true": true,
	"false": true, "error": true, "string": true, "int": true, "bool": true,
	"make": true, "new": true, "append": true, "len": true, "cap": true,
	"delete": true, "close": true, "goroutine": true, "fallthrough": true,
}

var shKeywords = map[string]bool{
	"echo": true, "cd": true, "ls": true, "mkdir": true, "rm": true,
	"cat": true, "grep": true, "sed": true, "awk": true, "curl": true,
	"export": true, "source": true, "sudo": true, "chmod": true,
	"git": true, "go": true, "make": true, "docker": true,
}

var goTypes = map[string]bool{
	"string": true, "int": true, "int8": true, "int16": true, "int32": true,
	"int64": true, "uint": true, "float32": true, "float64": true, "byte": true,
	"rune": true, "bool": true, "error": true, "any": true,
}

func colorTokens(line string) string {
	if idx := strings.Index(line, "//"); idx >= 0 {
		return line[:idx] + hlComment.Render(line[idx:])
	}
	if strings.HasPrefix(strings.TrimSpace(line), "#") {
		return hlComment.Render(line)
	}
	if strings.ContainsAny(line, "\"'`") {
		return hlString.Render(line)
	}
	words := strings.Fields(line)
	out := make([]string, len(words))
	for i, w := range words {
		clean := strings.Trim(w, "(),;:{}&*[]")
		switch {
		case goTypes[clean]:
			out[i] = strings.Replace(w, clean, hlType.Render(clean), 1)
		case goKeywords[clean] || shKeywords[clean]:
			out[i] = strings.Replace(w, clean, hlKeyword.Render(clean), 1)
		case len(clean) > 0 && clean[0] >= '0' && clean[0] <= '9':
			out[i] = strings.Replace(w, clean, hlNumber.Render(clean), 1)
		default:
			out[i] = w
		}
	}
	return strings.Join(out, " ")
}

func highlightCode(lines []string) []string {
	result := make([]string, 0, len(lines))
	inBlock := false
	lang := ""

	for _, line := range lines {
		if !inBlock {
			if strings.HasPrefix(line, "```") {
				inBlock = true
				lang = strings.TrimPrefix(line, "```")
				tag := ""
				if lang != "" {
					tag = "  " + hlLang.Render(lang)
				}
				result = append(result, hlCodeBg.Render("  ╾──"+tag))
				continue
			}
			result = append(result, line)
			continue
		}
		if strings.HasPrefix(line, "```") {
			inBlock = false
			lang = ""
			result = append(result, hlCodeBg.Render("  ╼──"))
			continue
		}
		_ = lang
		result = append(result, hlCodeBg.Render("  │ ")+colorTokens(line))
	}
	if inBlock {
		result = append(result, hlCodeBg.Render("  ╼──"))
	}
	return result
}
