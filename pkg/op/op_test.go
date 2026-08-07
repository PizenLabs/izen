package op

import (
	"testing"
	"time"

	"github.com/PizenLabs/izen/pkg/ir"
	"github.com/PizenLabs/izen/pkg/resource"
	"github.com/PizenLabs/izen/pkg/resource/file"
	"github.com/PizenLabs/izen/pkg/resource/git"
	"github.com/PizenLabs/izen/pkg/resource/terminal"
)

func TestOpTypeValues(t *testing.T) {
	cases := []struct {
		typ  OpType
		want string
	}{
		{OpWriteFile, "op.file.write"},
		{OpDeleteFile, "op.file.delete"},
		{OpRunCommand, "op.exec.cmd"},
		{OpGitCommit, "op.git.commit"},
	}
	for _, c := range cases {
		if !c.typ.Valid() {
			t.Errorf("%q: expected valid", c.want)
		}
		if got := c.typ.String(); got != c.want {
			t.Errorf("%q.String() = %q, want %q", c.want, got, c.want)
		}
	}
	if OpType("op.unknown").Valid() {
		t.Error("expected unknown operation type to be invalid")
	}
}

// newTargets returns one resource per kind for building test operations.
func newTargets(t *testing.T) (resource.Resource, resource.Resource, resource.Resource) {
	t.Helper()
	fr, err := file.NewFileResource(t.TempDir(), "a.go", 0)
	if err != nil {
		t.Fatalf("NewFileResource: %v", err)
	}
	tr, err := terminal.NewTerminalResource(t.TempDir(), nil, "")
	if err != nil {
		t.Fatalf("NewTerminalResource: %v", err)
	}
	gr, err := git.NewGitResource(t.TempDir())
	if err != nil {
		t.Fatalf("NewGitResource: %v", err)
	}
	return fr, tr, gr
}

func TestNewOperationValid(t *testing.T) {
	fileTarget, termTarget, gitTarget := newTargets(t)
	artifact := ir.NewFile("a.go", []byte("package a"))
	cases := []struct {
		name    string
		typ     OpType
		target  resource.Resource
		payload any
	}{
		{"write file", OpWriteFile, fileTarget, artifact},
		{"delete file", OpDeleteFile, fileTarget, nil},
		{"run command", OpRunCommand, termTarget, "echo hi"},
		{"git commit", OpGitCommit, gitTarget, "initial commit"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o, err := NewOperation("id-1", c.typ, c.target, c.payload, []string{"dep-1"}, 5*time.Second)
			if err != nil {
				t.Fatalf("NewOperation: %v", err)
			}
			if o.ID != "id-1" {
				t.Fatalf("unexpected ID %q", o.ID)
			}
			if o.Type != c.typ {
				t.Fatalf("unexpected type %s", o.Type)
			}
			if o.TargetResource != c.target {
				t.Fatalf("target not preserved")
			}
			if o.Timeout != 5*time.Second {
				t.Fatalf("unexpected timeout %v", o.Timeout)
			}
			if len(o.Preconditions) != 1 || o.Preconditions[0] != "dep-1" {
				t.Fatalf("unexpected preconditions %v", o.Preconditions)
			}
			if err := o.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

func TestNewOperationRejectsInvalid(t *testing.T) {
	fileTarget, termTarget, gitTarget := newTargets(t)
	artifact := ir.NewFile("a.go", []byte("package a"))
	cases := []struct {
		name  string
		build func() (Operation, error)
	}{
		{
			"empty ID",
			func() (Operation, error) {
				return NewOperation("", OpWriteFile, fileTarget, artifact, nil, 0)
			},
		},
		{
			"unknown type",
			func() (Operation, error) {
				return NewOperation("id-1", OpType("op.unknown"), fileTarget, artifact, nil, 0)
			},
		},
		{
			"nil target",
			func() (Operation, error) {
				return NewOperation("id-1", OpWriteFile, nil, artifact, nil, 0)
			},
		},
		{
			"kind mismatch",
			func() (Operation, error) {
				return NewOperation("id-1", OpRunCommand, fileTarget, "echo hi", nil, 0)
			},
		},
		{
			"write wrong payload",
			func() (Operation, error) {
				return NewOperation("id-1", OpWriteFile, fileTarget, "not an artifact", nil, 0)
			},
		},
		{
			"run wrong payload",
			func() (Operation, error) {
				return NewOperation("id-1", OpRunCommand, termTarget, 42, nil, 0)
			},
		},
		{
			"commit wrong payload",
			func() (Operation, error) {
				return NewOperation("id-1", OpGitCommit, gitTarget, 42, nil, 0)
			},
		},
		{
			"self dependency",
			func() (Operation, error) {
				return NewOperation("op-self", OpWriteFile, fileTarget, artifact, []string{"op-self"}, 0)
			},
		},
		{
			"empty precondition",
			func() (Operation, error) {
				return NewOperation("id-1", OpWriteFile, fileTarget, artifact, []string{""}, 0)
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := c.build(); err == nil {
				t.Fatalf("expected validation error")
			}
		})
	}
}

func TestOperationPreconditionsDefensiveCopy(t *testing.T) {
	fileTarget, _, _ := newTargets(t)
	artifact := ir.NewFile("a.go", []byte("package a"))
	pre := []string{"dep-1", "dep-2"}
	o, err := NewOperation("id-1", OpWriteFile, fileTarget, artifact, pre, 0)
	if err != nil {
		t.Fatalf("NewOperation: %v", err)
	}
	pre[0] = "mutated"
	if o.Preconditions[0] != "dep-1" {
		t.Fatalf("expected defensive copy, got %v", o.Preconditions)
	}
}
