package execution

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
)

type AffectedFunction struct {
	Name       string `json:"name"`
	Package    string `json:"package"`
	Exported   bool   `json:"exported"`
	ChangeType string `json:"change_type"`
}

type AffectedType struct {
	Name       string `json:"name"`
	Package    string `json:"package"`
	Exported   bool   `json:"exported"`
	ChangeType string `json:"change_type"`
}

type DiffReport struct {
	ModifiedPackages   []string           `json:"modified_packages"`
	AffectedFunctions  []AffectedFunction `json:"affected_functions"`
	AffectedTypes      []AffectedType     `json:"affected_types"`
	PublicAPIChanged   bool               `json:"public_api_changed"`
	NewFiles           []string           `json:"new_files,omitempty"`
	DeletedFiles       []string           `json:"deleted_files,omitempty"`
	SchemaChanges      bool               `json:"schema_changes"`
	TotalFilesModified int                `json:"total_files_modified"`
}

func (r DiffReport) String() string {
	var b strings.Builder

	b.WriteString("Modified package:")
	if len(r.ModifiedPackages) == 0 {
		b.WriteString("\nnone")
	} else {
		for _, pkg := range r.ModifiedPackages {
			fmt.Fprintf(&b, "\n  %s", pkg)
		}
	}

	if len(r.AffectedFunctions) > 0 {
		b.WriteString("\nAffected:")
		for _, fn := range r.AffectedFunctions {
			mark := "•"
			if fn.Exported {
				mark = "⬆"
			}
			fmt.Fprintf(&b, "\n  %s %s() [%s]", mark, fn.Name, fn.ChangeType)
		}
	}

	if len(r.AffectedTypes) > 0 {
		b.WriteString("\nTypes:")
		for _, t := range r.AffectedTypes {
			mark := "•"
			if t.Exported {
				mark = "⬆"
			}
			fmt.Fprintf(&b, "\n  %s %s [%s]", mark, t.Name, t.ChangeType)
		}
	}

	b.WriteString("\nPublic API:")
	if r.PublicAPIChanged {
		b.WriteString(" Modified")
	} else {
		b.WriteString(" No changes")
	}

	b.WriteString("\nDatabase:")
	if r.SchemaChanges {
		b.WriteString(" Schema changes detected")
	} else {
		b.WriteString(" No schema changes")
	}

	return b.String()
}

type DiffAnalyzer struct{}

func NewDiffAnalyzer() *DiffAnalyzer {
	return &DiffAnalyzer{}
}

func extractPkgFromPath(path string) string {
	dir := filepath.Dir(path)
	parts := strings.Split(dir, string(filepath.Separator))
	for i, p := range parts {
		if p == "internal" && i+1 < len(parts) {
			return filepath.Join("internal", parts[i+1])
		}
	}
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return "root"
}

func (da *DiffAnalyzer) AnalyzeContent(filePath, original, modified string) DiffReport {
	report := DiffReport{}
	report.ModifiedPackages = append(report.ModifiedPackages, extractPkgFromPath(filePath))
	report.TotalFilesModified = 1

	if original == "" && modified != "" {
		report.NewFiles = append(report.NewFiles, filePath)
		report.PublicAPIChanged = true
	} else if modified == "" && original != "" {
		report.DeletedFiles = append(report.DeletedFiles, filePath)
		report.PublicAPIChanged = true
	}

	if !strings.HasSuffix(filePath, ".go") {
		if strings.HasSuffix(filePath, ".sql") || strings.HasSuffix(filePath, ".sqlite") {
			report.SchemaChanges = true
		}
		return report
	}

	origFuncs := extractFunctions(original)
	modFuncs := extractFunctions(modified)

	origFuncMap := make(map[string]bool)
	for _, f := range origFuncs {
		origFuncMap[f] = true
	}

	for _, f := range modFuncs {
		if !origFuncMap[f] {
			isExported := len(f) > 0 && f[0] >= 'A' && f[0] <= 'Z'
			report.AffectedFunctions = append(report.AffectedFunctions, AffectedFunction{
				Name:       f,
				Package:    extractPkgFromPath(filePath),
				Exported:   isExported,
				ChangeType: "added",
			})
			if isExported {
				report.PublicAPIChanged = true
			}
		}
	}

	origFuncMapInv := make(map[string]bool)
	for _, f := range modFuncs {
		origFuncMapInv[f] = true
	}
	for _, f := range origFuncs {
		if !origFuncMapInv[f] {
			isExported := len(f) > 0 && f[0] >= 'A' && f[0] <= 'Z'
			report.AffectedFunctions = append(report.AffectedFunctions, AffectedFunction{
				Name:       f,
				Package:    extractPkgFromPath(filePath),
				Exported:   isExported,
				ChangeType: "removed",
			})
			if isExported {
				report.PublicAPIChanged = true
			}
		}
	}

	origTypes := extractTypes(original)
	modTypes := extractTypes(modified)

	origTypeMap := make(map[string]bool)
	for _, t := range origTypes {
		origTypeMap[t] = true
	}
	for _, t := range modTypes {
		if !origTypeMap[t] {
			isExported := len(t) > 0 && t[0] >= 'A' && t[0] <= 'Z'
			report.AffectedTypes = append(report.AffectedTypes, AffectedType{
				Name:       t,
				Package:    extractPkgFromPath(filePath),
				Exported:   isExported,
				ChangeType: "added",
			})
			if isExported {
				report.PublicAPIChanged = true
			}
		}
	}

	modTypeMap := make(map[string]bool)
	for _, t := range modTypes {
		modTypeMap[t] = true
	}
	for _, t := range origTypes {
		if !modTypeMap[t] {
			isExported := len(t) > 0 && t[0] >= 'A' && t[0] <= 'Z'
			report.AffectedTypes = append(report.AffectedTypes, AffectedType{
				Name:       t,
				Package:    extractPkgFromPath(filePath),
				Exported:   isExported,
				ChangeType: "removed",
			})
			if isExported {
				report.PublicAPIChanged = true
			}
		}
	}

	return report
}

func (da *DiffAnalyzer) AnalyzePatches(patches []StagedPatch) DiffReport {
	report := DiffReport{}
	pkgSet := make(map[string]bool)

	for _, sp := range patches {
		pkg := extractPkgFromPath(sp.File)
		if !pkgSet[pkg] {
			pkgSet[pkg] = true
			report.ModifiedPackages = append(report.ModifiedPackages, pkg)
		}

		if !strings.HasSuffix(sp.File, ".go") {
			if strings.HasSuffix(sp.File, ".sql") {
				report.SchemaChanges = true
			}
			continue
		}

		fns := extractFunctions(sp.Content)
		for _, f := range fns {
			isExported := len(f) > 0 && f[0] >= 'A' && f[0] <= 'Z'
			report.AffectedFunctions = append(report.AffectedFunctions, AffectedFunction{
				Name:       f,
				Package:    pkg,
				Exported:   isExported,
				ChangeType: "modified_or_added",
			})
			if isExported {
				report.PublicAPIChanged = true
			}
		}

		types := extractTypes(sp.Content)
		for _, t := range types {
			isExported := len(t) > 0 && t[0] >= 'A' && t[0] <= 'Z'
			report.AffectedTypes = append(report.AffectedTypes, AffectedType{
				Name:       t,
				Package:    pkg,
				Exported:   isExported,
				ChangeType: "modified_or_added",
			})
			if isExported {
				report.PublicAPIChanged = true
			}
		}
	}

	report.TotalFilesModified = len(patches)
	return report
}

type funcVisitor struct {
	Functions []string
}

func (v *funcVisitor) Visit(node ast.Node) ast.Visitor {
	switch n := node.(type) {
	case *ast.FuncDecl:
		if n.Name != nil {
			v.Functions = append(v.Functions, n.Name.Name)
		}
	case *ast.FuncLit:
	}
	return v
}

type typeVisitor struct {
	Types []string
}

func (v *typeVisitor) Visit(node ast.Node) ast.Visitor {
	switch n := node.(type) {
	case *ast.TypeSpec:
		if n.Name != nil {
			v.Types = append(v.Types, n.Name.Name)
		}
	case *ast.StructType:
	case *ast.InterfaceType:
	}
	return v
}

func extractFunctions(src string) []string {
	if strings.TrimSpace(src) == "" {
		return nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return extractFunctionsFallback(src)
	}
	v := &funcVisitor{}
	ast.Walk(v, f)
	return v.Functions
}

func extractTypes(src string) []string {
	if strings.TrimSpace(src) == "" {
		return nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return nil
	}
	v := &typeVisitor{}
	ast.Walk(v, f)
	return v.Types
}

var funcDeclRe = regexp.MustCompile(`(?:^|\n)\s*(?:func\s+)(\([^)]*\)\s*)?([A-Za-z_]\w*)\s*\(`)

func extractFunctionsFallback(src string) []string {
	var funcs []string
	matches := funcDeclRe.FindAllStringSubmatch(src, -1)
	seen := make(map[string]bool)
	for _, m := range matches {
		name := m[2]
		if name != "" && !seen[name] && !isCommonKeyword(name) {
			seen[name] = true
			funcs = append(funcs, name)
		}
	}
	return funcs
}

func isCommonKeyword(s string) bool {
	switch s {
	case "if", "for", "switch", "return", "defer", "go", "select", "case":
		return true
	}
	return false
}
