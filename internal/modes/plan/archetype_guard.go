package plan

import (
	"strings"

	"github.com/PizenLabs/izen/internal/discovery/recon"
)

// commandFamily identifies the primary language/toolchain implied by a shell
// command. The broader compatibility check below also scans wrapper payloads,
// so arguments and user prose cannot smuggle a second command family past the
// investigation guard.
func commandFamily(command string) string {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) == 0 {
		return ""
	}
	first := strings.ToLower(strings.TrimPrefix(fields[0], "./"))
	return first
}

// commandMentionsToolchain conservatively detects a toolchain executable
// anywhere in a command, including shell-wrapper payloads such as
// `sh -c "go mod tidy"`. The first-token check alone is not a boundary: a
// wrapper can otherwise smuggle an incompatible command past the archetype
// guard. Arguments that merely mention a tool name are rejected too, which
// is the safe choice for an execution task whose command will run verbatim.
func commandMentionsToolchain(command string, executables ...string) bool {
	fields := strings.Fields(strings.ToLower(command))
	for _, field := range fields {
		field = strings.Trim(field, "\"'`();|&<>[]{},$")
		field = strings.TrimPrefix(field, "./")
		if slash := strings.LastIndexByte(field, '/'); slash >= 0 {
			field = field[slash+1:]
		}
		for _, executable := range executables {
			if field == executable {
				return true
			}
		}
	}
	return false
}

// ArchetypeAllowsCommand reports whether a command belongs to the detected
// investigation workspace. Generic inspection commands (git, echo, grep, ...)
// remain available; language toolchains are admitted only to their matching
// archetype. The zero/unknown archetype preserves the historical permissive
// behavior for callers that have no workspace evidence.
func ArchetypeAllowsCommand(archetype recon.ProjectArchetype, command string) bool {
	family := commandFamily(command)
	if family == "" {
		return false
	}
	switch archetype {
	case recon.VANILLA_WEB:
		lowerCommand := strings.ToLower(strings.TrimSpace(command))
		if lowerCommand == "go.mod" || lowerCommand == "go.sum" {
			return false
		}
		if commandMentionsToolchain(command, "go", "gofmt", "cargo", "rustc", "pip", "pip3", "python", "python3", "make", "mvn", "gradle", "dotnet", "java", "php", "composer", "bundle", "gem", "npm", "npx", "yarn", "pnpm", "go.mod", "go.sum") {
			return false
		}
		switch family {
		case "go", "gofmt", "cargo", "rustc", "pip", "pip3", "python", "python3", "make", "mvn", "gradle", "dotnet", "java", "php", "composer", "bundle", "gem", "npm", "npx", "yarn", "pnpm":
			return false
		default:
			return true
		}
	case recon.REACT_NEXT:
		if commandMentionsToolchain(command, "go", "gofmt", "cargo", "rustc", "pip", "pip3", "python", "python3", "make", "mvn", "gradle", "dotnet", "java", "php", "composer", "bundle", "gem", "go.mod", "go.sum") {
			return false
		}
		switch family {
		case "go", "gofmt", "cargo", "rustc", "pip", "pip3", "python", "python3", "make", "mvn", "gradle", "dotnet", "java", "php", "composer", "bundle", "gem":
			return false
		default:
			return true
		}
	case recon.GO_BACKEND:
		if commandMentionsToolchain(command, "cargo", "rustc", "pip", "pip3", "python", "python3", "mvn", "gradle", "dotnet", "java", "php", "composer", "bundle", "gem") {
			return false
		}
		switch family {
		case "cargo", "rustc", "pip", "pip3", "python", "python3", "mvn", "gradle", "dotnet", "java", "php", "composer", "bundle", "gem":
			return false
		default:
			return true
		}
	default:
		return true
	}
}

func isFrontendMutation(task Task) bool {
	switch task.Type {
	case "FILE_MUTATE", "FILE_EDIT", "CODE_MOD":
		return true
	default:
		return false
	}
}

func isVanillaWebFile(path string) bool {
	lower := strings.ToLower(strings.TrimSpace(path))
	return strings.HasSuffix(lower, ".html") ||
		strings.HasSuffix(lower, ".css") ||
		strings.HasSuffix(lower, ".js")
}

// FilterTasksForArchetype removes language-specific tasks that cannot belong
// to the investigation archetype. It is a hard boundary, including for
// hardcoded tasks: a deterministic fallback must not bypass evidence
// compatibility.
func FilterTasksForArchetype(tasks []Task, archetype recon.ProjectArchetype) []Task {
	if len(tasks) == 0 || archetype == recon.UNKNOWN_GENERIC || archetype == "" {
		return tasks
	}
	clean := make([]Task, 0, len(tasks))
	for _, task := range tasks {
		if task.Type == "ENV_DEPS" && archetype == recon.VANILLA_WEB {
			continue
		}
		if archetype == recon.VANILLA_WEB && isFrontendMutation(task) &&
			!isVanillaWebFile(task.Target) {
			continue
		}
		if (task.Type == "SHELL_EXEC" || task.Type == "GIT_ACTION") && !ArchetypeAllowsCommand(archetype, task.Target) {
			continue
		}
		clean = append(clean, task)
	}
	return clean
}

func (e *Engine) allowsGoFallback() bool {
	return e != nil && !e.vanillaWeb && ArchetypeAllowsCommand(e.archetype, "go mod tidy")
}

// ForceShellExecOnCompileErrorForArchetype is the archetype-aware form of the
// historical anti-escape helper. It may synthesize a dependency command only
// when the investigation archetype owns that toolchain.
func ForceShellExecOnCompileErrorForArchetype(tasks []Task, problem, ledgerContent string, archetype recon.ProjectArchetype) []Task {
	tasks = FilterTasksForArchetype(tasks, archetype)
	if len(tasks) == 0 {
		return tasks
	}
	if !ArchetypeAllowsCommand(archetype, "go mod tidy") {
		return tasks
	}
	return ForceShellExecOnCompileError(tasks, problem, ledgerContent)
}

// ValidateShellExecCommandsForArchetype validates command syntax without ever
// replacing an incompatible command with a Go fallback. The legacy exported
// function remains unchanged for callers that intentionally have no
// archetype context.
func ValidateShellExecCommandsForArchetype(tasks []Task, ledgerContent string, archetype recon.ProjectArchetype) []Task {
	tasks = FilterTasksForArchetype(tasks, archetype)
	if len(tasks) == 0 {
		return tasks
	}
	for _, task := range tasks {
		if task.Type != "SHELL_EXEC" || task.IsHardcoded {
			continue
		}
		if !isValidShellCommand(task.Target) {
			// A language-compatible archetype may use the established
			// dependency conclusion fallback.
			if ArchetypeAllowsCommand(archetype, "go mod tidy") {
				return ValidateShellExecCommands(tasks, ledgerContent)
			}
			return tasks
		}
	}
	return tasks
}

// archetypeFallbackTarget chooses a target that is meaningful for a fallback
// task instead of defaulting every failed plan to main.go.
func archetypeFallbackTarget(archetype recon.ProjectArchetype, allowed []string, problem, ledger string) string {
	if archetype == recon.VANILLA_WEB {
		for _, file := range allowed {
			lower := strings.ToLower(file)
			if strings.HasSuffix(lower, ".html") || strings.HasSuffix(lower, ".css") || strings.HasSuffix(lower, ".js") {
				return file
			}
		}
		return "index.html"
	}
	if archetype == recon.REACT_NEXT {
		for _, file := range allowed {
			lower := strings.ToLower(file)
			if strings.HasSuffix(lower, ".html") || strings.HasSuffix(lower, ".css") ||
				strings.HasSuffix(lower, ".js") || strings.HasSuffix(lower, ".jsx") ||
				strings.HasSuffix(lower, ".ts") || strings.HasSuffix(lower, ".tsx") {
				return file
			}
		}
		return "index.html"
	}
	if target := detectDirectMutation(problem, ledger); target != nil && target.Target != "" {
		return target.Target
	}
	if raw := extractMutationTarget(strings.ToLower(problem + " " + ledger)); raw != "" {
		return raw
	}
	return "main.go"
}
