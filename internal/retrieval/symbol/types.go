package symbol

// LanguageID identifies a programming language.
type LanguageID string

const (
	LangGo         LanguageID = "go"
	LangJava       LanguageID = "java"
	LangTypeScript LanguageID = "typescript"
	LangJavaScript LanguageID = "javascript"
	LangPython     LanguageID = "python"
	LangRust       LanguageID = "rust"
	LangCC         LanguageID = "cpp"
	LangC          LanguageID = "c"
)

// SymbolKind classifies the kind of a symbols.
type SymbolKind string

const (
	SymbolPackage    SymbolKind = "package"
	SymbolModule     SymbolKind = "module"
	SymbolClass      SymbolKind = "class"
	SymbolInterface  SymbolKind = "interface"
	SymbolEnum       SymbolKind = "enum"
	SymbolStruct     SymbolKind = "struct"
	SymbolFunction   SymbolKind = "function"
	SymbolMethod     SymbolKind = "method"
	SymbolAnnotation SymbolKind = "annotation"
	SymbolVariable   SymbolKind = "variable"
	SymbolConstant   SymbolKind = "constant"
	SymbolType       SymbolKind = "type"
)

// RelationType classifies the kind of a dependency edge.
type RelationType string

const (
	RelationImports    RelationType = "imports"
	RelationExtends    RelationType = "extends"
	RelationImplements RelationType = "implements"
	RelationContains   RelationType = "contains"
	RelationUses       RelationType = "uses"
	RelationExports    RelationType = "exports"
)

// SymbolNode represents a symbol extracted from source code.
type SymbolNode struct {
	Name      string
	Kind      SymbolKind
	FilePath  string
	StartLine int
	EndLine   int
	Exported  bool
	Receiver  string
	Parent    string
	Signature string
}

// PackageNode represents a package or module in the source tree.
type PackageNode struct {
	Name        string
	RootPath    string
	Files       []string
	Symbols     []SymbolNode
	Layer       string
	EntryPoints []string
}

// DependencyEdge represents a dependency between packages.
type DependencyEdge struct {
	SourcePackage string
	TargetPackage string
	ImportPath    string
	RelationType  RelationType
}

// FileASTInfo holds the full AST extraction result for a single file.
type FileASTInfo struct {
	FilePath  string
	Language  LanguageID
	Package   string
	Symbols   []SymbolNode
	Imports   []DependencyEdge
	Classes   []SymbolNode
	Functions []SymbolNode
}

// PatternInfo holds the detected architectural pattern.
type PatternInfo struct {
	Name        string
	Confidence  string
	Description string
}
