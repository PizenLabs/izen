package extractors

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/PizenLabs/izen/internal/retrieval/symbol"
)

var (
	pyClassRe     = regexp.MustCompile(`(?:^|\n)\s*class\s+([a-zA-Z_]\w*)`)
	pyDefRe       = regexp.MustCompile(`(?:^|\n)\s*def\s+([a-zA-Z_]\w*)\s*\(`)
	pyAsyncDefRe  = regexp.MustCompile(`(?:^|\n)\s*async\s+def\s+([a-zA-Z_]\w*)\s*\(`)
	pyDataclassRe = regexp.MustCompile(`@dataclass`)
	pyImportRe    = regexp.MustCompile(`^(?:from\s+([a-zA-Z_]\w*(?:\.[a-zA-Z_]\w*)*)\s+import|import\s+([a-zA-Z_]\w*(?:\.[a-zA-Z_]\w*)*))`)
)

type pythonExtractor struct{}

func NewPythonExtractor() symbol.LanguageExtractor {
	return &pythonExtractor{}
}

func (e *pythonExtractor) DetectLanguage(rootPath string) (symbol.LanguageID, bool) {
	if fileExists(rootPath, "pyproject.toml") || fileExists(rootPath, "setup.py") || fileExists(rootPath, "requirements.txt") {
		return symbol.LangPython, true
	}
	return "", false
}

func (e *pythonExtractor) ExtractSymbols(filePath string, content []byte) (*symbol.FileASTInfo, error) {
	info := &symbol.FileASTInfo{
		FilePath: filePath,
		Language: symbol.LangPython,
	}

	lines := strings.Split(string(content), "\n")

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		if m := pyImportRe.FindStringSubmatch(trimmed); m != nil {
			if m[1] != "" {
				info.Imports = append(info.Imports, symbol.DependencyEdge{
					SourcePackage: "",
					TargetPackage: m[1],
					ImportPath:    m[1],
					RelationType:  symbol.RelationImports,
				})
			}
			if m[2] != "" {
				info.Imports = append(info.Imports, symbol.DependencyEdge{
					SourcePackage: "",
					TargetPackage: m[2],
					ImportPath:    m[2],
					RelationType:  symbol.RelationImports,
				})
			}
		}

		if pyDataclassRe.MatchString(trimmed) {
			info.Symbols = append(info.Symbols, symbol.SymbolNode{
				Name:      "dataclass",
				Kind:      symbol.SymbolAnnotation,
				FilePath:  filePath,
				StartLine: i + 1,
				EndLine:   i + 1,
				Exported:  false,
			})
		}

		if m := pyClassRe.FindStringSubmatch(trimmed); m != nil {
			name := m[1]
			sym := symbol.SymbolNode{
				Name:      name,
				Kind:      symbol.SymbolClass,
				FilePath:  filePath,
				StartLine: i + 1,
				Exported:  isPythonExported(name),
			}
			info.Symbols = append(info.Symbols, sym)
			info.Classes = append(info.Classes, sym)
		}

		if m := pyDefRe.FindStringSubmatch(trimmed); m != nil {
			name := m[1]
			sym := symbol.SymbolNode{
				Name:      name,
				Kind:      symbol.SymbolFunction,
				FilePath:  filePath,
				StartLine: i + 1,
				Exported:  isPythonExported(name),
			}
			info.Symbols = append(info.Symbols, sym)
			info.Functions = append(info.Functions, sym)
		}

		if m := pyAsyncDefRe.FindStringSubmatch(trimmed); m != nil {
			name := m[1]
			sym := symbol.SymbolNode{
				Name:      name,
				Kind:      symbol.SymbolFunction,
				FilePath:  filePath,
				StartLine: i + 1,
				Exported:  isPythonExported(name),
			}
			info.Symbols = append(info.Symbols, sym)
			info.Functions = append(info.Functions, sym)
		}
	}

	info.Calls = scopeRegexCalls(info.Functions, extractRegexCalls(content))
	info.Routes = extractPythonRoutes(lines)

	return info, nil
}

func (e *pythonExtractor) ExtractPackages(rootPath string) ([]symbol.PackageNode, error) {
	var packages []symbol.PackageNode
	seen := make(map[string]bool)

	_ = filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil //nolint:nilerr
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".py") {
			return nil
		}
		if symbol.ShouldIgnorePath(path, rootPath) {
			return nil
		}

		_, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil //nolint:nilerr
		}

		pkgName := determinePythonPackage(rootPath, path)
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

func (e *pythonExtractor) DetectArchitecturePattern(nodes []symbol.PackageNode) (symbol.PatternInfo, error) {
	hasDjango := false
	hasFlask := false
	hasFastAPI := false

	for _, node := range nodes {
		for _, file := range node.Files {
			content, readErr := os.ReadFile(file)
			if readErr != nil {
				continue
			}
			c := string(content)
			if strings.Contains(c, "django") || strings.Contains(c, "from django") {
				hasDjango = true
			}
			if strings.Contains(c, "flask") || strings.Contains(c, "from flask") || strings.Contains(c, "Flask(") {
				hasFlask = true
			}
			if strings.Contains(c, "fastapi") || strings.Contains(c, "FastAPI") {
				hasFastAPI = true
			}
		}
	}

	if hasFastAPI {
		return symbol.PatternInfo{
			Name:        "FastAPI Application",
			Confidence:  "high",
			Description: "FastAPI API with route handlers and dependency injection",
		}, nil
	}

	if hasDjango {
		return symbol.PatternInfo{
			Name:        "Django Application",
			Confidence:  "high",
			Description: "Django MVC application with models, views, and apps",
		}, nil
	}

	if hasFlask {
		return symbol.PatternInfo{
			Name:        "Flask Application",
			Confidence:  "medium",
			Description: "Flask microservice or web application",
		}, nil
	}

	return symbol.PatternInfo{
		Name:        "Python Package",
		Confidence:  "low",
		Description: "Standard Python package without detected framework patterns",
	}, nil
}

func determinePythonPackage(rootPath, filePath string) string {
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

func isPythonExported(name string) bool {
	if name == "" {
		return false
	}
	return name[0] >= 'A' && name[0] <= 'Z'
}
