// Package layer0 implements the Knowledge Resolution layer of the Izen
// engine. It deterministically scans a workspace and collapses runtime facts,
// agent rules, project documentation and README prose into a single
// ResolvedKnowledge value. Resolution is 100% rule-based; no LLM is involved.
//
// Resolution priority hierarchy (lower Priority values win):
//
//	Priority 1  RuntimeFacts - lockfiles, manifests, observed file tree
//	Priority 2  AgentRules   - AGENTS.md, CLAUDE.md, .cursorrules
//	Priority 3  ProjectDocs  - docs/architecture.md, CONTRIBUTING.md
//	Priority 4  HumanReads   - README.md
//
// When a declared convention (e.g. README says "npm install") conflicts with a
// runtime fact (e.g. pnpm-lock.yaml exists), the runtime fact always wins and
// the disagreement is recorded as a Conflict.
//
// A KnowledgeResolver is immutable after construction and safe for concurrent
// use. Resolve returns a fresh, immutable ResolvedKnowledge on every call;
// callers must treat the returned value as read-only.
package layer0

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Priority ranks the authority of a knowledge source. Lower values win.
type Priority int

const (
	// RuntimeFacts are derived from the actual workspace: lockfiles,
	// manifests and the observed file tree. They outrank every other source.
	RuntimeFacts Priority = 1
	// AgentRules covers AGENTS.md, CLAUDE.md and .cursorrules.
	AgentRules Priority = 2
	// ProjectDocs covers docs/architecture.md and CONTRIBUTING.md.
	ProjectDocs Priority = 3
	// HumanReads covers README.md.
	HumanReads Priority = 4
)

// String returns a stable machine-readable label for the priority.
func (p Priority) String() string {
	switch p {
	case RuntimeFacts:
		return "runtime_facts"
	case AgentRules:
		return "agent_rules"
	case ProjectDocs:
		return "project_docs"
	case HumanReads:
		return "human_reads"
	default:
		return fmt.Sprintf("priority(%d)", int(p))
	}
}

// Manager identifies a detected package/toolchain manager.
type Manager string

const (
	ManagerGo       Manager = "go"
	ManagerPnpm     Manager = "pnpm"
	ManagerNpm      Manager = "npm"
	ManagerYarn     Manager = "yarn"
	ManagerBun      Manager = "bun"
	ManagerCargo    Manager = "cargo"
	ManagerPoetry   Manager = "poetry"
	ManagerPip      Manager = "pip"
	ManagerPipenv   Manager = "pipenv"
	ManagerUv       Manager = "uv"
	ManagerComposer Manager = "composer"
	ManagerGradle   Manager = "gradle"
	ManagerMaven    Manager = "maven"
	ManagerUnknown  Manager = "unknown"
)

const (
	kindLockfile = "lockfile"
	kindManifest = "manifest"
)

// Lockfile describes a detected lockfile or manifest relative to the root.
type Lockfile struct {
	Path    string  `json:"path"`
	Manager Manager `json:"manager"`
	Kind    string  `json:"kind"`
}

// SourceDoc is a discovered knowledge source document.
type SourceDoc struct {
	Path     string   `json:"path"`
	Priority Priority `json:"priority"`
	Size     int64    `json:"size"`
	// Head is a bounded preview of the document content for context.
	Head string `json:"head,omitempty"`
}

// Convention is an active workspace convention (e.g. install, build, test).
type Convention struct {
	Name     string   `json:"name"`
	Value    string   `json:"value"`
	Priority Priority `json:"priority"`
	Source   string   `json:"source"`
}

// Constraint is a structural fact about the workspace.
type Constraint struct {
	Kind     string   `json:"kind"`
	Value    string   `json:"value"`
	Priority Priority `json:"priority"`
}

// Conflict records a declared convention that was overridden by a higher
// priority source. WinningValue/DeclaredAt describe the source that lost.
type Conflict struct {
	Name          string   `json:"name"`
	WinningValue  string   `json:"winning_value"`
	WinningAt     Priority `json:"winning_priority"`
	DeclaredValue string   `json:"declared_value"`
	DeclaredAt    Priority `json:"declared_at"`
	DeclaredSrc   string   `json:"declared_source"`
	Resolution    string   `json:"resolution"`
}

// ResolvedKnowledge is the output of a KnowledgeResolver scan. It is
// immutable after construction; callers must not mutate its slices.
type ResolvedKnowledge struct {
	Root                  string       `json:"root"`
	Managers              []Manager    `json:"managers"`
	PrimaryManager        Manager      `json:"primary_manager"`
	Lockfiles             []Lockfile   `json:"lockfiles"`
	FileTree              []string     `json:"file_tree"`
	AgentRules            []SourceDoc  `json:"agent_rules"`
	ProjectDocs           []SourceDoc  `json:"project_docs"`
	Readme                *SourceDoc   `json:"readme,omitempty"`
	ActiveConventions     []Convention `json:"active_conventions"`
	StructuralConstraints []Constraint `json:"structural_constraints"`
	Conflicts             []Conflict   `json:"conflicts"`
}

// Convention returns the active convention with the given name, if any.
func (k *ResolvedKnowledge) Convention(name string) (Convention, bool) {
	for _, c := range k.ActiveConventions {
		if c.Name == name {
			return c, true
		}
	}
	return Convention{}, false
}

// Supports reports whether an active convention with the given name exists.
func (k *ResolvedKnowledge) Supports(name string) bool {
	_, ok := k.Convention(name)
	return ok
}

// KnowledgeResolver scans a workspace root. It is immutable after construction
// and safe for concurrent Resolve calls.
type KnowledgeResolver struct {
	root string
	opts options
}

// Option customizes a KnowledgeResolver at construction time.
type Option func(*options)

type options struct {
	maxDepth    int
	maxTree     int
	maxDocBytes int64
	skipDirs    map[string]bool
}

// WithMaxDepth limits directory recursion depth for the file tree scan.
func WithMaxDepth(n int) Option {
	return func(o *options) { o.maxDepth = n }
}

// WithMaxTreeEntries caps the number of files recorded in the file tree.
func WithMaxTreeEntries(n int) Option {
	return func(o *options) { o.maxTree = n }
}

// WithMaxDocBytes caps how much of each source document is read.
func WithMaxDocBytes(n int64) Option {
	return func(o *options) { o.maxDocBytes = n }
}

func defaultOptions() options {
	return options{
		maxDepth:    6,
		maxTree:     1000,
		maxDocBytes: 256 * 1024,
		skipDirs:    defaultSkipDirs,
	}
}

// NewKnowledgeResolver returns a resolver for the given workspace root.
func NewKnowledgeResolver(root string, opts ...Option) *KnowledgeResolver {
	o := defaultOptions()
	for _, fn := range opts {
		fn(&o)
	}
	return &KnowledgeResolver{root: root, opts: o}
}

// Resolve scans the workspace and returns the resolved knowledge.
func (r *KnowledgeResolver) Resolve() (*ResolvedKnowledge, error) {
	if fi, err := os.Stat(r.root); err != nil {
		return nil, fmt.Errorf("layer0: workspace root %q: %w", r.root, err)
	} else if !fi.IsDir() {
		return nil, fmt.Errorf("layer0: workspace root %q is not a directory", r.root)
	}

	tree, lockfiles := r.walkTree()
	lockfiles = r.refineRootManifests(lockfiles)
	lockfiles = dedupeLockfiles(lockfiles)

	managers := managersOf(lockfiles)
	primary := selectPrimary(managers)

	st := r.collectState(tree, lockfiles)
	runtime := runtimeConventions(primary, st)

	var agentRules []SourceDoc
	var projectDocs []SourceDoc
	var declared []Convention

	for _, rel := range r.agentRuleCandidates() {
		doc, content := r.readDoc(rel, AgentRules)
		if doc == nil {
			continue
		}
		agentRules = append(agentRules, *doc)
		declared = append(declared, scanCommands(content, AgentRules, doc.Path)...)
	}
	for _, rel := range r.projectDocCandidates() {
		doc, content := r.readDoc(rel, ProjectDocs)
		if doc == nil {
			continue
		}
		projectDocs = append(projectDocs, *doc)
		declared = append(declared, scanCommands(content, ProjectDocs, doc.Path)...)
	}

	var readme *SourceDoc
	if rel := r.readmeCandidate(); rel != "" {
		if doc, content := r.readDoc(rel, HumanReads); doc != nil {
			readme = doc
			declared = append(declared, scanCommands(content, HumanReads, doc.Path)...)
		}
	}

	active, conflicts := resolveConventions(runtime, declared)
	sort.Slice(active, func(i, j int) bool {
		if active[i].Priority != active[j].Priority {
			return active[i].Priority < active[j].Priority
		}
		if active[i].Name != active[j].Name {
			return active[i].Name < active[j].Name
		}
		return active[i].Value < active[j].Value
	})

	constraints := buildConstraints(primary, managers, st)

	return &ResolvedKnowledge{
		Root:                  r.root,
		Managers:              managers,
		PrimaryManager:        primary,
		Lockfiles:             lockfiles,
		FileTree:              tree,
		AgentRules:            agentRules,
		ProjectDocs:           projectDocs,
		Readme:                readme,
		ActiveConventions:     active,
		StructuralConstraints: constraints,
		Conflicts:             conflicts,
	}, nil
}

// walkTree collects the file tree and any lockfiles/manifests found while
// respecting skip rules and the depth/tree caps.
func (r *KnowledgeResolver) walkTree() ([]string, []Lockfile) {
	tree := make([]string, 0, 64)
	var lockfiles []Lockfile
	root := r.root

	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if r.shouldSkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == ".DS_Store" {
			return nil
		}
		if len(tree) < r.opts.maxTree {
			tree = append(tree, rel)
		}
		if rule, ok := lockfileByName[strings.ToLower(d.Name())]; ok {
			lockfiles = append(lockfiles, Lockfile{
				Path:    rel,
				Manager: rule.manager,
				Kind:    rule.kind,
			})
		}
		return nil
	})

	sort.Strings(tree)
	return tree, lockfiles
}

func (r *KnowledgeResolver) shouldSkipDir(base string) bool {
	if strings.HasPrefix(base, ".") {
		return true
	}
	_, skip := r.opts.skipDirs[base]
	return skip
}

// refineRootManifests pins the manager of ambiguous manifests (package.json,
// pyproject.toml) at the workspace root using companion lockfiles/content.
func (r *KnowledgeResolver) refineRootManifests(lockfiles []Lockfile) []Lockfile {
	has := make(map[string]bool)
	for _, lf := range lockfiles {
		if !strings.Contains(lf.Path, "/") {
			has[strings.ToLower(lf.Path)] = true
		}
	}
	for i := range lockfiles {
		rel := lockfiles[i].Path
		if strings.Contains(rel, "/") {
			continue
		}
		switch strings.ToLower(rel) {
		case "package.json":
			switch {
			case has["pnpm-lock.yaml"]:
				lockfiles[i].Manager = ManagerPnpm
			case has["yarn.lock"]:
				lockfiles[i].Manager = ManagerYarn
			case has["bun.lock"] || has["bun.lockb"]:
				lockfiles[i].Manager = ManagerBun
			default:
				lockfiles[i].Manager = ManagerNpm
			}
		case "pyproject.toml":
			content, _ := readCap(filepath.Join(r.root, rel), 128*1024)
			switch {
			case strings.Contains(string(content), "[tool.poetry]"):
				lockfiles[i].Manager = ManagerPoetry
			case has["uv.lock"]:
				lockfiles[i].Manager = ManagerUv
			default:
				lockfiles[i].Manager = ManagerPip
			}
		}
	}
	return lockfiles
}

// agentRuleCandidates returns the deterministic set of agent-rule files.
func (r *KnowledgeResolver) agentRuleCandidates() []string {
	var out []string
	for _, dir := range []string{"", ".github"} {
		base := filepath.Join(r.root, dir)
		for _, name := range findInDir(base, []string{"AGENTS.md", "CLAUDE.md", ".cursorrules"}) {
			out = append(out, filepath.ToSlash(filepath.Join(dir, name)))
		}
	}
	sort.Strings(out)
	return out
}

// projectDocCandidates returns the deterministic set of project docs.
func (r *KnowledgeResolver) projectDocCandidates() []string {
	out := make([]string, 0, 4)
	out = append(out, findInDir(r.root, []string{"CONTRIBUTING.md"})...)
	for _, name := range findInDir(filepath.Join(r.root, "docs"), []string{"architecture.md", "ARCHITECTURE.md"}) {
		out = append(out, filepath.ToSlash(filepath.Join("docs", name)))
	}
	for _, name := range findInDir(filepath.Join(r.root, "docs", "architecture"), []string{"architecture.md", "ARCHITECTURE.md"}) {
		out = append(out, filepath.ToSlash(filepath.Join("docs", "architecture", name)))
	}
	sort.Strings(out)
	return out
}

// readmeCandidate returns the preferred README path, if any.
func (r *KnowledgeResolver) readmeCandidate() string {
	names := findInDir(r.root, []string{"README.md", "readme.md", "README.rst"})
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

// findInDir returns existing file names in dir matching names (case-insensitive),
// using the ACTUAL on-disk spelling of each match. The canonical candidate
// spelling is used only as the lower-cased match key; the returned name is the
// real entry name so the caller can build a path that os.Stat resolves on
// case-sensitive filesystems. Returning the canonical spelling here would
// misreport e.g. "ARCHITECTURE.md" as "architecture.md" and silently drop the
// doc when readDoc stats the (nonexistent) canonical path on Linux CI.
func findInDir(dir string, names []string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	want := make(map[string]string, len(names))
	for _, n := range names {
		key := strings.ToLower(n)
		if _, exists := want[key]; !exists {
			want[key] = n
		}
	}
	var found []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if _, ok := want[strings.ToLower(e.Name())]; ok {
			found = append(found, e.Name())
		}
	}
	sort.Strings(found)
	return found
}

// readDoc reads a document relative to the root, returning the doc metadata
// plus the bounded raw content used for convention scanning.
func (r *KnowledgeResolver) readDoc(rel string, prio Priority) (*SourceDoc, string) {
	full := filepath.Join(r.root, rel)
	fi, err := os.Stat(full)
	if err != nil || fi.IsDir() {
		return nil, ""
	}
	data, err := readCap(full, r.opts.maxDocBytes)
	if err != nil {
		return nil, ""
	}
	content := string(data)
	doc := &SourceDoc{
		Path:     filepath.ToSlash(rel),
		Priority: prio,
		Size:     fi.Size(),
		Head:     firstLines(content, 60),
	}
	return doc, content
}

// collectState gathers deterministic indicators needed to derive conventions
// and structural constraints.
func (r *KnowledgeResolver) collectState(tree []string, lockfiles []Lockfile) *scanState {
	st := &scanState{packageScripts: map[string]bool{}}

	for _, rel := range tree {
		base := strings.ToLower(filepath.Base(rel))
		switch {
		case base == "dockerfile":
			st.hasDockerfile = true
		case base == ".golangci.yml" || base == ".golangci.yaml" || base == ".golangci.toml":
			st.hasGolangci = true
		case base == "requirements.txt":
			st.hasRequirements = true
		case base == "pytest.ini" || base == "tox.ini" || base == "conftest.py":
			st.hasPytest = true
		case base == "ruff.toml" || base == ".ruff.toml":
			st.hasRuff = true
		case base == ".flake8":
			st.hasFlake8 = true
		case base == "setup.py" || base == "setup.cfg":
			st.hasPyBuild = true
		case strings.HasPrefix(base, "next.config."):
			st.nodeFramework = "next"
		case strings.HasPrefix(base, "vite.config."):
			st.nodeFramework = "vite"
		case strings.HasPrefix(base, "nuxt.config."):
			st.nodeFramework = "nuxt"
		case strings.HasPrefix(base, "svelte.config."):
			st.nodeFramework = "svelte"
		case strings.HasPrefix(base, "astro.config."):
			st.nodeFramework = "astro"
		}
		if strings.HasSuffix(base, ".html") || strings.HasSuffix(base, ".htm") {
			st.hasStaticHTML = true
		}
	}

	for _, lf := range lockfiles {
		switch strings.ToLower(lf.Path) {
		case "package.json":
			if !isNodeManager(lf.Manager) {
				continue
			}
			scripts := readJSONScripts(filepath.Join(r.root, lf.Path))
			for name := range scripts {
				switch name {
				case "build", "test", "lint", "format", "dev", "check":
					st.packageScripts[name] = true
				}
			}
		case "go.mod":
			if module, ok := readGoModule(filepath.Join(r.root, lf.Path)); ok {
				st.modulePath = module
			}
		case "pyproject.toml":
			content, err := readCap(filepath.Join(r.root, lf.Path), 128*1024)
			if err != nil {
				continue
			}
			c := string(content)
			st.pyToolPoetry = strings.Contains(c, "[tool.poetry]")
			if strings.Contains(c, "[build-system]") {
				st.hasPyBuild = true
			}
			if strings.Contains(c, "[tool.ruff]") {
				st.hasRuff = true
			}
			if strings.Contains(c, "[tool.black]") {
				st.hasBlack = true
			}
			if strings.Contains(c, "[tool.flake8]") {
				st.hasFlake8 = true
			}
			if strings.Contains(c, "[tool.pytest") {
				st.hasPytest = true
			}
		}
	}

	return st
}

type scanState struct {
	hasDockerfile   bool
	hasGolangci     bool
	hasRequirements bool
	hasPytest       bool
	hasRuff         bool
	hasFlake8       bool
	hasBlack        bool
	hasPyBuild      bool
	pyToolPoetry    bool
	hasStaticHTML   bool
	nodeFramework   string
	modulePath      string
	packageScripts  map[string]bool
}

// runtimeConventions derives conventions strictly from runtime facts (the
// detected managers and the observed file tree).
func runtimeConventions(primary Manager, st *scanState) []Convention {
	var out []Convention
	add := func(name, value string) {
		out = append(out, Convention{
			Name:     name,
			Value:    value,
			Priority: RuntimeFacts,
			Source:   "runtime_facts",
		})
	}

	switch primary {
	case ManagerGo:
		add("install", "go mod tidy")
		add("build", "go build ./...")
		add("test", "go test ./...")
		if st.hasGolangci {
			add("lint", "golangci-lint run ./...")
		} else {
			add("lint", "go vet ./...")
		}
		add("format", "gofmt -w .")
	case ManagerPnpm, ManagerNpm, ManagerYarn, ManagerBun:
		mgr := string(primary)
		add("install", mgr+" install")
		if st.packageScripts["build"] {
			add("build", mgr+" run build")
		}
		if st.packageScripts["test"] {
			add("test", mgr+" test")
		}
		if st.packageScripts["lint"] {
			add("lint", mgr+" run lint")
		}
		if st.packageScripts["format"] {
			add("format", mgr+" run format")
		}
		if st.packageScripts["dev"] {
			add("dev", mgr+" run dev")
		}
		if st.packageScripts["check"] {
			add("check", mgr+" run check")
		}
	case ManagerCargo:
		add("install", "cargo fetch")
		add("build", "cargo build")
		add("test", "cargo test")
		add("lint", "cargo clippy --all-targets -- -D warnings")
		add("format", "cargo fmt --all")
	case ManagerPoetry:
		add("install", "poetry install")
		if st.pyToolPoetry {
			add("build", "poetry build")
		}
		if st.hasPytest {
			add("test", "poetry run pytest")
		}
	case ManagerUv:
		add("install", "uv sync")
		if st.hasPytest {
			add("test", "uv run pytest")
		}
	case ManagerPipenv:
		add("install", "pipenv install")
		if st.hasPytest {
			add("test", "pipenv run pytest")
		}
	case ManagerPip:
		if st.hasRequirements {
			add("install", "pip install -r requirements.txt")
		} else {
			add("install", "pip install -e .")
		}
		if st.hasPytest {
			add("test", "pytest")
		}
		if st.hasRuff {
			add("lint", "ruff check .")
			add("format", "ruff format .")
		} else if st.hasFlake8 {
			add("lint", "flake8 .")
		}
		if st.hasBlack {
			add("format", "black .")
		}
	case ManagerComposer:
		add("install", "composer install")
	case ManagerGradle:
		add("build", "gradle build")
		add("test", "gradle test")
	case ManagerMaven:
		add("build", "mvn package")
		add("test", "mvn test")
	}
	return out
}

// buildConstraints derives structural facts about the workspace.
func buildConstraints(primary Manager, managers []Manager, st *scanState) []Constraint {
	var out []Constraint
	add := func(kind, value string) {
		out = append(out, Constraint{Kind: kind, Value: value, Priority: RuntimeFacts})
	}

	if primary != ManagerUnknown {
		add("package_manager", string(primary))
	}
	if st.modulePath != "" {
		add("go_module_path", st.modulePath)
	}
	if st.nodeFramework != "" {
		add("framework", st.nodeFramework)
	}
	if st.hasDockerfile {
		add("has_dockerfile", "true")
	}
	if primary == ManagerUnknown && st.hasStaticHTML {
		add("static_html_only", "true")
	}
	if len(managers) > 1 {
		add("monorepo", "true")
	}
	if len(st.packageScripts) > 0 {
		keys := make([]string, 0, len(st.packageScripts))
		for k := range st.packageScripts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		add("node_scripts", strings.Join(keys, ","))
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Value < out[j].Value
	})
	return out
}

// resolveConventions merges runtime and declared conventions. For each
// convention name the highest-priority candidate wins; lower-priority
// disagreements are recorded as conflicts. Consistent declarations (a value
// equal to or a prefix of the winner) are treated as confirmations, not
// conflicts.
func resolveConventions(runtime, declared []Convention) ([]Convention, []Conflict) {
	byName := make(map[string][]Convention)
	for _, c := range runtime {
		byName[c.Name] = append(byName[c.Name], c)
	}
	for _, c := range declared {
		byName[c.Name] = append(byName[c.Name], c)
	}

	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)

	var active = make([]Convention, 0, len(names))
	var conflicts []Conflict
	for _, name := range names {
		cands := byName[name]
		sort.SliceStable(cands, func(i, j int) bool {
			if cands[i].Priority != cands[j].Priority {
				return cands[i].Priority < cands[j].Priority
			}
			return cands[i].Source < cands[j].Source
		})
		winner := cands[0]
		active = append(active, winner)
		for _, c := range cands[1:] {
			if consistentValues(c.Value, winner.Value) {
				continue
			}
			conflict := Conflict{
				Name:          name,
				WinningValue:  winner.Value,
				WinningAt:     winner.Priority,
				DeclaredValue: c.Value,
				DeclaredAt:    c.Priority,
				DeclaredSrc:   c.Source,
			}
			if winner.Priority == RuntimeFacts {
				conflict.Resolution = "runtime_facts override declared convention"
			} else {
				conflict.Resolution = fmt.Sprintf("priority %s overrides %s", winner.Priority, c.Priority)
			}
			conflicts = append(conflicts, conflict)
		}
	}
	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].Name < conflicts[j].Name })
	return active, conflicts
}

// consistentValues reports whether two commands agree (equal or one a prefix
// of the other), e.g. "go test" is consistent with "go test ./...".
func consistentValues(a, b string) bool {
	return a == b || strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

// scanCommands scans document content for declared command conventions using
// a curated, deterministic table. Patterns are matched at word boundaries so
// that aliases such as "npm i" do not match inside "pnpm install".
func scanCommands(content string, prio Priority, src string) []Convention {
	seen := make(map[string]bool)
	var out []Convention
	for _, m := range declaredCommandMatchers {
		if !m.re.MatchString(content) {
			continue
		}
		rule := m.rule
		key := rule.name + "\x00" + rule.value
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Convention{Name: rule.name, Value: rule.value, Priority: prio, Source: src})
	}
	return out
}

// commandRule maps a scanned pattern to a canonical declared convention.
type commandRule struct {
	name    string
	value   string
	pattern string
}

var declaredCommandPatterns = []commandRule{
	// Go
	{"install", "go mod tidy", "go mod tidy"},
	{"install", "go mod download", "go mod download"},
	{"install", "go get", "go get"},
	{"build", "go build", "go build"},
	{"test", "go test", "go test"},
	{"lint", "go vet", "go vet"},
	{"lint", "golangci-lint run", "golangci-lint"},
	{"format", "gofmt", "gofmt"},
	// pnpm
	{"install", "pnpm install", "pnpm install"},
	{"install", "pnpm install", "pnpm i"},
	{"build", "pnpm run build", "pnpm run build"},
	{"build", "pnpm build", "pnpm build"},
	{"test", "pnpm test", "pnpm test"},
	{"lint", "pnpm run lint", "pnpm run lint"},
	{"lint", "pnpm lint", "pnpm lint"},
	{"format", "pnpm run format", "pnpm run format"},
	{"format", "pnpm format", "pnpm format"},
	{"dev", "pnpm run dev", "pnpm run dev"},
	{"dev", "pnpm dev", "pnpm dev"},
	// npm
	{"install", "npm install", "npm install"},
	{"install", "npm install", "npm i"},
	{"build", "npm run build", "npm run build"},
	{"test", "npm test", "npm test"},
	{"lint", "npm run lint", "npm run lint"},
	{"format", "npm run format", "npm run format"},
	{"dev", "npm run dev", "npm run dev"},
	// yarn
	{"install", "yarn install", "yarn install"},
	{"build", "yarn build", "yarn build"},
	{"test", "yarn test", "yarn test"},
	{"lint", "yarn lint", "yarn lint"},
	{"format", "yarn format", "yarn format"},
	{"dev", "yarn dev", "yarn dev"},
	// bun
	{"install", "bun install", "bun install"},
	{"build", "bun run build", "bun run build"},
	{"test", "bun test", "bun test"},
	{"lint", "bun run lint", "bun run lint"},
	{"dev", "bun run dev", "bun run dev"},
	// cargo
	{"install", "cargo fetch", "cargo fetch"},
	{"build", "cargo build", "cargo build"},
	{"test", "cargo test", "cargo test"},
	{"lint", "cargo clippy", "cargo clippy"},
	{"format", "cargo fmt", "cargo fmt"},
	// python
	{"install", "pip install", "pip install"},
	{"install", "poetry install", "poetry install"},
	{"install", "uv sync", "uv sync"},
	{"install", "pipenv install", "pipenv install"},
	{"test", "pytest", "pytest"},
	{"test", "python -m unittest", "python -m unittest"},
	{"lint", "ruff check", "ruff check"},
	{"lint", "flake8", "flake8"},
	{"format", "ruff format", "ruff format"},
	{"format", "black", "black "},
	// containers
	{"container", "docker build", "docker build"},
	{"container", "docker compose build", "docker compose build"},
}

// commandMatcher pairs a command rule with its word-boundary matcher.
type commandMatcher struct {
	rule commandRule
	re   *regexp.Regexp
}

var declaredCommandMatchers = func() []commandMatcher {
	out := make([]commandMatcher, 0, len(declaredCommandPatterns))
	for _, rule := range declaredCommandPatterns {
		re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(rule.pattern) + `\b`)
		out = append(out, commandMatcher{rule: rule, re: re})
	}
	return out
}()

var lockfileRules = []lockfileRule{
	{"go.mod", ManagerGo, kindManifest},
	{"go.sum", ManagerGo, kindLockfile},
	{"package.json", ManagerNpm, kindManifest},
	{"pnpm-lock.yaml", ManagerPnpm, kindLockfile},
	{"package-lock.json", ManagerNpm, kindLockfile},
	{"npm-shrinkwrap.json", ManagerNpm, kindLockfile},
	{"yarn.lock", ManagerYarn, kindLockfile},
	{"bun.lock", ManagerBun, kindLockfile},
	{"bun.lockb", ManagerBun, kindLockfile},
	{"Cargo.toml", ManagerCargo, kindManifest},
	{"Cargo.lock", ManagerCargo, kindLockfile},
	{"pyproject.toml", ManagerPip, kindManifest},
	{"poetry.lock", ManagerPoetry, kindLockfile},
	{"Pipfile.lock", ManagerPipenv, kindLockfile},
	{"requirements.txt", ManagerPip, kindManifest},
	{"uv.lock", ManagerUv, kindLockfile},
	{"composer.lock", ManagerComposer, kindLockfile},
	{"build.gradle", ManagerGradle, kindManifest},
	{"build.gradle.kts", ManagerGradle, kindManifest},
	{"pom.xml", ManagerMaven, kindManifest},
}

type lockfileRule struct {
	name    string
	manager Manager
	kind    string
}

var lockfileByName = func() map[string]lockfileRule {
	m := make(map[string]lockfileRule, len(lockfileRules))
	for _, r := range lockfileRules {
		m[strings.ToLower(r.name)] = r
	}
	return m
}()

// managerRank defines the deterministic precedence used to select the primary
// manager when multiple lockfiles coexist.
var managerRank = map[Manager]int{
	ManagerGo:       100,
	ManagerCargo:    90,
	ManagerPnpm:     80,
	ManagerYarn:     75,
	ManagerBun:      74,
	ManagerNpm:      70,
	ManagerUv:       60,
	ManagerPoetry:   55,
	ManagerPipenv:   50,
	ManagerPip:      40,
	ManagerComposer: 30,
	ManagerGradle:   20,
	ManagerMaven:    10,
}

var defaultSkipDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	"target":       true,
	"dist":         true,
	"build":        true,
	"out":          true,
	"bin":          true,
	"obj":          true,
	"coverage":     true,
	"__pycache__":  true,
	".eggs":        true,
	".tox":         true,
	".venv":        true,
	"venv":         true,
	"env":          true,
	".cache":       true,
	".next":        true,
	".nuxt":        true,
	".svelte-kit":  true,
	"htmlcov":      true,
}

func managersOf(lockfiles []Lockfile) []Manager {
	seen := make(map[Manager]bool)
	var out []Manager
	for _, lf := range lockfiles {
		if lf.Manager == ManagerUnknown || seen[lf.Manager] {
			continue
		}
		seen[lf.Manager] = true
		out = append(out, lf.Manager)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func selectPrimary(managers []Manager) Manager {
	if len(managers) == 0 {
		return ManagerUnknown
	}
	best := managers[0]
	bestRank := managerRank[best]
	for _, m := range managers[1:] {
		if r := managerRank[m]; r > bestRank {
			best, bestRank = m, r
		}
	}
	return best
}

func dedupeLockfiles(lfs []Lockfile) []Lockfile {
	seen := make(map[string]bool)
	out := make([]Lockfile, 0, len(lfs))
	for _, lf := range lfs {
		key := lf.Path + "|" + string(lf.Manager)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, lf)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Manager < out[j].Manager
	})
	return out
}

func isNodeManager(m Manager) bool {
	return m == ManagerNpm || m == ManagerPnpm || m == ManagerYarn || m == ManagerBun
}

func readJSONScripts(path string) map[string]string {
	data, err := readCap(path, 1<<20)
	if err != nil {
		return nil
	}
	var doc struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &doc); err != nil || doc.Scripts == nil {
		return nil
	}
	return doc.Scripts
}

func readGoModule(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")), true
		}
	}
	return "", false
}

func readCap(path string, max int64) ([]byte, error) {
	if max <= 0 {
		max = 256 * 1024
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		data = data[:max]
	}
	return data, nil
}

func firstLines(content string, max int) string {
	if max <= 0 {
		return ""
	}
	lines := strings.SplitN(content, "\n", max+1)
	n := len(lines)
	if n > max {
		n = max
	}
	return strings.Join(lines[:n], "\n")
}
