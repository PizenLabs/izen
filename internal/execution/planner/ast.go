package planner

import (
	"path/filepath"
	"strings"
)

// ── AST/structural decomposer (Go / Rust / TS) ──────────────────────────────
//
// The splitter cuts a source file into sections at TOP-LEVEL declaration
// boundaries. It is a structural scanner, not a full parser: a line starts a
// new section when, outside any bracket nesting, string literal or comment,
// it begins with one of the language's top-level declaration keywords at
// column zero. Doc comments and attribute runs immediately preceding a
// declaration are attached to it (backward extension), so a section is never
// separated from its documentation.
//
// Guarantees (fail-closed by construction):
//   - sections are contiguous, ordered and cover the whole input;
//   - declarations never split across sections (a boundary requires depth 0);
//   - identical inputs yield identical sections.

// ASTDecomposer implements Decomposer for Go, Rust and TypeScript sources.
type ASTDecomposer struct{}

// Supports reports whether the target is an AST-splittable source file.
func (ASTDecomposer) Supports(target string) bool {
	return astDeclPrefixesFor(target) != nil
}

// Split partitions the source into top-level declaration sections. The
// preamble (package clause, imports, header comments) forms its own leading
// section so every later section depends on nothing hidden inside it.
func (d ASTDecomposer) Split(target string, source []byte) ([]Section, error) {
	prefixes := astDeclPrefixesFor(target)
	if prefixes == nil {
		return nil, ErrNoDecomposer
	}
	lines := splitKeepNewline(source)
	starts := astDeclStarts(lines, prefixes)
	if len(starts) == 0 {
		// No recognisable top-level declaration: one indivisible section.
		return []Section{{Region: Region{StartLine: 1, EndLine: len(lines)}, Label: "(whole file)"}}, nil
	}
	var sections []Section
	if starts[0].start > 0 {
		sections = append(sections, Section{
			Region: Region{StartLine: 1, EndLine: starts[0].start},
			Label:  "(preamble)",
		})
	}
	for i, ds := range starts {
		end := len(lines)
		if i+1 < len(starts) {
			end = starts[i+1].start
		}
		sections = append(sections, Section{
			Region: Region{StartLine: ds.start + 1, EndLine: end},
			Label:  declLabel(lines[ds.decl]),
		})
	}
	return sections, nil
}

// astDeclPrefixesFor maps a target path onto its column-zero declaration
// prefix table; nil when the format is not AST-splittable.
func astDeclPrefixesFor(target string) []string {
	switch strings.ToLower(filepath.Ext(target)) {
	case ".go":
		return goDeclPrefixes
	case ".rs":
		return rustDeclPrefixes
	case ".ts", ".tsx", ".mts", ".cts":
		return tsDeclPrefixes
	default:
		return nil
	}
}

// Top-level declaration prefixes per language (column-zero matches).
var (
	goDeclPrefixes   = []string{"func ", "func(", "type ", "var ", "const "}
	rustDeclPrefixes = []string{
		"pub ", "fn ", "struct ", "enum ", "impl ", "trait ", "mod ",
		"use ", "const ", "static ", "type ", "macro_rules!",
	}
	tsDeclPrefixes = []string{
		"export ", "function ", "class ", "interface ", "type ", "enum ",
		"namespace ", "module ", "abstract ", "declare ",
		"const ", "let ", "var ",
	}
)

// attrPrefixes are trimmed line forms that belong FORWARD to the following
// declaration (doc comments, Rust attributes, block-comment tails, license
// separators). Blank lines attach forward too.
var attrPrefixes = []string{"#", "//", "/*", "*", "@"}

// astDeclStarts returns the 0-indexed lines that begin a top-level
// declaration, each extended backwards over its contiguous doc/attribute run.
// The label always comes from the RAW declaration line, never the comments.
func astDeclStarts(lines [][]byte, prefixes []string) []declStart {
	sc := &scanState{}
	var raw []int
	for i, line := range lines {
		atTop := sc.atTop()
		if atTop && hasAnyPrefix(string(line), prefixes) {
			raw = append(raw, i)
		}
		sc.step(line)
	}
	// Backward extension over attribute/comment runs. prev marks the raw
	// start of the previous declaration: extension never crosses into it.
	out := make([]declStart, 0, len(raw))
	prev := -1
	for _, s := range raw {
		top := s
		for k := s; k-1 > prev; {
			t := strings.TrimSpace(string(lines[k-1]))
			switch {
			case t != "" && isAttrLine(lines[k-1]):
				k--
				top = k // attach doc comment / attribute line
			case t == "" && top < s:
				k-- // blank INSIDE a documentation run: tentatively consume
			default:
				k = prev // separator blank or foreign code: stop extending
			}
		}
		if len(out) == 0 || out[len(out)-1].start != top {
			out = append(out, declStart{start: top, decl: s})
		}
		prev = s
	}
	return out
}

// declStart pairs the attached section top (after backward extension) with
// the raw declaration line used for the section label.
type declStart struct {
	start int // 0-indexed first line of the section (doc run included)
	decl  int // 0-indexed raw declaration line
}

// hasAnyPrefix reports whether s starts with any of the prefixes.
func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// isAttrLine reports whether the line belongs forward (doc comment, attribute,
// block-comment fragment or blank separator).
func isAttrLine(line []byte) bool {
	t := strings.TrimSpace(string(line))
	if t == "" {
		return true
	}
	return hasAnyPrefix(t, attrPrefixes)
}

// declLabel extracts the bounded identity of a declaration line.
func declLabel(line []byte) string {
	return truncateLabel(strings.TrimRight(string(line), "{ \t\r\n"))
}

// ── shared bracket/string/comment scanner ───────────────────────────────────

// scanState is a streaming classifier for C-brace languages. Lines are fed in
// order; atTop reports whether the NEXT line sits at top level (outside every
// bracket, string literal and comment) and can therefore be a boundary.
type scanState struct {
	depth   int
	quote   byte
	escape  bool
	comment bool // inside /* */ block comment
	lineDir bool // inside a // line comment (resets per line)
}

// atTop reports whether the scanner currently sits at a boundary position.
func (s *scanState) atTop() bool {
	return s.depth == 0 && s.quote == 0 && !s.comment
}

// step consumes one line, updating bracket/string/comment state.
func (s *scanState) step(line []byte) {
	s.lineDir = false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case s.lineDir:
			return // rest of the line is a // comment
		case s.escape:
			s.escape = false
		case s.comment:
			if c == '*' && i+1 < len(line) && line[i+1] == '/' {
				s.comment = false
				i++
			}
		case s.quote != 0:
			switch c {
			case '\\':
				s.escape = true
			case s.quote:
				s.quote = 0
			}
		default:
			switch c {
			case '/':
				if i+1 >= len(line) {
					continue
				}
				switch line[i+1] {
				case '/':
					s.lineDir = true
				case '*':
					s.comment = true
					i++
				}
			case '"', '\'', '`':
				s.quote = c
			case '{', '(', '[':
				s.depth++
			case '}', ')', ']':
				if s.depth > 0 {
					s.depth--
				}
			}
		}
	}
}

// splitKeepNewline splits source into lines without their newline bytes; a
// trailing newline does not create a phantom final line.
func splitKeepNewline(source []byte) [][]byte {
	if len(source) == 0 {
		return nil
	}
	lines := strings.Split(strings.TrimSuffix(string(source), "\n"), "\n")
	out := make([][]byte, len(lines))
	for i, l := range lines {
		out[i] = []byte(l)
	}
	return out
}

// dedupSorted removes consecutive duplicates from an ascending slice.
func dedupSorted(xs []int) []int {
	out := xs[:0]
	for i, x := range xs {
		if i == 0 || x != out[len(out)-1] {
			out = append(out, x)
		}
	}
	return out
}
