package extractors

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PizenLabs/izen/internal/retrieval/symbol"
)

func TestJavaExtractor_DetectLanguage(t *testing.T) {
	e := NewJavaExtractor()
	dir := t.TempDir()

	_ = os.WriteFile(filepath.Join(dir, "pom.xml"), []byte(`<project></project>`), 0644)
	lang, ok := e.DetectLanguage(dir)
	assertTrue(t, ok, "expected java detection for maven project")
	assertEqual(t, symbol.LangJava, lang)

	_ = os.Remove(filepath.Join(dir, "pom.xml"))
	_ = os.WriteFile(filepath.Join(dir, "build.gradle"), []byte(`plugins { id 'java' }`), 0644)
	lang, ok = e.DetectLanguage(dir)
	assertTrue(t, ok, "expected java detection for gradle project")
	assertEqual(t, symbol.LangJava, lang)

	_ = os.Remove(filepath.Join(dir, "build.gradle"))
	_, ok = e.DetectLanguage(dir)
	assertFalse(t, ok, "expected no detection for empty directory")
}

func TestJavaExtractor_ExtractSymbols(t *testing.T) {
	e := NewJavaExtractor()
	src := []byte(`package com.example.app;

import java.util.List;

@SpringBootApplication
public class Application {

    @RestController
    public class HelloController {

        @GetMapping("/hello")
        public String hello() {
            return "Hello";
        }
    }

    @Service
    public class MyService {
        public String process() {
            return " processed";
        }
    }

    @Repository
    public interface MyRepository {
        String findById(String id);
    }

    @Entity
    public class MyEntity {
        private Long id;
        private String name;
    }
}`)
	info, err := e.ExtractSymbols("test.java", src)
	if err != nil {
		t.Fatalf("ExtractSymbols failed: %v", err)
	}
	assertEqual(t, symbol.LangJava, info.Language)
	assertEqual(t, "com.example.app", info.Package)

	annotationNames := map[string]bool{}
	classNames := map[string]bool{}
	interfaceNames := map[string]bool{}
	for _, sym := range info.Symbols {
		switch sym.Kind {
		case symbol.SymbolAnnotation:
			annotationNames[sym.Name] = true
		case symbol.SymbolClass:
			classNames[sym.Name] = true
		case symbol.SymbolInterface:
			interfaceNames[sym.Name] = true
		}
	}

	assertTrue(t, annotationNames["SpringBootApplication"], "SpringBootApplication annotation")
	assertTrue(t, annotationNames["RestController"], "RestController annotation")
	assertTrue(t, annotationNames["Service"], "Service annotation")
	assertTrue(t, annotationNames["Repository"], "Repository annotation")
	assertTrue(t, annotationNames["Entity"], "Entity annotation")
	assertTrue(t, classNames["Application"], "Application class")
	assertTrue(t, classNames["HelloController"], "HelloController class")
	assertTrue(t, classNames["MyService"], "MyService class")
	assertTrue(t, classNames["MyEntity"], "MyEntity class")
	assertTrue(t, interfaceNames["MyRepository"], "MyRepository interface")
}

func TestJavaExtractor_DetectArchitecturePattern(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "src/main/java/com/example/app"), 0755)

	_ = os.WriteFile(filepath.Join(dir, "src/main/java/com/example/app/Application.java"), []byte(`package com.example.app;
@SpringBootApplication
public class Application {}
`), 0644)
	_ = os.WriteFile(filepath.Join(dir, "src/main/java/com/example/app/HelloController.java"), []byte(`package com.example.app;
@RestController
public class HelloController {}
`), 0644)
	_ = os.WriteFile(filepath.Join(dir, "src/main/java/com/example/app/MyService.java"), []byte(`package com.example.app;
@Service
public class MyService {}
`), 0644)
	_ = os.WriteFile(filepath.Join(dir, "src/main/java/com/example/app/MyRepository.java"), []byte(`package com.example.app;
@Repository
public interface MyRepository {}
`), 0644)
	_ = os.WriteFile(filepath.Join(dir, "src/main/java/com/example/app/MyEntity.java"), []byte(`package com.example.app;
@Entity
public class MyEntity {}
`), 0644)

	e := NewJavaExtractor()
	packages, err := e.ExtractPackages(dir)
	if err != nil {
		t.Fatalf("ExtractPackages failed: %v", err)
	}

	pattern, err := e.DetectArchitecturePattern(packages)
	if err != nil {
		t.Fatalf("DetectArchitecturePattern failed: %v", err)
	}
	assertEqual(t, "Spring Layered Architecture", pattern.Name)
	assertEqual(t, "high", pattern.Confidence)
}

func TestJavaExtractor_PartialSpringPattern(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "src/main/java/com/example/app"), 0755)

	_ = os.WriteFile(filepath.Join(dir, "src/main/java/com/example/app/Application.java"), []byte(`package com.example.app;
@SpringBootApplication
public class Application {}
`), 0644)

	e := NewJavaExtractor()
	packages, err := e.ExtractPackages(dir)
	if err != nil {
		t.Fatalf("ExtractPackages failed: %v", err)
	}

	pattern, err := e.DetectArchitecturePattern(packages)
	if err != nil {
		t.Fatalf("DetectArchitecturePattern failed: %v", err)
	}
	assertEqual(t, "Spring Application", pattern.Name)
	assertEqual(t, "medium", pattern.Confidence)
}

func TestJavaExtractor_NoFramework(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "src/main/java/com/example/simple"), 0755)

	_ = os.WriteFile(filepath.Join(dir, "src/main/java/com/example/simple/Util.java"), []byte(`package com.example.simple;
public class Util {}
`), 0644)

	e := NewJavaExtractor()
	packages, err := e.ExtractPackages(dir)
	if err != nil {
		t.Fatalf("ExtractPackages failed: %v", err)
	}

	pattern, err := e.DetectArchitecturePattern(packages)
	if err != nil {
		t.Fatalf("DetectArchitecturePattern failed: %v", err)
	}
	assertEqual(t, "Java Package Structure", pattern.Name)
	assertEqual(t, "low", pattern.Confidence)
}
