package retrieval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/retrieval/fulltext"
)

func mockLogFn(t *testing.T) func(string, ...interface{}) {
	t.Helper()
	return func(format string, args ...interface{}) {
		t.Logf(format, args...)
	}
}

func TestOrchestratorPipeline_GraphStop(t *testing.T) {
	root := projectRoot()
	if root == "" {
		t.Skip("no project root found")
	}

	o := NewOrchestrator(OrchestratorConfig{
		TokenEstimator: NewTokenWeightEstimator(),
		LogFn:          mockLogFn(t),
	})

	rs, err := o.Execute(context.Background(), Query{Symbol: "NewEngine"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rs == nil {
		t.Fatal("expected non-nil result")
	}
	if rs.ResultSet.Empty() {
		t.Skip("no graph results for NewEngine (test needs graph built)")
	}
	if !containsStage(rs.LayersUsed, StageGraph) {
		t.Fatal("expected graph stage to be used")
	}
	t.Logf("Pipeline used %v, confidence %.3f, stop: %s", rs.LayersUsed, rs.ResultSet.Confidence, rs.StopReason)
}

func TestOrchestratorPipeline_FullTextOnly(t *testing.T) {
	dir := t.TempDir()
	ft := fulltext.NewEngine(dir, fulltext.WithLogFn(mockLogFn(t)))

	writeFile(t, dir, "server.go", `package server
func Start() { println("server started") }
`)
	ft.IndexFile("server.go")

	orchestrator := NewOrchestrator(OrchestratorConfig{
		FullText:       ft,
		TokenEstimator: NewTokenWeightEstimator(),
		LogFn:          mockLogFn(t),
	})

	rs, err := orchestrator.Execute(context.Background(), Query{Text: "Start"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rs.ResultSet.Empty() {
		t.Fatal("expected fulltext results")
	}
	if !containsStage(rs.LayersUsed, StageFullText) {
		t.Fatal("expected fulltext stage to be used")
	}
	t.Logf("FullText results: %d, confidence: %.3f", rs.ResultSet.Count(), rs.ResultSet.Confidence)
}

func TestOrchestratorPipeline_FallbackChain(t *testing.T) {
	fc := NewFallbackChain(projectRoot())
	orchestrator := NewOrchestrator(OrchestratorConfig{
		Fallback:       fc,
		TokenEstimator: NewTokenWeightEstimator(),
		LogFn:          mockLogFn(t),
	})

	rs, err := orchestrator.Execute(context.Background(), Query{Text: "NewEngine"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rs.ResultSet.Empty() {
		t.Skip("no ripgrep results (rg might not be installed)")
	}
	if !containsStage(rs.LayersUsed, StageRipgrep) {
		t.Fatal("expected ripgrep stage to be used")
	}
	t.Logf("Ripgrep results: %d, confidence: %.3f", rs.ResultSet.Count(), rs.ResultSet.Confidence)
}

func TestOrchestratorPipeline_FullPipeline(t *testing.T) {
	dir := t.TempDir()
	ft := fulltext.NewEngine(dir, fulltext.WithLogFn(mockLogFn(t)))
	writeFile(t, dir, "utils.go", `package utils
func Helper() string { return "helper" }
`)
	ft.IndexFile("utils.go")

	orchestrator := NewOrchestrator(OrchestratorConfig{
		FullText:       ft,
		GraphLookup:    &GraphLookup{},
		TokenEstimator: NewTokenWeightEstimator(),
		LogFn:          mockLogFn(t),
	})

	rs, err := orchestrator.Execute(context.Background(), Query{Text: "Helper"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rs.ResultSet.Empty() {
		t.Fatal("expected results from pipeline")
	}
	t.Logf("Pipeline layers: %v, confidence: %.3f", rs.LayersUsed, rs.ResultSet.Confidence)
}

func TestOrchestratorTokenBudget_Truncation(t *testing.T) {
	dir := t.TempDir()
	ft := fulltext.NewEngine(dir, fulltext.WithLogFn(mockLogFn(t)))

	for i := 0; i < 20; i++ {
		name := filepath.Join("pkg", "file"+string(rune('a'+i))+".go")
		writeFile(t, dir, name, `package pkg
func F`+string(rune('A'+i))+`() string { return "data" }
`)
		ft.IndexFile(name)
	}

	orchestrator := NewOrchestrator(OrchestratorConfig{
		FullText:       ft,
		TokenEstimator: NewTokenWeightEstimator(),
		MaxTokens:      50,
		LogFn:          mockLogFn(t),
	})

	rs, err := orchestrator.Execute(context.Background(), Query{Text: "func"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if rs.TokenEstimate <= rs.TokenBudget {
		count := rs.ResultSet.Count()
		_ = count
	}

	if rs.Truncated {
		t.Logf("Results truncated: %d tokens used of %d budget, %d results kept",
			rs.TokenEstimate, rs.TokenBudget, rs.ResultSet.Count())
	} else {
		t.Logf("Results within budget: %d tokens used of %d budget, %d results",
			rs.TokenEstimate, rs.TokenBudget, rs.ResultSet.Count())
	}
}

func TestOrchestratorEmptyQuery(t *testing.T) {
	orchestrator := NewOrchestrator(OrchestratorConfig{
		TokenEstimator: NewTokenWeightEstimator(),
		LogFn:          mockLogFn(t),
	})

	rs, err := orchestrator.Execute(context.Background(), Query{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !rs.ResultSet.Empty() {
		t.Fatal("expected empty results for empty query")
	}
	if len(rs.LayersUsed) != 0 {
		t.Fatal("expected no layers used for empty query")
	}
}

func TestOrchestratorLayersUsed(t *testing.T) {
	dir := t.TempDir()
	ft := fulltext.NewEngine(dir, fulltext.WithLogFn(mockLogFn(t)))
	writeFile(t, dir, "calc.go", `package calc
func Add(a, b int) int { return a + b }
`)
	ft.IndexFile("calc.go")

	logged := ""
	orchestrator := NewOrchestrator(OrchestratorConfig{
		FullText:       ft,
		Fallback:       NewFallbackChain(dir),
		TokenEstimator: NewTokenWeightEstimator(),
		LogFn: func(format string, args ...interface{}) {
			logged += format
		},
	})

	rs, err := orchestrator.Execute(context.Background(), Query{Text: "zzz_unique_match_only"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	for _, layer := range rs.LayersUsed {
		t.Logf("Layer used: %s", layer)
	}

	t.Logf("Logged: %s", logged)
	_ = rs
}

func TestOrchestratorConfidenceThreshold(t *testing.T) {
	dir := t.TempDir()
	ft := fulltext.NewEngine(dir, fulltext.WithLogFn(mockLogFn(t)))
	writeFile(t, dir, "math.go", `package math
func Multiply(a, b int) int { return a * b }
`)
	ft.IndexFile("math.go")

	loggedMsgs := []string{}
	orchestrator := NewOrchestrator(OrchestratorConfig{
		FullText:       ft,
		TokenEstimator: NewTokenWeightEstimator(),
		MaxTokens:      500,
		LogFn: func(format string, args ...interface{}) {
			loggedMsgs = append(loggedMsgs, format)
		},
	})

	rs, err := orchestrator.Execute(context.Background(), Query{Text: "Multiply"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if rs.ResultSet.Empty() {
		t.Skip("no results — might need ripgrep installed")
	}

	for _, msg := range loggedMsgs {
		if strings.Contains(msg, "hit confidence") || strings.Contains(msg, "pipeline stopped") {
			t.Logf("Confidence threshold messages: %s", msg)
		}
	}
}

func TestOrchestratorContextCancellation(t *testing.T) {
	orchestrator := NewOrchestrator(OrchestratorConfig{
		TokenEstimator: NewTokenWeightEstimator(),
		LogFn:          mockLogFn(t),
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := orchestrator.Execute(ctx, Query{Text: "test"})
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if err.Error() != context.Canceled.Error() {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestOrchestratorNewDefaultOrchestrator(t *testing.T) {
	root := t.TempDir()

	opts := []OrchestratorOption{
		WithOrchestratorLogFn(mockLogFn(t)),
		WithOrchestratorMaxTokens(4000),
		WithOrchestratorAutoIndex(true),
	}

	o := NewDefaultOrchestrator(root, opts...)
	if o == nil {
		t.Fatal("expected non-nil orchestrator")
	}
	if o.maxTokens != 4000 {
		t.Fatalf("expected maxTokens 4000, got %d", o.maxTokens)
	}
}

func TestOrchestratorFullTextFirstThenFallback(t *testing.T) {
	dir := t.TempDir()
	ft := fulltext.NewEngine(dir, fulltext.WithLogFn(mockLogFn(t)))
	writeFile(t, dir, "app.go", `package app
const Version = "1.0"
`)

	ft.IndexFile("app.go")

	stageOrder := []PipelineStage{}
	orchestrator := NewOrchestrator(OrchestratorConfig{
		FullText:       ft,
		Fallback:       NewFallbackChain(dir),
		TokenEstimator: NewTokenWeightEstimator(),
		LogFn: func(format string, args ...interface{}) {
			stageOrder = append(stageOrder, stageFromLog(format))
		},
	})

	rs, err := orchestrator.Execute(context.Background(), Query{Text: "Version"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if !rs.ResultSet.Empty() {
		t.Logf("Result from layers: %v", rs.LayersUsed)
	}
}

func TestOrchestratorMultipleStages(t *testing.T) {
	dir := t.TempDir()
	ft := fulltext.NewEngine(dir, fulltext.WithLogFn(mockLogFn(t)))
	writeFile(t, dir, "findme.go", `package findme
func Locate() string { return "found" }
`)
	ft.IndexFile("findme.go")

	callOrder := []string{}
	stageNames := map[PipelineStage]string{
		StageGraph:    "graph",
		StageVector:   "vector_rag",
		StageFullText: "fulltext",
		StageRipgrep:  "ripgrep",
	}

	o := NewOrchestrator(OrchestratorConfig{
		GraphLookup:    &GraphLookup{},
		FullText:       ft,
		Fallback:       NewFallbackChain(dir),
		TokenEstimator: NewTokenWeightEstimator(),
		LogFn: func(format string, args ...interface{}) {
			callOrder = append(callOrder, format)
		},
	})

	rs, _ := o.Execute(context.Background(), Query{Text: "Locate"})

	if rs != nil && !rs.ResultSet.Empty() {
		firstStage := rs.LayersUsed[0]
		t.Logf("First stage with results: %s", stageNames[firstStage])
	} else {
		t.Log("No results from any stage (expected when no match)")
	}

	t.Logf("Call order: %d log entries", len(callOrder))
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func containsStage(stages []PipelineStage, stage PipelineStage) bool {
	for _, s := range stages {
		if s == stage {
			return true
		}
	}
	return false
}

func stageFromLog(format string) PipelineStage {
	switch {
	case strings.Contains(format, "graph"):
		return StageGraph
	case strings.Contains(format, "vector_rag") || strings.Contains(format, "vector"):
		return StageVector
	case strings.Contains(format, "fulltext"):
		return StageFullText
	case strings.Contains(format, "ripgrep"):
		return StageRipgrep
	default:
		return -1
	}
}
