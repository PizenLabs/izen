package substrate

import (
	"context"
)

// ReadScope provides read-only access to workspace and state snapshotting.
// It MUST NOT contain any Write, Apply, Mutate, or Commit methods.
type ReadScope interface {
	ReadFile(relPath string) ([]byte, error)
	ReadTree(root string) ([]string, error)
	Snapshot() (string, error)
}

type OperationType string

const (
	OpFileWrite  OperationType = "FILE_WRITE"
	OpFileDelete OperationType = "FILE_DELETE"
	OpExecCmd    OperationType = "EXEC_CMD"
)

type Operation struct {
	Type    OperationType
	Target  string
	Content []byte
	Args    []string
}

// Proposal is an immutable value object emitted by Strategies.
type Proposal struct {
	ID            string
	Intent        string
	Preconditions []string
	Operations    []Operation
}

// ExecutionProof contains verification evidence for committed mutations.
type ExecutionProof struct {
	ProposalID    string
	TransactionID string
	Status        string
	EvidencePath  string
	Error         error
}

// Substrate is the ONLY component in the system allowed to execute side-effects.
type Substrate interface {
	Execute(ctx context.Context, prop Proposal) (ExecutionProof, error)
}
