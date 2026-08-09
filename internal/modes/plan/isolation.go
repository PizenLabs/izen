package plan

import "strings"

// Frontend Domain Isolation (Module D)
//
// A FRONTEND_UI intent (HTML/CSS/JS edits) or a VANILLA_WEB archetype MUST
// NEVER stage Go dependency tasks. The Go dependency graph (go get, go mod
// tidy, go test, ...) is a hard domain boundary: staging such tasks for a
// pure-frontend workspace is a category error that would run Go toolchain
// commands against a project with no Go module.

// EnforceFrontendDomainIsolation is the hard post-filter over raw LLM task
// output (or deterministic Go dependency heuristics) for frontend workspaces.
// It strips every task that would mutate the Go dependency graph:
//
//   - ENV_DEPS types
//   - SHELL_EXEC / GIT_ACTION targets whose first token is the Go toolchain
//     (go get, go mod tidy, go test, go build, go run, go vet, ...)
//
// All other tasks (file mutations on static assets, non-Go shell commands)
// pass through untouched.
func EnforceFrontendDomainIsolation(tasks []Task) []Task {
	if len(tasks) == 0 {
		return tasks
	}
	clean := make([]Task, 0, len(tasks))
	for _, t := range tasks {
		if isGoDependencyTask(t) {
			continue
		}
		clean = append(clean, t)
	}
	return clean
}

// isGoDependencyTask reports whether a task mutates the Go dependency graph.
func isGoDependencyTask(t Task) bool {
	if t.Type == "ENV_DEPS" {
		return true
	}
	if t.Type != "SHELL_EXEC" && t.Type != "GIT_ACTION" {
		return false
	}
	fields := strings.Fields(strings.TrimSpace(t.Target))
	if len(fields) == 0 {
		return false
	}
	return fields[0] == "go"
}
