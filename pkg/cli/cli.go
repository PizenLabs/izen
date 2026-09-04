// Package cli wires the Izen control-plane orchestrator into a headless CLI.
// It composes the preflight engine, the proposal provider (an LLM adapter),
// the terminal UI projection bridge, and the approval gate into a single
// Stack, and exposes a Run helper that executes one prompt through the full
// deterministic control loop:
//
//	Preflight -> LLM Proposal -> Validation -> Gate Arming
//	          -> Authorization -> Atomic Commit
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PizenLabs/izen/pkg/projection/diff"
	"github.com/PizenLabs/izen/pkg/provider/capability"
	"github.com/PizenLabs/izen/pkg/runtime/authorization"
	runtimectx "github.com/PizenLabs/izen/pkg/runtime/context"
	"github.com/PizenLabs/izen/pkg/runtime/executor"
	"github.com/PizenLabs/izen/pkg/runtime/gate"
	"github.com/PizenLabs/izen/pkg/runtime/harness"
	"github.com/PizenLabs/izen/pkg/runtime/orchestrator"
	"github.com/PizenLabs/izen/pkg/runtime/preflight"
	"github.com/PizenLabs/izen/pkg/runtime/target"
	"github.com/PizenLabs/izen/pkg/runtime/ui/decision"
)

// DefaultTokenBudget is the default context token budget applied by Stack.Run.
const DefaultTokenBudget = 12000

// manifestNames is the set of dependency manifest basenames scanned for
// context units, mirroring the preflight risk classifier.
var manifestNames = []string{"go.mod", "go.sum", "package.json", "Cargo.toml", "Cargo.lock", "pyproject.toml", "Makefile"}

// LLMProvider is the minimal non-deterministic completion contract required by
// the proposal provider. cmd/izen adapts its configured AI provider to this
// interface.
type LLMProvider interface {
	Complete(ctx context.Context, system, prompt string) (string, error)
}

// ProposalProviderCLI implements orchestrator.ProposalProvider over an
// LLMProvider. The model response is treated as the full post-mutation content
// of the target file (optional markdown fences are stripped), and the resolved
// target reference is made absolute against the workspace root so the executor
// never writes relative to the process working directory.
type ProposalProviderCLI struct {
	llm  LLMProvider
	root string
}

// NewProposalProvider returns a ProposalProviderCLI backed by llm and rooted
// at root.
func NewProposalProvider(llm LLMProvider, root string) *ProposalProviderCLI {
	return &ProposalProviderCLI{llm: llm, root: root}
}

// GenerateProposal completes the compiled prompt and wraps the response in a
// ProposedMutation stamped with a fresh proposal ID and the resolved target.
func (p *ProposalProviderCLI) GenerateProposal(ctx context.Context, req *preflight.CompiledRequest) (*executor.ProposedMutation, error) {
	if p == nil || p.llm == nil {
		return nil, errors.New("cli: proposal provider has no LLM")
	}
	if req == nil || req.TargetRef == nil {
		return nil, errors.New("cli: proposal provider requires a compiled request with a target")
	}

	content, err := p.llm.Complete(ctx, systemPrompt(req), req.FormattedPrompt)
	if err != nil {
		return nil, fmt.Errorf("cli: generate proposal: %w", err)
	}
	if strings.TrimSpace(content) == "" {
		return nil, errors.New("cli: model returned an empty proposal")
	}

	ref := *req.TargetRef
	if !filepath.IsAbs(ref.Canonical) {
		ref.Canonical = filepath.Join(p.root, ref.Canonical)
	}

	return &executor.ProposedMutation{
		ProposalID: NewProposalID(),
		TargetRef:  &ref,
		RawPatch:   stripCodeFence(content),
	}, nil
}

// proposalSeq is a monotonic atomic sequence appended to the nanosecond clock
// so NewProposalID never collides even under rapid concurrent generation.
var proposalSeq atomic.Uint64

// NewProposalID returns a fresh, collision-resistant proposal ID.
func NewProposalID() string {
	seq := proposalSeq.Add(1)
	return fmt.Sprintf("cli-%d-%d", time.Now().UnixNano(), seq)
}

// systemPrompt instructs the model to emit exactly the post-mutation target
// content, giving it the resolved target identity and risk level.
func systemPrompt(req *preflight.CompiledRequest) string {
	var b strings.Builder
	b.WriteString("You are the Izen execution engine. The user intent and compiled context ")
	b.WriteString("follow in the <izen_task> envelope. Respond with ONLY the complete ")
	b.WriteString("post-mutation content of the target file, either raw or inside a single ")
	b.WriteString("markdown code fence. No commentary, no diff headers.")
	if req != nil && req.TargetRef != nil {
		fmt.Fprintf(&b, "\nTarget file: %s\nRisk: %s", req.TargetRef.Canonical, req.Risk.String())
	}
	return b.String()
}

// stripCodeFence removes a single surrounding markdown code fence from s when
// present, preserving the inner content byte-for-byte.
func stripCodeFence(s string) string {
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "```") {
		return s
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 2 {
		return ""
	}
	lines = lines[1:]
	if last := strings.TrimSpace(lines[len(lines)-1]); strings.HasPrefix(last, "```") {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// TerminalBridge implements orchestrator.UIProjectionBridge for a headless
// terminal: it renders the proposal diff, prompts for an approval decision,
// and reads the user's answer from a reader.
type TerminalBridge struct {
	in  io.Reader
	out io.Writer

	mu    sync.Mutex
	epoch authorization.InteractionEpoch
	armed bool
}

// NewTerminalBridge returns a bridge reading approval input from in and
// rendering to out.
func NewTerminalBridge(in io.Reader, out io.Writer) *TerminalBridge {
	return &TerminalBridge{in: in, out: out}
}

// RenderProposal renders the diff evidence using the viewport budget.
func (b *TerminalBridge) RenderProposal(evidence diff.MutationEvidence, cfg diff.ViewportConfig) error {
	if b == nil {
		return errors.New("cli: nil terminal bridge")
	}
	if b.out == nil {
		return errors.New("cli: terminal bridge has no output writer")
	}
	plan := diff.ComputeRenderPlan(evidence, cfg)
	if _, err := fmt.Fprintf(b.out, "izen: proposal diff %s (+%d -%d)\n", evidence.TargetFile, evidence.Added, evidence.Deleted); err != nil {
		return fmt.Errorf("cli: render proposal: %w", err)
	}
	for _, ln := range plan.VisibleLines {
		if _, err := fmt.Fprintf(b.out, "%s %s\n", diffMarker(ln.Type), ln.Content); err != nil {
			return fmt.Errorf("cli: render proposal: %w", err)
		}
	}
	if plan.TruncatedAt > 0 {
		if _, err := fmt.Fprintf(b.out, "izen: ... %d lines omitted ...\n", plan.TruncatedAt); err != nil {
			return fmt.Errorf("cli: render proposal: %w", err)
		}
	}
	return nil
}

// RenderDecisionSurface renders the annotated decision surface with explicit
// risk hierarchy. It is the sole UI entry for the hard-gated DecisionSurface.
func (b *TerminalBridge) RenderDecisionSurface(surface decision.Surface) error {
	if b == nil {
		return errors.New("cli: nil terminal bridge")
	}
	if b.out == nil {
		return errors.New("cli: terminal bridge has no output writer")
	}
	rendered := surface.Render(80)
	if _, err := fmt.Fprintln(b.out, rendered); err != nil {
		return fmt.Errorf("cli: render decision surface: %w", err)
	}
	return nil
}

// diffMarker returns the one-cell gutter marker for a mutation type.
func diffMarker(t diff.MutationType) string {
	switch t {
	case diff.MutationAdd:
		return "+"
	case diff.MutationDelete:
		return "-"
	default:
		return " "
	}
}

// OnSessionArmed records the armed epoch so WaitForApproval can stamp events
// with the authoritative session epoch.
func (b *TerminalBridge) OnSessionArmed(epoch authorization.InteractionEpoch) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.epoch = epoch
	b.armed = true
}

// WaitForApproval prompts for a decision and blocks until an explicit answer
// (or context cancellation) is received. "y"/"yes" executes, "i"/"inspect"
// grants inspect-only access, and anything else (including Enter) cancels.
func (b *TerminalBridge) WaitForApproval(ctx context.Context) (authorization.ApprovalEvent, error) {
	if b == nil {
		return authorization.ApprovalEvent{}, errors.New("cli: nil terminal bridge")
	}
	if b.in == nil || b.out == nil {
		return authorization.ApprovalEvent{}, errors.New("cli: terminal bridge is not fully wired")
	}
	b.mu.Lock()
	epoch := b.epoch
	armed := b.armed
	b.mu.Unlock()
	if !armed {
		return authorization.ApprovalEvent{}, errors.New("cli: approval requested before the session was armed")
	}

	if _, err := fmt.Fprint(b.out, "Apply this proposal? [y/N] "); err != nil {
		return authorization.ApprovalEvent{}, fmt.Errorf("cli: render approval prompt: %w", err)
	}
	ch := make(chan readResult, 1)
	go func() {
		ch <- readLine(b.in)
	}()
	select {
	case <-ctx.Done():
		return authorization.ApprovalEvent{}, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return authorization.ApprovalEvent{}, fmt.Errorf("cli: read approval: %w", r.err)
		}
		return approvalEventForInput(epoch, r.line), nil
	}
}

// readResult carries one line read from an input reader.
type readResult struct {
	line string
	err  error
}

// readLine reads a single line from r, treating EOF without a newline as an
// empty decision.
func readLine(r io.Reader) readResult {
	sc := bufio.NewScanner(r)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return readResult{err: err}
		}
		return readResult{line: ""}
	}
	return readResult{line: sc.Text()}
}

// approvalEventForInput maps a user answer to an ApprovalEvent stamped with
// the current session epoch.
func approvalEventForInput(epoch authorization.InteractionEpoch, line string) authorization.ApprovalEvent {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return authorization.ApprovalEvent{Epoch: epoch, Action: authorization.ActionExecute}
	case "i", "inspect":
		return authorization.ApprovalEvent{Epoch: epoch, Action: authorization.ActionInspect}
	default:
		return authorization.ApprovalEvent{Epoch: epoch, Action: authorization.ActionCancel}
	}
}

// Stack is the wired composition root for the CLI control-plane path. It is
// assembled by Wire and consumed by Run.
type Stack struct {
	Preflight    *preflight.PreflightEngine
	Validator    *executor.ProposalValidator
	Executor     *executor.RuntimeExecutor
	Gate         *authorization.ApprovalGate
	Provider     *ProposalProviderCLI
	Bridge       *TerminalBridge
	Orchestrator *orchestrator.Orchestrator
	TokenBudget  int
	Viewport     diff.ViewportConfig

	// New runtime invariants (wired for DI audit and hard-gate enforcement).
	BudgetGate      *preflight.Gate
	HarnessPipeline *harness.ExtractorPipeline
	GatePipeline    *gate.Pipeline
	Loop            *orchestrator.Loop
	SnapshotReader  orchestrator.SnapshotReader
	DecisionSurface *decision.Surface
}

// Wire assembles the full control-plane stack: the preflight engine (resolver
// + context compiler), the proposal provider over llm rooted at root, the
// terminal UI bridge over in/out, the deterministic validator, the atomic
// executor, and a zero-delay approval gate so authorization decisions are
// honored immediately.
//
// It also wires the new runtime invariants: preflight.NewGate with
// EvaluateBudgetGate, decision.NewSurface with AnnotateStrategies,
// harness.NewExtractorPipeline, gate.NewPipeline, and
// orchestrator.NewLoop with SnapshotReader dependency injection.
func Wire(llm LLMProvider, root string, in io.Reader, out io.Writer) *Stack {
	apGate := authorization.NewGate(authorization.WithMinDelayWindow(0))
	pf := preflight.NewEngine(target.NewTargetResolver(), runtimectx.NewCompiler())
	val := executor.NewValidator()
	exec := executor.NewExecutor()

	// New invariants wiring (DI audit).
	budgetGate := preflight.NewGate()
	harnessPipeline := harness.NewExtractorPipeline()
	gatePipeline := gate.NewPipeline()
	snapshotReader := orchestrator.FSSnapshotReader{}
	// Loop is wired with SnapshotReader DI; harness extractor is adapted per-target at Run time.
	loop := orchestrator.NewLoop(
		&orchestrator.MemoryBackedExtractor{Pipeline: harnessPipeline, TargetFile: ""},
		gatePipeline,
		exec,
		snapshotReader,
	)

	return &Stack{
		Preflight:       pf,
		Validator:       val,
		Executor:        exec,
		Gate:            apGate,
		Provider:        NewProposalProvider(llm, root),
		Bridge:          NewTerminalBridge(in, out),
		Orchestrator:    orchestrator.NewOrchestrator(pf, val, exec, apGate),
		TokenBudget:     DefaultTokenBudget,
		Viewport:        DefaultViewport(),
		BudgetGate:      budgetGate,
		HarnessPipeline: harnessPipeline,
		GatePipeline:    gatePipeline,
		Loop:            loop,
		SnapshotReader:  snapshotReader,
	}
}

// DefaultViewport returns a reasonable headless terminal viewport budget.
func DefaultViewport() diff.ViewportConfig {
	return diff.ViewportConfig{
		TermWidth:   100,
		TermHeight:  24,
		GutterWidth: 2,
		PrefixWidth: 1,
	}
}

// Run executes one prompt through the full control-loop cycle. The prompt is
// treated as the target file reference (for example "README.md").
// It enforces the hard-gate invariant: if EvaluateBudgetGate returns
// BudgetExceeded, the loop parks at the DecisionSurface with FULL_REWRITE
// explicitly disabled, and never invokes the model.
func (s *Stack) Run(ctx context.Context, root, prompt string) (*orchestrator.ExecutionResult, error) {
	if s == nil {
		return nil, errors.New("cli: nil stack")
	}
	if s.Orchestrator == nil {
		return nil, errors.New("cli: stack has no orchestrator")
	}
	budget := s.TokenBudget
	if budget <= 0 {
		budget = DefaultTokenBudget
	}

	// --- Hard-gate preflight: single snapshot read, no repetitive disk I/O ---
	var targetState preflight.TargetState
	var gateResult preflight.BudgetGateResult
	var astStatus preflight.ASTStatus
	var snapshotContent []byte
	var targetPath string

	if s.BudgetGate != nil && s.SnapshotReader != nil {
		// Resolve target without re-reading file multiple times.
		// Handle free-form prompts like "check this file @index.html and rewrite it".
		effectivePrompt := prompt
		if extracted := extractTargetFromPrompt(root, prompt); extracted != "" {
			effectivePrompt = extracted
		}
		if ref, err := target.NewTargetResolver().Resolve(root, effectivePrompt); err == nil && ref != nil && ref.Canonical != "" { //nolint:contextcheck
			canonical := ref.Canonical
			if !filepath.IsAbs(canonical) {
				targetPath = filepath.Join(root, canonical)
			} else {
				targetPath = canonical
			}
			// Single snapshot read per cycle (Observation phase).
			if data, err := s.SnapshotReader.ReadSnapshot(ctx, targetPath); err == nil {
				snapshotContent = data
			} else {
				// Treat missing or error as empty for gate purposes (conservative).
				snapshotContent = nil
			}
			astStatus = preflight.InferASTStatus(snapshotContent, targetPath)
			targetState = preflight.TargetState{
				Path:      targetPath,
				Content:   snapshotContent,
				ASTStatus: astStatus,
			}
			caps := capability.ModelCapabilities{
				MaxOutputTokens: budget,
			}
			gateResult = s.BudgetGate.EvaluateBudgetGate(targetState, caps)
			// Hard-gate enforcement: park at DecisionSurface if AST corrupt or budget exceeded.
			if astStatus == preflight.ASTCorrupt || gateResult.BudgetStatus == preflight.BudgetExceeded || gateResult.FullRewrite == preflight.StrategyForbidden {
				surface := decision.NewSurface(targetPath, astStatus, &gateResult)
				decision.AnnotateStrategies(&surface)
				if s.Bridge != nil {
					_ = s.Bridge.RenderDecisionSurface(surface)
				} else {
					// Fallback direct render to Bridge.out if Bridge is nil (should not happen)
					fmt.Fprintln(os.Stderr, surface.Render(80))
				}
				// Ensure FULL_REWRITE is explicitly disabled in the parked surface.
				// The surface already carries the [DISABLED: Exceeds Model Output Budget] annotation.
				// Return a parked result without invoking the model.
				return &orchestrator.ExecutionResult{
					Target: targetPath,
					Action: authorization.ActionCancel,
					Evidence: diff.MutationEvidence{
						TargetFile: targetPath,
					},
				}, nil
			}
		}
	}

	// Build candidate units from the single snapshot (zero repetitive reads).
	var units []runtimectx.ContextUnit
	if len(snapshotContent) > 0 && targetPath != "" {
		// Use snapshot content directly; do not re-read target file.
		rel := targetPath
		if !filepath.IsAbs(rel) {
			rel = targetPath
		} else if relPath, err := filepath.Rel(root, targetPath); err == nil {
			rel = relPath
		}
		units = append(units, runtimectx.ContextUnit{
			ID:        "target",
			Kind:      runtimectx.KindTargetState,
			Source:    rel,
			Content:   string(snapshotContent),
			TokenCost: tokenEstimate(len(snapshotContent)),
			Relevance: 1.0,
		})
	} else {
		units = candidateUnits(root, prompt) //nolint:contextcheck // target resolver API predates context propagation
		// If we already have snapshot content, we already accounted for target;
		// candidateUnits would re-read target — filter it out to preserve zero redundancy.
		// Keep only manifest units.
		if len(snapshotContent) > 0 {
			filtered := make([]runtimectx.ContextUnit, 0, len(units))
			for _, u := range units {
				if u.Kind != runtimectx.KindTargetState {
					filtered = append(filtered, u)
				}
			}
			// Prepend snapshot-based target unit.
			if len(filtered) < len(units) {
				units = filtered
				// Re-add snapshot unit at front
				rel := targetPath
				if rp, err := filepath.Rel(root, targetPath); err == nil {
					rel = rp
				}
				units = append([]runtimectx.ContextUnit{{
					ID:        "target",
					Kind:      runtimectx.KindTargetState,
					Source:    rel,
					Content:   string(snapshotContent),
					TokenCost: tokenEstimate(len(snapshotContent)),
					Relevance: 1.0,
				}}, units...)
			}
		}
	}
	// Append manifest units (not repetitive target reads) if not already included.
	if len(units) == 0 || (len(snapshotContent) == 0) {
		// Fallback to original helper for non-target cases
		units = candidateUnits(root, prompt) //nolint:contextcheck // target resolver API predates context propagation
	} else {
		// Ensure manifests are included
		for _, name := range manifestNames {
			manifestPath := filepath.Join(root, name)
			// Skip if already present
			already := false
			for _, u := range units {
				if u.Source == name {
					already = true
					break
				}
			}
			if already {
				continue
			}
			if data, err := os.ReadFile(manifestPath); err == nil {
				units = append(units, runtimectx.ContextUnit{
					ID:        "manifest-" + name,
					Kind:      runtimectx.KindManifest,
					Source:    name,
					Content:   string(data),
					TokenCost: tokenEstimate(len(data)),
					Relevance: 0.8,
				})
			}
		}
	}

	req := preflight.PreflightRequest{
		RawInput:       prompt,
		WorkDir:        root,
		TokenBudget:    budget,
		CandidateUnits: units,
	}
	return s.Orchestrator.RunCycle(ctx, req, s.Provider, s.Bridge, orchestrator.OrchestratorConfig{
		TokenBudget:    budget,
		ViewportConfig: s.Viewport,
	})
}

// extractTargetFromPrompt extracts a file path from a free-form prompt that may
// contain "@index.html" or "index.html" mentions. It returns the first file-like
// token that resolves to an existing file, or the raw @-mentioned token, or "".
func extractTargetFromPrompt(root, prompt string) string {
	// Prefer @-mentioned file.
	for _, tok := range strings.Fields(prompt) {
		if strings.HasPrefix(tok, "@") {
			clean := strings.Trim(tok, "\"'`.,;:!?()[]{}")
			clean = strings.TrimPrefix(clean, "@")
			if clean != "" {
				return clean
			}
		}
	}
	// Fallback: any token that looks like a file with extension and exists.
	for _, tok := range strings.Fields(prompt) {
		clean := strings.Trim(tok, "\"'`.,;:!?()[]{}")
		if strings.Contains(clean, ".") && !strings.Contains(clean, "://") {
			// Check if file exists in workspace.
			if _, err := os.Stat(filepath.Join(root, clean)); err == nil {
				return clean
			}
			// Also check basename match.
			if filepath.Ext(clean) != "" {
				return clean
			}
		}
	}
	return ""
}

// candidateUnits scans root for a minimal set of context units: the resolved
// target file state (when it exists) and any dependency manifests.
// It is kept for backward compatibility; new Run path uses snapshot-based units.
func candidateUnits(root, prompt string) []runtimectx.ContextUnit {
	units := make([]runtimectx.ContextUnit, 0, 5)
	// Use extracted target if prompt is a sentence.
	effectivePrompt := prompt
	if extracted := extractTargetFromPrompt(root, prompt); extracted != "" {
		effectivePrompt = extracted
	}
	if ref, err := target.NewTargetResolver().Resolve(root, effectivePrompt); err == nil && ref.Exists { //nolint:contextcheck // target resolver API predates context propagation
		if data, err := os.ReadFile(filepath.Join(root, ref.Canonical)); err == nil {
			units = append(units, runtimectx.ContextUnit{
				ID:        "target",
				Kind:      runtimectx.KindTargetState,
				Source:    ref.Canonical,
				Content:   string(data),
				TokenCost: tokenEstimate(len(data)),
				Relevance: 1.0,
			})
		}
	}
	for _, name := range manifestNames {
		if data, err := os.ReadFile(filepath.Join(root, name)); err == nil {
			units = append(units, runtimectx.ContextUnit{
				ID:        "manifest-" + name,
				Kind:      runtimectx.KindManifest,
				Source:    name,
				Content:   string(data),
				TokenCost: tokenEstimate(len(data)),
				Relevance: 0.8,
			})
		}
	}
	return units
}

// tokenEstimate approximates the token count of a byte payload (~4 bytes per
// token) so context units fit under the token budget.
func tokenEstimate(bytes int) int {
	if bytes <= 0 {
		return 0
	}
	if tokens := bytes / 4; tokens > 0 {
		return tokens
	}
	return 1
}

// ActionLabel returns a stable lowercase label for an approval action.
func ActionLabel(a authorization.ApprovalAction) string {
	switch a {
	case authorization.ActionExecute:
		return "execute"
	case authorization.ActionInspect:
		return "inspect"
	case authorization.ActionCancel:
		return "cancel"
	default:
		return "none"
	}
}
