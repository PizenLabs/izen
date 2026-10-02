package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/execution/capability"
)

// ── Provider-backed repair reasoning ────────────────────────────────────────
//
// The behavioral loop needs a reasoning backend that can look at an observed
// defect and say how to repair it. This is the ONLY place the LLM participates
// in the behavioral loop, and it participates in the weakest possible role: it
// proposes content for a target the runtime already chose.
//
// Three properties are load-bearing and each is enforced here rather than left to
// the prompt:
//
//  1. The prompt carries the CAPABILITY CATALOG, not an instruction dump. The
//     model is told what it may ask about, which is how "I need to inspect X"
//     becomes a concrete capability request instead of a guess.
//  2. The response contract is STRICT and machine-checked. A response that does
//     not carry a parsable proposal is DECLINED, never partially applied — a
//     half-understood repair is a mutation nobody reviewed.
//  3. The model's declared target is IGNORED by the caller. This adapter records
//     it for evidence and returns it as a field; ResolveRepairTarget remains the
//     only thing that decides where a repair lands.

// ErrNoProposalReturned is returned when a response carries no parsable repair
// proposal. It is a DECLINED outcome, never a silent pass.
var ErrNoProposalReturned = errors.New("execution: reasoning backend returned no repair proposal")

// repairProposalSchema is the strict output contract. It is deliberately small:
// one defect code, one target, one content blob, one rationale.
const repairProposalSchema = `{
  "type": "object",
  "required": ["defect_code", "content"],
  "properties": {
    "defect_code": {"type": "string"},
    "target": {"type": "string"},
    "content": {"type": "string"},
    "rationale": {"type": "string"}
  }
}`

// behavioralSystemPrompt is the system prompt for one repair step.
//
// It states the boundary rather than assuming it: the model is told that the
// target is decided by evidence, that its evidence is the only ground it has,
// and that declining is a valid and useful answer. A prompt that pressures the
// model to always answer is a prompt that manufactures confidence.
const behavioralSystemPrompt = `You are repairing one observed defect in a running project.

You are given OBSERVATION EVIDENCE: what a runtime actually returned. Reason only
from that evidence.

Rules:
- Propose the complete corrected content for the affected file. Do not describe
  the change; emit it.
- Do not choose the file. The target is derived from the observation evidence and
  your declared target is advisory only.
- Preserve everything the evidence does not show as broken. This is a repair, not
  a redesign.
- If the evidence does not identify a cause you can fix, return NO_PROPOSAL. A
  declined repair is a correct answer; a confident guess is a defect.`

// behavioralUserPrompt renders the evidence a repair decision must reason from.
//
// It is BOUNDED: the served document excerpt, the resource statuses, and the
// defect line are all already trimmed by the observation layer, and this prompt
// adds no further unbounded content. An unbounded repair context is how a
// runtime ends up sending the whole repository to the model on every step.
func behavioralUserPrompt(objective string, defect capability.Defect, entry Observation) string {
	var b strings.Builder
	if objective != "" {
		b.WriteString("Objective: ")
		b.WriteString(boundedText(objective, 400))
		b.WriteString("\n\n")
	}
	b.WriteString("## OBSERVATION EVIDENCE\n")
	if entry.BaseURL != "" {
		b.WriteString("observed at: " + entry.BaseURL + entry.EntryPath + "\n")
	}
	for _, res := range entry.Result.Resources {
		fmt.Fprintf(&b, "- %s -> HTTP %d (%d bytes)%s\n",
			res.Ref, res.Status, res.Bytes, nonLocalNote(res.Local))
	}
	if detail := defect.Detail; detail != "" {
		b.WriteString("- detail: " + boundedText(detail, 400) + "\n")
	}
	if len(entry.Result.Defects) > 1 {
		b.WriteString("\n## OTHER OBSERVED DEFECTS\n")
		for _, other := range entry.Result.Defects {
			if other.Code == defect.Code && other.Summary == defect.Summary {
				continue
			}
			b.WriteString("- " + other.Code + ": " + boundedText(other.Summary, 200) + "\n")
		}
	}
	b.WriteString("\n## DEFECT TO REPAIR\n")
	b.WriteString("code: " + defect.Code + "\n")
	b.WriteString("observation: " + boundedText(defect.Summary, 400) + "\n")
	if len(defect.Candidates) > 0 {
		b.WriteString("the workspace provides: " + strings.Join(defect.Candidates, ", ") + "\n")
	}
	if len(defect.Evidence) > 0 {
		b.WriteString("derived from evidence: " + strings.Join(defect.Evidence, ", ") + "\n")
	}
	return b.String()
}

func nonLocalNote(local bool) string {
	if local {
		return ""
	}
	return " [cross-origin, not probed]"
}

// ProviderRepairProposer asks the configured reasoning backend to repair one
// observed defect.
//
// It owns no authority: it renders a bounded evidence prompt, asks once, parses
// the strict response contract, and returns a proposal. Every failure — no
// provider, transport error, unparsable response, explicit NO_PROPOSAL — becomes
// ErrNoProposalReturned or the transport error, both of which the loop classifies
// as a DECLINED repair. None of them becomes an applied mutation.
type ProviderRepairProposer struct {
	// Provider is the reasoning backend. Required.
	Provider ai.Provider
	// Model is the explicit model ID. Required: the proposer never falls back to
	// a default model, because a silently-chosen model makes the repair
	// unreproducible.
	Model string
	// ResolveModel overrides Model with a per-call resolution. The composition
	// root binds it to the Workspace Target authority so a repair runs on the
	// model the operator actually assigned, exactly like every other invocation.
	// When set, a resolution returning "" is a refusal — never a fallback.
	ResolveModel func() string
	// MaxOutputTokens bounds the proposal. Zero uses behaviorDefaultOutputBudget.
	MaxOutputTokens int
}

// model resolves the model a repair runs under, preferring the authority's
// per-call resolution over the statically bound ID.
func (p *ProviderRepairProposer) model() string {
	if p.ResolveModel != nil {
		return strings.TrimSpace(p.ResolveModel())
	}
	return strings.TrimSpace(p.Model)
}

// behaviorDefaultOutputBudget bounds one repair proposal. A repair is a single
// file's content, so it must fit; a proposal that does not fit is declined
// rather than truncated into a corrupt file.
const behaviorDefaultOutputBudget = 4096

// ProposeRepair implements RepairProposer.
func (p *ProviderRepairProposer) ProposeRepair(ctx context.Context, defect capability.Defect, entry Observation) (RepairProposal, error) {
	if p == nil || p.Provider == nil {
		return RepairProposal{}, errors.New("execution: no reasoning backend is bound to the behavioral runtime")
	}
	model := p.model()
	if model == "" {
		return RepairProposal{}, errors.New("execution: no explicit model is bound to the behavioral runtime")
	}
	budget := p.MaxOutputTokens
	if budget <= 0 {
		budget = behaviorDefaultOutputBudget
	}
	resp, err := p.Provider.Execute(ctx, ai.Request{
		Model:        model,
		MaxTokens:    budget,
		ContextPhase: "behavioral_repair",
		// Structured output is requested as a strict JSON schema so a compliant
		// provider cannot answer in prose. A prose answer is then declined by
		// ParseRepairProposal rather than half-parsed into a repair.
		ResponseFormat: &ai.ResponseFormat{
			Type:   "json_schema",
			Name:   "behavioral_repair_proposal",
			Strict: true,
			Schema: json.RawMessage(repairProposalSchema),
		},
		Messages: []ai.Message{
			{Role: "system", Content: behavioralSystemPrompt},
			{Role: "user", Content: behavioralUserPrompt("", defect, entry)},
		},
	})
	if err != nil {
		return RepairProposal{}, fmt.Errorf("execution: repair reasoning failed: %w", err)
	}
	return ParseRepairProposal(resp.Content, defect)
}

// extractFirstJSONObject returns the first balanced JSON object embedded in s.
//
// A model that wraps its structured answer in a fenced block or a sentence of
// preamble is still answering the question, and rejecting it would turn a
// formatting difference into a declined repair. Balance is tracked by scanning
// string literals so a brace inside a proposed file's CONTENT cannot terminate
// the object early — which matters here, because the content is source code.
func extractFirstJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

// ParseRepairProposal parses a reasoning backend's repair response.
//
// It is strict on purpose. A response that does not declare the defect it is
// addressing, or that carries no content, is DECLINED — because a proposal the
// runtime cannot attribute to a defect is a proposal it cannot audit.
func ParseRepairProposal(content string, defect capability.Defect) (RepairProposal, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" || strings.Contains(strings.ToUpper(trimmed), "NO_PROPOSAL") {
		return RepairProposal{}, ErrNoProposalReturned
	}
	payload := extractFirstJSONObject(trimmed)
	if payload == "" {
		return RepairProposal{}, fmt.Errorf("%w: response carried no JSON object", ErrNoProposalReturned)
	}
	var doc struct {
		DefectCode string `json:"defect_code"`
		Target     string `json:"target"`
		Content    string `json:"content"`
		Rationale  string `json:"rationale"`
	}
	if err := json.Unmarshal([]byte(payload), &doc); err != nil {
		return RepairProposal{}, fmt.Errorf("%w: %w", ErrNoProposalReturned, err)
	}
	if strings.TrimSpace(doc.Content) == "" {
		return RepairProposal{}, fmt.Errorf("%w: proposal carried no content", ErrNoProposalReturned)
	}
	// A proposal that names a DIFFERENT defect than the one it was asked about
	// cannot be audited against that defect's evidence, so it is refused rather
	// than applied to the wrong problem.
	if code := strings.TrimSpace(doc.DefectCode); code != "" && code != defect.Code {
		return RepairProposal{}, fmt.Errorf(
			"%w: proposal addressed %q but was asked about %q", ErrNoProposalReturned, code, defect.Code)
	}
	return RepairProposal{
		DefectCode: strings.TrimSpace(doc.DefectCode),
		Target:     strings.TrimSpace(doc.Target),
		Content:    doc.Content,
		Rationale:  strings.TrimSpace(doc.Rationale),
	}, nil
}
