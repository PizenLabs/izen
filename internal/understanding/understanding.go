package understanding

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Component is one meaningful structural component of the workspace
// (e.g. "go-module", "static-web", "node-app"). Components describe what
// EXISTS; they never describe what should be modified.
type Component struct {
	// Name is the stable component label (prefer coarse labels such as
	// "static-web" over fabricated framework precision).
	Name string `json:"name"`
	// Kind is the coarse component family ("backend", "frontend",
	// "static", "config", ...).
	Kind string `json:"kind"`
	// Paths are the representative repository paths evidencing it.
	Paths []string `json:"paths,omitempty"`
	// Evidence carries the supporting repository facts.
	Evidence []Evidence `json:"evidence,omitempty"`
}

// ProjectUnderstanding is the canonical, evidence-backed semantic
// representation of the current workspace. It describes what the
// repository IS, not what Izen intends to modify. It carries no
// authorization, no mutation operation, and no execution semantics.
type ProjectUnderstanding struct {
	// Root is the absolute workspace root the understanding was derived from.
	Root string `json:"root"`
	// Kind is the EXISTING / GREENFIELD / UNKNOWN classification.
	Kind ProjectKind `json:"kind"`
	// Identity is the coarse project label (e.g. "static-web",
	// "go-module", "node-app", "mixed", "empty", "unknown").
	Identity string `json:"identity"`
	// Languages lists detected source languages by extension evidence.
	Languages []string `json:"languages,omitempty"`
	// Components lists the meaningful structural components found.
	Components []Component `json:"components,omitempty"`
	// Evidence lists every repository fact supporting the understanding.
	Evidence []Evidence `json:"evidence,omitempty"`
	// StaticWeb carries static-web structural evidence when present.
	StaticWeb *StaticWebSurface `json:"static_web,omitempty"`
	// Confidence is the evidence weight score clamped to [0,1].
	Confidence float64 `json:"confidence"`
	// SnapshotID binds the understanding to one workspace state. Any
	// material repository change invalidates it (see IsStale).
	SnapshotID string `json:"snapshot_id"`
	// Digest is the content digest of the workspace file surface the
	// understanding was derived from.
	Digest string `json:"digest"`
	// FileCount is the number of files observed during derivation.
	FileCount int `json:"file_count"`
	// CreatedAt records when the understanding was derived.
	CreatedAt time.Time `json:"created_at"`
	// Unavailable reports a discovery failure: no repository truth could
	// be established. An unavailable understanding is UNKNOWN, never
	// GREENFIELD, and must not be consumed as current truth.
	Unavailable bool `json:"unavailable"`
	// UnavailableReason explains the discovery failure, if any.
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

// Valid reports whether the understanding is usable as current truth:
// available, classified, and bound to a snapshot.
func (u ProjectUnderstanding) Valid() bool {
	return !u.Unavailable && u.Kind.Valid() && u.SnapshotID != "" && u.Digest != ""
}

// IsStale reports whether the workspace at Root has materially changed
// since this understanding was derived. A stale understanding must never
// be presented as current truth; call Derive again to refresh. A
// derivation error conservatively reports stale.
func (u ProjectUnderstanding) IsStale() bool {
	if !u.Valid() {
		return true
	}
	current, err := digestWorkspace(u.Root)
	if err != nil {
		return true
	}
	return current != u.Digest
}

// ModelProposal is an UNVERIFIED model-derived claim about the project
// (e.g. "the project uses React"). It is hypothesis, never repository
// truth. It can be recorded alongside an understanding for planner
// context, but it can never silently mutate authoritative state.
type ModelProposal struct {
	// Claim is the model-derived statement.
	Claim string `json:"claim"`
	// Basis is the stated basis for the claim, if any.
	Basis string `json:"basis,omitempty"`
}

// ProposalDisposition records the outcome of considering a model proposal.
// Accepted is ALWAYS false: evidence-backed state is retained and the
// proposal is kept as hypothesis only.
type ProposalDisposition struct {
	// Accepted is always false — proposals never override evidence.
	Accepted bool `json:"accepted"`
	// Reason explains why evidence-backed state was retained.
	Reason string `json:"reason"`
}

// ConsiderProposal evaluates a model proposal against an evidence-backed
// understanding. The understanding is returned UNCHANGED; the proposal is
// retained as hypothesis only. This is the LLM boundary: model output is
// a proposal, repository evidence is the source of structural truth.
func ConsiderProposal(u ProjectUnderstanding, p ModelProposal) (ProjectUnderstanding, ProposalDisposition) {
	claim := strings.TrimSpace(p.Claim)
	if claim == "" {
		return u, ProposalDisposition{Accepted: false, Reason: "empty proposal carries no information; evidence-backed state retained"}
	}
	for _, e := range u.Evidence {
		if strings.Contains(strings.ToLower(claim), strings.ToLower(e.ID)) {
			return u, ProposalDisposition{Accepted: false, Reason: "proposal overlaps existing evidence but remains hypothesis; evidence-backed state retained unchanged"}
		}
	}
	return u, ProposalDisposition{Accepted: false, Reason: "model proposal retained as unverified hypothesis; repository evidence remains authoritative"}
}

// ─── derivation ─────────────────────────────────────────────────────────

// scanAcc accumulates the raw workspace surface during derivation.
type scanAcc struct {
	root      string
	files     []string
	dirs      []string
	rootFiles map[string]bool
	fileSizes map[string]int64
}

// manifestSignals maps root-level indicator files to (identity, language,
// weight). Only these well-known manifests can evidence EXISTING on their
// own; everything else needs corroborating structure.
var manifestSignals = map[string]struct {
	identity string
	language string
	weight   float64
}{
	"go.mod":           {"go-module", "Go", 1.0},
	"Cargo.toml":       {"rust-crate", "Rust", 1.0},
	"package.json":     {"node-app", "JavaScript", 0.9},
	"pyproject.toml":   {"python-project", "Python", 0.9},
	"setup.py":         {"python-project", "Python", 0.8},
	"requirements.txt": {"python-project", "Python", 0.7},
	"pom.xml":          {"java-project", "Java", 0.9},
	"build.gradle":     {"java-project", "Java", 0.9},
	"Gemfile":          {"ruby-project", "Ruby", 0.9},
	"composer.json":    {"php-project", "PHP", 0.9},
	"CMakeLists.txt":   {"cpp-project", "C++", 0.8},
	"Makefile":         {"make-project", "", 0.5},
	"Dockerfile":       {"containerized", "", 0.4},
}

// configSignals are recognized framework/build config files.
var configSignals = map[string]float64{
	"vite.config.js": 0.6, "vite.config.ts": 0.6,
	"next.config.js": 0.7, "next.config.mjs": 0.7, "next.config.ts": 0.7,
	"astro.config.mjs": 0.7, "astro.config.ts": 0.7,
	"tailwind.config.js": 0.5, "tailwind.config.ts": 0.5,
	"tsconfig.json": 0.5, "jsconfig.json": 0.4,
}

// trivialFiles may exist in a genuinely empty workspace without making it
// EXISTING (scaffolding/README/license/ignore files and VCS internals).
var trivialFiles = map[string]bool{
	"README.md": true, "README": true, "LICENSE": true, "LICENSE.md": true,
	"NOTICE": true, "CHANGELOG.md": true, ".gitignore": true,
	".gitattributes": true, ".editorconfig": true,
}

// skipDirs are never descended into during derivation.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, ".izen": true,
	".codebase-memory": true, "__pycache__": true, ".idea": true,
	"target": true, "dist": true, "build": true,
}

// extLanguages maps source extensions to language labels.
var extLanguages = map[string]string{
	".go": "Go", ".py": "Python", ".rs": "Rust",
	".ts": "TypeScript", ".mts": "TypeScript", ".cts": "TypeScript",
	".tsx": "TypeScript", ".js": "JavaScript", ".jsx": "JavaScript",
	".mjs": "JavaScript", ".cjs": "JavaScript",
	".java": "Java", ".kt": "Kotlin", ".cs": "C#",
	".cpp": "C++", ".cc": "C++", ".hpp": "C++", ".c": "C", ".h": "C",
	".rb": "Ruby", ".php": "PHP", ".swift": "Swift",
	".html": "HTML", ".htm": "HTML", ".css": "CSS",
	".scss": "CSS", ".less": "CSS", ".sql": "SQL",
}

// Derive builds the ProjectUnderstanding for root from repository evidence.
// It is deterministic, performs no model calls, and never fails closed
// into GREENFIELD: unreadable roots and discovery failures yield UNKNOWN
// with Unavailable set.
func Derive(root string) ProjectUnderstanding {
	now := time.Now()
	if strings.TrimSpace(root) == "" {
		return unavailable("", "empty workspace root", now)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return unavailable(root, "cannot resolve workspace root", now)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return unavailable(abs, "workspace root is not accessible", now)
	}

	acc := &scanAcc{root: abs, rootFiles: map[string]bool{}, fileSizes: map[string]int64{}}
	walkErr := filepath.Walk(abs, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return filepath.SkipDir
		}
		rel, rerr := filepath.Rel(abs, path)
		if rerr != nil || rel == "." {
			return nil //nolint:nilerr // best-effort scan, keep walking
		}
		rel = filepath.ToSlash(rel)
		if fi.IsDir() {
			base := filepath.Base(rel)
			if skipDirs[base] || strings.HasPrefix(base, ".") && base != ".github" {
				if base != ".github" {
					return filepath.SkipDir
				}
			}
			acc.dirs = append(acc.dirs, rel)
			if strings.Count(rel, "/") > 4 || len(acc.files) > 4096 {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(filepath.Base(rel), ".") && filepath.Dir(rel) == "." {
			// Hidden root dotfiles (except a few manifests) are not content.
			switch filepath.Base(rel) {
			case ".gitignore", ".gitattributes", ".editorconfig":
				acc.files = append(acc.files, rel)
			}
			return nil
		}
		acc.files = append(acc.files, rel)
		acc.fileSizes[rel] = fi.Size()
		if filepath.Dir(rel) == "." {
			acc.rootFiles[filepath.Base(rel)] = true
		}
		return nil //nolint:nilerr // best-effort scan, keep walking
	})
	if walkErr != nil {
		return unavailable(abs, "workspace walk failed", now)
	}

	digest, derr := digestWorkspace(abs)
	if derr != nil {
		return unavailable(abs, "workspace digest failed", now)
	}

	u := ProjectUnderstanding{Root: abs, CreatedAt: now, FileCount: len(acc.files), Digest: digest}
	u.SnapshotID = snapshotID(abs, digest, now)

	var evidence []Evidence
	langSet := map[string]bool{}
	langWeight := map[string]float64{}

	// 1. Manifests (strongest signals).
	identities := map[string]float64{}
	for name, sig := range manifestSignals {
		if acc.rootFiles[name] {
			evidence = append(evidence, Evidence{
				Kind: EvidenceManifest, ID: "manifest:" + name,
				Detail: "module/package manifest " + name + " present at workspace root",
				Weight: sig.weight,
			})
			identities[sig.identity] += sig.weight
			if sig.language != "" {
				langSet[sig.language] = true
				langWeight[sig.language] += sig.weight
			}
		}
	}

	// 2. Config files (any depth, basename match).
	for _, f := range acc.files {
		if w, ok := configSignals[filepath.Base(f)]; ok {
			evidence = append(evidence, Evidence{
				Kind: EvidenceConfig, ID: "config:" + filepath.Base(f),
				Detail: "framework/build config " + f + " present",
				Weight: w,
			})
		}
		// Declared-dependency evidence from package.json (bounded read).
		if filepath.Base(f) == "package.json" {
			for _, dep := range readPackageDepNames(filepath.Join(abs, f)) {
				evidence = append(evidence, Evidence{
					Kind: EvidenceDependency, ID: "dependency:" + dep,
					Detail: "dependency " + dep + " declared in " + f,
					Weight: 0.4,
				})
			}
		}
	}

	// 3. Language-by-extension evidence.
	extCounts := map[string]int{}
	for _, f := range acc.files {
		ext := strings.ToLower(filepath.Ext(f))
		if lang, ok := extLanguages[ext]; ok {
			extCounts[lang]++
		}
	}
	for lang, n := range extCounts {
		w := 0.3
		if n >= 3 {
			w = 0.6
		}
		evidence = append(evidence, Evidence{
			Kind: EvidenceLanguage, ID: "language:" + strings.ToLower(lang),
			Detail: itoa(int64(n)) + " " + lang + " source file(s) by extension",
			Weight: w,
		})
		langSet[lang] = true
		langWeight[lang] += w
	}

	// 4. Static-web structural surface (first-class, no Go-extractor fiction).
	staticWeb := scanStaticWeb(acc)
	if staticWeb.Present {
		if len(staticWeb.Entrypoints) > 0 {
			evidence = append(evidence, Evidence{
				Kind: EvidenceStructure, ID: "structure:" + staticWeb.Entrypoints[0],
				Detail: "HTML entrypoint " + staticWeb.Entrypoints[0] + " present",
				Weight: 0.7,
			})
		}
		for _, c := range staticWeb.CSS {
			evidence = append(evidence, Evidence{
				Kind: EvidenceStructure, ID: "structure:" + c,
				Detail: "stylesheet " + c + " present", Weight: 0.3,
			})
			break // one representative trace keeps evidence inspectable
		}
		for _, s := range staticWeb.Scripts {
			evidence = append(evidence, Evidence{
				Kind: EvidenceStructure, ID: "structure:" + s,
				Detail: "script " + s + " present", Weight: 0.3,
			})
			break
		}
		for _, d := range staticWeb.AssetDirs {
			evidence = append(evidence, Evidence{
				Kind: EvidenceStructure, ID: "structure:" + d + "/",
				Detail: "asset directory " + d + "/ present", Weight: 0.3,
			})
			break
		}
		for _, r := range staticWeb.ScriptRefs {
			evidence = append(evidence, Evidence{
				Kind: EvidenceReference, ID: "reference:" + r,
				Detail: "HTML references script " + r, Weight: 0.2,
			})
			break
		}
		for _, r := range staticWeb.StyleRefs {
			evidence = append(evidence, Evidence{
				Kind: EvidenceReference, ID: "reference:" + r,
				Detail: "HTML references stylesheet " + r, Weight: 0.2,
			})
			break
		}
		u.StaticWeb = &staticWeb
	}

	// 5. Source-directory topology evidence.
	for _, d := range acc.dirs {
		switch strings.ToLower(filepath.Base(d)) {
		case "src", "cmd", "pkg", "internal", "lib", "app", "pages", "components", "sections", "partials":
			evidence = append(evidence, Evidence{
				Kind: EvidenceStructure, ID: "structure:" + d + "/",
				Detail: "source directory " + d + "/ present", Weight: 0.3,
			})
		}
	}

	// 6. VCS evidence (already-available git state only).
	if gs := detectGit(abs); gs != "" {
		evidence = append(evidence, Evidence{
			Kind: EvidenceVCS, ID: "vcs:" + gs,
			Detail: "workspace version-control state: " + gs, Weight: 0.1,
		})
	}

	u.Evidence = evidence
	for lang := range langSet {
		u.Languages = append(u.Languages, lang)
	}
	sort.Strings(u.Languages)
	u.Components = buildComponents(u, identities, staticWeb)
	u.Identity = identityFor(identities, staticWeb, len(acc.files))
	u.Kind = classify(acc, evidence, identities, staticWeb)
	u.Confidence = confidenceFor(evidence)
	return u
}

// classify applies the strict EXISTING / GREENFIELD / UNKNOWN semantics.
// Missing single files never imply GREENFIELD; only a genuinely empty
// workspace does. Anything insufficient or contradictory is UNKNOWN.
func classify(acc *scanAcc, evidence []Evidence, identities map[string]float64, sw StaticWebSurface) ProjectKind {
	if len(identities) > 0 {
		return KindExisting
	}
	// Structural EXISTING without a manifest: corroborated source surface.
	sourceFiles := 0
	for _, f := range acc.files {
		if trivialFiles[filepath.Base(f)] {
			continue
		}
		ext := strings.ToLower(filepath.Ext(f))
		if _, ok := extLanguages[ext]; ok {
			sourceFiles++
		}
	}
	if sw.Present && len(sw.HTML) > 0 {
		return KindExisting
	}
	if sourceFiles >= 2 {
		return KindExisting
	}
	if len(acc.files) == 0 {
		return KindGreenfield
	}
	// Only trivial files (README/LICENSE/.gitignore) and nothing else:
	// genuinely absent project area → GREENFIELD.
	nonTrivial := 0
	for _, f := range acc.files {
		if !trivialFiles[filepath.Base(f)] {
			nonTrivial++
		}
	}
	if nonTrivial == 0 {
		return KindGreenfield
	}
	return KindUnknown
}

// identityFor prefers coarse, evidence-backed labels and preserves
// uncertainty instead of manufacturing framework precision.
func identityFor(identities map[string]float64, sw StaticWebSurface, fileCount int) string {
	if len(identities) == 0 {
		if sw.Present {
			return "static-web"
		}
		if fileCount == 0 {
			return "empty"
		}
		return "unknown"
	}
	if len(identities) == 1 {
		for id := range identities {
			if id == "node-app" && sw.Present {
				return "node-app"
			}
			return id
		}
	}
	// Mixed signals: report coarse mixture, never a fabricated stack.
	ids := make([]string, 0, len(identities))
	for id := range identities {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) > 2 {
		ids = ids[:2]
	}
	return "mixed:" + strings.Join(ids, "+")
}

// buildComponents derives coarse structural components from evidence.
func buildComponents(u ProjectUnderstanding, identities map[string]float64, sw StaticWebSurface) []Component {
	var out []Component
	if sw.Present {
		paths := append([]string{}, sw.Entrypoints...)
		paths = append(paths, sw.CSS...)
		paths = append(paths, sw.Scripts...)
		paths = append(paths, sw.AssetDirs...)
		sort.Strings(paths)
		if len(paths) > 8 {
			paths = paths[:8]
		}
		out = append(out, Component{Name: "static-web", Kind: "static", Paths: paths})
	}
	ids := make([]string, 0, len(identities))
	for id := range identities {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if id == "node-app" && sw.Present {
			continue // already covered by the static-web component paths
		}
		out = append(out, Component{Name: id, Kind: componentKind(id)})
	}
	return out
}

func componentKind(identity string) string {
	switch identity {
	case "go-module", "rust-crate", "python-project", "java-project", "ruby-project", "php-project", "cpp-project":
		return "backend"
	case "node-app":
		return "frontend"
	case "containerized", "make-project":
		return "config"
	default:
		return "unknown"
	}
}

// confidenceFor clamps the summed evidence weight to [0,1] with a soft
// cap so weak evidence stays visibly uncertain (no false precision).
func confidenceFor(evidence []Evidence) float64 {
	var s float64
	for _, e := range evidence {
		s += e.Weight
	}
	if s > 1 {
		return 1
	}
	if s < 0 {
		return 0
	}
	// Round to two decimals for deterministic rendering.
	return float64(int(s*100+0.5)) / 100
}

// unavailable builds the conservative UNKNOWN understanding for discovery
// failures. Derivation failure is never GREENFIELD.
func unavailable(root, reason string, now time.Time) ProjectUnderstanding {
	return ProjectUnderstanding{
		Root: root, Kind: KindUnknown, Identity: "unknown",
		Evidence: []Evidence{{
			Kind: EvidenceAbsence, ID: "absence:workspace-evidence",
			Detail: "project evidence unavailable: " + reason, Weight: 0,
		}},
		Unavailable: true, UnavailableReason: reason, CreatedAt: now,
	}
}

// snapshotID binds an understanding to one workspace state and time.
func snapshotID(root, digest string, now time.Time) string {
	sum := sha256.Sum256([]byte(root + "\x00" + digest + "\x00" + now.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:8])
}

// digestWorkspace hashes the sorted file surface (path + size). It is the
// lifecycle boundary: any added/removed/resized file changes the digest
// and renders prior understanding stale.
func digestWorkspace(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", errMissingRoot
	}
	var entries []string
	walkErr := filepath.Walk(abs, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return filepath.SkipDir
		}
		rel, rerr := filepath.Rel(abs, path)
		if rerr != nil || rel == "." {
			return nil //nolint:nilerr // best-effort digest, keep walking
		}
		rel = filepath.ToSlash(rel)
		if fi.IsDir() {
			if skipDirs[filepath.Base(rel)] {
				return filepath.SkipDir
			}
			return nil //nolint:nilerr // best-effort digest, keep walking
		}
		entries = append(entries, rel+"\x00"+itoa(fi.Size()))
		if len(entries) > 8192 {
			return filepath.SkipDir
		}
		return nil //nolint:nilerr // best-effort digest, keep walking
	})
	if walkErr != nil {
		return "", walkErr
	}
	sort.Strings(entries)
	h := sha256.New()
	h.Write([]byte(abs + "\x00"))
	for _, e := range entries {
		h.Write([]byte(e + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// detectGit reports already-available VCS state without requiring git.
func detectGit(root string) string {
	fi, err := os.Stat(filepath.Join(root, ".git"))
	if err != nil || (!fi.IsDir() && fi.Size() == 0) {
		return ""
	}
	return "git-present"
}

// readPackageDepNames returns top-level dependency names from package.json,
// bounded so a huge manifest cannot affect derivation cost. Malformed input
// yields no evidence (UNKNOWN-leaning), never an error.
func readPackageDepNames(path string) []string {
	data, err := os.ReadFile(path) //nolint:gosec // walk-derived path
	if err != nil || len(data) > 256*1024 {
		return nil
	}
	var names []string
	for _, section := range []string{`"dependencies"`, `"devDependencies"`, `"peerDependencies"`} {
		block := extractJSONBlock(string(data), section)
		if block == "" {
			continue
		}
		for _, m := range depEntryRe.FindAllStringSubmatch(block, -1) {
			if len(m) > 1 && m[1] != "" {
				names = append(names, m[1])
			}
			if len(names) >= 16 {
				break
			}
		}
	}
	return names
}

// depEntryRe matches `"name": "version"` entries inside an extracted JSON
// object body. It is intentionally local (mirroring the inference facts
// reader) so this package stays dependency-free.
var depEntryRe = regexp.MustCompile(`"([^"]+)"\s*:\s*"([^"]*)"`)

// extractJSONBlock returns the raw body of a named top-level JSON object
// without a full JSON parser, tolerating the common package.json shape.
func extractJSONBlock(data, key string) string {
	idx := strings.Index(data, key)
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

// errMissingRoot is the sentinel for an inaccessible workspace root.
var errMissingRoot = errString("workspace root is not accessible")

type errString string

func (e errString) Error() string { return string(e) }

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [32]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
