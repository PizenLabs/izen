package prompt

import (
	"fmt"
	"runtime"
)

// EnvironmentContext returns a compact, authoritative statement of the host
// runtime environment (using the actual runtime.GOOS/GOARCH). Injecting this
// into the /plan (and /build) prompts anchors the model to the ACTUAL
// operating system so it does not hallucinate platform-specific commands for
// the wrong OS (e.g. `apt-get`/`sudo` on a macOS host where `brew`/`go install`
// are correct).
func EnvironmentContext() string {
	return EnvironmentContextForOS(runtime.GOOS)
}

// EnvironmentContextForOS is the OS-parameterised variant used by the central
// prompt composer (registry.Compose), which receives the host OS from the
// runtime and threads it into every mode's system prompt.
func EnvironmentContextForOS(os string) string {
	arch := runtime.GOARCH
	manager := osPackageManager(os)
	return fmt.Sprintf("HOST ENVIRONMENT CONSTRAINT — you are executing on %s/%s. "+
		"Generate commands ONLY for this OS. Preferred package/tooling command for this OS: %s. "+
		"NEVER emit commands for another OS (e.g. do not use `apt-get`/`apt`/`yum`/`dnf` on %s).",
		os, arch, manager, os)
}

// osPackageManager maps a host OS to its correct package/dependency tooling,
// so the plan engine proposes the right command for the actual environment.
func osPackageManager(os string) string {
	switch os {
	case "darwin":
		return "Homebrew (`brew`) — and Go modules via `go get`/`go mod tidy`"
	case "linux":
		return "the distro package manager (`apt`/`apt-get`, `dnf`, or `yum`) or `go install`"
	case "windows":
		return "Windows package managers (`winget`, `choco`) or `go install`"
	default:
		return "`go install` / the platform-native package manager"
	}
}

// PlanContract defines the behavioral contract for /plan mode.
// Phase 2 (Lightweight Execution Mapper): /plan is a deterministic transformer.
// It does NOT re-analyze root cause. It reads the compact Forensic Ledger JSON
// from /investigate and maps it directly to structural atomic_tasks and the
// architectural_strategy. No conversational filler, no re-investigation.
func PlanContract() string {
	return `MODE: /plan — Deterministic Execution Mapper

ROLE
- You are a deterministic transformer, not an analyst.
- Read the pre-compiled Forensic Ledger from /investigate.
- Map the root_cause and targets DIRECTLY to atomic_tasks.
- Do NOT re-analyze, re-investigate, or question the ledger.

PROTOCOL
1. Read the Forensic Ledger below (compact JSON from /investigate).
2. Map root_cause → Task 1 (always the dependency/code fix).
3. Map targets → FILE_MUTATE tasks at exact {file, line} coordinates.
4. End with a verification task when applicable.
5. Output ONLY the JSON schema — zero explanation, zero commentary.

SINGLE-TASK MANDATE (7B TRUNCATION PREVENTION)
If the root_cause is a missing Go package (e.g. "no required module provides package"),
emit EXACTLY ONE task: SHELL_EXEC with go get <exact_package_path>.
No FILE_MUTATE, no GIT_ACTION, no brew/docker/environment tasks.
Total JSON MUST stay under 300 tokens.

ANTI-HALLUCINATION (LOCAL 7B MODELS)
- If the ledger says "missing module X", Task 1 IS "go get X".
- Do NOT add brew install go, brew install docker, or any OS-level setup.
- Never propose installing Go, Docker, or compilers — they already run.
- Keep tasks strictly at the code/dependency boundary.

RULES
- Tasks MUST be atomic, independently verifiable, ordered by dependency.
- Missing dependency → Task 1 MUST be SHELL_EXEC with the exact install command.
- FILE_MUTATE tasks MUST target the exact relative file path and line.
- End with a verification task when supported by the evidence.
- Tool constraint: use native Go tooling FIRST (` + "`go get`" + `, ` + "`go mod tidy`" + `, ` + "`go install`" + `).
  Never default to system-level binaries (` + "`brew install`" + `, ` + "`docker`" + `).` +
		"\n"
}

// BuildPlanJSONPrompt builds the strict JSON prompt consumed by the TUI parser.
// Phase 2: Lightweight — reads the compact ledger, maps to tasks, no re-analysis.
func BuildPlanJSONPrompt(problem, ledgerContent, conclusion string) string {
	conclusionBlock := ""
	if conclusion != "" {
		conclusionBlock = fmt.Sprintf(`
CONCLUSION FROM LEDGER (authoritative — do not override)
%s

CRITICAL: Map this conclusion directly to a SHELL_EXEC task if dependency-related.`, conclusion)
	}

	return fmt.Sprintf(`You are the IZEN Plan Mapper. Read the /investigate Forensic Ledger below and produce a JSON plan.

HOST: %s

INPUT:
PROBLEM: %s
FORENSIC LEDGER:
%s%s

DIRECTIVES:
- Map root_cause → Task 1 (SHELL_EXEC for dep issues, FILE_MUTATE for code bugs).
- If root_cause is a missing Go module, emit EXACTLY: {"task_id":1,"strategy":"SHELL_EXEC","target":"go get <pkg>","description":"install missing dependency"}.
- Do NOT add brew, docker, or environment setup tasks.
- Total JSON under 300 tokens.

OUTPUT — raw JSON only, no fences, no comments:
{
  "context_anchor": {"source": "investigate-ledger", "target_packages": ["pkg"]},
  "architectural_strategy": "single sentence",
  "atomic_tasks": [
    {"task_id": 1, "file": "relative/path", "strategy": "SHELL_EXEC", "description": "why"}
  ]
}`,
		EnvironmentContext(),
		problem,
		ledgerContent,
		conclusionBlock,
	)
}

// BuildPlanPrompt builds the compact Markdown prompt for user-facing terminal output.
// Phase 2: Stripped down — the LLM returns data, UI handles rendering.
func BuildPlanPrompt(objective, contextStr string) string {
	return fmt.Sprintf(`%s

%s

USER OBJECTIVE
%s

OUTPUT — raw task blocks only, no prose:
- [ ] SHELL_EXEC: <exact_command> | <rationale>
- [ ] FILE_MUTATE: <relative_path> | <description>
- [ ] SHELL_EXEC: <verification> | verify

RULES:
- If a missing Go dependency is the root cause, output EXACTLY ONE SHELL_EXEC task.
- No brew, docker, or OS-level environment tasks.
- Keep the plan strictly at the code/dependency boundary.`,
		contextStr,
		EnvironmentContext(),
		objective,
	)
}
