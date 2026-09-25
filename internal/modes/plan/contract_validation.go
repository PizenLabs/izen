package plan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/PizenLabs/izen/internal/discovery/recon"
	"github.com/PizenLabs/izen/internal/protocol"
)

// ErrContractSchema is the plan-package spelling of the provider-neutral
// structural-output sentinel.  Keeping the alias here lets callers handle a
// schema failure without importing the protocol package directly.
var ErrContractSchema = protocol.ErrContractSchema

// ErrInvalidContract is re-exported for plan-boundary callers.
var ErrInvalidContract = protocol.ErrInvalidContract

// ContractSchemaError identifies the exact contract/schema boundary that
// rejected a model response.  It is intentionally safe to expose to the UI:
// it contains field-level evidence, never a provider response body.
type ContractSchemaError struct {
	Contract protocol.InteractionContract
	Schema   protocol.OutputSchema
	Field    string
	Message  string
}

func (e *ContractSchemaError) Error() string {
	if e == nil {
		return ""
	}
	contract := e.Contract
	if contract == "" {
		contract = "unknown"
	}
	schema := e.Schema
	if schema == "" {
		schema = "unknown"
	}
	field := e.Field
	if field == "" {
		field = "output"
	}
	message := e.Message
	if message == "" {
		message = "response does not satisfy the declared contract schema"
	}
	return fmt.Sprintf("%s: contract=%s schema=%s field=%s: %s", ErrContractSchema, contract, schema, field, message)
}

func (e *ContractSchemaError) Unwrap() error { return ErrContractSchema }

// ValidateContractOutput validates a raw model response against the declared
// descriptor before the plan parser is allowed to construct executable Task
// values.  The optional fastTrack argument selects the task-block schema used
// by the compact dependency path; omitting it means the canonical JSON plan
// schema.
func ValidateContractOutput(content string, descriptor protocol.ContractDescriptor, fastTrack ...bool) error {
	d, err := descriptor.Normalize()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrContractSchema, err)
	}
	useTaskBlocks := len(fastTrack) > 0 && fastTrack[0]
	if useTaskBlocks || d.OutputSchema == protocol.SchemaTaskBlocks {
		return validateTaskBlockContract(content, d)
	}
	if !d.StructuredOutput && d.OutputSchema == protocol.SchemaText {
		// Direct/text contracts do not produce staged plan tasks.  An empty
		// response is valid for a text turn; non-empty content is left to the
		// caller's text policy rather than being interpreted as a plan.
		return nil
	}
	return validateJSONPlanContract(content, d)
}

// ValidateTasksForContract is the final task-boundary guard used immediately
// before a plan is returned or written to a ledger.  It keeps a parser that is
// intentionally tolerant from becoming an authority bypass: every task still
// has to be a valid task and be permitted as a proposal by the descriptor.
func ValidateTasksForContract(tasks []Task, descriptor protocol.ContractDescriptor, fastTrack ...bool) error {
	d, err := descriptor.Normalize()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrContractSchema, err)
	}
	if len(tasks) == 0 {
		return nil
	}
	if d.Contract != protocol.StructuredCompletion && d.Contract != protocol.ToolEnabledCompletion && d.Contract != protocol.AgenticLoop {
		if d.Contract == protocol.DirectCompletion {
			return &protocol.ContractViolationError{Contract: d.Contract, Operation: "staged_task", Reason: "read-only contract cannot stage executable tasks"}
		}
		return &ContractSchemaError{Contract: d.Contract, Schema: d.OutputSchema, Field: "tasks", Message: "contract cannot stage plan tasks"}
	}
	if d.MaxTasks > 0 && len(tasks) > d.MaxTasks {
		return &ContractSchemaError{Contract: d.Contract, Schema: d.OutputSchema, Field: "atomic_tasks", Message: fmt.Sprintf("contains %d tasks, maximum is %d", len(tasks), d.MaxTasks)}
	}
	useTaskBlocks := len(fastTrack) > 0 && fastTrack[0]
	for i, task := range tasks {
		if err := ValidateTask(task); err != nil {
			return &ContractSchemaError{Contract: d.Contract, Schema: d.OutputSchema, Field: fmt.Sprintf("tasks[%d]", i), Message: err.Error()}
		}
		if !d.AllowsTaskType(string(task.Type)) {
			return &protocol.ContractViolationError{Contract: d.Contract, Operation: string(task.Type), Reason: "task type is outside the declared contract proposal boundary"}
		}
		if useTaskBlocks && task.Type != "SHELL_EXEC" &&
			(d.Archetype != protocol.ArchetypeVanillaWeb || task.Type != "FILE_MUTATE") {
			return &ContractSchemaError{Contract: d.Contract, Schema: d.OutputSchema, Field: fmt.Sprintf("tasks[%d].type", i), Message: "fast-track schema permits only SHELL_EXEC task blocks (or VANILLA_WEB FILE_MUTATE blocks)"}
		}
		if d.Archetype == protocol.ArchetypeVanillaWeb && task.Type == "SHELL_EXEC" && !ArchetypeAllowsCommand(recon.VANILLA_WEB, task.Target) {
			return &protocol.ContractViolationError{Contract: d.Contract, Operation: string(protocol.OperationShell), Reason: "shell command is incompatible with VANILLA_WEB archetype"}
		}
	}
	return nil
}

func validateJSONPlanContract(content string, descriptor protocol.ContractDescriptor) error {
	raw := strings.TrimSpace(stripJSONCodeFence(content))
	if !hasJSONBoundary(raw) {
		return &ContractSchemaError{Contract: descriptor.Contract, Schema: descriptor.OutputSchema, Field: "output", Message: "prose or an unbounded fragment surrounds the JSON artifact"}
	}
	if raw == "" {
		return &ContractSchemaError{Contract: descriptor.Contract, Schema: descriptor.OutputSchema, Field: "output", Message: "empty response"}
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return &ContractSchemaError{Contract: descriptor.Contract, Schema: descriptor.OutputSchema, Field: "output", Message: err.Error()}
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return &ContractSchemaError{Contract: descriptor.Contract, Schema: descriptor.OutputSchema, Field: "output", Message: "multiple JSON values are not allowed"}
		}
		return &ContractSchemaError{Contract: descriptor.Contract, Schema: descriptor.OutputSchema, Field: "output", Message: err.Error()}
	}

	if descriptor.OutputSchema == protocol.SchemaJSON {
		return validateGenericJSONValue(value, descriptor)
	}

	switch typed := value.(type) {
	case []any:
		if descriptor.OutputSchema == protocol.SchemaPlanJSON {
			return &ContractSchemaError{Contract: descriptor.Contract, Schema: descriptor.OutputSchema, Field: "output", Message: "plan schema requires one top-level JSON object"}
		}
		if len(typed) == 0 {
			return &ContractSchemaError{Contract: descriptor.Contract, Schema: descriptor.OutputSchema, Field: "atomic_tasks", Message: "task array is empty"}
		}
		for i, item := range typed {
			if err := validateJSONTaskValue(item, descriptor, fmt.Sprintf("tasks[%d]", i), false); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		if strategy, ok := typed["architectural_strategy"].(string); !ok || strings.TrimSpace(strategy) == "" {
			return &ContractSchemaError{Contract: descriptor.Contract, Schema: descriptor.OutputSchema, Field: "architectural_strategy", Message: "required non-empty string is missing"}
		}
		rawTasks, key, ok := taskArrayFromObject(typed)
		if !ok {
			return &ContractSchemaError{Contract: descriptor.Contract, Schema: descriptor.OutputSchema, Field: "atomic_tasks", Message: "required task array is missing"}
		}
		if descriptor.OutputSchema == protocol.SchemaPlanJSON && key != "atomic_tasks" {
			return &ContractSchemaError{Contract: descriptor.Contract, Schema: descriptor.OutputSchema, Field: key, Message: "plan schema requires the canonical atomic_tasks field"}
		}
		items, ok := rawTasks.([]any)
		if !ok || len(items) == 0 {
			return &ContractSchemaError{Contract: descriptor.Contract, Schema: descriptor.OutputSchema, Field: key, Message: "task array must contain at least one entry"}
		}
		if descriptor.MaxTasks > 0 && len(items) > descriptor.MaxTasks {
			return &ContractSchemaError{Contract: descriptor.Contract, Schema: descriptor.OutputSchema, Field: key, Message: fmt.Sprintf("contains %d tasks, maximum is %d", len(items), descriptor.MaxTasks)}
		}
		seenIDs := make(map[int]bool, len(items))
		for i, item := range items {
			canonical := key == "atomic_tasks"
			if err := validateJSONTaskValue(item, descriptor, fmt.Sprintf("%s[%d]", key, i), canonical); err != nil {
				return err
			}
			if canonical {
				id, err := jsonTaskID(item)
				if err != nil {
					return &ContractSchemaError{Contract: descriptor.Contract, Schema: descriptor.OutputSchema, Field: fmt.Sprintf("%s[%d].task_id", key, i), Message: err.Error()}
				}
				if seenIDs[id] {
					return &ContractSchemaError{Contract: descriptor.Contract, Schema: descriptor.OutputSchema, Field: fmt.Sprintf("%s[%d].task_id", key, i), Message: fmt.Sprintf("duplicate task_id %d", id)}
				}
				seenIDs[id] = true
			}
		}
		return nil
	default:
		return &ContractSchemaError{Contract: descriptor.Contract, Schema: descriptor.OutputSchema, Field: "output", Message: "top-level JSON value must be an object or array"}
	}
}

func hasJSONBoundary(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	first, last := raw[0], raw[len(raw)-1]
	return (first == '{' && last == '}') || (first == '[' && last == ']')
}

func validateGenericJSONValue(value any, descriptor protocol.ContractDescriptor) error {
	switch typed := value.(type) {
	case []any:
		if len(typed) == 0 {
			return &ContractSchemaError{Contract: descriptor.Contract, Schema: descriptor.OutputSchema, Field: "output", Message: "JSON array is empty"}
		}
		return nil
	case map[string]any:
		if raw, key, ok := taskArrayFromObject(typed); ok {
			items, ok := raw.([]any)
			if !ok || len(items) == 0 {
				return &ContractSchemaError{Contract: descriptor.Contract, Schema: descriptor.OutputSchema, Field: key, Message: "task array must contain at least one entry"}
			}
			for i, item := range items {
				if err := validateJSONTaskValue(item, descriptor, fmt.Sprintf("%s[%d]", key, i), false); err != nil {
					return err
				}
			}
		}
		return nil
	default:
		return &ContractSchemaError{Contract: descriptor.Contract, Schema: descriptor.OutputSchema, Field: "output", Message: "top-level JSON value must be an object or array"}
	}
}

func taskArrayFromObject(value map[string]any) (any, string, bool) {
	for _, key := range []string{"atomic_tasks", "tasks", "task"} {
		if raw, ok := value[key]; ok {
			return raw, key, true
		}
	}
	return nil, "", false
}

func jsonTaskID(value any) (int, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return 0, errors.New("task must be a JSON object")
	}
	raw, ok := object["task_id"]
	if !ok {
		return 0, errors.New("required task_id is missing")
	}
	switch typed := raw.(type) {
	case json.Number:
		parsed, err := strconv.Atoi(string(typed))
		if err != nil || parsed <= 0 {
			return 0, errors.New("task_id must be a positive integer")
		}
		return parsed, nil
	case float64:
		parsed := int(typed)
		if float64(parsed) != typed || parsed <= 0 {
			return 0, errors.New("task_id must be a positive integer")
		}
		return parsed, nil
	default:
		return 0, errors.New("task_id must be a positive integer")
	}
}

func validateJSONTaskValue(value any, descriptor protocol.ContractDescriptor, field string, canonical bool) error {
	object, ok := value.(map[string]any)
	if !ok {
		return &ContractSchemaError{Contract: descriptor.Contract, Schema: descriptor.OutputSchema, Field: field, Message: "task must be a JSON object"}
	}
	operation := firstString(object, "strategy", "type", "action")
	target := firstString(object, "file", "target", "command", "path")
	if strings.TrimSpace(operation) == "" {
		return &ContractSchemaError{Contract: descriptor.Contract, Schema: descriptor.OutputSchema, Field: field + ".strategy", Message: "required task strategy is missing"}
	}
	if strings.TrimSpace(target) == "" {
		return &ContractSchemaError{Contract: descriptor.Contract, Schema: descriptor.OutputSchema, Field: field + ".file", Message: "required task target is missing"}
	}
	if canonical && strings.TrimSpace(firstString(object, "description", "reason")) == "" {
		return &ContractSchemaError{Contract: descriptor.Contract, Schema: descriptor.OutputSchema, Field: field + ".description", Message: "required task description is missing"}
	}
	if !canonical && !knownPlanOperation(operation) {
		return &ContractSchemaError{Contract: descriptor.Contract, Schema: descriptor.OutputSchema, Field: field + ".action", Message: fmt.Sprintf("unknown task operation %q", operation)}
	}
	if !knownPlanOperation(operation) {
		return &ContractSchemaError{Contract: descriptor.Contract, Schema: descriptor.OutputSchema, Field: field + ".strategy", Message: fmt.Sprintf("unknown task operation %q", operation)}
	}
	return nil
}

func firstString(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := object[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func knownPlanOperation(operation string) bool {
	switch protocol.NormalizeOperation(operation) {
	case protocol.OperationFileMutate, protocol.OperationFileEdit, protocol.OperationShell, protocol.OperationGitAction, protocol.OperationVerify:
		return true
	default:
		return false
	}
}

func validateTaskBlockContract(content string, descriptor protocol.ContractDescriptor) error {
	result := ValidatePlanOutput(content)
	if !result.Valid || len(result.Blocks) == 0 {
		reason := "no valid task blocks"
		if !result.Valid && len(result.Invalid) > 0 {
			reason = result.Invalid[0].Reason
		}
		return &ContractSchemaError{Contract: descriptor.Contract, Schema: descriptor.OutputSchema, Field: "task_blocks", Message: reason}
	}
	if descriptor.MaxTasks > 0 && len(result.Blocks) > descriptor.MaxTasks {
		return &ContractSchemaError{
			Contract: descriptor.Contract,
			Schema:   descriptor.OutputSchema,
			Field:    "task_blocks",
			Message:  fmt.Sprintf("contains %d task blocks, maximum is %d", len(result.Blocks), descriptor.MaxTasks),
		}
	}
	for i, block := range result.Blocks {
		if strings.TrimSpace(block.Target) == "" {
			return &ContractSchemaError{Contract: descriptor.Contract, Schema: descriptor.OutputSchema, Field: fmt.Sprintf("task_blocks[%d].target", i), Message: "task target is empty"}
		}
	}
	return nil
}

// IsContractSchemaError reports whether err carries the canonical structural
// contract failure.
func IsContractSchemaError(err error) bool { return errors.Is(err, ErrContractSchema) }
