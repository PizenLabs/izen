package graph

import "time"

type SymbolKind int

const (
	SymbolFunction SymbolKind = iota
	SymbolMethod
	SymbolType
	SymbolStruct
	SymbolInterface
	SymbolVariable
	SymbolConstant
	SymbolImport
	SymbolPackage
	SymbolField
	SymbolEnum
)

func (k SymbolKind) String() string {
	switch k {
	case SymbolFunction:
		return "function"
	case SymbolMethod:
		return "method"
	case SymbolType:
		return "type"
	case SymbolStruct:
		return "struct"
	case SymbolInterface:
		return "interface"
	case SymbolVariable:
		return "variable"
	case SymbolConstant:
		return "constant"
	case SymbolImport:
		return "import"
	case SymbolPackage:
		return "package"
	case SymbolField:
		return "field"
	case SymbolEnum:
		return "enum"
	default:
		return "unknown"
	}
}

type Symbol struct {
	Name      string     `json:"name"`
	Kind      SymbolKind `json:"kind"`
	File      string     `json:"file"`
	Line      int        `json:"line"`
	Column    int        `json:"column"`
	EndLine   int        `json:"end_line"`
	EndColumn int        `json:"end_column"`
	Parent    string     `json:"parent,omitempty"`
	Signature string     `json:"signature,omitempty"`
	Exported  bool       `json:"exported"`
}

type Language string

const (
	LangGo     Language = "go"
	LangPython Language = "python"
	LangRust   Language = "rust"
)

func LangFromExt(ext string) (Language, bool) {
	switch ext {
	case ".go":
		return LangGo, true
	case ".py":
		return LangPython, true
	case ".rs":
		return LangRust, true
	default:
		return "", false
	}
}

type FileNode struct {
	Path     string   `json:"path"`
	Language Language `json:"language"`
	Symbols  []Symbol `json:"symbols"`
	Imports  []string `json:"imports"`
	Package  string   `json:"package,omitempty"`
	Size     int64    `json:"size"`
	Lines    int      `json:"lines"`
}

type Graph struct {
	Root       string               `json:"root"`
	Files      []FileNode           `json:"files"`
	Imports    map[string][]string  `json:"imports"`
	Dependents map[string][]string  `json:"dependents"`
	SymbolIdx  map[string][]Symbol  `json:"symbol_index"`
	FileMap    map[string]*FileNode `json:"-"`
	BuiltAt    time.Time            `json:"built_at"`
	FileCount  int                  `json:"file_count"`
	SymCount   int                  `json:"sym_count"`
}

func NewGraph(root string) *Graph {
	return &Graph{
		Root:       root,
		Imports:    make(map[string][]string),
		Dependents: make(map[string][]string),
		SymbolIdx:  make(map[string][]Symbol),
		FileMap:    make(map[string]*FileNode),
		BuiltAt:    time.Now(),
	}
}

func (g *Graph) AddFile(fn FileNode) {
	g.Files = append(g.Files, fn)
	g.FileMap[fn.Path] = &g.Files[len(g.Files)-1]
	g.FileCount++
	g.SymCount += len(fn.Symbols)

	for _, sym := range fn.Symbols {
		g.SymbolIdx[sym.Name] = append(g.SymbolIdx[sym.Name], sym)
	}

	if len(fn.Imports) > 0 {
		g.Imports[fn.Path] = fn.Imports
		for _, imp := range fn.Imports {
			g.Dependents[imp] = append(g.Dependents[imp], fn.Path)
		}
	}
}

func (g *Graph) LookupSymbol(name string) []Symbol {
	return g.SymbolIdx[name]
}

func (g *Graph) LookupFile(path string) *FileNode {
	return g.FileMap[path]
}

func (g *Graph) FilesByPackage(pkg string) []FileNode {
	var result []FileNode
	for _, f := range g.Files {
		if f.Package == pkg {
			result = append(result, f)
		}
	}
	return result
}

type Stats struct {
	FileCount     int    `json:"file_count"`
	SymbolCount   int    `json:"symbol_count"`
	ImportCount   int    `json:"import_count"`
	FunctionCount int    `json:"function_count"`
	MethodCount   int    `json:"method_count"`
	TypeCount     int    `json:"type_count"`
	Duration      string `json:"duration"`
}

func (g *Graph) Stats() Stats {
	s := Stats{
		FileCount:   g.FileCount,
		SymbolCount: g.SymCount,
	}
	for _, f := range g.Files {
		s.ImportCount += len(f.Imports)
		for _, sym := range f.Symbols {
			switch sym.Kind {
			case SymbolFunction:
				s.FunctionCount++
			case SymbolMethod:
				s.MethodCount++
			case SymbolType, SymbolStruct, SymbolInterface:
				s.TypeCount++
			}
		}
	}
	return s
}
