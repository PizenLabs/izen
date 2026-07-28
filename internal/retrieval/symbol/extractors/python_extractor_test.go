package extractors

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/retrieval/symbol"
)

func TestPythonExtractor_DetectLanguage(t *testing.T) {
	e := NewPythonExtractor()
	dir := t.TempDir()

	_ = os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(`[project]`), 0644)
	lang, ok := e.DetectLanguage(dir)
	assertTrue(t, ok, "expected python detection for pyproject.toml")
	assertEqual(t, symbol.LangPython, lang)

	_ = os.Remove(filepath.Join(dir, "pyproject.toml"))
	_ = os.WriteFile(filepath.Join(dir, "setup.py"), []byte(`from setuptools import setup`), 0644)
	lang, ok = e.DetectLanguage(dir)
	assertTrue(t, ok, "expected python detection for setup.py")
	assertEqual(t, symbol.LangPython, lang)

	_ = os.Remove(filepath.Join(dir, "setup.py"))
	_ = os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte(`requests`), 0644)
	lang, ok = e.DetectLanguage(dir)
	assertTrue(t, ok, "expected python detection for requirements.txt")
	assertEqual(t, symbol.LangPython, lang)

	_ = os.Remove(filepath.Join(dir, "requirements.txt"))
	_, ok = e.DetectLanguage(dir)
	assertFalse(t, ok, "expected no detection for empty directory")
}

func TestPythonExtractor_ExtractSymbols(t *testing.T) {
	e := NewPythonExtractor()
	src := []byte(`from typing import List
import requests

@dataclass
class User:
    id: int
    name: str

class UserService:
    def get_user(self, id: int) -> User:
        return User(id=id, name="test")

async def fetch_users() -> List[User]:
    return []

def main():
    pass

def cli():
    pass
`)
	info, err := e.ExtractSymbols("test.py", src)
	if err != nil {
		t.Fatalf("ExtractSymbols failed: %v", err)
	}
	assertEqual(t, symbol.LangPython, info.Language)

	classNames := map[string]bool{}
	funcNames := map[string]bool{}
	annotationNames := map[string]bool{}
	for _, sym := range info.Symbols {
		switch sym.Kind {
		case symbol.SymbolClass:
			classNames[sym.Name] = true
		case symbol.SymbolFunction:
			funcNames[sym.Name] = true
		case symbol.SymbolAnnotation:
			annotationNames[sym.Name] = true
		}
	}

	assertTrue(t, classNames["User"], "User class")
	assertTrue(t, classNames["UserService"], "UserService class")
	assertTrue(t, funcNames["get_user"], "get_user method")
	assertTrue(t, funcNames["fetch_users"], "fetch_users async function")
	assertTrue(t, funcNames["main"], "main function")
	assertTrue(t, funcNames["cli"], "cli function")
	assertTrue(t, annotationNames["dataclass"], "dataclass annotation")
}

func TestPythonExtractor_ExtractPackages(t *testing.T) {
	e := NewPythonExtractor()
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "mypkg"), 0755)
	_ = os.WriteFile(filepath.Join(dir, "mypkg", "__init__.py"), []byte("# mypkg"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "mypkg", "models.py"), []byte("class Foo:\n    pass\n"), 0644)

	packages, err := e.ExtractPackages(dir)
	if err != nil {
		t.Fatalf("ExtractPackages failed: %v", err)
	}
	assertTrue(t, len(packages) >= 1, "expected at least one package")
}

func TestPythonExtractor_DetectArchitecturePattern(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "app"), 0755)

	_ = os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(`[project]`), 0644)
	_ = os.WriteFile(filepath.Join(dir, "app/main.py"), []byte(`
from fastapi import FastAPI
app = FastAPI()
`), 0644)

	e := NewPythonExtractor()
	packages, err := e.ExtractPackages(dir)
	if err != nil {
		t.Fatalf("ExtractPackages failed: %v", err)
	}

	pattern, err := e.DetectArchitecturePattern(packages)
	if err != nil {
		t.Fatalf("DetectArchitecturePattern failed: %v", err)
	}
	assertEqual(t, "FastAPI Application", pattern.Name)
	assertEqual(t, "high", pattern.Confidence)
}

func TestPythonExtractor_DjangoPattern(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "myproject"), 0755)

	_ = os.WriteFile(filepath.Join(dir, "setup.py"), []byte(`from setuptools import setup`), 0644)
	_ = os.WriteFile(filepath.Join(dir, "myproject/models.py"), []byte(`
from django.db import models
class User(models.Model):
    pass
`), 0644)

	e := NewPythonExtractor()
	packages, err := e.ExtractPackages(dir)
	if err != nil {
		t.Fatalf("ExtractPackages failed: %v", err)
	}

	pattern, err := e.DetectArchitecturePattern(packages)
	if err != nil {
		t.Fatalf("DetectArchitecturePattern failed: %v", err)
	}
	assertEqual(t, "Django Application", pattern.Name)
	assertEqual(t, "high", pattern.Confidence)
}
