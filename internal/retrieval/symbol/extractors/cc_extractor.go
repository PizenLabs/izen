package extractors

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/PizenLabs/izen/internal/retrieval/symbol"
)

var (
	cppIncludeRe   = regexp.MustCompile(`#include\s+[<"]([^>"]+)[>"]`)
	cppClassRe     = regexp.MustCompile(`(?:^|\n)\s*class\s+([a-zA-Z_]\w*)`)
	cppStructRe    = regexp.MustCompile(`(?:^|\n)\s*struct\s+([a-zA-Z_]\w*)`)
	cppEnumRe      = regexp.MustCompile(`(?:^|\n)\s*enum\s+(?:class\s+)?([a-zA-Z_]\w*)`)
	cppNamespaceRe = regexp.MustCompile(`(?:^|\n)\s*namespace\s+([a-zA-Z_]\w*)`)
	cppFuncRe      = regexp.MustCompile(`(?:^|\n)\s*(?:inline\s+)?(?:static\s+)?(?:const\s+)?(?:\w[\w<>\[\]]*(?:\s+\w+)?\s+)+([a-zA-Z_]\w*)\s*\(`)
	cppTypedefRe   = regexp.MustCompile(`(?:^|\n)\s*typedef\s+(?:\w[\w<>\[\]]*(?:\s+\w+)?\s+)+([a-zA-Z_]\w*)`)
	cppUsingRe     = regexp.MustCompile(`(?:^|\n)\s*using\s+([a-zA-Z_]\w*)\s*=`)
)

type ccExtractor struct{}

func NewCCExtractor() symbol.LanguageExtractor {
	return &ccExtractor{}
}

func (e *ccExtractor) DetectLanguage(rootPath string) (symbol.LanguageID, bool) {
	if fileExists(rootPath, "Makefile") || fileExists(rootPath, "CMakeLists.txt") {
		return symbol.LangCC, true
	}
	count := 0
	_ = filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil //nolint:nilerr
		}
		if info.IsDir() {
			return nil
		}
		if symbol.ShouldIgnorePath(path, rootPath) {
			return nil
		}
		lower := strings.ToLower(path)
		if strings.HasSuffix(lower, ".c") || strings.HasSuffix(lower, ".h") ||
			strings.HasSuffix(lower, ".cpp") || strings.HasSuffix(lower, ".cc") ||
			strings.HasSuffix(lower, ".cxx") || strings.HasSuffix(lower, ".hpp") || strings.HasSuffix(lower, ".hh") {
			count++
		}
		return nil
	})
	return symbol.LangCC, count > 0
}

func (e *ccExtractor) ExtractSymbols(filePath string, content []byte) (*symbol.FileASTInfo, error) {
	info := &symbol.FileASTInfo{
		FilePath: filePath,
		Language: determineCCLanguage(filePath),
	}

	lines := strings.Split(string(content), "\n")

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		if m := cppIncludeRe.FindStringSubmatch(trimmed); m != nil {
			info.Imports = append(info.Imports, symbol.DependencyEdge{
				SourcePackage: "",
				TargetPackage: m[1],
				ImportPath:    m[1],
				RelationType:  symbol.RelationImports,
			})
		}

		if m := cppClassRe.FindStringSubmatch(trimmed); m != nil {
			name := m[1]
			sym := symbol.SymbolNode{
				Name:      name,
				Kind:      symbol.SymbolClass,
				FilePath:  filePath,
				StartLine: i + 1,
				Exported:  isCPPExported(trimmed),
			}
			info.Symbols = append(info.Symbols, sym)
			info.Classes = append(info.Classes, sym)
		}

		if m := cppStructRe.FindStringSubmatch(trimmed); m != nil {
			name := m[1]
			sym := symbol.SymbolNode{
				Name:      name,
				Kind:      symbol.SymbolStruct,
				FilePath:  filePath,
				StartLine: i + 1,
				Exported:  isCPPExported(trimmed),
			}
			info.Symbols = append(info.Symbols, sym)
		}

		if m := cppEnumRe.FindStringSubmatch(trimmed); m != nil {
			sym := symbol.SymbolNode{
				Name:      m[1],
				Kind:      symbol.SymbolEnum,
				FilePath:  filePath,
				StartLine: i + 1,
				Exported:  isCPPExported(trimmed),
			}
			info.Symbols = append(info.Symbols, sym)
		}

		if m := cppNamespaceRe.FindStringSubmatch(trimmed); m != nil {
			sym := symbol.SymbolNode{
				Name:      m[1],
				Kind:      symbol.SymbolModule,
				FilePath:  filePath,
				StartLine: i + 1,
				Exported:  isCPPExported(trimmed),
			}
			info.Symbols = append(info.Symbols, sym)
		}

		if m := cppFuncRe.FindStringSubmatch(trimmed); m != nil {
			sym := symbol.SymbolNode{
				Name:      m[1],
				Kind:      symbol.SymbolFunction,
				FilePath:  filePath,
				StartLine: i + 1,
				Exported:  isCPPExported(trimmed),
			}
			info.Symbols = append(info.Symbols, sym)
			info.Functions = append(info.Functions, sym)
		}

		if m := cppTypedefRe.FindStringSubmatch(trimmed); m != nil {
			sym := symbol.SymbolNode{
				Name:      m[1],
				Kind:      symbol.SymbolType,
				FilePath:  filePath,
				StartLine: i + 1,
				Exported:  isCPPExported(trimmed),
			}
			info.Symbols = append(info.Symbols, sym)
		}

		if m := cppUsingRe.FindStringSubmatch(trimmed); m != nil {
			sym := symbol.SymbolNode{
				Name:      m[1],
				Kind:      symbol.SymbolType,
				FilePath:  filePath,
				StartLine: i + 1,
				Exported:  isCPPExported(trimmed),
			}
			info.Symbols = append(info.Symbols, sym)
		}
	}

	return info, nil
}

func (e *ccExtractor) ExtractPackages(rootPath string) ([]symbol.PackageNode, error) {
	var packages []symbol.PackageNode
	seen := make(map[string]bool)

	_ = filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil //nolint:nilerr
		}
		if info.IsDir() {
			return nil
		}

		isSource := false
		lower := strings.ToLower(path)
		if strings.HasSuffix(lower, ".c") || strings.HasSuffix(lower, ".h") ||
			strings.HasSuffix(lower, ".cpp") || strings.HasSuffix(lower, ".cc") ||
			strings.HasSuffix(lower, ".cxx") || strings.HasSuffix(lower, ".hpp") || strings.HasSuffix(lower, ".hh") {
			isSource = true
		}
		if !isSource {
			return nil
		}
		if symbol.ShouldIgnorePath(path, rootPath) {
			return nil
		}

		pkgName := determineCCPackage(rootPath, path)
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

func (e *ccExtractor) DetectArchitecturePattern(nodes []symbol.PackageNode) (symbol.PatternInfo, error) {
	return symbol.PatternInfo{
		Name:        "C/C++ Package",
		Confidence:  "low",
		Description: "C/C++ project with header and source files",
	}, nil
}

func determineCCLanguage(filePath string) symbol.LanguageID {
	lower := strings.ToLower(filePath)
	if strings.HasSuffix(lower, ".c") {
		return symbol.LangC
	}
	return symbol.LangCC
}

func determineCCPackage(rootPath, filePath string) string {
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

func isCPPExported(trimmed string) bool {
	return strings.HasPrefix(trimmed, "class") || strings.HasPrefix(trimmed, "struct") || strings.HasPrefix(trimmed, "public")
}
