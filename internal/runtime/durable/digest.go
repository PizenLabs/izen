package durable

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ComputeTreeDigest returns a deterministic SHA256 digest of the target
// worktree. It walks workDir (or a single file when target is a file),
// skipping .izen and .git, and hashes sorted "relpath\x00size\x00content"
// records. The result plays the role of the spec's "SHA256 / Git Tree
// Digest": stable across processes, sensitive to any byte change.
//
// An empty digest ("") is never returned for a readable workDir; errors
// are reported instead.
func ComputeTreeDigest(workDir string, scope ...string) (string, error) {
	if strings.TrimSpace(workDir) == "" {
		return "", fmt.Errorf("durable: empty workDir")
	}
	fi, err := os.Stat(workDir)
	if err != nil {
		return "", fmt.Errorf("durable: stat workDir: %w", err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("durable: workDir %q is not a directory", workDir)
	}

	roots := scope
	if len(roots) == 0 {
		roots = []string{"."}
	}
	h := sha256.New()
	names := make([]string, 0, 64)
	contents := make(map[string][]byte, 64)

	for _, r := range roots {
		abs := filepath.Join(workDir, filepath.FromSlash(r))
		absFi, err := os.Stat(abs)
		if err != nil {
			// A scope entry that does not exist contributes a tombstone
			// so create-vs-delete is digest-distinguishable.
			name := "missing:" + filepath.ToSlash(r)
			names = append(names, name)
			contents[name] = []byte("missing")
			continue
		}
		if !absFi.IsDir() {
			rel, _ := filepath.Rel(workDir, abs)
			name := filepath.ToSlash(rel)
			data, err := os.ReadFile(abs)
			if err != nil {
				return "", fmt.Errorf("durable: read %s: %w", rel, err)
			}
			names = append(names, name)
			contents[name] = data
			continue
		}
		err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				if path != abs {
					base := d.Name()
					if base == ".izen" || base == ".git" {
						return filepath.SkipDir
					}
				} else {
					// Skip top-level .izen/.git when walking workDir root.
					return nil
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(workDir, path)
			if err != nil {
				return err
			}
			relSlash := filepath.ToSlash(rel)
			first := strings.Split(relSlash, "/")[0]
			if first == ".izen" || first == ".git" {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				// File vanished mid-walk: treat as empty rather than
				// failing the whole digest.
				data = []byte{}
			}
			names = append(names, relSlash)
			contents[relSlash] = data
			return nil
		})
		if err != nil {
			return "", fmt.Errorf("durable: walk %s: %w", r, err)
		}
	}

	sort.Strings(names)
	for _, n := range names {
		data := contents[n]
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00", n, len(data))
		_, _ = h.Write(data)
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
