package knowledge

import (
	"reflect"
	"sort"
	"testing"
)

func TestLanguageFor(t *testing.T) {
	cases := map[string]string{
		"main.go":    "go",
		"app.js":     "js",
		"app.tsx":    "ts",
		"app.ts":     "ts",
		"server.py":  "python",
		"lib.rs":     "rust",
		"Main.java":  "java",
		"app.rb":     "ruby",
		"index.php":  "php",
		"main.c":     "c",
		"util.cs":    "csharp",
		"README.md":  "",
		"styles.css": "",
		"index.html": "",
	}
	for name, want := range cases {
		if got := languageFor(name); got != want {
			t.Errorf("languageFor(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestExtractGoSymbols(t *testing.T) {
	content := `package main

import "fmt"

var port = 8080

const name = "demo"

type todo struct {
	ID int
}

func NewTodo() *todo {
	return &todo{}
}

func (t *todo) Render() string {
	return fmt.Sprintf("%d", t.ID)
}

func main() {
	_ = port
	_ = name
}
`
	symbols := ExtractSymbols("go", "main.go", content)
	byName := map[string]Symbol{}
	for _, s := range symbols {
		byName[s.Name] = s
	}

	if s, ok := byName["NewTodo"]; !ok || s.Kind != SymbolFunc {
		t.Errorf("NewTodo = %+v, want top-level func", byName["NewTodo"])
	}
	if s, ok := byName["Render"]; !ok || s.Kind != SymbolMethod {
		t.Errorf("Render = %+v, want method", byName["Render"])
	}
	if s, ok := byName["todo"]; !ok || s.Kind != SymbolType {
		t.Errorf("todo = %+v, want type", byName["todo"])
	}
	if s, ok := byName["port"]; !ok || s.Kind != SymbolVar {
		t.Errorf("port = %+v, want var", byName["port"])
	}
	if s, ok := byName["name"]; !ok || s.Kind != SymbolConst {
		t.Errorf("name = %+v, want const", byName["name"])
	}
	if _, ok := byName["main"]; !ok {
		t.Errorf("main not extracted: %+v", symbols)
	}
}

func TestExtractJSTypescriptSymbols(t *testing.T) {
	content := `import React from "react";

export function add(a, b) { return a + b; }

const total = 3;

let running = false;

export class Counter {
  increment() {}
}

export interface Props {
  count: number;
}

export type State = { done: boolean };
`
	symbols := ExtractSymbols("ts", "app.ts", content)
	byName := map[string]Symbol{}
	for _, s := range symbols {
		byName[s.Name] = s
	}
	if s, ok := byName["add"]; !ok || s.Kind != SymbolFunc {
		t.Errorf("add = %+v, want func", byName["add"])
	}
	if s, ok := byName["total"]; !ok || s.Kind != SymbolConst {
		t.Errorf("total = %+v, want const", byName["total"])
	}
	if s, ok := byName["running"]; !ok || s.Kind != SymbolConst {
		t.Errorf("running = %+v, want const (let classified as const)", byName["running"])
	}
	if s, ok := byName["Counter"]; !ok || s.Kind != SymbolClass {
		t.Errorf("Counter = %+v, want class", byName["Counter"])
	}
	if s, ok := byName["Props"]; !ok || s.Kind != SymbolInterface {
		t.Errorf("Props = %+v, want interface", byName["Props"])
	}
	if s, ok := byName["State"]; !ok || s.Kind != SymbolType {
		t.Errorf("State = %+v, want type", byName["State"])
	}
}

func TestExtractPythonRustSymbols(t *testing.T) {
	py := `import os

def greet(name: str) -> str:
    return "hi " + name

class Server:
    def start(self):
        pass
`
	pySymbols := ExtractSymbols("python", "server.py", py)
	pyByName := map[string]Symbol{}
	for _, s := range pySymbols {
		pyByName[s.Name] = s
	}
	if s, ok := pyByName["greet"]; !ok || s.Kind != SymbolFunc {
		t.Errorf("greet = %+v, want python func", pyByName["greet"])
	}
	if s, ok := pyByName["Server"]; !ok || s.Kind != SymbolClass {
		t.Errorf("Server = %+v, want python class", pyByName["Server"])
	}

	rs := `pub struct Config {
    port: u16,
}

pub enum Error {
    NotFound,
}

fn main() {}

trait Runnable {
    fn run(&self);
}
`
	rsSymbols := ExtractSymbols("rust", "lib.rs", rs)
	rsByName := map[string]Symbol{}
	for _, s := range rsSymbols {
		rsByName[s.Name] = s
	}
	if s, ok := rsByName["Config"]; !ok || s.Kind != SymbolStruct {
		t.Errorf("Config = %+v, want struct", rsByName["Config"])
	}
	if s, ok := rsByName["Error"]; !ok || s.Kind != SymbolEnum {
		t.Errorf("Error = %+v, want enum", rsByName["Error"])
	}
	if s, ok := rsByName["main"]; !ok || s.Kind != SymbolFunc {
		t.Errorf("main = %+v, want func", rsByName["main"])
	}
	if s, ok := rsByName["Runnable"]; !ok || s.Kind != SymbolTrait {
		t.Errorf("Runnable = %+v, want trait", rsByName["Runnable"])
	}
}

func TestExtractSkipsCommentsAndBlank(t *testing.T) {
	content := "// func fake(not a decl)\n# def not_a_def\n\nfunc real() {}"
	symbols := ExtractSymbols("go", "main.go", content)
	if len(symbols) != 1 || symbols[0].Name != "real" {
		t.Errorf("symbols = %+v, want just real", symbols)
	}
}

func TestExtractLineNumbers(t *testing.T) {
	content := "package main\n\nfunc one() {}\nfunc two() {}"
	symbols := ExtractSymbols("go", "main.go", content)
	if len(symbols) != 2 {
		t.Fatalf("symbols = %d, want 2", len(symbols))
	}
	if symbols[0].Line != 3 || symbols[1].Line != 4 {
		t.Errorf("line numbers = %d,%d, want 3,4", symbols[0].Line, symbols[1].Line)
	}
}

func TestExtractUnknownLang(t *testing.T) {
	if got := ExtractSymbols("", "readme.md", "function fake() {}"); len(got) != 0 {
		t.Errorf("unknown language extracted symbols: %+v", got)
	}
	if got := ExtractSymbols("go", "main.go", ""); len(got) != 0 {
		t.Errorf("empty content extracted symbols: %+v", got)
	}
}

func TestSymbolTableAddLookupDedup(t *testing.T) {
	tbl := NewSymbolTable()
	tbl.Add(Symbol{Name: "Foo", Kind: SymbolFunc, File: "a.go", Line: 1})
	tbl.Add(Symbol{Name: "Foo", Kind: SymbolFunc, File: "b.go", Line: 5})
	tbl.Add(Symbol{Name: "Foo", Kind: SymbolFunc, File: "a.go", Line: 1}) // duplicate

	if tbl.Count() != 2 {
		t.Errorf("Count = %d, want 2 (duplicate dropped)", tbl.Count())
	}
	hits := tbl.Lookup("Foo")
	if len(hits) != 2 {
		t.Errorf("Lookup(Foo) = %d hits, want 2", len(hits))
	}
	if hits[0].File != "a.go" || hits[1].File != "b.go" {
		t.Errorf("Lookup order = %+v, want insertion order", hits)
	}
	if len(tbl.Lookup("Missing")) != 0 {
		t.Error("missing symbol resolved")
	}

	names := tbl.Names()
	if !reflect.DeepEqual(names, []string{"Foo"}) {
		t.Errorf("Names = %v, want [Foo]", names)
	}
	all := tbl.All()
	if len(all) != 2 {
		t.Errorf("All = %d, want 2", len(all))
	}
	// Mutation of a returned slice must not corrupt the table.
	all[0].Name = "tampered"
	if tbl.Lookup("Foo")[0].Name == "tampered" {
		t.Error("All() aliased table storage")
	}
}

func TestSymbolTableAddMany(t *testing.T) {
	tbl := NewSymbolTable()
	tbl.AddMany([]Symbol{
		{Name: "A", Kind: SymbolFunc, File: "a.go", Line: 1},
		{Name: "B", Kind: SymbolType, File: "b.go", Line: 2},
	})
	if tbl.Count() != 2 {
		t.Errorf("Count = %d, want 2", tbl.Count())
	}
	sort.Strings(tbl.Names())
}
