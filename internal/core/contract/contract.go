package contract

import "context"

type PermissionLevel string

const (
	PermReadOnly   PermissionLevel = "READ_ONLY"
	PermWorkspace  PermissionLevel = "WORKSPACE_WRITE"
	PermExec       PermissionLevel = "EXECUTE_COMMAND"
	PermTest       PermissionLevel = "RUN_TESTS"
	PermPatch      PermissionLevel = "APPLY_PATCH"
	PermCheckpoint PermissionLevel = "CREATE_CHECKPOINT"
)

type StageContract struct {
	Name           string
	AllowedPerms   []PermissionLevel
	HasSideEffects bool
	CanRetry       bool
}

func (c StageContract) Permitted(level PermissionLevel) bool {
	for _, p := range c.AllowedPerms {
		if p == level {
			return true
		}
	}
	return false
}

type StageInput struct {
	RequestID string
	Context   map[string]interface{}
	Payload   interface{}
}

type StageOutput struct {
	Success     bool
	Data        interface{}
	Error       error
	Recoverable bool
}

type PipelineStage interface {
	Contract() StageContract
	Execute(ctx context.Context, in StageInput) (StageOutput, error)
}
