package execution

// ── ISOLATED WORKSPACE DISCOVERY (Phase 16.1) ──────────────────────────────
//
// Discovery and Resolution are DIFFERENT QUESTIONS and this file owns exactly
// one of them:
//
//	Resolution (resolver.go):  "is the target the caller stated a file?" — a
//	                           pure path-level question answered with os.Stat.
//
//	Discovery  (this file):    "what does the workspace contain?" — a bounded,
//	                           read-only scan that produces EVIDENCE.
//
// The distinction is I13 (Evidence ≠ Authority). Discovery may produce
// `WorkspaceEvidence`; it may NEVER produce a `MutationTarget` and it may NEVER
// authorize a filesystem mutation. A scan result is a fact about the
// repository, not a decision about the objective. Collapsing the two is how a
// "refactor the project" request silently becomes a write to whichever file the
// scan happened to return first.
//
// A WorkspaceProfile therefore carries candidate EVIDENCE tagged with its kind.
// A candidate is injected into the context (what the model may be shown) and
// nothing else. The one and only path from a candidate to a mutation target is
// an explicit human/resolver decision, which happens in resolver.go — never
// here.

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// EvidenceKind is the closed vocabulary of evidence a discovery pass may
// produce. It exists so a candidate can never be mistaken for an authority: a
// value in this vocabulary describes WHERE a path came from, never what the
// runtime is permitted to do with it.
type EvidenceKind string

const (
	// EvidenceKindManifest: a dependency/build manifest (go.mod, package.json…).
	EvidenceKindManifest EvidenceKind = "MANIFEST"
	// EvidenceKindSourceRoot: a directory that carries authored source.
	EvidenceKindSourceRoot EvidenceKind = "SOURCE_ROOT"
	// EvidenceKindCandidate: a regular file the bounded scan observed. It is
	// CONTEXT, never a MutationTarget.
	EvidenceKindCandidate EvidenceKind = "CANDIDATE"
)

// CandidateEvidence is one observed workspace file. Kind is pinned to
// EvidenceKindCandidate by construction, and that pinning is the point: an
// artifact of discovery that could carry a mutation kind would be authority
// wearing evidence's clothes.
type CandidateEvidence struct {
	// Path is the workspace-relative, slash-separated path.
	Path string `json:"path"`
	// Kind is always EvidenceKindCandidate.
	Kind EvidenceKind `json:"kind"`
	// Depth is the path depth the scan reached this file at.
	Depth int `json:"depth"`
	// Digest is the SHA-256 of the file's bytes when readable.
	Digest string `json:"digest,omitempty"`
}

// ManifestEvidence is a discovered build/dependency manifest. It is evidence
// about the SHAPE of the workspace (what language/build system it is), never
// about which file an objective may mutate.
type ManifestEvidence struct {
	Path   string       `json:"path"`
	Kind   EvidenceKind `json:"kind"`
	Digest string       `json:"digest,omitempty"`
}

// SourceRoot is a discovered authored-source directory. It scopes the CONTEXT
// of a mutation (where relevant code lives) and nothing more.
type SourceRoot struct {
	Path     string       `json:"path"`
	Kind     EvidenceKind `json:"kind"`
	Manifest string       `json:"manifest,omitempty"`
}

// WorkspaceProfile is the complete, structured output of one bounded discovery
// pass. It is INPUT TO CONTEXT ASSEMBLY — never input to execution authority.
type WorkspaceProfile struct {
	// Root is the workspace root the scan was rooted at.
	Root string `json:"root"`
	// MaxDepth is the depth bound the scan honoured.
	MaxDepth int `json:"max_depth"`
	// Manifests are discovered build/dependency manifests.
	Manifests []ManifestEvidence `json:"manifests,omitempty"`
	// SourceRoots are discovered authored-source directories.
	SourceRoots []SourceRoot `json:"source_roots,omitempty"`
	// Candidates are the bounded, deterministic (lexicographically ordered)
	// workspace-relative regular files the scan observed.
	Candidates []CandidateEvidence `json:"candidates,omitempty"`
	// FilesScanned is how many regular files the scan visited.
	FilesScanned int `json:"files_scanned"`
	// Truncated reports whether the candidate cap or an unreadable entry was
	// reached, which means the profile is evidence of a SUBSET.
	Truncated bool `json:"truncated"`
	// IgnoreRules names the deny rules applied, so an empty candidate set is
	// never mistaken for an empty workspace.
	IgnoreRules []string `json:"ignore_rules,omitempty"`
}

// CandidatePaths projects the candidate set as the string list the resolution
// and evidence surfaces consume.
func (p WorkspaceProfile) CandidatePaths() []string {
	if len(p.Candidates) == 0 {
		return nil
	}
	out := make([]string, 0, len(p.Candidates))
	for _, c := range p.Candidates {
		out = append(out, c.Path)
	}
	return out
}

// HasManifest reports whether a manifest with the given basename was discovered.
func (p WorkspaceProfile) HasManifest(name string) bool {
	name = strings.TrimSpace(name)
	for _, m := range p.Manifests {
		if path.Base(m.Path) == name {
			return true
		}
	}
	return false
}

// Evidence projects the profile onto the compact WorkspaceEvidence record the
// resolution verdict and the UI carry. The two are different views of one
// scan; neither is authority.
func (p WorkspaceProfile) Evidence() WorkspaceEvidence {
	return WorkspaceEvidence{
		Root:         p.Root,
		MaxDepth:     p.MaxDepth,
		Candidates:   p.CandidatePaths(),
		FilesScanned: p.FilesScanned,
		Truncated:    p.Truncated,
		IgnoreRules:  append([]string(nil), p.IgnoreRules...),
	}
}

// WorkspaceEvidence is the STRUCTURED record of one bounded discovery pass. It
// exists so that an UNRESOLVED result is still useful: the human who is asked
// to disambiguate sees the exact candidate set the runtime looked at, rather
// than being told "the workspace was ambiguous" and left to guess.
type WorkspaceEvidence struct {
	// Root is the workspace root the scan was rooted at.
	Root string `json:"root"`
	// MaxDepth is the depth bound the scan honoured.
	MaxDepth int `json:"max_depth"`
	// Candidates are the bounded, deterministic (lexicographically ordered)
	// workspace-relative files the scan observed.
	Candidates []string `json:"candidates"`
	// FilesScanned is how many regular files the scan visited.
	FilesScanned int `json:"files_scanned"`
	// Truncated reports whether the candidate cap was reached, which means the
	// candidate list is evidence of a subset and must be presented as such.
	Truncated bool `json:"truncated"`
	// IgnoreRules names the deny rules applied, so an empty candidate set is
	// never mistaken for an empty workspace.
	IgnoreRules []string `json:"ignore_rules"`
}

// ResolverMaxDepth is the deterministic depth bound of workspace discovery.
// Depth 1 is a file at the workspace root, so D=3 reaches src/a/b/file without
// ever crawling a vendored tree.
const ResolverMaxDepth = 3

// maxDiscoveryCandidates bounds how many candidate paths one discovery pass may
// return. A truncated candidate list is still evidence — it is reported as
// truncated — but it is not a claim to have seen the whole workspace.
const maxDiscoveryCandidates = 512

// errDiscoverySkipped is the control signal one discovery walk step returns to
// say "this entry is not a candidate and I could not have said more about it".
// It exists so the skip is a NAMED outcome rather than a swallowed error: a
// discovery pass that silently drops entries and one that deliberately skips
// them look identical in the return value, and only the second one is
// trustworthy.
var errDiscoverySkipped = errors.New("execution: target discovery skipped an entry")

// defaultIgnoreDirs are the directories a bounded discovery pass never enters.
// They are build output, dependency trees and runtime metadata: none of them
// are plausible user-authored mutation targets, and entering them is what makes
// a three-level scan expensive.
var defaultIgnoreDirs = []string{
	".git", ".hg", ".svn", ".izen", "node_modules", "vendor",
	"dist", "build", "out", "target", "__pycache__", ".venv", "venv",
	".next", ".nuxt", ".cache", ".pytest_cache", ".mypy_cache", ".gradle",
	".idea", ".vscode", "coverage", "bin", "obj",
}

// manifestNames are the well-known build/dependency manifests. Their presence
// is evidence of the workspace's build system — it never names a mutation
// target.
var manifestNames = map[string]bool{
	"go.mod": true, "go.sum": true,
	"package.json": true, "package-lock.json": true, "pnpm-lock.yaml": true, "yarn.lock": true,
	"Cargo.toml": true, "Cargo.lock": true,
	"pyproject.toml": true, "requirements.txt": true, "setup.py": true, "Pipfile": true,
	"pom.xml": true, "build.gradle": true, "build.gradle.kts": true, "settings.gradle": true,
	"composer.json": true, "Gemfile": true, "mix.exs": true, "CMakeLists.txt": true,
	"Makefile": true, "Dockerfile": true, "BUILD": true, "BUILD.bazel": true,
}

// sourceRootNames are the conventional authored-source directories. A directory
// carrying any of these names (or a manifest-less directory that holds sources)
// is recorded as source-root evidence.
var sourceRootNames = map[string]bool{
	"src": true, "lib": true, "internal": true, "pkg": true, "cmd": true,
	"app": true, "apps": true, "packages": true, "source": true, "sources": true,
}

// WorkspaceDiscovery performs the bounded, read-only workspace scan. It is
// deliberately SEPARATE from TargetResolver: it owns no target authority, it
// answers no question about what the runtime may mutate, and it cannot be
// reached by a caller looking for a target. Its only output is evidence.
type WorkspaceDiscovery struct {
	root     string
	maxDepth int
	// stat and readFile are injected so discovery is testable without a
	// filesystem and so a test can prove a read never mutates anything.
	stat     func(string) (fs.FileInfo, error)
	readFile func(string) ([]byte, error)
	// ignoreRules is the effective deny set for this discovery pass.
	ignoreRules []ignoreRule
}

// NewWorkspaceDiscovery returns a discovery pass rooted at root with the
// bounded depth and the workspace's own .gitignore rules applied.
func NewWorkspaceDiscovery(root string) *WorkspaceDiscovery {
	d := &WorkspaceDiscovery{
		root:     root,
		maxDepth: ResolverMaxDepth,
		stat:     os.Stat,
		readFile: os.ReadFile,
	}
	d.ignoreRules = buildIgnoreRules(root, defaultIgnoreDirs)
	return d
}

// Root returns the workspace root the discovery pass is bound to.
func (d *WorkspaceDiscovery) Root() string {
	if d == nil {
		return ""
	}
	return d.root
}

// WithMaxDepth overrides the discovery depth bound. A non-positive value is
// ignored: the bound is a safety property, and a caller cannot remove it.
func (d *WorkspaceDiscovery) WithMaxDepth(depth int) *WorkspaceDiscovery {
	if d == nil {
		return nil
	}
	if depth > 0 && depth < d.maxDepth {
		d.maxDepth = depth
	}
	return d
}

// Discover runs the bounded scan and returns the structured workspace profile.
// It reads directory entries and, for candidates, file bytes (for the digest);
// it writes nothing, mutates nothing, and grants nothing.
func (d *WorkspaceDiscovery) Discover() WorkspaceProfile {
	profile := WorkspaceProfile{
		Root:        d.root,
		MaxDepth:    d.maxDepth,
		IgnoreRules: ignoreRuleNames(d.ignoreRules),
	}
	if d == nil || strings.TrimSpace(d.root) == "" {
		return profile
	}
	root := filepath.Clean(d.root)
	sourceRoots := map[string]bool{}
	walkErr := filepath.WalkDir(root, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			// An entry we cannot read is evidence we did NOT see it. Skipping it
			// without recording that would overstate the coverage this record
			// claims, so the truncated flag is set and the walk is told to
			// continue over the entry's siblings.
			profile.Truncated = true
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return errDiscoverySkipped
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			// A path the walk produced that cannot be expressed relative to the
			// root is outside the workspace by definition. Skipping it is the
			// containment rule, not an error to report.
			return fs.SkipDir
		}
		if rel == "." {
			return nil // the workspace root itself
		}
		rel = filepath.ToSlash(rel)
		depth := strings.Count(rel, "/") + 1
		if entry.IsDir() {
			if d.ignored(rel, true) {
				return fs.SkipDir
			}
			if depth <= d.maxDepth && sourceRootNames[path.Base(rel)] {
				sourceRoots[rel] = true
			}
			return nil
		}
		if depth > d.maxDepth {
			return nil
		}
		if d.ignored(rel, false) {
			return nil
		}
		if !entry.Type().IsRegular() {
			info, statErr := entry.Info()
			if statErr != nil || !info.Mode().IsRegular() {
				profile.Truncated = true
				return errDiscoverySkipped
			}
		}
		profile.FilesScanned++
		if len(profile.Candidates) >= maxDiscoveryCandidates {
			profile.Truncated = true
			return fs.SkipAll
		}
		candidate := CandidateEvidence{
			Path:  path.Clean(rel),
			Kind:  EvidenceKindCandidate,
			Depth: depth,
		}
		if d.readFile != nil {
			if data, readErr := d.readFile(filepath.Join(root, filepath.FromSlash(rel))); readErr == nil {
				candidate.Digest = sourceSHA256(data)
			}
		}
		profile.Candidates = append(profile.Candidates, candidate)
		if manifestNames[path.Base(rel)] {
			profile.Manifests = append(profile.Manifests, ManifestEvidence{
				Path:   candidate.Path,
				Kind:   EvidenceKindManifest,
				Digest: candidate.Digest,
			})
		}
		return nil
	})
	// errDiscoverySkipped is a control signal, not a failure: an unreadable or
	// non-regular entry was already recorded as truncated coverage. Any OTHER
	// walk error is a real discovery failure and must mark the evidence
	// incomplete rather than be reported as a clean scan.
	if walkErr != nil && !errors.Is(walkErr, errDiscoverySkipped) {
		profile.Truncated = true
	}
	// Deterministic order: discovery evidence must not depend on directory
	// iteration order, or the same workspace yields different candidate lists
	// on different machines and an ambiguity report becomes unreproducible.
	sort.Slice(profile.Candidates, func(i, j int) bool {
		return profile.Candidates[i].Path < profile.Candidates[j].Path
	})
	sort.Slice(profile.Manifests, func(i, j int) bool {
		return profile.Manifests[i].Path < profile.Manifests[j].Path
	})
	for rootPath := range sourceRoots {
		profile.SourceRoots = append(profile.SourceRoots, SourceRoot{
			Path: rootPath,
			Kind: EvidenceKindSourceRoot,
		})
	}
	sort.Slice(profile.SourceRoots, func(i, j int) bool {
		return profile.SourceRoots[i].Path < profile.SourceRoots[j].Path
	})
	return profile
}

// ignored reports whether rel is excluded by the discovery deny rules. The
// LAST matching rule wins, which is the .gitignore semantic negation models
// rely on ("ignore everything, then re-include this one file").
func (d *WorkspaceDiscovery) ignored(rel string, isDir bool) bool {
	ignored := false
	for _, rule := range d.ignoreRules {
		if rule.match(rel, isDir) {
			ignored = !rule.negate
		}
	}
	return ignored
}

// ignoreRule is one deterministic .gitignore-shaped deny rule.
type ignoreRule struct {
	pattern  string
	dirOnly  bool
	anchored bool
	negate   bool
}

// match implements one .gitignore-shaped rule against a slash-separated
// workspace-relative path. It supports the subset that actually governs which
// files are plausible targets: anchored patterns, directory-only patterns, a
// trailing "*" wildcard and "**" for any depth. Anything more exotic is left
// unmatched rather than approximated, because an approximated ignore rule
// silently narrows the evidence.
func (rule ignoreRule) match(rel string, isDir bool) bool {
	if rule.dirOnly && !isDir {
		return false
	}
	candidate := rel
	if rule.anchored {
		if ok, _ := path.Match(rule.pattern, candidate); ok {
			return true
		}
		return pathPrefixDirMatch(rule.pattern, candidate)
	}
	for _, seg := range strings.Split(candidate, "/") {
		if ok, _ := path.Match(rule.pattern, seg); ok {
			return true
		}
	}
	return false
}

// pathPrefixDirMatch reports whether an anchored pattern names a directory
// prefix of rel.
func pathPrefixDirMatch(pattern, rel string) bool {
	if !strings.Contains(pattern, "/") {
		return false
	}
	dir := pattern
	if idx := strings.IndexAny(dir, "*?["); idx >= 0 {
		dir = dir[:idx]
	}
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" {
		return false
	}
	return rel == dir || strings.HasPrefix(rel, dir+"/")
}

// buildIgnoreRules assembles the effective deny set: the built-in directory
// denials followed by the workspace's own .gitignore, so a project that
// ignores "build/" never has its build output offered as a mutation target.
func buildIgnoreRules(root string, builtinDirs []string) []ignoreRule {
	rules := make([]ignoreRule, 0, len(builtinDirs)+16)
	for _, d := range builtinDirs {
		rules = append(rules, ignoreRule{pattern: d, dirOnly: true})
	}
	if root == "" {
		return rules
	}
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return rules
	}
	for _, line := range strings.Split(string(data), "\n") {
		rule, ok := parseIgnoreRule(line)
		if ok {
			rules = append(rules, rule)
		}
	}
	return rules
}

// parseIgnoreRule parses one .gitignore line. Blank lines and comments yield
// ok=false rather than an empty rule, so an empty pattern can never match
// everything by accident.
func parseIgnoreRule(line string) (ignoreRule, bool) {
	raw := strings.TrimSpace(line)
	if raw == "" || strings.HasPrefix(raw, "#") {
		return ignoreRule{}, false
	}
	rule := ignoreRule{pattern: raw}
	if strings.HasPrefix(rule.pattern, "!") {
		rule.negate = true
		rule.pattern = strings.TrimPrefix(rule.pattern, "!")
	}
	if strings.HasSuffix(rule.pattern, "/") {
		rule.dirOnly = true
		rule.pattern = strings.TrimSuffix(rule.pattern, "/")
	}
	if strings.HasPrefix(rule.pattern, "/") {
		rule.anchored = true
		rule.pattern = strings.TrimPrefix(rule.pattern, "/")
	} else if strings.Contains(rule.pattern, "/") {
		rule.anchored = true
	}
	rule.pattern = path.Clean(rule.pattern)
	if rule.pattern == "" || rule.pattern == "." {
		return ignoreRule{}, false
	}
	return rule, true
}

// ignoreRuleNames projects the effective deny set for the evidence record.
func ignoreRuleNames(rules []ignoreRule) []string {
	out := make([]string, 0, len(rules))
	for _, rule := range rules {
		name := rule.pattern
		if rule.dirOnly {
			name += "/"
		}
		if rule.negate {
			name = "!" + name
		}
		out = append(out, name)
	}
	return out
}
