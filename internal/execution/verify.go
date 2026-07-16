package execution

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

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
}

// hallucinatedRe matches stray markdown artifact patterns that local models
// hallucinate as standalone lines within code blocks.
var hallucinatedRe = regexp.MustCompile(`(?i)^\s*\[/?(code|file|source|block|end|diff)\]\s*$`)

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
func SanitizeLLMResponse(raw string) string {
	if raw == "" {
		return raw
	}
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
}

type Verifier struct {
	root   string
	steps  []VerificationStep
	langID language.ID
}

var defaultVerificationSteps = []VerificationStep{
	{Name: "go fmt", Command: "go fmt ./...", Optional: false},
	{Name: "go vet", Command: "go vet ./...", Optional: false},
	{Name: "go test", Command: "go test ./...", Optional: false},
	{Name: "golangci-lint", Command: "golangci-lint run ./...", Optional: true},
	{Name: "govulncheck", Command: "govulncheck ./...", Optional: true},
}

func NewVerifier(root string) *Verifier {
	return &Verifier{
		root:  root,
		steps: make([]VerificationStep, len(defaultVerificationSteps)),
	}
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
func (v *Verifier) RunSyntaxQuickCheck() VerificationReport {
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
		result := v.runStep(step)
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
		result := make([]VerificationStep, len(defaultVerificationSteps))
		copy(result, defaultVerificationSteps)
		return result
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

	if len(steps) == 0 {
		result := make([]VerificationStep, len(defaultVerificationSteps))
		copy(result, defaultVerificationSteps)
		return result
	}

	return steps
}

func (v *Verifier) SetLanguage(langID language.ID) {
	v.langID = langID
	v.steps = stepsForLanguage(langID)
}

func (v *Verifier) SetCustomSteps(steps []VerificationStep) {
	v.steps = steps
}

func (v *Verifier) RunAll() VerificationReport {
	if len(v.steps) == 0 {
		v.steps = stepsForLanguage(v.langID)
	}

	var report VerificationReport
	report.Passed = true

	for _, step := range v.steps {
		result := v.runStep(step)
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

func (v *Verifier) runStep(step VerificationStep) VerificationResult {
	runner := NewRunner(v.root, false, false)

	rawResult, err := runner.Run(step.Command)

	result := VerificationResult{Step: step}

	if err != nil {
		result.Error = err.Error()
		if rawResult != nil {
			result.Output = rawResult.Stderr
			if result.Output == "" {
				result.Output = rawResult.Stdout
			}
		}
		if rawResult != nil && rawResult.ExitCode == 0 {
			result.Passed = true
		}
		if !result.Passed {
			result.SyntaxErrors = ParseSyntaxErrors(result.Output)
		}
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
