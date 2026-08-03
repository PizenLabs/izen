package plan

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/PizenLabs/izen/internal/ai"
	"github.com/PizenLabs/izen/internal/core/stream"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/prompt"
	"github.com/PizenLabs/izen/internal/retrieval"
	wscap "github.com/PizenLabs/izen/internal/workspace/capability"
	wssnapshot "github.com/PizenLabs/izen/internal/workspace/snapshot"
	"github.com/PizenLabs/izen/pkg/grounding"
	"github.com/PizenLabs/izen/pkg/recon"
)

// ProviderFunc defines a structured function signature matching the ai.Request format.
type ProviderFunc func(ctx context.Context, req ai.Request) (*ai.Response, error)

// StreamProviderFunc matches the ai.Provider.ExecuteStream signature. When
// wired, the plan engine performs its LLM synthesis through a streaming
// connection so the accumulated buffer survives finish_reason: "length"
// truncation instead of being discarded by a non-streaming round-trip.
type StreamProviderFunc func(ctx context.Context, req ai.Request) (io.ReadCloser, error)

// Engine is the core interface for the plan module, coordinating between data store,
// parser, and AI provider to process plans.
type Engine struct {
	store        *PlanStore
	parser       func(string) []Task
	provider     ProviderFunc
	streamProv   StreamProviderFunc
	UserName     string   // collaborating engineer identity, injected into system prompts
	rootPath     string   // workspace root for file discovery
	AllowedFiles []string // grounded file tree for scope guard validation
	vanillaWeb   bool     // when true, skip Go-specific fast-track paths

	// snapCache and capReg are injected at bootstrap for archetype-aware
	// diagnostic gating. They are optional; nil values are safe.
	snapCache *wssnapshot.SnapshotCache
	capReg    *wscap.ArchetypeCapabilityRegistry

	// bus is the event bus this engine publishes domain events to. Engines are
	// headless: they publish and never touch the UI directly. Optional; nil
	// disables event emission.
	bus *events.Bus

	// usageMu guards lastInput/lastOutput, the provider-reported token usage of
	// the most recent LLM synthesis. The UI reads it via LastUsage() to commit
	// token metrics to the status.Tracker even when the response was truncated
	// (finish_reason: "length").
	usageMu    sync.RWMutex
	lastInput  int
	lastOutput int
}

// NewEngine creates a new Engine instance with the provided components.
// Default parser is ParseJSONPlan — falls back to ParseMarkdownToTasks for legacy plans.
func NewEngine(store *PlanStore) *Engine {
	return &Engine{
		store:    store,
		parser:   parsePlanContent,
		provider: nil,
	}
}

// SetUserName sets the engineer identity for system prompt injection.
func (e *Engine) SetUserName(name string) { e.UserName = name }

// SetRootPath sets the workspace root for file discovery.
func (e *Engine) SetRootPath(rootPath string) { e.rootPath = rootPath }

// SetAllowedFiles sets the grounded file tree for scope guard validation.
func (e *Engine) SetAllowedFiles(files []string) { e.AllowedFiles = files }

// WithSnapshotCache injects a workspace snapshot cache for archetype-aware
// diagnostic gating. May be nil.
func (e *Engine) WithSnapshotCache(sc *wssnapshot.SnapshotCache) *Engine {
	e.snapCache = sc
	return e
}

// WithCapabilityRegistry injects an archetype capability registry for
// archetype-aware diagnostic gating. May be nil.
func (e *Engine) WithCapabilityRegistry(cr *wscap.ArchetypeCapabilityRegistry) *Engine {
	e.capReg = cr
	return e
}

// WithEventBus injects the event bus this engine publishes domain events to.
// The engine stays headless: it never mutates UI state or writes to the
// terminal directly — consumers subscribe to the bus as projections. May be
// nil to disable emission.
func (e *Engine) WithEventBus(bus *events.Bus) *Engine {
	e.bus = bus
	return e
}

// emit publishes a domain event. It is a strict no-op when no bus is wired,
// so engines keep working unchanged in headless/CLI contexts.
func (e *Engine) emit(ev events.DomainEvent) {
	if e != nil && e.bus != nil {
		e.bus.Publish(ev)
	}
}

// DiscoverAllowedFiles runs pkg/recon and pkg/grounding to discover the
// workspace file tree. Returns the allowed file list or an error.
// If AllowedFiles is already set, returns them immediately.
func (e *Engine) DiscoverAllowedFiles() ([]string, error) {
	if len(e.AllowedFiles) > 0 {
		return e.AllowedFiles, nil
	}
	if e.rootPath == "" {
		return nil, fmt.Errorf("plan engine: rootPath not set — call SetRootPath first")
	}
	archetype, err := recon.DetectArchetype(e.rootPath)
	if err != nil {
		return nil, fmt.Errorf("plan engine: recon failed: %w", err)
	}
	intent := &grounding.CanonicalIntent{
		RawPrompt:    "workspace discovery",
		CleanIntent:  "workspace discovery",
		TargetScopes: nil,
		Confidence:   1.0,
	}
	gc, err := grounding.SliceContext(archetype, intent, e.rootPath)
	if err != nil {
		return nil, fmt.Errorf("plan engine: grounding failed: %w", err)
	}
	e.AllowedFiles = gc.AllowedFileTree
	return e.AllowedFiles, nil
}

// GroundedConstraint returns the ALLOWED_FILE_TREE constraint block for prompt
// injection, or empty string if no allowed files are set.
func (e *Engine) GroundedConstraint() string {
	if len(e.AllowedFiles) == 0 {
		return ""
	}
	return prompt.GroundedConstraint("", e.AllowedFiles)
}

// parsePlanContent enforces strict JSON schema with recovery.
// Phase 3: If JSON parsing fails, it attempts auto-repair via autoCloseJSON
// and retries before giving up. Markdown-only output is rejected.
func parsePlanContent(content string) []Task {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}

	result := ParseJSONPlan(content)
	if result.Valid {
		if err := ValidateAllTasks(result.Tasks); err != nil {
			return nil
		}
		return result.Tasks
	}

	// Phase 3: Attempt auto-repair of truncated JSON before giving up.
	repaired := autoCloseJSON(content)
	if repaired != content {
		result = ParseJSONPlan(repaired)
		if result.Valid {
			if err := ValidateAllTasks(result.Tasks); err != nil {
				return nil
			}
			return result.Tasks
		}
	}

	return nil
}

// SetProvider configures the AI provider for this engine using the structured signature.
func (e *Engine) SetProvider(provider ProviderFunc) {
	if e != nil {
		e.provider = provider
	}
}

// SetStreamProvider configures the streaming AI provider for this engine. When
// wired, LLM synthesis runs over ExecuteStream so the accumulated text buffer
// is retained even when the provider truncates the response (finish_reason:
// "length"). Optional; when nil the engine falls back to the non-streaming
// SetProvider path.
func (e *Engine) SetStreamProvider(sp StreamProviderFunc) {
	if e != nil {
		e.streamProv = sp
	}
}

// LastUsage returns the provider-reported token usage (input, output) of the
// most recent LLM synthesis, committed even when the response was truncated by
// the completion ceiling. The UI reads it after ProcessFromLedger returns to
// update the session counters and the status.Tracker.
func (e *Engine) LastUsage() (input, output int) {
	if e == nil {
		return 0, 0
	}
	e.usageMu.RLock()
	defer e.usageMu.RUnlock()
	return e.lastInput, e.lastOutput
}

// recordUsage commits provider-reported token usage. It is called on every
// synthesis attempt, truncated or not, so the token metrics are never lost to
// a finish_reason: "length" terminal event.
func (e *Engine) recordUsage(input, output int) {
	if e == nil {
		return
	}
	e.usageMu.Lock()
	e.lastInput = input
	e.lastOutput = output
	e.usageMu.Unlock()
}

// usageReader is implemented by stream results that report provider usage.
type usageReader interface {
	Usage() (input, output int)
}

// complete performs a single LLM synthesis call. When a streaming provider is
// wired it runs over ExecuteStream and accumulates the buffer rune-safe,
// stripping reasoning sentinels; a response that ends with finish_reason
// "length" (or any non-stop truncation) still yields its accumulated content as
// VALID output — the plan engine can parse whatever checklist/JSON was
// generated instead of failing with "empty response from provider". When the
// stream produced only reasoning/thinking text (content empty), the reasoning
// is used as the payload via the reasoning fallback. The provider-reported
// usage is committed to the engine regardless of the terminal finish_reason.
//
// Reasoning forwarding: when an event bus is wired, every reasoning/thinking
// chunk (reasoning_content deltas routed through the request's ReasoningHandler
// and <thought>/sentinel markers parsed out of the raw stream by
// accumulateStream) is published to the bus as an EventReasoningStream as it
// arrives, so the UI can render live thinking during plan synthesis. Reasoning
// tokens are never dropped even when the request times out or yields empty
// final content — the already-accumulated chunks are published before any
// error/truncation path returns. A terminal IsComplete event closes the block
// so the UI collapses it to a summary line.
func (e *Engine) complete(ctx context.Context, req ai.Request) (*ai.Response, error) {
	if e.streamProv == nil {
		resp, err := e.provider(ctx, req)
		if err != nil {
			return nil, err
		}
		if resp != nil {
			e.recordUsage(resp.TokenInput, resp.TokenOutput)
		}
		return resp, nil
	}

	// Reasoning sink: publishes each reasoning chunk to the event bus exactly
	// as it streams in. reasoningPublished tracks whether any chunk was
	// forwarded so the terminal IsComplete event is only emitted when there is
	// an active thinking block to collapse.
	reasoningPublished := false
	var reasoningSink func(string)
	if e.bus != nil {
		reasoningSink = func(chunk string) {
			if chunk == "" {
				return
			}
			reasoningPublished = true
			e.bus.Publish(events.NewReasoningStream(chunk, false))
		}
		// Providers that route reasoning via the request-level handler
		// (OpenAI/Claude/Gemini/Ollama/Groq/...). Providers that embed
		// sentinel markers in the raw stream (OpenRouter) are captured by the
		// accumulateStream sink through the splitter — never double-forwarded,
		// because those readers do not consult ReasoningHandler.
		req.ReasoningHandler = func(chunk string) error {
			reasoningSink(chunk)
			return nil
		}
	}

	req.Stream = true
	rawStream, err := e.streamProv(ctx, req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rawStream.Close() }()

	content, reasoning, finishReason, input, output := accumulateStream(rawStream, reasoningSink)
	e.recordUsage(input, output)
	_ = finishReason

	if strings.TrimSpace(content) == "" && strings.TrimSpace(reasoning) != "" {
		// Reasoning fallback: the model emitted only thinking content (a
		// Mini/reasoning model with empty message content). Promote the
		// reasoning text to the payload so plan synthesis succeeds instead of
		// failing with "empty response from provider".
		content = reasoning
	}

	// Terminal reasoning event: collapses the live thinking block in the UI.
	// Emitted whenever reasoning was forwarded, even if the request timed out
	// or yielded empty final content — the UI must never be left with an
	// orphaned open thinking box.
	if reasoningPublished {
		e.bus.Publish(events.NewReasoningStream("", true))
	}

	// Truncation-aware response: the accumulated buffer is the canonical
	// content. A zero-length buffer (genuinely no tokens emitted) is left empty
	// so the caller can surface a proper "empty response" diagnostic.
	return &ai.Response{
		Content:     content,
		TokenInput:  input,
		TokenOutput: output,
	}, nil
}

// accumulateStream drains an SSE-backed stream to EOF, keeping EVERY byte that
// arrived. It is truncation-agnostic: a stream that ends with finish_reason
// "length" (provider hit the completion ceiling) has its partial buffer treated
// as valid content rather than discarded. Reasoning sentinels are stripped via
// the Splitter so thinking text never pollutes the parseable JSON; the
// extracted reasoning is returned alongside so a reasoning-only stream can fall
// back to it when the content buffer is empty.
//
// The stream is classified incrementally (not buffered-then-split), so every
// reasoning/thinking chunk is routed to the optional sink funcs as it arrives.
// This lets callers publish reasoning to the event bus continuously — reasoning
// tokens are never dropped even when the request times out or yields empty
// final content, because chunks already read are forwarded before any error
// path returns. Each sink is invoked with verbatim reasoning text; a nil sink
// is ignored.
func accumulateStream(r io.Reader, sinks ...func(string)) (content, reasoning, finishReason string, input, output int) {
	var contentParts, reasoningParts strings.Builder
	runeBuf := stream.NewRuneBuffer()
	splitter := stream.NewSplitter()

	publishReasoning := func(text string) {
		if text == "" {
			return
		}
		for _, s := range sinks {
			if s != nil {
				s(text)
			}
		}
	}

	emitFrame := func(fr stream.Frame) {
		switch fr.Kind {
		case stream.ChunkContent:
			if fr.Text != "" {
				contentParts.WriteString(fr.Text)
			}
		case stream.ChunkReasoning:
			if fr.Text != "" {
				reasoningParts.WriteString(fr.Text)
				publishReasoning(fr.Text)
			}
		}
	}

	flushBuffered := func() {
		if rem := runeBuf.Flush(); rem != "" {
			splitter.Write(rem, emitFrame)
		}
		splitter.Flush(emitFrame)
	}

	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if text := runeBuf.Write(buf[:n]); text != "" {
				splitter.Write(text, emitFrame)
			}
		}
		if err == io.EOF {
			flushBuffered()
			break
		}
		if err != nil {
			// Non-EOF error: keep whatever accumulated so far; the caller
			// decides whether the partial content is usable.
			flushBuffered()
			break
		}
	}
	if up, ok := r.(usageReader); ok {
		input, output = up.Usage()
	}
	if frp, ok := r.(ai.FinishReasonProvider); ok {
		finishReason = frp.FinishReason()
	}
	return contentParts.String(), reasoningParts.String(), finishReason, input, output
}

// ProcessFromLedger generates an execution plan directly from investigation
// ledger data using enforced structured output (JSON mode). Returns parsed
// Task structs, bypassing the conversational text-streaming path entirely.
//
// When fastTrack is true (used for local 7B models on a 0-TODO + compile/dep
// blocker), the heavy JSON-schema instruction and full forensic ledger prompt
// are replaced with a minimal shell-resolution prompt so the model can produce
// its first token within a tight local budget instead of choking on context.
func (e *Engine) ProcessFromLedger(ctx context.Context, ledgerContent string, problem string, modelName string) ([]Task, error) {
	return e.processFromLedger(ctx, ledgerContent, problem, modelName, false)
}

// ProcessFromLedgerFastTrack is the lightweight variant used for local SLMs that
// hit a 0-TODO + dependency/compilation blocker. It skips the JSON-schema system
// prompt and the full forensic ledger prompt in favour of a minimal resolution
// prompt, keeping the prompt tiny enough for a 7B model to answer quickly.
func (e *Engine) ProcessFromLedgerFastTrack(ctx context.Context, promptText string, modelName string) ([]Task, error) {
	return e.processFromLedger(ctx, "", "", modelName, true, promptText)
}

func (e *Engine) processFromLedger(ctx context.Context, ledgerContent string, problem string, modelName string, fastTrack bool, fastPrompt ...string) (tasks []Task, err error) {
	if e == nil || (e.provider == nil && e.streamProv == nil) {
		return nil, fmt.Errorf("plan engine: provider not set")
	}

	// ── HEADLESS EVENT EMISSION ───────────────────────────────
	// The plan engine is headless: every observable outcome is published to
	// the event bus. The deferred block guarantees a terminal event (success
	// or failure) fires for every early-return path below.
	raw := problem
	if raw == "" && len(fastPrompt) > 0 {
		raw = fastPrompt[0]
	}
	e.emit(events.NewCommandReceived(raw, "plan"))
	defer func() {
		if err != nil {
			e.emit(events.NewExecutionFailed(events.FailurePermanent, err, "plan"))
			return
		}
		if len(tasks) > 0 {
			targets := make([]string, 0, len(tasks))
			for _, t := range tasks {
				targets = append(targets, t.Target)
			}
			e.emit(events.NewPlanStaged(len(tasks), targets, "plan"))
			e.emit(events.NewStageCompleted("plan", 0, fmt.Sprintf("staged %d tasks", len(tasks))))
		}
	}()

	// Detect workspace archetype at run start. When VANILLA_WEB, Go-specific
	// fast-track paths (canonical import mismatch, undefined symbol) are
	// skipped and the LLM is instructed to avoid Go toolchain commands.
	if !fastTrack && e.rootPath != "" {
		if ac, err := recon.DetectArchetype(e.rootPath); err == nil && ac != nil {
			e.vanillaWeb = (ac.Type == recon.VANILLA_WEB)
		}
		// Also check the capability registry if available. This extends the
		// vanillaWeb guard to cover non-Go archetypes (e.g., NODE_APP, PYTHON_ENV)
		// that the capability registry identifies as lacking Go tooling.
		if !e.vanillaWeb && e.capReg != nil && e.snapCache != nil {
			//nolint:contextcheck
			if snap, snapErr := e.snapCache.GetSnapshot(e.rootPath); snapErr == nil {
				if !e.capReg.ArchetypeHasGoTools(snap.Archetype) {
					e.vanillaWeb = true
				}
			}
		}
	}

	// ── DIRECT MUTATION FAST-TRACK ──────────────────────────
	// When the prompt is a simple file replacement (refactor LICENSE
	// from MIT to APACHE, change X to Y in @file, etc.), bypass
	// /investigate mode entirely. Do NOT run test suites (go test).
	// Route directly to BUILD / MUTATION pipeline with a
	// deterministic, hardcoded task — zero LLM synthesis needed.
	//
	// This implements the "Direct Mutation Fast-Track Rule":
	//   1. Detect direct mutation intent in the prompt/problem text.
	//   2. Route directly to a deterministic FILE_MUTATE task.
	//   3. Skip investigation, test execution, and JSON synthesis.
	if !fastTrack {
		if target := detectDirectMutation(problem, ledgerContent); target != nil {
			e.emit(events.NewIntentParsed("direct_mutation", problem, 1.0))
			return []Task{*target}, nil
		}
	}

	// ── CANONICAL IMPORT MISMATCH (lx coordinate handshake) ──────────────
	// When the ledger contains a canonical import path mismatch error
	// ("module declares its path as: X but was required as: Y"), use the lx
	// daemon to resolve the exact file:line coordinates where the old path
	// appears. Then generate deterministic FILE_EDIT tasks at those coordinates
	// followed by SHELL_EXEC go mod tidy — replacing the SHELL_EXEC-only
	// short-circuit that previously bypassed precision file editing.
	//
	// VANILLA_WEB GUARD: This block is SKIPPED for HTML/CSS/JS workspaces that
	// have no Go files — the canonical import path signal is a false positive
	// from non-Go tooling that happens to emit similar-looking error text.
	//
	// This implements the "Lynx Coordinate Handshake" architectural spec:
	//   Step 1: Parse diagnostic output for canonical mismatch.
	//   Step 2: Leverage lx related/resolve for precision discovery (no full
	//           file loading into LLM context).
	//   Step 3: Minimal context ledger population (under 100 tokens).
	//   Step 4: Atomic execution blueprint (FILE_EDIT + SHELL_EXEC).
	if !fastTrack && !e.vanillaWeb && HasCanonicalImportMismatch(ledgerContent) {
		mismatch := retrieval.ParseCanonicalMismatch(ledgerContent)
		if mismatch != nil && mismatch.OldPath != "" && mismatch.NewPath != "" {
			router := retrieval.GetGlobalRouter()
			if router != nil {
				resolver := retrieval.NewSearchEngineResolver(router.Engine())
				//nolint:contextcheck // search engine API predates context propagation
				refs, err := resolver.ResolveCanonicalMismatch(mismatch)
				if err == nil && len(refs) > 0 {
					tasks := make([]Task, 0, len(refs)+2)
					for i, ref := range refs {
						desc := fmt.Sprintf("Replace import path %q with %q at %s:%d",
							mismatch.OldPath, mismatch.NewPath, ref.File, ref.StartLine)
						tasks = append(tasks, Task{
							StepNum:     i + 1,
							IsDone:      false,
							Status:      "idle",
							Type:        "FILE_MUTATE",
							Target:      ref.File,
							Description: desc,
							Rationale:   fmt.Sprintf("Canonical import mismatch resolved by search at %s:%d-%d", ref.File, ref.StartLine, ref.EndLine),
							Solution:    fmt.Sprintf("Replaced %q with %q in %s", mismatch.OldPath, mismatch.NewPath, ref.File),
							IsHardcoded: true,
						})
					}
					tidyStep := len(refs) + 1
					tasks = append(tasks, Task{
						StepNum:     tidyStep,
						IsDone:      false,
						Status:      "idle",
						Type:        "SHELL_EXEC",
						Target:      "go mod tidy",
						Description: "Re-synchronize the dependency manifest after canonical import fix.",
						Rationale:   "Clean up stale go.mod/go.sum entries after import path correction.",
						Solution:    "Dependency manifest re-synchronized.",
						IsHardcoded: true,
					})
					return tasks, nil
				}
			}
		}
	}

	// ── UNDEFINED SYMBOL (instant fast-path, zero LLM/lx) ─────────
	//
	// Phase 1 — Standard Library Case-Sensitivity Check
	// If the symbol is a capitalized stdlib package name (e.g., "Log" → "log"),
	// generate a deterministic FILE_EDIT with STDLIB solution format.
	//
	// Phase 2 — Deterministic fallback (zero external calls)
	// For non-stdlib or unresolvable symbol errors, construct a FILE_MUTATE
	// task directly from the error coordinates. No lx daemon, no LLM — the
	// error file/line/symbol already carries the exact fix location.
	//
	// VANILLA_WEB GUARD: This block is SKIPPED for HTML/CSS/JS workspaces —
	// "undefined" errors in JavaScript are normal runtime semantics, not
	// Go-style compilation errors, and stdlib case-correction is Go-specific.
	//
	// CRITICAL: Both paths complete in < 1ms. The LLM synthesis retry loop
	// and lx daemon handshake are NEVER reached for undefined symbol errors.
	if !fastTrack && !e.vanillaWeb && HasUndefinedSymbolError(ledgerContent) {
		undef := retrieval.ParseUndefinedSymbol(ledgerContent)
		if undef != nil && undef.Symbol != "" {
			sanitizedTarget, _ := retrieval.SanitizeTargetPath(undef.File)
			if sanitizedTarget == "" {
				sanitizedTarget = undef.File
			}

			// Phase 1: Standard library case-sensitivity correction.
			if pkgName, importPath, matched := retrieval.CheckStdlibCaseCorrection(undef.Symbol); matched {
				return []Task{
					{
						StepNum:     1,
						IsDone:      false,
						Status:      "idle",
						Type:        "FILE_MUTATE",
						Target:      sanitizedTarget,
						Description: fmt.Sprintf("Fix %q at %s:%d: replace %q with %q and add import %q", undef.Symbol, sanitizedTarget, undef.Line, undef.Symbol, pkgName, importPath),
						Rationale:   fmt.Sprintf("Undefined symbol %q is a capitalized stdlib package name — correct to %q.", undef.Symbol, pkgName),
						Solution:    fmt.Sprintf("STDLIB:%s:%s:%s", undef.Symbol, pkgName, importPath),
						IsHardcoded: true,
					},
				}, nil
			}

			// Phase 2: Deterministic fallback — no lx, no LLM.
			return []Task{
				{
					StepNum:     1,
					IsDone:      false,
					Status:      "idle",
					Type:        "FILE_MUTATE",
					Target:      sanitizedTarget,
					Description: fmt.Sprintf("Fix undefined symbol %q at %s:%d", undef.Symbol, sanitizedTarget, undef.Line),
					Rationale:   fmt.Sprintf("Undefined symbol %q at %s:%d — requires import or definition", undef.Symbol, sanitizedTarget, undef.Line),
					Solution:    fmt.Sprintf("Fix undefined symbol %q in %s", undef.Symbol, sanitizedTarget),
					IsHardcoded: true,
				},
			}, nil
		}
	}

	// REMOTE DEPENDENCY BLOCKER short-circuit: if the ledger explicitly
	// identifies a remote dependency through forensic analysis, bypass LLM
	// synthesis entirely and generate deterministic go get / go mod tidy
	// tasks. This guarantees 100% success for missing package resolution,
	// eliminating the 3-attempt JSON synthesis crash loop.
	if !fastTrack && strings.Contains(ledgerContent, "REMOTE DEPENDENCY BLOCKER") {
		conclusion := ExtractConclusionFromLedger(ledgerContent)
		if dep := dependencyFromConclusion(conclusion); dep != "" && !isPlaceholderToken(dep) {
			taskGet := Task{
				StepNum:     1,
				IsDone:      false,
				Status:      "idle",
				Type:        "SHELL_EXEC",
				Target:      fmt.Sprintf("go get %s", dep),
				Description: fmt.Sprintf("Install missing dependency %s to resolve compiler/import blocker.", dep),
				Rationale:   fmt.Sprintf("Inject the explicit third-party module %s missing from the execution boundary.", dep),
				Solution:    fmt.Sprintf("Missing package %s successfully resolves and dependency block clears.", dep),
				IsHardcoded: true,
			}
			taskTidy := Task{
				StepNum:     2,
				IsDone:      false,
				Status:      "idle",
				Type:        "SHELL_EXEC",
				Target:      "go mod tidy",
				Description: "Re-synchronize the dependency manifest with active imports after blocker identification.",
				Rationale:   "Re-synchronize the dependency manifest with active imports after blocker identification.",
				Solution:    "Clean up stale pointers and establish structural registry alignment.",
				IsHardcoded: true,
			}
			return []Task{taskGet, taskTidy}, nil
		}
		return []Task{
			{
				StepNum:     1,
				IsDone:      false,
				Status:      "idle",
				Type:        "SHELL_EXEC",
				Target:      "go mod tidy",
				Description: "Re-synchronize the dependency manifest with active imports after blocker identification.",
				Rationale:   "Re-synchronize the dependency manifest with active imports after blocker identification.",
				Solution:    "Clean up stale pointers and establish structural registry alignment.",
				IsHardcoded: true,
			},
		}, nil
	}

	e.emit(events.NewIntentParsed("plan.synthesize", problem, 0.8))

	var req ai.Request
	if fastTrack && len(fastPrompt) > 0 {
		req = ai.Request{
			Model: modelName,
			Messages: []ai.Message{
				{
					Role:    "system",
					Content: prompt.CompactPlanContract(),
				},
				{
					Role:    "user",
					Content: fastPrompt[0],
				},
			},
			Stream:    false,
			MaxTokens: 1536,
		}
	} else {
		// ── COMPACT SYNTHESIS SYSTEM PROMPT ──────────
		// Plan synthesis uses the model-agnostic PlanSynthesisSystemPrompt:
		// a single high-signal block (no identity/contract preamble) small
		// enough for Mini/7B models to follow without choking on context. It
		// enforces the Action (strategy), Target (file), Reason (rationale)
		// JSON output contract and forbids thinking blocks. Direct file
		// mutations bypass it with the zero-prose mutation prompt.
		isDirectMut := detectDirectMutation(problem, ledgerContent) != nil
		systemPrompt := prompt.PlanSynthesisSystemPrompt()
		if isDirectMut {
			systemPrompt = prompt.PlanDirectMutationSystemPrompt()
		}
		// MINI-MODEL HARDENING: small / non-reasoning cloud models (e.g. Cohere
		// North Mini) routinely emit narrative reasoning prose instead of the
		// required JSON. Inject an explicit raw-JSON-only constraint so the
		// output stays parseable instead of exhausting the silent retry budget.
		if c := prompt.MiniModelJSONConstraint(modelName); c != "" {
			systemPrompt += "\n\n" + c
		}
		// VANILLA_WEB ARCHETYPE: Strict negative constraints injected at the
		// end of the system prompt as a hard archetype lock. The LLM MUST NOT
		// generate any backend toolchain commands for HTML/CSS/JS workspaces.
		if e.vanillaWeb {
			systemPrompt += `

[CRITICAL ARCHETYPE LOCK: VANILLA_WEB]
Target workspace has NO backend toolchains.
FORBIDDEN COMMANDS: go, npm, cargo, pip, make.
ALLOWED ACTIONS: Pure file mutations on .html, .css, .js files only.`
		}

		// Extract the investigation conclusion so it can be injected as a
		// high-priority override signal. The conclusion carries the resolved
		// diagnosis (e.g. corrected dependency paths) that must take precedence
		// over raw error text when synthesising shell tasks.
		conclusion := ExtractConclusionFromLedger(ledgerContent)
		groundedPayload := e.GroundedConstraint()
		req = ai.Request{
			Model: modelName,
			Messages: []ai.Message{
				{
					Role:    "system",
					Content: systemPrompt,
				},
				{
					Role:    "user",
					Content: prompt.BuildPlanJSONPrompt(problem, ledgerContent, conclusion, isDirectMut, groundedPayload),
				},
			},
			Stream:    false,
			MaxTokens: 1536,
			ResponseFormat: &ai.ResponseFormat{
				Type: "json_object",
			},
		}
	}

	// UNDEFINED SYMBOL GUARDRAIL: When the ledger contains an undefined symbol
	// error, explicitly instruct the LLM to generate ONLY file modification tasks.
	// Shell execution commands like go mod tidy are NEVER valid for code typos.
	if !fastTrack && HasUndefinedSymbolError(ledgerContent) {
		req.Messages[len(req.Messages)-1].Content += `

[SYSTEM: UNDEFINED SYMBOL ERROR — CODE FIX ONLY]
The error is an undefined symbol/identifier typo in code. DO NOT generate ENV_DEPS or shell execution tasks like go mod tidy. Generate ONLY a FILE_MUTATE / CODE_MOD task targeting the source file containing the error. No SHELL_EXEC, no environment setup, no dependency installation.`
	}

	resp, err := e.complete(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("plan engine: provider call failed: %w", err)
	}

	if resp == nil || strings.TrimSpace(resp.Content) == "" {
		e.diagnoseSynthesisFailure("Provider returned an empty response for plan synthesis: no content and no reasoning/thinking text was emitted. This usually means the model produced only a thinking block or hit a context/output ceiling — retry with a smaller ledger or a different model.")
		return nil, fmt.Errorf("plan engine: empty response from provider — no content or reasoning/thinking text was emitted; retry with a smaller ledger or a different model")
	}

	// Persist raw plan output to disk.
	_ = e.store.SaveRawMarkdown("plan", resp.Content)

	if fastTrack && len(fastPrompt) > 0 {
		// Fast-track: the model returns a minimal markdown shell checklist. A
		// local 7B model may still emit the occasional placeholder/non-shell
		// task; rather than hard-aborting the whole plan, we keep only the valid
		// SHELL_EXEC tasks (placeholder FILE_MUTATE lines are dropped). If nothing
		// usable survives, we surface a clear fallback instead of a build abort.
		raw := ParseMarkdownToTasks(resp.Content)
		clean := make([]Task, 0, len(raw))
		for _, t := range raw {
			if t.Type == "SHELL_EXEC" && strings.TrimSpace(t.Target) != "" {
				clean = append(clean, t)
			}
		}
		if len(clean) == 0 {
			return nil, fmt.Errorf("plan engine: fast-track produced no runnable shell tasks (model returned: %s)", truncateForLog(resp.Content))
		}
		return ValidateShellExecCommands(clean, ledgerContent), nil
	}

	// ── JSON PARSING — ELEVATED SILENT RETRY LOOP ──────────────────
	// The loop covers the provider call, JSON code-fence stripping, structural
	// json.Unmarshal parsing, AND semantic SHELL_EXEC validation in a single
	// retry envelope. Both structural failures (truncated/malformed JSON) and
	// semantic failures (hallucinated file paths as SHELL_EXEC targets) trigger
	// an automated retry with an augmented prompt. This eliminates the manual
	// friction of /mode investigate ↔ /mode plan toggling by handling the
	// correction transparently.
	maxSilentRetries := 2
	// lastRawContent preserves the most recent non-empty model output across the
	// retry loop. The final response may be nil (a retry returned no content),
	// but the prose the model DID emit on an earlier attempt is still the best
	// signal the heuristic fallback can mine for file paths.
	lastRawContent := ""
	for attempt := 0; attempt <= maxSilentRetries; attempt++ {
		// On retry (attempt > 0), re-invoke the provider with an augmented
		// prompt that includes the strict enforcement instruction from the
		// previous rejection.
		if attempt > 0 {
			e.emit(events.NewStageCompleted("plan.synthesize.retry", 0,
				fmt.Sprintf("JSON syntax or command schema broken — refining prompt and retrying internally (Attempt %d/%d)", attempt, maxSilentRetries)))
			req.Messages[len(req.Messages)-1].Content += shellExecReinforcement(attempt, maxSilentRetries)
			var retryErr error
			resp, retryErr = e.complete(ctx, req)
			if retryErr != nil || resp == nil || resp.Content == "" {
				continue
			}
			_ = e.store.SaveRawMarkdown("plan", resp.Content)
		}
		if resp != nil && strings.TrimSpace(resp.Content) != "" {
			lastRawContent = resp.Content
		}

		jsonResult := ParseJSONPlan(resp.Content)

		if jsonResult.Valid && len(jsonResult.Tasks) > 0 {
			var candidates []Task
			if err := ValidateAllTasks(jsonResult.Tasks); err != nil {
				candidates = filterValidTasks(jsonResult.Tasks)
			} else {
				candidates = jsonResult.Tasks
			}

			// Align FILE_MUTATE targets with actual compiler error file paths
			// from the ledger. This prevents the LLM from hallucinating targets
			// like "syntax/main.go" when the real error is in "cmd/api/main.go".
			candidates = AlignFileTargetWithErrors(candidates, ledgerContent)

			// Filter out unsolicited new-file creation in pkg/ or internal/
			// when resolving single-file undefined symbol errors. This prevents
			// the LLM from generating over-engineered plans (e.g. creating
			// pkg/util/logs/log.go) for a trivial stdlib case fix.
			candidates = FilterUnsolicitedPkgFiles(candidates, ledgerContent)

			// Strip SHELL_EXEC/GIT_ACTION tasks when the error is an undefined
			// symbol. The LLM may hallucinate go mod tidy for what is actually
			// a code typo — this ensures only FILE_MUTATE tasks survive.
			candidates = FilterUndefinedSymbolShellExec(candidates, ledgerContent)

			// EVIDENCE-BASED ANTI-HALLUCINATION BARRIER: drop FILE_MUTATE /
			// FILE_EDIT targets that reference files not present on disk.
			// Prevents generic plans that modify every asset (script.js,
			// styles.css, etc.) on speculation — only files that actually
			// exist can be mutated.
			candidates = FilterNonExistentMutationTargets(candidates, e.rootPath)

			// VANILLA_WEB GUARD: Deterministic post-filter over raw LLM task
			// output. Strips Go toolchain tasks (go mod, go test, go get),
			// ENV_DEPS tasks, and falls back to a safe default when all tasks
			// are filtered out. This is the hard anti-escape barrier for
			// VANILLA_WEB archetype that overrides any LLM hallucination.
			if e.vanillaWeb {
				candidates = filterGoCommands(candidates)
				candidates = SanitizeTasksForArchetype(candidates, recon.VANILLA_WEB)
			}

			if len(candidates) > 0 {
				if !hasInvalidShellExecCommand(candidates) {
					// All checks passed — return with compile-error enforcement.
					return ForceShellExecOnCompileError(candidates, problem, ledgerContent), nil
				}

				// Semantic failure: invalid SHELL_EXEC commands detected.
				if attempt < maxSilentRetries {
					continue
				}

				// Max retries exceeded for semantic failures — deterministic fallback.
				return ValidateShellExecCommands(
					ForceShellExecOnCompileError(candidates, problem, ledgerContent),
					ledgerContent,
				), nil
			}
		}

		// Tolerant markdown fallback: Mini models sometimes emit task blocks
		// (- [ ] SHELL_EXEC: ... | why) despite the JSON instruction. Accept
		// them through the same validation pipeline instead of burning the
		// retry budget on reformatting.
		if md := e.tolerantMarkdownTasks(resp.Content, problem, ledgerContent); len(md) > 0 {
			return md, nil
		}

		// Structural parse failure or all candidates filtered out.
		if attempt < maxSilentRetries {
			continue
		}
	}

	// ── HEURISTIC PROSE FALLBACK ──────────────────────────────────────
	// Last resort before hard failure: the model emitted narrative reasoning
	// prose (a common failure mode of free/mini cloud models) that neither the
	// JSON parser nor the markdown task parser could consume. Mine the text for
	// file paths and construct one FILE_MUTATE task per detected file so the
	// execution survives instead of dying with "all 3 JSON synthesis attempts
	// failed". A clear plan.synthesize.fallback event notifies the user that a
	// heuristic plan was generated.
	if strings.TrimSpace(lastRawContent) != "" {
		if heuristic := extractTasksFromProse(lastRawContent); len(heuristic) > 0 {
			heuristic = FilterNonExistentMutationTargets(heuristic, e.rootPath)
			if e.vanillaWeb {
				heuristic = filterGoCommands(heuristic)
				heuristic = SanitizeTasksForArchetype(heuristic, recon.VANILLA_WEB)
			}
			if len(heuristic) > 0 {
				e.emit(events.NewPlanFallback("prose", fmt.Sprintf(
					"The model returned non-JSON prose; a heuristic plan with %d FILE_MUTATE task(s) was extracted from the mentioned file paths.", len(heuristic))))
				return ForceShellExecOnCompileError(heuristic, problem, ledgerContent), nil
			}
		}
	}

	// ── EMERGENCY FALLBACK ────────────────────────────────────────
	// All 3 LLM synthesis attempts (initial + 2 retries) failed to produce a
	// valid JSON plan. Try to extract a dependency from the conclusion for a
	// go get task; if no dependency is found, return a hard error instead of
	// hallucinating shell commands like go mod tidy.
	// NIL-SAFETY: after the retry loop the last provider response may be nil
	// (e.g. every retry returned a nil response), so resp is never
	// dereferenced without a guard.
	excerpt := "<no provider output>"
	if resp != nil {
		excerpt = truncateForLog(resp.Content)
	}
	e.diagnoseSynthesisFailure(fmt.Sprintf(
		"All %d plan synthesis attempts failed after sanitization. Last provider output excerpt: %q. The model produced neither parseable JSON nor task blocks.", maxSilentRetries+1, excerpt))
	if HasUndefinedSymbolError(ledgerContent) {
		return nil, fmt.Errorf("plan engine: all %d JSON synthesis attempts failed for undefined symbol error — could not determine correct code fix", maxSilentRetries+1)
	}
	if IsCompilationOrDependencyError(problem) || IsCompilationOrDependencyError(ledgerContent) {
		conclusion := ExtractConclusionFromLedger(ledgerContent)
		if dep := dependencyFromConclusion(conclusion); dep != "" && !isPlaceholderToken(dep) {
			return []Task{
				{
					StepNum:     1,
					IsDone:      false,
					Status:      "idle",
					Type:        "SHELL_EXEC",
					Target:      fmt.Sprintf("go get %s", dep),
					Description: fmt.Sprintf("Emergency fallback: install missing dependency %s", dep),
					IsHardcoded: true,
				},
			}, nil
		}
		return nil, fmt.Errorf("plan engine: all %d JSON synthesis attempts failed — no valid shell command could be derived", maxSilentRetries+1)
	}

	// ── ABSOLUTE FALLBACK ────────────────────────────────────────
	if hasGoFileParseError(ledgerContent) || hasGoFileParseError(problem) {
		return nil, fmt.Errorf("plan engine: all %d JSON synthesis attempts exhausted for compile error — no valid tasks could be synthesized", maxSilentRetries+1)
	}

	return nil, fmt.Errorf("plan engine: all %d JSON synthesis attempts failed and no dependency error detected", maxSilentRetries+1)
}

// diagnoseSynthesisFailure publishes a clear, actionable plan-synthesis
// diagnostic to the event bus so the presentation layer can surface context
// (stage, sanitized message) instead of a raw engine error. It is a no-op when
// no bus is wired or the engine is nil.
func (e *Engine) diagnoseSynthesisFailure(message string) {
	if e == nil {
		return
	}
	e.emit(events.NewStageCompleted("plan.synthesize.error", 0, message))
}

// tolerantMarkdownTasks parses a non-JSON model response as markdown task
// blocks and runs the full validation pipeline (target validity, disk
// existence, vanilla-web and shell-command guards). It returns nil when the
// output is unusable so the caller can fall back to the JSON retry loop.
func (e *Engine) tolerantMarkdownTasks(content, problem, ledgerContent string) []Task {
	md := ParseMarkdownToTasks(content)
	if len(md) == 0 {
		return nil
	}
	md = filterValidTasks(md)
	md = FilterNonExistentMutationTargets(md, e.rootPath)
	if e.vanillaWeb {
		md = filterGoCommands(md)
		md = SanitizeTasksForArchetype(md, recon.VANILLA_WEB)
	}
	if len(md) == 0 || hasInvalidShellExecCommand(md) {
		return nil
	}
	return ForceShellExecOnCompileError(md, problem, ledgerContent)
}

// compilerErrorFileRe extracts the exact file path from a Go compiler error
// line of the form "path/file.go:line:col: message". The captured group is
// the file path before the first colon-number sequence.
var compilerErrorFileRe = regexp.MustCompile(`([^\s:]+\.(go|ts|js|py|rs)):\d+:\d+:`)

// AlignFileTargetWithErrors validates and corrects FILE_MUTATE task targets
// against actual compiler error file paths extracted from the ledger content.
// If a non-hardcoded FILE_MUTATE target does not match any file path found in
// the compiler errors (e.g. the LLM hallucinated "syntax/main.go" instead of
// "cmd/api/main.go"), it is replaced with the correct path from the first
// matching compiler error. Hardcoded tasks (from lx resolution) are left
// unchanged since their targets are deterministic.
func AlignFileTargetWithErrors(tasks []Task, ledgerContent string) []Task {
	if len(tasks) == 0 || ledgerContent == "" {
		return tasks
	}
	errorFiles := parseCompilerErrorFiles(ledgerContent)
	if len(errorFiles) == 0 {
		return tasks
	}
	for i, t := range tasks {
		if t.Type != "FILE_MUTATE" && t.Type != "FILE_EDIT" {
			continue
		}
		if t.IsHardcoded {
			continue
		}
		if !matchesAnyErrorFile(t.Target, errorFiles) {
			tasks[i].Target = errorFiles[0]
			tasks[i].Rationale = fmt.Sprintf("Target aligned to compiler error file: %s", errorFiles[0])
		}
	}
	return tasks
}

// parseCompilerErrorFiles extracts unique file paths from Go compiler error
// lines in the given content. It matches lines like "cmd/api/main.go:9:2:
// undefined: x" using compilerErrorFileRe and returns the deduplicated list
// of file paths in occurrence order.
func parseCompilerErrorFiles(content string) []string {
	dedup := make(map[string]bool)
	var files []string
	for _, line := range strings.Split(content, "\n") {
		m := compilerErrorFileRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		f := m[1]
		if !dedup[f] {
			dedup[f] = true
			files = append(files, f)
		}
	}
	return files
}

// matchesAnyErrorFile reports whether the given target path matches any of the
// compiler error file paths. Comparison is done with filepath.Clean to handle
// variations like "./cmd/api/main.go" vs "cmd/api/main.go".
func matchesAnyErrorFile(target string, errorFiles []string) bool {
	for _, ef := range errorFiles {
		if target == ef {
			return true
		}
	}
	return false
}

// unsolicitedPkgPrefixes are path prefixes that indicate new helper/wrapper
// file creation. When resolving a single-file undefined symbol error, any
// LLM-generated task targeting these prefixes is considered unsolicited
// and rejected.
var unsolicitedPkgPrefixes = []string{
	"pkg/",
	"internal/",
}

// FilterUnsolicitedPkgFiles filters out LLM-generated tasks that attempt to
// create new files in pkg/ or internal/ when resolving a single undefined
// symbol error in a simple target file. This prevents the LLM from generating
// over-engineered plans (e.g. creating pkg/util/logs/log.go) for trivial
// stdlib case fixes. Hardcoded tasks are preserved.
func FilterUnsolicitedPkgFiles(tasks []Task, ledgerContent string) []Task {
	if len(tasks) == 0 || ledgerContent == "" {
		return tasks
	}
	// Only apply this filter when the ledger contains a single-file undefined
	// symbol error (which should be resolved with a simple fixed, not a new
	// package).
	if !HasUndefinedSymbolError(ledgerContent) {
		return tasks
	}
	// Determine the error file path.
	undef := retrieval.ParseUndefinedSymbol(ledgerContent)
	if undef == nil || undef.File == "" {
		return tasks
	}
	filtered := make([]Task, 0, len(tasks))
	for _, t := range tasks {
		if t.IsHardcoded {
			filtered = append(filtered, t)
			continue
		}
		if t.Type != "FILE_MUTATE" && t.Type != "FILE_EDIT" {
			filtered = append(filtered, t)
			continue
		}
		// Allow tasks targeting the actual error file.
		if t.Target == undef.File {
			filtered = append(filtered, t)
			continue
		}
		// Reject tasks targeting pkg/ or internal/ prefixes.
		rejected := false
		for _, prefix := range unsolicitedPkgPrefixes {
			if strings.HasPrefix(t.Target, prefix) {
				rejected = true
				break
			}
		}
		if !rejected {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

// FilterNonExistentMutationTargets drops FILE_MUTATE / FILE_EDIT tasks whose
// target file does not exist on disk under rootPath. Mutation tasks
// semantically modify EXISTING files — a target that is not present on disk is
// a hallucinated speculative asset (e.g. "script.js" in a workspace that has no
// such file) that would only fail later with "patch hunk does not match file
// content". This is the deterministic anti-hallucination barrier backing the
// EVIDENCE-BASED PLANNING prompt directive: no static/generic assumption that
// every asset needs modification survives unless the file actually exists.
//
// SHELL_EXEC / GIT_ACTION tasks and hardcoded tasks pass through untouched.
// When rootPath is empty (verification impossible), all tasks are preserved so
// behaviour is unchanged in headless/CLI contexts without a root.
func FilterNonExistentMutationTargets(tasks []Task, rootPath string) []Task {
	if len(tasks) == 0 {
		return tasks
	}
	if rootPath == "" {
		return tasks
	}
	filtered := make([]Task, 0, len(tasks))
	for _, t := range tasks {
		if t.Type == "FILE_MUTATE" || t.Type == "FILE_EDIT" {
			if t.IsHardcoded {
				filtered = append(filtered, t)
				continue
			}
			if !mutationTargetExists(t.Target, rootPath) {
				continue
			}
		}
		filtered = append(filtered, t)
	}
	return filtered
}

// mutationTargetExists reports whether the given task target resolves to an
// existing regular file under rootPath. Absolute targets are checked directly;
// relative targets are joined onto rootPath.
func mutationTargetExists(target, rootPath string) bool {
	if target == "" {
		return false
	}
	candidate := target
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(rootPath, candidate)
	}
	info, err := os.Stat(candidate)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// FilterUndefinedSymbolShellExec removes SHELL_EXEC and GIT_ACTION tasks
// when the primary error is an undefined symbol. The LLM may hallucinate
// go mod tidy for code typos; this filter ensures only FILE_MUTATE tasks
// survive. Hardcoded tasks (from lx resolution) are preserved.
// Returns the original slice unchanged if no undefined symbol error is detected
// or if no tasks need filtering. If all non-hardcoded tasks are removed,
// returns empty slice so the retry loop can re-prompt the LLM.
func FilterUndefinedSymbolShellExec(tasks []Task, ledgerContent string) []Task {
	if len(tasks) == 0 || ledgerContent == "" {
		return tasks
	}
	if !HasUndefinedSymbolError(ledgerContent) {
		return tasks
	}
	filtered := make([]Task, 0, len(tasks))
	for _, t := range tasks {
		if t.IsHardcoded {
			filtered = append(filtered, t)
			continue
		}
		if t.Type == "SHELL_EXEC" || t.Type == "GIT_ACTION" {
			continue
		}
		filtered = append(filtered, t)
	}
	if len(filtered) == 0 && len(tasks) > 0 {
		return filtered
	}
	return filtered
}

// filterValidTasks filters a task slice to only tasks with valid, non-empty
// targets. Invalid tasks are dropped silently — identical resilience pattern
// used by the fast-track path — so a local 7B model with one bad task does
// not abort the entire plan. Returns the original slice if all tasks are valid.
// filterGoCommands strips SHELL_EXEC tasks whose targets contain Go toolchain
// commands (go mod tidy, go get, go test, go build, go install, go run, go vet,
// go list, go clean). Used by the VANILLA_WEB archetype guard to prevent Go
// commands from reaching execution in HTML/CSS/JS workspaces.
func filterGoCommands(tasks []Task) []Task {
	clean := make([]Task, 0, len(tasks))
	for _, t := range tasks {
		if t.Type == "SHELL_EXEC" || t.Type == "GIT_ACTION" {
			target := strings.Fields(strings.TrimSpace(t.Target))
			if len(target) >= 2 && target[0] == "go" {
				continue // skip go mod tidy, go get, etc.
			}
		}
		clean = append(clean, t)
	}
	return clean
}

func filterValidTasks(tasks []Task) []Task {
	clean := make([]Task, 0, len(tasks))
	for _, t := range tasks {
		isValid, _ := ValidateTaskTarget(t.Target, t.Type)
		if isValid {
			clean = append(clean, t)
		}
	}
	return clean
}

// ForceShellExecOnCompileError enforces the IZEN /plan anti-escape law for
// compilation or dependency failures: when the root cause is a build/dep error,
// the plan MUST resolve it through go.mod / SHELL_EXEC (e.g. `go get`,
// `go mod tidy`) — NEVER by patching documentation or unrelated source files.
//
// HARDENING: SHELL_EXEC tasks are REJECTED when the primary blocker is an
// undefined symbol error (e.g. undefined: Log), because the LLM routinely
// hallucinates go mod tidy for what is actually a stdlib case typo. The
// exception is when a go.mod/go.sum missing file error is explicitly present,
// indicating a real dependency issue rather than a code typo.
//
// If the synthesized tasks already contain a SHELL_EXEC task, they are returned
// unchanged (the model complied). Otherwise a deterministic SHELL_EXEC recovery
// task is prepended so the build engine always has a runnable shell step to
// clear the blocker instead of stalling or escaping into README.md.
func ForceShellExecOnCompileError(tasks []Task, problem, ledgerContent string) []Task {
	if len(tasks) == 0 {
		return tasks
	}
	if !IsCompilationOrDependencyError(problem) && !IsCompilationOrDependencyError(ledgerContent) {
		return tasks
	}
	// Ban SHELL_EXEC for undefined symbol errors unless go.mod/go.sum missing.
	if HasUndefinedSymbolError(ledgerContent) && !hasGoModMissingError(ledgerContent) {
		return tasks
	}
	for _, t := range tasks {
		if t.Type == "SHELL_EXEC" && strings.TrimSpace(t.Target) != "" {
			return tasks
		}
	}

	// No shell task present → prepend a deterministic dependency-resolution
	// SHELL_EXEC. Prefer the corrected dependency path from the investigation
	// conclusion when available; otherwise fall back to `go mod tidy`.
	cmd := "go mod tidy"
	if conclusion := ExtractConclusionFromLedger(ledgerContent); conclusion != "" {
		if dep := dependencyFromConclusion(conclusion); dep != "" && !isPlaceholderToken(dep) {
			cmd = fmt.Sprintf("go get %s", dep)
		}
	}
	recovery := Task{
		StepNum:     0,
		IsDone:      false,
		Status:      "idle",
		Type:        "SHELL_EXEC",
		Target:      cmd,
		Description: "Resolve compilation/dependency blocker via module tooling (forced by /plan anti-escape law)",
	}
	out := make([]Task, 0, len(tasks)+1)
	out = append(out, recovery)
	out = append(out, tasks...)
	for i := range out {
		out[i].StepNum = i + 1
	}
	return out
}

// hasGoModMissingError reports whether the content indicates a missing go.mod
// or go.sum file error. This is the exception to the SHELL_EXEC ban for
// undefined symbol errors: when go.mod is genuinely missing, a shell task
// like `go mod tidy` is appropriate.
func hasGoModMissingError(content string) bool {
	lower := strings.ToLower(content)
	return strings.Contains(lower, "go.mod") || strings.Contains(lower, "go.sum")
}

// knownShellBinaries is the set of recognised executable binaries that a
// SHELL_EXEC target may legitimately start with. Any first token outside this
// set — especially bare file paths like "go.mod" or "relative/path/to/go.mod" —
// is treated as a hallucinated command and triggers the deterministic fallback
// in ValidateShellExecCommands.
// SanitizeTasksForArchetype is a strict programmatic post-filter applied to
// raw LLM task output before staging the plan. For VANILLA_WEB archetype:
//   - Discards ANY task containing "go mod", "go test", "go get", or "ENV_DEPS" kind
//   - If all tasks are filtered out, falls back to a single default task:
//     "Inspecting and fixing static HTML/CSS/JS files"
//
// For non-VANILLA_WEB archetypes, tasks are returned unchanged.
func SanitizeTasksForArchetype(tasks []Task, archetype recon.ProjectArchetype) []Task {
	if archetype != recon.VANILLA_WEB || len(tasks) == 0 {
		return tasks
	}
	clean := make([]Task, 0, len(tasks))
	for _, t := range tasks {
		target := strings.ToLower(strings.TrimSpace(t.Target))
		desc := strings.ToLower(t.Description)
		// Discard tasks with forbidden Go commands or ENV_DEPS kind.
		if t.Type == "ENV_DEPS" {
			continue
		}
		if strings.Contains(target, "go mod") ||
			strings.Contains(target, "go test") ||
			strings.Contains(target, "go get") ||
			strings.Contains(desc, "go mod") ||
			strings.Contains(desc, "go test") ||
			strings.Contains(desc, "go get") {
			continue
		}
		clean = append(clean, t)
	}
	if len(clean) == 0 {
		// All tasks filtered out — fallback to a single default task.
		return []Task{
			{
				StepNum:     1,
				IsDone:      false,
				Status:      "idle",
				Type:        "FILE_MUTATE",
				Target:      "",
				Description: "Inspecting and fixing static HTML/CSS/JS files",
				Rationale:   "All LLM-generated tasks were filtered out by VANILLA_WEB archetype guard",
				IsHardcoded: true,
			},
		}
	}
	return clean
}

var knownShellBinaries = map[string]bool{
	"go":             true,
	"git":            true,
	"make":           true,
	"npm":            true,
	"npx":            true,
	"yarn":           true,
	"pip":            true,
	"pip3":           true,
	"cargo":          true,
	"brew":           true,
	"docker":         true,
	"docker-compose": true,
	"cd":             true,
	"mkdir":          true,
	"cp":             true,
	"mv":             true,
	"rm":             true,
	"touch":          true,
	"echo":           true,
	"cat":            true,
	"curl":           true,
	"wget":           true,
	"chmod":          true,
	"chown":          true,
	"python":         true,
	"python3":        true,
	"node":           true,
	"deno":           true,
	"bun":            true,
	"ls":             true,
	"grep":           true,
	"rg":             true,
	"sed":            true,
	"awk":            true,
	"find":           true,
	"sort":           true,
	"tee":            true,
	"ln":             true,
	"source":         true,
	"export":         true,
	"sudo":           true,
	"bash":           true,
	"sh":             true,
	"zsh":            true,
	"terraform":      true,
	"tofu":           true,
	"kubectl":        true,
	"helm":           true,
	"go.mod":         false, // explicitly NOT a valid binary
	"go.sum":         false, // explicitly NOT a valid binary
}

// isValidShellCommand checks whether a SHELL_EXEC target is a valid runnable
// command rather than a hallucinated file path or placeholder text. A command
// is valid when its first token is a known binary and it is not a bare file
// path (e.g. "relative/path/to/go.mod", "./go.mod", or "go.mod" as a bare
// command).
func isValidShellCommand(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return false
	}
	// Forbid bare file paths ending in .mod or .sum.
	if strings.HasSuffix(cmd, ".mod") || strings.HasSuffix(cmd, ".sum") {
		return false
	}
	first := strings.Fields(cmd)[0]
	// Forbid relative/absolute paths as the command token.
	if strings.Contains(first, "/") {
		return false
	}
	// Forbid bare go.mod/go.sum invoked as a command.
	if first == "go.mod" || first == "go.sum" || first == "go.work" {
		return false
	}
	return knownShellBinaries[first]
}

// ValidateShellExecCommands checks all SHELL_EXEC tasks for valid command
// format per isValidShellCommand. If any SHELL_EXEC target is invalid — a
// bare file path, ends in .mod/.sum, or does not start with a known binary —
// the entire LLM output is rejected and replaced with a deterministic fallback
// derived from the forensic ledger conclusion. This prevents local 7B models
// from hallucinating execution commands like "relative/path/to/go.mod" as
// SHELL_EXEC targets.
func ValidateShellExecCommands(tasks []Task, ledgerContent string) []Task {
	if len(tasks) == 0 {
		return tasks
	}
	for _, t := range tasks {
		if t.Type != "SHELL_EXEC" {
			continue
		}
		if t.IsHardcoded {
			continue
		}
		if !isValidShellCommand(t.Target) {
			conclusion := ExtractConclusionFromLedger(ledgerContent)
			if dep := dependencyFromConclusion(conclusion); dep != "" && !isPlaceholderToken(dep) {
				return []Task{
					{
						StepNum:     1,
						IsDone:      false,
						Status:      "idle",
						Type:        "SHELL_EXEC",
						Target:      fmt.Sprintf("go get %s", dep),
						Description: fmt.Sprintf("Install missing dependency %s (sanitized: LLM produced invalid command)", dep),
					},
				}
			}
			return []Task{
				{
					StepNum:     1,
					IsDone:      false,
					Status:      "idle",
					Type:        "SHELL_EXEC",
					Target:      "go mod tidy",
					Description: "Resolve dependency blocker (sanitized: LLM produced invalid command)",
				},
			}
		}
	}
	return tasks
}

// hasInvalidShellExecCommand returns true if any SHELL_EXEC task in the slice
// has a target that fails isValidShellCommand. Unlike ValidateShellExecCommands,
// this is a pure check with no side effects — used by the silent retry loop to
// detect LLM command hallucination without triggering deterministic substitution.
func hasInvalidShellExecCommand(tasks []Task) bool {
	for _, t := range tasks {
		if t.Type == "SHELL_EXEC" && !t.IsHardcoded && !isValidShellCommand(t.Target) {
			return true
		}
	}
	return false
}

// shellExecReinforcement returns the strict enforcement instruction appended to
// the prompt on each silent retry attempt. It reminds the model what format
// SHELL_EXEC targets must follow after a previous hallucination failure.
func shellExecReinforcement(attempt, maxRetries int) string {
	return fmt.Sprintf("\n\n[SYSTEM: CRITICAL FAILURE PREVENTED] (Retry %d/%d) The SHELL_EXEC target you just generated was rejected because it is not a valid runnable command. You MUST output a real executable command — e.g. 'go get <package>', 'go mod tidy', 'git clone <url>' — NOT a file path. FORBIDDEN targets include: 'go.mod', 'go.sum', './relative/path', 'relative/path/to/go.mod', or any bare file name. The target must start with a known binary name like go, git, make, npm, docker, etc.",
		attempt, maxRetries)
}

// isPlaceholderToken reports whether s is a raw template placeholder
// (e.g. "<exact_package_path>", "<pkg>", "<module_path>", "<package>")
// that must never be used as a real command target. The heuristic is any
// string containing angle-bracket-delimited content — these are LLM prompt
// template markers, not actual package paths.
func isPlaceholderToken(s string) bool {
	s = strings.TrimSpace(s)
	return strings.Contains(s, "<") && strings.Contains(s, ">")
}

// dependencyFromConclusion extracts a plausible module path from an
// investigation conclusion string (e.g. "use github.com/moby/moby/client").
// It returns the first token that looks like a Go module path; empty otherwise.
//
// The REMOTE DEPENDENCY BLOCKER token may be appended inline behind a semicolon
// (e.g. "...; ## REMOTE DEPENDENCY BLOCKER (lx bypassed): [pkg](url)") rather
// than on its own line, so this function performs a GLOBAL substring scan and
// robustly isolates the trailing package identifier regardless of inline
// semicolon / space / newline noise or markdown-link wrapping.
func dependencyFromConclusion(conclusion string) string {
	// Parse the explicit package trailing the REMOTE DEPENDENCY BLOCKER token.
	// This guarantees we apply the real package the forensic analysis recorded
	// (e.g. github.com/docker/docker/client) instead of heuristic-matching an
	// unrelated token in the conclusion text.
	const token = "## REMOTE DEPENDENCY BLOCKER (lx bypassed): "
	if idx := strings.Index(conclusion, token); idx >= 0 {
		rest := conclusion[idx+len(token):]
		// The package may be on the same inline line behind a semicolon, or
		// wrapped in a markdown link [pkg](url). Isolate the first candidate
		// package token, stripping inline formatting noise aggressively.
		if pkg := extractPackageFromBlockerTail(rest); pkg != "" {
			return pkg
		}
	}

	// Fallback: heuristic scan for a well-formed package path.
	for _, tok := range strings.Fields(conclusion) {
		t := strings.TrimRight(strings.TrimLeft(tok, "\"'"), "\"'.,")
		if isWellFormedModulePath(t) {
			return t
		}
	}
	return ""
}

// extractPackageFromBlockerTail isolates the dependency package path from the
// tail string that follows the REMOTE DEPENDENCY BLOCKER token. It handles:
//   - inline semicolon-separated noise ("...; pkg")
//   - markdown link wrapping ("[pkg](url)")
//   - trailing punctuation / parentheses
//   - visually-clipped fragments (e.g. "g...") by falling back to the clean
//     namespace embedded inside a markdown link or the first well-formed path.
func extractPackageFromBlockerTail(rest string) string {
	// Split on any inline separator so a leading "..." fragment (before a
	// semicolon) does not poison the extraction.
	for _, seg := range strings.FieldsFunc(rest, func(r rune) bool {
		return r == ';' || r == '\n' || r == '\r' || r == '\t'
	}) {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		// Unwrap a markdown link: [pkg](url) → pkg. Also tolerate a bare
		// markdown link with no following url.
		if pkg := unwrapMarkdownLink(seg); pkg != "" {
			if isWellFormedModulePath(pkg) {
				return pkg
			}
			// Clipped fragment inside the link (e.g. "g...") — keep scanning
			// for a clean namespace elsewhere in the segment.
		}
		// Plain token: strip trailing punctuation and parentheses.
		candidate := strings.TrimRight(seg, ".,;:)]}")
		candidate = strings.TrimLeft(candidate, "([")
		if isWellFormedModulePath(candidate) {
			return candidate
		}
	}
	return ""
}

// unwrapMarkdownLink extracts the link text from a markdown link of the form
// [text](url). If the segment is not a markdown link it returns empty string.
func unwrapMarkdownLink(seg string) string {
	seg = strings.TrimSpace(seg)
	open := strings.Index(seg, "[")
	if open < 0 {
		return ""
	}
	closeB := strings.Index(seg[open:], "]")
	if closeB < 0 {
		return ""
	}
	text := seg[open+1 : open+closeB]
	// Defensive: if the link text itself is a clipped fragment (e.g. "g...")
	// but a full URL follows, recover the namespace from the URL host+path.
	if isClippedFragment(text) {
		if urlStart := strings.Index(seg[open+closeB:], "("); urlStart >= 0 {
			urlEnd := strings.Index(seg[open+closeB+urlStart:], ")")
			if urlEnd >= 0 {
				url := seg[open+closeB+urlStart+1 : open+closeB+urlStart+urlEnd]
				if cleaned := modulePathFromURL(url); cleaned != "" {
					return cleaned
				}
			}
		}
	}
	return strings.TrimSpace(text)
}

// modulePathFromURL recovers a Go module path from a repository URL such as
// https://github.com/docker/docker/client → github.com/docker/docker/client.
func modulePathFromURL(url string) string {
	url = strings.TrimSpace(url)
	url = strings.TrimPrefix(url, "https://")
	url = strings.TrimPrefix(url, "http://")
	url = strings.TrimSuffix(url, "/")
	if url == "" {
		return ""
	}
	return url
}

// isClippedFragment reports whether a token is a visually-clipped package
// fragment (e.g. "g...", "github.com/do...") rather than a usable module path.
func isClippedFragment(tok string) bool {
	if strings.Contains(tok, "...") {
		return true
	}
	// A path that ends mid-segment with no final element is also clipped.
	if strings.HasSuffix(tok, "/") {
		return true
	}
	return false
}

// isWellFormedModulePath reports whether tok looks like a usable Go module path:
// it must contain a dot (domain) and either a slash or a known module host
// prefix. Clipped fragments are explicitly rejected so the caller can fall back.
func isWellFormedModulePath(tok string) bool {
	tok = strings.TrimSpace(tok)
	if tok == "" || isClippedFragment(tok) {
		return false
	}
	return strings.Contains(tok, ".") &&
		(strings.Contains(tok, "/") ||
			strings.HasPrefix(tok, "github.com") ||
			strings.HasPrefix(tok, "golang.org"))
}

// truncateForLog caps a model response excerpt so error messages stay readable.
// Uses rune-aware slicing to avoid splitting multi-byte UTF-8 characters.
func truncateForLog(s string) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) > 200 {
		return string(runes[:200]) + "..."
	}
	return s
}

// ProcessPlan generates an execution plan by dispatching to the AI provider
// with strict JSON output enforcement. When the objective indicates a
// direct file mutation, bypasses the Senior Architect prompt and uses
// the zero-prose direct mutation prompt instead.
func (e *Engine) ProcessPlan(ctx context.Context, modelName string, objective string, contextStr string) error {
	if e == nil || e.provider == nil {
		return nil
	}

	isDirectMut := detectDirectMutation(objective, "") != nil

	req := ai.Request{
		Model: modelName,
		Messages: []ai.Message{
			{
				Role:    "system",
				Content: prompt.PlanSynthesisSystemPrompt(),
			},
			{
				Role:    "user",
				Content: prompt.BuildPlanPrompt(objective, contextStr, isDirectMut, ""),
			},
		},
		Stream:    false,
		MaxTokens: 1536,
	}

	if isDirectMut {
		req.Messages[0].Content = prompt.PlanDirectMutationSystemPrompt()
	}

	resp, err := e.provider(ctx, req)
	if err != nil {
		return err
	}
	if resp == nil {
		return fmt.Errorf("plan engine: provider returned a nil response")
	}

	return e.store.SaveRawMarkdown("plan", resp.Content)
}

// Parse parses plan content (JSON or markdown) into tasks.
func (e *Engine) Parse(content string) []Task {
	return e.parser(content)
}

// ParseJSON parses JSON plan content specifically.
func (e *Engine) ParseJSON(content string) (*PlanOutput, error) {
	result := ParseJSONPlan(content)
	if !result.Valid {
		return nil, &PlanSchemaError{Message: result.Error}
	}
	return result.Plan, nil
}

// Store returns the underlying PlanStore for direct access.
func (e *Engine) Store() *PlanStore {
	return e.store
}

// TickTask marks the N-th task as complete in the current plan file.
func (e *Engine) TickTask(stepNum int) error {
	return e.store.TickTaskHoanThanh(stepNum)
}

// directMutationVerbs are verbs/phrases that signal an intent to
// perform a simple file replacement or format conversion rather than
// a diagnosis or investigation. Order matters: longer phrases first.
var directMutationVerbs = []string{
	"refactor", "change", "convert", "replace", "update", "modify",
	"reformat", "transform", "switch", "migrate", "change to",
}

// directMutationFilePattern matches prompts that reference a specific
// file and want to change its content or format (e.g. "refactor MIT LICENSE to APACHE").
var directMutationFilePattern = regexp.MustCompile(`(?i)(license|readme|dockerfile|makefile|\.env|\.gitignore)\b`)

// detectDirectMutation inspects the problem description and ledger
// content to determine whether this is a simple file mutation that
// should bypass the investigation/LLM synthesis pipeline entirely.
// Returns a deterministic hardcoded FILE_MUTATE task when the input
// qualifies, or nil when normal processing should continue.
func detectDirectMutation(problem string, ledgerContent string) *Task {
	combined := strings.ToLower(strings.TrimSpace(problem) + " " + strings.TrimSpace(ledgerContent))
	if combined == "" {
		return nil
	}

	hasVerb := false
	for _, v := range directMutationVerbs {
		if strings.Contains(combined, v) {
			hasVerb = true
			break
		}
	}
	if !hasVerb {
		return nil
	}

	if !directMutationFilePattern.MatchString(combined) {
		return nil
	}

	targetFile := extractMutationTarget(combined)
	if targetFile == "" {
		targetFile = "LICENSE"
	}

	sourceFormat, targetFormat := extractFormatChange(combined)

	solution := fmt.Sprintf("File %s mutated successfully.", targetFile)
	if sourceFormat != "" && targetFormat != "" {
		solution = fmt.Sprintf("Converted %s from %s to %s in %s.", targetFile, sourceFormat, targetFormat, targetFile)
	}

	return &Task{
		StepNum:     1,
		IsDone:      false,
		Status:      "idle",
		Type:        "FILE_MUTATE",
		Target:      targetFile,
		Description: fmt.Sprintf("Refactor %s: %s%s", targetFile, sourceFormat, targetFormat),
		Rationale:   "Direct file mutation detected — bypass investigation and LLM synthesis.",
		Solution:    solution,
		IsHardcoded: true,
	}
}

// extractMutationTarget finds the target filename from a mutation prompt string.
func extractMutationTarget(lower string) string {
	if strings.Contains(lower, "license") {
		return "LICENSE"
	}
	if strings.Contains(lower, "readme") {
		return "README.md"
	}
	if strings.Contains(lower, "dockerfile") {
		return "Dockerfile"
	}
	if strings.Contains(lower, "makefile") {
		return "Makefile"
	}
	if strings.Contains(lower, ".env") {
		return ".env"
	}
	if strings.Contains(lower, ".gitignore") {
		return ".gitignore"
	}
	return "LICENSE"
}

// extractFormatChange tries to identify the source and target formats
// from a mutation prompt (e.g. "MIT" → "APACHE_2.0").
func extractFormatChange(lower string) (sourceFormat, targetFormat string) {
	formatPatterns := []struct {
		source string
		target string
	}{
		{"mit", "apache_2.0"},
		{"apache", "mit"},
		{"gpl", "mit"},
		{"mit", "gpl"},
		{"bsd", "apache_2.0"},
		{"apache", "bsd"},
	}
	for _, fp := range formatPatterns {
		if strings.Contains(lower, fp.source) {
			sourceFormat = strings.ToUpper(fp.source)
			targetFormat = strings.ToUpper(fp.target)
			return sourceFormat, targetFormat
		}
	}
	return "", ""
}

// PlanSchemaError indicates a plan output schema violation.
type PlanSchemaError struct {
	Message string
}

func (e *PlanSchemaError) Error() string {
	return "plan output schema violation: " + e.Message
}
