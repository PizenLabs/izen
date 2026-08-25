package planner

import (
	"path/filepath"
	"strings"
)

// ── Block decomposer (HTML / Markdown / Config) ─────────────────────────────
//
// Block splitting cuts documents without brace structure into their natural
// top-level units:
//
//	Markdown   headings (# …) outside fenced code blocks
//	HTML       top-level elements (tag-depth walk; script/style raw text
//	           respected, void elements and comments never nest)
//	Config     TOML/INI [section] headers, YAML top-level keys and ---
//	           document separators, JSON root-object members
//
// The same guarantees hold as for AST splitting: contiguous ordered coverage,
// no block ever split internally, deterministic output.

// BlockDecomposer implements Decomposer for markup and configuration formats.
type BlockDecomposer struct{}

// Supports reports whether the target is a block-splittable format.
func (BlockDecomposer) Supports(target string) bool {
	return blockFormatFor(target) != formatUnknown
}

// Split partitions the source into top-level block sections.
func (d BlockDecomposer) Split(target string, source []byte) ([]Section, error) {
	lines := splitKeepNewline(source)
	var starts []int
	var label func(line []byte, i int) string
	switch blockFormatFor(target) {
	case formatMarkdown:
		starts = mdSectionStarts(lines)
		label = mdLabel
	case formatHTML:
		starts = htmlTopLevelStarts(lines)
		label = htmlLabel
	default: // formatConfig
		starts = configSectionStarts(target, lines)
		label = func(line []byte, _ int) string { return truncateLabel(string(line)) }
	}
	if len(starts) == 0 {
		return []Section{{Region: Region{StartLine: 1, EndLine: len(lines)}, Label: "(whole file)"}}, nil
	}
	var sections []Section
	if starts[0] > 0 {
		sections = append(sections, Section{
			Region: Region{StartLine: 1, EndLine: starts[0]},
			Label:  "(header)",
		})
	}
	for i, start := range starts {
		end := len(lines)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		sections = append(sections, Section{
			Region: Region{StartLine: start + 1, EndLine: end},
			Label:  label(lines[start], start),
		})
	}
	return sections, nil
}

// ── format registry ─────────────────────────────────────────────────────────

type blockFormat int

const (
	formatUnknown blockFormat = iota
	formatMarkdown
	formatHTML
	formatConfig
)

func blockFormatFor(target string) blockFormat {
	switch strings.ToLower(filepath.Ext(target)) {
	case ".md", ".markdown", ".mdx":
		return formatMarkdown
	case ".html", ".htm", ".xhtml":
		return formatHTML
	case ".json", ".yaml", ".yml", ".toml", ".ini", ".cfg", ".conf",
		".properties", ".env":
		return formatConfig
	default:
		return formatUnknown
	}
}

// ── markdown ────────────────────────────────────────────────────────────────

// mdSectionStarts returns 0-indexed heading lines outside fenced code blocks.
func mdSectionStarts(lines [][]byte) []int {
	var starts []int
	fence := ""
	for i, line := range lines {
		t := strings.TrimSpace(string(line))
		if f := mdFenceMarker(t); f != "" {
			if fence == "" {
				fence = f
			} else if strings.HasPrefix(t, fence) {
				fence = ""
			}
			continue
		}
		if fence == "" && isMDHeading(t) {
			starts = append(starts, i)
		}
	}
	return starts
}

// mdFenceMarker returns the fence opener/closer form of a trimmed line ("```"
// or "~~~"), or "" when the line is not a fence toggle.
func mdFenceMarker(trimmed string) string {
	switch {
	case strings.HasPrefix(trimmed, "```"):
		return "```"
	case strings.HasPrefix(trimmed, "~~~"):
		return "~~~"
	default:
		return ""
	}
}

// isMDHeading reports whether a trimmed line is an ATX heading.
func isMDHeading(trimmed string) bool {
	if !strings.HasPrefix(trimmed, "#") {
		return false
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == '#' {
		n++
	}
	return n <= 6 && n < len(trimmed) && trimmed[n] == ' '
}

func mdLabel(line []byte, _ int) string { return truncateLabel(string(line)) }

// ── HTML ────────────────────────────────────────────────────────────────────

// voidElements never nest: they have no closing tag.
var voidElements = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true,
	"hr": true, "img": true, "input": true, "link": true, "meta": true,
	"param": true, "source": true, "track": true, "wbr": true,
}

// htmlTopLevelStarts walks tag nesting across all lines and returns the
// 0-indexed lines on which TOP-LEVEL elements open. Structural wrappers
// (html, body, head) do not count toward nesting, so both fragment and
// full-document layouts yield their real children as sections.
func htmlTopLevelStarts(lines [][]byte) []int {
	var starts []int
	depth := 0
	rawText := "" // inside <script>/<style>: ignore everything until the close
	for i, line := range lines {
		s := string(line)
		for x := 0; x < len(s); {
			if rawText != "" {
				j := indexFold(s, "</"+rawText, x)
				if j < 0 {
					break // whole remaining line is raw text content
				}
				rawText = ""
				x = j
				continue
			}
			lt := strings.IndexByte(s[x:], '<')
			if lt < 0 {
				break
			}
			x += lt
			rest := s[x:]
			switch {
			case strings.HasPrefix(rest, "<!--"):
				if end := strings.Index(rest, "-->"); end >= 0 {
					x += end + 3
					continue
				}
				x = len(s)
				continue
			case strings.HasPrefix(strings.ToLower(rest), "<!doctype"):
				x = len(s)
				continue
			case strings.HasPrefix(rest, "<?"):
				if end := strings.Index(rest, "?>"); end >= 0 {
					x += end + 2
					continue
				}
				x = len(s)
				continue
			}
			gt := strings.IndexByte(rest, '>')
			if gt < 0 {
				break // multi-line attribute list: handled next line
			}
			interior := strings.TrimSpace(rest[1:gt])
			selfClosing := strings.HasSuffix(interior, "/")
			if selfClosing {
				interior = strings.TrimSpace(strings.TrimRight(interior, "/"))
			}
			closing := strings.HasPrefix(interior, "/")
			name := strings.ToLower(tagName(strings.TrimPrefix(interior, "/")))
			switch {
			case closing:
				if !isWrapper(name) && depth > 0 {
					depth--
				}
			case selfClosing || voidElements[name]:
				// Void / self-closing: no depth change.
			default:
				if name == "script" || name == "style" {
					rawText = name
				}
				if !isWrapper(name) {
					if depth == 0 && (len(starts) == 0 || starts[len(starts)-1] != i) {
						starts = append(starts, i)
					}
					depth++
				}
			}
			x += gt + 1
		}
	}
	return starts
}

// isWrapper reports whether the tag is a structural document wrapper.
func isWrapper(lower string) bool {
	return lower == "html" || lower == "body" || lower == "head"
}

// tagName extracts the tag name from the interior of "<…>".
func tagName(tag string) string {
	tag = strings.TrimSpace(tag)
	for i, r := range tag {
		if r == ' ' || r == '\t' || r == '\n' || r == '/' {
			return tag[:i]
		}
	}
	return tag
}

// indexFold is a case-insensitive strings.Index restricted to [from, end).
func indexFold(s, sub string, from int) int {
	if from >= len(s) {
		return -1
	}
	j := strings.Index(strings.ToLower(s[from:]), strings.ToLower(sub))
	if j < 0 {
		return -1
	}
	return from + j
}

// htmlLabel renders the bounded identity of a top-level element's open tag.
func htmlLabel(line []byte, _ int) string {
	s := strings.TrimSpace(string(line))
	if gt := strings.IndexByte(s, '>'); gt >= 0 && strings.HasPrefix(s, "<") {
		s = s[:gt+1]
	}
	return truncateLabel(s)
}

// ── config ──────────────────────────────────────────────────────────────────

// configSectionStarts returns the 0-indexed lines that begin a top-level
// configuration unit for the target's concrete config dialect.
func configSectionStarts(target string, lines [][]byte) []int {
	switch strings.ToLower(filepath.Ext(target)) {
	case ".toml", ".ini", ".cfg", ".conf", ".properties", ".env":
		return iniSectionStarts(lines)
	case ".yaml", ".yml":
		return yamlSectionStarts(lines)
	default: // .json
		return jsonMemberStarts(lines)
	}
}

// iniSectionStarts: [section] headers begin new units.
func iniSectionStarts(lines [][]byte) []int {
	var starts []int
	for i, line := range lines {
		t := strings.TrimSpace(string(line))
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			starts = append(starts, i)
		}
	}
	return starts
}

// yamlSectionStarts: top-level mapping keys and "---" document separators.
func yamlSectionStarts(lines [][]byte) []int {
	var starts []int
	for i, line := range lines {
		s := string(line)
		if strings.HasPrefix(s, "\t") || strings.HasPrefix(s, " ") {
			continue // indented: nested under a parent key
		}
		t := strings.TrimSpace(s)
		switch {
		case t == "---" || t == "...":
			starts = append(starts, i)
		case t == "" || strings.HasPrefix(t, "#"):
			continue
		case isYAMLKey(t):
			starts = append(starts, i)
		}
	}
	return starts
}

// isYAMLKey reports whether a trimmed line opens a top-level mapping entry
// ("key:" or "key: value"), never a sequence item ("- item").
func isYAMLKey(t string) bool {
	if strings.HasPrefix(t, "-") {
		return false
	}
	idx := strings.Index(t, ":")
	return idx > 0
}

// jsonMemberStarts: root-object members — `"key": ...` lines at bracket
// depth 1. Elements of nested arrays/objects never qualify.
func jsonMemberStarts(lines [][]byte) []int {
	var starts []int
	depth := 0
	inStr := false
	escape := false
	for i, line := range lines {
		memberStart := -1
		for _, c := range []byte(line) {
			switch {
			case escape:
				escape = false
			case inStr && c == '\\':
				escape = true
			case c == '"':
				inStr = !inStr
			case inStr:
			case c == '{' || c == '[':
				depth++
			case c == '}' || c == ']':
				if depth > 0 {
					depth--
				}
			}
		}
		t := strings.TrimSpace(string(line))
		if depth == 1 && !inStr && strings.HasPrefix(t, "\"") &&
			strings.Contains(t, ":") {
			memberStart = i
		}
		if memberStart >= 0 {
			starts = append(starts, memberStart)
		}
	}
	return dedupSorted(starts)
}
