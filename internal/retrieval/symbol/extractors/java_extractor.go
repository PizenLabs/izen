package extractors

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/PizenLabs/izen/internal/retrieval/symbol"
)

var (
	javaPackageRe    = regexp.MustCompile(`^\s*package\s+([a-zA-Z_]\w*(?:\.[a-zA-Z_]\w*)*)\s*;`)
	javaClassRe      = regexp.MustCompile(`(?:public|protected|private)?\s*(?:abstract\s+|final\s+)?class\s+([a-zA-Z_]\w*)`)
	javaInterfaceRe  = regexp.MustCompile(`(?:public|protected|private)?\s*(?:abstract\s+)?interface\s+([a-zA-Z_]\w*)`)
	javaEnumRe       = regexp.MustCompile(`(?:public|protected|private)?\s*enum\s+([a-zA-Z_]\w*)`)
	javaRecordRe     = regexp.MustCompile(`(?:public|protected|private)?\s*record\s+([a-zA-Z_]\w*)\s*\(`)
	javaMethodRe     = regexp.MustCompile(`(?:public|protected|private)?\s*(?:static\s+)?(?:final\s+)?(?:synchronized\s+)?(?:\w[\w<>\[\]]*(?:\s+\w+)?\s+)+(\w+)\s*\(`)
	springBootAppRe  = regexp.MustCompile(`@SpringBootApplication`)
	restControllerRe = regexp.MustCompile(`@RestController`)
	serviceRe        = regexp.MustCompile(`@Service`)
	repositoryRe     = regexp.MustCompile(`@Repository`)
	entityRe         = regexp.MustCompile(`@Entity`)
	componentRe      = regexp.MustCompile(`@Component`)
)

type javaExtractor struct{}

func NewJavaExtractor() symbol.LanguageExtractor {
	return &javaExtractor{}
}

func (e *javaExtractor) DetectLanguage(rootPath string) (symbol.LanguageID, bool) {
	if fileExists(rootPath, "pom.xml") || fileExists(rootPath, "build.gradle") || fileExists(rootPath, "build.gradle.kts") {
		return symbol.LangJava, true
	}
	return "", false
}

func (e *javaExtractor) ExtractSymbols(filePath string, content []byte) (*symbol.FileASTInfo, error) {
	info := &symbol.FileASTInfo{
		FilePath: filePath,
		Language: symbol.LangJava,
	}

	lines := strings.Split(string(content), "\n")
	pkg := ""

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		if m := javaPackageRe.FindStringSubmatch(trimmed); m != nil {
			pkg = m[1]
			info.Package = pkg
		}

		if springBootAppRe.MatchString(trimmed) {
			info.Symbols = append(info.Symbols, symbol.SymbolNode{
				Name:      "SpringBootApplication",
				Kind:      symbol.SymbolAnnotation,
				FilePath:  filePath,
				StartLine: i + 1,
				EndLine:   i + 1,
				Exported:  true,
			})
		}

		if restControllerRe.MatchString(trimmed) {
			info.Symbols = append(info.Symbols, symbol.SymbolNode{
				Name:      "RestController",
				Kind:      symbol.SymbolAnnotation,
				FilePath:  filePath,
				StartLine: i + 1,
				EndLine:   i + 1,
				Exported:  true,
			})
		}

		if serviceRe.MatchString(trimmed) {
			info.Symbols = append(info.Symbols, symbol.SymbolNode{
				Name:      "Service",
				Kind:      symbol.SymbolAnnotation,
				FilePath:  filePath,
				StartLine: i + 1,
				EndLine:   i + 1,
				Exported:  true,
			})
		}

		if repositoryRe.MatchString(trimmed) {
			info.Symbols = append(info.Symbols, symbol.SymbolNode{
				Name:      "Repository",
				Kind:      symbol.SymbolAnnotation,
				FilePath:  filePath,
				StartLine: i + 1,
				EndLine:   i + 1,
				Exported:  true,
			})
		}

		if entityRe.MatchString(trimmed) {
			info.Symbols = append(info.Symbols, symbol.SymbolNode{
				Name:      "Entity",
				Kind:      symbol.SymbolAnnotation,
				FilePath:  filePath,
				StartLine: i + 1,
				EndLine:   i + 1,
				Exported:  true,
			})
		}

		if componentRe.MatchString(trimmed) {
			info.Symbols = append(info.Symbols, symbol.SymbolNode{
				Name:      "Component",
				Kind:      symbol.SymbolAnnotation,
				FilePath:  filePath,
				StartLine: i + 1,
				EndLine:   i + 1,
				Exported:  true,
			})
		}

		if m := javaClassRe.FindStringSubmatch(trimmed); m != nil {
			name := m[1]
			sym := symbol.SymbolNode{
				Name:      name,
				Kind:      symbol.SymbolClass,
				FilePath:  filePath,
				StartLine: i + 1,
				Exported:  isJavaExported(trimmed),
			}
			info.Symbols = append(info.Symbols, sym)
			info.Classes = append(info.Classes, sym)
		}

		if m := javaInterfaceRe.FindStringSubmatch(trimmed); m != nil {
			name := m[1]
			sym := symbol.SymbolNode{
				Name:      name,
				Kind:      symbol.SymbolInterface,
				FilePath:  filePath,
				StartLine: i + 1,
				Exported:  isJavaExported(trimmed),
			}
			info.Symbols = append(info.Symbols, sym)
		}

		if m := javaEnumRe.FindStringSubmatch(trimmed); m != nil {
			name := m[1]
			sym := symbol.SymbolNode{
				Name:      name,
				Kind:      symbol.SymbolEnum,
				FilePath:  filePath,
				StartLine: i + 1,
				Exported:  isJavaExported(trimmed),
			}
			info.Symbols = append(info.Symbols, sym)
		}

		if m := javaRecordRe.FindStringSubmatch(trimmed); m != nil {
			name := m[1]
			sym := symbol.SymbolNode{
				Name:      name,
				Kind:      symbol.SymbolStruct,
				FilePath:  filePath,
				StartLine: i + 1,
				Exported:  isJavaExported(trimmed),
			}
			info.Symbols = append(info.Symbols, sym)
			info.Classes = append(info.Classes, sym)
		}

		if m := javaMethodRe.FindStringSubmatch(trimmed); m != nil {
			name := m[1]
			sym := symbol.SymbolNode{
				Name:      name,
				Kind:      symbol.SymbolMethod,
				FilePath:  filePath,
				StartLine: i + 1,
				Exported:  isJavaExported(trimmed),
			}
			info.Symbols = append(info.Symbols, sym)
			info.Functions = append(info.Functions, sym)
		}
	}

	return info, nil
}

func (e *javaExtractor) ExtractPackages(rootPath string) ([]symbol.PackageNode, error) {
	var packages []symbol.PackageNode
	seen := make(map[string]bool)

	_ = filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil //nolint:nilerr
		}
		if info.IsDir() || !strings.HasSuffix(path, ".java") {
			return nil
		}

		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil //nolint:nilerr
		}

		pkg := extractJavaPackage(src)
		if pkg == "" {
			return nil
		}

		if !seen[pkg] {
			seen[pkg] = true
			packages = append(packages, symbol.PackageNode{
				Name:     pkg,
				RootPath: rootPath,
				Files:    []string{path},
			})
		} else {
			for i := range packages {
				if packages[i].Name == pkg {
					packages[i].Files = append(packages[i].Files, path)
					break
				}
			}
		}
		return nil
	})

	return packages, nil
}

func (e *javaExtractor) DetectArchitecturePattern(nodes []symbol.PackageNode) (symbol.PatternInfo, error) {
	hasSpring := false
	hasController := false
	hasService := false
	hasRepository := false

	for _, node := range nodes {
		for _, file := range node.Files {
			src, err := os.ReadFile(file)
			if err != nil {
				continue
			}
			content := string(src)
			if springBootAppRe.MatchString(content) {
				hasSpring = true
			}
			if restControllerRe.MatchString(content) {
				hasController = true
			}
			if serviceRe.MatchString(content) {
				hasService = true
			}
			if repositoryRe.MatchString(content) {
				hasRepository = true
			}
		}
	}

	if hasSpring && hasController && hasService && hasRepository {
		return symbol.PatternInfo{
			Name:        "Spring Layered Architecture",
			Confidence:  "high",
			Description: "Spring Boot application with Controller-Service-Repository layering",
		}, nil
	}

	if hasSpring {
		return symbol.PatternInfo{
			Name:        "Spring Application",
			Confidence:  "medium",
			Description: "Spring-based application with partial layering",
		}, nil
	}

	return symbol.PatternInfo{
		Name:        "Java Package Structure",
		Confidence:  "low",
		Description: "Standard Java package organization without detected framework patterns",
	}, nil
}

func extractJavaPackage(src []byte) string {
	lines := strings.Split(string(src), "\n")
	for _, line := range lines {
		if m := javaPackageRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			return m[1]
		}
	}
	return ""
}

func isJavaExported(trimmed string) bool {
	return strings.HasPrefix(trimmed, "public")
}
