package inference

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/PizenLabs/izen/pkg/engine/context"
)

// WorkspaceFacts is the raw, uninterpreted surface of a workspace the
// inference engine reasons over. It is assembled before any hypothesis is
// formed and never mutated by the engine.
type WorkspaceFacts struct {
	// Root is the absolute workspace root.
	Root string
	// Files lists the workspace files as slash-separated relative paths.
	Files []string
	// Directories lists the top-level directories.
	Directories []string
	// Configs lists recognized framework config file paths.
	Configs []string
	// Dependencies maps dependency name → declared version (package.json and
	// go.mod).
	Dependencies map[string]string
	// GoDeps lists module paths declared in go.mod.
	GoDeps []string
}

// NewWorkspaceFacts returns an empty facts struct rooted at root.
func NewWorkspaceFacts(root string) WorkspaceFacts {
	return WorkspaceFacts{
		Root:         root,
		Dependencies: map[string]string{},
	}
}

// PromptSlots carries the user prompt signals the inference engine
// aggregates alongside WorkspaceFacts.
type PromptSlots struct {
	// Raw is the raw user prompt.
	Raw string
	// Features lists free-form features mentioned by the user (pages,
	// endpoints, migrations).
	Features []string
}

// HasKeyword reports whether the raw prompt mentions the keyword
// (case-insensitive substring match).
func (s PromptSlots) HasKeyword(keyword string) bool {
	return strings.Contains(strings.ToLower(s.Raw), strings.ToLower(keyword))
}

// WorkspaceInspector collects WorkspaceFacts from a workspace root. It is the
// deterministic first stage of the IR-driven intent compiler: it never
// assumes the workspace state and degrades to an empty surface on a missing
// root.
type WorkspaceInspector struct {
	root string
}

// NewWorkspaceInspector returns an inspector bound to the workspace root.
func NewWorkspaceInspector(root string) *WorkspaceInspector {
	return &WorkspaceInspector{root: root}
}

// Root returns the workspace root the inspector is bound to.
func (w *WorkspaceInspector) Root() string { return w.root }

// Inspect assembles the WorkspaceFacts of the bound workspace root.
func (w *WorkspaceInspector) Inspect() WorkspaceFacts {
	return AnalyzeWorkspace(w.root)
}

// analyzeRootWalk caps how many entries the workspace scanner visits, so
// `/explain-decision` stays fast on large repositories.
const analyzeRootWalk = 4096

// AnalyzeWorkspace scans a workspace directory and assembles WorkspaceFacts
// from the filesystem: the file surface, recognized config files, top-level
// directories and dependency manifests (package.json + go.mod). It never
// fails on a missing or unreadable root — it returns the empty facts in that
// case.
func AnalyzeWorkspace(root string) WorkspaceFacts {
	facts := NewWorkspaceFacts(root)
	if root == "" {
		return facts
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return facts
	}

	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > 4 || len(facts.Files) >= analyzeRootWalk {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			full := filepath.Join(dir, name)
			rel, rerr := filepath.Rel(root, full)
			if rerr != nil {
				rel = full
			}
			rel = filepath.ToSlash(rel)
			if e.IsDir() {
				if depth == 0 {
					facts.Directories = append(facts.Directories, name)
				}
				walk(full, depth+1)
				continue
			}
			facts.Files = append(facts.Files, rel)
			base := filepath.Base(name)
			switch base {
			case "package.json":
				facts.Dependencies = mergeDeps(facts.Dependencies, readPackageDeps(full))
			case "go.mod":
				facts.GoDeps = append(facts.GoDeps, readGoModDeps(full)...)
				for _, m := range facts.GoDeps {
					if !strings.Contains(m, "/") {
						continue
					}
					parts := strings.Split(m, "/")
					mod := strings.Join(parts[:min(3, len(parts))], "/")
					facts.Dependencies["go:"+mod] = "go.mod"
				}
			}
			if isConfigFile(base) {
				facts.Configs = append(facts.Configs, rel)
			}
		}
	}
	walk(root, 0)

	sort.Strings(facts.Files)
	sort.Strings(facts.Directories)
	sort.Strings(facts.Configs)
	sort.Strings(facts.GoDeps)
	return facts
}

// FromPlanningContext assembles WorkspaceFacts from an assembled microkernel
// PlanningContext. The filesystem chunk's newline-separated relative paths
// become Files; the prompt chunk becomes the raw prompt slot. A missing
// chunk degrades to an empty surface.
func FromPlanningContext(pc context.PlanningContext) WorkspaceFacts {
	facts := NewWorkspaceFacts("")
	if chunk, ok := pc.Get(context.ProviderFilesystem); ok {
		for _, line := range strings.Split(chunk.Content, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			line = filepath.ToSlash(line)
			facts.Files = append(facts.Files, line)
			base := filepath.Base(line)
			if isConfigFile(base) {
				facts.Configs = append(facts.Configs, line)
			}
			parts := strings.Split(line, "/")
			if len(parts) == 2 {
				facts.Directories = append(facts.Directories, parts[0])
			}
		}
	}
	if chunk, ok := pc.Get(context.ProviderPrompt); ok {
		_ = chunk // the prompt is consumed by the caller via slots
	}
	return facts
}

// PromptFromPlanningContext extracts the raw prompt from a PlanningContext.
func PromptFromPlanningContext(pc context.PlanningContext) string {
	if chunk, ok := pc.Get(context.ProviderPrompt); ok {
		return chunk.Content
	}
	return ""
}

// isConfigFile reports whether a base file name is a recognized framework
// config signal.
func isConfigFile(base string) bool {
	for _, c := range knownConfigs {
		if base == c {
			return true
		}
	}
	return false
}

// knownConfigs lists every config file the framework detectors look for.
var knownConfigs = []string{
	"next.config.js", "next.config.mjs", "next.config.ts",
	"astro.config.js", "astro.config.mjs", "astro.config.ts",
	"vite.config.js", "vite.config.ts",
	"tailwind.config.js", "tailwind.config.ts", "tailwind.config.mjs",
	"tsconfig.json", "jsconfig.json",
}

// readPackageDeps parses the dependencies, devDependencies and
// peerDependencies objects of a package.json into a name → version map.
func readPackageDeps(path string) map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, section := range []string{"dependencies", "devDependencies", "peerDependencies"} {
		block := extractJSONObject(string(data), section)
		if block == "" {
			continue
		}
		for _, m := range depEntryRe.FindAllStringSubmatch(block, -1) {
			if m[1] == "" {
				continue
			}
			out[m[1]] = m[2]
		}
	}
	return out
}

// depEntryRe matches `"name": "version"` entries inside an extracted JSON
// object body.
var depEntryRe = regexp.MustCompile(`"([^"]+)"\s*:\s*"([^"]*)"`)

// readGoModDeps parses the require block of a go.mod into module paths.
func readGoModDeps(path string) []string {
	var out []string
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	inRequire := false
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "require") {
			inRequire = true
			line = strings.TrimSpace(strings.TrimPrefix(line, "require"))
			if line == "(" {
				continue
			}
		} else if line == ")" {
			inRequire = false
			continue
		}
		if !inRequire {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 1 && strings.Contains(fields[0], ".") {
			out = append(out, fields[0])
		}
	}
	return out
}

// extractJSONObject returns the raw body of a named top-level JSON object
// without a full JSON parser, tolerating the common package.json shape.
func extractJSONObject(data, key string) string {
	idx := strings.Index(data, `"`+key+`"`)
	if idx < 0 {
		return ""
	}
	open := strings.Index(data[idx:], "{")
	if open < 0 {
		return ""
	}
	start := idx + open
	depth := 0
	for i := start; i < len(data); i++ {
		switch data[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return data[start : i+1]
			}
		}
	}
	return ""
}

// mergeDeps folds a parsed dependency map into an existing one.
func mergeDeps(dst, src map[string]string) map[string]string {
	if dst == nil {
		dst = map[string]string{}
	}
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
