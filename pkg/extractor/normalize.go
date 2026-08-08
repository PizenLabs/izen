package extractor

import (
	"bytes"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/izen/pkg/ir"
)

// utf8BOM is the UTF-8 byte-order-mark prefix that must never reach disk.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// NormalizeArtifact applies the deterministic content and path normalization
// rules of the V3 Artifact Protocol and returns a new, normalized artifact.
// The input is never mutated.
//
// Content normalization:
//   - CRLF line endings ("\r\n") are converted to LF ("\n").
//   - A leading UTF-8 BOM (0xEF 0xBB 0xBF) is stripped.
//   - A trailing newline is enforced at EOF: file artifacts whose non-empty
//     content does not end in "\n" get one appended.
//
// Path normalization:
//   - Backslashes are converted to forward slashes and duplicate separators
//     are collapsed, so "src//main.go" and "src\main.go" both become
//     "src/main.go".
//   - A leading "./" is stripped and a leading "/" is removed, enforcing
//     workspace-relative paths.
//   - Any path that still resolves outside the workspace (a ".." prefix)
//     is rejected as empty, since an escaped path can never be a valid
//     workspace-relative contract.
//
// The SHA-256 Hash is recomputed from the normalized content so the artifact
// stays self-consistent, and ID defaults back to Path when empty.
func NormalizeArtifact(art ir.Artifact) ir.Artifact {
	out := art

	if art.Kind == ir.ArtifactFile {
		out.Content = normalizeContent(art.Content)
	}
	out.Path = normalizePath(art.Path)
	if out.ID == "" {
		out.ID = out.Path
	}
	if out.Hash != "" || art.Kind == ir.ArtifactFile {
		out.Hash = ir.ComputeHash(out.Content)
	}
	return out
}

// normalizeContent applies the byte-level content rules in a single pass:
// CRLF -> LF, BOM strip, and trailing-newline enforcement. It returns a fresh
// slice and never aliases src.
func normalizeContent(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}
	dst := make([]byte, 0, len(src)+1)
	start := 0
	if bytes.HasPrefix(src, utf8BOM) {
		start = len(utf8BOM)
	}
	for i := start; i < len(src); i++ {
		if src[i] == '\r' && i+1 < len(src) && src[i+1] == '\n' {
			dst = append(dst, '\n')
			i++
			continue
		}
		dst = append(dst, src[i])
	}
	if len(dst) > 0 && dst[len(dst)-1] != '\n' {
		dst = append(dst, '\n')
	}
	return dst
}

// normalizePath converts a declared artifact path into a clean
// workspace-relative path. Un-normalizable or escaping paths return "".
func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimPrefix(p, "/")
	p = filepath.ToSlash(filepath.Clean(p))
	p = strings.TrimPrefix(p, "/")
	if p == "." || p == "" {
		return ""
	}
	if p == ".." || strings.HasPrefix(p, "../") {
		return ""
	}
	return p
}
