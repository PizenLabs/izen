package extractors

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/retrieval/symbol"
)

func TestTSExtractor_DetectLanguage(t *testing.T) {
	e := NewTSExtractor()
	dir := t.TempDir()

	_ = os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name": "test"}`), 0644)
	lang, ok := e.DetectLanguage(dir)
	assertTrue(t, ok, "expected TS detection for package.json")
	assertEqual(t, symbol.LangTypeScript, lang)

	_ = os.WriteFile(filepath.Join(dir, "tsconfig.json"), []byte(`{}`), 0644)
	lang, ok = e.DetectLanguage(dir)
	assertTrue(t, ok, "expected TS detection for tsconfig.json")
	assertEqual(t, symbol.LangTypeScript, lang)
}

func TestTSExtractor_ExtractSymbols(t *testing.T) {
	e := NewTSExtractor()
	src := []byte(`export interface User { id: string; name: string; }
export class UserService {
  getUser(): User { return { id: "1", name: "test" }; }
}
export type ID = string;
export enum Role { Admin = "admin", User = "user" }
export function fetchUser(id: string): User { return {} as User; }
export const DEFAULT_ROLE = Role.User;
export default function App() { return null; }
`)
	info, err := e.ExtractSymbols("test.ts", src)
	if err != nil {
		t.Fatalf("ExtractSymbols failed: %v", err)
	}
	assertEqual(t, symbol.LangTypeScript, info.Language)

	classNames := map[string]bool{}
	interfaceNames := map[string]bool{}
	enumNames := map[string]bool{}
	funcNames := map[string]bool{}
	varNames := map[string]bool{}
	for _, sym := range info.Symbols {
		switch sym.Kind {
		case symbol.SymbolClass:
			classNames[sym.Name] = true
		case symbol.SymbolInterface:
			interfaceNames[sym.Name] = true
		case symbol.SymbolEnum:
			enumNames[sym.Name] = true
		case symbol.SymbolFunction:
			funcNames[sym.Name] = true
		case symbol.SymbolConstant:
			varNames[sym.Name] = true
		}
	}

	assertTrue(t, interfaceNames["User"], "User interface")
	assertTrue(t, classNames["UserService"], "UserService class")
	assertTrue(t, classNames["ID"], "ID type alias")
	assertTrue(t, enumNames["Role"], "Role enum")
	assertTrue(t, funcNames["fetchUser"], "fetchUser function")
	assertTrue(t, varNames["DEFAULT_ROLE"], "DEFAULT_ROLE constant")
	assertTrue(t, funcNames["App"], "App default export function")
}

func TestTSExtractor_DetectArchitecturePattern(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "src"), 0755)

	_ = os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name": "nest-app"}`), 0644)
	_ = os.WriteFile(filepath.Join(dir, "src/main.ts"), []byte(`
import { Module, Controller, Injectable } from '@nestjs/common';
@Module({})
export class AppModule {}
`), 0644)

	e := NewTSExtractor()
	packages, err := e.ExtractPackages(dir)
	if err != nil {
		t.Fatalf("ExtractPackages failed: %v", err)
	}

	pattern, err := e.DetectArchitecturePattern(packages)
	if err != nil {
		t.Fatalf("DetectArchitecturePattern failed: %v", err)
	}
	assertEqual(t, "NestJS Layered Architecture", pattern.Name)
	assertEqual(t, "high", pattern.Confidence)
}

func TestTSExtractor_NextJSPattern(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "src/pages"), 0755)

	_ = os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name": "next-app"}`), 0644)
	_ = os.WriteFile(filepath.Join(dir, "src/pages/index.tsx"), []byte(`
import type { NextPage } from 'next';
const Home: NextPage = () => null;
export default Home;
`), 0644)

	e := NewTSExtractor()
	packages, err := e.ExtractPackages(dir)
	if err != nil {
		t.Fatalf("ExtractPackages failed: %v", err)
	}

	pattern, err := e.DetectArchitecturePattern(packages)
	if err != nil {
		t.Fatalf("DetectArchitecturePattern failed: %v", err)
	}
	assertEqual(t, "Next.js Application", pattern.Name)
	assertEqual(t, "medium", pattern.Confidence)
}

func TestJSExtractor_DetectLanguage(t *testing.T) {
	e := NewTSExtractor()
	dir := t.TempDir()

	_ = os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name": "js-app"}`), 0644)
	_ = os.WriteFile(filepath.Join(dir, "index.js"), []byte(`console.log("hello");`), 0644)

	lang, ok := e.DetectLanguage(dir)
	assertTrue(t, ok, "expected JS detection")
	assertEqual(t, symbol.LangTypeScript, lang)
}
