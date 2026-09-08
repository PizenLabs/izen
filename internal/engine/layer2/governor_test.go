package layer2

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestGovernorBuildBasic(t *testing.T) {
	sor := newTestSor(t, goFixture())
	g := NewContextGovernor(sor)

	ctx, err := g.Build(ContextRequest{TargetSymbol: "Service.Run"}, DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Target == nil || ctx.Target.QualName != "Service.Run" {
		t.Fatalf("target mismatch: %+v", ctx.Target)
	}
	if len(ctx.Files) == 0 {
		t.Fatal("no files in context")
	}
	if ctx.Files[0].Path != "svc/service.go" {
		t.Errorf("target file should rank first, got %q", ctx.Files[0].Path)
	}
	if ctx.Stats.Tokens > ctx.Stats.BudgetTokens {
		t.Errorf("budget exceeded: %d > %d", ctx.Stats.Tokens, ctx.Stats.BudgetTokens)
	}
	if !ctx.Stats.BudgetMet {
		t.Error("budget not met")
	}
	if len(ctx.Symbols) == 0 {
		t.Error("no ranked symbols in context")
	}
}

func TestGovernorBuildTargetFile(t *testing.T) {
	sor := newTestSor(t, goFixture())
	g := NewContextGovernor(sor)

	ctx, err := g.Build(ContextRequest{TargetFile: "svc/service.go"}, DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(ctx.Files) == 0 || ctx.Files[0].Path != "svc/service.go" {
		t.Errorf("target file should be first, got %v", paths(ctx.Files))
	}
}

func TestGovernorInvalidPolicy(t *testing.T) {
	sor := newTestSor(t, goFixture())
	g := NewContextGovernor(sor)
	req := ContextRequest{TargetSymbol: "Service.Run"}

	p := DefaultPolicy()
	p.MaxTokenBudget = 0
	if _, err := g.Build(req, p); !errors.Is(err, ErrInvalidPolicy) {
		t.Errorf("expected ErrInvalidPolicy, got %v", err)
	}

	p = DefaultPolicy()
	p.CompressionRatio = 1.5
	if _, err := g.Build(req, p); !errors.Is(err, ErrInvalidPolicy) {
		t.Errorf("expected ErrInvalidPolicy, got %v", err)
	}
}

func TestGovernorMissingRequest(t *testing.T) {
	sor := newTestSor(t, goFixture())
	g := NewContextGovernor(sor)

	if _, err := g.Build(ContextRequest{}, DefaultPolicy()); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("expected ErrInvalidRequest, got %v", err)
	}
	if _, err := g.Build(ContextRequest{TargetSymbol: "nope.Nope"}, DefaultPolicy()); !errors.Is(err, ErrTargetNotFound) {
		t.Errorf("expected ErrTargetNotFound, got %v", err)
	}
}

func TestGovernorBudgetStrict(t *testing.T) {
	sor := newTestSor(t, goFixture())
	g := NewContextGovernor(sor)
	req := ContextRequest{TargetSymbol: "Service.Run"}

	for _, budget := range []int{800, 400, 200} {
		p := DefaultPolicy()
		p.MaxTokenBudget = budget
		ctx, err := g.Build(req, p)
		if err != nil {
			t.Fatalf("budget %d: %v", budget, err)
		}
		if ctx.Stats.Tokens > budget {
			t.Errorf("budget %d exceeded: %d", budget, ctx.Stats.Tokens)
		}
		if ctx.Stats.BudgetTokens != budget {
			t.Errorf("budget metadata mismatch: %d", ctx.Stats.BudgetTokens)
		}
	}
}

func TestGovernorBudgetTooSmall(t *testing.T) {
	sor := newTestSor(t, goFixture())
	g := NewContextGovernor(sor)
	p := DefaultPolicy()
	p.MaxTokenBudget = 20

	_, err := g.Build(ContextRequest{TargetSymbol: "Service.Run"}, p)
	if !errors.Is(err, ErrBudgetTooSmall) {
		t.Fatalf("expected ErrBudgetTooSmall, got %v", err)
	}
}

func TestGovernorMaxFiles(t *testing.T) {
	sor := newTestSor(t, goFixture())
	g := NewContextGovernor(sor)
	p := DefaultPolicy()
	p.MaxFiles = 1

	ctx, err := g.Build(ContextRequest{TargetSymbol: "Service.Run"}, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(ctx.Files) != 1 {
		t.Fatalf("expected 1 file, got %d (%v)", len(ctx.Files), paths(ctx.Files))
	}
	if ctx.Files[0].Path != "svc/service.go" {
		t.Errorf("expected target file, got %q", ctx.Files[0].Path)
	}
	if ctx.Stats.Files != 1 {
		t.Errorf("stats files = %d", ctx.Stats.Files)
	}
}

func TestGovernorExpandDependencies(t *testing.T) {
	sor := newTestSor(t, goFixture())
	g := NewContextGovernor(sor)
	// Target a leaf: main.go only surfaces through dependency expansion.
	req := ContextRequest{TargetSymbol: "compute"}

	base := DefaultPolicy()
	base.ExpandDependencies = false
	ctxBase, err := g.Build(req, base)
	if err != nil {
		t.Fatal(err)
	}

	exp := DefaultPolicy()
	exp.ExpandDependencies = true
	ctxExp, err := g.Build(req, exp)
	if err != nil {
		t.Fatal(err)
	}

	if len(ctxExp.Files) <= len(ctxBase.Files) {
		t.Errorf("expected dependency expansion to add files: base=%d exp=%d",
			len(ctxBase.Files), len(ctxExp.Files))
	}
	if !containsStr(paths(ctxExp.Files), "cmd/app/main.go") {
		t.Errorf("expected main.go in expanded context, got %v", paths(ctxExp.Files))
	}
	if containsStr(paths(ctxBase.Files), "cmd/app/main.go") {
		t.Errorf("main.go should not appear without expansion, got %v", paths(ctxBase.Files))
	}
}

func TestGovernorCompression(t *testing.T) {
	sor := newTestSor(t, goFixture())
	g := NewContextGovernor(sor)
	req := ContextRequest{TargetSymbol: "Service.Run"}

	keep := DefaultPolicy()
	keep.CompressionRatio = 1.0
	ctxKeep, err := g.Build(req, keep)
	if err != nil {
		t.Fatal(err)
	}

	aggr := DefaultPolicy()
	aggr.CompressionRatio = 0.2
	ctxAggr, err := g.Build(req, aggr)
	if err != nil {
		t.Fatal(err)
	}

	targetFile := "svc/service.go"
	raw := sourceOf(ctxKeep.Files, targetFile)
	comp := sourceOf(ctxAggr.Files, targetFile)
	if raw == "" || comp == "" {
		t.Fatal("missing target file in context")
	}
	if len(comp) >= len(raw) {
		t.Errorf("compression should shrink source: raw=%d comp=%d", len(raw), len(comp))
	}

	for _, want := range []string{"type Service struct", "type Runner interface", "func (s *Service) Run() error"} {
		if !strings.Contains(comp, want) {
			t.Errorf("preserved %q missing from compressed output", want)
		}
	}
	for _, bad := range []string{"return n * 2", "if a > b"} {
		if strings.Contains(comp, bad) {
			t.Errorf("stripped body %q leaked into compressed output", bad)
		}
	}

	var aggrFile *FileContext
	for i := range ctxAggr.Files {
		if ctxAggr.Files[i].Path == targetFile {
			aggrFile = &ctxAggr.Files[i]
		}
	}
	if aggrFile == nil || !aggrFile.Compressed {
		t.Error("expected compressed flag on target file")
	}
	if aggrFile.StrippedBodies <= 0 {
		t.Errorf("expected stripped bodies, got %d", aggrFile.StrippedBodies)
	}
	if ctxAggr.Stats.CompressedFiles <= 0 {
		t.Errorf("expected compressed file count > 0, got %d", ctxAggr.Stats.CompressedFiles)
	}
	if ctxKeep.Stats.CompressedFiles != 0 {
		t.Errorf("ratio 1.0 should not compress, got %d compressed files", ctxKeep.Stats.CompressedFiles)
	}
}

func TestGovernorAllowBinary(t *testing.T) {
	sor := newTestSor(t, goFixture(), WithSourceReader(func(root, path string) ([]byte, error) {
		if path == "svc/service.go" {
			return []byte{0x00, 0x01, 0x02, 'f', 'o', 'o'}, nil
		}
		return os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	}))
	g := NewContextGovernor(sor)
	req := ContextRequest{TargetSymbol: "Service.Run"}

	p := DefaultPolicy()
	p.AllowBinary = false
	ctx, err := g.Build(req, p)
	if err != nil {
		t.Fatal(err)
	}
	if containsStr(paths(ctx.Files), "svc/service.go") {
		t.Error("binary file must be excluded when AllowBinary=false")
	}

	p.AllowBinary = true
	ctx2, err := g.Build(req, p)
	if err != nil {
		t.Fatal(err)
	}
	if !containsStr(paths(ctx2.Files), "svc/service.go") {
		t.Error("binary file must be included when AllowBinary=true")
	}
}

func TestGovernorBuildTS(t *testing.T) {
	sor := newTestSor(t, tsFixture())
	g := NewContextGovernor(sor)

	ctx, err := g.Build(ContextRequest{TargetFile: "web/server.ts"}, DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(ctx.Files) == 0 {
		t.Fatal("no files in context")
	}
	if ctx.Files[0].Path != "web/server.ts" {
		t.Errorf("target file should rank first, got %v", paths(ctx.Files))
	}
	if ctx.Stats.Tokens > ctx.Stats.BudgetTokens {
		t.Errorf("budget exceeded: %d > %d", ctx.Stats.Tokens, ctx.Stats.BudgetTokens)
	}
}

func TestGovernorImportsSnapshot(t *testing.T) {
	sor := newTestSor(t, goFixture())
	g := NewContextGovernor(sor)

	ctx, err := g.Build(ContextRequest{TargetSymbol: "Service.Run"}, DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if imps, ok := ctx.Imports["cmd/app/main.go"]; ok {
		found := false
		for _, imp := range imps {
			if strings.HasSuffix(imp, "/fixture/svc") {
				found = true
			}
		}
		if !found {
			t.Errorf("expected svc import in snapshot, got %v", imps)
		}
	}
}

// TestGovernorImmutability mutates a returned context and verifies a fresh
// Build is unaffected: returned values never alias the SoR or each other.
func TestGovernorImmutability(t *testing.T) {
	sor := newTestSor(t, goFixture())
	g := NewContextGovernor(sor)
	req := ContextRequest{TargetSymbol: "Service.Run"}

	ctx1, err := g.Build(req, DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(ctx1.Files) > 0 {
		ctx1.Files[0].Source = "corrupted"
		if len(ctx1.Files[0].Symbols) > 0 {
			ctx1.Files[0].Symbols[0].Name = "corrupted"
		}
	}

	ctx2, err := g.Build(req, DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if len(ctx2.Files) == 0 {
		t.Fatal("no files in second build")
	}
	if ctx2.Files[0].Source == "corrupted" {
		t.Error("Build output aliased with caller mutation")
	}
	for _, sc := range ctx2.Symbols {
		if sc.Name == "corrupted" {
			t.Error("symbol list aliased with caller mutation")
		}
	}
}

// TestGovernorConcurrency verifies the governor is safe for concurrent Build
// calls and that every result respects its policy budget under -race.
func TestGovernorConcurrency(t *testing.T) {
	sor := newTestSor(t, goFixture())
	g := NewContextGovernor(sor)
	req := ContextRequest{TargetSymbol: "Service.Run"}
	p := DefaultPolicy()

	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, err := g.Build(req, p)
			if err != nil {
				errs <- err
				return
			}
			if ctx.Stats.Tokens > p.MaxTokenBudget {
				errs <- fmt.Errorf("budget exceeded: %d", ctx.Stats.Tokens)
			}
			if len(ctx.Files) == 0 {
				errs <- fmt.Errorf("empty context")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
