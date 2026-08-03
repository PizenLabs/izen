package prompt

import (
	"fmt"
	"runtime"
	"strings"
)

// EnvironmentContext returns a compact, authoritative host runtime statement
// using the actual runtime.GOOS/GOARCH. Injecting this anchors the model to
// the ACTUAL operating system so it never hallucinate OS-specific commands
// (e.g. `apt-get` on a macOS host where `brew`/`go install` are correct).
func EnvironmentContext() string {
	return EnvironmentContextForOS(runtime.GOOS)
}

// EnvironmentContextForOS is the OS-parameterised variant used by Compose,
// which receives the host OS from the runtime and threads it into every mode.
func EnvironmentContextForOS(os string) string {
	arch := runtime.GOARCH
	manager := osPackageManager(os)
	return fmt.Sprintf(
		"HOST ENVIRONMENT: %s/%s. Generate commands ONLY for this OS. "+
			"Preferred tooling: %s. "+
			"NEVER emit commands for another OS (e.g. no `apt-get`/`apt`/`yum`/`dnf` on %s).",
		os, arch, manager, os,
	)
}

// osPackageManager maps a host OS to its correct package/dependency tooling.
func osPackageManager(os string) string {
	switch os {
	case "darwin":
		return "Go modules via `go get`/`go mod tidy` / platform binary tooling"
	case "linux":
		return "the distro package manager (`apt`/`apt-get`, `dnf`, or `yum`) or `go install`"
	case "windows":
		return "Windows package managers (`winget`, `choco`) or `go install`"
	default:
		return "`go install` / the platform-native package manager"
	}
}

// ComplexityThreshold defines the boundary between compact and verbose plan prose.
// Tasks scoring > HighComplexityThreshold receive the full plan contract.
const (
	LowComplexityThreshold    = 4
	MediumComplexityThreshold = 7
	HighComplexityThreshold   = 8
)

// ComplexityScore rates a planning task from 1 (trivial) to 10 (architectural).
type ComplexityScore int

const (
	ComplexityTrivial       ComplexityScore = 1
	ComplexitySimple        ComplexityScore = 3
	ComplexityModerate      ComplexityScore = 5
	ComplexityComplex       ComplexityScore = 7
	ComplexityArchitectural ComplexityScore = 9
)

// IsHighComplexity returns true when the score exceeds the threshold or the
// user explicitly requested a high-intent analysis via --high or /intent high.
func IsHighComplexity(score ComplexityScore, hasHighFlag bool) bool {
	if hasHighFlag {
		return true
	}
	return score > HighComplexityThreshold
}

// AssessComplexity assigns a heuristic complexity score based on task keywords.
// Scoring:
//
//	1-3: simple config/doc edits (LICENSE, README, formatting)
//	4-6: moderate code changes (add tests, small refactors, single-file edits)
//	7-8: multi-file refactors, API changes
//	9-10: architectural changes, cross-cutting concerns, migrations
func AssessComplexity(objective string) ComplexityScore {
	lower := strings.ToLower(objective)

	highComplexityKeywords := []string{
		"migration", "architect", "redesign", "restructure",
		"cross-cutting", "concurrency", "distributed",
		"database", "schema", "api design", "protocol",
		"security", "authentication", "authorization",
		"pipeline", "event-driven", "message queue",
	}
	lowComplexityKeywords := []string{
		"license", "readme", "typo", "comment", "format",
		"rename", "spelling", "grammar", "whitespace",
		"capitalize", "config", "version bump",
	}

	for _, kw := range highComplexityKeywords {
		if strings.Contains(lower, kw) {
			return ComplexityComplex
		}
	}
	for _, kw := range lowComplexityKeywords {
		if strings.Contains(lower, kw) {
			return ComplexitySimple
		}
	}

	return ComplexityModerate
}

// PlanContract returns the technical plan contract for
// HIGH complexity tasks. Used when IsHighComplexity is true.
func PlanContract() string {
	return fmt.Sprintf(`MODE: /plan — structured task plan.

Read the investigation ledger below and produce a JSON task plan.

Output ONLY raw JSON, no fences, no comments.

SCHEMA:
%s

RULES
- Tasks MUST be atomic and ordered by dependency.
- SHELL_EXEC target MUST be a real runnable command (e.g. "go get github.com/foo/bar"), NOT a file path.
- Missing Go dependency → SHELL_EXEC task with the actual package path.
- FORBIDDEN as SHELL_EXEC target: file paths (go.mod, go.sum, relative paths), prose, placeholders.
- No brew, docker, or OS-level setup tasks.
- Total JSON under 300 tokens.%s`,
		PlanContractSchema(),
		TokenThriftyConstraint,
	)
}

// CompactPlanContract returns a lean 3-bullet checklist contract for
// LOW and MEDIUM complexity tasks. Omits role and verbose analysis.
func CompactPlanContract() string {
	return `MODE: /plan — compact task checklist.

Map the objective to minimal tasks. Output ONLY raw task blocks.

OUTPUT FORMAT:
- [ ] SHELL_EXEC: <command> | <rationale>
- [ ] FILE_MUTATE: <relative_path> | <description>
- [ ] SHELL_EXEC: <verification> | verify

RULES
- Missing Go dependency → SHELL_EXEC: go get <real_package> | install missing dependency.
- FORBIDDEN: "go.mod", "go.sum", relative paths as shell target.
- No brew, docker, or OS-level tasks. Stay at the code/dependency boundary.` + TokenThriftyConstraint
}

// SelectPlanContract returns the appropriate plan contract based on complexity.
// High-complexity tasks or explicit high-intent requests get the full
// plan contract; everything else gets the compact checklist.
func SelectPlanContract(objective string, complexity ComplexityScore, hasHighFlag bool) string {
	if IsHighComplexity(complexity, hasHighFlag) {
		return PlanContract()
	}
	return CompactPlanContract()
}

// planDirectives are the shared DIRECTIVES rules for BuildPlanJSONPrompt.
// Extracted to eliminate copy-paste between the isDirectMutation branches.
const planDirectives = `DIRECTIVES
- Map root_cause → Task 1: SHELL_EXEC for dep issues, FILE_MUTATE for code bugs.
- Go missing module → emit EXACTLY ONE task: {"task_id":1,"strategy":"SHELL_EXEC","target":"go get <real_package>","description":"install missing dependency","rationale":"<why>","solution":"<end state>"}. Use the ACTUAL package path — never literal angle brackets.
- FORBIDDEN as SHELL_EXEC target: file paths (go.mod, go.sum, ./relative/path), prose, or any non-runnable text.
- Do NOT add brew, docker, or environment setup tasks.`

// conclusionBlock formats the authoritative ledger conclusion block, or
// returns an empty string when conclusion is empty.
func conclusionBlock(conclusion string) string {
	if conclusion == "" {
		return ""
	}
	return fmt.Sprintf(`
CONCLUSION FROM LEDGER (authoritative — do not override)
%s

CRITICAL: Map this conclusion directly to a SHELL_EXEC task if dependency-related.
The SHELL_EXEC target MUST be a valid command with the ACTUAL package path (e.g. "go get github.com/docker/docker/client"), not a file path or placeholder.`,
		conclusion)
}

// EvidenceBasedPlanningDirective is the strict anti-hallucination instruction
// injected into /plan prompts. It forces the model to ground every
// FILE_MUTATE / ATOMIC_REPLACE / DIFF_PATCH target in actually-inspected file
// content: a plan must NEVER assume a file (script.js, styles.css, etc.) needs
// modification unless an explicit duplicate DOM node, diff, or defect was
// CONFIRMED inside that file's exact content. This prevents generic
// architectural blueprints that modify every asset on speculation.
func EvidenceBasedPlanningDirective() string {
	return `[CRITICAL: EVIDENCE-BASED PLANNING]
- BEFORE listing any FILE_MUTATE / ATOMIC_REPLACE / DIFF_PATCH task, READ the ACTUAL content of the affected file(s) to locate the exact duplicate DOM node or the exact defect. Do not reason from file names alone.
- A file MAY ONLY appear as a task target when an explicit duplicate/diff/defect was CONFIRMED inside that file's content.
- NEVER assume every asset (script.js, styles.css, etc.) needs modification. When a defect is confined to one file (e.g. a duplicated element in index.html), emit EXACTLY ONE FILE_MUTATE task for that file — do NOT add sibling assets as speculative targets.
- If a file's content could not be inspected, do NOT emit a task for it. Restrict tasks to files whose content you have verified.
- DO NOT schedule FILE_MUTATE / CODE_MOD tasks for files that need no concrete modification. A destructive edit that strips >80% of a file without an explicit delete instruction is rejected as a no-op by the build guardrail, so a task with an empty or trivial edit payload wastes a build cycle. If a file genuinely needs no change, exclude it from the plan entirely.`
}

// planJSONSchema is the canonical output schema for BuildPlanJSONPrompt. The
// schema body lives in schema.go (PlanTaskSchema); this reference keeps the
// builder compact and the definition single-sourced.
var planJSONSchema = PlanTaskSchema()

// GroundedConstraint returns the ALLOWED_FILE_TREE constraint block for
// injection into plan prompts. When archetype or allowedFiles is empty,
// returns an empty string (no constraint).
func GroundedConstraint(archetype string, allowedFiles []string) string {
	if archetype == "" && len(allowedFiles) == 0 {
		return ""
	}
	var b strings.Builder
	if archetype != "" {
		fmt.Fprintf(&b, "PROJECT ARCHETYPE: %s\n", archetype)
	}
	b.WriteString("ALLOWED_FILE_TREE:\n")
	if len(allowedFiles) == 0 {
		b.WriteString("  (none — do not create any new files)\n")
	} else {
		for _, f := range allowedFiles {
			fmt.Fprintf(&b, "  - %s\n", f)
		}
	}
	b.WriteString("\nCRITICAL: You MUST ONLY target files present in ALLOWED_FILE_TREE.\n")
	b.WriteString("Do NOT create or invent fictional file paths or frameworks.\n")
	b.WriteString("Do NOT suggest files outside ALLOWED_FILE_TREE under any circumstance.\n")
	return b.String()
}

// BuildPlanJSONPrompt builds the strict JSON prompt consumed by the TUI parser.
// Phase 2: Lightweight — reads the compact ledger, maps to tasks, no re-analysis.
// When isDirectMutation is true, omits EnvironmentContext and constrains output
// to a compact single-file patch.
// groundedPayload is an optional ALLOWED_FILE_TREE constraint block (empty string
// skips injection).
func BuildPlanJSONPrompt(problem, ledgerContent, conclusion string, isDirectMutation bool, groundedPayload string) string {
	cb := conclusionBlock(conclusion)
	gp := groundedPayload
	if gp != "" {
		gp = "\n" + gp
	}

	evidence := EvidenceBasedPlanningDirective()

	if isDirectMutation {
		return fmt.Sprintf(`You are the IZEN Plan Mapper. Read the investigation ledger below and produce a JSON plan.

INPUT:
PROBLEM: %s
LEDGER:
%s%s

%s
%s
- Total JSON under 150 tokens.
%s
OUTPUT — raw JSON only, no fences, no comments:
%s`,
			problem, ledgerContent, cb,
			planDirectives,
			evidence,
			gp,
			planJSONSchema,
		)
	}

	return fmt.Sprintf(`You are the IZEN Plan Mapper. Read the investigation ledger below and produce a JSON plan.

HOST: %s

INPUT:
PROBLEM: %s
LEDGER:
%s%s

%s
%s
- For EVERY task provide rationale (why) and solution (expected end state).
- Include root_core_factor in strategic_overview describing the fundamental root cause.
- Total JSON under 300 tokens.
%s
OUTPUT — raw JSON only, no fences, no comments:
%s`,
		EnvironmentContext(),
		problem, ledgerContent, cb,
		planDirectives,
		evidence,
		gp,
		planJSONSchema,
	)
}

// planTaskBlocks is the shared output format for BuildPlanPrompt.
const planTaskBlocks = `OUTPUT — raw task blocks only, no prose:
- [ ] SHELL_EXEC: <exact_command> | <rationale>
- [ ] FILE_MUTATE: <relative_path> | <description>
- [ ] SHELL_EXEC: <verification> | verify

RULES
- Missing Go dependency → output EXACTLY ONE SHELL_EXEC task with the ACTUAL package path (e.g. "go get github.com/docker/docker/client"). NOT a file path or placeholder.
- FORBIDDEN as SHELL_EXEC target: "go.mod", "go.sum", relative paths, or any non-command text.
- No brew, docker, or OS-level environment tasks. Stay at the code/dependency boundary.`

// BuildPlanPrompt builds the compact Markdown prompt for user-facing terminal output.
// Phase 2: Stripped down — the LLM returns data, UI handles rendering.
// When isDirectMutation is true, constrains output to raw task blocks only.
// groundedPayload is an optional ALLOWED_FILE_TREE constraint block (empty string
// skips injection).
func BuildPlanPrompt(objective, contextStr string, isDirectMutation bool, groundedPayload string) string {
	gp := groundedPayload
	if gp != "" {
		gp = "\n" + gp
	}
	evidence := EvidenceBasedPlanningDirective()

	if isDirectMutation {
		return fmt.Sprintf(`%s%s

USER OBJECTIVE
%s

%s
%s
- Total output under 150 tokens.`,
			contextStr, gp, objective, planTaskBlocks, evidence,
		)
	}

	return fmt.Sprintf(`%s%s

%s

USER OBJECTIVE
%s

%s
%s`,
		contextStr, gp,
		EnvironmentContext(),
		objective,
		planTaskBlocks,
		evidence,
	)
}

// PlanDirectMutationSystemPrompt returns a zero-prose system prompt for
// direct file mutations (e.g. refactor LICENSE, change config). Unlike
// PlanSystemPrompt, it omits all analysis sections and instructs the model
// to output ONLY the direct task item for execution.
func PlanDirectMutationSystemPrompt() string {
	return "STRICT RULE: Direct file mutation detected.\n" +
		"Output ONLY the direct task item for execution.\n" +
		"No preamble, no summary." +
		"\n" + strings.TrimSpace(TokenThriftyConstraint)
}

// PlanSynthesisSystemPrompt returns the compact, model-agnostic system prompt
// used for JSON plan synthesis in /plan. Unlike the composed prompts it
// carries no identity/contract preamble (CommonContract, environment context),
// keeping the instruction block small enough for Mini/7B models to follow
// without choking on context. It enforces the Action (strategy), Target
// (file), Reason (rationale) output contract and explicitly forbids thinking
// blocks and markdown decorations.
func PlanSynthesisSystemPrompt() string {
	return `You are IZEN, a deterministic task planner.
Read the investigation ledger and emit a task plan.
OUTPUT: ONE raw JSON object. No markdown fences, no comments, no prose, no <think>/<thought> blocks, no explanations.

SCHEMA:
` + PlanSynthesisSchema() + `

RULES
- strategy: SHELL_EXEC = runnable command only; FILE_MUTATE = source file edit.
- file: command text for SHELL_EXEC, relative project path for FILE_MUTATE.
- One task per change; keep tasks atomic and ordered.
- Never target documentation files (README.md, docs, LICENSE).
- Missing Go dependency -> SHELL_EXEC "go get <real package>".
- Keep the whole JSON under 300 tokens.`
}

// miniModelNameMarkers are substrings that identify small / non-reasoning
// models (e.g. Cohere North Mini, GPT-4o-mini, Gemini Flash, llama-3.2-1b)
// that tend to emit narrative prose instead of structured JSON. The check is
// deliberately broad because these model families share the same
// instruction-following weakness.
var miniModelNameMarkers = []string{
	"mini",
	"nano",
	"flash",
	"lite",
	"small",
	"tiny",
	"command-r",
	"command r",
	// Small parameter-count suffixes (1B/3B/7B/8B class local SLMs).
	"1b",
	"3b",
	"7b",
	"8b",
}

// IsMiniModel reports whether modelName refers to a small / non-reasoning model
// that benefits from the strict raw-JSON output constraint.
func IsMiniModel(modelName string) bool {
	name := strings.ToLower(strings.TrimSpace(modelName))
	if name == "" {
		return false
	}
	for _, m := range miniModelNameMarkers {
		if strings.Contains(name, m) {
			return true
		}
	}
	return false
}

// MiniModelJSONConstraint returns the strict raw-JSON output directive for
// small / non-reasoning models, or an empty string when modelName does not look
// like one. Callers append the non-empty result to the plan system prompt so a
// mini model is never tempted to wrap its JSON plan in explanations, preamble,
// or markdown formatting.
func MiniModelJSONConstraint(modelName string) string {
	if !IsMiniModel(modelName) {
		return ""
	}
	return "CRITICAL: Respond ONLY with raw JSON array/object. Do NOT include explanations, preamble, or markdown formatting outside the JSON."
}
