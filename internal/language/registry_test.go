package language

import (
	"testing"
)

func TestGlobalRegistryHasLanguages(t *testing.T) {
	reg := Global()
	all := reg.All()
	if len(all) == 0 {
		t.Fatal("expected at least one language in global registry")
	}
}

func TestLookupByName(t *testing.T) {
	reg := Global()
	def, ok := reg.LookupByName("go")
	if !ok {
		t.Fatal("expected to find Go")
	}
	if def.ID != Go {
		t.Fatalf("expected ID 'go', got %q", def.ID)
	}
	if !def.HasTreeSitter {
		t.Fatal("expected Go to have tree-sitter grammar")
	}
}

func TestFromExtension(t *testing.T) {
	reg := Global()
	tests := []struct {
		ext string
		id  ID
	}{
		{".go", Go},
		{".py", Python},
		{".rs", Rust},
		{".ts", TypeScript},
		{".tsx", TSX},
		{".js", JavaScript},
		{".java", Java},
		{".kt", Kotlin},
		{".cs", CSharp},
		{".rb", Ruby},
		{".php", PHP},
		{".swift", Swift},
		{".cpp", CPP},
		{".c", C},
		{".sql", SQL},
		{".scala", Scala},
		{".ex", Elixir},
		{".lua", Lua},
		{".sh", Bash},
		{".proto", Protobuf},
		{".yaml", YAML},
		{".toml", TOML},
		{".html", HTML},
		{".css", CSS},
	}
	for _, tt := range tests {
		def, ok := reg.FromExtension(tt.ext)
		if !ok {
			t.Errorf("FromExtension(%q) not found", tt.ext)
			continue
		}
		if def.ID != tt.id {
			t.Errorf("FromExtension(%q) = %q, want %q", tt.ext, def.ID, tt.id)
		}
	}
}

func TestFromIndicatorFile(t *testing.T) {
	reg := Global()
	tests := []struct {
		file string
		id   ID
	}{
		{"go.mod", Go},
		{"Cargo.toml", Rust},
		{"package.json", JavaScript},
		{"pom.xml", Java},
		{"Gemfile", Ruby},
		{"composer.json", PHP},
		{"requirements.txt", Python},
		{"CMakeLists.txt", CPP},
		{"build.gradle", Java},
	}
	for _, tt := range tests {
		def, ok := reg.FromIndicatorFile(tt.file)
		if !ok {
			t.Errorf("FromIndicatorFile(%q) not found", tt.file)
			continue
		}
		if def.ID != tt.id {
			t.Errorf("FromIndicatorFile(%q) = %q, want %q", tt.file, def.ID, tt.id)
		}
	}
}

func TestLookupByNameCaseInsensitive(t *testing.T) {
	reg := Global()
	def, ok := reg.LookupByName("PYTHON")
	if !ok {
		t.Fatal("expected to find python by uppercase name")
	}
	if def.ID != Python {
		t.Fatalf("expected Python, got %q", def.ID)
	}
}

func TestLookupReturnsNilForMissing(t *testing.T) {
	reg := Global()
	_, ok := reg.Lookup("nonexistent")
	if ok {
		t.Fatal("expected false for nonexistent language")
	}
}

func TestAllReturnsCopy(t *testing.T) {
	reg := Global()
	all1 := reg.All()
	all2 := reg.All()
	if len(all1) != len(all2) {
		t.Fatal("expected same length")
	}
	all1[0] = &Def{ID: "test"}
	if len(all1) == len(all2) && all2[0].ID == "test" {
		t.Fatal("All() should return a copy")
	}
}

func TestFilterByCategoryLanguages(t *testing.T) {
	reg := Global()
	langs := reg.FilterByCategory(CategoryLanguage)
	if len(langs) == 0 {
		t.Fatal("expected at least one language category")
	}
}
