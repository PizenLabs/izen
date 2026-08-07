// Package app wires the Izen V3 Agent Runtime Engine into a single callable
// pipeline. It is the integration seam the CLI (cmd/izen) uses to route every
// prompt strictly through:
//
//	Capability Registry → Extractor Pipeline → Artifact IR
//	→ Planner → ExecutionGraph → Kernel Engine
//
// Side-effects are dispatched exclusively through the pkg/event bus so a TUI
// (or the CLI's own terminal observer) can render real-time status updates.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/PizenLabs/izen/pkg/capability"
	"github.com/PizenLabs/izen/pkg/event"
	"github.com/PizenLabs/izen/pkg/extractor"
	"github.com/PizenLabs/izen/pkg/ir"
	"github.com/PizenLabs/izen/pkg/planner"
)

// Errors returned by the Pipeline.
var (
	// ErrNilPipeline is returned when Run is invoked on a nil receiver.
	ErrNilPipeline = errors.New("app: nil pipeline")
	// ErrNoGenerator is returned when Run is invoked without a Generator.
	ErrNoGenerator = errors.New("app: no generator configured")
	// ErrEmptyIntent is returned when Run is handed an empty intent.
	ErrEmptyIntent = errors.New("app: empty intent")
	// ErrEmptyGeneration is returned when the generator produced no output.
	ErrEmptyGeneration = errors.New("app: generator returned an empty response")
)

// Generator performs a single LLM generation pass with an explicit system
// prompt and a user prompt. The CLI adapts the configured ai.Provider to this
// contract so the pipeline stays free of any provider dependency.
type Generator interface {
	Complete(ctx context.Context, system, prompt string, maxTokens int) (string, error)
}

// Clarifier resolves the ClarificationQuestions the pipeline raises for an
// ambiguous intent. The TUI host subscribes to the TypeClarificationRequired
// event to learn which questions are pending, then feeds the user's selections
// into the response channel the pipeline is blocking on.
//
// Implementations MUST send exactly one ir.ClarificationResponse on resp and
// return, or return an error. When no Clarifier is configured the pipeline
// auto-resolves the default options so a headless run never hangs.
type Clarifier interface {
	// Clarify receives the questions to ask and the channel the pipeline
	// blocks on. resp is buffered with capacity one: a synchronous send that
	// does not block the caller is guaranteed.
	Clarify(ctx context.Context, questions []ir.ClarificationQuestion, resp chan<- ir.ClarificationResponse) error
}

// ClarifierFunc adapts a function value to the Clarifier interface.
type ClarifierFunc func(ctx context.Context, questions []ir.ClarificationQuestion, resp chan<- ir.ClarificationResponse) error

// Clarify implements Clarifier.
func (f ClarifierFunc) Clarify(ctx context.Context, questions []ir.ClarificationQuestion, resp chan<- ir.ClarificationResponse) error {
	return f(ctx, questions, resp)
}

// Request is a single pipeline invocation.
type Request struct {
	// Intent is the raw user prompt to route through the pipeline.
	Intent string
	// Targets are optional workspace files the intent references.
	Targets []string
	// IntentIR is the compiled intent (optional). When it reports
	// DecisionAmbiguity, Run pauses at the clarification gate: it publishes a
	// TypeClarificationRequired event and blocks on a response channel until
	// the configured Clarifier resolves every question, then folds the
	// answers back onto the intent and continues.
	IntentIR *ir.IntentIR
}

// Result is the full audit trail of one pipeline run.
type Result struct {
	// Intent is the original user prompt.
	Intent string
	// Capabilities are the capability instances resolved for the intent.
	Capabilities []capability.Capability
	// SystemPrompt is the capability-constrained system prompt used for the
	// final accepted generation.
	SystemPrompt string
	// RawOutput is the raw LLM output of the final accepted generation.
	RawOutput string
	// Artifacts are the extracted, validated ir.Artifact values.
	Artifacts []ir.Artifact
	// Validations holds the per-artifact capability validation outcomes.
	Validations []ArtifactValidation
	// Plan is the planner result whose graph was executed.
	Plan *planner.PlanResult
	// IntentIR is the compiled intent processed by the run (nil when the
	// request carried none). After an ambiguity is resolved it carries the
	// user's selections and the reconciled PreserveWorkspace flag.
	IntentIR *ir.IntentIR
	// Mode records which planning strategy ran.
	Mode Mode
	// Answer carries the direct text answer for conversational intents.
	Answer string
	// ExtractionAttempts counts LLM generations performed before acceptance.
	ExtractionAttempts int
	// RepairRounds counts validation-gate repair rounds performed.
	RepairRounds int
	// Events is a best-effort snapshot of events observed on the bus during
	// the run. Live observers receive the same events in real time.
	Events []event.Event
}

// ArtifactValidation is the outcome of validating one artifact against the
// resolved capability set.
type ArtifactValidation struct {
	// Artifact is the validated artifact.
	Artifact ir.Artifact
	// Checks holds one ValidationResult per resolved capability.
	Checks []capability.ValidationResult
	// Passed reports whether every capability accepted the artifact.
	Passed bool
	// Reasons aggregates every rejection reason across failing checks.
	Reasons []string
}

// Pipeline routes a user intent through the full V3 flow. Construct with
// NewPipeline; generators are mandatory and defaults are registered for
// capabilities, extractors and the event bus.
type Pipeline struct {
	registry    *capability.Registry
	extractors  []extractor.Extractor
	generator   Generator
	bus         *event.MemoryEventBus
	clarifier   Clarifier
	root        string
	mode        Mode
	detect      Detector
	verify      func(root string) string
	modelTier   string
	maxAttempts int
	maxRepairs  int

	eventsMu sync.Mutex
	events   []event.Event
}

// NewPipeline constructs a pipeline with the default wiring: all default
// semantic capabilities registered, MarkdownFence + JSON extractors, an
// in-memory event bus, and auto greenfield/brownfield planning. Options
// override individual pieces.
func NewPipeline(opts ...Option) (*Pipeline, error) {
	p := &Pipeline{
		registry:    capability.NewRegistry(),
		extractors:  []extractor.Extractor{extractor.NewMarkdownFenceExtractor(), extractor.NewJSONExtractor()},
		bus:         event.NewMemoryEventBus(event.DefaultBufferSize),
		root:        ".",
		mode:        ModeAuto,
		detect:      defaultDetector,
		modelTier:   "full",
		maxAttempts: 3,
		maxRepairs:  2,
	}
	for _, opt := range opts {
		opt(p)
	}
	if p.registry == nil {
		return nil, errors.New("app: nil capability registry")
	}
	if err := capability.RegisterDefaults(p.registry); err != nil {
		return nil, fmt.Errorf("app: register default capabilities: %w", err)
	}
	p.bus.Subscribe(nil, func(e event.Event) { p.recordEvent(e) })
	return p, nil
}

// Bus returns the shared event bus the pipeline and the kernel publish on.
// Callers attach their own observers (e.g. a TUI or a terminal status
// renderer) before invoking Run.
func (p *Pipeline) Bus() *event.MemoryEventBus { return p.bus }

// Run routes req through the V3 pipeline and returns the audit trail.
func (p *Pipeline) Run(ctx context.Context, req Request) (*Result, error) {
	if p == nil {
		return nil, ErrNilPipeline
	}
	if p.generator == nil {
		return nil, ErrNoGenerator
	}
	if strings.TrimSpace(req.Intent) == "" {
		return nil, ErrEmptyIntent
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	res := &Result{Intent: req.Intent}
	if req.IntentIR != nil {
		intentCopy := *req.IntentIR
		intentCopy.Entities = copyMap(req.IntentIR.Entities)
		intentCopy.Technologies = append([]string(nil), req.IntentIR.Technologies...)
		res.IntentIR = &intentCopy
	}

	// Clarification gate: an ambiguous intent pauses the pipeline before any
	// planning or generation. The TypeClarificationRequired event notifies UI
	// hosts which questions are pending; the pipeline then blocks on a
	// response channel until the Clarifier feeds the user's selections back.
	if req.IntentIR != nil && req.IntentIR.DecisionAmbiguity {
		if err := p.clarifyGate(ctx, res.IntentIR); err != nil {
			return nil, err
		}
		// The reconciled intent drives the planning strategy: a "replace the
		// workspace" answer forces a greenfield one-shot write.
		req.IntentIR = res.IntentIR
	}

	// Conversational intents short-circuit to a single direct chat pass.
	if IsConversationalIntent(req.Intent) {
		p.emitStage(ctx, "chat")
		out, err := p.generate(ctx, chatSystemPrompt, req.Intent)
		if err != nil {
			return nil, fmt.Errorf("app: chat generation: %w", err)
		}
		res.Answer = strings.TrimSpace(out)
		p.settleEvents()
		res.Events = p.snapshotEvents()
		return res, nil
	}

	// Stage 1 — Policy & capability resolution.
	caps, err := ResolveCapabilitiesForIntent(p.registry, req.Intent)
	if err != nil {
		return nil, fmt.Errorf("app: resolve capabilities: %w", err)
	}
	res.Capabilities = caps
	res.SystemPrompt = p.buildSystemPrompt(caps, req.Targets)
	p.emitStage(ctx, "capability.resolve")

	// Stages 2-5 — generate, extract, evaluate evidence, validate. A
	// rejected extraction or a failed validation gate re-prompts with the
	// failure payload; nothing is written to disk before every artifact
	// passes.
	attempts := 0
	rejections := 0
	repairs := 0
	var raw string
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		attempts++

		raw, err = p.generate(ctx, res.SystemPrompt, p.userPrompt(req))
		if err != nil {
			return nil, fmt.Errorf("app: generation: %w", err)
		}
		p.emitStage(ctx, "extraction")

		extraction := p.extract(ctx, raw)
		if extraction.Evaluate() != extractor.DecisionAccept {
			rejections++
			if rejections >= p.maxAttempts {
				return nil, &ExtractionError{Attempts: attempts, Evidence: extraction.EvidenceSet(), Raw: raw}
			}
			res.SystemPrompt = appendExtractionFailure(res.SystemPrompt, raw, extraction)
			continue
		}

		artifacts := extraction.Artifacts
		res.Artifacts = artifacts
		validations := p.validate(ctx, caps, artifacts)
		res.Validations = validations
		if !allValidationsPass(validations) {
			if repairs >= p.maxRepairs {
				return nil, &ValidationError{Repairs: repairs, Validations: validations}
			}
			repairs++
			res.SystemPrompt = appendValidationRejection(res.SystemPrompt, validations)
			p.emitStage(ctx, "validation.repair")
			continue
		}

		res.RawOutput = raw
		res.ExtractionAttempts = attempts
		res.RepairRounds = repairs
		break
	}

	// Stage 6 — Planning & strategy selection.
	p.emitStage(ctx, "plan")
	planResult, planMode, bfPlanner, err := p.plan(ctx, req.Intent, res.Artifacts, req.IntentIR)
	if err != nil {
		return nil, fmt.Errorf("app: plan: %w", err)
	}
	res.Plan = planResult
	res.Mode = planMode

	// Stage 7 — Kernel engine execution.
	p.emitStage(ctx, "execute")
	if err := p.execute(ctx, planResult, planMode, bfPlanner); err != nil {
		return nil, fmt.Errorf("app: execute: %w", err)
	}

	p.settleEvents()
	res.Events = p.snapshotEvents()
	return res, nil
}

// generate performs one LLM generation pass, refusing empty output.
func (p *Pipeline) generate(ctx context.Context, system, prompt string) (string, error) {
	p.emitStage(ctx, "generate")
	out, err := p.generator.Complete(ctx, system, prompt, 0)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return "", ErrEmptyGeneration
	}
	return out, nil
}

// clarifyGate pauses the pipeline on an ambiguous intent. It publishes the
// pending ClarificationQuestions as a TypeClarificationRequired event, then
// blocks on a response channel until the configured Clarifier resolves them.
// Without a Clarifier the default options are auto-selected so headless runs
// never hang. The reconciled intent has DecisionAmbiguity cleared and
// PreserveWorkspace folded in from the selected branch.
func (p *Pipeline) clarifyGate(ctx context.Context, intent *ir.IntentIR) error {
	if intent == nil || !intent.DecisionAmbiguity {
		return nil
	}
	if len(intent.ClarificationQuestions) == 0 {
		intent.DecisionAmbiguity = false
		return nil
	}
	questions := intent.ClarificationQuestions
	p.bus.Publish(event.NewEvent(event.TypeClarificationRequired, "pipeline", questions))

	responseChan := make(chan ir.ClarificationResponse, 1)
	go p.dispatchClarification(ctx, questions, responseChan)

	select {
	case resp := <-responseChan:
		applyClarification(intent, resp)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// dispatchClarification routes the pending questions to the configured
// Clarifier (or the default auto-resolution) and delivers the response to the
// channel the pipeline is blocking on. A failing Clarifier degrades to the
// default answers and surfaces the error on the bus so the run can continue.
func (p *Pipeline) dispatchClarification(ctx context.Context, questions []ir.ClarificationQuestion, resp chan<- ir.ClarificationResponse) {
	if p.clarifier == nil {
		resp <- ir.ClarificationResponse{Answers: ir.DefaultAnswers(questions)}
		return
	}
	if err := p.clarifier.Clarify(ctx, questions, resp); err != nil {
		if ctx.Err() != nil {
			return
		}
		p.bus.Publish(event.NewEvent(event.TypeTaskFailed, "pipeline", err))
		resp <- ir.ClarificationResponse{Answers: ir.DefaultAnswers(questions)}
	}
}

// applyClarification folds the user's answers back onto the intent: every
// question records its SelectedOption/CustomAnswer, DecisionAmbiguity is
// cleared, and PreserveWorkspace is reconciled from the semantic option IDs.
func applyClarification(intent *ir.IntentIR, resp ir.ClarificationResponse) {
	if intent == nil {
		return
	}
	intent.ClarificationQuestions = ir.ApplyAnswers(intent.ClarificationQuestions, resp.Answers)
	intent.DecisionAmbiguity = false
	for _, a := range resp.Answers {
		switch a.OptionID {
		case ir.OptionReplaceWorkspace:
			intent.PreserveWorkspace = false
		case ir.OptionBuildAlongside, ir.OptionMergeSelective, ir.OptionTypeYourOwn:
			intent.PreserveWorkspace = true
		}
	}
}

// copyMap returns a defensive copy of src.
func copyMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return make(map[string]string)
	}
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// extract runs the configured extractors over raw and returns the first
// accepted ExtractionResult. When none accepts, the first result is returned
// so its evidence set can drive the retry.
func (p *Pipeline) extract(ctx context.Context, raw string) extractor.ExtractionResult {
	var best extractor.ExtractionResult
	first := true
	for _, x := range p.extractors {
		res := x.Extract(ctx, raw)
		if first {
			best = res
			first = false
		}
		if res.Evaluate() == extractor.DecisionAccept {
			return res
		}
	}
	return best
}

// validate runs every resolved capability against every artifact's content.
func (p *Pipeline) validate(ctx context.Context, caps []capability.Capability, artifacts []ir.Artifact) []ArtifactValidation {
	out := make([]ArtifactValidation, 0, len(artifacts))
	for _, a := range artifacts {
		v := ArtifactValidation{Artifact: a, Passed: true}
		for _, c := range caps {
			res := c.Validate(ctx, a.Content)
			v.Checks = append(v.Checks, res)
			if res.Failed() {
				v.Passed = false
				for _, reason := range res.Reasons {
					v.Reasons = append(v.Reasons, fmt.Sprintf("%s: %s", c.ID(), reason))
				}
			}
		}
		out = append(out, v)
	}
	return out
}

// buildSystemPrompt assembles the capability-constrained system prompt from
// the resolved capability set.
func (p *Pipeline) buildSystemPrompt(caps []capability.Capability, targets []string) string {
	var b strings.Builder
	b.WriteString("You are Izen, the human-centered coding engine. Route the user's intent into a coherent set of workspace files that Izen writes for you.\n")
	if len(targets) > 0 {
		b.WriteString("\nWorkspace target files already referenced:\n")
		for _, t := range targets {
			b.WriteString("  - " + t + "\n")
		}
	}
	b.WriteString("\nACTIVE CAPABILITIES (enforce every constraint, never fall back to a generic template):\n")
	for _, c := range caps {
		b.WriteString(c.PromptRepresentation(p.modelTier) + "\n")
	}
	b.WriteString("\nOUTPUT FORMAT\n")
	b.WriteString("Emit exactly one fenced block per file. The fence header MUST name the language and the workspace-relative target path:\n")
	b.WriteString("```html:index.html\n<!DOCTYPE html>\n...\n```\n")
	b.WriteString("A fence header is always \"```lang:path\" where path is the relative file location (e.g. html:index.html, go:main.go, css:styles/main.css). Plain code fences without a :path header, or prose, will be rejected.\n")
	b.WriteString("You may add brief narration before the blocks. Never write to disk yourself.\n")
	return b.String()
}

// userPrompt folds targets into the user-facing prompt.
func (p *Pipeline) userPrompt(req Request) string {
	if len(req.Targets) == 0 {
		return req.Intent
	}
	return req.Intent + "\n\nWorkspace context files: " + strings.Join(req.Targets, ", ")
}

// emitStage publishes a pipeline stage checkpoint on the shared bus.
func (p *Pipeline) emitStage(ctx context.Context, stage string) {
	if err := ctx.Err(); err != nil {
		return
	}
	p.bus.Publish(event.NewEvent(event.TypeStateCheckpt, "pipeline", StageEvent{Stage: stage}))
}

// recordEvent appends an observed event to the best-effort audit log.
func (p *Pipeline) recordEvent(e event.Event) {
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	p.events = append(p.events, e)
}

// eventCount returns the number of events observed so far.
func (p *Pipeline) eventCount() int {
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	return len(p.events)
}

// settleEvents briefly waits for the bus's asynchronous dispatch goroutines
// to drain into the recorder so a post-run snapshot is effectively complete.
func (p *Pipeline) settleEvents() {
	deadline := time.Now().Add(25 * time.Millisecond)
	last := -1
	for time.Now().Before(deadline) {
		n := p.eventCount()
		if n == last {
			return
		}
		last = n
		time.Sleep(2 * time.Millisecond)
	}
}

// snapshotEvents returns a defensive copy of the observed events.
func (p *Pipeline) snapshotEvents() []event.Event {
	p.eventsMu.Lock()
	defer p.eventsMu.Unlock()
	return append([]event.Event(nil), p.events...)
}

// StageEvent is the payload of a pipeline stage checkpoint.
type StageEvent struct {
	// Stage names the pipeline stage that began.
	Stage string
}

// ExtractionError reports that the extractor pipeline never accepted the raw
// LLM output within the attempt budget.
type ExtractionError struct {
	// Attempts is the number of generations performed.
	Attempts int
	// Evidence is the evidence set of the final rejected extraction.
	Evidence []extractor.EvidenceFlag
	// Raw is the final rejected raw output.
	Raw string
}

// Error implements error.
func (e *ExtractionError) Error() string {
	return fmt.Sprintf("app: extraction rejected after %d attempt(s); evidence: %v", e.Attempts, e.Evidence)
}

// ValidationError reports that artifacts failed the capability validation
// gate after every repair round.
type ValidationError struct {
	// Repairs is the number of repair rounds already performed.
	Repairs int
	// Validations holds the failing per-artifact outcomes.
	Validations []ArtifactValidation
}

// Error implements error.
func (e *ValidationError) Error() string {
	paths := make([]string, 0, len(e.Validations))
	for _, v := range e.Validations {
		if !v.Passed {
			paths = append(paths, v.Artifact.Path)
		}
	}
	return fmt.Sprintf("app: artifacts failed capability validation after %d repair round(s): %s",
		e.Repairs, strings.Join(paths, ", "))
}

// ResolveCapabilitiesForIntent maps a user intent to the active capability id
// set, preserving request order, and resolves them through the registry. The
// generic catch-all is only used when no specific capability matches.
func ResolveCapabilitiesForIntent(reg *capability.Registry, intent string) ([]capability.Capability, error) {
	if reg == nil {
		return nil, errors.New("app: nil capability registry")
	}
	lower := strings.ToLower(intent)
	ids := make([]capability.CapabilityID, 0, 4)
	add := func(id capability.CapabilityID) {
		for _, existing := range ids {
			if existing == id {
				return
			}
		}
		ids = append(ids, id)
	}
	switch {
	case strings.Contains(lower, "portfolio"):
		add(capability.CapPortfolioWebsite)
		add(capability.CapSemanticHTML)
	case strings.Contains(lower, "typescript"), strings.Contains(lower, "react"), strings.Contains(lower, "tsx"), strings.Contains(lower, "frontend"):
		add(capability.CapTypeScript)
	case strings.Contains(lower, "html"), strings.Contains(lower, "landing"), strings.Contains(lower, "website"), strings.Contains(lower, "web page"), strings.Contains(lower, " web "):
		add(capability.CapSemanticHTML)
	case strings.HasPrefix(lower, "go "), strings.Contains(lower, "go "), strings.Contains(lower, "golang"), strings.Contains(lower, "backend"), strings.Contains(lower, " api"):
		add(capability.CapGoBackend)
	}
	if len(ids) == 0 {
		add(capability.CapGenericCode)
	}
	return reg.Resolve(ids...)
}

// IsConversationalIntent reports whether intent is a conversational prompt
// (greeting, small talk, identity/memory question, or a read-only explain)
// that runs as a direct chat pass instead of the code-generation pipeline.
func IsConversationalIntent(intent string) bool {
	lower := strings.ToLower(strings.TrimSpace(intent))
	if len(lower) < 3 {
		return true
	}
	switch lower {
	case "hi", "hello", "hey", "yo", "sup", "hiya", "howdy":
		return true
	}
	for _, g := range []string{"hi ", "hello ", "hey ", "how are you", "what's up", "good morning", "good evening", "yo "} {
		if strings.HasPrefix(lower, g) && len(lower) <= len(g)+8 {
			return true
		}
	}
	for _, p := range []string{
		"who are you", "what are you", "what can you do", "do you remember",
		"thank you", "thanks", "explain ", "explain:", "describe ", "what is ", "what does ", "what's ",
	} {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

const chatSystemPrompt = "You are Izen, the human-centered coding engine. " +
	"Answer the user's question directly and conversationally."

// appendExtractionFailure re-prompts the model with the evidence payload of a
// rejected extraction.
func appendExtractionFailure(system, raw string, res extractor.ExtractionResult) string {
	var b strings.Builder
	b.WriteString("\n\n### EXTRACTION REJECTED — RETRY REQUIRED\n")
	b.WriteString("Your previous response produced no valid artifact blocks, so nothing was written.\n")
	b.WriteString("Evidence observed: ")
	writeFlags(&b, res.EvidenceSet())
	if missing := missingEvidence(res); len(missing) > 0 {
		b.WriteString("\nMissing evidence: ")
		writeFlags(&b, missing)
	}
	b.WriteString("\nYour previous output:\n```\n")
	b.WriteString(truncate(raw, 2000))
	b.WriteString("\n```\n")
	b.WriteString("Respond AGAIN with every file as a fenced block. Each fence header MUST be exactly \"```lang:path\" naming the language and the relative target path, e.g. \"```html:index.html\" or \"```go:main.go\":\n")
	b.WriteString("```lang:path\n<file content>\n```\n")
	return system + b.String()
}

// appendValidationRejection re-prompts the model with the reasons the
// validation gate rejected the generated artifacts.
func appendValidationRejection(system string, vals []ArtifactValidation) string {
	var b strings.Builder
	b.WriteString("\n\n### VALIDATION REJECTED — REPAIR REQUIRED\n")
	b.WriteString("One or more generated files failed capability validation and were NOT written. Fix the files and respond again.\n")
	for _, v := range vals {
		if v.Passed {
			continue
		}
		b.WriteString("- " + v.Artifact.Path + ": " + strings.Join(v.Reasons, "; ") + "\n")
	}
	b.WriteString("Re-emit every file as a fenced block, matching the capability constraints exactly. Never fall back to a generic template.\n")
	return system + b.String()
}

// missingEvidence returns the structural evidence flags absent from res.
func missingEvidence(res extractor.ExtractionResult) []extractor.EvidenceFlag {
	missing := make([]extractor.EvidenceFlag, 0, 4)
	for _, f := range []extractor.EvidenceFlag{
		extractor.EvValidFenceHeader, extractor.EvPathInHeader, extractor.EvFenceClosed, extractor.EvValidUTF8,
	} {
		if !res.HasEvidence(f) {
			missing = append(missing, f)
		}
	}
	return missing
}

// writeFlags renders an evidence flag list onto b.
func writeFlags(b *strings.Builder, flags []extractor.EvidenceFlag) {
	for i, f := range flags {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(f.String())
	}
}

// truncate caps s to max runes, preserving a trailing marker.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "..."
}

// allValidationsPass reports whether every artifact passed the gate.
func allValidationsPass(vals []ArtifactValidation) bool {
	for _, v := range vals {
		if !v.Passed {
			return false
		}
	}
	return true
}

// ensureParentDirs creates the parent directories of every file artifact
// path under root. Paths that escape the workspace root are rejected before
// any directory is created. It also ensures the root itself exists.
func ensureParentDirs(root string, artifacts []ir.Artifact) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(absRoot, 0o755); err != nil {
		return err
	}
	for _, a := range artifacts {
		if a.Kind != ir.ArtifactFile || a.Path == "" {
			continue
		}
		clean := filepath.Clean(a.Path)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("app: artifact path %q escapes workspace root %q", a.Path, root)
		}
		dir := filepath.Dir(filepath.Join(absRoot, clean))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("app: create parent directory for %q: %w", a.Path, err)
		}
	}
	return nil
}
