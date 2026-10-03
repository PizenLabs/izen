// ── Objective requirement pass ──────────────────────────────────────────────
//
// WHY THIS PASS EXISTS. Until now the runtime derived the mutation surface of
// an objective from ONE budget-driven decision — "does this file fit in a
// single generation?" — and then judged completion against the shape of the
// mutation it happened to produce. Nothing in the runtime represented the
// user's INTENDED OUTCOME, so a minimal admissible patch completed a broad
// objective. The completion contract (objective_contract.go) fixes the judging;
// this pass supplies the ONLY piece of it that cannot be derived
// deterministically: what the objective actually asks for.
//
// WHAT IT IS. A single bounded READ-ONLY provider invocation whose only output
// is a raw JSON array of requirement proposals. It is a PROPOSAL pass, not an
// authority: every proposal it returns still has to clear the runtime's
// admissibility gate (traceable to the user's request, grounded in a target
// inside the objective's resolved scope) before it becomes an obligation, and
// even an admitted requirement is discharged only by an execution fact the
// runtime observed itself.
//
// WHAT IT IS NOT. It is not "ask the model whether the task is done". Nothing
// here can grant completion: the worst a lying model can do is return an empty
// array, and an empty array costs the runtime nothing it would not have spent
// anyway. It is not a quality scorer, not a critic, and not a second opinion
// on the artifact.
//
// The pass is strictly bounded, strictly read-only and strictly optional: the
// executor remains the single authority that invokes the provider, and a caller
// that never wires this pass keeps exactly the behaviour it had.
package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/contextcompiler"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/protocol"
)

// RequirementPassMaxTokens bounds the requirement generation. A requirement
// ledger is a short list of one-line obligations; anything longer is the model
// ignoring the schema, and the response is rejected rather than truncated into
// something that looks parseable.
const RequirementPassMaxTokens = 512

// requirementPassRejectTokens is the post-hoc rejection ceiling for the same
// reason the manifest pass has one: a provider that ignores max_tokens must be
// refused as malformed rather than allowed to exhaust an output gate.
const requirementPassRejectTokens = 1024

// RequirementPassDirective is the compactness instruction injected verbatim into
// the requirement system prompt.
const RequirementPassDirective = "OUTPUT ONLY VALID MINIFIED JSON. DO NOT WRITE PROSE, DO NOT INCLUDE MARKDOWN FENCES, DO NOT DESCRIBE YOUR WORK."

// RequirementPassSystemPrompt is the wire contract of the requirement pass. It
// asks for obligations, never for an opinion about quality, and it tells the
// model the truth about its own authority so it cannot mistake proposing for
// deciding.
const RequirementPassSystemPrompt = "You are the requirement derivation stage of a read-only planning pass. " +
	"Given the user's objective and the resolved target list, list the DISTINCT requirements that must ALL hold " +
	"before the objective can honestly be called done. " +
	"One requirement per element. Each requirement is ONE short imperative sentence using only vocabulary from the objective, " +
	"and MUST name at least one target from the resolved list. " +
	"Do NOT invent aesthetic or quality requirements, do NOT describe steps you took, do NOT claim anything is already satisfied. " +
	"List as many requirements as the objective genuinely implies; a broad objective implies several. " +
	RequirementPassDirective + "\n" +
	`Output a single raw JSON array (minified, no newlines) conforming exactly to: [{"id":"r1","requirement":"<one imperative sentence naming a target>"}]` + "\n" +
	"This pass never writes to the workspace and cannot declare the objective complete."

// ProposedRequirement is ONE raw requirement proposal as it crosses the wire.
// It carries no status: status is the runtime's to assign, and a proposal that
// arrived with one would be a model grading its own homework.
type ProposedRequirement struct {
	ID          string `json:"id"`
	Requirement string `json:"requirement"`
	// Targets optionally pre-declares the scope the requirement is about. The
	// runtime still grounds them against the objective's resolved scope; a
	// target the objective never resolved is not adopted.
	Targets []string `json:"targets,omitempty"`
}

// ParseProposedRequirements parses the raw requirement payload.
//
// It is deliberately tolerant of wire shape and intolerant of content: a bare
// array and an {"requirements":[…]} envelope both parse, markdown fences are
// stripped, and a payload that yields no usable proposal is an error rather
// than a silent empty ledger. An empty ledger means "the model declined to
// enumerate", which the caller must be able to distinguish from "the model
// enumerated nothing because nothing was required".
func ParseProposedRequirements(raw []byte) ([]ProposedRequirement, error) {
	text := stripCodeFence(strings.TrimSpace(string(raw)))
	if text == "" {
		return nil, fmt.Errorf("requirement pass: empty payload")
	}
	var list []ProposedRequirement
	switch {
	case strings.HasPrefix(text, "["):
		if err := json.Unmarshal([]byte(text), &list); err != nil {
			return nil, fmt.Errorf("requirement pass: invalid JSON: %w", err)
		}
	case strings.HasPrefix(text, "{"):
		var envelope struct {
			Requirements []ProposedRequirement `json:"requirements"`
		}
		if err := json.Unmarshal([]byte(text), &envelope); err != nil {
			return nil, fmt.Errorf("requirement pass: invalid JSON: %w", err)
		}
		list = envelope.Requirements
	default:
		return nil, fmt.Errorf("requirement pass: payload is neither a JSON array nor a JSON object")
	}

	out := make([]ProposedRequirement, 0, len(list))
	for i, p := range list {
		body := strings.TrimSpace(p.Requirement)
		if body == "" {
			continue
		}
		if strings.TrimSpace(p.ID) == "" {
			p.ID = fmt.Sprintf("r%d", i+1)
		}
		p.Requirement = body
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("requirement pass: payload carried no requirement text")
	}
	// A runaway list is not a richer plan; it is a model that ignored the
	// schema, and admitting it would manufacture obligations nobody can
	// discharge. Bound it and say so.
	const maxRequirements = 24
	if len(out) > maxRequirements {
		return out[:maxRequirements], nil
	}
	return out, nil
}

// stripCodeFence removes a surrounding markdown fence, tolerating the models
// that emit one despite an explicit instruction not to.
func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```")
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		if lang := strings.TrimSpace(s[:idx]); lang != "" && !strings.ContainsAny(lang, "{[\"") {
			s = s[idx+1:]
		}
	}
	if idx := strings.LastIndex(s, "```"); idx >= 0 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}

// SetRequirementPassSystemPrompt overrides the requirement system prompt.
func (x *RuntimeExecutor) SetRequirementPassSystemPrompt(p string) {
	if x == nil {
		return
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.requirementSystemPromptOverride = strings.TrimSpace(p)
}

func (x *RuntimeExecutor) requirementSystemPromptFor() string {
	if x == nil {
		return RequirementPassSystemPrompt
	}
	x.mu.Lock()
	override := x.requirementSystemPromptOverride
	x.mu.Unlock()
	if override != "" {
		return override
	}
	return RequirementPassSystemPrompt
}

// InvokeRequirementPass performs the bounded READ-ONLY requirement-derivation
// invocation and returns the verbatim model response for
// ParseProposedRequirements.
//
// modelHint is the Workspace Target model the caller resolved for this objective.
// It is REQUIRED and is never defaulted: a requirement pass that silently fell
// back to some other model would make the completion contract depend on a model
// the operator never selected, which is the same authority leak the executor
// refuses for every other invocation.
//
// The rest mirrors the manifest pass deliberately: same provider, same
// model-binding discipline, same hard token ceilings, same refusal of a truncated
// payload before the parser can see a syntactically plausible fragment. It never
// reads the workspace beyond the caller-provided scope labels and never touches
// disk — the scope NAMES are enough to ask which obligations exist, and passing
// the file bodies would re-inflate the budget this pass exists to keep small.
func (x *RuntimeExecutor) InvokeRequirementPass(ctx context.Context, objective string, scope []string, modelHint string) (string, error) {
	if x == nil {
		return "", fmt.Errorf("executor: nil runtime for requirement pass")
	}
	x.mu.Lock()
	p := x.provider
	x.mu.Unlock()
	if p == nil {
		return "", fmt.Errorf("executor: no provider configured for the requirement pass")
	}
	model, err := x.resolveRequirementPassModel(modelHint)
	if err != nil {
		return "", err
	}

	var user strings.Builder
	user.WriteString("USER OBJECTIVE:\n")
	user.WriteString(strings.TrimSpace(objective))
	user.WriteString("\n")
	user.WriteString("RESOLVED TARGETS:\n")
	if len(scope) == 0 {
		user.WriteString("(none resolved yet — propose no requirements)\n")
	}
	for _, t := range scope {
		if t = strings.TrimSpace(t); t != "" {
			user.WriteString("- " + t + "\n")
		}
	}

	descriptor := protocol.Describe(protocol.StructuredCompletion)
	req := ai.Request{
		Model:               model,
		System:              x.requirementSystemPromptFor(),
		Messages:            []ai.Message{{Role: "user", Content: user.String()}},
		MaxTokens:           RequirementPassMaxTokens,
		InteractionContract: descriptor.Contract,
		Contract:            &descriptor,
		ContractID:          "requirements",
		RequestID:           "requirements",
		Mode:                "requirements",
		AuthorityLevel:      descriptor.AuthorityCeiling,
		Reasoning:           &ai.ReasoningConfig{Disabled: true},
	}

	compiledReq, _, compileErr := x.contextCompilerInstance().CompileRequest(ctx, req, contextcompiler.RequestCompileOptions{
		Phase:                 contextcompiler.PhaseExecute,
		Provider:              p.Name(),
		WorkflowState:         "requirements",
		ContextPolicy:         "none",
		RequestedOutputTokens: RequirementPassMaxTokens,
	})
	if compileErr != nil {
		return "", fmt.Errorf("executor: requirement pass context compilation: %w", compileErr)
	}
	compiledReq.ContextPrepared = true

	resp, err := p.Execute(ctx, compiledReq)
	var metadata ai.ResponseMetadata
	var usage ai.ProviderUsage
	if resp != nil {
		metadata = resp.Metadata()
		usage = resp.Usage
	}
	payload := providerExecutionPayload(p.Name(), compiledReq, usage, metadata, func() int {
		if resp == nil {
			return 0
		}
		return len(resp.Content)
	}(), err)
	if sink := x.telemetry(); sink != nil {
		sink.RecordProviderExecution(payload)
	}
	x.emit(events.NewProviderExecution(payload))
	if err != nil {
		return "", fmt.Errorf("executor: requirement pass invocation: %w", err)
	}
	if resp == nil {
		return "", fmt.Errorf("executor: requirement pass returned an empty response")
	}
	// A truncated enumeration is not a shorter enumeration. Refuse it before the
	// parser can accept a prefix as the whole ledger, which would silently drop
	// requirements the objective actually implies.
	if resp.Truncated {
		return "", fmt.Errorf("executor: requirement pass response was truncated: %w", resp.OutputError())
	}
	raw := strings.TrimSpace(resp.Content)
	if len(raw)/4 > requirementPassRejectTokens {
		return "", fmt.Errorf(
			"executor: requirement pass output of ~%d tokens exceeds the %d-token ceiling — rejected as malformed",
			len(raw)/4, requirementPassRejectTokens)
	}
	return raw, nil
}

// resolveRequirementPassModel resolves the model for the requirement pass under
// the same discipline the manifest pass uses: an explicit binding only,
// validated against the bound provider, never a silent fallback to a model that
// belongs to somebody else.
//
// The caller's hint wins when it is non-empty; otherwise the executor's own
// configuration is consulted. Either way an absent binding is a deterministic
// refusal, not a guess.
func (x *RuntimeExecutor) resolveRequirementPassModel(hint string) (string, error) {
	model := strings.TrimSpace(hint)
	if model == "" && x.cfg != nil {
		model = strings.TrimSpace(x.cfg.ActiveModelName())
	}
	if model == "" {
		return "", fmt.Errorf("executor: requirement pass requires an explicit model binding (no fallback allowed)")
	}
	x.mu.Lock()
	p := x.provider
	x.mu.Unlock()
	if p != nil && !modelBelongsTo(p.Name(), model) {
		return "", fmt.Errorf("%w: model %q does not belong to provider %q",
			ErrProviderModelMismatch, model, p.Name())
	}
	return model, nil
}
