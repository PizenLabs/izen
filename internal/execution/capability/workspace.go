package capability

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// workspaceIgnoreDirs are directories a bounded workspace scan never enters.
// They are dependency trees, build output and runtime metadata: none of them
// are authored source, and entering them is what makes a three-level scan
// expensive.
var workspaceIgnoreDirs = []string{
	".git", ".hg", ".svn", ".izen", "node_modules", "vendor",
	"dist", "build", "out", "target", "__pycache__", ".venv", "venv",
	".next", ".nuxt", ".cache", ".pytest_cache", ".mypy_cache", ".gradle",
	".idea", ".vscode", "coverage", "bin", "obj",
}

// discoveryDepth bounds how deep WorkspaceDiscover walks. Depth 1 is a file at
// the workspace root.
const discoveryDepth = 4

// maxDiscoveryFiles bounds how many regular files one discovery pass records.
// Reaching the bound sets Truncated, so the profile is honestly reported as
// evidence of a SUBSET rather than of the whole workspace.
const maxDiscoveryFiles = 2000

// maxReadBytes bounds a single FileRead result.
const maxReadBytes = 256 * 1024

// maxSearchResults bounds a single FileSearch result set.
const maxSearchResults = 50

// maxSearchScanned bounds how many files one search pass opens.
const maxSearchScanned = 4000

// manifestExtensions maps a file extension onto the toolchain manifest kind it
// evidences. The mapping is extension-based on PURPOSE: a manifest is identified
// by what it IS (a JSON dependency graph, a module graph), and its CONTENT is
// validated before it is trusted. A file that merely happens to be named
// go.mod never grants a capability, because the JSON/module parse is what
// admits it.
var manifestExtensions = map[string]string{
	".json": "json_manifest",
	".mod":  "module_manifest",
	".toml": "toml_manifest",
	".lock": "lock_manifest",
	".yaml": "yaml_manifest",
	".yml":  "yaml_manifest",
}

// FileFact is one observed workspace file. Every field is something the scan
// read; nothing is inferred about what the file MEANS to an objective.
type FileFact struct {
	// Path is the workspace-relative, slash-separated path.
	Path string `json:"path"`
	// Depth is the path depth the scan reached this file at.
	Depth int `json:"depth"`
	// Bytes is the size on disk.
	Bytes int64 `json:"bytes"`
	// Ext is the lowercased extension including the dot ("" when none).
	Ext string `json:"ext,omitempty"`
	// Text marks a file the scan decoded as UTF-8 text (and not binary).
	Text bool `json:"text"`
}

// Profile is the complete structured output of one WorkspaceDiscover pass.
//
// It is EVIDENCE. It is never authority: no field in it says what may be
// mutated. The one and only path from a Profile to a mutation target is the
// existing target resolver plus the existing authorization gate.
type Profile struct {
	Root string `json:"root"`
	// MaxDepth is the depth bound the scan honoured.
	MaxDepth int `json:"max_depth"`
	// Files are the bounded, deterministically ordered regular files observed.
	Files []FileFact `json:"files"`
	// Manifests are the files whose CONTENT validated as a toolchain manifest,
	// keyed by the toolchain kind they evidence.
	Manifests []ManifestFact `json:"manifests,omitempty"`
	// Toolchains are the toolchains the validated manifests prove are present,
	// in canonical order.
	Toolchains []string `json:"toolchains,omitempty"`
	// Entry is the discovered entry document, or nil when the workspace has no
	// single defensible entry document. It is a CANDIDATE derived from
	// structural evidence (the document that a runtime would actually serve and
	// that actually references further resources), never from a filename map.
	Entry *EntryDoc `json:"entry,omitempty"`
	// EntryCandidates lists every document that scored as a possible entry when
	// more than one did. Non-empty EntryCandidates with a nil Entry is a
	// TARGET_UNCERTAIN condition, not a silent choice.
	EntryCandidates []EntryCandidate `json:"entry_candidates,omitempty"`
	// FilesScanned counts the regular files the scan visited.
	FilesScanned int `json:"files_scanned"`
	// Truncated reports that a bound was reached, so the profile is evidence of
	// a subset of the workspace.
	Truncated bool `json:"truncated"`
}

// ManifestFact is one validated toolchain manifest.
type ManifestFact struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	Toolchain string `json:"toolchain"`
	// Scripts names the runnable scripts a node manifest declares, when it
	// declares any. This is what "detect scripts" means: read them off the
	// manifest, never guess them.
	Scripts []string `json:"scripts,omitempty"`
	// Tasks names the declared tasks of a makefile-style manifest.
	Tasks []string `json:"tasks,omitempty"`
}

// EntryDoc is the discovered runnable entry document of a workspace.
type EntryDoc struct {
	// Path is the workspace-relative document path.
	Path string `json:"path"`
	// Kind names the runtime shape the document implies ("static_document").
	Kind string `json:"kind"`
	// References counts the local subresources the document actually
	// references. It is the structural reason this document was chosen: an
	// entry document is the one the runtime serves and that pulls in more.
	References int `json:"references"`
	// Serveable reports whether the document can be served as-is (it declares a
	// document structure rather than being a fragment).
	Serveable bool `json:"serveable"`
}

// EntryCandidate is one scored entry-document candidate, retained so an
// ambiguous entry is reported with its alternatives instead of silently
// resolved.
type EntryCandidate struct {
	Path       string `json:"path"`
	Depth      int    `json:"depth"`
	References int    `json:"references"`
	Serveable  bool   `json:"serveable"`
}

// Toolchains returns a defensive copy of the validated toolchain list.
func (p Profile) ToolchainSet() map[string]bool {
	out := make(map[string]bool, len(p.Toolchains))
	for _, t := range p.Toolchains {
		out[t] = true
	}
	return out
}

// Paths projects the observed file list.
func (p Profile) Paths() []string {
	out := make([]string, 0, len(p.Files))
	for _, f := range p.Files {
		out = append(out, f.Path)
	}
	return out
}

// Discover enumerates the workspace and derives its shape from what is on disk.
//
// It reads directory entries and file bytes and writes nothing. Every claim it
// makes is checkable against the filesystem: the file list, the validated
// manifests, and the entry document are all re-derivable by the reader.
func (r *Runner) Discover(_ context.Context) (Profile, Evidence, error) {
	profile := Profile{Root: r.root, MaxDepth: discoveryDepth}
	ev := Evidence{ID: "ws.discover", Capability: WorkspaceDiscover, OK: true, Fields: map[string]string{}}
	if err := r.authorize(WorkspaceDiscover); err != nil {
		profile.Root = ""
		return profile, Refuse(WorkspaceDiscover, err), err
	}
	if strings.TrimSpace(r.root) == "" {
		return r.failedDiscover(profile, ev, FailureCapabilityFailed, "no workspace root is bound to the capability runtime")
	}
	root, err := filepath.Abs(r.root)
	if err != nil {
		return r.failedDiscover(profile, ev, FailureCapabilityFailed, "workspace root cannot be resolved: "+err.Error())
	}
	profile.Root = root

	skip := make(map[string]bool, len(workspaceIgnoreDirs))
	for _, d := range workspaceIgnoreDirs {
		skip[d] = true
	}

	walkErr := filepath.WalkDir(root, func(p string, entry os.DirEntry, err error) error {
		if err != nil {
			// An entry the walk cannot read is coverage this pass did NOT get, so
			// it is marked truncated and skipped rather than aborting the scan.
			profile.Truncated = true
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil //nolint:nilerr // truncation is recorded; the scan continues
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil || rel == "." {
			// A path that cannot be expressed relative to the root is outside the
			// workspace by definition, so it is out of scope rather than an error.
			return nil //nolint:nilerr // out-of-scope path, not a scan failure
		}
		rel = filepath.ToSlash(rel)
		depth := strings.Count(rel, "/") + 1
		if entry.IsDir() {
			if skip[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if depth > profile.MaxDepth {
			return nil
		}
		if skip[path.Base(rel)] {
			return nil
		}
		info, statErr := entry.Info()
		if statErr != nil || !info.Mode().IsRegular() {
			// A symlink, socket or unreadable entry is not a candidate file, and
			// the fact that it was seen but not counted is itself coverage.
			profile.Truncated = true
			return nil //nolint:nilerr // non-regular entry recorded as truncated coverage
		}
		if profile.FilesScanned >= maxDiscoveryFiles {
			profile.Truncated = true
			return filepath.SkipAll
		}
		profile.FilesScanned++
		fact := FileFact{
			Path:  rel,
			Depth: depth,
			Bytes: info.Size(),
			Ext:   strings.ToLower(filepath.Ext(rel)),
		}
		if fact.Bytes <= maxReadBytes {
			fact.Text = isTextFile(filepath.Join(root, filepath.FromSlash(rel)))
		}
		profile.Files = append(profile.Files, fact)
		return nil
	})
	if walkErr != nil {
		profile.Truncated = true
	}
	// Deterministic order: discovery evidence must not depend on directory
	// iteration order, or the same workspace yields different evidence on
	// different machines and an ambiguity report becomes irreproducible.
	sort.Slice(profile.Files, func(i, j int) bool { return profile.Files[i].Path < profile.Files[j].Path })

	r.deriveManifests(&profile, root)
	r.deriveEntry(&profile, root)

	ev.Summary = fmt.Sprintf("discovered %d file(s); toolchains=%s; entry=%s",
		len(profile.Files), joinOrNone(profile.Toolchains, "none"), entryPath(profile.Entry))
	if profile.Truncated {
		ev.Summary += " (truncated)"
	}
	ev.Fields["files"] = strconv.Itoa(len(profile.Files))
	ev.Fields["files_scanned"] = strconv.Itoa(profile.FilesScanned)
	ev.Fields["truncated"] = strconv.FormatBool(profile.Truncated)
	ev.Fields["root"] = root
	if profile.Entry != nil {
		ev.Fields["entry"] = profile.Entry.Path
		ev.Fields["entry_references"] = strconv.Itoa(profile.Entry.References)
	}
	if len(profile.Toolchains) > 0 {
		ev.Fields["toolchains"] = strings.Join(profile.Toolchains, ",")
	}
	return profile, ev, nil
}

// failedDiscover returns a blocked discover record. Discovery failing is not an
// empty workspace: it is an explicit, classified capability failure, so a caller
// can never mistake "I could not look" for "there is nothing there".
func (r *Runner) failedDiscover(profile Profile, ev Evidence, class FailureClass, reason string) (Profile, Evidence, error) {
	ev.OK = false
	ev.Class = class
	ev.Summary = reason
	ev.Detail = reason
	return profile, ev, fmt.Errorf("capability %s: %s", WorkspaceDiscover, reason)
}

// deriveManifests validates candidate manifests by CONTENT and records the
// toolchain each one actually proves. A manifest is never trusted because of
// its name: the parse is the admission.
func (r *Runner) deriveManifests(p *Profile, root string) {
	toolchains := map[string]bool{}
	for _, f := range p.Files {
		kind, ok := manifestExtensions[f.Ext]
		if !ok || !f.Text {
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(f.Path))
		switch kind {
		case "json_manifest":
			tc, scripts := validateJSONManifest(abs)
			if tc == "" {
				continue
			}
			p.Manifests = append(p.Manifests, ManifestFact{Path: f.Path, Kind: kind, Toolchain: tc, Scripts: scripts})
			toolchains[tc] = true
		case "module_manifest":
			if !validateModuleManifest(abs) {
				continue
			}
			p.Manifests = append(p.Manifests, ManifestFact{Path: f.Path, Kind: kind, Toolchain: "go"})
			toolchains["go"] = true
		case "toml_manifest":
			tc := validateTOMLManifest(abs)
			if tc == "" {
				continue
			}
			p.Manifests = append(p.Manifests, ManifestFact{Path: f.Path, Kind: kind, Toolchain: tc})
			toolchains[tc] = true
		case "yaml_manifest":
			p.Manifests = append(p.Manifests, ManifestFact{Path: f.Path, Kind: kind, Toolchain: "yaml"})
			toolchains["yaml"] = true
		case "lock_manifest":
			p.Manifests = append(p.Manifests, ManifestFact{Path: f.Path, Kind: kind, Toolchain: "lock"})
			toolchains["lock"] = true
		}
	}
	// A makefile has no extension; its tasks are real declared evidence of
	// runnable targets, so it is validated on content like every other manifest.
	if tc, tasks := validateMakefile(filepath.Join(root, "Makefile")); tc != "" {
		p.Manifests = append(p.Manifests, ManifestFact{Path: "Makefile", Kind: "task_manifest", Toolchain: tc, Tasks: tasks})
		toolchains[tc] = true
	}
	sort.Slice(p.Manifests, func(i, j int) bool { return p.Manifests[i].Path < p.Manifests[j].Path })
	for tc := range toolchains {
		p.Toolchains = append(p.Toolchains, tc)
	}
	sort.Strings(p.Toolchains)
}

// validateJSONManifest admits a JSON document as a node manifest only when it
// actually declares the fields that make one a dependency/build graph. A JSON
// file with an unrelated shape is not a manifest.
func validateJSONManifest(abs string) (toolchain string, scripts []string) {
	data, err := os.ReadFile(abs)
	if err != nil || len(data) > maxReadBytes {
		return "", nil
	}
	lower := strings.ToLower(string(data))
	isPackage := strings.Contains(lower, "\"dependencies\"") ||
		strings.Contains(lower, "\"devdependencies\"") ||
		strings.Contains(lower, "\"scripts\"")
	if !isPackage {
		return "", nil
	}
	var doc struct {
		Name    string            `json:"name"`
		Scripts map[string]string `json:"scripts"`
	}
	if err := jsonUnmarshal(data, &doc); err != nil {
		return "", nil
	}
	names := make([]string, 0, len(doc.Scripts))
	for k := range doc.Scripts {
		names = append(names, k)
	}
	sort.Strings(names)
	return "node", names
}

// validateModuleManifest admits a module manifest only when it declares a
// module path. That single line is the whole proof.
func validateModuleManifest(abs string) bool {
	f, err := os.Open(abs)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "module ") && len(strings.TrimSpace(strings.TrimPrefix(line, "module "))) > 0 {
			return true
		}
	}
	return false
}

// validateTOMLManifest admits a TOML manifest when it declares a section that
// only a real toolchain manifest carries.
func validateTOMLManifest(abs string) string {
	data, err := os.ReadFile(abs)
	if err != nil || len(data) > maxReadBytes {
		return ""
	}
	lower := strings.ToLower(string(data))
	switch {
	case strings.Contains(lower, "[package]"):
		return "rust"
	case strings.Contains(lower, "[project]"):
		return "python"
	case strings.Contains(lower, "[build-system]"):
		return "python"
	case strings.Contains(lower, "[tool.poetry]"):
		return "python"
	default:
		return ""
	}
}

// validateMakefile admits a task manifest only when it declares at least one
// real target, and reports which targets it declares.
func validateMakefile(abs string) (toolchain string, tasks []string) {
	f, err := os.Open(abs)
	if err != nil {
		return "", nil
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "\t") || strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, _, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" || strings.ContainsAny(name, " \t=") || strings.HasSuffix(name, "=") {
			continue
		}
		tasks = append(tasks, name)
	}
	if len(tasks) == 0 {
		return "", nil
	}
	sort.Strings(tasks)
	if len(tasks) > 32 {
		tasks = tasks[:32]
	}
	return "make", tasks
}

// deriveEntry selects the workspace's entry document from STRUCTURAL evidence.
//
// The rule is deliberately not a filename map. A document is a candidate when
// its bytes parse as a document (it declares a document element) rather than a
// fragment, and it is scored by how many local subresources it actually
// references. The runtime serves one document and that document pulls in more,
// so the document with the most real outbound references is the entry; a
// fragment that nothing references is not.
//
// Ties are resolved by DEPTH and only then lexicographically — never by a
// filename convention. Depth is a real structural property: the document a
// runtime serves sits at the workspace root, and a second, deeper document that
// references exactly as much is a page the root page links to rather than the
// page a runtime would open. When depth ALSO ties, there is genuinely no
// evidence to separate the candidates and they are all reported as candidates.
func (r *Runner) deriveEntry(p *Profile, root string) {
	candidates := make([]EntryCandidate, 0, 8)
	for _, f := range p.Files {
		if f.Depth > 2 || !f.Text {
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(f.Path))
		data, err := os.ReadFile(abs)
		if err != nil || len(data) > maxReadBytes {
			continue
		}
		body := string(data)
		if !looksLikeDocument(body) {
			continue
		}
		candidates = append(candidates, EntryCandidate{
			Path:       f.Path,
			Depth:      f.Depth,
			References: countSubresourceRefs(body),
			Serveable:  true,
		})
	}
	if len(candidates) == 0 {
		return
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.References != b.References {
			return a.References > b.References
		}
		if a.Depth != b.Depth {
			return a.Depth < b.Depth
		}
		return a.Path < b.Path
	})
	top := candidates[0]
	for _, c := range candidates[1:] {
		if c.References == top.References && c.Depth == top.Depth {
			// Nothing in the evidence separates these. Reporting all of them is
			// the truthful answer; picking one would be exactly the guess this
			// runtime must not make.
			p.EntryCandidates = append(p.EntryCandidates, candidates...)
			return
		}
	}
	p.Entry = &EntryDoc{
		Path:       top.Path,
		Kind:       "static_document",
		References: top.References,
		Serveable:  top.Serveable,
	}
}

// looksLikeDocument reports whether body declares a document structure. It
// checks for the document declaration itself, not for a filename.
func looksLikeDocument(body string) bool {
	lower := strings.ToLower(body)
	if !strings.Contains(lower, "<html") && !strings.Contains(lower, "<!doctype html") {
		return false
	}
	return strings.Contains(lower, "<head") || strings.Contains(lower, "<body")
}

// isTextFile reports whether a file decodes as UTF-8 text. A NUL byte in the
// leading block is the binary marker; nothing else is read.
func isTextFile(abs string) bool {
	f, err := os.Open(abs)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 512)
	n, _ := f.Read(head)
	for _, c := range head[:n] {
		if c == 0 {
			return false
		}
	}
	return true
}

func entryPath(e *EntryDoc) string {
	if e == nil {
		return "none"
	}
	return e.Path
}

func joinOrNone(in []string, none string) string {
	if len(in) == 0 {
		return none
	}
	return strings.Join(in, ",")
}
