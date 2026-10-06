package execution

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/PizenLabs/izen/internal/core/authorization"
	"github.com/PizenLabs/izen/internal/language"
)

// ── Micro-Fix Loop Architecture ──────────────────────────────────────────────
//
// The micro-fix loop uses the host compiler as a deterministic structural
// guardrail. Immediately after a code patch is hot-applied to disk, a
// low-overhead local compiler check runs. If syntax degradation is detected
// (e.g., missing closing brace, undefined package), the patch is rolled back,
// the specific faulty lines and error message are extracted, and a pinpointed
// high-velocity micro-prompt is routed back for a targeted syntax fix.
//
// The loop operates at the execution layer (not the LLM layer) — it is a
// native compiler check that never reaches the context prompt unless the
// micro-fix prompt is explicitly generated.
// ─────────────────────────────────────────────────────────────────────────────

// SyntaxErrorRe matches compiler error lines and extracts file:line:message
// triples used by the micro-fix loop to pinpoint exact failure coordinates.
var SyntaxErrorRe = regexp.MustCompile(`^([^:]+\.\w+):(\d+):\s*(.+)$`)

// hallucinatedPrefixes are line prefixes injected by local LLMs inside code
// blocks that must be stripped before the content reaches the patch engine.
var hallucinatedPrefixes = []string{
	"FILE:",
	"file:",
	"**FILE_CREATE:",
	"**FILE_CREATE ",
	"**FILE_CREATE**",
	"<<<<<<< FILE_CREATE:",
	"<<<<<<< FILE_CREATE ",
	">>>>>>> END_FILE",
	"[target]",
	"[Target]",
	"[/target]",
	"[/Target]",
	"```diff",
	"```go",
	"```rust",
	"```python",
	"```typescript",
	"```javascript",
	"```",
}

// hallucinatedRe matches stray markdown artifact patterns that local models
// hallucinate as standalone lines within code blocks.
var hallucinatedRe = regexp.MustCompile(`(?i)^\s*(\*\*FILE_CREATE:?[^*]*\*\*|<<<<<<< FILE_CREATE:?.*|\[/?(code|file|source|block|end|diff)\])\s*$`)

// SyntaxError is a parsed compiler syntax error with structured position info.
type SyntaxError struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// ParseSyntaxErrors extracts structured SyntaxError entries from raw compiler
// output. Used by the micro-fix loop to build high-velocity fix prompts.
func ParseSyntaxErrors(output string) []SyntaxError {
	if output == "" {
		return nil
	}
	var errors []SyntaxError
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		m := SyntaxErrorRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		lineNum, _ := strconv.Atoi(m[2])
		errors = append(errors, SyntaxError{
			File:    m[1],
			Line:    lineNum,
			Message: strings.TrimSpace(m[3]),
		})
	}
	return errors
}

// BuildMicroFixPrompt constructs a high-velocity, pinpointed micro-prompt
// targeting specific syntax errors in a file. The prompt is small enough
// (typically <200 tokens) for a rapid local LLM inference.
func BuildMicroFixPrompt(file string, errors []SyntaxError) string {
	if len(errors) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("### MICRO-FIX: Syntax Repair Required\n")
	fmt.Fprintf(&b, "File: %s\n", file)
	b.WriteString("The following syntax errors were detected after attempting a patch:\n\n")

	for _, e := range errors {
		fmt.Fprintf(&b, "- Line %d: %s\n", e.Line, e.Message)
	}

	b.WriteString("\nOutput ONLY the corrected file content. No explanations. No markdown fences.\n")
	b.WriteString("Preserve all surrounding code exactly. Fix only the reported syntax issues.\n")
	return b.String()
}

// SanitizeLLMResponse cleans hallucinated metadata artifacts from raw LLM
// responses before they enter the patch engine. Local models commonly inject
// structural decorators like "FILE: path/to/file" or "[target]" inside code
// blocks, which would otherwise corrupt the file content or unified diff.
//
// This function strips:
//   - Lines starting with FILE: (case-sensitive, common local LLM decoration)
//   - Standalone [target] / [/target] markers
//   - Stray code-fence lines (```diff, ```go, etc.) that leak inside blocks
//   - Lines matching [/?(code|file|source|block|end|diff)] markers
//
// fencePrefixRe matches a markdown code-fence opening line, with or without an
// attached language identifier (e.g. "```", "```go", "```mit text"). It is
// anchored to the START of the string only so it correctly detects a leading
// fence even when the rest of the file follows on subsequent lines.
var fencePrefixRe = regexp.MustCompile("^\\s*`{3}[a-zA-Z0-9._-]*\\s*")

// stripMarkdownFences removes a single wrapping markdown code block from a raw
// LLM response. Local models frequently return file contents wrapped in
// "```lang ... ```"; writing that verbatim injects literal triple backticks into
// the file and corrupts its syntax. We strip a leading opening fence (with any
// optional language identifier) and a trailing closing fence, returning the
// core content. The function is a no-op for already-clean (unwrapped) input — it
// returns it unchanged so legitimate trailing newlines are preserved.
func stripMarkdownFences(content string) string {
	stripped := false

	// Strip a leading opening fence (``` or ```go, etc.).
	if fencePrefixRe.MatchString(content) {
		if idx := strings.Index(content, "\n"); idx != -1 {
			content = content[idx+1:]
			stripped = true
		}
	}

	// Strip a trailing closing fence.
	if strings.HasSuffix(content, "```") {
		content = strings.TrimSuffix(content, "```")
		stripped = true
	}

	if !stripped {
		return content
	}
	return strings.TrimSpace(content)
}

func SanitizeLLMResponse(raw string) string {
	if raw == "" {
		return raw
	}
	// Strip a whole response that the model wrapped in a single markdown code
	// block (e.g. "```mit ... ```") before any interior-line cleaning.
	raw = stripMarkdownFences(raw)
	lines := strings.Split(raw, "\n")
	var result []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			result = append(result, line)
			continue
		}
		skip := false
		for _, prefix := range hallucinatedPrefixes {
			if strings.HasPrefix(trimmed, prefix) {
				skip = true
				break
			}
		}
		if !skip && hallucinatedRe.MatchString(trimmed) {
			skip = true
		}
		if skip {
			continue
		}
		result = append(result, line)
	}
	return strings.Join(result, "\n")
}

// FirstPassingSteps runs only the syntax-critical verification steps (fmt, vet,
// build) — the "quick check" used by the micro-fix loop, skipping slower steps
// like full test suites and linters.
var SyntaxQuickCheckSteps = []VerificationStep{
	{Name: "go fmt", Command: "go fmt ./...", Optional: false},
	{Name: "go vet", Command: "go vet ./...", Optional: false},
}

type VerificationStep struct {
	Name     string `json:"name"`
	Command  string `json:"command"`
	Optional bool   `json:"optional"`
}

type VerificationResult struct {
	Step   VerificationStep `json:"step"`
	Passed bool             `json:"passed"`
	Output string           `json:"output,omitempty"`
	Error  string           `json:"error,omitempty"`
	// SyntaxErrors are extracted by the micro-fix loop when the step fails.
	SyntaxErrors []SyntaxError `json:"syntax_errors,omitempty"`
}

type VerificationReport struct {
	Results []VerificationResult `json:"results"`
	Passed  bool                 `json:"passed"`
	// Skipped is true when NO verification contract exists for the target's
	// language (unknown language, or a language definition with an empty
	// Verification config). It is semantically NOT APPLICABLE — distinct from
	// a verification that ran and failed. A skipped gate never claims a pass
	// and never rolls back a patch.
	Skipped bool `json:"skipped,omitempty"`
	// Reason explains why verification was skipped ("" when it ran).
	Reason string `json:"reason,omitempty"`
}

type Verifier struct {
	root   string
	steps  []VerificationStep
	langID language.ID
	auth   *authorization.MutationAuthorization
	// explicit records whether `steps` was supplied by an operator through
	// SetCustomSteps rather than DERIVED from a language definition. An
	// explicitly supplied contract is itself an authority: the per-target
	// artifact-identity seam refines DERIVED contracts and leaves explicit ones
	// exactly as given. Silently replacing an injected contract with a derived
	// one would make the injection a lie.
	explicit bool
}

func (v *Verifier) SetAuthorization(auth *authorization.MutationAuthorization) {
	v.auth = auth
}

func (v *Verifier) Authorization() *authorization.MutationAuthorization {
	return v.auth
}

func NewVerifier(root string) *Verifier {
	// A plain verifier carries NO implicit steps. It never falls back to the
	// Go verification commands on its own: verification steps must be attached
	// explicitly via SetCustomSteps or via SetLanguage (which resolves the
	// language's own configured steps). A verifier with no steps reports
	// Skipped — semantically "no verification applicable", never a fabricated
	// pass and never a Go fallback (Phase 7 P1).
	return &Verifier{root: root}
}

func NewLanguageVerifier(root string, langID language.ID) *Verifier {
	v := &Verifier{
		root:   root,
		langID: langID,
	}
	v.steps = stepsForLanguage(langID)
	return v
}

// RunSyntaxQuickCheck runs only the syntax-critical steps (fmt, vet) for the
// micro-fix loop. It returns a VerificationReport with parsed SyntaxErrors on
// failures. This is faster than a full RunAll and is designed for the tight
// micro-fix loop.
func (v *Verifier) RunSyntaxQuickCheck(ctx context.Context) VerificationReport {
	steps := SyntaxQuickCheckSteps
	if v.steps != nil {
		steps = v.steps
		if len(steps) > 2 {
			steps = steps[:2]
		}
	}

	var report VerificationReport
	report.Passed = true

	for _, step := range steps {
		if step.Optional {
			continue
		}
		result := v.runStep(ctx, step)
		if !result.Passed {
			result.SyntaxErrors = ParseSyntaxErrors(result.Output)
		}
		report.Results = append(report.Results, result)
		if !result.Passed {
			report.Passed = false
		}
	}

	return report
}

func stepsForLanguage(langID language.ID) []VerificationStep {
	def, ok := language.Global().Lookup(langID)
	if !ok {
		// Unknown language: no verification contract exists. Return nil —
		// NEVER fall back to the Go verification steps for a language the
		// registry does not know (Phase 7 P1).
		return nil
	}

	v := def.Verification
	var steps []VerificationStep

	for _, cmd := range v.Fmt {
		steps = append(steps, VerificationStep{Name: fmt.Sprintf("fmt (%s)", cmd), Command: cmd, Optional: true})
	}
	for _, cmd := range v.Lint {
		steps = append(steps, VerificationStep{Name: fmt.Sprintf("lint (%s)", cmd), Command: cmd, Optional: true})
	}
	for _, cmd := range v.Vet {
		steps = append(steps, VerificationStep{Name: fmt.Sprintf("vet (%s)", cmd), Command: cmd, Optional: false})
	}
	for _, cmd := range v.Build {
		steps = append(steps, VerificationStep{Name: fmt.Sprintf("build (%s)", cmd), Command: cmd, Optional: false})
	}
	for _, cmd := range v.Test {
		steps = append(steps, VerificationStep{Name: fmt.Sprintf("test (%s)", cmd), Command: cmd, Optional: false})
	}

	// A language with an empty Verification config (e.g. HTML, CSS) has NO
	// verification contract: return nil so the gate reports Skipped instead of
	// running Go commands against a non-Go project (Phase 7 P1).
	return steps
}

func (v *Verifier) SetLanguage(langID language.ID) {
	v.langID = langID
	v.steps = stepsForLanguage(langID)
	v.explicit = false
}

// SetCustomSteps binds an EXPLICIT verification contract. It is the operator's
// authority for this gate and is never overridden by per-target artifact
// identity resolution: an injected contract answers for every target it is
// asked about, by construction.
func (v *Verifier) SetCustomSteps(steps []VerificationStep) {
	v.steps = steps
	v.explicit = true
}

// TargetLanguage reports the language identity the verification gate uses for a
// workspace-relative target.
//
// This is the Artifact Identity rule (Workspace Contract §27): "Verification
// must correspond to the actual artifact or operation. A CSS file must not
// inherit an HTML verification identity merely because the files belong to the
// same task." The verifier is constructed once per workspace from the
// workspace's PRIMARY language, which is a property of the ENCLOSING project,
// not of any individual target. Two mutations of different artifact types in
// one run therefore shared a single contract unless the gate is told which file
// it is verifying.
//
// TargetLanguage resolves the language from the target's OWN observable
// identity — its file extension, resolved through the existing language
// registry. It never consults the enclosing target, the prompt, or any
// benchmark vocabulary. A target whose extension the registry does not know
// carries NO determinable language, so the workspace language is the
// workspace-level fallback and nothing more; it is never a substitute for a
// determinable identity.
func (v *Verifier) TargetLanguage(target string) language.ID {
	if strings.TrimSpace(target) != "" {
		if def, ok := language.Global().FromExtension(filepath.Ext(target)); ok {
			return def.ID
		}
	}
	return v.langID
}

// stepsForTarget resolves the verification contract that belongs to THIS
// target. It returns the contract together with the language identity it was
// derived from, and a determinable flag reporting whether the target carried
// its own language identity at all.
//
// The distinction matters: a target with a determinable language whose
// registry definition declares NO verification commands must report NOT
// APPLICABLE. Falling back to the enclosing workspace's contract in that case
// is precisely the identity leak this seam exists to close (a styles.css target
// must not be compiled with the workspace's Go commands).
func (v *Verifier) stepsForTarget(target string) (steps []VerificationStep, langID language.ID, determinate bool) {
	if v.explicit {
		// An explicitly bound contract is the operator's authority. Per-target
		// identity resolution refines DERIVED contracts only.
		return v.steps, v.langID, false
	}
	own := v.languageOf(target)
	if own == "" {
		// No language identity on the target itself.
		return nil, v.langID, false
	}
	return stepsForLanguage(own), own, true
}

// languageOf resolves ONLY the target's own extension identity, with no
// workspace fallback. An unknown extension yields the empty ID, which is the
// honest "this target carries no determinable language" answer.
func (v *Verifier) languageOf(target string) language.ID {
	if strings.TrimSpace(target) == "" {
		return ""
	}
	if def, ok := language.Global().FromExtension(filepath.Ext(target)); ok {
		return def.ID
	}
	return ""
}

// RunAllFor runs the verification gate for ONE specific target, resolving the
// verification contract from that target's own artifact identity.
//
// It is the same gate RunAll performs; the target is simply supplied so the
// contract is chosen per artifact instead of per workspace. A target with no
// determinable language falls back to the workspace contract, exactly like
// RunAll.
func (v *Verifier) RunAllFor(ctx context.Context, target string) VerificationReport {
	if v == nil {
		return VerificationReport{Skipped: true, Reason: "no verifier configured"}
	}
	steps, langID, determinate := v.stepsForTarget(target)
	if !determinate {
		// No language identity on the target itself: answer for the enclosing
		// workspace, because that is the only identity available.
		return v.runSteps(ctx, v.steps, v.langID)
	}
	return v.runSteps(ctx, steps, langID)
}

// RunAll runs the verification gate for the verifier's bound language. Prefer
// RunAllFor whenever the target being written is known: RunAll cannot choose a
// per-artifact contract and therefore answers for the ENCLOSING workspace.
func (v *Verifier) RunAll(ctx context.Context) VerificationReport {
	if v == nil {
		return VerificationReport{Skipped: true, Reason: "no verifier configured"}
	}
	return v.runSteps(ctx, v.steps, v.langID)
}

// runSteps executes one resolved verification contract and reports the outcome.
// An empty contract is NOT APPLICABLE — semantically distinct from a pass and
// from a failure: nothing ran, nothing claimed, nothing rolled back (Phase 7
// P1). Go verification is NEVER an implicit fallback.
func (v *Verifier) runSteps(ctx context.Context, steps []VerificationStep, langID language.ID) VerificationReport {
	if len(steps) == 0 {
		// No verification contract exists for this target (unknown language or
		// a language definition with an empty Verification config). Report the
		// gate as NOT APPLICABLE — semantically distinct from a pass and from a
		// failure: nothing ran, nothing claimed, nothing rolled back (Phase 7
		// P1). Go verification is NEVER an implicit fallback.
		return VerificationReport{
			Skipped: true,
			Reason:  "no verification configured for language " + string(langID),
		}
	}

	var report VerificationReport
	report.Passed = true

	for _, step := range steps {
		result := v.runStep(ctx, step)
		// Populate SyntaxErrors for the micro-fix loop.
		if !result.Passed {
			result.SyntaxErrors = ParseSyntaxErrors(result.Output)
		}
		report.Results = append(report.Results, result)

		if !result.Passed && !step.Optional {
			report.Passed = false
		}
	}

	return report
}

func (v *Verifier) runStep(ctx context.Context, step VerificationStep) VerificationResult {
	runner := NewRunner(v.root, false, false)
	if v.auth != nil {
		runner.SetAuthorization(v.auth)
	}

	rawResult, err := runner.Run(ctx, step.Command)

	result := VerificationResult{Step: step}

	if err != nil {
		result.Error = err.Error()
		if rawResult != nil {
			result.Output = rawResult.Stderr
			if result.Output == "" {
				result.Output = rawResult.Stdout
			}
		}
		// A step that returned an error is NEVER a pass. The only shape where
		// Runner.Run yields a non-nil error with ExitCode == 0 is a command
		// that never ran at all: the context was already withdrawn before
		// exec.Start, or the process could not be started. Treating that as a
		// pass would let a cancelled or unstarted verification gate a mutation
		// as verified — verification absence must never become verification
		// success (R6-C).
		result.Passed = false
		result.SyntaxErrors = ParseSyntaxErrors(result.Output)
		return result
	}

	result.Passed = rawResult.ExitCode == 0
	result.Output = rawResult.Stderr
	if result.Output == "" {
		result.Output = rawResult.Stdout
	}
	if rawResult.ExitCode != 0 && result.Output == "" {
		result.Output = fmt.Sprintf("exit code: %d", rawResult.ExitCode)
	}

	if !result.Passed {
		result.SyntaxErrors = ParseSyntaxErrors(result.Output)
	}

	return result
}

func (r VerificationReport) String() string {
	var b strings.Builder
	b.WriteString("=== Verification Report ===\n")
	for _, res := range r.Results {
		status := "PASS"
		if !res.Passed {
			status = "FAIL"
		}
		opt := ""
		if res.Step.Optional {
			opt = " (optional)"
		}
		fmt.Fprintf(&b, "  %s: %s%s\n", res.Step.Name, status, opt)
		if !res.Passed && res.Output != "" {
			for _, line := range strings.Split(res.Output, "\n") {
				if strings.TrimSpace(line) != "" {
					fmt.Fprintf(&b, "    |> %s\n", line)
				}
			}
		}
	}
	overall := "PASSED"
	if !r.Passed {
		overall = "FAILED"
	}
	fmt.Fprintf(&b, "  Overall: %s\n", overall)
	return b.String()
}
