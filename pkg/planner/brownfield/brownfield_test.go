package brownfield

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/pkg/graph"
	"github.com/PizenLabs/izen/pkg/ir"
	"github.com/PizenLabs/izen/pkg/kernel"
	"github.com/PizenLabs/izen/pkg/op"
	"github.com/PizenLabs/izen/pkg/resource/file"
)

// writeRepair builds an op.OpWriteFile repair operation writing an artifact
// to relPath inside root.
func writeRepair(root, id, relPath, content string) (op.Operation, error) {
	res, err := file.NewFileResource(root, relPath, 0)
	if err != nil {
		return op.Operation{}, err
	}
	return op.NewOperation(id, op.OpWriteFile, res, ir.NewFile(relPath, []byte(content)), nil, time.Second)
}

func TestBrownfieldPlannerClosedLoopRepair(t *testing.T) {
	root := t.TempDir()
	artifacts := []ir.Artifact{
		ir.NewFile("main.go", []byte("package main\n")),
	}
	verify := func(string) string { return "cat helper.go" }

	repairs := 0
	repair := func(ctx context.Context, failure *graph.ExecutionFailure, report FailureReport, attempt int) ([]op.Operation, error) {
		repairs++
		if report.Symptom != SymptomMissingFile || report.MissingPath != "helper.go" {
			return nil, nil
		}
		o, err := writeRepair(root, fmt.Sprintf("fix-%d", attempt), "helper.go", "package main\n")
		if err != nil {
			return nil, err
		}
		return []op.Operation{o}, nil
	}

	p, err := NewBrownfieldPlanner(root, WithVerifyCommand(verify), WithRepairFunc(repair))
	if err != nil {
		t.Fatalf("NewBrownfieldPlanner: %v", err)
	}
	result, err := p.Plan(t.Context(), "refactor", artifacts)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	if err := p.ExecuteAndRepair(t.Context(), kernel.NewEngine(nil), result.Graph, 3); err != nil {
		t.Fatalf("ExecuteAndRepair: %v", err)
	}
	if !result.Graph.IsCompleted() {
		t.Fatal("expected graph completed after repair")
	}
	if repairs != 1 {
		t.Fatalf("expected exactly one repair cycle, got %d", repairs)
	}
	if _, ok := result.Graph.GetNode("fix-0"); !ok {
		t.Fatal("expected injected repair node fix-0")
	}
	data, err := os.ReadFile(filepath.Join(root, "helper.go"))
	if err != nil {
		t.Fatalf("read helper.go: %v", err)
	}
	if string(data) != "package main\n" {
		t.Fatalf("unexpected helper.go content %q", data)
	}
}

func TestBrownfieldPlannerRepairBudgetExhausted(t *testing.T) {
	root := t.TempDir()
	artifacts := []ir.Artifact{
		ir.NewFile("main.go", []byte("package main\n")),
	}
	verify := func(string) string { return "exit 1" }
	// Every repair op fails to execute, so each repair cycle surfaces a new
	// failure and the budget is consumed without ever reaching green.
	repair := func(ctx context.Context, failure *graph.ExecutionFailure, report FailureReport, attempt int) ([]op.Operation, error) {
		o, err := writeRepair(root, fmt.Sprintf("noop-%d", attempt), "no/such/dir/noop.go", "x")
		if err != nil {
			return nil, err
		}
		return []op.Operation{o}, nil
	}

	p, err := NewBrownfieldPlanner(root, WithVerifyCommand(verify), WithRepairFunc(repair))
	if err != nil {
		t.Fatalf("NewBrownfieldPlanner: %v", err)
	}
	result, err := p.Plan(t.Context(), "refactor", artifacts)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	err = p.ExecuteAndRepair(t.Context(), kernel.NewEngine(nil), result.Graph, 2)
	if err == nil {
		t.Fatal("expected repair budget exhaustion")
	}
	if !strings.Contains(err.Error(), "budget") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := result.Graph.GetNode("noop-1"); !ok {
		t.Fatal("expected second injected repair node")
	}
}

func TestBrownfieldPlannerNoRepairStrategy(t *testing.T) {
	root := t.TempDir()
	p, err := NewBrownfieldPlanner(root, WithVerifyCommand(func(string) string { return "exit 1" }))
	if err != nil {
		t.Fatalf("NewBrownfieldPlanner: %v", err)
	}
	result, err := p.Plan(t.Context(), "refactor", nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	err = p.ExecuteAndRepair(t.Context(), kernel.NewEngine(nil), result.Graph, 3)
	if !errors.Is(err, ErrNoRepair) {
		t.Fatalf("expected ErrNoRepair, got %v", err)
	}
	if repairs, ok := result.Graph.GetNode("bf-repair-1"); ok {
		t.Fatalf("expected no repair node injected, got %s", repairs.ID())
	}
}

func TestBrownfieldPlannerDefaultRepairWritesMissingArtifact(t *testing.T) {
	root := t.TempDir()
	artifacts := []ir.Artifact{
		ir.NewFile("main.go", []byte("package main\n")),
		ir.NewFile("helper.go", []byte("package main\n")),
	}

	p, err := NewBrownfieldPlanner(root, WithVerifyCommand(func(string) string { return "cat helper.go" }))
	if err != nil {
		t.Fatalf("NewBrownfieldPlanner: %v", err)
	}
	result, err := p.Plan(t.Context(), "refactor", artifacts)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	if err := p.ExecuteAndRepair(t.Context(), kernel.NewEngine(nil), result.Graph, 2); err != nil {
		t.Fatalf("ExecuteAndRepair: %v", err)
	}
	if !result.Graph.IsCompleted() {
		t.Fatal("expected graph completed")
	}
	data, err := os.ReadFile(filepath.Join(root, "helper.go"))
	if err != nil {
		t.Fatalf("read helper.go: %v", err)
	}
	if string(data) != "package main\n" {
		t.Fatalf("unexpected helper.go content %q", data)
	}
}

func TestAnalyzeFailureSymptoms(t *testing.T) {
	tests := []struct {
		name string
		err  error
		data string
		want FailureSymptom
		path string
	}{
		{"missing shell file", errors.New("terminal: command \"test -f helper.go\" failed: exit status 1"),
			"test: helper.go: No such file or directory\n", SymptomMissingFile, "helper.go"},
		{"missing open", errors.New("graph: command \"go build\": exit status 1"),
			"open helper.go: no such file or directory", SymptomMissingFile, "helper.go"},
		{"cannot find package", errors.New("graph: command: exit status 1"),
			`main.go:3:8: cannot find package "foo"`, SymptomCompileError, ""},
		{"undefined symbol", errors.New("graph: command: exit status 1"),
			"./main.go:3:14: undefined: helper", SymptomCompileError, ""},
		{"test failure", errors.New("graph: command: exit status 1"),
			"--- FAIL: TestThing\nFAIL\n", SymptomTestFailure, ""},
		{"panic", errors.New("graph: command: exit status 2"),
			"panic: index out of range", SymptomTestFailure, ""},
		{"unknown", errors.New("boom"), "", SymptomUnknown, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := AnalyzeFailure(
				kernel.TaskResult{Status: kernel.StatusFailed, Error: tt.err, Data: tt.data},
				"test -f helper.go",
			)
			if report.Symptom != tt.want {
				t.Fatalf("symptom = %q, want %q", report.Symptom, tt.want)
			}
			if report.MissingPath != tt.path {
				t.Fatalf("missing path = %q, want %q", report.MissingPath, tt.path)
			}
			if report.Command != "test -f helper.go" {
				t.Fatalf("command = %q, want %q", report.Command, "test -f helper.go")
			}
		})
	}
}

func TestExecuteAndRepairValidation(t *testing.T) {
	p, err := NewBrownfieldPlanner(t.TempDir())
	if err != nil {
		t.Fatalf("NewBrownfieldPlanner: %v", err)
	}
	result, err := p.Plan(t.Context(), "refactor", nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	if err := p.ExecuteAndRepair(t.Context(), nil, result.Graph, 1); !errors.Is(err, ErrNilEngine) {
		t.Fatalf("expected ErrNilEngine, got %v", err)
	}
	if err := p.ExecuteAndRepair(t.Context(), kernel.NewEngine(nil), nil, 1); !errors.Is(err, ErrNilGraph) {
		t.Fatalf("expected ErrNilGraph, got %v", err)
	}
}

func TestBrownfieldPlannerConstructorRequiresRoot(t *testing.T) {
	if _, err := NewBrownfieldPlanner(""); !errors.Is(err, ErrEmptyWorkspaceRoot) {
		t.Fatalf("expected ErrEmptyWorkspaceRoot, got %v", err)
	}
}
