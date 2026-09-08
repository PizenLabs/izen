package plan

import (
	"path/filepath"
	"testing"
)

func TestLowererMapsFileKinds(t *testing.T) {
	ws := t.TempDir()
	steps := []Step{
		NewStep(StepCreate, "cmd/main.go", WithID("s1")),
		NewStep(StepModify, "internal/app.go", WithID("s2")),
		NewStep(StepRead, "README.md", WithID("s3")),
		NewStep(StepDelete, "old.txt", WithID("s4")),
	}
	vp := validateOf(t, steps...)
	ep, err := NewPlanLowerer(ws).Lower(vp)
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}
	es := ep.Steps()
	want := []struct {
		cmd   string
		res   string
		shell bool
	}{
		{"write-file", filepath.Join(ws, "cmd", "main.go"), false},
		{"write-file", filepath.Join(ws, "internal", "app.go"), false},
		{"read-file", filepath.Join(ws, "README.md"), false},
		{"remove-file", filepath.Join(ws, "old.txt"), false},
	}
	for i, w := range want {
		if es[i].Command() != w.cmd {
			t.Errorf("step %d command = %q, want %q", i, es[i].Command(), w.cmd)
		}
		if es[i].ResolvedTarget() != w.res {
			t.Errorf("step %d resolved = %q, want %q", i, es[i].ResolvedTarget(), w.res)
		}
		if es[i].Shell() != w.shell {
			t.Errorf("step %d shell = %v", i, es[i].Shell())
		}
		if es[i].WorkDir() != ws {
			t.Errorf("step %d workdir = %q", i, es[i].WorkDir())
		}
	}
}

func TestLowererMapsRunAndVerify(t *testing.T) {
	ws := t.TempDir()
	vp := validateOf(t,
		NewStep(StepRun, "go vet ./...", WithID("s1")),
		NewStep(StepVerify, "verify", WithID("s2")),
	)
	ep, err := NewPlanLowerer(ws).Lower(vp)
	if err != nil {
		t.Fatal(err)
	}
	es := ep.Steps()
	if es[0].Command() != "go vet ./..." || !es[0].Shell() {
		t.Fatalf("run step = %+v", es[0])
	}
	if es[1].Command() != "verify" || es[1].Shell() {
		t.Fatalf("verify step = %+v", es[1])
	}
	if !es[1].Verify() {
		t.Fatal("verify step should report Verify()=true")
	}
}

func TestLowererRejectsPathEscape(t *testing.T) {
	ws := t.TempDir()
	vp := validateOf(t, NewStep(StepCreate, "../../evil.go", WithID("s1")))
	if _, err := NewPlanLowerer(ws).Lower(vp); err == nil {
		t.Fatal("escaping target must be rejected")
	}
}

func TestLowererRejectsInvalidPlan(t *testing.T) {
	ws := t.TempDir()
	vp, _ := NewPlanValidator().Validate(normalizedFrom(t,
		NewStep(StepKind("explode"), "a.go", WithID("s1")),
	))
	if _, err := NewPlanLowerer(ws).Lower(vp); err == nil {
		t.Fatal("invalid plan must not lower")
	}
	if _, err := NewPlanLowerer(ws).Lower(nil); err == nil {
		t.Fatal("nil plan must error")
	}
}

func TestLowererInheritsEnv(t *testing.T) {
	ws := t.TempDir()
	vp := validateOf(t, NewStep(StepCreate, "a.go", WithID("s1")))
	ep, err := NewPlanLowerer(ws, WithLowererEnv(map[string]string{"FOO": "bar"})).Lower(vp)
	if err != nil {
		t.Fatal(err)
	}
	if ep.Env()["FOO"] != "bar" {
		t.Fatal("plan env missing seeded value")
	}
	if got := ep.Steps()[0].Env(); got["FOO"] != "bar" {
		t.Fatal("step env missing seeded value")
	}
}

func TestLowererDoesNotMutateInput(t *testing.T) {
	ws := t.TempDir()
	steps := []Step{NewStep(StepCreate, "a.go", WithID("s1"))}
	vp := validateOf(t, steps...)
	before := vp.Steps()
	if _, err := NewPlanLowerer(ws).Lower(vp); err != nil {
		t.Fatal(err)
	}
	after := vp.Steps()
	if !sameStepOrder(before, after) {
		t.Fatal("lowerer mutated its input")
	}
}

func TestLowererAbsoluteWorkDir(t *testing.T) {
	// NewPlanLowerer must absolutise the working directory.
	l := NewPlanLowerer("relative/dir")
	if !filepath.IsAbs(l.WorkDir()) {
		t.Fatalf("workdir = %q, want absolute", l.WorkDir())
	}
}
