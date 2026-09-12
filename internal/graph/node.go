package graph

import (
	"context"
	"errors"
	"fmt"
	"time"

	domaintask "github.com/PizenLabs/izen/internal/domain/task"
	"github.com/PizenLabs/izen/internal/ir"
	"github.com/PizenLabs/izen/internal/op"
)

// Capability contracts satisfied by the concrete resource adapters. OpNode
// executes operations exclusively through these small interfaces so the graph
// never touches a file, process or repository directly.
type (
	// fileWriter writes raw content onto a file target.
	fileWriter interface {
		Write([]byte) error
	}
	// fileDeleter removes a file target.
	fileDeleter interface {
		Delete() error
	}
	// commandRunner executes a shell command on a terminal target.
	commandRunner interface {
		Run(ctx context.Context, command string) (string, error)
	}
	// gitCommiter creates a commit on a git repository target.
	gitCommiter interface {
		Commit(ctx context.Context, message string) (string, error)
	}
)

// OpNode wraps an op.Operation and satisfies domaintask.Executable so the
// operation runs on the Phase A kernel.Engine (execution bridge).
type OpNode struct {
	op op.Operation
}

// Compile-time assertion that OpNode satisfies domaintask.Executable.
var _ domaintask.Executable = (*OpNode)(nil)

// NewOpNode validates the operation and wraps it in an executable graph node.
// The operation preconditions are copied defensively.
func NewOpNode(operation op.Operation) (*OpNode, error) {
	if err := operation.Validate(); err != nil {
		return nil, err
	}
	operation.Preconditions = append([]string(nil), operation.Preconditions...)
	return &OpNode{op: operation}, nil
}

// ID returns the wrapped operation's ID.
func (n *OpNode) ID() string { return n.op.ID }

// Requires returns a defensive copy of the operation preconditions.
func (n *OpNode) Requires() []string {
	return append([]string(nil), n.op.Preconditions...)
}

// Timeout returns the wrapped operation's execution timeout.
func (n *OpNode) Timeout() time.Duration { return n.op.Timeout }

// Operation returns the wrapped operation value.
func (n *OpNode) Operation() op.Operation { return n.op }

// Execute dispatches the operation onto its target resource through the
// resource capability contracts. A canceled context short-circuits to
// StatusCanceled; failures surface as StatusFailed with the cause.
func (n *OpNode) Execute(ctx context.Context, _ domaintask.Runtime) domaintask.TaskResult {
	if err := ctx.Err(); err != nil {
		return canceledResult(err)
	}
	switch n.op.Type {
	case op.OpWriteFile:
		return n.executeWriteFile(ctx)
	case op.OpDeleteFile:
		return n.executeDeleteFile(ctx)
	case op.OpRunCommand:
		return n.executeRunCommand(ctx)
	case op.OpGitCommit:
		return n.executeGitCommit(ctx)
	default:
		return failedResult(fmt.Errorf("graph: unsupported operation type %q", n.op.Type))
	}
}

func (n *OpNode) executeWriteFile(ctx context.Context) domaintask.TaskResult {
	artifact, ok := n.artifactPayload()
	if !ok {
		return failedResult(errors.New("graph: write operation requires an ir.Artifact payload"))
	}
	w, ok := n.op.TargetResource.(fileWriter)
	if !ok {
		return failedResult(errors.New("graph: write operation target does not support file writes"))
	}
	if err := ctx.Err(); err != nil {
		return canceledResult(err)
	}
	if err := w.Write(artifact.Content); err != nil {
		return failedResult(fmt.Errorf("graph: write %q: %w", artifact.Path, err))
	}
	return domaintask.TaskResult{Status: domaintask.ExecStatusCompleted}
}

func (n *OpNode) executeDeleteFile(ctx context.Context) domaintask.TaskResult {
	d, ok := n.op.TargetResource.(fileDeleter)
	if !ok {
		return failedResult(errors.New("graph: delete operation target does not support file deletion"))
	}
	if err := ctx.Err(); err != nil {
		return canceledResult(err)
	}
	if err := d.Delete(); err != nil {
		return failedResult(fmt.Errorf("graph: delete operation: %w", err))
	}
	return domaintask.TaskResult{Status: domaintask.ExecStatusCompleted}
}

func (n *OpNode) executeRunCommand(ctx context.Context) domaintask.TaskResult {
	command, ok := n.op.Payload.(string)
	if !ok {
		return failedResult(errors.New("graph: command operation requires a string payload"))
	}
	r, ok := n.op.TargetResource.(commandRunner)
	if !ok {
		return failedResult(errors.New("graph: command operation target does not support shell execution"))
	}
	out, err := r.Run(ctx, command)
	if err != nil {
		return domaintask.TaskResult{
			Status: domaintask.ExecStatusFailed,
			Error:  fmt.Errorf("graph: command %q: %w", command, err),
			Data:   out,
		}
	}
	return domaintask.TaskResult{Status: domaintask.ExecStatusCompleted, Data: out}
}

func (n *OpNode) executeGitCommit(ctx context.Context) domaintask.TaskResult {
	message, ok := n.op.Payload.(string)
	if !ok {
		return failedResult(errors.New("graph: git commit operation requires a string payload"))
	}
	c, ok := n.op.TargetResource.(gitCommiter)
	if !ok {
		return failedResult(errors.New("graph: git commit operation target does not support commits"))
	}
	sha, err := c.Commit(ctx, message)
	if err != nil {
		return failedResult(fmt.Errorf("graph: git commit: %w", err))
	}
	return domaintask.TaskResult{Status: domaintask.ExecStatusCompleted, Data: sha}
}

// artifactPayload extracts the ir.Artifact payload, accepting both a value and
// a non-nil pointer.
func (n *OpNode) artifactPayload() (ir.Artifact, bool) {
	switch p := n.op.Payload.(type) {
	case ir.Artifact:
		return p, true
	case *ir.Artifact:
		if p != nil {
			return *p, true
		}
	}
	return ir.Artifact{}, false
}

func failedResult(err error) domaintask.TaskResult {
	return domaintask.TaskResult{Status: domaintask.ExecStatusFailed, Error: err}
}

func canceledResult(err error) domaintask.TaskResult {
	return domaintask.TaskResult{Status: domaintask.ExecStatusCanceled, Error: err}
}
