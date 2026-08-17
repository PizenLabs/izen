package autonomy

import (
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/events"
)

func TestAnalyzeFileDetectsGoAST(t *testing.T) {
	fi := AnalyzeFile("server.go", "package main\nfunc main() {}\n")
	if fi.Language != "Go" {
		t.Errorf("language = %q, want Go", fi.Language)
	}
	if fi.Strategy != StrategyAST {
		t.Errorf("strategy = %s, want ast", fi.Strategy)
	}
	if fi.ParserKind != ParserAST {
		t.Errorf("parser kind = %s, want ast", fi.ParserKind)
	}
	if !fi.ParserAvailable {
		t.Error("Go AST parser must be reported available")
	}
	if fi.Lines != 2 {
		t.Errorf("lines = %d, want 2", fi.Lines)
	}
	if fi.Size == 0 {
		t.Error("size must be non-zero")
	}
}

func TestAnalyzeFileDegradesTreeSitter(t *testing.T) {
	fi := AnalyzeFile("app.py", "def f():\n    pass\n")
	if fi.Strategy != StrategyTreeSitter {
		t.Errorf("strategy = %s, want tree_sitter", fi.Strategy)
	}
	if fi.ParserKind != ParserTreeSitter {
		t.Errorf("parser kind = %s, want tree-sitter", fi.ParserKind)
	}
	if fi.ParserAvailable {
		t.Error("tree-sitter backend is not wired — parser must be reported unavailable")
	}
	if fi.Language != "Python" {
		t.Errorf("language = %q, want Python", fi.Language)
	}
}

func TestAnalyzeFileSemanticHTMLAndText(t *testing.T) {
	if fi := AnalyzeFile("index.html", "<html></html>"); fi.Strategy != StrategySemantic {
		t.Errorf("html strategy = %s, want semantic", fi.Strategy)
	}
	if fi := AnalyzeFile("README.md", "hello world"); fi.Strategy != StrategySemantic {
		t.Errorf("markdown strategy = %s, want semantic", fi.Strategy)
	}
}

func TestAnalyzeFileEmptyIsNone(t *testing.T) {
	fi := AnalyzeFile("a.go", "")
	if fi.Strategy != StrategyNone {
		t.Errorf("empty strategy = %s, want none", fi.Strategy)
	}
	if fi.ParserAvailable {
		t.Error("empty artifact must not claim a parser")
	}
}

func TestMeasureComplexity(t *testing.T) {
	c := MeasureComplexity("if a {\n  for b {\n    x()\n  }\n}\n")
	if c.MaxNesting < 2 {
		t.Errorf("max nesting = %d, want >= 2", c.MaxNesting)
	}
	if c.Cyclomatic < 2 {
		t.Errorf("cyclomatic = %d, want >= 2", c.Cyclomatic)
	}
	if c.TokenEstimate <= 0 {
		t.Error("token estimate must be positive")
	}
	if c.AvgLineLength <= 0 {
		t.Error("avg line length must be positive")
	}
}

func TestEstimateTokens(t *testing.T) {
	if n := EstimateTokens(strings.Repeat("word ", 100)); n < 100 {
		t.Errorf("tokens = %d, want >= 100", n)
	}
}

func TestCompileGoUsesAST(t *testing.T) {
	content := `package server

import (
	"fmt"
	"strings"
)

type Handler struct{}

func (h *Handler) Serve() error { return nil }

func NewHandler() *Handler { return &Handler{} }
`
	ctx := CompileContext("server.go", content)
	if ctx.Intelligence.Strategy != StrategyAST {
		t.Errorf("strategy = %s, want ast", ctx.Intelligence.Strategy)
	}
	if len(ctx.Code.Symbols) < 3 {
		t.Errorf("symbols = %d, want >= 3 (Handler/Serve/NewHandler)", len(ctx.Code.Symbols))
	}
	if len(ctx.Code.Dependencies) < 2 {
		t.Errorf("dependencies = %v, want fmt+strings", ctx.Code.Dependencies)
	}
	if len(ctx.Regions) == 0 {
		t.Error("regions must be compiled from the affected scope")
	}
}

func TestCompilePythonRecordsDegradation(t *testing.T) {
	ctx := CompileContext("app.py", "def handler(event, context):\n    return event\n")
	found := false
	for _, f := range ctx.Code.Findings {
		if f.Type == "file.parser_degraded" {
			found = true
		}
	}
	if !found {
		t.Error("expected file.parser_degraded finding for unwired tree-sitter language")
	}
	if len(ctx.Code.Symbols) < 1 {
		t.Error("AST-lite scanner must still extract symbols")
	}
}

func TestCompileFindingsHaveConfidenceAndAction(t *testing.T) {
	ctx := CompileContext("index.html", `<html><body><div><p>unclosed p`)
	for _, f := range ctx.Evidence() {
		if f.Confidence <= 0 {
			t.Errorf("finding %s has no confidence", f.Type)
		}
		if f.Action == "" {
			t.Errorf("finding %s has no suggested action", f.Type)
		}
	}
	if ctx.AggregateConfidence() <= 0 {
		t.Error("aggregate confidence must be positive")
	}
}

func TestGoParseErrorDegradesWithFinding(t *testing.T) {
	ctx := CompileContext("broken.go", "package broken\nfunc broken( {\n")
	found := false
	for _, f := range ctx.Code.Findings {
		if f.Type == "code.parse_error" {
			found = true
		}
	}
	if !found {
		t.Error("expected code.parse_error finding for broken Go source")
	}
}

func TestEngineAnalyzePublishesIntelContext(t *testing.T) {
	bus := events.NewBus(32)
	eng := NewEngine(WithEventBus(bus))

	ch := make(chan events.ContextCompiledPayload, 1)
	sub := bus.Subscribe(events.EventContextCompiled, func(ev events.DomainEvent) {
		if p, ok := ev.Payload().(events.ContextCompiledPayload); ok {
			select {
			case ch <- p:
			default:
			}
		}
	})
	defer sub.Cancel()

	eng.Analyze("server.go", "package main\nfunc main() {}\n")
	var got events.ContextCompiledPayload
	select {
	case got = <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("ContextCompiled event not delivered")
	}
	if got.Path != "server.go" || got.Kind != "code" {
		t.Errorf("payload path/kind = %q/%q", got.Path, got.Kind)
	}
	if got.Language != "Go" {
		t.Errorf("payload language = %q, want Go", got.Language)
	}
	if got.Strategy != "ast" {
		t.Errorf("payload strategy = %q, want ast", got.Strategy)
	}
}
