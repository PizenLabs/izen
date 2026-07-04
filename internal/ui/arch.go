package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/internal/graph"
	"github.com/PizenLabs/izen/internal/retrieval"
)

type archPackage struct {
	Name    string
	Structs []archStruct
	Routes  []archRoute
}

type archStruct struct {
	Name     string
	Kind     string
	Exported bool
	Methods  []archMethod
}

type archMethod struct {
	Name     string
	Exported bool
}

type archRoute struct {
	File   string
	Symbol string
	Line   int
}

type treeItem struct {
	label    string
	children []treeItem
}

func buildArchTree(g *graph.Graph) []archPackage {
	seen := make(map[string]bool)
	packages := make(map[string]*archPackage)

	for _, fn := range g.Files {
		if seen[fn.Path] {
			continue
		}
		seen[fn.Path] = true

		pkgName := fn.Package
		if pkgName == "" {
			parts := strings.Split(fn.Path, string([]rune{'/'}))
			if len(parts) > 1 {
				pkgName = parts[len(parts)-2]
			} else {
				pkgName = "root"
			}
		}

		ap, ok := packages[pkgName]
		if !ok {
			ap = &archPackage{Name: pkgName}
			packages[pkgName] = ap
		}

		structsMap := make(map[string]*archStruct)
		for _, sym := range fn.Symbols {
			switch sym.Kind {
			case graph.SymbolStruct, graph.SymbolInterface, graph.SymbolType:
				if _, exists := structsMap[sym.Name]; !exists {
					s := archStruct{
						Name:     sym.Name,
						Exported: sym.Exported,
					}
					switch sym.Kind {
					case graph.SymbolInterface:
						s.Kind = "interface"
					case graph.SymbolStruct:
						s.Kind = "struct"
					default:
						s.Kind = "type"
					}
					structsMap[sym.Name] = &s
				}
			case graph.SymbolMethod:
				parent := sym.Parent
				if parent != "" {
					s, exists := structsMap[parent]
					if !exists {
						s = &archStruct{
							Name: parent,
							Kind: "struct",
						}
						structsMap[parent] = s
					}
					s.Methods = append(s.Methods, archMethod{
						Name:     sym.Name,
						Exported: sym.Exported,
					})
				}
			}
		}

		for _, s := range structsMap {
			ap.Structs = append(ap.Structs, *s)
		}

		for _, sym := range fn.Symbols {
			if isRouteLike(sym) {
				ap.Routes = append(ap.Routes, archRoute{
					File:   fn.Path,
					Symbol: sym.Name,
					Line:   sym.Line,
				})
			}
		}
	}

	result := make([]archPackage, 0, len(packages))
	for _, ap := range packages {
		sort.Slice(ap.Structs, func(i, j int) bool {
			if ap.Structs[i].Exported != ap.Structs[j].Exported {
				return ap.Structs[i].Exported
			}
			return ap.Structs[i].Name < ap.Structs[j].Name
		})
		for si := range ap.Structs {
			sort.Slice(ap.Structs[si].Methods, func(i, j int) bool {
				if ap.Structs[si].Methods[i].Exported != ap.Structs[si].Methods[j].Exported {
					return ap.Structs[si].Methods[i].Exported
				}
				return ap.Structs[si].Methods[i].Name < ap.Structs[si].Methods[j].Name
			})
		}
		sort.Slice(ap.Routes, func(i, j int) bool {
			if ap.Routes[i].File != ap.Routes[j].File {
				return ap.Routes[i].File < ap.Routes[j].File
			}
			return ap.Routes[i].Line < ap.Routes[j].Line
		})
		result = append(result, *ap)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})

	return result
}

func isRouteLike(sym graph.Symbol) bool {
	switch sym.Kind {
	case graph.SymbolFunction, graph.SymbolMethod:
		name := sym.Name
		if strings.Contains(name, "Handler") || strings.Contains(name, "handler") {
			return true
		}
		if strings.Contains(name, "Route") || strings.Contains(name, "route") {
			return true
		}
		if strings.HasPrefix(name, "Serve") || strings.HasPrefix(name, "Handle") {
			return true
		}
	}
	return false
}

func writeTreeItems(sb *strings.Builder, items []treeItem, prefix string) {
	for i, item := range items {
		isLast := i == len(items)-1
		connector := "├── "
		if isLast {
			connector = "└── "
		}
		sb.WriteString(prefix + connector + item.label + "\n")

		if len(item.children) > 0 {
			childPrefix := prefix
			if isLast {
				childPrefix += "    "
			} else {
				childPrefix += "│   "
			}
			writeTreeItems(sb, item.children, childPrefix)
		}
	}
}

func (m *model) renderArch() string {
	if m.graph == nil {
		return "no graph data available — run a build command first"
	}

	packages := buildArchTree(m.graph)

	var b strings.Builder
	b.WriteString("ARCHITECTURE OVERVIEW\n")

	lc := retrieval.GetLynxController()
	lynxAvailable := lc != nil && lc.IsRunning()

	if lynxAvailable {
		b.WriteString("  [graph + lynx]\n")
	} else {
		b.WriteString("  [graph]\n")
	}
	b.WriteString("\n")

	totalStructs := 0
	totalRoutes := 0

	for _, pkg := range packages {
		if len(pkg.Structs) == 0 && len(pkg.Routes) == 0 {
			continue
		}

		fmt.Fprintf(&b, "  %s/\n", pkg.Name)
		totalStructs += len(pkg.Structs)
		totalRoutes += len(pkg.Routes)

		var items []treeItem

		for _, s := range pkg.Structs {
			label := s.Name + " [" + s.Kind + "]"
			if s.Exported {
				label = "[exp] " + label
			}

			var methods []treeItem
			for _, m := range s.Methods {
				mLabel := m.Name + "()"
				if m.Exported {
					mLabel = "[exp] " + mLabel
				}
				methods = append(methods, treeItem{label: mLabel})
			}

			items = append(items, treeItem{label: label, children: methods})
		}

		for _, r := range pkg.Routes {
			label := fmt.Sprintf("%s  (%s:%d)", r.Symbol, r.File, r.Line)
			items = append(items, treeItem{label: label})
		}

		writeTreeItems(&b, items, "  ")
		b.WriteString("\n")
	}

	b.WriteString("---\n")
	fmt.Fprintf(&b, "packages: %d  |  ", len(packages))
	fmt.Fprintf(&b, "types/structs/interfaces: %d  |  ", totalStructs)
	fmt.Fprintf(&b, "route-like symbols: %d\n", totalRoutes)

	return b.String()
}
