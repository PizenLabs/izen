package prompt

// schema.go consolidates every JSON/task-block schema definition used across
// the prompt layer so output contracts are defined in exactly one place and
// referenced (never re-inlined) by the mode contracts. Structured-output
// contracts are functional invariants — they are deliberately NOT subject to
// the verbosity StylePolicy, which only governs prose responses.

// PlanTaskSchema is the canonical output schema for BuildPlanJSONPrompt.
// It is the strict JSON shape the TUI plan parser accepts for full-path
// (non-direct-mutation) plan synthesis.
func PlanTaskSchema() string {
	return `{
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
}

// PlanSynthesisSchema is the compact output schema used by the model-agnostic
// PlanSynthesisSystemPrompt. It is a flattened atomic_tasks shape that small
// (Mini/7B-class) models can follow without the full context_anchor overhead.
func PlanSynthesisSchema() string {
	return `{
  "architectural_strategy": "one sentence",
  "atomic_tasks": [
    {"task_id": 1, "strategy": "SHELL_EXEC", "file": "exact command or relative path", "description": "title", "rationale": "why this step", "solution": "expected end state"}
  ]
}`
}

// PlanContractSchema is the task array shape consumed by the high-complexity
// PlanContract (JSON task plan with SHELL_EXEC|FILE_MUTATE strategies).
func PlanContractSchema() string {
	return `{
  "tasks": [
    {"id": 1, "type": "SHELL_EXEC|FILE_MUTATE", "target": "<exact file path or command>", "description": "<what and why>", "rationale": "<why this step>"}
  ]
}`
}

// DispatchSchema is the single-diagnostic-tool routing schema emitted by the
// /investigate AI dispatcher (Strategy JSON).
func DispatchSchema() string {
	return `{"tool": "<env|trace|diagnose|lx>", "target": "<symbol/file/test name or empty>"}`
}

// TaskBlockSchemaTemplate is the concrete example block for the
// "- [ ] <TYPE>: <Target> | <Rationale>" output contract.
const TaskBlockSchemaTemplate = `- [ ] FILE_MUTATE: path/to/file.go | describe the change
- [ ] SHELL_EXEC: go build ./... | reason for running this command
- [ ] GIT_ACTION: commit -m "message" | why this commit is needed`

// TaskBlockSchemaInstruction returns the schema definition block for system
// prompts that demand strict task-block output. It is the single source of the
// "- [ ] <TYPE>: <Target> | <Rationale>" contract shared by /plan and /build.
func TaskBlockSchemaInstruction() string {
	return `You MUST output ONLY task blocks. Each line MUST follow this EXACT syntax:

  - [ ] <TYPE>: <Target> | <Rationale>

ALLOWED TYPES (case-sensitive):
  FILE_MUTATE — Target is the exact file path (relative to project root)
  SHELL_EXEC  — Target is the exact shell command to execute
  GIT_ACTION  — Target is the git operation

RULES:
  1. Every line MUST start with "- [ ]" or "- [x]".
  2. No introductory text, no concluding text, no markdown code fences.
  3. Use "|" (pipe) to separate Target from Rationale.
  4. Target paths MUST be relative to the project root.
  5. No speculative paths — only reference files in the directory tree above.

Example:
` + TaskBlockSchemaTemplate
}
