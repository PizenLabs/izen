package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Archetype represents the detected project type.
type Archetype string

const (
	VANILLA_WEB  Archetype = "VANILLA_WEB"
	GO_MODULE    Archetype = "GO_MODULE"
	NODE_APP     Archetype = "NODE_APP"
	RUST_CARGO   Archetype = "RUST_CARGO"
	PYTHON_ENV   Archetype = "PYTHON_ENV"
	GENERIC_TEXT Archetype = "GENERIC_TEXT"
)

// FileInfo stores metadata about a single file in the workspace file tree.
type FileInfo struct {
	Path        string    `json:"path"`
	Size        int64     `json:"size"`
	ModTime     time.Time `json:"mod_time"`
	ContentHash string    `json:"content_hash,omitempty"`
}

// GitStatus holds the current git branch and dirty state.
type GitStatus struct {
	Branch  string `json:"branch"`
	IsDirty bool   `json:"is_dirty"`
	HasGit  bool   `json:"has_git"`
}

// WorkspaceSnapshot is an immutable-by-read view of the workspace state.
// Read operations use an in-memory cache and never hit raw disk during a
// single pipeline run. Call Refresh() to invalidate and rebuild after
// file mutations.
type WorkspaceSnapshot struct {
	ID        string              `json:"id"`
	RootPath  string              `json:"root_path"`
	Archetype Archetype           `json:"archetype"`
	FileTree  map[string]FileInfo `json:"file_tree"`
	Manifests map[string]bool     `json:"manifests"`
	GitStatus GitStatus           `json:"git_status"`
	CreatedAt time.Time           `json:"created_at"`
}

// SnapshotCache provides thread-safe caching of a single WorkspaceSnapshot.
// GetSnapshot returns the cached version or builds one on cache miss.
// Refresh forces a rebuild from disk.
type SnapshotCache struct {
	mu      sync.RWMutex
	current *WorkspaceSnapshot
}

// NewSnapshotCache returns an empty cache ready for use.
func NewSnapshotCache() *SnapshotCache {
	return &SnapshotCache{}
}

// Current returns the cached snapshot or nil if none has been built.
func (sc *SnapshotCache) Current() *WorkspaceSnapshot {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	if sc.current == nil {
		return nil
	}
	return sc.current
}

// GetSnapshot returns the cached snapshot for root, building and caching it
// on first call or after a prior Refresh. It is safe for concurrent use.
func (sc *SnapshotCache) GetSnapshot(root string) (*WorkspaceSnapshot, error) {
	sc.mu.RLock()
	if sc.current != nil && sc.current.RootPath == root {
		snap := sc.current
		sc.mu.RUnlock()
		return snap, nil
	}
	sc.mu.RUnlock()

	return sc.buildAndCache(root)
}

// Refresh invalidates the in-memory cache and rebuilds the snapshot from
// disk. Call after any successful file mutation (e.g., APPLY_PATCH).
func (sc *SnapshotCache) Refresh(root string) (*WorkspaceSnapshot, error) {
	return sc.buildAndCache(root)
}

func (sc *SnapshotCache) buildAndCache(root string) (*WorkspaceSnapshot, error) {
	snap, err := BuildSnapshot(root)
	if err != nil {
		return nil, err
	}

	sc.mu.Lock()
	sc.current = snap
	sc.mu.Unlock()
	return snap, nil
}

// BuildSnapshot constructs a fresh WorkspaceSnapshot by scanning the
// given root directory. It does not consult or update the cache.
func BuildSnapshot(root string) (*WorkspaceSnapshot, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("snapshot: cannot access %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("snapshot: %s is not a directory", root)
	}

	root, err = filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("snapshot: cannot resolve %s: %w", root, err)
	}

	archetype := DetectArchetype(root)
	fileTree := buildFileTree(root)
	manifests := detectManifests(root)
	gitStatus := detectGitStatus(root)

	return &WorkspaceSnapshot{
		ID:        fmt.Sprintf("%x", sha256.Sum256([]byte(root+time.Now().String())))[:16],
		RootPath:  root,
		Archetype: archetype,
		FileTree:  fileTree,
		Manifests: manifests,
		GitStatus: gitStatus,
		CreatedAt: time.Now(),
	}, nil
}

// FileCount returns the number of tracked files in the snapshot.
func (ws *WorkspaceSnapshot) FileCount() int {
	return len(ws.FileTree)
}

// HasManifest reports whether the given manifest filename exists.
func (ws *WorkspaceSnapshot) HasManifest(name string) bool {
	return ws.Manifests[name]
}

// DetectArchetype determines the project archetype by inspecting manifest
// files and source extensions in root.
func DetectArchetype(root string) Archetype {
	if hasFile(root, "go.mod") {
		return GO_MODULE
	}
	if hasFile(root, "Cargo.toml") {
		return RUST_CARGO
	}
	if hasFile(root, "package.json") {
		return NODE_APP
	}
	if hasFile(root, "requirements.txt") ||
		hasFile(root, "setup.py") ||
		hasFile(root, "pyproject.toml") ||
		hasFile(root, "Pipfile") {
		return PYTHON_ENV
	}
	if hasAnyExtension(root, ".html", ".css", ".js") {
		return VANILLA_WEB
	}
	return GENERIC_TEXT
}

// buildFileTree walks root and collects metadata for every non-hidden,
// non-vendor file. Binary files larger than 10 MB are hashed by size only.
func buildFileTree(root string) map[string]FileInfo {
	tree := make(map[string]FileInfo)
	skipDirs := map[string]bool{
		".git": true, "node_modules": true, "vendor": true,
		".izen": true, ".codebase-memory": true, ".idea": true,
		"__pycache__": true, ".DS_Store": true,
	}

	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return filepath.SkipDir
		}
		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if rel == "" {
			return nil
		}
		if strings.HasPrefix(rel, ".") || strings.HasPrefix(rel, ".zen") {
			return nil
		}
		if info.Size() == 0 {
			tree[rel] = FileInfo{
				Path:    rel,
				Size:    0,
				ModTime: info.ModTime(),
			}
			return nil
		}
		fi := FileInfo{
			Path:    rel,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		}
		if info.Size() < 10*1024*1024 {
			if hash, err := hashFile(path); err == nil {
				fi.ContentHash = hash
			}
		}
		tree[rel] = fi
		return nil
	})

	return tree
}

// detectManifests checks for the presence of key configuration files at root.
func detectManifests(root string) map[string]bool {
	manifestNames := []string{
		"go.mod", "go.sum",
		"package.json", "package-lock.json",
		"index.html",
		"Cargo.toml", "Cargo.lock",
		"requirements.txt", "setup.py", "pyproject.toml", "Pipfile",
		"Makefile", "Dockerfile",
		"pom.xml", "build.gradle", "build.gradle.kts",
		"Gemfile", "composer.json", "CMakeLists.txt",
		".gitignore", "README.md",
	}
	m := make(map[string]bool, len(manifestNames))
	for _, name := range manifestNames {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			m[name] = true
		}
	}
	return m
}

// detectGitStatus returns the current git branch and dirty state.
func detectGitStatus(root string) GitStatus {
	gs := GitStatus{HasGit: false}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	branch, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return gs
	}
	gs.HasGit = true
	gs.Branch = strings.TrimSpace(string(branch))

	porcelain, err := exec.CommandContext(ctx, "git", "-C", root, "status", "--porcelain").Output()
	if err == nil && len(strings.TrimSpace(string(porcelain))) > 0 {
		gs.IsDirty = true
	}

	return gs
}

// hasFile reports whether name exists as a regular file in root.
func hasFile(root, name string) bool {
	info, err := os.Stat(filepath.Join(root, name))
	return err == nil && !info.IsDir()
}

// hasAnyExtension reports whether root contains any file with one of the
// given extensions (shallow scan, one level deep).
func hasAnyExtension(root string, exts ...string) bool {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	extSet := make(map[string]bool, len(exts))
	for _, e := range exts {
		extSet[e] = true
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if extSet[strings.ToLower(filepath.Ext(entry.Name()))] {
			return true
		}
	}
	return false
}

// hashFile computes the SHA-256 hex digest of a file.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
