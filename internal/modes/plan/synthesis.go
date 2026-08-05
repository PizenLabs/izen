package plan

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/domain/task"
)

type FlatPlanSpec struct {
	Target      string `json:"target"`
	Action      string `json:"action"`
	TemplateKey string `json:"template_key,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Description string `json:"description,omitempty"`
}

func ParseFlatPlanSpec(content string) (*FlatPlanSpec, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, nil
	}

	content = stripJSONCodeFence(content)

	var spec FlatPlanSpec
	if err := json.Unmarshal([]byte(content), &spec); err != nil {
		return nil, err
	}

	if spec.Target == "" {
		return nil, nil
	}

	return &spec, nil
}

func stripJSONCodeFence(content string) string {
	content = strings.TrimSpace(content)
	for strings.HasPrefix(content, "```") {
		firstNewline := strings.Index(content, "\n")
		if firstNewline != -1 {
			content = content[firstNewline+1:]
		} else {
			break
		}
		content = strings.TrimSpace(content)
	}
	for strings.HasSuffix(content, "```") {
		lastBackticks := strings.LastIndex(content, "```")
		if lastBackticks != -1 {
			content = strings.TrimSpace(content[:lastBackticks])
		} else {
			break
		}
	}
	content = strings.TrimSpace(content)
	return content
}

func FlatSpecToTasks(spec *FlatPlanSpec) []Task {
	if spec == nil || spec.Target == "" {
		return nil
	}

	taskType := task.TaskFileMutate
	if spec.Action == "SHELL_EXEC" {
		taskType = task.TaskShellExec
	}

	description := fmt.Sprintf("%s %s", spec.Action, spec.Target)
	solution := fmt.Sprintf("Completed %s on %s", spec.Action, spec.Target)
	rationale := fmt.Sprintf("Flat plan spec: action=%s target=%s", spec.Action, spec.Target)

	if spec.TemplateKey != "" {
		description = fmt.Sprintf("Apply %s template to %s", spec.TemplateKey, spec.Target)
		solution = fmt.Sprintf("Applied %s template to %s", spec.TemplateKey, spec.Target)
	}
	if spec.Description != "" {
		description = spec.Description
	}
	if spec.Reason != "" {
		rationale = spec.Reason
	}

	return []Task{{
		StepNum:     1,
		Status:      task.StatusIdle,
		Type:        taskType,
		Target:      spec.Target,
		Description: description,
		Rationale:   rationale,
		Solution:    solution,
		IsHardcoded: true,
	}}
}
