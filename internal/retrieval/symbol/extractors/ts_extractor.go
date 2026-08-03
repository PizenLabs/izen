package extractors

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/PizenLabs/izen/internal/retrieval/symbol"
)

var (
	tsExportFromRe    = regexp.MustCompile(`export\s+\*\s+from\s+['"]([^'"]+)['"]`)
	tsImportRe        = regexp.MustCompile(`import\s+(?:[\w*{}\s,]+\s+from\s+)?['"]([^'"]+)['"]`)
	tsClassRe         = regexp.MustCompile(`(?:export\s+)?(?:abstract\s+)?class\s+([a-zA-Z_]\w*)`)
	tsInterfaceRe     = regexp.MustCompile(`(?:export\s+)?interface\s+([a-zA-Z_]\w*)`)
	tsTypeRe          = regexp.MustCompile(`(?:export\s+)?type\s+([a-zA-Z_]\w*)\s*=`)
	tsEnumRe          = regexp.MustCompile(`(?:export\s+)?enum\s+([a-zA-Z_]\w*)`)
	tsFunctionRe      = regexp.MustCompile(`(?:export\s+)?(?:async\s+)?function\s+([a-zA-Z_]\w*)`)
	tsConstRe         = regexp.MustCompile(`(?:export\s+)?(?:const|let|var)\s+([a-zA-Z_]\w*)`)
	tsDefaultExportRe = regexp.MustCompile(`export\s+default\s+(?:function\s+)?([a-zA-Z_]\w*)`)
)

type tsExtractor struct{}

func NewTSExtractor() symbol.LanguageExtractor {
	return &tsExtractor{}
}

func (e *tsExtractor) DetectLanguage(rootPath string) (symbol.LanguageID, bool) {
	if !fileExists(rootPath, "package.json") && !fileExists(rootPath, "tsconfig.json") {
		return "", false
	}

	hasTS := hasExtension(rootPath, ".ts")
	hasTSX := hasExtension(rootPath, ".tsx")
	hasJS := hasExtension(rootPath, ".js")
	hasJSX := hasExtension(rootPath, ".jsx")

	if hasTS || hasTSX {
		return symbol.LangTypeScript, true
	}
	if hasJS || hasJSX {
		return symbol.LangJavaScript, true
	}

	return symbol.LangTypeScript, true
}

func (e *tsExtractor) ExtractSymbols(filePath string, content []byte) (*symbol.FileASTInfo, error) {
	info := &symbol.FileASTInfo{
		FilePath: filePath,
		Language: determineTSLanguage(filePath),
	}

	lines := strings.Split(string(content), "\n")

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		if m := tsExportFromRe.FindStringSubmatch(trimmed); m != nil {
			info.Imports = append(info.Imports, symbol.DependencyEdge{
				SourcePackage: "",
				TargetPackage: m[1],
				ImportPath:    m[1],
				RelationType:  symbol.RelationExports,
			})
			continue
		}

		if m := tsImportRe.FindStringSubmatch(trimmed); m != nil {
			info.Imports = append(info.Imports, symbol.DependencyEdge{
				SourcePackage: "",
				TargetPackage: m[1],
				ImportPath:    m[1],
				RelationType:  symbol.RelationImports,
			})
			continue
		}

		if m := tsClassRe.FindStringSubmatch(trimmed); m != nil {
			name := m[1]
			sym := symbol.SymbolNode{
				Name:      name,
				Kind:      symbol.SymbolClass,
				FilePath:  filePath,
				StartLine: i + 1,
				Exported:  isTSExported(trimmed),
			}
			info.Symbols = append(info.Symbols, sym)
			info.Classes = append(info.Classes, sym)
		}

		if m := tsInterfaceRe.FindStringSubmatch(trimmed); m != nil {
			name := m[1]
			sym := symbol.SymbolNode{
				Name:      name,
				Kind:      symbol.SymbolInterface,
				FilePath:  filePath,
				StartLine: i + 1,
				Exported:  isTSExported(trimmed),
			}
			info.Symbols = append(info.Symbols, sym)
		}

		if m := tsTypeRe.FindStringSubmatch(trimmed); m != nil {
			name := m[1]
			sym := symbol.SymbolNode{
				Name:      name,
				Kind:      symbol.SymbolClass,
				FilePath:  filePath,
				StartLine: i + 1,
				Exported:  isTSExported(trimmed),
			}
			info.Symbols = append(info.Symbols, sym)
		}

		if m := tsEnumRe.FindStringSubmatch(trimmed); m != nil {
			name := m[1]
			sym := symbol.SymbolNode{
				Name:      name,
				Kind:      symbol.SymbolEnum,
				FilePath:  filePath,
				StartLine: i + 1,
				Exported:  isTSExported(trimmed),
			}
			info.Symbols = append(info.Symbols, sym)
		}

		if m := tsFunctionRe.FindStringSubmatch(trimmed); m != nil {
			name := m[1]
			sym := symbol.SymbolNode{
				Name:      name,
				Kind:      symbol.SymbolFunction,
				FilePath:  filePath,
				StartLine: i + 1,
				Exported:  isTSExported(trimmed),
			}
			info.Symbols = append(info.Symbols, sym)
			info.Functions = append(info.Functions, sym)
		}

		if m := tsConstRe.FindStringSubmatch(trimmed); m != nil {
			name := m[1]
			sym := symbol.SymbolNode{
				Name:      name,
				Kind:      symbol.SymbolConstant,
				FilePath:  filePath,
				StartLine: i + 1,
				Exported:  isTSExported(trimmed),
			}
			info.Symbols = append(info.Symbols, sym)
		}

		if m := tsDefaultExportRe.FindStringSubmatch(trimmed); m != nil {
			name := m[1]
			sym := symbol.SymbolNode{
				Name:      name,
				Kind:      symbol.SymbolFunction,
				FilePath:  filePath,
				StartLine: i + 1,
				Exported:  true,
			}
			info.Symbols = append(info.Symbols, sym)
			info.Functions = append(info.Functions, sym)
		}
	}

	info.Calls = scopeRegexCalls(info.Functions, extractRegexCalls(content))
	info.Routes = extractTSRoutes(lines)

	return info, nil
}

func (e *tsExtractor) ExtractPackages(rootPath string) ([]symbol.PackageNode, error) {
	var packages []symbol.PackageNode
	seen := make(map[string]bool)

	_ = filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil //nolint:nilerr
		}
		if info.IsDir() {
			return nil
		}
		if !hasExtension(path, ".ts") && !hasExtension(path, ".tsx") && !hasExtension(path, ".js") && !hasExtension(path, ".jsx") {
			return nil
		}
		if symbol.ShouldIgnorePath(path, rootPath) {
			return nil
		}

		_, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil //nolint:nilerr
		}

		pkgName := determinePackageName(rootPath, path)
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

func (e *tsExtractor) DetectArchitecturePattern(nodes []symbol.PackageNode) (symbol.PatternInfo, error) {
	hasExpress := false
	hasNext := false
	hasReact := false
	hasNest := false

	for _, node := range nodes {
		for _, file := range node.Files {
			src, err := os.ReadFile(file)
			if err != nil {
				continue
			}
			content := string(src)
			if strings.Contains(content, "express") || strings.Contains(content, "Express") {
				hasExpress = true
			}
			if strings.Contains(content, "NextPage") || strings.Contains(content, "getServerSideProps") || strings.Contains(content, "getStaticProps") {
				hasNext = true
			}
			if strings.Contains(content, "@Module") || strings.Contains(content, "@Controller") || strings.Contains(content, "@Injectable") {
				hasNest = true
			}
			if strings.Contains(content, "React") || strings.Contains(content, "jsx") || strings.Contains(content, "render(") {
				hasReact = true
			}
		}
	}

	if hasNest {
		return symbol.PatternInfo{
			Name:        "NestJS Layered Architecture",
			Confidence:  "high",
			Description: "NestJS application with modules, controllers, and services",
		}, nil
	}

	if hasExpress {
		return symbol.PatternInfo{
			Name:        "Express API",
			Confidence:  "medium",
			Description: "Express.js API server",
		}, nil
	}

	if hasNext {
		return symbol.PatternInfo{
			Name:        "Next.js Application",
			Confidence:  "medium",
			Description: "Next.js application with page-based routing",
		}, nil
	}

	if hasReact {
		return symbol.PatternInfo{
			Name:        "React Application",
			Confidence:  "medium",
			Description: "React frontend application",
		}, nil
	}

	return symbol.PatternInfo{
		Name:        "JavaScript/TypeScript Package",
		Confidence:  "low",
		Description: "Standard JS/TS package without detected framework patterns",
	}, nil
}

func determineTSLanguage(filePath string) symbol.LanguageID {
	if strings.HasSuffix(filePath, ".ts") || strings.HasSuffix(filePath, ".tsx") {
		return symbol.LangTypeScript
	}
	return symbol.LangJavaScript
}

func determinePackageName(rootPath, filePath string) string {
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

func isTSExported(trimmed string) bool {
	return strings.HasPrefix(trimmed, "export")
}
