package extractors

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/PizenLabs/izen/internal/retrieval/symbol"
)

var (
	rustModuleRe = regexp.MustCompile(`(?:^|\n)\s*mod\s+([a-zA-Z_]\w*)`)
	rustTraitRe  = regexp.MustCompile(`(?:^|\n)\s*trait\s+([a-zA-Z_]\w*)`)
	rustStructRe = regexp.MustCompile(`(?:^|\n)\s*struct\s+([a-zA-Z_]\w*)`)
	rustEnumRe   = regexp.MustCompile(`(?:^|\n)\s*enum\s+([a-zA-Z_]\w*)`)
	rustFnRe     = regexp.MustCompile(`(?:^|\n)(?:pub\s+)?(?:async\s+)?fn\s+([a-zA-Z_]\w*)`)
	rustImplRe   = regexp.MustCompile(`(?:^|\n)\s*impl(?:\s+<[^>]+>)?\s+([a-zA-Z_]\w*)`)
	rustUseRe    = regexp.MustCompile(`(?:^|\n)\s*use\s+([a-zA-Z_]\w*(?:::[a-zA-Z_]\w*)*)`)
)

type rustExtractor struct{}

func NewRustExtractor() symbol.LanguageExtractor {
	return &rustExtractor{}
}

func (e *rustExtractor) DetectLanguage(rootPath string) (symbol.LanguageID, bool) {
	if fileExists(rootPath, "Cargo.toml") {
		return symbol.LangRust, true
	}
	return "", false
}

func (e *rustExtractor) ExtractSymbols(filePath string, content []byte) (*symbol.FileASTInfo, error) {
	info := &symbol.FileASTInfo{
		FilePath: filePath,
		Language: symbol.LangRust,
	}

	lines := strings.Split(string(content), "\n")

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		if m := rustUseRe.FindStringSubmatch(trimmed); m != nil {
			info.Imports = append(info.Imports, symbol.DependencyEdge{
				SourcePackage: "",
				TargetPackage: m[1],
				ImportPath:    m[1],
				RelationType:  symbol.RelationImports,
			})
		}

		if m := rustModuleRe.FindStringSubmatch(trimmed); m != nil {
			sym := symbol.SymbolNode{
				Name:      m[1],
				Kind:      symbol.SymbolModule,
				FilePath:  filePath,
				StartLine: i + 1,
				Exported:  strings.HasPrefix(trimmed, "pub"),
			}
			info.Symbols = append(info.Symbols, sym)
		}

		if m := rustTraitRe.FindStringSubmatch(trimmed); m != nil {
			sym := symbol.SymbolNode{
				Name:      m[1],
				Kind:      symbol.SymbolInterface,
				FilePath:  filePath,
				StartLine: i + 1,
				Exported:  strings.HasPrefix(trimmed, "pub"),
			}
			info.Symbols = append(info.Symbols, sym)
		}

		if m := rustStructRe.FindStringSubmatch(trimmed); m != nil {
			name := m[1]
			sym := symbol.SymbolNode{
				Name:      name,
				Kind:      symbol.SymbolStruct,
				FilePath:  filePath,
				StartLine: i + 1,
				Exported:  strings.HasPrefix(trimmed, "pub"),
			}
			info.Symbols = append(info.Symbols, sym)
			info.Classes = append(info.Classes, sym)
		}

		if m := rustEnumRe.FindStringSubmatch(trimmed); m != nil {
			sym := symbol.SymbolNode{
				Name:      m[1],
				Kind:      symbol.SymbolEnum,
				FilePath:  filePath,
				StartLine: i + 1,
				Exported:  strings.HasPrefix(trimmed, "pub"),
			}
			info.Symbols = append(info.Symbols, sym)
		}

		if m := rustFnRe.FindStringSubmatch(trimmed); m != nil {
			sym := symbol.SymbolNode{
				Name:      m[1],
				Kind:      symbol.SymbolFunction,
				FilePath:  filePath,
				StartLine: i + 1,
				Exported:  strings.HasPrefix(trimmed, "pub"),
			}
			info.Symbols = append(info.Symbols, sym)
			info.Functions = append(info.Functions, sym)
		}

		if m := rustImplRe.FindStringSubmatch(trimmed); m != nil {
			sym := symbol.SymbolNode{
				Name:      m[1],
				Kind:      symbol.SymbolClass,
				FilePath:  filePath,
				StartLine: i + 1,
				Exported:  strings.HasPrefix(trimmed, "pub"),
			}
			info.Symbols = append(info.Symbols, sym)
			info.Classes = append(info.Classes, sym)
		}
	}

	return info, nil
}

func (e *rustExtractor) ExtractPackages(rootPath string) ([]symbol.PackageNode, error) {
	var packages []symbol.PackageNode
	seen := make(map[string]bool)

	_ = filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil //nolint:nilerr
		}
		if info.IsDir() || !strings.HasSuffix(path, ".rs") {
			return nil
		}
		if strings.Contains(path, "target/") {
			return nil
		}

		pkgName := determineRustPackage(rootPath, path)
		if pkgName == "" {
			return nil
		}

		if !seen[pkgName] {
			seen[pkgName] = true
			packages = append(packages, symbol.PackageNode{
				Name:     pkgName,
				RootPath: rootPath,
				Files:    []string{path},
			})
		} else {
			for i := range packages {
				if packages[i].Name == pkgName {
					packages[i].Files = append(packages[i].Files, path)
					break
				}
			}
		}
		return nil
	})

	return packages, nil
}

func (e *rustExtractor) DetectArchitecturePattern(nodes []symbol.PackageNode) (symbol.PatternInfo, error) {
	hasLib := false
	hasBin := false

	for _, node := range nodes {
		for _, file := range node.Files {
			if strings.Contains(file, "lib.rs") {
				hasLib = true
			}
			if strings.Contains(file, "main.rs") {
				hasBin = true
			}
		}
	}

	if hasLib && hasBin {
		return symbol.PatternInfo{
			Name:        "Rust Library + Binary",
			Confidence:  "high",
			Description: "Rust crate with both library and binary targets",
		}, nil
	}

	if hasLib {
		return symbol.PatternInfo{
			Name:        "Rust Library",
			Confidence:  "high",
			Description: "Rust library crate",
		}, nil
	}

	if hasBin {
		return symbol.PatternInfo{
			Name:        "Rust Binary",
			Confidence:  "medium",
			Description: "Rust binary application",
		}, nil
	}

	return symbol.PatternInfo{
		Name:        "Rust Package",
		Confidence:  "low",
		Description: "Rust project without detected structure patterns",
	}, nil
}

func determineRustPackage(rootPath, filePath string) string {
	dir := filepath.Dir(filePath)
	rel, err := filepath.Rel(rootPath, dir)
	if err != nil {
		return "root"
	}
	if rel == "." {
		return "root"
	}
	return rel
}
