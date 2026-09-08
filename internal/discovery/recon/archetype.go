package recon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type ProjectArchetype string

const (
	VANILLA_WEB     ProjectArchetype = "VANILLA_WEB"
	REACT_NEXT      ProjectArchetype = "REACT_NEXT"
	GO_BACKEND      ProjectArchetype = "GO_BACKEND"
	UNKNOWN_GENERIC ProjectArchetype = "UNKNOWN_GENERIC"
)

type ArchetypeContext struct {
	Type         ProjectArchetype `json:"type"`
	Entrypoints  []string         `json:"entrypoints"`
	HasBuildStep bool             `json:"has_build_step"`
}

// IsVANILLA_WEB reports whether the workspace contains only HTML/CSS/JS
// without Go files — enabling archetype-aware guards that disable Go-specific
// checks (go.mod verification, go test, go mod init) in /investigate and /plan.
func IsVANILLA_WEB(rootPath string) bool {
	ctx, err := DetectArchetype(rootPath)
	if err != nil {
		return false
	}
	return ctx.Type == VANILLA_WEB
}

func (a *ArchetypeContext) String() string {
	b, _ := json.Marshal(a)
	return string(b)
}

func DetectArchetype(rootPath string) (*ArchetypeContext, error) {
	info, err := os.Stat(rootPath)
	if err != nil {
		return nil, fmt.Errorf("recon: cannot access %s: %w", rootPath, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("recon: %s is not a directory", rootPath)
	}

	entries, err := os.ReadDir(rootPath)
	if err != nil {
		return nil, fmt.Errorf("recon: cannot read %s: %w", rootPath, err)
	}

	ctx := &ArchetypeContext{
		Type: UNKNOWN_GENERIC,
	}

	var hasGoMod, hasPackageJSON, hasMakefile bool
	var extCounts = map[string]int{
		".html": 0,
		".css":  0,
		".js":   0,
	}
	var hasReact, hasNext bool

	for _, entry := range entries {
		if entry.IsDir() {
			name := entry.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" || name == ".izen" || name == ".codebase-memory" {
				continue
			}
			continue
		}

		name := entry.Name()
		lower := strings.ToLower(name)

		switch lower {
		case "go.mod":
			hasGoMod = true
		case "package.json":
			hasPackageJSON = true
		case "makefile":
			hasMakefile = ctx.HasBuildStep
			ctx.HasBuildStep = true
		case "index.html":
			ctx.Entrypoints = append(ctx.Entrypoints, name)
		case "main.go":
			ctx.Entrypoints = append(ctx.Entrypoints, name)
		case "app.go":
			ctx.Entrypoints = append(ctx.Entrypoints, name)
		}

		ext := filepath.Ext(lower)
		if _, ok := extCounts[ext]; ok {
			extCounts[ext]++
		}
	}

	if hasGoMod {
		ctx.Type = GO_BACKEND
		ctx.HasBuildStep = ctx.HasBuildStep || hasMakefile
		if len(ctx.Entrypoints) == 0 {
			if _, err := os.Stat(filepath.Join(rootPath, "main.go")); err == nil {
				ctx.Entrypoints = append(ctx.Entrypoints, "main.go")
			}
		}
		return ctx, nil
	}

	if hasPackageJSON {
		hasReact, hasNext = checkPackageJSON(filepath.Join(rootPath, "package.json"))
		if hasReact || hasNext {
			ctx.Type = REACT_NEXT
			ctx.HasBuildStep = true
			if len(ctx.Entrypoints) == 0 {
				if _, err := os.Stat(filepath.Join(rootPath, "src/index.tsx")); err == nil {
					ctx.Entrypoints = append(ctx.Entrypoints, "src/index.tsx")
				} else if _, err := os.Stat(filepath.Join(rootPath, "src/index.jsx")); err == nil {
					ctx.Entrypoints = append(ctx.Entrypoints, "src/index.jsx")
				}
			}
			return ctx, nil
		}
	}

	totalWeb := extCounts[".html"] + extCounts[".css"] + extCounts[".js"]
	if totalWeb > 0 || extCounts[".html"] > 0 {
		ctx.Type = VANILLA_WEB
		if len(ctx.Entrypoints) == 0 {
			if extCounts[".html"] > 0 {
				if _, err := os.Stat(filepath.Join(rootPath, "index.html")); err == nil {
					ctx.Entrypoints = append(ctx.Entrypoints, "index.html")
				} else {
					entries, _ := os.ReadDir(rootPath)
					for _, e := range entries {
						if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".html") {
							ctx.Entrypoints = append(ctx.Entrypoints, e.Name())
							break
						}
					}
				}
			}
		}
		return ctx, nil
	}

	return ctx, nil
}

type packageJSON struct {
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

func checkPackageJSON(path string) (hasReact, hasNext bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, false
	}
	var pkg packageJSON
	if err := json.Unmarshal(data, &pkg); err != nil {
		return false, false
	}
	for name := range pkg.Dependencies {
		lower := strings.ToLower(name)
		if lower == "react" || lower == "react-dom" {
			hasReact = true
		}
		if lower == "next" {
			hasNext = true
		}
	}
	for name := range pkg.DevDependencies {
		lower := strings.ToLower(name)
		if lower == "react" || lower == "react-dom" {
			hasReact = true
		}
		if lower == "next" {
			hasNext = true
		}
	}
	return hasReact, hasNext
}
