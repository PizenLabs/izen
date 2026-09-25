package contextcompiler

import (
	"context"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/protocol"
	"github.com/PizenLabs/izen/internal/session"
)

// RequestCompileOptions supplies the semantic phase and model facts that are
// not intrinsic to an ai.Request. It is intentionally provider-neutral: the
// caller resolves catalog metadata and passes the values across the boundary.
type RequestCompileOptions struct {
	Phase                 Phase
	Provider              string
	WorkflowState         string
	ContextPolicy         string
	Scope                 string
	Lineage               string
	Exclusions            []string
	Files                 []FileContext
	ContextWindow         int
	MaxOutputTokens       int
	RequestedOutputTokens int
	ContextBudget         int
	ModelMetadata         *protocol.ModelMetadata
	// IncludeSchema copies the compiler's schema reservation into the request
	// system field. Native-schema adapters normally leave this false because
	// their serializer owns the wire field; prompt-fallback adapters can set it
	// when they do not pass through the standard serializer.
	IncludeSchema bool
}

// ToolDescriptors converts native request tool definitions into the bounded,
// descriptive form used for prompt accounting. The returned descriptors are
// copies and do not grant authority.
func ToolDescriptors(tools []ai.ToolDefinition) []ToolDescriptor {
	if len(tools) == 0 {
		return nil
	}
	out := make([]ToolDescriptor, 0, len(tools))
	for _, tool := range tools {
		out = append(out, ToolDescriptor{
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			Schema:      string(tool.Function.Parameters),
		})
	}
	return out
}

// CompileRequest is the AgentContext facade used by core request builders. It
// always delegates to Compile, accounts for system messages, the interaction
// schema, tool descriptors and supplied workspace files, then returns a private
// request copy whose user turn contains only the fitted context projection.
func (c *Compiler) CompileRequest(ctx context.Context, req ai.Request, opts RequestCompileOptions) (ai.Request, *AgentContext, error) {
	if c == nil {
		return ai.Request{}, nil, fmt.Errorf("contextcompiler: nil compiler")
	}
	if ctx == nil {
		return ai.Request{}, nil, fmt.Errorf("contextcompiler: nil context")
	}

	messages := append([]ai.Message(nil), req.Messages...)
	var systemParts []string
	if strings.TrimSpace(req.System) != "" {
		systemParts = append(systemParts, req.System)
	}
	lastUser := -1
	for i, message := range messages {
		if strings.EqualFold(message.Role, "user") && strings.TrimSpace(message.Content) != "" {
			lastUser = i
		}
	}
	if lastUser < 0 {
		// Legacy callers sometimes use an empty role or a single non-system
		// turn. Preserve that compatibility without mistaking a trailing
		// assistant/tool result for the current user request.
		for i, message := range messages {
			if !strings.EqualFold(message.Role, "system") && strings.TrimSpace(message.Content) != "" {
				lastUser = i
			}
		}
	}
	var recent []session.Message
	var userParts []string
	for i, message := range messages {
		if strings.EqualFold(message.Role, "system") {
			if strings.TrimSpace(message.Content) != "" {
				systemParts = append(systemParts, message.Content)
			}
			continue
		}
		if strings.TrimSpace(message.Content) == "" {
			continue
		}
		if i == lastUser {
			continue
		}
		recent = append(recent, session.Message{Role: message.Role, Content: message.Content})
		userParts = append(userParts, message.Content)
	}
	userText := ""
	if lastUser >= 0 {
		userText = messages[lastUser].Content
	} else {
		userText = strings.Join(userParts, "\n\n")
	}

	descriptor := req.Contract
	if descriptor == nil && req.InteractionContract.Valid() {
		normalized := protocol.Describe(req.InteractionContract)
		descriptor = &normalized
	}
	metadata := opts.ModelMetadata
	if metadata == nil {
		metadata = req.ModelMetadata
	}
	if metadata != nil && metadata.ID != "" && !strings.EqualFold(strings.TrimSpace(metadata.ID), strings.TrimSpace(req.Model)) {
		metadata = nil
		req.ModelMetadata = nil
	}
	contextWindow := opts.ContextWindow
	maxOutput := opts.MaxOutputTokens
	requestedOutput := opts.RequestedOutputTokens
	if metadata != nil {
		if contextWindow <= 0 {
			contextWindow = metadata.ContextWindow
		}
		if maxOutput <= 0 {
			maxOutput = metadata.MaxOutputTokens
		}
	}
	if requestedOutput <= 0 {
		requestedOutput = req.MaxTokens
	}
	if requestedOutput <= 0 && descriptor != nil {
		requestedOutput = descriptor.MaxOutputTokens
	}
	phase := opts.Phase.Normalize()
	if opts.Phase != "" && phase == "" {
		return ai.Request{}, nil, fmt.Errorf("contextcompiler: invalid phase %q", opts.Phase)
	}
	if phase == "" && descriptor != nil {
		phase = PhaseForContract(descriptor.Contract)
	}
	policy := normalizeContextPolicy(opts.ContextPolicy)
	schemaOverlay := ""
	if descriptor == nil {
		schemaOverlay = responseFormatSchema(req.ResponseFormat)
	}
	systemText := joinDistinct(systemParts)
	in := Input{
		Phase:                 phase,
		Provider:              opts.Provider,
		Model:                 req.Model,
		ContextWindow:         contextWindow,
		MaxOutputTokens:       maxOutput,
		RequestedOutputTokens: requestedOutput,
		ContextBudget:         opts.ContextBudget,
		SystemInstructions:    systemText,
		SchemaOverlay:         schemaOverlay,
		ToolDescriptors:       ToolDescriptors(req.Tools),
		Contract:              descriptor,
		ContextPolicy:         policy,
		Scope:                 opts.Scope,
		Lineage:               opts.Lineage,
		Exclusions:            append([]string(nil), opts.Exclusions...),
		UserRequest:           userText,
		RecentTurns:           recent,
		WorkflowState:         opts.WorkflowState,
		Files:                 append([]FileContext(nil), opts.Files...),
	}
	agent, err := c.CompileAgentContext(ctx, in)
	if err != nil {
		return ai.Request{}, nil, err
	}
	if metadata != nil {
		copy := *metadata
		req.ModelMetadata = &copy
	}
	if req.Contract != nil {
		copy := req.Contract.Clone()
		req.Contract = &copy
	}
	if requestedOutput > 0 {
		req.MaxTokens = requestedOutput
	}

	// Rebuild the transport message projection instead of replacing only the
	// last turn in the original slice. Leaving prior turns in place would send
	// recent history twice (once in the compiler projection and once as the
	// original messages), defeating the aggregate request budget. System
	// instructions are canonicalized into Request.System for the same reason;
	// every production adapter has an explicit top-level system field.
	contextText := agent.Compiled.ContextOnly()
	req.System = systemText
	if strings.TrimSpace(contextText) != "" {
		req.Messages = []ai.Message{{Role: "user", Content: contextText}}
	} else {
		req.Messages = nil
	}
	req.ContextPhase = string(phase)
	req.ContextPolicy = policy
	req.ContextPrepared = true
	if opts.IncludeSchema && agent.Compiled.SchemaText() != "" {
		req.System = appendSchemaOverlay(req.System, agent.Compiled.SchemaText())
	}
	return req, agent, nil
}

// CompilePrompt is a descriptive compatibility alias for CompileRequest.
func (c *Compiler) CompilePrompt(ctx context.Context, req ai.Request, opts RequestCompileOptions) (ai.Request, *AgentContext, error) {
	return c.CompileRequest(ctx, req, opts)
}

func responseFormatSchema(format *ai.ResponseFormat) string {
	if format == nil {
		return ""
	}
	if len(format.Schema) > 0 {
		return string(format.Schema)
	}
	if format.JSONSchema != nil && len(format.JSONSchema.Schema) > 0 {
		return string(format.JSONSchema.Schema)
	}
	// The wire serializer supplies this conservative object schema for a
	// json_schema envelope that omitted a document. Account for that implicit
	// schema rather than treating the request as schema-free.
	if strings.EqualFold(strings.TrimSpace(format.Type), "json_schema") {
		return `{"type":"object"}`
	}
	return ""
}

func joinDistinct(parts []string) string {
	var b strings.Builder
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(part)
	}
	return b.String()
}

func appendSchemaOverlay(existing, overlay string) string {
	overlay = strings.TrimSpace(overlay)
	if overlay == "" {
		return existing
	}
	if strings.Contains(existing, "[STRUCTURED_OUTPUT_CONTRACT]") {
		return existing
	}
	if strings.TrimSpace(existing) == "" {
		return overlay
	}
	return strings.TrimSpace(existing) + "\n\n" + overlay
}
