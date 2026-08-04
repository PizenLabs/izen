package layer4

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/lea"
	"github.com/PizenLabs/izen/pkg/engine/layer1"
	"github.com/PizenLabs/izen/pkg/engine/layer2"
)

// ---------------------------------------------------------------------------
// Test fixtures and helpers.
// ---------------------------------------------------------------------------

// structuralFixture is a small Go workspace with an in-repo package svc, a
// main package and an unrelated api package.
func structuralFixture() map[string]string {
	return map[string]string{
		"go.mod": "module github.com/example/val\n\ngo 1.26\n",
		"svc/service.go": `package svc

import "fmt"

// Service is the core type.
type Service struct{}

// Run executes the service.
func (s *Service) Run() error {
	fmt.Println("run")
	return nil
}

// Compute doubles n when positive.
func Compute(n int) int {
	if n < 0 {
		return 0
	}
	return n * 2
}
`,
		"cmd/app/main.go": `package main

import (
	"fmt"

	"github.com/example/val/svc"
)

func main() {
	s := &svc.Service{}
	_ = s.Run()
	_ = svc.Compute(2)
	fmt.Println("ok")
}
`,
		"api/handler.go": `package api

import "github.com/example/val/svc"

// Handle returns a computed value.
func Handle() int {
	return svc.Compute(3)
}
`,
	}
}

func writeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return root
}

func newTestSor(t *testing.T, files map[string]string) (*layer2.Sor, string) {
	t.Helper()
	root := writeRepo(t, files)
	e := lea.NewEngine(root)
	t.Cleanup(func() { _ = e.Close() })
	stats, err := e.Index(context.Background())
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if stats.Files == 0 {
		t.Fatal("no files indexed")
	}
	return layer2.NewSor(e), root
}

func patch(path, oldSrc, newSrc string) Patch {
	return Patch{Path: path, Old: oldSrc, New: newSrc, Changed: true}
}

func validResult(stage Stage, summary string) *ValidationResult {
	return &ValidationResult{OK: true, Stage: stage, ExitCode: 0, Summary: summary}
}

// fakeCaps is a test capability reader.
type fakeCaps struct {
	caps map[layer1.Capability]string
}

func (f fakeCaps) Supports(cap layer1.Capability) bool {
	_, ok := f.caps[cap]
	return ok
}

func (f fakeCaps) Resolve(cap layer1.Capability) (string, bool) {
	c, ok := f.caps[cap]
	return c, ok
}

func fullCaps() fakeCaps {
	return fakeCaps{caps: map[layer1.Capability]string{
		layer1.CapBuild:  "go build ./...",
		layer1.CapTest:   "go test ./...",
		layer1.CapLint:   "go vet ./...",
		layer1.CapFormat: "gofmt -w .",
	}}
}

// ---------------------------------------------------------------------------
// Plan resolver tests.
// ---------------------------------------------------------------------------

func TestResolverPlanFullStack(t *testing.T) {
	r := NewResolver(fullCaps())
	plan, err := r.Plan()
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	want := []Stage{StageStructural, StageSyntax, StageLint, StageBuild, StageTest}
	if got := plan.Stages(); !equalStages(got, want) {
		t.Errorf("stages = %v, want %v", got, want)
	}
	if plan.Stack != "" {
		t.Errorf("stack = %q, want empty", plan.Stack)
	}
}

func TestResolverPlanBuildOnly(t *testing.T) {
	caps := fakeCaps{caps: map[layer1.Capability]string{layer1.CapBuild: "go build ./..."}}
	plan, err := NewResolver(caps).Plan()
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := plan.Stages(); !equalStages(got, []Stage{StageStructural, StageSyntax, StageBuild}) {
		t.Errorf("stages = %v", got)
	}
	if plan.HasStage(StageTest) {
		t.Error("test stage fabricated without the test capability")
	}
	if plan.HasStage(StageLint) {
		t.Error("lint stage fabricated without the lint capability")
	}
}

func TestResolverPlanStaticNoToolchain(t *testing.T) {
	caps := fakeCaps{caps: map[layer1.Capability]string{layer1.CapContainer: "docker build ."}}
	plan, err := NewResolver(caps).Plan()
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	want := []Stage{StageStructural, StageSyntax}
	if got := plan.Stages(); !equalStages(got, want) {
		t.Errorf("stages = %v, want %v", got, want)
	}
}

func TestResolverPlanNoCaps(t *testing.T) {
	_, err := NewResolver(nil).Plan()
	if !errors.Is(err, ErrNoCapabilities) {
		t.Errorf("err = %v, want ErrNoCapabilities", err)
	}
}

func TestResolverCommandFor(t *testing.T) {
	r := NewResolver(fullCaps())
	cmd, ok := r.CommandFor(StageBuild)
	if !ok || cmd != "go build ./..." {
		t.Errorf("CommandFor(build) = %q, %v", cmd, ok)
	}
	if _, ok := r.CommandFor(Stage("bogus")); ok {
		t.Error("bogus stage resolved a command")
	}
}

func TestResolverBuildDAG(t *testing.T) {
	r := NewResolver(fullCaps())
	dag, err := r.BuildDAG(func(stage Stage) (Validator, error) {
		return NewFuncValidator(string(stage), stage, func(context.Context, []Patch) (*ValidationResult, error) {
			return validResult(stage, "ok"), nil
		}), nil
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	order, err := dag.TopoSort()
	if err != nil {
		t.Fatalf("TopoSort: %v", err)
	}
	if got := idsOf(order); !equalStrings(got, []string{"structural", "syntax", "lint", "build", "test"}) {
		t.Errorf("order = %v", got)
	}
	// Dependency wiring: cheap stages are roots; lint gates on structural and
	// syntax; build gates on every cheaper stage; test gates on build.
	checks := map[string][]string{
		"structural": {},
		"syntax":     {},
		"lint":       {"structural", "syntax"},
		"build":      {"structural", "syntax", "lint"},
		"test":       {"build"},
	}
	for id, want := range checks {
		n, ok := dag.Node(id)
		if !ok {
			t.Fatalf("node %s missing", id)
		}
		if !equalStrings(n.DependsOn, want) {
			t.Errorf("node %s deps = %v, want %v", id, n.DependsOn, want)
		}
	}
}

func TestResolverBuildDAGWithoutLint(t *testing.T) {
	caps := fakeCaps{caps: map[layer1.Capability]string{layer1.CapBuild: "go build ./...", layer1.CapTest: "go test ./..."}}
	r := NewResolver(caps)
	dag, err := r.BuildDAG(func(stage Stage) (Validator, error) {
		return passValidator(string(stage)), nil
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	build, ok := dag.Node("build")
	if !ok {
		t.Fatal("build node missing")
	}
	// Without lint, build gates directly on the two cheap in-RAM stages.
	if !equalStrings(build.DependsOn, []string{"structural", "syntax"}) {
		t.Errorf("build deps = %v, want [structural syntax]", build.DependsOn)
	}
	test, ok := dag.Node("test")
	if !ok {
		t.Fatal("test node missing")
	}
	if !equalStrings(test.DependsOn, []string{"build"}) {
		t.Errorf("test deps = %v, want [build]", test.DependsOn)
	}
}

func TestPlanDrivenShortCircuitOnStructuralFailure(t *testing.T) {
	r := NewResolver(fullCaps())
	ran := make(map[string]*atomic.Int32)
	for _, id := range []string{"structural", "syntax", "lint", "build", "test"} {
		ran[id] = &atomic.Int32{}
	}
	dag, err := r.BuildDAG(func(stage Stage) (Validator, error) {
		return NewFuncValidator(string(stage), stage, func(context.Context, []Patch) (*ValidationResult, error) {
			ran[string(stage)].Add(1)
			if stage == StageStructural {
				return &ValidationResult{OK: false, Stage: stage, Summary: "structural broke"}, nil
			}
			return validResult(stage, string(stage)), nil
		}), nil
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	res, err := dag.Execute(context.Background(), nil)
	if err == nil || !errors.Is(err, ErrShortCircuited) {
		t.Fatalf("err = %v", err)
	}
	if ran["structural"].Load() != 1 {
		t.Error("structural must run once")
	}
	for _, id := range []string{"lint", "build", "test"} {
		if n := ran[id].Load(); n != 0 {
			t.Errorf("stage %s executed %d time(s); must be short-circuited", id, n)
		}
		if res.Nodes[id].Status != StatusSkipped {
			t.Errorf("stage %s status = %v, want skipped", id, res.Nodes[id].Status)
		}
	}
	if res.Nodes["structural"].Status != StatusFailed {
		t.Errorf("structural status = %v, want failed", res.Nodes["structural"].Status)
	}
}

func TestResolverBuildDAGMissingValidator(t *testing.T) {
	r := NewResolver(fullCaps())
	_, err := r.BuildDAG(func(stage Stage) (Validator, error) {
		if stage == StageBuild {
			return nil, nil
		}
		return NewFuncValidator(string(stage), stage, func(context.Context, []Patch) (*ValidationResult, error) {
			return validResult(stage, "ok"), nil
		}), nil
	})
	if !errors.Is(err, ErrNoValidator) {
		t.Errorf("err = %v, want ErrNoValidator", err)
	}
}

// ---------------------------------------------------------------------------
// DAG engine tests.
// ---------------------------------------------------------------------------

func passValidator(id string) *FuncValidator {
	return NewFuncValidator(id, StageStructural, func(context.Context, []Patch) (*ValidationResult, error) {
		return validResult(StageStructural, id), nil
	})
}

func failValidator(id string) *FuncValidator {
	return NewFuncValidator(id, StageStructural, func(context.Context, []Patch) (*ValidationResult, error) {
		return &ValidationResult{OK: false, Stage: StageStructural, Summary: id + " failed"}, nil
	})
}

// mustAdd registers a node, failing the test on construction errors.
func mustAdd(t *testing.T, dag *DAG, id string, stage Stage, v Validator, deps ...string) {
	t.Helper()
	if err := dag.AddNode(id, stage, v, deps...); err != nil {
		t.Fatalf("AddNode(%s): %v", id, err)
	}
}

func TestDAGLinearChain(t *testing.T) {
	dag := New()
	if err := dag.AddNode("a", StageStructural, passValidator("a")); err != nil {
		t.Fatal(err)
	}
	if err := dag.AddNode("b", StageSyntax, passValidator("b"), "a"); err != nil {
		t.Fatal(err)
	}
	if err := dag.AddNode("c", StageBuild, passValidator("c"), "b"); err != nil {
		t.Fatal(err)
	}
	res, err := dag.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.OK {
		t.Error("expected OK")
	}
	if got := strings.Join(res.Order, ","); got != "a,b,c" {
		t.Errorf("order = %s", got)
	}
	for _, id := range []string{"a", "b", "c"} {
		if res.Nodes[id].Status != StatusPassed {
			t.Errorf("node %s status = %v", id, res.Nodes[id].Status)
		}
	}
}

func TestDAGDiamondParallelLeaves(t *testing.T) {
	dag := New()
	if err := dag.AddNode("root", StageStructural, passValidator("root")); err != nil {
		t.Fatal(err)
	}
	if err := dag.AddNode("left", StageSyntax, passValidator("left"), "root"); err != nil {
		t.Fatal(err)
	}
	if err := dag.AddNode("right", StageLint, passValidator("right"), "root"); err != nil {
		t.Fatal(err)
	}
	if err := dag.AddNode("join", StageBuild, passValidator("join"), "left", "right"); err != nil {
		t.Fatal(err)
	}

	active := int32(0)
	maxActive := int32(0)
	track := func() {
		cur := atomic.AddInt32(&active, 1)
		for {
			prev := atomic.LoadInt32(&maxActive)
			if cur <= prev || atomic.CompareAndSwapInt32(&maxActive, prev, cur) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		atomic.AddInt32(&active, -1)
	}
	dag.nodes["left"].Validator = NewFuncValidator("left", StageSyntax, func(context.Context, []Patch) (*ValidationResult, error) {
		track()
		return validResult(StageSyntax, "left"), nil
	})
	dag.nodes["right"].Validator = NewFuncValidator("right", StageLint, func(context.Context, []Patch) (*ValidationResult, error) {
		track()
		return validResult(StageLint, "right"), nil
	})

	res, err := dag.ExecuteWithConcurrency(context.Background(), nil, 4)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.OK {
		t.Fatal("expected OK")
	}
	if maxActive < 2 {
		t.Errorf("maxActive = %d, want >= 2 (leaves did not run concurrently)", maxActive)
	}
	if got := strings.Join(res.Order, ","); got != "root,left,right,join" {
		t.Errorf("order = %s", got)
	}
}

func TestDAGEarlyShortCircuit(t *testing.T) {
	ran := make(map[string]*atomic.Int32)
	for _, id := range []string{"structural", "syntax", "build", "test"} {
		ran[id] = &atomic.Int32{}
	}
	dag := New()
	deps := map[string][]string{
		"structural": {},
		"syntax":     {"structural"},
		"build":      {"syntax"},
		"test":       {"build"},
	}
	for id, ds := range deps {
		mustAdd(t, dag, id, Stage(id), NewFuncValidator(id, Stage(id), func(context.Context, []Patch) (*ValidationResult, error) {
			ran[id].Add(1)
			if id == "structural" {
				return &ValidationResult{OK: false, Stage: StageStructural, Summary: "structural broke"}, nil
			}
			return validResult(Stage(id), id), nil
		}), ds...)
	}

	res, err := dag.Execute(context.Background(), nil)
	if err == nil {
		t.Fatal("expected short-circuit error")
	}
	if !errors.Is(err, ErrShortCircuited) {
		t.Errorf("err = %v, want ErrShortCircuited", err)
	}
	if !errors.Is(err, ErrValidationFailed) {
		t.Errorf("err = %v, want wrapped ErrValidationFailed", err)
	}
	if res.OK {
		t.Error("expected non-OK result")
	}
	if ran["structural"].Load() != 1 {
		t.Errorf("structural ran %d times", ran["structural"].Load())
	}
	for _, id := range []string{"syntax", "build", "test"} {
		if n := ran[id].Load(); n != 0 {
			t.Errorf("node %s executed %d time(s); must be short-circuited", id, n)
		}
		if res.Nodes[id].Status != StatusSkipped {
			t.Errorf("node %s status = %v, want skipped", id, res.Nodes[id].Status)
		}
	}
	if res.Nodes["structural"].Status != StatusFailed {
		t.Errorf("structural status = %v, want failed", res.Nodes["structural"].Status)
	}
}

func TestDAGFailureShortCircuitsRun(t *testing.T) {
	ran := make(map[string]*atomic.Int32)
	for _, id := range []string{"broken", "child", "sibling"} {
		ran[id] = &atomic.Int32{}
	}
	dag := New()
	mustAdd(t, dag, "broken", StageStructural, NewFuncValidator("broken", StageStructural, func(context.Context, []Patch) (*ValidationResult, error) {
		ran["broken"].Add(1)
		return &ValidationResult{OK: false, Stage: StageStructural, Summary: "broken"}, nil
	}))
	mustAdd(t, dag, "child", StageBuild, NewFuncValidator("child", StageBuild, func(context.Context, []Patch) (*ValidationResult, error) {
		ran["child"].Add(1)
		return validResult(StageBuild, "child"), nil
	}), "broken")
	// sibling is an independent root: it may complete before the failure
	// cancellation lands (it is never skipped by dependency, only by
	// cancellation), so only its pass/skip invariant is asserted.
	mustAdd(t, dag, "sibling", StageSyntax, NewFuncValidator("sibling", StageSyntax, func(context.Context, []Patch) (*ValidationResult, error) {
		ran["sibling"].Add(1)
		return validResult(StageSyntax, "sibling"), nil
	}))

	res, err := dag.ExecuteWithConcurrency(context.Background(), nil, 2)
	if err == nil || !errors.Is(err, ErrShortCircuited) {
		t.Fatalf("err = %v", err)
	}
	if ran["broken"].Load() != 1 {
		t.Error("failing node must run exactly once")
	}
	if ran["child"].Load() != 0 {
		t.Error("dependent of a failed node must never run")
	}
	if res.Nodes["broken"].Status != StatusFailed {
		t.Errorf("broken = %v, want failed", res.Nodes["broken"].Status)
	}
	if res.Nodes["child"].Status != StatusSkipped {
		t.Errorf("child = %v, want skipped", res.Nodes["child"].Status)
	}
	if st := res.Nodes["sibling"].Status; st != StatusPassed && st != StatusSkipped {
		t.Errorf("sibling = %v, want passed or skipped", st)
	}
	if res.OK {
		t.Error("run must not report OK")
	}
}

func TestDAGValidatorErrorFailsRun(t *testing.T) {
	dag := New()
	mustAdd(t, dag, "boom", StageStructural, NewFuncValidator("boom", StageStructural, func(context.Context, []Patch) (*ValidationResult, error) {
		return nil, errors.New("exploded")
	}))
	mustAdd(t, dag, "dependent", StageBuild, passValidator("dependent"), "boom")
	_, err := dag.Execute(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrValidationFailed) {
		t.Errorf("err = %v, want ErrValidationFailed", err)
	}
}

func TestDAGCycle(t *testing.T) {
	dag := New()
	mustAdd(t, dag, "a", StageStructural, passValidator("a"), "b")
	mustAdd(t, dag, "b", StageSyntax, passValidator("b"), "a")
	_, err := dag.TopoSort()
	if !errors.Is(err, ErrCycleDetected) {
		t.Errorf("err = %v, want ErrCycleDetected", err)
	}
	if _, err := dag.Execute(context.Background(), nil); !errors.Is(err, ErrCycleDetected) {
		t.Errorf("Execute err = %v", err)
	}
}

func TestDAGUnknownDependency(t *testing.T) {
	dag := New()
	mustAdd(t, dag, "a", StageStructural, passValidator("a"))
	mustAdd(t, dag, "b", StageSyntax, passValidator("b"), "missing")
	_, err := dag.TopoSort()
	if !errors.Is(err, ErrUnknownDependency) {
		t.Errorf("err = %v, want ErrUnknownDependency", err)
	}
}

func TestDAGDuplicateNode(t *testing.T) {
	dag := New()
	if err := dag.AddNode("a", StageStructural, passValidator("a")); err != nil {
		t.Fatal(err)
	}
	if err := dag.AddNode("a", StageSyntax, passValidator("a2")); !errors.Is(err, ErrDuplicateNode) {
		t.Errorf("err = %v, want ErrDuplicateNode", err)
	}
	if err := dag.AddNode("", StageStructural, passValidator("x")); !errors.Is(err, ErrDuplicateNode) {
		t.Errorf("empty id err = %v", err)
	}
	if err := dag.AddNode("n", StageStructural, nil); !errors.Is(err, ErrNoValidator) {
		t.Errorf("nil validator err = %v", err)
	}
}

func TestDAGEmpty(t *testing.T) {
	dag := New()
	_, err := dag.Execute(context.Background(), nil)
	if !errors.Is(err, ErrEmptyDAG) {
		t.Errorf("err = %v, want ErrEmptyDAG", err)
	}
}

func TestDAGContextCancellation(t *testing.T) {
	dag := New()
	started := make(chan struct{})
	release := make(chan struct{})
	mustAdd(t, dag, "slow", StageStructural, NewFuncValidator("slow", StageStructural, func(ctx context.Context, _ []Patch) (*ValidationResult, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return validResult(StageStructural, "slow"), nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = dag.Execute(ctx, nil)
	}()
	<-started
	cancel()
	<-done
}

func TestDAGConcurrentExecutionsRace(t *testing.T) {
	dag := New()
	mustAdd(t, dag, "structural", StageStructural, passValidator("structural"))
	mustAdd(t, dag, "syntax", StageSyntax, passValidator("syntax"), "structural")
	mustAdd(t, dag, "lint", StageLint, passValidator("lint"), "syntax")
	mustAdd(t, dag, "build", StageBuild, passValidator("build"), "lint")
	mustAdd(t, dag, "test", StageTest, passValidator("test"), "build")
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := dag.ExecuteWithConcurrency(context.Background(), nil, 4)
			if err != nil {
				t.Errorf("Execute: %v", err)
				return
			}
			if !res.OK {
				t.Error("expected OK")
			}
		}()
	}
	wg.Wait()
}

func TestDAGResultPassedFailedSkipped(t *testing.T) {
	dag := New()
	mustAdd(t, dag, "a", StageStructural, failValidator("a"))
	mustAdd(t, dag, "b", StageSyntax, passValidator("b"), "a")
	res, _ := dag.Execute(context.Background(), nil)
	if len(res.Failed()) != 1 || res.Failed()[0] != "a" {
		t.Errorf("Failed = %v", res.Failed())
	}
	if len(res.Skipped()) != 1 || res.Skipped()[0] != "b" {
		t.Errorf("Skipped = %v", res.Skipped())
	}
	if len(res.Passed()) != 0 {
		t.Errorf("Passed = %v", res.Passed())
	}
	if len(res.Cancelled) != 1 || res.Cancelled[0] != "b" {
		t.Errorf("Cancelled = %v", res.Cancelled)
	}
}

// ---------------------------------------------------------------------------
// Structural validator tests.
// ---------------------------------------------------------------------------

func mustFailStructural(t *testing.T, v *StructuralValidator, patches []Patch, substr string) *ValidationResult {
	t.Helper()
	res, err := v.Validate(context.Background(), patches)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if res.OK {
		t.Fatal("expected structural failure")
	}
	if substr != "" && !strings.Contains(res.Summary, substr) {
		t.Errorf("summary = %q, want contains %q", res.Summary, substr)
	}
	return res
}

func mustPassStructural(t *testing.T, v *StructuralValidator, patches []Patch) {
	t.Helper()
	res, err := v.Validate(context.Background(), patches)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !res.OK {
		t.Fatalf("expected pass, got %q", res.Summary)
	}
}

func TestStructuralValidEdit(t *testing.T) {
	sor, _ := newTestSor(t, structuralFixture())
	v := NewStructuralValidator(sor)
	mustPassStructural(t, v, []Patch{
		patch("svc/service.go", "", `package svc

// Compute doubles n when positive.
func Compute(n int) int {
	return n * 4
}
`),
	})
}

func TestStructuralSyntaxError(t *testing.T) {
	sor, _ := newTestSor(t, structuralFixture())
	v := NewStructuralValidator(sor)
	res := mustFailStructural(t, v, []Patch{
		patch("svc/service.go", "", "package svc\n\nfunc Compute( {{{{ \n"),
	}, "syntax error")
	if !strings.HasPrefix(res.Summary, "svc/service.go:") {
		t.Errorf("location = %q, want svc/service.go:line:col", res.Summary)
	}
}

func TestStructuralBrokenImport(t *testing.T) {
	sor, _ := newTestSor(t, structuralFixture())
	v := NewStructuralValidator(sor)
	newMain := `package main

import (
	"fmt"

	"github.com/example/val/ghost"
	"github.com/example/val/svc"
)

func main() {
	_ = svc.Compute(2)
	fmt.Println(ghost.Missing())
}
`
	res := mustFailStructural(t, v, []Patch{
		patch("cmd/app/main.go", "", newMain),
	}, "broken import")
	if !strings.HasPrefix(res.Summary, "cmd/app/main.go:") {
		t.Errorf("location = %q", res.Summary)
	}
}

func TestStructuralDeleteImportedFile(t *testing.T) {
	sor, _ := newTestSor(t, structuralFixture())
	v := NewStructuralValidator(sor)
	res := mustFailStructural(t, v, []Patch{
		{Path: "svc/service.go", New: "", Changed: true},
	}, "broken import")
	// Both cmd/app/main.go and api/handler.go import the deleted package; the
	// check reports the first importer in deterministic (sorted) order.
	if !strings.HasPrefix(res.Summary, "cmd/app/main.go:") && !strings.HasPrefix(res.Summary, "api/handler.go:") {
		t.Errorf("location = %q, want an importer location", res.Summary)
	}
}

func TestStructuralDeleteUnreferencedFile(t *testing.T) {
	sor, _ := newTestSor(t, structuralFixture())
	v := NewStructuralValidator(sor)
	mustPassStructural(t, v, []Patch{
		{Path: "api/handler.go", New: "", Changed: true},
	})
}

func TestStructuralDeleteWithAllImportersFixed(t *testing.T) {
	sor, _ := newTestSor(t, structuralFixture())
	v := NewStructuralValidator(sor)
	mustPassStructural(t, v, []Patch{
		{Path: "svc/service.go", New: "", Changed: true},
		patch("cmd/app/main.go", "", `package main

import "fmt"

func main() {
	fmt.Println("ok")
}
`),
		patch("api/handler.go", "", `package api

func Handle() int {
	return 0
}
`),
	})
}

func TestStructuralNewPackageImportResolves(t *testing.T) {
	sor, _ := newTestSor(t, structuralFixture())
	v := NewStructuralValidator(sor)
	mustPassStructural(t, v, []Patch{
		patch("metrics/collector.go", "", `package metrics

// Collect returns a metric value.
func Collect() int {
	return 7
}
`),
		patch("api/handler.go", "", `package api

import "github.com/example/val/metrics"

func Handle() int {
	return metrics.Collect()
}
`),
	})
}

func TestStructuralDanglingRename(t *testing.T) {
	sor, _ := newTestSor(t, structuralFixture())
	v := NewStructuralValidator(sor)
	renamed := `package svc

import "fmt"

type Service struct{}

func (s *Service) Run() error {
	fmt.Println("run")
	return nil
}

func ComputeAll(n int) int {
	if n < 0 {
		return 0
	}
	return n * 2
}
`
	res := mustFailStructural(t, v, []Patch{
		patch("svc/service.go", "", renamed),
	}, "dangling reference")
	if !strings.HasPrefix(res.Summary, "cmd/app/main.go:") && !strings.HasPrefix(res.Summary, "api/handler.go:") {
		t.Errorf("location = %q, want caller location", res.Summary)
	}
}

func TestStructuralRenameWithAllCallersPatched(t *testing.T) {
	sor, _ := newTestSor(t, structuralFixture())
	v := NewStructuralValidator(sor)
	mustPassStructural(t, v, []Patch{
		patch("svc/service.go", "", `package svc

import "fmt"

type Service struct{}

func (s *Service) Run() error {
	fmt.Println("run")
	return nil
}

func ComputeAll(n int) int {
	if n < 0 {
		return 0
	}
	return n * 2
}
`),
		patch("cmd/app/main.go", "", `package main

import (
	"fmt"

	"github.com/example/val/svc"
)

func main() {
	s := &svc.Service{}
	_ = s.Run()
	_ = svc.ComputeAll(2)
	fmt.Println("ok")
}
`),
		patch("api/handler.go", "", `package api

import "github.com/example/val/svc"

func Handle() int {
	return svc.ComputeAll(3)
}
`),
	})
}

func TestStructuralMissingQualifiedReference(t *testing.T) {
	sor, _ := newTestSor(t, structuralFixture())
	v := NewStructuralValidator(sor)
	newMain := `package main

import (
	"fmt"

	"github.com/example/val/svc"
)

func main() {
	_ = svc.Nonexistent(2)
	fmt.Println("ok")
}
`
	res := mustFailStructural(t, v, []Patch{
		patch("cmd/app/main.go", "", newMain),
	}, "missing symbol")
	if !strings.HasPrefix(res.Summary, "cmd/app/main.go:") {
		t.Errorf("location = %q", res.Summary)
	}
}

func TestStructuralEmptyPath(t *testing.T) {
	sor, _ := newTestSor(t, structuralFixture())
	v := NewStructuralValidator(sor)
	res, err := v.Validate(context.Background(), []Patch{{Path: "", New: "x", Changed: true}})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if res.OK {
		t.Error("empty path accepted")
	}
}

func TestStructuralPathEscape(t *testing.T) {
	sor, _ := newTestSor(t, structuralFixture())
	v := NewStructuralValidator(sor)
	res, err := v.Validate(context.Background(), []Patch{{Path: "../escape.go", New: "package x", Changed: true}})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if res.OK {
		t.Error("escaping path accepted")
	}
}

func TestStructuralNilSor(t *testing.T) {
	v := NewStructuralValidator(nil)
	_, err := v.Validate(context.Background(), nil)
	if !errors.Is(err, ErrNoSor) {
		t.Errorf("err = %v, want ErrNoSor", err)
	}
}

func TestStructuralNoPatches(t *testing.T) {
	sor, _ := newTestSor(t, structuralFixture())
	v := NewStructuralValidator(sor)
	res, err := v.Validate(context.Background(), nil)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !res.OK {
		t.Errorf("no-op validation must pass, got %q", res.Summary)
	}
}

// ---------------------------------------------------------------------------
// Helper unit tests.
// ---------------------------------------------------------------------------

func TestParseModuleName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"module github.com/example/val\n", "github.com/example/val"},
		{"\nmodule github.com/a/b // comment\n", "github.com/a/b"},
		{"// module not-module\nmodule github.com/x/y\n", "github.com/x/y"},
		{"no module here\n", ""},
	}
	for _, c := range cases {
		if got := parseModuleName([]byte(c.in)); got != c.want {
			t.Errorf("parseModuleName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestGoParseError(t *testing.T) {
	if loc := goParseError("x.go", []byte("package x\n\nfunc F() {}\n")); loc != "" {
		t.Errorf("valid source reported error at %q", loc)
	}
	loc := goParseError("x.go", []byte("package x\n\nfunc F( {\n"))
	if loc == "" || !strings.HasPrefix(loc, "x.go:") {
		t.Errorf("invalid source location = %q", loc)
	}
}

func TestResolvePkgDir(t *testing.T) {
	dirs := []string{"api", "cmd/app", "svc"}
	if got := resolvePkgDir("github.com/example/val/svc", dirs); got != "svc" {
		t.Errorf("svc resolve = %q", got)
	}
	if got := resolvePkgDir("svc", dirs); got != "svc" {
		t.Errorf("exact resolve = %q", got)
	}
	if got := resolvePkgDir("github.com/example/val/ghost", dirs); got != "" {
		t.Errorf("ghost resolve = %q, want empty", got)
	}
	if got := resolvePkgDir("fmt", dirs); got != "" {
		t.Errorf("fmt resolve = %q, want empty", got)
	}
}

func TestDeletedNames(t *testing.T) {
	oldSrc := "package p\n\nfunc A() {}\nfunc B() {}\ntype T struct{}\n"
	newSrc := "package p\n\nfunc B() {}\ntype U struct{}\n"
	got := deletedNames(oldSrc, newSrc)
	if !got["A"] || !got["T"] || got["B"] || got["U"] {
		t.Errorf("deletedNames = %v", got)
	}
}

func TestDefaultImportName(t *testing.T) {
	cases := map[string]string{
		"github.com/example/val/svc": "svc",
		"github.com/foo/go-bar":      "bar",
		"encoding/json":              "json",
	}
	for in, want := range cases {
		if got := defaultImportName(in); got != want {
			t.Errorf("defaultImportName(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Validator tests.
// ---------------------------------------------------------------------------

func TestSyntaxValidator(t *testing.T) {
	root := t.TempDir()
	v := NewSyntaxValidator(root)
	res, err := v.Validate(context.Background(), []Patch{{
		Path: "svc/x.go", New: "package svc\n\nfunc X() {}\n", Changed: true,
	}})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !res.OK {
		t.Errorf("valid Go rejected: %q", res.Summary)
	}

	res, err = v.Validate(context.Background(), []Patch{{Path: "svc/bad.go", New: "not valid go {{{", Changed: true}})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if res.OK {
		t.Error("invalid Go accepted")
	}
	if !strings.HasPrefix(res.Location, "svc/bad.go:") {
		t.Errorf("location = %q", res.Location)
	}
}

func TestSyntaxValidatorNonGo(t *testing.T) {
	v := NewSyntaxValidator(t.TempDir())
	res, err := v.Validate(context.Background(), []Patch{{Path: "web/app.ts", New: "let x: =", Changed: true}})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !res.OK {
		t.Errorf("non-Go file must be skipped, got %q", res.Summary)
	}
}

func TestCommandValidator(t *testing.T) {
	root := t.TempDir()
	ok := NewCommandValidator(StageBuild, "sh -c \"echo building; exit 0\"", root)
	res, err := ok.Validate(context.Background(), nil)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !res.OK || res.ExitCode != 0 {
		t.Errorf("res = %+v", res)
	}
	if !strings.Contains(res.Stdout, "building") {
		t.Errorf("stdout = %q", res.Stdout)
	}

	fail := NewCommandValidator(StageTest, "sh -c \"echo boom >&2; exit 3\"", root)
	res, err = fail.Validate(context.Background(), nil)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if res.OK || res.ExitCode != 3 {
		t.Errorf("res = %+v", res)
	}
	if !strings.Contains(res.Stderr, "boom") {
		t.Errorf("stderr = %q", res.Stderr)
	}
	if res.Stage != StageTest {
		t.Errorf("stage = %v", res.Stage)
	}
}

func TestCommandValidatorEmpty(t *testing.T) {
	v := NewCommandValidator(StageBuild, "", t.TempDir())
	_, err := v.Validate(context.Background(), nil)
	if !errors.Is(err, ErrEmptyCommand) {
		t.Errorf("err = %v, want ErrEmptyCommand", err)
	}
}

func TestCommandValidatorMissingBinary(t *testing.T) {
	v := NewCommandValidator(StageBuild, "definitely-not-a-real-binary-xyz", t.TempDir())
	res, err := v.Validate(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	if !errors.Is(err, ErrValidationFailed) {
		t.Errorf("err = %v, want ErrValidationFailed", err)
	}
	if res != nil {
		t.Errorf("res = %+v", res)
	}
}

func TestCapabilityValidators(t *testing.T) {
	caps := fullCaps()
	root := t.TempDir()

	bv, err := BuildValidator(caps, root)
	if err != nil {
		t.Fatalf("BuildValidator: %v", err)
	}
	if bv.Cmd != "go build ./..." {
		t.Errorf("build cmd = %q", bv.Cmd)
	}
	tv, err := TestValidator(caps, root)
	if err != nil {
		t.Fatalf("TestValidator: %v", err)
	}
	if tv.Cmd != "go test ./..." {
		t.Errorf("test cmd = %q", tv.Cmd)
	}
	lv, err := LintValidator(caps, root)
	if err != nil {
		t.Fatalf("LintValidator: %v", err)
	}
	if lv.Cmd != "go vet ./..." {
		t.Errorf("lint cmd = %q", lv.Cmd)
	}

	if _, err := BuildValidator(fakeCaps{}, root); !errors.Is(err, ErrUnsupportedCapability) {
		t.Errorf("unsupported err = %v", err)
	}
}

func TestFuncValidatorNilFn(t *testing.T) {
	v := NewFuncValidator("nil", StageStructural, nil)
	_, err := v.Validate(context.Background(), nil)
	if !errors.Is(err, ErrNoValidator) {
		t.Errorf("err = %v, want ErrNoValidator", err)
	}
}

func TestValidationResultWithStage(t *testing.T) {
	r := &ValidationResult{OK: true, ExitCode: 0}
	got := r.WithStage(StageBuild)
	if got.Stage != StageBuild {
		t.Errorf("stage = %v", got.Stage)
	}
	if got == r {
		t.Error("WithStage must return a copy")
	}
	if r.Stage != "" {
		t.Error("original mutated")
	}
}

func TestStageCostsOrdering(t *testing.T) {
	if StageStructural.Cost() >= StageSyntax.Cost() {
		t.Error("structural must be cheaper than syntax")
	}
	if StageSyntax.Cost() >= StageLint.Cost() {
		t.Error("syntax must be cheaper than lint")
	}
	if StageLint.Cost() >= StageBuild.Cost() {
		t.Error("lint must be cheaper than build")
	}
	if StageBuild.Cost() >= StageTest.Cost() {
		t.Error("build must be cheaper than test")
	}
	if !StageStructural.Cheap() || !StageSyntax.Cheap() {
		t.Error("structural/syntax must be cheap")
	}
	if StageBuild.Cheap() || StageTest.Cheap() {
		t.Error("build/test must not be cheap")
	}
}

func TestResolverWithStack(t *testing.T) {
	r := NewResolver(fullCaps(), WithStack(layer1.StackGo))
	plan, err := r.Plan()
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Stack != layer1.StackGo {
		t.Errorf("stack = %q", plan.Stack)
	}
}

func equalStages(a, b []Stage) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestStructuralConcurrent(t *testing.T) {
	sor, _ := newTestSor(t, structuralFixture())
	v := NewStructuralValidator(sor)
	valid := []Patch{patch("svc/service.go", "", `package svc

func Compute(n int) int {
	return n * 4
}
`)}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if res, err := v.Validate(context.Background(), valid); err != nil || !res.OK {
				t.Errorf("Validate: %v %v", err, res)
			}
		}()
	}
	wg.Wait()
}

func TestPlanExecuteEndToEnd(t *testing.T) {
	calls := &atomic.Int32{}
	allCaps := fullCaps()
	r := NewResolver(allCaps, WithStack(layer1.StackGo))
	validatorFor := func(stage Stage) (Validator, error) {
		if stage == StageLint || stage == StageBuild || stage == StageTest {
			return NewCommandValidator(stage, "sh -c \"echo "+string(stage)+"; exit 0\"", t.TempDir()), nil
		}
		return NewFuncValidator(string(stage), stage, func(context.Context, []Patch) (*ValidationResult, error) {
			calls.Add(1)
			return validResult(stage, string(stage)), nil
		}), nil
	}
	dag, err := r.BuildDAG(validatorFor)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	res, err := dag.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.OK {
		t.Fatalf("result not OK: %v", res.Err)
	}
	if calls.Load() != 2 {
		t.Errorf("in-RAM stages called %d times, want 2", calls.Load())
	}
	for _, id := range []string{"structural", "syntax", "lint", "build", "test"} {
		if res.Nodes[id].Status != StatusPassed {
			t.Errorf("node %s status = %v", id, res.Nodes[id].Status)
		}
	}
}
