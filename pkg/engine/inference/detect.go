package inference

import (
	"path/filepath"
	"sort"
	"strings"
)

// Evidence weights. These constants are part of the public evidence contract:
// the inspector and the tests rely on config +0.30 and dependency +0.60 for
// the canonical Next.js workspace.
const (
	// weightConfig is the contribution of one matching config file.
	weightConfig = 0.30
	// weightDependency is the contribution of one matching dependency.
	weightDependency = 0.60
	// weightPrompt is the contribution of one matching prompt keyword.
	weightPrompt = 0.40
	// weightFile is the contribution of one matching workspace file.
	weightFile = 0.20
	// weightDir is the contribution of one matching workspace directory.
	weightDir = 0.20
)

// detectorSpec describes the signals that support one candidate label.
type detectorSpec struct {
	label   string
	configs []string
	deps    []string
	files   []string
	dirs    []string
	prompts []string
}

// detect runs one candidate detector and returns its hypothesis, or nil when
// no signal matched. Every emitted trace is auditable: config files carry
// "config:<file>", dependencies "dependency:<name>", prompt keywords
// "prompt:<keyword>", and structural signals "workspace:<path>".
func detect(spec detectorSpec, facts WorkspaceFacts, slots PromptSlots) *hypothesis {
	var traces []EvidenceTrace
	for _, cfg := range spec.configs {
		if hasConfig(facts.Configs, facts.Files, cfg) {
			traces = append(traces, EvidenceTrace{
				Source: SourceConfig,
				ID:     cfg,
				Weight: weightConfig,
				Reason: "config file " + cfg + " detected in the workspace",
			})
		}
	}
	for _, dep := range spec.deps {
		if hasDep(facts.Dependencies, dep) {
			traces = append(traces, EvidenceTrace{
				Source: SourceDependency,
				ID:     dep,
				Weight: weightDependency,
				Reason: "dependency " + dep + " declared in the project manifest",
			})
		}
	}
	for _, kw := range spec.prompts {
		if slots.HasKeyword(kw) {
			traces = append(traces, EvidenceTrace{
				Source: SourcePrompt,
				ID:     kw,
				Weight: weightPrompt,
				Reason: "prompt mentions \"" + kw + "\"",
			})
		}
	}
	for _, f := range spec.files {
		if hasFile(facts.Files, f) {
			traces = append(traces, EvidenceTrace{
				Source: SourceWorkspace,
				ID:     f,
				Weight: weightFile,
				Reason: "workspace file " + f + " present",
			})
		}
	}
	for _, d := range spec.dirs {
		if hasDir(facts.Directories, facts.Files, d) {
			traces = append(traces, EvidenceTrace{
				Source: SourceWorkspace,
				ID:     d + "/",
				Weight: weightDir,
				Reason: "workspace directory " + d + " present",
			})
		}
	}
	if len(traces) == 0 {
		return nil
	}
	return &hypothesis{label: spec.label, evidence: traces}
}

// ─── Framework detectors ────────────────────────────────────────────────────

var frameworkSpecs = []detectorSpec{
	{
		label:   "Next.js",
		configs: []string{"next.config.js", "next.config.mjs", "next.config.ts"},
		deps:    []string{"next"},
		prompts: []string{"next.js", "nextjs"},
		dirs:    []string{"app", "pages"},
	},
	{
		label:   "Astro",
		configs: []string{"astro.config.js", "astro.config.mjs", "astro.config.ts"},
		deps:    []string{"astro"},
		prompts: []string{"astro"},
		dirs:    []string{"src"},
	},
	{
		label:   "React + Vite",
		configs: []string{"vite.config.js", "vite.config.ts"},
		deps:    []string{"vite", "react", "react-dom", "@vitejs/plugin-react"},
		prompts: []string{"vite", "react"},
		dirs:    []string{"src"},
	},
	{
		label:   "Go + Gin",
		deps:    []string{"github.com/gin-gonic/gin"},
		files:   []string{"main.go", "go.mod"},
		prompts: []string{"gin", "golang", "go backend"},
		dirs:    []string{"cmd"},
	},
	{
		label: "Static HTML/CSS/JS",
		files: []string{"index.html", "styles.css", "script.js"},
		prompts: []string{
			"html, css", "html css", "html and css",
			"html, css and js", "html css js", "html, css, and js",
			"html, css, and javascript", "html, css and javascript",
			"static website", "vanilla website",
		},
	},
}

func detectFramework(facts WorkspaceFacts, slots PromptSlots) []hypothesis {
	return detectAll(frameworkSpecs, facts, slots)
}

// ─── Language detectors ─────────────────────────────────────────────────────

var languageSpecs = []detectorSpec{
	{
		label:   "TypeScript",
		configs: []string{"tsconfig.json", "jsconfig.json"},
		deps:    []string{"typescript"},
		files:   []string{"next-env.d.ts"},
		prompts: []string{"typescript", "tsx"},
	},
	{
		label:   "JavaScript",
		deps:    []string{"@babel/core"},
		files:   []string{"index.js", "app.js", "main.jsx"},
		prompts: []string{"javascript", "plain js"},
	},
	{
		label:   "Go",
		deps:    []string{"go:github.com/gin-gonic/gin"},
		files:   []string{"main.go", "go.mod"},
		prompts: []string{"golang", "go backend", "go server"},
	},
	{
		label:   "Static HTML",
		files:   []string{"index.html", "styles.css", "script.js"},
		prompts: []string{"html, css", "html css", "static website"},
	},
}

func detectLanguage(facts WorkspaceFacts, slots PromptSlots) []hypothesis {
	return detectAll(languageSpecs, facts, slots)
}

// ─── Styling detectors ──────────────────────────────────────────────────────

var stylingSpecs = []detectorSpec{
	{
		label:   "Tailwind CSS",
		configs: []string{"tailwind.config.js", "tailwind.config.ts", "tailwind.config.mjs"},
		deps:    []string{"tailwindcss"},
		prompts: []string{"tailwind"},
	},
	{
		label:   "SCSS",
		deps:    []string{"sass", "node-sass"},
		prompts: []string{"scss", "sass"},
	},
	{
		label:   "styled-components",
		deps:    []string{"styled-components"},
		prompts: []string{"styled components", "styled-components"},
	},
	{
		label:   "CSS Modules",
		files:   []string{"global.css", "globals.css", "styles.css"},
		prompts: []string{"css", "plain css"},
	},
}

func detectStyling(facts WorkspaceFacts, slots PromptSlots) []hypothesis {
	return detectAll(stylingSpecs, facts, slots)
}

// ─── Router detectors ───────────────────────────────────────────────────────

var routerSpecs = []detectorSpec{
	{
		label:   "File-based routing",
		dirs:    []string{"app", "pages", "src"},
		files:   []string{"_app.tsx", "_app.jsx", "index.astro"},
		prompts: []string{"file-based routing", "app router", "pages router"},
	},
	{
		label:   "React Router",
		deps:    []string{"react-router-dom", "react-router"},
		prompts: []string{"react router"},
	},
	{
		label:   "Gin router",
		deps:    []string{"github.com/gin-gonic/gin"},
		prompts: []string{"gin", "go api"},
	},
}

func detectRouter(facts WorkspaceFacts, slots PromptSlots) []hypothesis {
	return detectAll(routerSpecs, facts, slots)
}

// ─── shared helpers ─────────────────────────────────────────────────────────

// detectAll runs a set of detectors and returns the non-empty hypotheses
// ranked by score, highest first.
func detectAll(specs []detectorSpec, facts WorkspaceFacts, slots PromptSlots) []hypothesis {
	var out []hypothesis
	for _, spec := range specs {
		if h := detect(spec, facts, slots); h != nil {
			out = append(out, *h)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Score() > out[j].Score()
	})
	return out
}

// hasConfig reports whether the exact config base name appears in the
// recognized config list or the raw file list.
func hasConfig(configs, files []string, base string) bool {
	for _, c := range configs {
		if filepath.Base(c) == base || c == base {
			return true
		}
	}
	for _, f := range files {
		if filepath.Base(f) == base {
			return true
		}
	}
	return false
}

// hasDep reports whether the dependency name (or a go: module prefix) is
// declared in the manifest map.
func hasDep(deps map[string]string, name string) bool {
	if v, ok := deps[name]; ok && v != "" {
		return true
	}
	if strings.HasPrefix(name, "go:") {
		mod := strings.TrimPrefix(name, "go:")
		for k := range deps {
			if strings.HasPrefix(k, "go:") && strings.Contains(k, mod) {
				return true
			}
		}
	}
	return false
}

// hasFile reports whether a workspace file is named base or its path ends
// with "/<base>".
func hasFile(files []string, base string) bool {
	for _, f := range files {
		if filepath.Base(f) == base {
			return true
		}
	}
	return false
}

// hasDir reports whether a top-level directory exists or a file path lives
// under it.
func hasDir(dirs, files []string, name string) bool {
	for _, d := range dirs {
		if d == name {
			return true
		}
	}
	for _, f := range files {
		if strings.HasPrefix(f, name+"/") {
			return true
		}
	}
	return false
}
