package language

import (
	"path/filepath"
	"strings"
	"sync"
)

// Language IDs
const (
	Go         ID = "go"
	Python     ID = "python"
	Rust       ID = "rust"
	TypeScript ID = "typescript"
	TSX        ID = "tsx"
	JavaScript ID = "javascript"
	Java       ID = "java"
	Kotlin     ID = "kotlin"
	CSharp     ID = "csharp"
	CPP        ID = "cpp"
	C          ID = "c"
	Ruby       ID = "ruby"
	PHP        ID = "php"
	Swift      ID = "swift"
	Solidity   ID = "solidity"
	SQL        ID = "sql"
	Scala      ID = "scala"
	Elixir     ID = "elixir"
	Lua        ID = "lua"
	Bash       ID = "bash"
	Zig        ID = "zig"
	Haskell    ID = "haskell"
	R          ID = "r"
	Dart       ID = "dart"
	Protobuf   ID = "protobuf"
	YAML       ID = "yaml"
	TOML       ID = "toml"
	HTML       ID = "html"
	CSS        ID = "css"
)

// Framework IDs
const (
	FwReact     ID = "react"
	FwVue       ID = "vue"
	FwAngular   ID = "angular"
	FwNextJS    ID = "nextjs"
	FwNuxt      ID = "nuxt"
	FwExpress   ID = "express"
	FwNestJS    ID = "nestjs"
	FwSvelte    ID = "svelte"
	FwDjango    ID = "django"
	FwFlask     ID = "flask"
	FwFastAPI   ID = "fastapi"
	FwSpring    ID = "spring"
	FwQuarkus   ID = "quarkus"
	FwMicronaut ID = "micronaut"
	FwGin       ID = "gin"
	FwEcho      ID = "echo"
	FwFiber     ID = "fiber"
	FwAxum      ID = "axum"
	FwActix     ID = "actix"
	FwRocket    ID = "rocket"
	FwLaravel   ID = "laravel"
	FwSymfony   ID = "symfony"
	FwRails     ID = "rails"
	FwSinatra   ID = "sinatra"
)

type Registry struct {
	mu           sync.RWMutex
	defs         map[ID]*Def
	extMap       map[string]ID
	indicatorMap map[string]ID
	all          []*Def
}

var global *Registry
var once sync.Once

func Global() *Registry {
	once.Do(func() {
		global = NewRegistry()
		for _, d := range allDefs {
			global.Register(d)
		}
	})
	return global
}

func NewRegistry() *Registry {
	return &Registry{
		defs:         make(map[ID]*Def),
		extMap:       make(map[string]ID),
		indicatorMap: make(map[string]ID),
	}
}

func (r *Registry) Register(def Def) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := def
	r.defs[d.ID] = &d
	r.all = append(r.all, &d)
	for _, ext := range d.Extensions {
		r.extMap[ext] = d.ID
	}
	for _, ind := range d.IndicatorFiles {
		r.indicatorMap[strings.ToLower(ind)] = d.ID
	}
}

func (r *Registry) Lookup(id ID) (*Def, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.defs[id]
	return d, ok
}

func (r *Registry) FromExtension(ext string) (*Def, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ext = strings.ToLower(ext)
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	id, ok := r.extMap[ext]
	if !ok {
		return nil, false
	}
	d, ok := r.defs[id]
	return d, ok
}

func (r *Registry) FromIndicatorFile(filename string) (*Def, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	base := strings.ToLower(filepath.Base(filename))
	if id, ok := r.indicatorMap[base]; ok {
		d, ok := r.defs[id]
		return d, ok
	}
	return nil, false
}

func (r *Registry) All() []*Def {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*Def, len(r.all))
	copy(result, r.all)
	return result
}

func (r *Registry) LookupByName(name string) (*Def, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	name = strings.ToLower(name)
	for _, d := range r.defs {
		if strings.EqualFold(string(d.ID), name) || strings.EqualFold(d.Name, name) {
			return d, true
		}
	}
	return nil, false
}

// FilterByCategory returns all definitions matching a category.
func (r *Registry) FilterByCategory(cat Category) []*Def {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*Def
	for _, d := range r.defs {
		if d.Category == cat {
			result = append(result, d)
		}
	}
	return result
}
