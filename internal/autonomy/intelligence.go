package autonomy

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/PizenLabs/izen/internal/language"
)

// AnalysisStrategy selects the structural analyzer the runtime must run for an
// artifact before any AI reasoning. Selection is deterministic: it is a pure
// function of the detected language, the artifact kind, and the availability
// of a parser for that language.
type AnalysisStrategy string

const (
	// StrategyTreeSitter is the preferred strategy for programming languages
	// whose language definition declares tree-sitter support. When no
	// tree-sitter backend is wired the analyzer degrades to the AST-lite
	// scanner and records a file.parser_degraded finding.
	StrategyTreeSitter AnalysisStrategy = "tree_sitter"
	// StrategyAST is used when a real parser is compiled into the runtime
	// (Go's stdlib go/ast). It produces the highest-confidence findings.
	StrategyAST AnalysisStrategy = "ast"
	// StrategySemantic is used for markup, data and free text: the runtime
	// walks the document structure or scans tokens instead of parsing a
	// grammar.
	StrategySemantic AnalysisStrategy = "semantic"
	// StrategyNone is selected for empty or undetectable artifacts.
	StrategyNone AnalysisStrategy = "none"
)

func (s AnalysisStrategy) String() string { return string(s) }

// ParserKind names the parser technology the runtime can actually run today.
type ParserKind string

const (
	ParserTreeSitter ParserKind = "tree-sitter"
	ParserAST        ParserKind = "ast"
	ParserSemantic   ParserKind = "semantic"
	ParserNone       ParserKind = "none"
)

func (p ParserKind) String() string { return string(p) }

// Complexity measures the structural complexity of an artifact. It is a
// deterministic, language-agnostic approximation — it never invokes a parser
// or a model.
type Complexity struct {
	Cyclomatic    int     `json:"cyclomatic"`
	MaxNesting    int     `json:"max_nesting"`
	AvgLineLength float64 `json:"avg_line_length"`
	TokenEstimate int     `json:"token_estimate"`
}

// FileIntelligence is the output of the File Intelligence stage: the compiled
// answer to "what is this file, can the runtime parse it, how complex is it,
// and which analyzer should run?". It is produced deterministically BEFORE any
// analyzer runs or any AI reasoning happens.
type FileIntelligence struct {
	Path            string           `json:"path"`
	Language        string           `json:"language"`
	ParserKind      ParserKind       `json:"parser_kind"`
	ParserAvailable bool             `json:"parser_available"`
	Strategy        AnalysisStrategy `json:"strategy"`
	Size            int              `json:"size"`
	Lines           int              `json:"lines"`
	Complexity      Complexity       `json:"complexity"`
}

// controlFlowRe matches the branching keywords that contribute cyclomatic
// complexity across C-family, Python, Ruby, JS/TS, Rust and Go.
var controlFlowRe = regexp.MustCompile(`(?m)\b(if|for|while|switch|case|catch|foreach|select|match|loop|when|elif|except)\b`)

// AnalyzeFile runs the File Intelligence stage over an artifact. It is a pure
// function of the path and content: the same inputs always yield the same
// intelligence, so the downstream analyzer selection is fully deterministic.
func AnalyzeFile(path, content string) FileIntelligence {
	fi := FileIntelligence{
		Path:       path,
		Size:       len(content),
		Lines:      countContentLines(content),
		Complexity: MeasureComplexity(content),
	}
	fi.Language = detectLanguageID(path)
	fi.Strategy = selectStrategy(path, content)
	switch fi.Strategy {
	case StrategyAST:
		fi.ParserKind = ParserAST
		fi.ParserAvailable = true
	case StrategyTreeSitter:
		fi.ParserKind = ParserTreeSitter
		// Declared support on the language definition does not mean a backend
		// is wired into the runtime today. The analyzer degrades to the
		// AST-lite scanner and records a file.parser_degraded finding until a
		// backend exists.
		fi.ParserAvailable = false
	case StrategySemantic:
		fi.ParserKind = ParserSemantic
		fi.ParserAvailable = true
	default:
		fi.ParserKind = ParserNone
		fi.ParserAvailable = false
	}
	return fi
}

// detectLanguageID resolves the human-readable language name from the language
// registry. Unknown extensions fall back to the artifact kind.
func detectLanguageID(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if def, ok := language.Global().FromExtension(ext); ok {
		return def.Name
	}
	return string(KindOf(path))
}

// selectStrategy picks the analysis strategy deterministically from the
// artifact's language support and kind. Real parsers (Go's stdlib AST) win;
// declared tree-sitter support is preferred for programming languages but
// degrades gracefully; markup, data and prose use the semantic scanner.
func selectStrategy(path, content string) AnalysisStrategy {
	if strings.TrimSpace(content) == "" {
		return StrategyNone
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".go" {
		return StrategyAST
	}
	if KindOf(path) == KindHTML {
		return StrategySemantic
	}
	if isProgrammingExt(ext) {
		if def, ok := language.Global().FromExtension(ext); ok && def.HasTreeSitter {
			return StrategyTreeSitter
		}
		return StrategySemantic
	}
	return StrategySemantic
}

// programmingExts are the extensions the runtime treats as programming
// languages (as opposed to markup, data or prose). Go is handled separately
// because it has a real compiled-in parser.
var programmingExts = map[string]bool{
	".py": true, ".pyi": true, ".rs": true, ".ts": true, ".tsx": true,
	".js": true, ".jsx": true, ".mjs": true, ".cjs": true, ".java": true,
	".kt": true, ".kts": true, ".cs": true, ".cpp": true, ".cc": true,
	".cxx": true, ".hpp": true, ".hh": true, ".hxx": true, ".h": true,
	".c": true, ".rb": true, ".php": true, ".swift": true, ".scala": true,
	".ex": true, ".exs": true, ".lua": true, ".sh": true, ".bash": true,
	".zsh": true, ".zig": true, ".hs": true, ".r": true, ".dart": true,
}

func isProgrammingExt(ext string) bool { return programmingExts[ext] }

// MeasureComplexity computes deterministic structural complexity metrics:
// cyclomatic count (branching keywords), maximum nesting depth (running
// open/close balance), average non-empty line length, and a ~4-char/token
// estimate. It never invokes a parser or a model.
func MeasureComplexity(content string) Complexity {
	lines := strings.Split(content, "\n")
	c := Complexity{}
	depth := 0
	nonEmpty := 0
	totalLen := 0
	for _, line := range lines {
		c.Cyclomatic += len(controlFlowRe.FindAllString(line, -1))
		open := strings.Count(line, "{") + strings.Count(line, "(") + strings.Count(line, "[")
		close := strings.Count(line, "}") + strings.Count(line, ")") + strings.Count(line, "]")
		depth += open - close
		if depth > c.MaxNesting {
			c.MaxNesting = depth
		}
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			nonEmpty++
			totalLen += len(trimmed)
		}
	}
	if nonEmpty > 0 {
		c.AvgLineLength = float64(totalLen) / float64(nonEmpty)
	}
	c.TokenEstimate = EstimateTokens(content)
	return c
}

// EstimateTokens estimates the token cost of the artifact (~4 chars/token).
func EstimateTokens(content string) int {
	chars := len([]rune(content))
	if chars == 0 {
		return 0
	}
	tokens := chars / 4
	if words := len(strings.Fields(content)); words > tokens {
		tokens = words
	}
	return tokens
}

func countContentLines(content string) int {
	if content == "" {
		return 0
	}
	n := strings.Count(content, "\n")
	if !strings.HasSuffix(content, "\n") {
		n++
	}
	return n
}
