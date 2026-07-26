package llm

import (
	"fmt"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/core/artifact"
	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/capability"
	"github.com/PizenLabs/izen/internal/core/classifier"
	"github.com/PizenLabs/izen/internal/core/workflow"
)

// ─── Proposal Types ──────────────────────────────────────────────────────────

type IntentProposal struct {
	Prompt  string
	Mode    string
	Created time.Time
}

type PlanProposal struct {
	Steps    []string
	Strategy string
	Delta    budget.BudgetDelta
	Created  time.Time
}

type PatchProposal struct {
	File    string
	Content string
	Diff    string
	Created time.Time
}

type FailureClassification struct {
	Class   classifier.FailureClass
	Reason  string
	Details map[string]string
}

// ─── ProposalGenerator ───────────────────────────────────────────────────────

type ProposalGenerator struct {
	store    *artifact.Store
	authEng  *authorization.AuthorizationEngine
	classify *classifier.FailureClassifier
	workflow *workflow.WorkflowStateMachine
}

func NewProposalGenerator(
	store *artifact.Store,
	authEng *authorization.AuthorizationEngine,
	classify *classifier.FailureClassifier,
	wf *workflow.WorkflowStateMachine,
) *ProposalGenerator {
	return &ProposalGenerator{
		store:    store,
		authEng:  authEng,
		classify: classify,
		workflow: wf,
	}
}

func (pg *ProposalGenerator) Store() *artifact.Store                         { return pg.store }
func (pg *ProposalGenerator) AuthEngine() *authorization.AuthorizationEngine { return pg.authEng }
func (pg *ProposalGenerator) Classifier() *classifier.FailureClassifier      { return pg.classify }

// ─── Intent Proposal ─────────────────────────────────────────────────────────

func ParseIntentProposal(llmOutput, mode string) *IntentProposal {
	content := SanitizeOutput(llmOutput)
	if content == "" {
		return nil
	}
	return &IntentProposal{
		Prompt:  content,
		Mode:    mode,
		Created: time.Now().UTC(),
	}
}

func (ip *IntentProposal) ToArtifact() *artifact.IntentArtifact {
	return artifact.NewIntentArtifact(ip.Prompt, ip.Mode)
}

func (pg *ProposalGenerator) SubmitIntent(llmOutput, mode string) (*artifact.IntentArtifact, error) {
	proposal := ParseIntentProposal(llmOutput, mode)
	if proposal == nil {
		return nil, fmt.Errorf("proposal: empty LLM output for intent")
	}
	ia := proposal.ToArtifact()
	if err := pg.store.Save(ia); err != nil {
		return nil, fmt.Errorf("proposal: save intent: %w", err)
	}
	return ia, nil
}

// ─── Plan Proposal ───────────────────────────────────────────────────────────

func ParsePlanProposal(llmOutput string) *PlanProposal {
	content := SanitizeOutput(llmOutput)
	if content == "" {
		return nil
	}

	lines := strings.Split(content, "\n")
	var steps []string
	strategy := "unknown"

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(trimmed), "strategy:") {
			strategy = strings.TrimSpace(strings.TrimPrefix(trimmed, "strategy:"))
			strategy = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(strategy), "strategy:"))
			continue
		}
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "1.") {
			step := strings.TrimSpace(trimmed[2:])
			if step != "" {
				steps = append(steps, step)
			}
		}
	}
	if len(steps) == 0 {
		for idx, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" {
				steps = append(steps, fmt.Sprintf("step_%d: %s", idx+1, trimmed))
			}
		}
	}

	return &PlanProposal{
		Steps:    steps,
		Strategy: strategy,
		Delta:    budget.BudgetDelta{Files: len(steps), DiffLines: 100, Tokens: 2000, Attempts: 1},
		Created:  time.Now().UTC(),
	}
}

func (pp *PlanProposal) ToArtifact() *artifact.PlanArtifact {
	return artifact.NewPlanArtifact(pp.Steps, pp.Strategy)
}

func (pg *ProposalGenerator) SubmitPlan(llmOutput string) (*artifact.PlanArtifact, error) {
	proposal := ParsePlanProposal(llmOutput)
	if proposal == nil {
		return nil, fmt.Errorf("proposal: empty LLM output for plan")
	}
	if len(proposal.Steps) == 0 {
		return nil, fmt.Errorf("proposal: no plan steps found in LLM output")
	}
	pa := proposal.ToArtifact()
	if err := pg.store.Save(pa); err != nil {
		return nil, fmt.Errorf("proposal: save plan: %w", err)
	}
	return pa, nil
}

// ─── Patch Proposal ──────────────────────────────────────────────────────────

func ParsePatchProposal(llmOutput string) []*PatchProposal {
	content := SanitizeOutput(llmOutput)
	if content == "" {
		return nil
	}

	var proposals []*PatchProposal
	lines := strings.Split(content, "\n")
	var currentFile string
	var currentContent strings.Builder
	inFile := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "FILE:") || strings.HasPrefix(trimmed, "file:") {
			if inFile && currentFile != "" {
				proposals = append(proposals, &PatchProposal{
					File:    currentFile,
					Content: strings.TrimSpace(currentContent.String()),
					Created: time.Now().UTC(),
				})
			}
			currentFile = strings.TrimSpace(strings.TrimPrefix(trimmed, "FILE:"))
			currentFile = strings.TrimSpace(strings.TrimPrefix(currentFile, "file:"))
			currentContent.Reset()
			inFile = true
			continue
		}
		if inFile {
			currentContent.WriteString(line)
			currentContent.WriteString("\n")
		}
	}
	if inFile && currentFile != "" {
		proposals = append(proposals, &PatchProposal{
			File:    currentFile,
			Content: strings.TrimSpace(currentContent.String()),
			Created: time.Now().UTC(),
		})
	}

	return proposals
}

func (pp *PatchProposal) ToArtifact() *artifact.PatchArtifact {
	changes := []string{pp.File}
	return artifact.NewPatchArtifact(pp.Content, changes)
}

func (pg *ProposalGenerator) SubmitPatch(llmOutput string) ([]artifact.Artifact, error) {
	proposals := ParsePatchProposal(llmOutput)
	if len(proposals) == 0 {
		return nil, fmt.Errorf("proposal: no patch proposals found in LLM output")
	}

	var artifacts []artifact.Artifact
	for _, p := range proposals {
		pa := p.ToArtifact()
		if err := pg.store.Save(pa); err != nil {
			return artifacts, fmt.Errorf("proposal: save patch: %w", err)
		}
		artifacts = append(artifacts, pa)
	}
	return artifacts, nil
}

// ─── Failure Classification ──────────────────────────────────────────────────

func (pg *ProposalGenerator) ClassifyFailure(output string, exitCode int) FailureClassification {
	result := pg.classify.Classify(output, exitCode)

	fc := FailureClassification{
		Class:   result.Class,
		Reason:  result.Reason,
		Details: result.Details,
	}

	if result.Class == classifier.FailureUnknownClass && pg.workflow != nil {
		_ = pg.workflow.SendEvent(workflow.EventFailureIdentified, workflow.TransitionContext{
			FailureClass: result.Class,
		})
	}

	return fc
}

func (fc FailureClassification) IsUnknown() bool {
	return fc.Class == classifier.FailureUnknownClass
}

func (fc FailureClassification) String() string {
	return fmt.Sprintf("FailureClassification{class=%s reason=%q}", fc.Class, fc.Reason)
}

// ─── Proposal → Authorization Pipeline ───────────────────────────────────────

func (pg *ProposalGenerator) SubmitProposalToAuthorization(
	intent *artifact.IntentArtifact,
	plan *artifact.PlanArtifact,
	patch *artifact.PatchArtifact,
	requiredCaps authorization.CapabilityFlags,
	caps *capability.CapabilitySet,
) (*authorization.MutationAuthorization, error) {
	proposal := &authorization.MutationProposal{
		IntentID:     intent.ID(),
		PlanID:       plan.ID(),
		PatchID:      patch.ID(),
		TargetFiles:  patch.Changes,
		RequiredCaps: requiredCaps,
		EstimatedDelta: budget.BudgetDelta{
			Files:     len(patch.Changes),
			DiffLines: 100,
			Tokens:    2000,
			Attempts:  1,
		},
		CreatedAt: time.Now().UTC(),
	}

	auth, err := pg.authEng.Evaluate(proposal, plan, patch, caps, nil, nil, false, false)
	if err != nil {
		return nil, fmt.Errorf("authorization: %w", err)
	}
	return auth, nil
}
