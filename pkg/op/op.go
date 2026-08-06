// Package op defines the Declarative Operation IR of the Izen Agent Runtime
// V3. An Operation bridges an ir.Artifact and a resource.Resource into a
// single declarative unit: it names a target resource, a typed payload and a
// set of preconditions, and never mutates a target directly.
//
// The package is deliberately free of any AI, LLM or prompt dependencies and
// performs no I/O; execution of operations is delegated to pkg/graph.
package op

import (
	"errors"
	"fmt"
	"time"

	"github.com/PizenLabs/izen/pkg/ir"
	"github.com/PizenLabs/izen/pkg/resource"
)

// OpType discriminates the kind of a declarative Operation.
type OpType string

// Supported operation types.
const (
	// OpWriteFile writes an ir.Artifact payload onto a file resource.
	OpWriteFile OpType = "op.file.write"
	// OpDeleteFile removes the file wrapped by a file resource.
	OpDeleteFile OpType = "op.file.delete"
	// OpRunCommand executes a string command on a terminal resource.
	OpRunCommand OpType = "op.exec.cmd"
	// OpGitCommit creates a commit with a string message on a git resource.
	OpGitCommit OpType = "op.git.commit"
)

// Valid reports whether t is one of the canonical operation types.
func (t OpType) Valid() bool {
	switch t {
	case OpWriteFile, OpDeleteFile, OpRunCommand, OpGitCommit:
		return true
	default:
		return false
	}
}

// String returns the machine-readable operation type label.
func (t OpType) String() string { return string(t) }

// requiredKind returns the resource kind an operation of type t must target.
func (t OpType) requiredKind() (resource.ResourceKind, bool) {
	switch t {
	case OpWriteFile, OpDeleteFile:
		return resource.KindFile, true
	case OpRunCommand:
		return resource.KindTerminal, true
	case OpGitCommit:
		return resource.KindGitRepo, true
	default:
		return "", false
	}
}

// Operation is a declarative, target-resource-bound unit of work. It holds a
// resource.Resource target and a typed payload but never performs I/O itself;
// the target is mutated exclusively through the Resource abstraction.
type Operation struct {
	// ID uniquely identifies the operation within an execution graph.
	ID string
	// Type discriminates the operation kind.
	Type OpType
	// TargetResource is the resource the operation acts upon.
	TargetResource resource.Resource
	// Payload carries the operation payload: an ir.Artifact for write
	// operations, a command string for exec operations, a commit message
	// string for git commits, and nothing for deletes.
	Payload any
	// Preconditions lists the IDs of operations that must complete first.
	Preconditions []string
	// Timeout bounds the operation execution; zero disables the bound.
	Timeout time.Duration
}

// NewOperation constructs and validates an Operation. The preconditions slice
// is copied defensively.
func NewOperation(id string, typ OpType, target resource.Resource, payload any, preconditions []string, timeout time.Duration) (Operation, error) {
	o := Operation{
		ID:             id,
		Type:           typ,
		TargetResource: target,
		Payload:        payload,
		Preconditions:  append([]string(nil), preconditions...),
		Timeout:        timeout,
	}
	if err := o.Validate(); err != nil {
		return Operation{}, err
	}
	return o, nil
}

// Validate checks the operation is well-formed: a non-empty ID, a supported
// type, a non-nil target of the matching resource kind, and a payload typed
// for the operation kind. It also rejects self-referencing or empty
// preconditions.
func (o Operation) Validate() error {
	if o.ID == "" {
		return errors.New("op: operation ID is required")
	}
	if !o.Type.Valid() {
		return fmt.Errorf("op: unsupported operation type %q", o.Type)
	}
	if o.TargetResource == nil {
		return errors.New("op: operation must target a resource")
	}
	if kind, ok := o.Type.requiredKind(); ok && o.TargetResource.Kind() != kind {
		return fmt.Errorf("op: operation %s requires a %s target, got %s", o.Type, kind, o.TargetResource.Kind())
	}
	if err := o.validatePayload(); err != nil {
		return err
	}
	for _, pre := range o.Preconditions {
		if pre == "" {
			return errors.New("op: precondition IDs must not be empty")
		}
		if pre == o.ID {
			return fmt.Errorf("op: operation %s must not depend on itself", o.ID)
		}
	}
	return nil
}

func (o Operation) validatePayload() error {
	switch o.Type {
	case OpWriteFile:
		switch o.Payload.(type) {
		case ir.Artifact, *ir.Artifact:
			return nil
		default:
			return errors.New("op: write file operation requires an ir.Artifact payload")
		}
	case OpRunCommand, OpGitCommit:
		if _, ok := o.Payload.(string); !ok {
			return fmt.Errorf("op: %s operation requires a string payload", o.Type)
		}
	}
	return nil
}
