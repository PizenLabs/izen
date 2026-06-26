package execution

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Patch struct {
	ID        string    `json:"id"`
	File      string    `json:"file"`
	Original  string    `json:"original"`
	Modified  string    `json:"modified"`
	CreatedAt time.Time `json:"created_at"`
	Applied   bool      `json:"applied"`
}

type StagedPatch struct {
	File    string
	Content string
	RawDiff string
}

type PatchQueue struct {
	patches      []StagedPatch
	staged       bool
	mu           sync.Mutex
	pm           *PatchManager
	root         string
	contextFiles []string
}

func NewPatchQueue(root string, pm *PatchManager) *PatchQueue {
	return &PatchQueue{
		pm:   pm,
		root: root,
	}
}

func (pq *PatchQueue) SetContextFiles(files []string) {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	pq.contextFiles = files
}

func (pq *PatchQueue) validateContextTarget(file string) error {
	if len(pq.contextFiles) == 0 {
		return nil
	}
	for _, cf := range pq.contextFiles {
		if cf == file {
			return nil
		}
	}
	return fmt.Errorf("patch target %s is not in the active context files: %v", file, pq.contextFiles)
}

func (pq *PatchQueue) IsStaged() bool {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	return pq.staged
}

func (pq *PatchQueue) List() []StagedPatch {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	result := make([]StagedPatch, len(pq.patches))
	copy(result, pq.patches)
	return result
}

func (pq *PatchQueue) Stage(file, content, rawDiff string) {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	for i, p := range pq.patches {
		if p.File == file {
			pq.patches[i].Content = content
			pq.patches[i].RawDiff = rawDiff
			return
		}
	}
	pq.patches = append(pq.patches, StagedPatch{File: file, Content: content, RawDiff: rawDiff})
	pq.staged = true
}

func (pq *PatchQueue) ApplyNext() error {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	if len(pq.patches) == 0 {
		return fmt.Errorf("no staged patches")
	}
	p := pq.patches[0]
	if p.File == "" {
		return fmt.Errorf("staged patch has empty file path")
	}
	if err := pq.validateContextTarget(p.File); err != nil {
		return err
	}
	fullPath := filepath.Join(pq.root, p.File)
	if _, err := os.Stat(filepath.Dir(fullPath)); err != nil {
		return fmt.Errorf("target directory for %s does not exist: %w", p.File, err)
	}
	if p.Content == "" {
		return fmt.Errorf("staged patch for %s has empty content", p.File)
	}
	patch := &Patch{
		ID:       fmt.Sprintf("staged-%d", time.Now().UnixNano()),
		File:     p.File,
		Modified: p.Content,
	}
	orig, err := os.ReadFile(fullPath)
	if err == nil {
		patch.Original = string(orig)
	}
	if err := pq.pm.Apply(patch); err != nil {
		return err
	}
	pq.patches = pq.patches[1:]
	if len(pq.patches) == 0 {
		pq.staged = false
	}
	return nil
}

func (pq *PatchQueue) ApplyAll() (int, error) {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	applied := 0
	for _, p := range pq.patches {
		if p.File == "" {
			return applied, fmt.Errorf("staged patch has empty file path")
		}
		if err := pq.validateContextTarget(p.File); err != nil {
			return applied, err
		}
		if p.Content == "" {
			return applied, fmt.Errorf("staged patch for %s has empty content", p.File)
		}
		patch := &Patch{
			ID:       fmt.Sprintf("staged-%d", time.Now().UnixNano()),
			File:     p.File,
			Modified: p.Content,
		}
		orig, err := os.ReadFile(filepath.Join(pq.root, p.File))
		if err == nil {
			patch.Original = string(orig)
		}
		if err := pq.pm.Apply(patch); err != nil {
			return applied, err
		}
		applied++
	}
	pq.patches = nil
	pq.staged = false
	return applied, nil
}

func (pq *PatchQueue) Clear() {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	pq.patches = nil
	pq.staged = false
}

type PatchManager struct {
	root   string
	patDir string
}

func NewPatchManager(root string) *PatchManager {
	return &PatchManager{
		root:   root,
		patDir: filepath.Join(root, ".izen", "patches"),
	}
}

func (pm *PatchManager) Capture(file string) (*Patch, error) {
	fullPath := filepath.Join(pm.root, file)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}

	patch := &Patch{
		ID:        fmt.Sprintf("pat-%d", time.Now().UnixNano()),
		File:      file,
		Original:  string(data),
		CreatedAt: time.Now(),
		Applied:   true,
	}

	if err := pm.store(patch); err != nil {
		return nil, err
	}

	return patch, nil
}

func (pm *PatchManager) Apply(patch *Patch) error {
	if patch.File == "" {
		return fmt.Errorf("patch has empty file path")
	}
	if patch.Modified == "" {
		return fmt.Errorf("patch for %s has empty content", patch.File)
	}
	cleaned := filepath.Clean(patch.File)
	if cleaned == "." || cleaned == "/" {
		return fmt.Errorf("invalid patch file path: %s", patch.File)
	}
	if strings.Contains(cleaned, "..") {
		return fmt.Errorf("path traversal detected in patch file: %s", patch.File)
	}
	fullPath := filepath.Join(pm.root, cleaned)
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	if err := os.WriteFile(fullPath, []byte(patch.Modified), 0644); err != nil {
		return fmt.Errorf("write %s: %w", patch.File, err)
	}

	patch.Applied = true
	return pm.store(patch)
}

func (pm *PatchManager) Rollback(patchID string) error {
	patch, err := pm.Load(patchID)
	if err != nil {
		return err
	}

	if !patch.Applied {
		return fmt.Errorf("patch %s is not applied", patchID)
	}

	fullPath := filepath.Join(pm.root, patch.File)
	if err := os.WriteFile(fullPath, []byte(patch.Original), 0644); err != nil {
		return fmt.Errorf("rollback write %s: %w", patch.File, err)
	}

	patch.Applied = false
	return pm.store(patch)
}

func (pm *PatchManager) Load(id string) (*Patch, error) {
	path := filepath.Join(pm.patDir, id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load patch %s: %w", id, err)
	}

	var p Patch
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("decode patch %s: %w", id, err)
	}

	return &p, nil
}

func (pm *PatchManager) List() ([]Patch, error) {
	entries, err := os.ReadDir(pm.patDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var patches []Patch
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		p, err := pm.Load(id)
		if err != nil {
			continue
		}
		patches = append(patches, *p)
	}

	return patches, nil
}

func (pm *PatchManager) Remove(id string) error {
	path := filepath.Join(pm.patDir, id+".json")
	return os.Remove(path)
}

func (pm *PatchManager) store(patch *Patch) error {
	if err := os.MkdirAll(pm.patDir, 0755); err != nil {
		return err
	}

	path := filepath.Join(pm.patDir, patch.ID+".json")
	data, err := json.MarshalIndent(patch, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}
