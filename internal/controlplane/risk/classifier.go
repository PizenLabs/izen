package risk

import (
	"path/filepath"
	"regexp"

	"github.com/PizenLabs/izen/internal/lea"
)

// Tier represents a deterministic risk classification level.
type Tier int

const (
	Tier0 Tier = iota // Safe: docs, comments, non-sensitive config changes
	Tier1             // Low Risk: standard refactors, isolated feature additions
	Tier2             // Medium Risk: core logic modifications, multi-file updates
	Tier3             // High Risk: auth, DB migrations, security/crypto, permissions, network, system files
)

func (t Tier) String() string {
	switch t {
	case Tier0:
		return "Tier0"
	case Tier1:
		return "Tier1"
	case Tier2:
		return "Tier2"
	case Tier3:
		return "Tier3"
	default:
		return "TierUnknown"
	}
}

// Target describes a single mutation target.
type Target struct {
	File   string
	Symbol string // empty for file-level targets
}

// Tier3FilePatterns force escalation to Tier 3 when a target path matches.
var Tier3FilePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(^|/)auth(/|$)`),
	regexp.MustCompile(`(?i)(^|/)crypto(/|$)`),
	regexp.MustCompile(`(?i)(^|/)security(/|$)`),
	regexp.MustCompile(`(?i)db/migrations/`),
	regexp.MustCompile(`(?i)(^|/)permissions?(/|$)`),
	regexp.MustCompile(`(?i)(^|/)network(/|$)`),
	regexp.MustCompile(`(?i)(^|/)networking(/|$)`),
	regexp.MustCompile(`(?i)(^|/)(etc|usr|bin|sbin|var)/`),
	regexp.MustCompile(`(?i)(^|/)(Dockerfile|docker-compose|\.dockerignore)$`),
	regexp.MustCompile(`(?i)(^|/)(\.github/workflows/|\.gitlab-ci|Jenkinsfile)`),
}

// Tier0Patterns match non-sensitive, documentation, or config changes.
var Tier0Patterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\.(md|txt|rst|adoc)$`),
	regexp.MustCompile(`(?i)\.(yml|yaml|json|toml|ini|cfg)$`),
	regexp.MustCompile(`(?i)\.(gitignore|gitattributes|editorconfig)$`),
}

// Tier1Patterns match low-risk isolated changes.
var Tier1Patterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(_test\.go|_spec\.\w+|_test\.\w+)$`),
	regexp.MustCompile(`(?i)\.(css|scss|less)$`),
}

// Classifier determines the risk tier for mutation targets.
type Classifier struct {
	graph *lea.FileGraph // optional; used for symbol impact escalation
}

// NewClassifier creates a Classifier. The graph parameter is optional — when
// provided, symbol impact escalation is enabled.
func NewClassifier(g *lea.FileGraph) *Classifier {
	return &Classifier{graph: g}
}

// Classify determines the risk tier for the given targets. It short-circuits
// at Tier 3. If the graph is non-nil and any target modifies an exported symbol
// that is referenced by a Tier 3 file, the result is escalated to Tier 3.
func (c *Classifier) Classify(targets []Target) Tier {
	if len(targets) == 0 {
		return Tier0
	}

	tier := Tier0

	for _, t := range targets {
		ft := c.classifyFile(t.File)
		if ft > tier {
			tier = ft
		}
		if tier == Tier3 {
			return Tier3
		}
	}

	if tier < Tier3 && c.graph != nil {
		for _, t := range targets {
			if t.Symbol != "" && c.symbolImpactsTier3File(t) {
				return Tier3
			}
		}
	}

	return tier
}

// classifyFile returns the tier for a single file path based on regex matching.
// Tier 3 patterns are checked first (highest risk), then Tier 0 (safe), then
// Tier 1 (low risk). Anything unmatched falls through to Tier 2 (medium risk).
func (c *Classifier) classifyFile(path string) Tier {
	clean := filepath.ToSlash(filepath.Clean(path))

	for _, re := range Tier3FilePatterns {
		if re.MatchString(clean) {
			return Tier3
		}
	}

	for _, re := range Tier0Patterns {
		if re.MatchString(clean) {
			return Tier0
		}
	}

	for _, re := range Tier1Patterns {
		if re.MatchString(clean) {
			return Tier1
		}
	}

	return Tier2
}

// symbolImpactsTier3File returns true if the target's exported symbol is
// referenced by any file in the graph classified as Tier 3.
func (c *Classifier) symbolImpactsTier3File(target Target) bool {
	if c.graph == nil || target.Symbol == "" {
		return false
	}

	symbols := c.graph.LookupSymbol(target.Symbol)
	if len(symbols) == 0 {
		return false
	}

	definingPkg := ""
	for _, sym := range symbols {
		if fn, ok := c.graph.FileMap[sym.File]; ok && fn.Package != "" {
			definingPkg = fn.Package
			break
		}
	}

	for _, fn := range c.graph.Files {
		if c.classifyFile(fn.Path) < Tier3 {
			continue
		}
		for _, imp := range fn.Imports {
			if matchImport(imp, definingPkg) {
				return true
			}
		}
	}

	return false
}

// matchImport reports whether an import path ends with the package name or
// equals it, indicating the file imports the given package.
func matchImport(importPath, pkg string) bool {
	if pkg == "" {
		return false
	}
	if importPath == pkg {
		return true
	}
	return len(importPath) > len(pkg) &&
		importPath[len(importPath)-len(pkg)-1] == '/' &&
		importPath[len(importPath)-len(pkg):] == pkg
}
