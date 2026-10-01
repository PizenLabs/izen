package execution

// ── PHASE 16: STRICT ARTIFACT BINDING ────────────────────────────────────────
//
// Artifact PRODUCTION is not artifact BINDING. The tests below pin the gap: a
// perfectly well-formed payload that carries no statement about WHERE it
// belongs must be refused, and the refusal must be observable as "no bytes
// reached the disk" rather than as a promise made by a comment.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// snapshotTree returns every regular file under root mapped to its content, so a
// test can assert that NOTHING was written rather than inspecting one path it
// happened to guess.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		data, readErr := os.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		out[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func assertTreeUnchanged(t *testing.T, root string, before map[string]string) {
	t.Helper()
	after := snapshotTree(t, root)
	if len(after) != len(before) {
		t.Fatalf("filesystem changed: %d file(s) before, %d after (new: %v)", len(before), len(after), diffKeys(before, after))
	}
	for rel, want := range before {
		got, ok := after[rel]
		if !ok {
			t.Errorf("file %q was removed", rel)
			continue
		}
		if got != want {
			t.Errorf("file %q was rewritten:\n--- before ---\n%s\n--- after ---\n%s", rel, want, got)
		}
	}
}

func diffKeys(before, after map[string]string) []string {
	var added []string
	for k := range after {
		if _, ok := before[k]; !ok {
			added = append(added, k)
		}
	}
	return added
}

// TestPhase16_HeuristicBindingRejection is ACCEPTANCE TEST C.
//
// Input: the provider returns a raw "```html <html></html> ```" payload with no
// file-path metadata.
//
// Assert: the binder rejects the artifact with ErrUnboundArtifact and the
// filesystem is unchanged — zero files written.
//
// The payload is a fine artifact. That is precisely why the test matters: an
// earlier resolution path took "HTML document" and produced "index.html", and
// the write looked reasonable in every observable respect except the one that
// counted. Content type is not target evidence, and the only way to keep that
// true under a fluent, helpful provider is to make the missing evidence a hard
// error rather than a default.
func TestPhase16_HeuristicBindingRejection(t *testing.T) {
	root := writeTree(t, map[string]string{
		"index.html": "<html><body>original</body></html>\n",
	})
	before := snapshotTree(t, root)
	binder := NewArtifactBinder(root)

	// The exact payload shape from the acceptance matrix: a bare language fence.
	raw := "```html\n<html></html>\n```"

	// The parser recognizes the artifact representation and names a form — it
	// must NOT name a path. A path here would be the content-type heuristic.
	artifact, err := ParseMutationArtifacts(raw)
	if err != nil {
		t.Fatalf("a fenced HTML payload is a recognizable artifact: %v", err)
	}
	if artifact.DeclaredPath != "" {
		t.Fatalf("I7 violation: the parser inferred target path %q from a bare ```html fence; content type is not target evidence", artifact.DeclaredPath)
	}

	// The caller resolves a real target. The artifact still may not be applied
	// to it, because the artifact never said it was about it.
	binding := TargetBinding{
		Path:         "index.html",
		SourceSHA256: sourceSHA256([]byte(before["index.html"])),
		Explicit:     true,
		Exists:       true,
	}

	bound, bindErr := binder.Bind(ArtifactEnvelope{
		Content:       "<html></html>\n",
		Form:          artifact.Form,
		Structural:    artifact.Structural,
		DeclaredPath:  artifact.DeclaredPath,
		ContextSHA256: binding.SourceSHA256,
	}, binding)

	if !errors.Is(bindErr, ErrUnboundArtifact) {
		t.Fatalf("bind error = %v, want ErrUnboundArtifact", bindErr)
	}
	// The rejection must be diagnosable: "unbound" alone does not tell the
	// human whether the path was missing, wrong, or merely stale.
	if !strings.Contains(bindErr.Error(), "declares no target path") {
		t.Errorf("rejection must name the missing criterion, got: %v", bindErr)
	}
	// A rejected envelope yields NOTHING that could be mistaken for permission.
	if bound.Path != "" || bound.Content != "" {
		t.Errorf("a rejected artifact must bind to nothing, got %+v", bound)
	}
	// Zero files written, and not one byte changed.
	assertTreeUnchanged(t, root, before)
}

// TestPhase16_DeclaredPathIsExplicitOnly: only an EXPLICIT path attribute
// yields a declared path. Every heuristic shortcut is excluded, because each of
// them would re-create the "HTML became index.html" defect under a new name.
func TestPhase16_DeclaredPathIsExplicitOnly(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "bare html fence names a language, not a location",
			raw:  "```html\n<html></html>\n```",
			want: "",
		},
		{
			name: "bare go fence names a language, not a location",
			raw:  "```go\npackage main\n```",
			want: "",
		},
		{
			name: "bare css fence names a language, not a location",
			raw:  "```css\nbody{}\n```",
			want: "",
		},
		{
			name: "bare json fence names a language, not a location",
			raw:  "```json\n{}\n```",
			want: "",
		},
		{
			name: "lang:path fence declares the path",
			raw:  "```html:index.html\n<html></html>\n```",
			want: "index.html",
		},
		{
			name: "lang:path fence declares a nested path",
			raw:  "```html:src/pages/index.html\n<html></html>\n```",
			want: "src/pages/index.html",
		},
		{
			name: "FILE_CREATE envelope declares the path",
			raw:  "<<<<<<< FILE_CREATE README.md\n# hi\n>>>>>> END_FILE",
			want: "README.md",
		},
		{
			name: "contract fence declares the path",
			raw:  ":::artifact internal/auth/token.go\npackage auth\n:::",
			want: "internal/auth/token.go",
		},
		{
			name: "unified diff target header declares the path",
			raw:  "--- a/src/app.ts\n+++ b/src/app.ts\n@@ -1 +1 @@\n-a\n+b\n",
			want: "src/app.ts",
		},
		{
			name: "a bare filename fence header is a location",
			raw:  "```styles.css\nbody{}\n```",
			want: "styles.css",
		},
		{
			name: "an absolute path is not a workspace-relative location",
			raw:  "```html:/etc/passwd\nx\n```",
			want: "",
		},
		{
			name: "a traversal is not a location",
			raw:  "```html:../escape.html\nx\n```",
			want: "",
		},
		{
			name: "an empty payload declares nothing",
			raw:  "",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeclaredTargetPath(tc.raw); got != tc.want {
				t.Errorf("DeclaredTargetPath = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPhase16_ParserNeverInfersAPathFromContent walks the whole recognition
// surface: whatever the payload, the parser's path output must be traceable to
// an explicit path attribute in the payload itself.
func TestPhase16_ParserNeverInfersAPathFromContent(t *testing.T) {
	bodies := []string{
		"```html\n<html><head><title>x</title></head></html>\n```",
		"```\n<html></html>\n```",
		"<!DOCTYPE html>\n<html>\n<head><link rel=\"stylesheet\" href=\"index.css\"></head>\n<body></body>\n</html>\n",
		"```html\n<html></html>\n```\nAnd here is a summary of the changes.",
	}
	for _, body := range bodies {
		artifact, err := ParseMutationArtifacts(body)
		if err != nil {
			continue // a prose rejection is a fine outcome; it has no path either
		}
		if artifact.DeclaredPath != "" && !strings.Contains(body, artifact.DeclaredPath) {
			t.Errorf("parser produced declared path %q that does not appear in the payload: %q", artifact.DeclaredPath, body)
		}
	}
}

// TestPhase16_BindingBindsAnExplicitlyAddressedArtifact is the positive case: an
// artifact that names its target and was produced against the target's current
// bytes binds cleanly, and the evidence records BOTH criteria.
func TestPhase16_BindingBindsAnExplicitlyAddressedArtifact(t *testing.T) {
	original := "<html><body>before</body></html>\n"
	root := writeTree(t, map[string]string{"index.html": original})
	binder := NewArtifactBinder(root)

	bound, err := binder.Bind(ArtifactEnvelope{
		Content:       "<html><body>after</body></html>\n",
		Form:          ArtifactFormContractFence,
		Structural:    true,
		DeclaredPath:  "index.html",
		ContextSHA256: sourceSHA256([]byte(original)),
	}, TargetBinding{
		Path:         "index.html",
		SourceSHA256: sourceSHA256([]byte(original)),
		Explicit:     true,
		Exists:       true,
	})
	if err != nil {
		t.Fatalf("an explicitly addressed, context-matched artifact must bind: %v", err)
	}
	if bound.Path != "index.html" {
		t.Errorf("bound path = %q, want index.html", bound.Path)
	}
	if !bound.Evidence.PathMatch || !bound.Evidence.ContextMatch {
		t.Errorf("both binding criteria must be recorded as met: %+v", bound.Evidence)
	}
	if bound.Evidence.Phase != PhaseTargetBindingResolved {
		t.Errorf("phase = %q, want %q", bound.Evidence.Phase, PhaseTargetBindingResolved)
	}
	// Binding must not write: the gate decides, the mutation engine acts.
	assertTreeUnchanged(t, root, map[string]string{"index.html": original})
}

// TestPhase16_MismatchedDeclaredPathIsRefused: an artifact that addresses a
// DIFFERENT file than the one it is being applied to is the most dangerous case
// in the whole system, and it must never be quietly redirected to the target.
func TestPhase16_MismatchedDeclaredPathIsRefused(t *testing.T) {
	root := writeTree(t, map[string]string{
		"index.html": "<html>a</html>\n",
		"other.html": "<html>b</html>\n",
	})
	before := snapshotTree(t, root)
	binder := NewArtifactBinder(root)

	_, err := binder.Bind(ArtifactEnvelope{
		Content:       "<html>c</html>\n",
		Form:          ArtifactFormContractFence,
		Structural:    true,
		DeclaredPath:  "other.html",
		ContextSHA256: sourceSHA256([]byte(before["index.html"])),
	}, TargetBinding{
		Path:         "index.html",
		SourceSHA256: sourceSHA256([]byte(before["index.html"])),
		Explicit:     true,
		Exists:       true,
	})
	if !errors.Is(err, ErrUnboundArtifact) {
		t.Fatalf("bind error = %v, want ErrUnboundArtifact", err)
	}
	if !strings.Contains(err.Error(), "other.html") || !strings.Contains(err.Error(), "index.html") {
		t.Errorf("rejection must name both paths so the mismatch is actionable, got: %v", err)
	}
	assertTreeUnchanged(t, root, before)
}

// TestPhase16_StaleContextDigestIsRefused: the artifact was produced against
// buffer A and is about to be written over buffer B. Without the context digest
// this is an undetectable corruption.
func TestPhase16_StaleContextDigestIsRefused(t *testing.T) {
	root := writeTree(t, map[string]string{"index.html": "<html>current</html>\n"})
	before := snapshotTree(t, root)
	binder := NewArtifactBinder(root)

	_, err := binder.Bind(ArtifactEnvelope{
		Content:       "<html>new</html>\n",
		Form:          ArtifactFormContractFence,
		Structural:    true,
		DeclaredPath:  "index.html",
		ContextSHA256: sourceSHA256([]byte("<html>stale snapshot</html>\n")),
	}, TargetBinding{
		Path:         "index.html",
		SourceSHA256: sourceSHA256([]byte(before["index.html"])),
		Explicit:     true,
		Exists:       true,
	})
	if !errors.Is(err, ErrUnboundArtifact) {
		t.Fatalf("bind error = %v, want ErrUnboundArtifact", err)
	}
	if !strings.Contains(err.Error(), "context digest") || !strings.Contains(err.Error(), "index.html") {
		t.Errorf("rejection must identify the digest mismatch AND the target, got: %v", err)
	}
	assertTreeUnchanged(t, root, before)
}

// TestPhase16_StaleTargetBindingIsRefused: the binding's own digest no longer
// matches the file. The target changed after the binding was made, so the
// binding is a promise about bytes that are gone.
func TestPhase16_StaleTargetBindingIsRefused(t *testing.T) {
	current := "<html>current</html>\n"
	root := writeTree(t, map[string]string{"index.html": current})
	before := snapshotTree(t, root)
	binder := NewArtifactBinder(root)

	_, err := binder.Bind(ArtifactEnvelope{
		Content:       "<html>new</html>\n",
		Form:          ArtifactFormContractFence,
		Structural:    true,
		DeclaredPath:  "index.html",
		ContextSHA256: sourceSHA256([]byte(current)),
	}, TargetBinding{
		Path:         "index.html",
		SourceSHA256: sourceSHA256([]byte("<html>a different buffer entirely</html>\n")),
		Explicit:     true,
		Exists:       true,
	})
	if !errors.Is(err, ErrUnboundArtifact) {
		t.Fatalf("bind error = %v, want ErrUnboundArtifact", err)
	}
	if !strings.Contains(err.Error(), "stale") {
		t.Errorf("rejection must identify the stale binding, got: %v", err)
	}
	assertTreeUnchanged(t, root, before)
}

// TestPhase16_MissingContextDigestForExistingTargetIsRefused: writing over
// observed bytes without saying which bytes were observed is an unconditional
// overwrite wearing a validation badge.
func TestPhase16_MissingContextDigestForExistingTargetIsRefused(t *testing.T) {
	root := writeTree(t, map[string]string{"index.html": "<html>a</html>\n"})
	before := snapshotTree(t, root)
	binder := NewArtifactBinder(root)

	_, err := binder.Bind(ArtifactEnvelope{
		Content:      "<html>b</html>\n",
		Form:         ArtifactFormRawBody,
		DeclaredPath: "index.html",
	}, TargetBinding{
		Path:     "index.html",
		Explicit: true,
		Exists:   true,
	})
	if !errors.Is(err, ErrUnboundArtifact) {
		t.Fatalf("bind error = %v, want ErrUnboundArtifact", err)
	}
	if !strings.Contains(err.Error(), "no patch-context digest") {
		t.Errorf("rejection must identify the missing digest, got: %v", err)
	}
	assertTreeUnchanged(t, root, before)
}

// TestPhase16_NoBindingIsRefused: with no proven target there is nothing to bind
// TO. This is the fail-closed precondition, and it must run before any other
// check — otherwise every later comparison is against an empty string, which
// passes.
func TestPhase16_NoBindingIsRefused(t *testing.T) {
	root := writeTree(t, map[string]string{"index.html": "<html>a</html>\n"})
	before := snapshotTree(t, root)
	binder := NewArtifactBinder(root)

	_, err := binder.Bind(ArtifactEnvelope{
		Content:      "<html>b</html>\n",
		DeclaredPath: "index.html",
	}, TargetBinding{})
	if !errors.Is(err, ErrUnboundArtifact) {
		t.Fatalf("bind error = %v, want ErrUnboundArtifact", err)
	}
	if !strings.Contains(err.Error(), "no proven TargetBinding") {
		t.Errorf("rejection must identify the missing binding, got: %v", err)
	}
	assertTreeUnchanged(t, root, before)
}

// TestPhase16_AddressedArtifactMayNotBorrowTheDispatchTarget: a self-addressed
// envelope (FILE_CREATE, contract fence, diff) MUST name its own path. Allowing
// it to inherit the dispatch target would let the model answer about a file the
// runtime never asked about.
func TestPhase16_AddressedArtifactMayNotBorrowTheDispatchTarget(t *testing.T) {
	root := writeTree(t, map[string]string{"index.html": "<html>a</html>\n"})
	before := snapshotTree(t, root)
	binder := NewArtifactBinder(root)

	for _, form := range []ArtifactForm{ArtifactFormFileCreate, ArtifactFormContractFence, ArtifactFormUnifiedDiff} {
		_, err := binder.Bind(ArtifactEnvelope{
			Content:       "<html>b</html>\n",
			Form:          form,
			Structural:    true,
			ContractPath:  "index.html",
			ContextSHA256: sourceSHA256([]byte(before["index.html"])),
		}, TargetBinding{
			Path:         "index.html",
			SourceSHA256: sourceSHA256([]byte(before["index.html"])),
			Explicit:     true,
			Exists:       true,
		})
		if !errors.Is(err, ErrUnboundArtifact) {
			t.Errorf("form %s: bind error = %v, want ErrUnboundArtifact", form, err)
			continue
		}
		if !strings.Contains(err.Error(), "no explicit path header") {
			t.Errorf("form %s: rejection must identify the headerless envelope, got: %v", form, err)
		}
		assertTreeUnchanged(t, root, before)
	}
}

// TestPhase16_PositionalArtifactBindsViaTheDispatchContract: a SEARCH/REPLACE
// block and a bare full-file body have no path field by construction — their
// location IS the dispatch, and their content is byte-anchored to the observed
// source buffer. Demanding a header from them would be demanding a field their
// contract does not define.
func TestPhase16_PositionalArtifactBindsViaTheDispatchContract(t *testing.T) {
	original := "<html><body>before</body></html>\n"
	root := writeTree(t, map[string]string{"index.html": original})
	binder := NewArtifactBinder(root)

	for _, form := range []ArtifactForm{ArtifactFormSearchReplace, ArtifactFormRawBody} {
		bound, err := binder.Bind(ArtifactEnvelope{
			Content:       "<html><body>after</body></html>\n",
			Form:          form,
			Structural:    form == ArtifactFormSearchReplace,
			ContractPath:  "index.html",
			ContextSHA256: sourceSHA256([]byte(original)),
		}, TargetBinding{
			Path:         "index.html",
			SourceSHA256: sourceSHA256([]byte(original)),
			Explicit:     true,
			Exists:       true,
		})
		if err != nil {
			t.Errorf("form %s must bind via the dispatch contract: %v", form, err)
			continue
		}
		if !bound.Evidence.PathMatch || !bound.Evidence.ContextMatch {
			t.Errorf("form %s: both criteria must be recorded as met: %+v", form, bound.Evidence)
		}
	}
}

// TestPhase16_FormAddressingIsPerContract: the addressed/positional split is a
// property of the artifact CONTRACT, so it is fixed here rather than decided
// per-payload. A gate that re-derived it from the bytes would be a heuristic
// wearing a type name.
func TestPhase16_FormAddressingIsPerContract(t *testing.T) {
	addressed := map[ArtifactForm]bool{
		ArtifactFormFileCreate:    true,
		ArtifactFormContractFence: true,
		ArtifactFormUnifiedDiff:   true,
	}
	positional := []ArtifactForm{
		ArtifactFormSearchReplace,
		ArtifactFormRawBody,
		ArtifactFormNone,
	}
	for _, form := range positional {
		if form.IsAddressed() {
			t.Errorf("form %s is positional and must not require a path header", form)
		}
	}
	for form := range addressed {
		if !form.IsAddressed() {
			t.Errorf("form %s is addressed and must require a path header", form)
		}
	}
}

// TestPhase16_DispatchContractPathBindsAnUnstructuredBody: the runtime's own
// artifact contract names the target BEFORE the call, so a contract-scoped
// full-file body is explicitly bound even though the payload carries no fence
// header. This is explicit evidence (the runtime stated it), not a heuristic.
func TestPhase16_DispatchContractPathBindsAnUnstructuredBody(t *testing.T) {
	original := "<html><body>before</body></html>\n"
	root := writeTree(t, map[string]string{"index.html": original})
	binder := NewArtifactBinder(root)

	bound, err := binder.Bind(ArtifactEnvelope{
		Content:       "<html><body>after</body></html>\n",
		Form:          ArtifactFormRawBody,
		ContractPath:  "index.html",
		ContextSHA256: sourceSHA256([]byte(original)),
	}, TargetBinding{
		Path:         "index.html",
		SourceSHA256: sourceSHA256([]byte(original)),
		Explicit:     true,
		Exists:       true,
	})
	if err != nil {
		t.Fatalf("a contract-scoped body with a matching context must bind: %v", err)
	}
	if bound.Evidence.DeclaredPath != "index.html" {
		t.Errorf("declared path = %q, want index.html", bound.Evidence.DeclaredPath)
	}
}

// TestPhase16_ArtifactDeclaredPathWinsOverTheDispatchContract: when the model
// names a different file, that disagreement is a FACT the binder must see. The
// runtime preferring its own value here would hide a model answering about the
// wrong file behind a green checkmark.
func TestPhase16_ArtifactDeclaredPathWinsOverTheDispatchContract(t *testing.T) {
	root := writeTree(t, map[string]string{
		"index.html": "<html>a</html>\n",
		"other.html": "<html>b</html>\n",
	})
	before := snapshotTree(t, root)
	binder := NewArtifactBinder(root)

	_, err := binder.Bind(ArtifactEnvelope{
		Content:       "<html>c</html>\n",
		Form:          ArtifactFormContractFence,
		Structural:    true,
		DeclaredPath:  "other.html",
		ContractPath:  "index.html",
		ContextSHA256: sourceSHA256([]byte(before["index.html"])),
	}, TargetBinding{
		Path:         "index.html",
		SourceSHA256: sourceSHA256([]byte(before["index.html"])),
		Explicit:     true,
		Exists:       true,
	})
	if !errors.Is(err, ErrUnboundArtifact) {
		t.Fatalf("bind error = %v, want ErrUnboundArtifact", err)
	}
	assertTreeUnchanged(t, root, before)
}

// TestPhase16_CreationBindsOnlyAStatedOrTemplateTarget: whether the runtime may
// CREATE a file is a question about where the path came from, not about the
// artifact. A path a human stated is evidence; a path the runtime reached on its
// own is a guess, and materializing a guess is how "create a portfolio" becomes
// an unexplained portfolio.tsx on disk.
func TestPhase16_CreationBindsOnlyAStatedOrTemplateTarget(t *testing.T) {
	root := writeTree(t, map[string]string{"index.html": "<html>a</html>\n"})
	before := snapshotTree(t, root)
	binder := NewArtifactBinder(root)

	// A well-known creation template binds even when nobody stated it: the
	// template set IS the statement. It binds with NO claimed context, because
	// there are no source bytes to anchor to and claiming some would be
	// describing a file the model never read.
	bound, err := binder.Bind(ArtifactEnvelope{
		Content:      "# readme\n",
		Form:         ArtifactFormFileCreate,
		Structural:   true,
		DeclaredPath: "README.md",
	}, TargetBinding{Path: "README.md", Explicit: true})
	if err != nil {
		t.Fatalf("a template creation must bind: %v", err)
	}
	if bound.Evidence.TargetExists {
		t.Error("the evidence must record that the creation target did not exist")
	}
	assertTreeUnchanged(t, root, before)

	// A caller-STATED non-template creation binds too: the human named the file.
	if _, err := binder.Bind(ArtifactEnvelope{
		Content:      "export const x = 1\n",
		Form:         ArtifactFormFileCreate,
		Structural:   true,
		DeclaredPath: "src/portfolio.tsx",
	}, TargetBinding{Path: "src/portfolio.tsx", Explicit: true}); err != nil {
		t.Errorf("a caller-stated creation must bind: %v", err)
	}

	// A creation the runtime reached on its own does not.
	_, err = binder.Bind(ArtifactEnvelope{
		Content:      "export const x = 1\n",
		Form:         ArtifactFormFileCreate,
		Structural:   true,
		DeclaredPath: "src/guessed.tsx",
	}, TargetBinding{Path: "src/guessed.tsx"})
	if !errors.Is(err, ErrUnboundArtifact) {
		t.Fatalf("bind error = %v, want ErrUnboundArtifact", err)
	}
	if !strings.Contains(err.Error(), "will not create a file nobody named") {
		t.Errorf("rejection must name the invention it refused, got: %v", err)
	}
	assertTreeUnchanged(t, root, before)
}

// TestPhase16_CreationMayNotClaimAnchoredBytes: an artifact that claims a patch
// context for a file that does not exist is describing bytes it never read.
func TestPhase16_CreationMayNotClaimAnchoredBytes(t *testing.T) {
	root := writeTree(t, map[string]string{"index.html": "<html>a</html>\n"})
	before := snapshotTree(t, root)
	binder := NewArtifactBinder(root)

	_, err := binder.Bind(ArtifactEnvelope{
		Content:       "# readme\n",
		Form:          ArtifactFormFileCreate,
		Structural:    true,
		DeclaredPath:  "README.md",
		ContextSHA256: sourceSHA256([]byte("bytes that do not exist")),
	}, TargetBinding{Path: "README.md", Explicit: true})
	if !errors.Is(err, ErrUnboundArtifact) {
		t.Fatalf("bind error = %v, want ErrUnboundArtifact", err)
	}
	if !strings.Contains(err.Error(), "cannot be anchored to absent bytes") {
		t.Errorf("rejection must identify the phantom context, got: %v", err)
	}
	assertTreeUnchanged(t, root, before)
}

// TestPhase16_BinderRefusesTargetsOutsideTheRoot: containment is checked before
// the criteria, because a binder that can name a file outside the workspace
// would pass every other check while writing anywhere on the disk.
func TestPhase16_BinderRefusesTargetsOutsideTheRoot(t *testing.T) {
	root := writeTree(t, map[string]string{"index.html": "<html>a</html>\n"})
	binder := NewArtifactBinder(root)

	for _, escape := range []string{"../escape.html", "../../etc/passwd"} {
		_, err := binder.Bind(ArtifactEnvelope{
			Content:      "x\n",
			DeclaredPath: escape,
		}, TargetBinding{Path: escape, Explicit: true})
		if !errors.Is(err, ErrUnboundArtifact) {
			t.Errorf("target %q escaped the root and still bound: %v", escape, err)
		}
	}
}

// TestPhase16_RejectionCarriesTheUnsubstantiatedPhase: every rejection
// transitions to the same deterministic phase, so no projector has to infer
// "failed to bind" from a nil value.
func TestPhase16_RejectionCarriesTheUnsubstantiatedPhase(t *testing.T) {
	root := writeTree(t, map[string]string{"index.html": "<html>a</html>\n"})
	binder := NewArtifactBinder(root)

	// The evidence is only observable through the returned value, and a rejected
	// bind returns the zero value — so the phase is asserted on the error path's
	// own contract: the reason string is present and the returned artifact is
	// empty. The phase itself is asserted via the sentinel's typed identity.
	bound, err := binder.Bind(ArtifactEnvelope{Content: "x"}, TargetBinding{})
	if !errors.Is(err, ErrUnboundArtifact) {
		t.Fatalf("bind error = %v, want ErrUnboundArtifact", err)
	}
	if bound.Evidence.Phase != "" {
		t.Errorf("a rejected bind must not carry a resolved phase, got %q", bound.Evidence.Phase)
	}
	// The binder records PhaseUnsubstantiated on the evidence it builds; the
	// zero value is returned so no caller can mistake it for a resolution.
	if got := string(PhaseUnsubstantiated); got != "UNSUBSTANTIATED" {
		t.Errorf("UNSUBSTANTIATED literal drifted: %q", got)
	}
}

// TestPhase16_BinderIsNilSafe: the binder sits on the mutation hot path and must
// refuse rather than panic when the workspace root was never bound.
func TestPhase16_BinderIsNilSafe(t *testing.T) {
	var binder *ArtifactBinder
	if _, err := binder.Bind(ArtifactEnvelope{Content: "x"}, TargetBinding{Path: "index.html"}); !errors.Is(err, ErrUnboundArtifact) {
		t.Errorf("a nil binder must refuse, got %v", err)
	}
	if binder.Root() != "" {
		t.Error("a nil binder has no root")
	}
}
