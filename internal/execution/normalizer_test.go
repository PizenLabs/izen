package execution

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeNormalizerTarget(t *testing.T, root, target, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(target))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
}

func TestNormalizer_FencedCodeToPatch(t *testing.T) {
	root := t.TempDir()
	original := "foo\nbar\nbaz\n"
	writeNormalizerTarget(t, root, "note.txt", original)

	// Raw markdown fenced code block without SEARCH/REPLACE headers.
	// Contains a 1-line modification (bar -> qux) wrapped in fences.
	raw := "```\nfoo\nqux\nbaz\n```"

	v := NewNormalizingArtifactValidator(NewDefaultArtifactValidator()).WithRoot(root)
	bp, err := v.ValidateArtifact([]byte(raw), "note.txt")
	if err != nil {
		t.Fatalf("ValidateArtifact failed: %v", err)
	}
	if bp == nil {
		t.Fatal("expected BoundedPatch, got nil")
	}
	// The normalizer must have synthesized an explicit SEARCH/REPLACE block.
	if !strings.Contains(string(bp.Content), "<<<<<<< SEARCH") {
		t.Fatalf("expected synthesized SEARCH/REPLACE, got Content=%q", string(bp.Content))
	}
	if strings.TrimSpace(bp.Search) == "" {
		t.Fatalf("expected non-empty Search, got %q", bp.Search)
	}
	// Applying the patch to the original must yield the fenced content.
	extracted, _ := ExtractCodeBlockContent(raw)
	extracted = strings.TrimSpace(extracted)
	if blocks := ParseSearchReplaceBlocks(string(bp.Content)); len(blocks) > 0 {
		if got, ok := ApplySearchReplaceBlocks(original, blocks); ok {
			if strings.TrimSpace(got) != strings.TrimSpace(extracted) {
				t.Fatalf("applied patch = %q, want %q", got, extracted)
			}
		} else {
			t.Fatalf("failed to apply synthesized patch")
		}
	}
	// Also verify anchor resolution is unambiguous.
	sl := strings.Split(bp.Search, "\n")
	if _, _, err := ResolveAnchors(sl, original); err != nil {
		t.Fatalf("ResolveAnchors for synthesized search failed: %v", err)
	}
}

func TestNormalizer_FullFileDiffSynthesis(t *testing.T) {
	root := t.TempDir()
	original := "<!DOCTYPE html>\n<html>\n<body>\n<p>one</p>\n<p>two</p>\n<p>three</p>\n<p>four</p>\n</body>\n</html>\n"
	writeNormalizerTarget(t, root, "index.html", original)

	// Full HTML file rewrite containing a 3-line modification (two/three/four -> TWO/THREE/FOUR).
	modified := "<!DOCTYPE html>\n<html>\n<body>\n<p>one</p>\n<p>TWO</p>\n<p>THREE</p>\n<p>FOUR</p>\n</body>\n</html>\n"

	v := NewNormalizingArtifactValidator(NewDefaultArtifactValidator()).WithRoot(root)
	bp, err := v.ValidateArtifact([]byte(modified), "index.html")
	if err != nil {
		t.Fatalf("ValidateArtifact failed: %v", err)
	}
	if bp == nil {
		t.Fatal("expected BoundedPatch, got nil")
	}
	if !strings.Contains(string(bp.Content), "<<<<<<< SEARCH") {
		t.Fatalf("expected synthesized SEARCH/REPLACE for full-file rewrite, got %q", string(bp.Content))
	}
	// The synthesized patch must be minimal, not the full file.
	if strings.TrimSpace(bp.Search) == strings.TrimSpace(original) {
		t.Fatal("synthesized Search is the full file, expected minimal patch")
	}
	if strings.Contains(bp.Search, "<!DOCTYPE html>") && strings.Contains(bp.Search, "</html>") {
		t.Fatalf("Search should be minimal 3-line block, got full file: %q", bp.Search)
	}
	// Search should contain the original 3 lines, Replace the new 3 lines.
	if !strings.Contains(bp.Search, "<p>two</p>") || !strings.Contains(bp.Search, "<p>three</p>") {
		t.Fatalf("Search missing expected lines, got %q", bp.Search)
	}
	if !strings.Contains(bp.Replace, "<p>TWO</p>") {
		t.Fatalf("Replace missing expected lines, got %q", bp.Replace)
	}
	// Applying the patch must reconstruct the modified file.
	blocks := ParseSearchReplaceBlocks(string(bp.Content))
	if len(blocks) == 0 {
		t.Fatal("no SEARCH blocks in synthesized patch")
	}
	if got, ok := ApplySearchReplaceBlocks(original, blocks); ok {
		if got != modified {
			t.Fatalf("applied patch reconstruction failed:\n got %q\nwant %q", got, modified)
		}
	} else {
		t.Fatal("failed to apply synthesized patch")
	}
}

func TestNormalizer_RejectAmbiguousAnchor(t *testing.T) {
	root := t.TempDir()
	original := "common\nunique1\ncommon\nunique2\n"
	writeNormalizerTarget(t, root, "dup.txt", original)

	// SEARCH block whose anchor "common" appears twice in the target.
	raw := "<<<<<<< SEARCH\ncommon\n=======\nreplaced\n>>>>>>>"

	v := NewNormalizingArtifactValidator(NewDefaultArtifactValidator()).WithRoot(root)
	_, err := v.ValidateArtifact([]byte(raw), "dup.txt")
	if !errors.Is(err, ErrAmbiguousAnchor) {
		t.Fatalf("err = %v, want ErrAmbiguousAnchor", err)
	}

	// Also test ResolveAnchors directly: 0 matches -> ErrFormatRejected
	t.Run("resolve_zero_matches", func(t *testing.T) {
		_, _, err := ResolveAnchors([]string{"nonexistent line"}, original)
		if !errors.Is(err, ErrFormatRejected) {
			t.Fatalf("zero matches err = %v, want ErrFormatRejected", err)
		}
	})
	t.Run("resolve_ambiguous", func(t *testing.T) {
		_, _, err := ResolveAnchors([]string{"common"}, original)
		if !errors.Is(err, ErrAmbiguousAnchor) {
			t.Fatalf("ambiguous err = %v, want ErrAmbiguousAnchor", err)
		}
	})
	t.Run("resolve_exact_match", func(t *testing.T) {
		s, e, err := ResolveAnchors([]string{"unique1"}, original)
		if err != nil {
			t.Fatalf("exact match failed: %v", err)
		}
		if s != 2 || e != 2 {
			t.Fatalf("exact match lines = %d-%d, want 2-2", s, e)
		}
	})
}

func TestNormalizer_RejectTruncationMarker(t *testing.T) {
	root := t.TempDir()
	original := "<!DOCTYPE html>\n<html>\n<body>\n<p>hello</p>\n</body>\n</html>\n"
	writeNormalizerTarget(t, root, "index.html", original)

	// Full-file output containing truncation marker.
	raw := "<!DOCTYPE html>\n<html>\n<body>\n<p>hello</p>\n<!-- rest of content -->\n</body>\n</html>\n"

	v := NewNormalizingArtifactValidator(NewDefaultArtifactValidator()).WithRoot(root)
	_, err := v.ValidateArtifact([]byte(raw), "index.html")
	if !errors.Is(err, ErrFormatRejected) {
		t.Fatalf("truncation err = %v, want ErrFormatRejected", err)
	}

	// Also test with // ... marker
	raw2 := "package main\n// ...\nfunc main() {}\n"
	writeNormalizerTarget(t, root, "main.go", "package main\nfunc main() { println(\"hi\") }\n")
	_, err = v.ValidateArtifact([]byte(raw2), "main.go")
	if !errors.Is(err, ErrFormatRejected) {
		t.Fatalf("truncation // ... err = %v, want ErrFormatRejected", err)
	}

	// DetectAndNormalize should also reject truncation during synthesis.
	t.Run("detect_truncation_in_full_file", func(t *testing.T) {
		_, err := DetectAndNormalize([]byte(raw), original)
		if !errors.Is(err, ErrFormatRejected) {
			t.Fatalf("DetectAndNormalize truncation err = %v, want ErrFormatRejected", err)
		}
	})
}

func TestNormalizer_DetectAndNormalize_FencedExtraction(t *testing.T) {
	original := "foo\nbar\nbaz\n"
	raw := "Here is the fix:\n```\nfoo\nqux\nbaz\n```\nThanks!"
	norm, err := DetectAndNormalize([]byte(raw), original)
	if err != nil {
		t.Fatalf("DetectAndNormalize failed: %v", err)
	}
	if !strings.Contains(string(norm), "<<<<<<< SEARCH") {
		t.Fatalf("expected SEARCH after fenced extraction, got %q", string(norm))
	}
}

func TestNormalizer_ResolveAnchors_WhitespaceFallback(t *testing.T) {
	// Target uses tabs, search uses spaces - should match via trimmed fallback.
	target := "func main() {\n\tprintln(\"hello\")\n}\n"
	search := []string{"  println(\"hello\")"}
	s, e, err := ResolveAnchors(search, target)
	if err != nil {
		t.Fatalf("whitespace-trimmed match failed: %v", err)
	}
	if s != 2 || e != 2 {
		t.Fatalf("lines = %d-%d, want 2-2", s, e)
	}
}
