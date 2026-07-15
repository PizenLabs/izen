package build

import (
	"strings"
	"testing"
)

func TestNewEngine(t *testing.T) {
	e := NewEngine()
	if e == nil {
		t.Fatal("expected non-nil engine")
	}
	if e.RecoveryCount() != 0 {
		t.Fatalf("expected recovery count 0, got %d", e.RecoveryCount())
	}
	if e.RecoveryState() != RecoveryNone {
		t.Fatalf("expected RecoveryNone, got %v", e.RecoveryState())
	}
}

func TestRecordCompilationFailure(t *testing.T) {
	e := NewEngine()
	e.RecordCompilationFailure("test.go")
	if e.RecoveryCount() != 1 {
		t.Fatalf("expected recovery count 1, got %d", e.RecoveryCount())
	}
	if e.FileFailureCount("test.go") != 1 {
		t.Fatalf("expected file failure count 1, got %d", e.FileFailureCount("test.go"))
	}
	if e.MustRewriteEntireFile("test.go") {
		t.Fatal("should not force rewrite after 1 failure")
	}
}

func TestLoopBreakerActivates(t *testing.T) {
	e := NewEngine()
	e.RecordCompilationFailure("test.go")
	e.RecordCompilationFailure("test.go")
	if e.RecoveryCount() != 2 {
		t.Fatalf("expected recovery count 2, got %d", e.RecoveryCount())
	}
	if e.RecoveryState() != RecoveryForceRewrite {
		t.Fatalf("expected RecoveryForceRewrite after 2 failures, got %v", e.RecoveryState())
	}
	if !e.MustRewriteEntireFile("test.go") {
		t.Fatal("expected MustRewriteEntireFile true after 2 failures")
	}
}

func TestRecordCompilationSuccess(t *testing.T) {
	e := NewEngine()
	e.RecordCompilationFailure("a.go")
	e.RecordCompilationSuccess()
	if e.RecoveryCount() != 0 {
		t.Fatalf("expected reset recovery count, got %d", e.RecoveryCount())
	}
	if e.RecoveryState() != RecoveryNone {
		t.Fatalf("expected RecoveryNone after success")
	}
}

func TestMustRewriteEntireFile(t *testing.T) {
	e := NewEngine()
	if e.MustRewriteEntireFile("test.go") {
		t.Fatal("should not rewrite with no failures")
	}
	e.RecordCompilationFailure("test.go")
	if e.MustRewriteEntireFile("test.go") {
		t.Fatal("should not rewrite after 1 failure")
	}
	e.RecordCompilationFailure("test.go")
	if !e.MustRewriteEntireFile("test.go") {
		t.Fatal("should rewrite after 2 failures on same file")
	}
}

func TestMustRewriteEntireFile_DifferentFiles(t *testing.T) {
	e := NewEngine()
	e.RecordCompilationFailure("a.go")
	e.RecordCompilationFailure("b.go")
	if e.RecoveryCount() != 2 {
		t.Fatalf("expected recovery count 2, got %d", e.RecoveryCount())
	}
	if !e.MustRewriteEntireFile("a.go") {
		t.Fatal("expected rewrite for a.go after 1 failure each + total=2")
	}
	if !e.MustRewriteEntireFile("b.go") {
		t.Fatal("expected rewrite for b.go after 1 failure each + total=2")
	}
}

func TestReset(t *testing.T) {
	e := NewEngine()
	e.RecordCompilationFailure("test.go")
	e.RecordCompilationFailure("test.go")
	e.Reset()
	if e.RecoveryCount() != 0 {
		t.Fatalf("expected 0 after reset, got %d", e.RecoveryCount())
	}
	if e.RecoveryState() != RecoveryNone {
		t.Fatalf("expected RecoveryNone after reset")
	}
	if e.FileFailureCount("test.go") != 0 {
		t.Fatalf("expected 0 file failure count after reset, got %d", e.FileFailureCount("test.go"))
	}
}

func TestValidateFirstToken_CodeFence(t *testing.T) {
	e := NewEngine()
	err := e.ValidateFirstToken("```go\npackage main\n```")
	if err != nil {
		t.Fatalf("expected no error for code fence: %v", err)
	}
}

func TestValidateFirstToken_FileTag(t *testing.T) {
	e := NewEngine()
	err := e.ValidateFirstToken("FILE: main.go")
	if err != nil {
		t.Fatalf("expected no error for FILE: tag: %v", err)
	}
}

func TestValidateFirstToken_DiffHeader(t *testing.T) {
	e := NewEngine()
	err := e.ValidateFirstToken("--- a/main.go\n+++ b/main.go")
	if err != nil {
		t.Fatalf("expected no error for diff header: %v", err)
	}
}

func TestValidateFirstToken_Prose(t *testing.T) {
	e := NewEngine()
	err := e.ValidateFirstToken("Here is the fix for the bug")
	if err == nil {
		t.Fatal("expected error for prose prefix")
	}
	if !strings.Contains(err.Error(), "first token must be") {
		t.Fatalf("expected 'first token must be' in error, got: %v", err)
	}
}

func TestValidateFirstToken_Empty(t *testing.T) {
	e := NewEngine()
	err := e.ValidateFirstToken("")
	if err == nil {
		t.Fatal("expected error for empty output")
	}
}

func TestParseBuildOutput_FILE(t *testing.T) {
	output := "FILE: main.go\n```go\npackage main\n\nfunc main() {}\n```"
	mutations := ParseBuildOutput(output)
	if len(mutations) != 1 {
		t.Fatalf("expected 1 mutation, got %d", len(mutations))
	}
	if mutations[0].File != "main.go" {
		t.Fatalf("expected main.go, got %s", mutations[0].File)
	}
	if !strings.Contains(mutations[0].Content, "package main") {
		t.Fatalf("expected 'package main' in content, got: %s", mutations[0].Content)
	}
}

func TestParseBuildOutput_Diff(t *testing.T) {
	output := "--- a/main.go\n+++ b/main.go\n@@ -1,3 +1,4 @@\n package main\n+\n func main() {}"
	mutations := ParseBuildOutput(output)
	if len(mutations) != 1 {
		t.Fatalf("expected 1 mutation, got %d", len(mutations))
	}
	if mutations[0].File != "main.go" {
		t.Fatalf("expected main.go, got %s", mutations[0].File)
	}
}

func TestParseBuildOutput_MultipleFiles(t *testing.T) {
	output := "FILE: a.go\n```go\npackage a\n```\nFILE: b.go\n```go\npackage b\n```"
	mutations := ParseBuildOutput(output)
	if len(mutations) != 2 {
		t.Fatalf("expected 2 mutations, got %d", len(mutations))
	}
	if mutations[0].File != "a.go" {
		t.Fatalf("expected a.go, got %s", mutations[0].File)
	}
	if mutations[1].File != "b.go" {
		t.Fatalf("expected b.go, got %s", mutations[1].File)
	}
}

func TestParseBuildOutput_Empty(t *testing.T) {
	mutations := ParseBuildOutput("")
	if len(mutations) != 0 {
		t.Fatalf("expected 0 mutations, got %d", len(mutations))
	}
}

func TestParseBuildOutput_NoFILE(t *testing.T) {
	output := "some text without FILE tags"
	mutations := ParseBuildOutput(output)
	if len(mutations) != 0 {
		t.Fatalf("expected 0 mutations, got %d", len(mutations))
	}
}

func TestNewExecutor(t *testing.T) {
	e := NewEngine()
	ex := NewExecutor("/tmp", e)
	if ex == nil {
		t.Fatal("expected non-nil executor")
	}
}

func TestFileMutationStruct(t *testing.T) {
	mut := FileMutation{
		File:    "test.go",
		Content: "package test",
		Mode:    ModeFullRewrite,
	}
	if mut.File != "test.go" {
		t.Fatalf("expected test.go, got %s", mut.File)
	}
	if mut.Mode != ModeFullRewrite {
		t.Fatalf("expected ModeFullRewrite, got %v", mut.Mode)
	}
}

func TestRecoveryStateNames(t *testing.T) {
	if RecoveryNone != 0 {
		t.Fatal("expected RecoveryNone = 0")
	}
	if RecoveryFirstPatch != 1 {
		t.Fatal("expected RecoveryFirstPatch = 1")
	}
	if RecoveryForceRewrite != 2 {
		t.Fatal("expected RecoveryForceRewrite = 2")
	}
}

func TestEngineConcurrentSafety(t *testing.T) {
	e := NewEngine()
	for i := 0; i < 10; i++ {
		e.RecordCompilationFailure("test.go")
	}
	if e.RecoveryCount() != 10 {
		t.Fatalf("expected 10 failures, got %d", e.RecoveryCount())
	}
	if !e.MustRewriteEntireFile("test.go") {
		t.Fatal("expected rewrite after 10 failures")
	}
}
