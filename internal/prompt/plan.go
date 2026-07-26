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
// Tasks scoring > HighComplexityThreshold receive the full Senior Architect treatment.
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

// PlanContract returns the verbose Senior Principal Structural Architect contract.
// Used ONLY when the task complexity exceeds HighComplexityThreshold or the user
// explicitly opted into high-intent analysis.
func PlanContract() string {
	return `MODE: /plan — Structural Architecture Synthesis

ROLE: Senior Principal Structural Architect.
- Read the pre-compiled Forensic Ledger from /investigate.
- Synthesize a structured plan: Root Core Factor, Impact Domain, Risk Evaluation, Verification Vector.
- Every task MUST include: track classification, rationale (why), expected solution (end state).
- Do NOT re-analyze, re-investigate, or question the ledger.

PROTOCOL
1. Read the Forensic Ledger (compact JSON from /investigate).
2. Identify the Root Core Factor — one sentence stating the fundamental root cause.
3. Map root_cause → Task 1 (always the dependency/code fix).
4. Map targets → FILE_MUTATE tasks at exact {file, line} coordinates.
5. End with a verification task when applicable.
6. For EVERY task provide: rationale (why this task is necessary) and solution (expected end state).
7. Output ONLY the JSON schema — zero explanation, zero commentary.

GO DEPENDENCY RULE (STRICT)
For missing Go package/module errors ("no required module provides package"):
- Extract the EXACT package path from the ledger conclusion.
- Emit EXACTLY ONE SHELL_EXEC task: "go get <real_package_path>".
- Total JSON MUST stay under 300 tokens.
- FORBIDDEN in command string: file names (go.mod, go.sum), relative paths, prose, brew/docker/apt, angle-bracket placeholders.

ANTI-HALLUCINATION
- Missing module X → Task 1 IS "go get X". No brew, no docker, no OS-level setup.
- Never propose installing Go, Docker, or compilers — they already run.
- SHELL_EXEC target MUST be a real runnable command (e.g. "go get github.com/foo/bar"), NOT a file path or placeholder.

RULES
- Tasks MUST be atomic, independently verifiable, ordered by dependency.
- Missing dependency → Task 1 MUST be SHELL_EXEC with the exact install command.
- FILE_MUTATE tasks MUST target the exact relative file path and line.
- Use native Go tooling first (` + "`go get`" + `, ` + "`go mod tidy`" + `, ` + "`go install`" + `). Never default to ` + "`brew install`" + ` or ` + "`docker`" + `.`
}

// CompactPlanContract returns a stripped-down 3-bullet checklist contract for
// LOW and MEDIUM complexity tasks. Omits ROLE, protocol details, and verbose
// analysis instructions. Used when IsHighComplexity is false.
func CompactPlanContract() string {
	return `MODE: /plan — Quick Task Checklist

ROLE: Execution Mapper. Map the objective to a compact task list.

PROTOCOL
- Read the objective. Identify the file(s) to modify.
- Output a 3-bullet checklist: (1) prep/setup, (2) the change itself, (3) verification.
- Every SHELL_EXEC must be a real runnable command.
- Output ONLY raw task blocks. No preamble, no analysis, no commentary.

OUTPUT FORMAT:
- [ ] SHELL_EXEC: <command> | <rationale>
- [ ] FILE_MUTATE: <relative_path> | <description>
- [ ] SHELL_EXEC: <verification> | verify

RULES
- Missing Go dependency → SHELL_EXEC: go get <real_package> | install missing dependency.
- FORBIDDEN: "go.mod", "go.sum", relative paths as shell target.
- No brew, docker, or OS-level tasks. Stay at the code/dependency boundary.`
}

// SelectPlanContract returns the appropriate plan contract based on complexity.
// High-complexity tasks or explicit high-intent requests get the full Senior
// Architect prose; everything else gets the compact 3-bullet checklist.
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

// planJSONSchema is the canonical output schema for BuildPlanJSONPrompt.
const planJSONSchema = `{
  "context_anchor": {"source": "investigate-ledger", "target_packages": ["pkg"]},
  "architectural_strategy": "single sentence",
  "strategic_overview": {
    "root_core_factor": "fundamental root cause",
    "impact_domain": "architectural layer affected",
    "risk_evaluation": "Low | Medium | High | Critical",
    "verification_vector": "how correctness will be verified"
  },
  "atomic_tasks": [
    {"task_id": 1, "file": "relative/path", "strategy": "SHELL_EXEC", "description": "title", "rationale": "why this task is needed", "solution": "expected end state"}
  ]
}`

// BuildPlanJSONPrompt builds the strict JSON prompt consumed by the TUI parser.
// Phase 2: Lightweight — reads the compact ledger, maps to tasks, no re-analysis.
// When isDirectMutation is true, omits EnvironmentContext and constrains output
// to max_tokens: 150.
func BuildPlanJSONPrompt(problem, ledgerContent, conclusion string, isDirectMutation bool) string {
	cb := conclusionBlock(conclusion)

	if isDirectMutation {
		return fmt.Sprintf(`You are the IZEN Plan Mapper. Read the /investigate Forensic Ledger below and produce a JSON plan.

INPUT:
PROBLEM: %s
FORENSIC LEDGER:
%s%s

%s
- Total JSON under 150 tokens.

OUTPUT — raw JSON only, no fences, no comments:
%s`,
			problem, ledgerContent, cb,
			planDirectives,
			planJSONSchema,
		)
	}

	return fmt.Sprintf(`You are the IZEN Plan Mapper. Read the /investigate Forensic Ledger below and produce a JSON plan.

HOST: %s

INPUT:
PROBLEM: %s
FORENSIC LEDGER:
%s%s

%s
- For EVERY task provide rationale (why) and solution (expected end state).
- Include root_core_factor in strategic_overview describing the fundamental root cause.
- Total JSON under 300 tokens.

OUTPUT — raw JSON only, no fences, no comments:
%s`,
		EnvironmentContext(),
		problem, ledgerContent, cb,
		planDirectives,
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
func BuildPlanPrompt(objective, contextStr string, isDirectMutation bool) string {
	if isDirectMutation {
		return fmt.Sprintf(`%s

USER OBJECTIVE
%s

%s
- Total output under 150 tokens.`,
			contextStr, objective, planTaskBlocks,
		)
	}

	return fmt.Sprintf(`%s

%s

USER OBJECTIVE
%s

%s`,
		contextStr,
		EnvironmentContext(),
		objective,
		planTaskBlocks,
	)
}

// PlanDirectMutationSystemPrompt returns a zero-prose system prompt for
// direct file mutations (e.g. refactor LICENSE, change config). Unlike
// PlanSystemPrompt, it omits all analysis sections and instructs the model
// to output ONLY the direct task item for execution.
func PlanDirectMutationSystemPrompt() string {
	return "STRICT RULE: Direct file mutation detected.\n" +
		"Output ONLY the direct task item for execution.\n" +
		"No CONTEXT & ROLE, no FORENSIC HANDOFF, no preamble, no summary."
}
