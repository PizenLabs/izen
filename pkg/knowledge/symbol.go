// Package knowledge maintains the in-memory RuntimeKnowledge symbol table for
// the Izen runtime. It tracks the project archetype, framework tags, workspace
// files and lightweight per-file symbol summaries so planners and the intent
// compiler can answer structural questions from memory instead of re-walking
// the disk on every query.
//
// The package is deliberately dependency-free: it performs I/O only inside an
// explicit Scan/Ensure and exposes a plain value model everywhere else.
package knowledge

import (
	"regexp"
	"sort"
	"strings"
	"sync"
)

// SymbolKind discriminates the language-level construct a Symbol names.
type SymbolKind string

// Symbol kinds extracted by the lightweight per-line scanner.
const (
	SymbolFunc      SymbolKind = "func"
	SymbolMethod    SymbolKind = "method"
	SymbolType      SymbolKind = "type"
	SymbolInterface SymbolKind = "interface"
	SymbolClass     SymbolKind = "class"
	SymbolStruct    SymbolKind = "struct"
	SymbolEnum      SymbolKind = "enum"
	SymbolConst     SymbolKind = "const"
	SymbolVar       SymbolKind = "var"
	SymbolTrait     SymbolKind = "trait"
)

// Symbol is one named declaration discovered in a workspace file.
type Symbol struct {
	// Name is the declared identifier.
	Name string
	// Kind classifies the construct.
	Kind SymbolKind
	// File is the workspace-relative path of the declaring file.
	File string
	// Line is the 1-based line of the declaration.
	Line int
}

// languageRules maps a source language to the per-line declaration rules used
// by the symbol scanner. Each rule carries the kind it emits and a regular
// expression whose first capture group is the symbol name.
type declRule struct {
	kind SymbolKind
	re   *regexp.Regexp
}

// newDeclRule compiles one per-line rule.
func newDeclRule(kind SymbolKind, pattern string) declRule {
	return declRule{kind: kind, re: regexp.MustCompile(pattern)}
}

// rulesByLanguage covers the recognised source languages. Patterns match the
// trimmed start of a line so indented (method/class member) declarations are
// captured too.
var rulesByLanguage = map[string][]declRule{
	"go": {
		newDeclRule(SymbolMethod, `^func\s+\([^)]*\)\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`),
		newDeclRule(SymbolFunc, `^func\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`),
		newDeclRule(SymbolType, `^type\s+([A-Za-z_][A-Za-z0-9_]*)\s`),
		newDeclRule(SymbolConst, `^const\s+([A-Za-z_][A-Za-z0-9_]*)\s*(?:=|:=)`),
		newDeclRule(SymbolVar, `^var\s+([A-Za-z_][A-Za-z0-9_]*)\s*(?:=|:=)`),
	},
	"js": {
		newDeclRule(SymbolFunc, `^(?:export\s+)?function\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*\(`),
		newDeclRule(SymbolConst, `^(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*=`),
		newDeclRule(SymbolClass, `^(?:export\s+)?class\s+([A-Za-z_$][A-Za-z0-9_$]*)\s`),
	},
	"ts": {
		newDeclRule(SymbolFunc, `^(?:export\s+)?function\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*\(`),
		newDeclRule(SymbolConst, `^(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*=`),
		newDeclRule(SymbolClass, `^(?:export\s+)?class\s+([A-Za-z_$][A-Za-z0-9_$]*)\s`),
		newDeclRule(SymbolInterface, `^(?:export\s+)?interface\s+([A-Za-z_$][A-Za-z0-9_$]*)`),
		newDeclRule(SymbolType, `^(?:export\s+)?type\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*=`),
	},
	"python": {
		newDeclRule(SymbolFunc, `^def\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`),
		newDeclRule(SymbolClass, `^class\s+([A-Za-z_][A-Za-z0-9_]*)\s*[(:]`),
	},
	"rust": {
		newDeclRule(SymbolFunc, `^fn\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`),
		newDeclRule(SymbolStruct, `^(?:pub\s+)?struct\s+([A-Za-z_][A-Za-z0-9_]*)`),
		newDeclRule(SymbolEnum, `^(?:pub\s+)?enum\s+([A-Za-z_][A-Za-z0-9_]*)`),
		newDeclRule(SymbolTrait, `^(?:pub\s+)?trait\s+([A-Za-z_][A-Za-z0-9_]*)`),
		newDeclRule(SymbolConst, `^(?:pub\s+)?const\s+([A-Za-z_][A-Za-z0-9_]*)\s*:`),
	},
	"java": {
		newDeclRule(SymbolClass, `^(?:public|private|protected)\s+(?:abstract\s+|final\s+)*class\s+([A-Za-z_][A-Za-z0-9_]*)`),
		newDeclRule(SymbolInterface, `^(?:public\s+)?interface\s+([A-Za-z_][A-Za-z0-9_]*)`),
	},
	"ruby": {
		newDeclRule(SymbolMethod, `^def\s+([A-Za-z_][A-Za-z0-9_]*[!?]?)\s*[\(]?`),
		newDeclRule(SymbolClass, `^class\s+([A-Za-z_:][A-Za-z0-9_:]*)`),
	},
	"php": {
		newDeclRule(SymbolFunc, `^function\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`),
		newDeclRule(SymbolClass, `^(?:abstract\s+|final\s+)?class\s+([A-Za-z_][A-Za-z0-9_]*)`),
	},
	"c": {
		newDeclRule(SymbolType, `^typedef\s+.*\s+([A-Za-z_][A-Za-z0-9_]*)\s*;`),
		newDeclRule(SymbolStruct, `^struct\s+([A-Za-z_][A-Za-z0-9_]*)\s*[{;]`),
	},
	"csharp": {
		newDeclRule(SymbolClass, `^(?:public|private|protected|internal)\s+(?:abstract\s+|sealed\s+)*class\s+([A-Za-z_][A-Za-z0-9_]*)`),
		newDeclRule(SymbolInterface, `^(?:public\s+)?interface\s+([A-Za-z_][A-Za-z0-9_]*)`),
		newDeclRule(SymbolEnum, `^(?:public\s+)?enum\s+([A-Za-z_][A-Za-z0-9_]*)`),
	},
}

// languageFor maps a file name to the rule set that should scan it. Recognised
// source extensions return their rule key; everything else returns "".
func languageFor(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".go"):
		return "go"
	case strings.HasSuffix(lower, ".js"), strings.HasSuffix(lower, ".jsx"), strings.HasSuffix(lower, ".mjs"), strings.HasSuffix(lower, ".cjs"):
		return "js"
	case strings.HasSuffix(lower, ".ts"), strings.HasSuffix(lower, ".tsx"):
		return "ts"
	case strings.HasSuffix(lower, ".py"):
		return "python"
	case strings.HasSuffix(lower, ".rs"):
		return "rust"
	case strings.HasSuffix(lower, ".java"):
		return "java"
	case strings.HasSuffix(lower, ".rb"):
		return "ruby"
	case strings.HasSuffix(lower, ".php"):
		return "php"
	case strings.HasSuffix(lower, ".c"), strings.HasSuffix(lower, ".h"), strings.HasSuffix(lower, ".cc"), strings.HasSuffix(lower, ".cpp"):
		return "c"
	case strings.HasSuffix(lower, ".cs"):
		return "csharp"
	default:
		return ""
	}
}

// ExtractSymbols scans content line by line and returns every declaration the
// rules for lang recognise. lang must be a rule-set key returned by
// languageFor (or "" to skip scanning).
func ExtractSymbols(lang, file, content string) []Symbol {
	if lang == "" || content == "" {
		return nil
	}
	rules := rulesByLanguage[lang]
	if len(rules) == 0 {
		return nil
	}
	lines := strings.Split(content, "\n")
	var out []Symbol
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") {
			continue
		}
		for _, rule := range rules {
			if m := rule.re.FindStringSubmatch(trimmed); len(m) == 2 {
				out = append(out, Symbol{Name: m[1], Kind: rule.kind, File: file, Line: i + 1})
				break
			}
		}
	}
	return out
}

// SymbolTable is a thread-safe in-memory index of Symbols keyed by name. It
// answers lookup queries without touching the filesystem.
type SymbolTable struct {
	mu     sync.RWMutex
	byName map[string][]Symbol
	all    []Symbol
}

// NewSymbolTable builds an empty SymbolTable.
func NewSymbolTable() *SymbolTable {
	return &SymbolTable{byName: make(map[string][]Symbol)}
}

// Add inserts one symbol. Duplicates of an identical declaration are ignored.
func (t *SymbolTable) Add(s Symbol) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, existing := range t.byName[s.Name] {
		if existing.File == s.File && existing.Line == s.Line && existing.Kind == s.Kind {
			return
		}
	}
	t.byName[s.Name] = append(t.byName[s.Name], s)
	t.all = append(t.all, s)
}

// AddMany inserts a batch of symbols.
func (t *SymbolTable) AddMany(symbols []Symbol) {
	for _, s := range symbols {
		t.Add(s)
	}
}

// Lookup returns every declaration named name, in insertion order.
func (t *SymbolTable) Lookup(name string) []Symbol {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return append([]Symbol(nil), t.byName[name]...)
}

// Count returns the total number of indexed symbols.
func (t *SymbolTable) Count() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.all)
}

// All returns a defensive copy of every indexed symbol.
func (t *SymbolTable) All() []Symbol {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return append([]Symbol(nil), t.all...)
}

// Names returns every unique symbol name in stable sorted order.
func (t *SymbolTable) Names() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	names := make([]string, 0, len(t.byName))
	for n := range t.byName {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
