package scopeguard

import (
	"fmt"
	"path/filepath"
	"strings"
)

// OpClass classifies what a worker proposal wants to do.
type OpClass string

const (
	// OpRead covers read-only proposals (never scope-gated for rejection,
	// but still normalized for audit).
	OpRead OpClass = "READ"
	// OpWrite covers file creation/overwrite.
	OpWrite OpClass = "WRITE"
	// OpPatch covers diff/patch/edit application.
	OpPatch OpClass = "PATCH"
	// OpDelete covers file deletion.
	OpDelete OpClass = "DELETE"
	// OpTest covers diagnostic execution (tests, linters, builds).
	OpTest OpClass = "TEST"
)

// Mutating reports whether the op class mutates workspace state.
func (o OpClass) Mutating() bool {
	switch o {
	case OpWrite, OpPatch, OpDelete:
		return true
	default:
		return false
	}
}

// Valid reports whether o is a known op class.
func (o OpClass) Valid() bool {
	switch o {
	case OpRead, OpWrite, OpPatch, OpDelete, OpTest:
		return true
	default:
		return false
	}
}

// Proposal is a structured worker proposal. Workers NEVER execute
// capabilities directly; they generate proposals for the pipeline.
type Proposal struct {
	// ID identifies the proposal across guard/gateway/executor hops.
	ID string `json:"id"`
	// TaskID binds the proposal to exactly one durable task.
	TaskID string `json:"taskId"`
	// Workspace is the originating semantic workspace.
	Workspace Workspace `json:"workspace"`
	// Op is the requested capability class.
	Op OpClass `json:"op"`
	// TargetFiles are the proposed mutation targets (workspace-relative).
	TargetFiles []string `json:"targetFiles"`
	// OperationID is the idempotency key for ExecutionCursor dispatch.
	OperationID string `json:"operationId,omitempty"`
	// StepID groups the proposal within the task step graph.
	StepID string `json:"stepId,omitempty"`
	// Detail is a bounded human-readable justification.
	Detail string `json:"detail,omitempty"`
}

// Validate performs schema-level checks (closed discovery schema:
// control-bearing fields are rejected by requiring explicit values).
func (p Proposal) Validate() error {
	if strings.TrimSpace(p.ID) == "" {
		return fmt.Errorf("scopeguard: empty proposal id")
	}
	if strings.TrimSpace(p.TaskID) == "" {
		return fmt.Errorf("scopeguard: empty task id")
	}
	if !p.Op.Valid() {
		return fmt.Errorf("scopeguard: unknown op class %q", p.Op)
	}
	if !p.Workspace.Valid() {
		return fmt.Errorf("scopeguard: unknown workspace %q", p.Workspace)
	}
	if len(p.TargetFiles) == 0 {
		return fmt.Errorf("scopeguard: proposal carries no target files")
	}
	for _, t := range p.TargetFiles {
		if strings.TrimSpace(t) == "" {
			return fmt.Errorf("scopeguard: proposal carries an empty target")
		}
		if filepath.IsAbs(t) {
			return fmt.Errorf("scopeguard: absolute target %q escapes workspace", t)
		}
		if t != filepath.Clean(t) || strings.HasPrefix(t, "..") {
			return fmt.Errorf("scopeguard: target %q escapes workspace", t)
		}
	}
	return nil
}

// normalizeScope cleans and sorts a scope list for deterministic matching.
func normalizeScope(scope []string) []string {
	out := make([]string, 0, len(scope))
	for _, s := range scope {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		s = filepath.Clean(s)
		out = append(out, s)
	}
	return out
}

// normalizeTarget cleans one target path for comparison.
func normalizeTarget(t string) string {
	return filepath.Clean(strings.TrimSpace(t))
}

// withinScope reports whether target sits inside any scope entry.
// A scope entry matches either exactly or as a directory prefix;
// a scope entry that names a file matches only that file.
func withinScope(scope []string, target string) bool {
	t := normalizeTarget(target)
	for _, s := range scope {
		if t == s {
			return true
		}
		if strings.HasPrefix(t, s+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
