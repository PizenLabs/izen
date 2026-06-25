package graph

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

		sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/rust"
)

type Parser struct {
	parsers map[Language]*sitter.Parser
}

func NewParser() *Parser {
	p := &Parser{
		parsers: make(map[Language]*sitter.Parser),
	}
	p.init(LangGo, golang.GetLanguage())
	p.init(LangPython, python.GetLanguage())
	p.init(LangRust, rust.GetLanguage())

	return p
}

func (p *Parser) init(lang Language, grammar *sitter.Language) {
	parser := sitter.NewParser()
	parser.SetLanguage(grammar)
	p.parsers[lang] = parser
}

func (p *Parser) ParseFile(root, relPath string, lang Language) (*FileNode, error) {
	abs := filepath.Join(root, relPath)
	source, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", relPath, err)
	}

	lines := 0
	for _, b := range source {
		if b == '\n' {
			lines++
		}
	}

	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}

	fn := FileNode{
		Path:     relPath,
		Language: lang,
		Size:     info.Size(),
		Lines:    lines + 1,
	}

	parser, ok := p.parsers[lang]
	if !ok {
		return &fn, nil
	}

	tree, err := parser.ParseCtx(context.Background(), nil, source)
	if err != nil {
		return &fn, nil
	}

	rootNode := tree.RootNode()
	if rootNode == nil {
		return &fn, nil
	}

	fn.Imports = extractImports(rootNode, source, lang)
	pkg := extractPackage(rootNode, source, lang)
	fn.Package = pkg
	fn.Symbols = extractSymbols(rootNode, source, lang, relPath)

	return &fn, nil
}

func extractPackage(n *sitter.Node, src []byte, lang Language) string {
	if lang != LangGo {
		return ""
	}
	c := int(n.NamedChildCount())
	for i := range c {
		child := n.NamedChild(i)
		if child == nil {
			continue
		}
		if child.Type() == "package_clause" {
			nameNode := child.ChildByFieldName("name")
			if nameNode != nil {
				return nameNode.Content(src)
			}
		}
	}
	return ""
}

func extractImports(n *sitter.Node, src []byte, lang Language) []string {
	var imports []string

	switch lang {
	case LangGo:
		collectNodeTypes(n, src, "import_spec", func(node *sitter.Node, content string) {
			path := strings.Trim(content, "\"")
			imports = append(imports, path)
		})
	case LangPython:
		collectImportStmts(n, src, &imports)
	case LangRust:
		collectNodeTypes(n, src, "use_declaration", func(node *sitter.Node, content string) {
			imports = append(imports, content)
		})
	}

	return uniqueStrings(imports)
}

func collectImportStmts(n *sitter.Node, src []byte, imports *[]string) {
	c := int(n.ChildCount())
	for i := range c {
		child := n.Child(i)
		if child == nil {
			continue
		}
		typ := child.Type()
		if typ == "import_statement" || typ == "import_declaration" {
			*imports = append(*imports, child.Content(src))
		}
		collectImportStmts(child, src, imports)
	}
}

func extractSymbols(n *sitter.Node, src []byte, lang Language, file string) []Symbol {
	var symbols []Symbol

	switch lang {
	case LangGo:
		extractGoSymbols(n, src, file, &symbols)
	case LangPython:
		extractPythonSymbols(n, src, file, &symbols)
	case LangRust:
		extractRustSymbols(n, src, file, &symbols)
	}

	return symbols
}

func extractGoSymbols(n *sitter.Node, src []byte, file string, symbols *[]Symbol) {
	c := int(n.NamedChildCount())
	for i := range c {
		child := n.NamedChild(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "function_declaration":
			sym := makeSymbol(child, src, file, SymbolFunction)
			if sym.Name != "" {
				nameNode := child.ChildByFieldName("name")
				if nameNode != nil {
					sym.Exported = isExportedName(nameNode.Content(src))
				}
				*symbols = append(*symbols, sym)
			}

		case "method_declaration":
			sym := makeSymbol(child, src, file, SymbolMethod)
			if sym.Name != "" {
				nameNode := child.ChildByFieldName("name")
				if nameNode != nil {
					sym.Exported = isExportedName(nameNode.Content(src))
				}
				recv := child.ChildByFieldName("receiver")
				if recv != nil {
					sym.Parent = extractGoTypeName(recv, src)
				}
				*symbols = append(*symbols, sym)
			}

		case "type_declaration":
			extractGoTypeSpecs(child, src, file, symbols)

		case "var_declaration":
			extractGoVarSpecs(child, src, file, SymbolVariable, symbols)

		case "const_declaration":
			extractGoVarSpecs(child, src, file, SymbolConstant, symbols)
		}

		extractGoSymbols(child, src, file, symbols)
	}
}

func extractGoTypeSpecs(n *sitter.Node, src []byte, file string, symbols *[]Symbol) {
	c := int(n.NamedChildCount())
	for i := range c {
		child := n.NamedChild(i)
		if child == nil || child.Type() != "type_spec" {
			continue
		}

		nameNode := child.ChildByFieldName("name")
		if nameNode == nil {
			continue
		}
		name := nameNode.Content(src)
		exported := isExportedName(name)

		typeNode := child.ChildByFieldName("type")
		var kind SymbolKind = SymbolType

		if typeNode != nil {
			switch typeNode.Type() {
			case "struct_type":
				kind = SymbolStruct
				extractGoFields(typeNode, src, file, name, symbols)
			case "interface_type":
				kind = SymbolInterface
				extractGoFields(typeNode, src, file, name, symbols)
			}
		}

		sym := makeSymbol(child, src, file, kind)
		sym.Name = name
		sym.Exported = exported
		*symbols = append(*symbols, sym)
	}
}

func extractGoVarSpecs(n *sitter.Node, src []byte, file string, kind SymbolKind, symbols *[]Symbol) {
	c := int(n.NamedChildCount())
	for i := range c {
		child := n.NamedChild(i)
		if child == nil || child.Type() != "const_spec" && child.Type() != "var_spec" {
			continue
		}

		nameNode := child.ChildByFieldName("name")
		if nameNode == nil {
			continue
		}

		sym := makeSymbol(child, src, file, kind)
		sym.Name = nameNode.Content(src)
		sym.Exported = isExportedName(sym.Name)
		*symbols = append(*symbols, sym)
	}
}

func extractGoFields(n *sitter.Node, src []byte, file, parent string, symbols *[]Symbol) {
	c := int(n.NamedChildCount())
	for i := range c {
		child := n.NamedChild(i)
		if child == nil || child.Type() != "field_declaration" {
			continue
		}

		nameNode := child.ChildByFieldName("name")
		if nameNode == nil {
			continue
		}

		sym := makeSymbol(child, src, file, SymbolField)
		sym.Name = nameNode.Content(src)
		sym.Parent = parent
		*symbols = append(*symbols, sym)
	}
}

func extractGoTypeName(n *sitter.Node, src []byte) string {
	c := int(n.NamedChildCount())
	for i := range c {
		child := n.NamedChild(i)
		if child == nil {
			continue
		}
		if child.Type() == "type_identifier" {
			return child.Content(src)
		}
	}
	return ""
}

func extractTSSymbols(n *sitter.Node, src []byte, file string, symbols *[]Symbol) {
	c := int(n.NamedChildCount())
	for i := range c {
		child := n.NamedChild(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "function_declaration":
			sym := makeSymbol(child, src, file, SymbolFunction)
			*symbols = append(*symbols, sym)
		case "method_definition":
			sym := makeSymbol(child, src, file, SymbolMethod)
			*symbols = append(*symbols, sym)
		case "class_declaration":
			sym := makeSymbol(child, src, file, SymbolType)
			*symbols = append(*symbols, sym)
		case "interface_declaration":
			sym := makeSymbol(child, src, file, SymbolInterface)
			*symbols = append(*symbols, sym)
		case "type_alias_declaration":
			sym := makeSymbol(child, src, file, SymbolType)
			*symbols = append(*symbols, sym)
		case "enum_declaration":
			sym := makeSymbol(child, src, file, SymbolEnum)
			*symbols = append(*symbols, sym)
		case "lexical_declaration", "variable_declaration":
			extractTSVarDeclarations(child, src, file, SymbolVariable, symbols)
		}

		extractTSSymbols(child, src, file, symbols)
	}
}

func extractTSVarDeclarations(n *sitter.Node, src []byte, file string, kind SymbolKind, symbols *[]Symbol) {
	c := int(n.NamedChildCount())
	for i := range c {
		child := n.NamedChild(i)
		if child == nil || child.Type() != "variable_declarator" {
			continue
		}
		nameNode := child.ChildByFieldName("name")
		if nameNode != nil {
			sym := makeSymbol(child, src, file, kind)
			sym.Name = nameNode.Content(src)
			*symbols = append(*symbols, sym)
		}
	}
}

func extractPythonSymbols(n *sitter.Node, src []byte, file string, symbols *[]Symbol) {
	c := int(n.NamedChildCount())
	for i := range c {
		child := n.NamedChild(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "function_definition":
			sym := makeSymbol(child, src, file, SymbolFunction)
			*symbols = append(*symbols, sym)
		case "class_definition":
			sym := makeSymbol(child, src, file, SymbolType)
			*symbols = append(*symbols, sym)
		}

		extractPythonSymbols(child, src, file, symbols)
	}
}

func extractRustSymbols(n *sitter.Node, src []byte, file string, symbols *[]Symbol) {
	c := int(n.ChildCount())
	for i := range c {
		child := n.Child(i)
		if child == nil {
			continue
		}

		switch child.Type() {
		case "function_item":
			sym := makeSymbol(child, src, file, SymbolFunction)
			*symbols = append(*symbols, sym)
		case "struct_item":
			sym := makeSymbol(child, src, file, SymbolStruct)
			*symbols = append(*symbols, sym)
		case "trait_item":
			sym := makeSymbol(child, src, file, SymbolInterface)
			*symbols = append(*symbols, sym)
		case "impl_item":
			sym := makeSymbol(child, src, file, SymbolMethod)
			*symbols = append(*symbols, sym)
		case "enum_item":
			sym := makeSymbol(child, src, file, SymbolEnum)
			*symbols = append(*symbols, sym)
		case "type_item":
			sym := makeSymbol(child, src, file, SymbolType)
			*symbols = append(*symbols, sym)
		case "const_item":
			sym := makeSymbol(child, src, file, SymbolConstant)
			*symbols = append(*symbols, sym)
		}

		extractRustSymbols(child, src, file, symbols)
	}
}

func makeSymbol(n *sitter.Node, src []byte, file string, kind SymbolKind) Symbol {
	sym := Symbol{
		Kind:   kind,
		File:   file,
		Line:   int(n.StartPoint().Row) + 1,
		Column: int(n.StartPoint().Column) + 1,
	}

	end := n.EndPoint()
	sym.EndLine = int(end.Row) + 1
	sym.EndColumn = int(end.Column) + 1

	nameNode := n.ChildByFieldName("name")
	if nameNode != nil {
		sym.Name = nameNode.Content(src)
	} else {
		sym.Name = extractFirstName(n, src)
	}

	sym.Signature = truncateString(n.Content(src), 120)

	return sym
}

func extractFirstName(n *sitter.Node, src []byte) string {
	c := int(n.NamedChildCount())
	for i := range c {
		child := n.NamedChild(i)
		if child == nil {
			continue
		}
		nameNode := child.ChildByFieldName("name")
		if nameNode != nil {
			return nameNode.Content(src)
		}
	}
	return ""
}

func collectNodeTypes(n *sitter.Node, src []byte, targetType string, fn func(*sitter.Node, string)) {
	c := int(n.ChildCount())
	for i := range c {
		child := n.Child(i)
		if child == nil {
			continue
		}
		if child.Type() == targetType {
			fn(child, child.Content(src))
		}
		collectNodeTypes(child, src, targetType, fn)
	}
}

func isExportedName(name string) bool {
	if name == "" {
		return false
	}
	return name[0] >= 'A' && name[0] <= 'Z'
}

func uniqueStrings(s []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			result = append(result, v)
		}
	}
	return result
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}