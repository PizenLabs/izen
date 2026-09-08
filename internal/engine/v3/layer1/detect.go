package layer1

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config controls capability auto-detection for a workspace.
type Config struct {
	// Commands explicitly overrides auto-detection. A non-empty value both
	// enables the capability and fixes its command; an empty value disables
	// it. Overrides are applied after stack detection and always win.
	Commands map[Capability]string
}

// Detect builds an immutable capability graph for the workspace at root.
func Detect(root string) (*Graph, error) {
	return DetectWithConfig(root, Config{})
}

// DetectWithConfig builds a capability graph, applying explicit command
// overrides on top of auto-detection.
func DetectWithConfig(root string, cfg Config) (*Graph, error) {
	fi, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("layer1: workspace root %q: %w", root, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("layer1: workspace root %q is not a directory", root)
	}
	det := &detector{root: root}
	commands := det.detect()
	for cap, cmd := range cfg.Commands {
		if strings.TrimSpace(cmd) == "" {
			delete(commands, cap)
			continue
		}
		commands[cap] = cmd
	}
	return &Graph{stack: det.stack, commands: commands}, nil
}

// detector collects deterministic indicators from the workspace root and
// derives the stack plus per-capability commands.
type detector struct {
	root  string
	stack Stack

	hasGoMod    bool
	hasCargo    bool
	hasPkgJSON  bool
	hasPython   bool
	hasDocker   bool
	hasCompose  bool
	hasGolangci bool
	hasPrettier bool
	hasStatic   bool
	hasPnpm     bool
	hasYarn     bool
	hasBun      bool
	hasUv       bool
	hasPytest   bool
	hasRuff     bool
	hasFlake8   bool
	hasBlack    bool
	hasPyBuild  bool
	hasPoetry   bool

	scripts map[string]bool
}

func (d *detector) detect() map[Capability]string {
	d.scan()
	switch d.stack {
	case StackGo:
		return d.detectGo()
	case StackNode:
		return d.detectNode()
	case StackRust:
		return d.detectRust()
	case StackPython:
		return d.detectPython()
	case StackDocker:
		return d.detectDockerOnly()
	case StackStatic:
		return d.detectStatic()
	default:
		return d.detectUnknown()
	}
}

func (d *detector) scan() {
	entries, err := os.ReadDir(d.root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		lower := strings.ToLower(e.Name())
		switch {
		case lower == "go.mod" || lower == "go.sum":
			d.hasGoMod = true
		case lower == "cargo.toml" || lower == "cargo.lock":
			d.hasCargo = true
		case lower == "package.json":
			d.hasPkgJSON = true
		case lower == "pyproject.toml" || lower == "setup.py" || lower == "setup.cfg" ||
			lower == "requirements.txt" || lower == "pipfile":
			d.hasPython = true
		case lower == "dockerfile":
			d.hasDocker = true
		case strings.HasPrefix(lower, "docker-compose.") || strings.HasPrefix(lower, "compose."):
			d.hasCompose = true
		case lower == "pnpm-lock.yaml":
			d.hasPnpm = true
		case lower == "yarn.lock":
			d.hasYarn = true
		case lower == "bun.lock" || lower == "bun.lockb":
			d.hasBun = true
		case lower == "uv.lock":
			d.hasUv = true
		case lower == ".golangci.yml" || lower == ".golangci.yaml" || lower == ".golangci.toml":
			d.hasGolangci = true
		case lower == "pytest.ini" || lower == "tox.ini" || lower == "conftest.py":
			d.hasPytest = true
		case lower == "ruff.toml" || lower == ".ruff.toml":
			d.hasRuff = true
		case lower == ".flake8":
			d.hasFlake8 = true
		case strings.HasSuffix(lower, ".html") || strings.HasSuffix(lower, ".htm") ||
			strings.HasSuffix(lower, ".css") || strings.HasSuffix(lower, ".js"):
			d.hasStatic = true
		}
		if isPrettierConfig(lower) {
			d.hasPrettier = true
		}
	}

	if d.hasPkgJSON {
		scripts, pkgPrettier := readPackageMeta(filepath.Join(d.root, "package.json"))
		d.scripts = scripts
		d.hasPrettier = d.hasPrettier || pkgPrettier
	}
	if d.hasPython {
		if content, err := os.ReadFile(filepath.Join(d.root, "pyproject.toml")); err == nil {
			c := string(content)
			d.hasPoetry = strings.Contains(c, "[tool.poetry]")
			d.hasPyBuild = strings.Contains(c, "[build-system]")
			if strings.Contains(c, "[tool.pytest") {
				d.hasPytest = true
			}
			if strings.Contains(c, "[tool.ruff]") {
				d.hasRuff = true
			}
			if strings.Contains(c, "[tool.black]") {
				d.hasBlack = true
			}
			if strings.Contains(c, "[tool.flake8]") {
				d.hasFlake8 = true
			}
		}
	}

	d.stack = d.chooseStack()
}

func (d *detector) chooseStack() Stack {
	switch {
	case d.hasGoMod:
		return StackGo
	case d.hasCargo:
		return StackRust
	case d.hasPkgJSON:
		return StackNode
	case d.hasPython:
		return StackPython
	case d.hasDocker || d.hasCompose:
		return StackDocker
	case d.hasStatic:
		return StackStatic
	default:
		return StackUnknown
	}
}

func (d *detector) detectGo() map[Capability]string {
	commands := map[Capability]string{
		CapBuild:  "go build ./...",
		CapTest:   "go test ./...",
		CapFormat: "gofmt -w .",
	}
	if d.hasGolangci {
		commands[CapLint] = "golangci-lint run ./..."
	} else {
		commands[CapLint] = "go vet ./..."
	}
	d.addContainer(commands)
	return commands
}

func (d *detector) detectNode() map[Capability]string {
	commands := map[Capability]string{}
	mgr := d.nodeManager()
	if d.scripts["build"] {
		commands[CapBuild] = mgr + " run build"
	}
	if d.scripts["test"] {
		commands[CapTest] = mgr + " test"
	}
	if d.scripts["lint"] {
		commands[CapLint] = mgr + " run lint"
	}
	if d.scripts["format"] {
		commands[CapFormat] = mgr + " run format"
	} else if d.hasPrettier {
		commands[CapFormat] = mgr + " exec prettier --write ."
	}
	d.addContainer(commands)
	return commands
}

func (d *detector) detectRust() map[Capability]string {
	commands := map[Capability]string{
		CapBuild:  "cargo build",
		CapTest:   "cargo test",
		CapLint:   "cargo clippy --all-targets -- -D warnings",
		CapFormat: "cargo fmt --all",
	}
	d.addContainer(commands)
	return commands
}

func (d *detector) detectPython() map[Capability]string {
	commands := map[Capability]string{}
	switch {
	case d.hasPoetry:
		commands[CapBuild] = "poetry build"
	case d.hasPyBuild:
		commands[CapBuild] = "python -m build"
	}
	if d.hasPytest {
		cmd := "pytest"
		switch {
		case d.hasPoetry:
			cmd = "poetry run pytest"
		case d.hasUv:
			cmd = "uv run pytest"
		}
		commands[CapTest] = cmd
	}
	if d.hasRuff {
		commands[CapLint] = "ruff check ."
		commands[CapFormat] = "ruff format ."
	} else {
		if d.hasFlake8 {
			commands[CapLint] = "flake8 ."
		}
		if d.hasBlack {
			commands[CapFormat] = "black ."
		}
	}
	d.addContainer(commands)
	return commands
}

func (d *detector) detectDockerOnly() map[Capability]string {
	commands := map[Capability]string{}
	switch {
	case d.hasCompose:
		commands[CapContainer] = "docker compose build"
	case d.hasDocker:
		commands[CapContainer] = d.dockerBuildCommand()
	}
	return commands
}

// detectStatic resolves a pure static HTML/CSS project. No build or test
// toolchain exists, so build/test/lint/format are never fabricated; only a
// container capability is reported when a Dockerfile is present.
func (d *detector) detectStatic() map[Capability]string {
	commands := map[Capability]string{}
	d.addContainer(commands)
	return commands
}

func (d *detector) detectUnknown() map[Capability]string {
	commands := map[Capability]string{}
	d.addContainer(commands)
	return commands
}

func (d *detector) addContainer(commands map[Capability]string) {
	if d.hasDocker {
		commands[CapContainer] = d.dockerBuildCommand()
	}
}

func (d *detector) nodeManager() string {
	switch {
	case d.hasPnpm:
		return "pnpm"
	case d.hasYarn:
		return "yarn"
	case d.hasBun:
		return "bun"
	default:
		return "npm"
	}
}

func (d *detector) dockerBuildCommand() string {
	name := sanitizeImageName(filepath.Base(d.root))
	if name == "" {
		name = "app"
	}
	return "docker build -t " + name + " ."
}

func sanitizeImageName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		}
	}
	return b.String()
}

func isPrettierConfig(lower string) bool {
	switch lower {
	case ".prettierrc", ".prettierrc.json", ".prettierrc.yaml", ".prettierrc.yml",
		".prettierrc.js", ".prettierrc.mjs", ".prettierrc.cjs",
		"prettier.config.js", "prettier.config.mjs", "prettier.config.cjs", "prettier.config.ts":
		return true
	}
	return false
}

// readPackageMeta extracts script names and whether a top-level prettier
// config is declared inside package.json.
func readPackageMeta(path string) (map[string]bool, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var doc struct {
		Scripts  map[string]string `json:"scripts"`
		Prettier any               `json:"prettier"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, false
	}
	scripts := make(map[string]bool, len(doc.Scripts))
	for name := range doc.Scripts {
		scripts[name] = true
	}
	return scripts, doc.Prettier != nil
}
