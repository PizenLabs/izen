package llm

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/protocol"
)

const structuredOutputPromptMarker = "[STRUCTURED_OUTPUT_CONTRACT]"

type promptJSONSchema struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

type promptResponseFormat struct {
	Type       string            `json:"type"`
	JSONSchema *promptJSONSchema `json:"json_schema,omitempty"`
}

type promptContractPlan struct {
	Descriptor       *protocol.ContractDescriptor
	Schema           json.RawMessage
	NativeSchema     bool
	ResponseFormat   *promptResponseFormat
	InlineConstraint string
}

func preparePromptContract(provider string, req PromptRequest) (PromptRequest, promptContractPlan, error) {
	descriptor, err := normalizedPromptDescriptor(req)
	if err != nil {
		return PromptRequest{}, promptContractPlan{}, err
	}
	plan := promptContractPlan{}
	if descriptor == nil {
		return req, plan, nil
	}
	copy := descriptor.Clone()
	plan.Descriptor = &copy
	req.InteractionContract = copy.Contract
	req.Contract = &copy
	if !copy.StructuredOutput && copy.OutputSchema == protocol.SchemaText {
		return req, plan, nil
	}
	schema, err := copy.JSONSchema()
	if err != nil {
		return PromptRequest{}, promptContractPlan{}, err
	}
	plan.Schema = append(json.RawMessage(nil), schema...)
	constraint, err := copy.InlineSchemaConstraint()
	if err != nil {
		return PromptRequest{}, promptContractPlan{}, err
	}
	plan.InlineConstraint = constraint
	if nativePromptSchemaSupported(provider, req.Model, req.SchemaMode, req.ModelMetadata) && len(schema) > 0 {
		plan.NativeSchema = true
		plan.ResponseFormat = &promptResponseFormat{
			Type: "json_schema",
			JSONSchema: &promptJSONSchema{
				Name:   promptSchemaName(*plan.Descriptor),
				Strict: true,
				Schema: append(json.RawMessage(nil), schema...),
			},
		}
		return req, plan, nil
	}
	plan.NativeSchema = false
	if constraint != "" {
		injectPromptConstraint(&req, constraint)
	}
	return req, plan, nil
}

func normalizedPromptDescriptor(req PromptRequest) (*protocol.ContractDescriptor, error) {
	if req.Contract != nil {
		normalized, err := req.Contract.Clone().Normalize()
		if err != nil {
			return nil, fmt.Errorf("llm: %w: %w", protocol.ErrInvalidContract, err)
		}
		if req.InteractionContract != "" && req.InteractionContract != normalized.Contract {
			return nil, fmt.Errorf("llm: %w: request contract %q does not match descriptor %q", protocol.ErrInvalidContract, req.InteractionContract, normalized.Contract)
		}
		return &normalized, nil
	}
	if req.InteractionContract == "" {
		return nil, nil
	}
	if !req.InteractionContract.Valid() {
		return nil, fmt.Errorf("llm: %w: unknown interaction contract %q", protocol.ErrInvalidContract, req.InteractionContract)
	}
	descriptor := protocol.Describe(req.InteractionContract)
	return &descriptor, nil
}

func nativePromptSchemaSupported(provider, model, mode string, metadata *protocol.ModelMetadata) bool {
	if metadata != nil {
		if metadata.ID != "" && !strings.EqualFold(strings.TrimSpace(metadata.ID), strings.TrimSpace(model)) {
			metadata = nil
		}
		if metadata != nil && !metadata.SupportsStructuredOutput {
			return false
		}
		if metadata != nil && metadata.SupportsStructuredOutput {
			return nativePromptProvider(provider)
		}
	}
	if strings.EqualFold(strings.TrimSpace(mode), "prompt") {
		return false
	}
	if protocol.IsConstrainedModel(model) && !strings.EqualFold(strings.TrimSpace(provider), "ollama") {
		return false
	}
	return nativePromptProvider(provider)
}

func nativePromptProvider(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "openai", "openrouter", "ollama":
		return true
	default:
		return false
	}
}

func promptSchemaName(descriptor protocol.ContractDescriptor) string {
	name := "izen_" + string(descriptor.Contract) + "_" + string(descriptor.OutputSchema)
	if descriptor.SchemaVersion != "" {
		name += "_" + descriptor.SchemaVersion
	}
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		default:
			return '_'
		}
	}, name)
	name = strings.Trim(name, "_")
	if len(name) > 64 {
		name = name[:64]
	}
	if name == "" {
		return "izen_interaction_contract"
	}
	return name
}

func injectPromptConstraint(req *PromptRequest, constraint string) {
	if strings.Contains(req.System, structuredOutputPromptMarker) {
		return
	}
	for _, message := range req.Messages {
		if strings.Contains(message.Content, structuredOutputPromptMarker) {
			return
		}
	}
	if req.System != "" {
		req.System = strings.TrimSpace(req.System) + "\n\n" + constraint
		return
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "system" {
			req.Messages = append([]Message(nil), req.Messages...)
			req.Messages[i].Content = strings.TrimSpace(req.Messages[i].Content) + "\n\n" + constraint
			return
		}
	}
	req.System = constraint
}

func nativePromptSchema(plan promptContractPlan) json.RawMessage {
	if !plan.NativeSchema {
		return nil
	}
	return append(json.RawMessage(nil), plan.Schema...)
}

func stampPromptResponse(response LLMResponse, provider, model string, plan promptContractPlan) LLMResponse {
	response.Provider = provider
	response.Model = model
	if plan.Descriptor != nil {
		copy := plan.Descriptor.Clone()
		response.InteractionContract = copy.Contract
		response.Contract = &copy
	}
	response.NativeSchema = plan.NativeSchema
	response.Schema = string(plan.Schema)
	response.InlineConstraint = plan.InlineConstraint
	return response
}
