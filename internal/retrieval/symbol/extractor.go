package symbol

// LanguageExtractor is the interface that all language-specific
// AST/Symbol extractors must implement.
type LanguageExtractor interface {
	// DetectLanguage inspects the root path for project markers
	// (build files, config files) and returns the language ID if recognized.
	DetectLanguage(rootPath string) (LanguageID, bool)

	// ExtractSymbols parses a single source file and returns its
	// AST symbol information.
	ExtractSymbols(filePath string, content []byte) (*FileASTInfo, error)

	// ExtractPackages walks the root path and discovers all packages/modules.
	ExtractPackages(rootPath string) ([]PackageNode, error)

	// DetectArchitecturePattern analyzes the package graph and returns
	// the detected architectural pattern (e.g. "layered", "monolith", "microservices").
	DetectArchitecturePattern(nodes []PackageNode) (PatternInfo, error)
}
