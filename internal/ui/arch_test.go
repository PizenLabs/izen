package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/graph"
	"github.com/PizenLabs/izen/internal/retrieval/symbol"
)

func TestGraphFromPolyglot_JavaProject(t *testing.T) {
	syms := []symbol.FileASTInfo{
		{
			FilePath: "src/main/java/com/example/App.java",
			Language: symbol.LangJava,
			Package:  "com.example",
			Symbols: []symbol.SymbolNode{
				{
					Name:      "App",
					Kind:      symbol.SymbolClass,
					FilePath:  "src/main/java/com/example/App.java",
					StartLine: 1,
					EndLine:   10,
					Exported:  true,
				},
				{
					Name:      "main",
					Kind:      symbol.SymbolMethod,
					FilePath:  "src/main/java/com/example/App.java",
					StartLine: 5,
					EndLine:   8,
					Parent:    "App",
					Exported:  true,
				},
			},
			Imports: []symbol.DependencyEdge{
				{ImportPath: "java.util.List"},
			},
		},
	}

	g := graphFromPolyglot(syms)
	if g == nil {
		t.Fatal("expected non-nil graph")
	}
	if len(g.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(g.Files))
	}

	fn := g.Files[0]
	if fn.Path != "src/main/java/com/example/App.java" {
		t.Errorf("expected path src/main/java/com/example/App.java, got %s", fn.Path)
	}
	if fn.Language != graph.LangJava {
		t.Errorf("expected language java, got %s", fn.Language)
	}
	if len(fn.Symbols) != 2 {
		t.Fatalf("expected 2 symbols, got %d", len(fn.Symbols))
	}
	if fn.Package != "com.example" {
		t.Errorf("expected package com.example, got %s", fn.Package)
	}
	if len(fn.Imports) != 1 {
		t.Fatalf("expected 1 import, got %d", len(fn.Imports))
	}
}

func TestGraphFromPolyglot_TSProject(t *testing.T) {
	syms := []symbol.FileASTInfo{
		{
			FilePath: "src/index.ts",
			Language: symbol.LangTypeScript,
			Package:  "",
			Symbols: []symbol.SymbolNode{
				{
					Name:      "App",
					Kind:      symbol.SymbolClass,
					FilePath:  "src/index.ts",
					StartLine: 1,
					EndLine:   20,
					Exported:  true,
				},
				{
					Name:      "render",
					Kind:      symbol.SymbolFunction,
					FilePath:  "src/index.ts",
					StartLine: 3,
					EndLine:   7,
					Exported:  true,
				},
			},
			Imports: []symbol.DependencyEdge{
				{ImportPath: "react"},
			},
		},
	}

	g := graphFromPolyglot(syms)
	if g == nil {
		t.Fatal("expected non-nil graph")
	}
	if len(g.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(g.Files))
	}

	fn := g.Files[0]
	if fn.Language != graph.LangTypeScript {
		t.Errorf("expected language typescript, got %s", fn.Language)
	}
	if len(fn.Symbols) != 2 {
		t.Fatalf("expected 2 symbols, got %d", len(fn.Symbols))
	}
}

func TestGraphFromPolyglot_PythonProject(t *testing.T) {
	syms := []symbol.FileASTInfo{
		{
			FilePath: "app.py",
			Language: symbol.LangPython,
			Package:  "",
			Symbols: []symbol.SymbolNode{
				{
					Name:      "main",
					Kind:      symbol.SymbolFunction,
					FilePath:  "app.py",
					StartLine: 1,
					EndLine:   5,
					Exported:  true,
				},
			},
		},
	}

	g := graphFromPolyglot(syms)
	if g == nil {
		t.Fatal("expected non-nil graph")
	}
	if len(g.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(g.Files))
	}

	fn := g.Files[0]
	if fn.Language != graph.LangPython {
		t.Errorf("expected language python, got %s", fn.Language)
	}
	if len(fn.Symbols) != 1 {
		t.Fatalf("expected 1 symbol, got %d", len(fn.Symbols))
	}
}

func TestGraphFromPolyglot_Empty(t *testing.T) {
	g := graphFromPolyglot(nil)
	if g == nil {
		t.Fatal("expected non-nil graph")
	}
	if len(g.Files) != 0 {
		t.Fatalf("expected 0 files, got %d", len(g.Files))
	}
}

func TestGraphFromPolyglot_UnknownKind(t *testing.T) {
	syms := []symbol.FileASTInfo{
		{
			FilePath: "test.txt",
			Language: symbol.LangGo,
			Symbols: []symbol.SymbolNode{
				{
					Name:      "unknown",
					Kind:      "unknown_kind",
					FilePath:  "test.txt",
					StartLine: 1,
					EndLine:   1,
				},
			},
		},
	}

	g := graphFromPolyglot(syms)
	if g == nil {
		t.Fatal("expected non-nil graph")
	}
	if len(g.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(g.Files))
	}
	if len(g.Files[0].Symbols) != 0 {
		t.Errorf("expected 0 symbols (unknown kind skipped), got %d", len(g.Files[0].Symbols))
	}
}

func TestSymbolKindToGraphKind(t *testing.T) {
	tests := []struct {
		name     string
		sk       symbol.SymbolKind
		expected int
	}{
		{"Function", symbol.SymbolFunction, int(graph.SymbolFunction)},
		{"Method", symbol.SymbolMethod, int(graph.SymbolMethod)},
		{"Struct", symbol.SymbolStruct, int(graph.SymbolStruct)},
		{"Interface", symbol.SymbolInterface, int(graph.SymbolInterface)},
		{"Class", symbol.SymbolClass, int(graph.SymbolClass)},
		{"Variable", symbol.SymbolVariable, int(graph.SymbolVariable)},
		{"Constant", symbol.SymbolConstant, int(graph.SymbolConstant)},
		{"Enum", symbol.SymbolEnum, int(graph.SymbolEnum)},
		{"Type", symbol.SymbolType, int(graph.SymbolType)},
		{"Package", symbol.SymbolPackage, int(graph.SymbolPackage)},
		{"Module", symbol.SymbolModule, int(graph.SymbolImport)},
		{"Unknown", "nonexistent", -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := symbolKindToGraphKind(tt.sk)
			if got != tt.expected {
				t.Errorf("symbolKindToGraphKind(%q) = %d, want %d", tt.sk, got, tt.expected)
			}
		})
	}
}

func TestDetectGraphLanguageForGraph_ExplicitLang(t *testing.T) {
	g := graph.NewGraph("/tmp")
	g.AddFile(graph.FileNode{
		Path:     "src/Main.java",
		Language: graph.LangJava,
	})

	lang := detectGraphLanguageForGraph(g, &model{workspaceRoot: "/tmp"})
	if lang != "java" {
		t.Errorf("expected java, got %s", lang)
	}
}

func TestDetectGraphLanguageForGraph_FallbackToGo(t *testing.T) {
	g := graph.NewGraph("/tmp")

	lang := detectGraphLanguageForGraph(g, &model{workspaceRoot: "/tmp"})
	if lang != "go" {
		t.Errorf("expected go fallback, got %s", lang)
	}
}

func TestDetectGraphLanguageForGraph_NilGraph(t *testing.T) {
	lang := detectGraphLanguageForGraph(nil, &model{workspaceRoot: "/tmp"})
	if lang != "go" {
		t.Errorf("expected go fallback for nil graph, got %s", lang)
	}
}

func TestBuildArchGraph_NonGoProject(t *testing.T) {
	dir := t.TempDir()

	_ = os.MkdirAll(filepath.Join(dir, "src", "main", "java", "com", "example"), 0755)
	err := os.WriteFile(
		filepath.Join(dir, "src", "main", "java", "com", "example", "App.java"),
		[]byte("package com.example;\npublic class App {\n  public static void main(String[] args) {}\n}\n"),
		0644,
	)
	if err != nil {
		t.Fatal(err)
	}

	srcSym := symbol.SymbolNode{
		Name:      "App",
		Kind:      symbol.SymbolClass,
		FilePath:  "src/main/java/com/example/App.java",
		StartLine: 2,
		EndLine:   4,
	}
	srcAST := symbol.FileASTInfo{
		FilePath: "src/main/java/com/example/App.java",
		Language: symbol.LangJava,
		Package:  "com.example",
		Symbols:  []symbol.SymbolNode{srcSym},
	}

	g := graphFromPolyglot([]symbol.FileASTInfo{srcAST})
	if g == nil {
		t.Fatal("expected non-nil graph")
	}
	if len(g.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(g.Files))
	}
	if len(g.Files[0].Symbols) != 1 {
		t.Fatalf("expected 1 symbol, got %d", len(g.Files[0].Symbols))
	}
}

func TestBuildArchGraph_TsProject(t *testing.T) {
	dir := t.TempDir()

	_ = os.MkdirAll(filepath.Join(dir, "src"), 0755)
	err := os.WriteFile(
		filepath.Join(dir, "src", "index.ts"),
		[]byte("export function render(): void {}\n"),
		0644,
	)
	if err != nil {
		t.Fatal(err)
	}

	srcAST := symbol.FileASTInfo{
		FilePath: "src/index.ts",
		Language: symbol.LangTypeScript,
		Symbols: []symbol.SymbolNode{
			{
				Name:      "render",
				Kind:      symbol.SymbolFunction,
				FilePath:  "src/index.ts",
				StartLine: 1,
				EndLine:   1,
			},
		},
	}

	g := graphFromPolyglot([]symbol.FileASTInfo{srcAST})
	if g == nil {
		t.Fatal("expected non-nil graph from polyglot")
	}
	if len(g.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(g.Files))
	}
	if g.Files[0].Language != graph.LangTypeScript {
		t.Errorf("expected typescript, got %s", g.Files[0].Language)
	}
}

func TestSymbolKindToGraphKind_UnknownKind(t *testing.T) {
	result := symbolKindToGraphKind("nonexistent_kind")
	if result != -1 {
		t.Errorf("expected -1 for unknown kind, got %d", result)
	}
}

func TestGraphFromPolyglot_MultipleFiles(t *testing.T) {
	syms := []symbol.FileASTInfo{
		{
			FilePath: "src/main/App.java",
			Language: symbol.LangJava,
			Symbols: []symbol.SymbolNode{
				{Name: "App", Kind: symbol.SymbolClass, FilePath: "src/main/App.java", StartLine: 1, EndLine: 10},
			},
		},
		{
			FilePath: "src/main/Utils.ts",
			Language: symbol.LangTypeScript,
			Symbols: []symbol.SymbolNode{
				{Name: "helper", Kind: symbol.SymbolFunction, FilePath: "src/main/Utils.ts", StartLine: 1, EndLine: 5},
			},
		},
	}

	g := graphFromPolyglot(syms)
	if g == nil {
		t.Fatal("expected non-nil graph")
	}
	if len(g.Files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(g.Files))
	}
}

func TestGraphFromPolyglot_Imports(t *testing.T) {
	syms := []symbol.FileASTInfo{
		{
			FilePath: "src/main/App.java",
			Language: symbol.LangJava,
			Symbols: []symbol.SymbolNode{
				{Name: "App", Kind: symbol.SymbolClass, FilePath: "src/main/App.java", StartLine: 1, EndLine: 10},
			},
			Imports: []symbol.DependencyEdge{
				{ImportPath: "java.util.List"},
				{ImportPath: "java.util.Map"},
			},
		},
	}

	g := graphFromPolyglot(syms)
	if g == nil {
		t.Fatal("expected non-nil graph")
	}
	fn := g.Files[0]
	if len(fn.Imports) != 2 {
		t.Fatalf("expected 2 imports, got %d", len(fn.Imports))
	}
}

func TestRenderArch_NonGoGraph(t *testing.T) {
	g := graph.NewGraph("/tmp")
	g.AddFile(graph.FileNode{
		Path:     "src/main/java/com/example/App.java",
		Language: graph.LangJava,
		Package:  "com.example",
		Symbols: []graph.Symbol{
			{Name: "App", Kind: graph.SymbolClass, File: "src/main/java/com/example/App.java", Line: 1, Exported: true},
			{Name: "main", Kind: graph.SymbolMethod, File: "src/main/java/com/example/App.java", Line: 3, Parent: "App", Exported: true},
		},
		Imports: []string{"java.util.List"},
	})

	m := &model{graph: g, workspaceRoot: "/tmp"}
	result := m.renderArch("")

	if result == "" {
		t.Fatal("expected non-empty architecture report")
	}
	if len(result) < 20 {
		t.Errorf("expected meaningful report, got: %q", result)
	}
}

func TestRenderArch_EmptyNonGoGraph(t *testing.T) {
	g := graph.NewGraph("/tmp")

	m := &model{graph: g, workspaceRoot: "/tmp"}
	result := m.renderArch("")

	if result != "no packages found in graph" {
		t.Errorf("expected 'no packages found in graph', got: %q", result)
	}
}

func TestRenderArch_CollapsedMode(t *testing.T) {
	g := graph.NewGraph("/tmp")
	g.AddFile(graph.FileNode{
		Path:     "internal/ui/workspace.go",
		Language: graph.LangGo,
		Package:  "ui",
		Symbols: []graph.Symbol{
			{Name: "Workspace", Kind: graph.SymbolStruct, File: "internal/ui/workspace.go", Line: 1, Exported: true},
			{Name: "render", Kind: graph.SymbolMethod, File: "internal/ui/workspace.go", Line: 10, Parent: "Workspace", Exported: true},
		},
		Imports: []string{"github.com/charmbracelet/bubbletea"},
	})
	g.AddFile(graph.FileNode{
		Path:     "internal/engine/workflow.go",
		Language: graph.LangGo,
		Package:  "engine",
		Symbols: []graph.Symbol{
			{Name: "Workflow", Kind: graph.SymbolStruct, File: "internal/engine/workflow.go", Line: 1, Exported: true},
			{Name: "Run", Kind: graph.SymbolMethod, File: "internal/engine/workflow.go", Line: 5, Parent: "Workflow", Exported: true},
		},
	})

	m := &model{graph: g, workspaceRoot: "/tmp"}
	result := m.renderArch("")

	if result == "" {
		t.Fatal("expected non-empty collapsed report")
	}
	if !strings.Contains(result, "LAYERS") {
		t.Errorf("expected collapsed view to contain LAYERS section, got: %q", result)
	}
	if !strings.Contains(result, "/arch --all") {
		t.Errorf("expected collapsed view to contain usage hint for --all, got: %q", result)
	}
	if len(result) > 5000 {
		t.Errorf("collapsed view should be compact, got %d bytes", len(result))
	}
}

func TestRenderArch_FullMode(t *testing.T) {
	g := graph.NewGraph("/tmp")
	g.AddFile(graph.FileNode{
		Path:     "internal/ui/workspace.go",
		Language: graph.LangGo,
		Package:  "ui",
		Symbols: []graph.Symbol{
			{Name: "Workspace", Kind: graph.SymbolStruct, File: "internal/ui/workspace.go", Line: 1, Exported: true},
		},
	})

	m := &model{graph: g, workspaceRoot: "/tmp"}
	result := m.renderArch("--all")

	if result == "" {
		t.Fatal("expected non-empty full report")
	}
	if !strings.Contains(result, "PACKAGE MAP") {
		t.Errorf("expected full view to contain PACKAGE MAP section, got: %q", result)
	}
}

func TestRenderArch_SumAlias(t *testing.T) {
	g := graph.NewGraph("/tmp")
	g.AddFile(graph.FileNode{
		Path:     "internal/ui/workspace.go",
		Language: graph.LangGo,
		Package:  "ui",
		Symbols: []graph.Symbol{
			{Name: "Workspace", Kind: graph.SymbolStruct, File: "internal/ui/workspace.go", Line: 1, Exported: true},
		},
	})

	m := &model{graph: g, workspaceRoot: "/tmp"}
	result := m.renderArch("--sum")

	if result == "" {
		t.Fatal("expected non-empty report for --sum alias")
	}
}

func TestRenderArch_DrilldownLayer(t *testing.T) {
	g := graph.NewGraph("/tmp")
	g.AddFile(graph.FileNode{
		Path:     "internal/ui/workspace.go",
		Language: graph.LangGo,
		Package:  "ui",
		Symbols: []graph.Symbol{
			{Name: "Workspace", Kind: graph.SymbolStruct, File: "internal/ui/workspace.go", Line: 1, Exported: true},
			{Name: "Render", Kind: graph.SymbolMethod, File: "internal/ui/workspace.go", Line: 10, Parent: "Workspace", Exported: true},
		},
	})
	g.AddFile(graph.FileNode{
		Path:     "internal/core/artifact/store.go",
		Language: graph.LangGo,
		Package:  "artifact",
		Symbols: []graph.Symbol{
			{Name: "Store", Kind: graph.SymbolStruct, File: "internal/core/artifact/store.go", Line: 1, Exported: true},
		},
	})

	m := &model{graph: g, workspaceRoot: "/tmp"}

	// Match case-insensitively
	result := m.renderArch("Presentation")
	if result == "" {
		t.Fatal("expected non-empty drill-down result")
	}
	if strings.Contains(result, "no packages found") {
		t.Errorf("drill-down should find results for layer 'Presentation', got empty or no-packages message")
	}

	// Match package name
	result2 := m.renderArch("ui")
	if result2 == "" {
		t.Fatal("expected non-empty drill-down result for package 'ui'")
	}
	if strings.Contains(result2, "ui") {
		// Package name should appear in output
	} else {
		t.Errorf("drill-down for package 'ui' should contain the package name")
	}
}

func TestRenderArch_DrilldownNotFound(t *testing.T) {
	g := graph.NewGraph("/tmp")
	g.AddFile(graph.FileNode{
		Path:     "internal/ui/workspace.go",
		Language: graph.LangGo,
		Package:  "ui",
		Symbols: []graph.Symbol{
			{Name: "Workspace", Kind: graph.SymbolStruct, File: "internal/ui/workspace.go", Line: 1, Exported: true},
		},
	})

	m := &model{graph: g, workspaceRoot: "/tmp"}
	result := m.renderArch("NonExistentLayer")

	if result == "" {
		t.Fatal("expected non-empty result")
	}
	if !strings.Contains(result, "No matching layer or package found") {
		t.Errorf("expected 'No matching' message for unknown target, got: %q", result)
	}
}

func TestSanitizeInputBuffer_OrphanedMouseSequence(t *testing.T) {
	// [<64;83;30M is an orphaned DEC private-mode mouse sequence
	// that leaks into the input buffer when the terminal's leading
	// ESC byte is stripped during scrolling. The sanitizer must remove it.
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "orphaned mouse sequence without ESC",
			input:    "hello[<64;83;30Mworld",
			expected: "helloworld",
		},
		{
			name:     "orphaned mouse sequence at start",
			input:    "[<0;26;37Mprompt>",
			expected: "prompt>",
		},
		{
			name:     "valid ANSI escape with ESC is stripped",
			input:    "\x1b[?2004h\x1b[?2004l text",
			expected: " text",
		},
		{
			name:     "clean text passes through",
			input:    "just typing normally",
			expected: "just typing normally",
		},
		{
			name:     "mixed orphaned and valid sequences",
			input:    "\x1b[?2004htext[<64;83;30Mnormal",
			expected: "textnormal",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeInputBuffer(tt.input)
			if result != tt.expected {
				t.Errorf("sanitizeInputBuffer(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestInputBufferNoLeakage verifies that the sanitizeInputBuffer function
// strips the specific mouse event sequence reported as leaking: [<64;83;30M
func TestInputBufferNoLeakage(t *testing.T) {
	// This is the exact sequence reported as leaking into the prompt input
	// during mouse wheel scrolling: [<64;83;30M
	leaked := "some text[<64;83;30Mmore text"
	clean := sanitizeInputBuffer(leaked)
	if strings.Contains(clean, "[<64;83;30M") {
		t.Errorf("sanitizeInputBuffer should strip orphaned mouse sequence, got: %q", clean)
	}
	if clean != "some textmore text" {
		t.Errorf("sanitizeInputBuffer should remove orphaned mouse sequence entirely, got: %q", clean)
	}
}
