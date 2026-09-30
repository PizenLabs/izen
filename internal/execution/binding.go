package execution

// ── STRICT ARTIFACT BINDING (Phase 16) ──────────────────────────────────────
//
// Artifact PRODUCTION is not artifact BINDING. A model that answers with a
// perfect, well-formed HTML document has produced bytes; it has not said which
// file those bytes belong to. Those are different facts, and the runtime used to
// conflate them: the target was whatever the dispatch happened to be aimed at,
// and the artifact was whatever the parser could extract from the payload.
//
// The consequence was a silent arbitrary write. A ```html fence carries no
// path, so the parser had to supply one, and the only thing it had to go on was
// the CONTENT — which is why "HTML document" reliably became "index.html".
// That is not a bug in the path default; it is the absence of a binding step
// being papered over with a content-type heuristic.
//
// This file is that missing step, and it is FAIL-CLOSED by construction:
//
//	An artifact is bound only when BOTH criteria hold:
//	  1. an EXPLICIT path attribute in the artifact envelope names exactly the
//	     resolved TargetBinding.Path, and
//	  2. the artifact's patch-context SHA-256 equals the SHA-256 of the target
//	     file's current bytes.
//
//	Either one failing returns ErrUnboundArtifact. There is no third branch, no
//	"probably fine" path, and no default path. The binder cannot write; it only
//	decides whether a write is authorized to proceed, and the mutation engine
//	remains the only component that touches the disk.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ErrUnboundArtifact is the typed, terminal rejection for an artifact that
// carries no deterministic target evidence. It is deliberately a single error
// for every failure mode — missing path, mismatched path, stale context digest
// — because from the mutation engine's point of view they are one condition:
// this artifact is not allowed near the disk.
//
// It is TERMINAL rather than retryable. A re-prompt cannot fix a missing target
// binding; only a different target decision can, and that decision belongs to
// the human. Retrying here is how a run spends real provider billing to arrive
// at the same unbound artifact.
var ErrUnboundArtifact = errors.New("execution: unbound artifact — the artifact carries no deterministic target evidence; it will not be applied to disk")

// ArtifactBindingEvidence records WHICH criteria were evaluated and how they
// came out. It exists so a rejection is diagnosable: "unbound" alone tells the
// human nothing about whether the path was missing, wrong, or merely stale.
type ArtifactBindingEvidence struct {
	// DeclaredPath is the explicit path attribute found in the artifact
	// envelope ("" when the envelope declared none).
	DeclaredPath string `json:"declared_path,omitempty"`
	// BoundPath is the proven TargetBinding path the artifact was checked
	// against.
	BoundPath string `json:"bound_path"`
	// PathMatch reports whether an explicit path attribute was present AND
	// equal to the bound path.
	PathMatch bool `json:"path_match"`
	// ArtifactContextSHA256 is the patch-context digest the artifact claims to
	// have been generated against.
	ArtifactContextSHA256 string `json:"artifact_context_sha256,omitempty"`
	// TargetSHA256 is the digest of the target file's CURRENT bytes on disk.
	TargetSHA256 string `json:"target_sha256,omitempty"`
	// ContextMatch reports whether the artifact's context digest equals the
	// target's current digest.
	ContextMatch bool `json:"context_match"`
	// TargetExists reports whether the bound target was present at bind time.
	TargetExists bool `json:"target_exists"`
	// Phase is the deterministic state a failed binding transitions to.
	Phase TargetBindingPhase `json:"phase"`
}

// BoundArtifact is an artifact that PROVED its target. It is the only shape
// that may cross into the mutation engine.
type BoundArtifact struct {
	// Path is the bound target, carried verbatim from the TargetBinding.
	Path string
	// Content is the artifact body, bound to Path.
	Content string
	// SourceSHA256 is the digest of the target bytes this artifact is bound to.
	SourceSHA256 string
	// Evidence is the per-criterion binding record.
	Evidence ArtifactBindingEvidence
}

// ArtifactEnvelope is the PARSER'S OUTPUT plus the explicit path attributes the
// envelope carried. The parser's job is to recognize the artifact
// representation; the binder's job is to decide where it goes. Neither one
// infers a path the other did not state.
type ArtifactEnvelope struct {
	// Content is the artifact body the parser resolved.
	Content string
	// Form names the recognized artifact representation.
	Form ArtifactForm
	// Structural reports whether the payload carried an explicit artifact
	// delimiter. A structural envelope is authoritative about its own shape and
	// is never prose-tested.
	Structural bool
	// DeclaredPath is the path the ENVELOPE explicitly named — the file named in
	// a "```lang:path" fence header, a ":::artifact <path>" contract fence, a
	// "<<<<<<< FILE_CREATE <path>" envelope, or a unified-diff target header.
	//
	// It is "" for a bare fence. It is NEVER derived from the content: an HTML
	// body does not imply index.html, and a JSON body does not imply data.json.
	// That inference is the exact defect this type exists to prevent.
	DeclaredPath string
	// ContractPath is the path the RUNTIME'S artifact contract named for this
	// dispatch (the target the executor told the model it was modifying). It is
	// explicit evidence too — it was stated before the call, not read out of
	// the answer — and it is what binds a contract-scoped full-file body.
	ContractPath string
	// ContextSHA256 is the digest of the source buffer the artifact was
	// generated against. It is what makes a stale binding detectable.
	ContextSHA256 string
}

// ArtifactBinder is the single gate between the artifact parser and the
// mutation engine. It holds the workspace root so it can read the target's
// current bytes and compare them against the artifact's claimed context.
type ArtifactBinder struct {
	root     string
	readFile func(string) ([]byte, error)
	stat     func(string) (os.FileInfo, error)
}

// NewArtifactBinder returns a binder rooted at the workspace root. root is
// resolved once so a later chdir cannot silently re-point the binder at a
// different tree mid-run.
func NewArtifactBinder(root string) *ArtifactBinder {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	return &ArtifactBinder{
		root:     abs,
		readFile: os.ReadFile,
		stat:     os.Stat,
	}
}

// Root returns the absolute workspace root the binder is confined to.
func (b *ArtifactBinder) Root() string {
	if b == nil {
		return ""
	}
	return b.root
}

// Bind validates an artifact envelope against a proven TargetBinding and
// returns the bound artifact. On any failure it returns ErrUnboundArtifact and
// a zero BoundArtifact: the caller receives nothing it could mistake for
// permission to write.
//
// The binder performs no writes. "Reject" here means the mutation engine is
// never handed a bound artifact, so there is no path by which an unbound
// artifact reaches os.WriteFile.
func (b *ArtifactBinder) Bind(env ArtifactEnvelope, binding TargetBinding) (BoundArtifact, error) {
	ev := ArtifactBindingEvidence{
		BoundPath:             binding.Path,
		ArtifactContextSHA256: strings.ToLower(strings.TrimSpace(env.ContextSHA256)),
	}
	reject := func(reason string) (BoundArtifact, error) {
		ev.Phase = PhaseUnsubstantiated
		return BoundArtifact{}, fmt.Errorf("%w: %s", ErrUnboundArtifact, reason)
	}

	// ── Precondition: a proven target must exist ────────────────────────
	// Without a bound path there is nothing to bind TO, and every downstream
	// check would be comparing an artifact against an empty string — which
	// passes, and would authorize a write to a file nobody named.
	if strings.TrimSpace(binding.Path) == "" {
		return reject("no proven TargetBinding: the artifact has no path to bind to")
	}

	targetAbs, ok := b.resolve(binding.Path)
	if !ok {
		return reject(fmt.Sprintf("bound target %q is not a readable file inside the workspace root", binding.Path))
	}
	boundNorm := path.Clean(filepath.ToSlash(binding.Path))

	// ── Read the target's CURRENT bytes ────────────────────────────────
	exists := true
	targetDigest := ""
	data, readErr := b.read(targetAbs)
	switch {
	case readErr == nil:
		targetDigest = sourceSHA256(data)
	case os.IsNotExist(readErr):
		exists = false
	default:
		// An unreadable target is not evidence of an absent one. Refusing here
		// is the difference between "create" and "overwrite a file we cannot see".
		return reject(fmt.Sprintf("bound target %q is present but unreadable", binding.Path))
	}
	ev.TargetExists = exists
	ev.TargetSHA256 = targetDigest

	// ── CRITERION 1: an explicit path attribute must name the bound path ──
	//
	// The envelope must carry a path, and that path must BE the bound target.
	// Two distinct failures are reported separately because they call for
	// different fixes: a missing path is a contract the model ignored, a
	// mismatched path is the model answering about a different file.
	declared := firstNonEmptyPath(env.DeclaredPath, env.ContractPath)
	if declared == "" {
		return reject(fmt.Sprintf(
			"artifact envelope declares no target path for %s; content type is not target evidence and the runtime will not infer a path from the artifact's content",
			boundNorm))
	}
	if path.Clean(filepath.ToSlash(declared)) != boundNorm {
		return reject(fmt.Sprintf(
			"artifact envelope names target %q but the proven TargetBinding is %q; an artifact may only be applied to the target it was bound to",
			declared, boundNorm))
	}
	ev.DeclaredPath = declared
	ev.PathMatch = true

	// An ADDRESSED envelope (FILE_CREATE, a ":::artifact <path>" contract fence,
	// a unified diff) names its own file, so that header is its target evidence
	// and it MUST be present. Borrowing the dispatch target instead would let a
	// model answer with a self-addressed envelope about a file the runtime never
	// asked about — and the check that would have caught it is exactly the one
	// being skipped.
	//
	// A POSITIONAL envelope (a SEARCH/REPLACE block, a bare full-file body)
	// carries no path by construction: its location is the dispatch the runtime
	// made, and its content is byte-anchored to the source buffer whose digest
	// criterion 2 just verified. Demanding a path header from those is
	// demanding a field their format does not have.
	if env.Structural && ArtifactForm(env.Form).IsAddressed() && strings.TrimSpace(env.DeclaredPath) == "" {
		ev.PathMatch = false
		return reject(fmt.Sprintf(
			"addressed artifact envelope (%s) carries no explicit path header; a self-addressed envelope may not borrow the dispatch target", env.Form))
	}

	// ── CRITERION 2: the patch context must match the target's bytes ────
	//
	// This is what catches the mutation that would otherwise be silently wrong:
	// the artifact was produced against buffer A and is about to be written over
	// buffer B.
	switch {
	case exists:
		if ev.ArtifactContextSHA256 == "" {
			return reject(fmt.Sprintf(
				"artifact declares no patch-context digest for existing target %s; a write over observed bytes requires a context binding", boundNorm))
		}
		if ev.ArtifactContextSHA256 != targetDigest {
			return reject(fmt.Sprintf(
				"artifact for %s claims context digest %s but the target's current bytes hash to %s; the target changed after the artifact was produced",
				boundNorm, shortDigest(ev.ArtifactContextSHA256), shortDigest(targetDigest)))
		}
		// The binding itself must not be stale either. A binding whose own
		// digest no longer matches is a binding to bytes that are gone.
		if binding.SourceSHA256 != "" && !strings.EqualFold(binding.SourceSHA256, targetDigest) {
			return reject(fmt.Sprintf(
				"TargetBinding for %s is stale: it was bound to %s and the target now hashes to %s",
				boundNorm, shortDigest(binding.SourceSHA256), shortDigest(targetDigest)))
		}
	case !exists:
		// A creation against a non-existent target is legitimate, but only with
		// NO claimed context: an artifact that claims to have patched bytes
		// which do not exist is describing a file it never read.
		if ev.ArtifactContextSHA256 != "" {
			return reject(fmt.Sprintf(
				"artifact claims patch context %s for a target that does not exist; a creation cannot be anchored to absent bytes",
				shortDigest(ev.ArtifactContextSHA256)))
		}
		// Whether the runtime may CREATE the file is a question about where the
		// path came from, not about the artifact. A path a human stated is
		// evidence; a path the runtime reached on its own is a guess, and
		// materializing a guess is how a "create a portfolio" request turns
		// into an unexplained portfolio.tsx on disk.
		if !binding.Explicit && !IsTemplateTarget(boundNorm) {
			return reject(fmt.Sprintf(
				"target %s does not exist, was not stated by the caller, and is not a well-known creation target; the runtime will not create a file nobody named",
				boundNorm))
		}
	}
	ev.ContextMatch = true
	ev.Phase = PhaseTargetBindingResolved

	return BoundArtifact{
		Path:         boundNorm,
		Content:      env.Content,
		SourceSHA256: targetDigest,
		Evidence:     ev,
	}, nil
}

// resolve maps a workspace-relative path to an absolute path inside the
// binder's root, refusing anything that escapes it. A binder that can name a
// file outside the workspace is an arbitrary-write primitive, and every other
// check in this file would still pass.
func (b *ArtifactBinder) resolve(rel string) (string, bool) {
	if b == nil || strings.TrimSpace(b.root) == "" {
		return "", false
	}
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	if rel == "" || rel == "." {
		return "", false
	}
	abs := filepath.Join(b.root, filepath.FromSlash(rel))
	// Containment is checked FIRST and unconditionally. A non-existent path
	// takes the creation branch, so an early return on os.IsNotExist would let
	// "../escape.html" walk straight past the check that every other criterion
	// depends on.
	cleanRoot := filepath.Clean(b.root)
	cleanAbs := filepath.Clean(abs)
	if cleanAbs != cleanRoot && !strings.HasPrefix(cleanAbs, cleanRoot+string(os.PathSeparator)) {
		return "", false
	}
	if b.stat == nil {
		return "", false
	}
	info, err := b.stat(cleanAbs)
	if err != nil {
		// A non-existent path is not an error here: the creation branch of Bind
		// handles it. Return the contained path so the caller can classify it.
		if os.IsNotExist(err) {
			return cleanAbs, true
		}
		return "", false
	}
	if info.IsDir() {
		return "", false
	}
	return cleanAbs, true
}

func (b *ArtifactBinder) read(abs string) ([]byte, error) {
	if b == nil || b.readFile == nil {
		return nil, os.ErrNotExist
	}
	return b.readFile(abs)
}

// firstNonEmptyPath returns the first non-empty, cleaned path attribute of an
// envelope. The ARTIFACT's own declared path always wins over the dispatch
// contract path: when a model names a different file, that disagreement is a
// fact the binder must see, not something the runtime should paper over by
// preferring its own value.
func firstNonEmptyPath(paths ...string) string {
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		return path.Clean(filepath.ToSlash(p))
	}
	return ""
}

// DeclaredTargetPath extracts the target path an artifact envelope EXPLICITLY
// names, and returns "" when it names none.
//
// This is the whole of I7: the parser recognizes artifact REPRESENTATIONS, and
// a path is a path only when the payload says so. A bare "```html" fence is a
// content type, not a location, and this function must therefore return "" for
// it. Deriving "index.html" from "<html>" is the exact failure this package
// was created to eliminate, and the acceptance test that pins it is
// TestPhase16_HeuristicBindingRejection.
func DeclaredTargetPath(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// FILE_CREATE / END_FILE: "<<<<<<< FILE_CREATE <path>".
		if p, ok := pathAfterMarker(trimmed, "FILE_CREATE"); ok {
			return cleanDeclaredPath(p)
		}
		// Contract fence: ":::artifact <path>".
		if strings.HasPrefix(trimmed, ":::artifact") {
			return cleanDeclaredPath(strings.TrimSpace(strings.TrimPrefix(trimmed, ":::artifact")))
		}
		// A fence header: "```lang:path" or "```path".
		if strings.HasPrefix(trimmed, "```") {
			info := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
			if info == "" {
				continue
			}
			if p, ok := declaredPathFromFenceInfo(info); ok {
				return cleanDeclaredPath(p)
			}
			// A bare language tag names a LANGUAGE, not a location. Returning a
			// path here is the content-type heuristic I7 forbids.
			continue
		}
		// A unified-diff target header: "+++ b/<path>".
		if strings.HasPrefix(trimmed, "+++ ") {
			return cleanDeclaredPath(strings.TrimSpace(strings.TrimPrefix(trimmed, "+++ ")))
		}
	}
	return ""
}

// declaredPathFromFenceInfo reads a fence info string. A "lang:path" form names
// both; a bare "lang" or a bare "path" form names one. The discriminator is
// structural — does the info string contain a separator that yields a path-like
// remainder — not semantic.
func declaredPathFromFenceInfo(info string) (string, bool) {
	if idx := strings.Index(info, ":"); idx > 0 {
		candidate := strings.TrimSpace(info[idx+1:])
		if candidate != "" {
			return candidate, true
		}
	}
	// No colon: the whole info string is a path only when it looks like one.
	// A language tag has no extension and no path separator, so this test
	// excludes "html" and "go" while admitting "index.html" and "src/app.ts".
	if strings.ContainsAny(info, "/.") {
		return info, true
	}
	return "", false
}

// pathAfterMarker reports the remainder of line after the first occurrence of
// marker, when the marker is present as a standalone token.
func pathAfterMarker(line, marker string) (string, bool) {
	idx := strings.Index(line, marker)
	if idx < 0 {
		return "", false
	}
	rest := strings.TrimSpace(line[idx+len(marker):])
	// Strip any trailing envelope terminator the model appended to the header.
	rest = strings.TrimRight(rest, ">")
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", false
	}
	return rest, true
}

// cleanDeclaredPath normalizes a declared path to a slash-separated relative
// path, or "" when it cannot be one. An absolute path or a traversal is
// dropped rather than normalized: a declared path is evidence, and evidence
// that names a location outside the workspace is not evidence at all.
func cleanDeclaredPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	// A unified diff writes the target as "b/<path>"; the "b/" is a diff
	// convention, not a directory.
	if strings.HasPrefix(p, "b/") || strings.HasPrefix(p, "a/") {
		if len(p) > 2 {
			p = p[2:]
		}
	}
	p = filepath.ToSlash(p)
	if strings.HasPrefix(p, "/") {
		return ""
	}
	cleaned := path.Clean(p)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return ""
	}
	return cleaned
}

// ArtifactSHA256 is the exported digest primitive for artifact content. It is
// the one place a caller hashes artifact bytes, so a caller cannot accidentally
// hash with a different algorithm and produce a digest that never matches.
func ArtifactSHA256(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
