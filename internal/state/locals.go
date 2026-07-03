package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	LocalDir         = ".izen"
	RuntimeMetaFile  = "runtime.meta"
	SessionFile      = "session.json"
	GraphDir         = "graph"
	GraphCacheFile   = "ast.db"
	SymbolsDBFile    = "symbols.db"
	DirtyOverlayFile = "dirty.overlay"
	HistoryDir       = "history"
	InputLogFile     = "input.log"
	EventsLogFile    = "events.log"
	AuditDir         = "audit"
	MutationsLogFile = "mutations.log"
	ShellLogFile     = "shell.log"
	CheckpointsDir   = "checkpoints"
	PatchesDir       = "patches"
)

type RuntimeMeta struct {
	LxVersion     string `json:"lx_version"`
	GraphVersion  string `json:"graph_version"`
	SchemaVersion string `json:"schema_version"`
}

func LocalPath(root string, elems ...string) string {
	all := append([]string{root, LocalDir}, elems...)
	return filepath.Join(all...)
}

func HasLocalState(root string) bool {
	sessionPath := filepath.Join(root, LocalDir, SessionFile)
	if fi, err := os.Stat(sessionPath); err == nil && !fi.IsDir() {
		return true
	}
	dir := filepath.Join(root, LocalDir)
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		return true
	}
	return false
}

func InitLocalState(root string) error {
	dirs := []string{
		LocalPath(root, GraphDir),
		LocalPath(root, HistoryDir),
		LocalPath(root, AuditDir),
		LocalPath(root, CheckpointsDir),
		LocalPath(root, PatchesDir),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	WriteRuntimeMeta(root, RuntimeMeta{
		LxVersion:     "0.1.0",
		GraphVersion:  "v1",
		SchemaVersion: "1",
	})
	return nil
}

func WriteRuntimeMeta(root string, meta RuntimeMeta) error {
	path := LocalPath(root, RuntimeMetaFile)
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func LoadRuntimeMeta(root string) (*RuntimeMeta, error) {
	path := LocalPath(root, RuntimeMetaFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var meta RuntimeMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func CheckVersion(root string, currentLxVersion string) error {
	meta, err := LoadRuntimeMeta(root)
	if err != nil {
		return fmt.Errorf("read runtime.meta: %w", err)
	}
	if meta == nil {
		return nil
	}
	if meta.LxVersion != "" && meta.LxVersion != currentLxVersion {
		fmt.Fprintf(os.Stderr, "izen: version mismatch — runtime.meta lx_version=%s, current=%s (degraded state)\n", meta.LxVersion, currentLxVersion)
		fmt.Fprintf(os.Stderr, "izen: suggest: remove %s and restart to rebuild\n", LocalPath(root, RuntimeMetaFile))
	}
	return nil
}

func MigrateLegacyFiles(root string) error {
	oldCache := filepath.Join(root, LocalDir, "graph.cache.v1")
	newCache := LocalPath(root, GraphDir, GraphCacheFile)
	if _, err := os.Stat(oldCache); err == nil {
		if _, err := os.Stat(newCache); os.IsNotExist(err) {
			if err := os.Rename(oldCache, newCache); err != nil {
				return fmt.Errorf("migrate graph.cache.v1: %w", err)
			}
		}
	}

	oldHistory := filepath.Join(root, LocalDir, "history.log")
	newHistory := LocalPath(root, HistoryDir, InputLogFile)
	if _, err := os.Stat(oldHistory); err == nil {
		if _, err := os.Stat(newHistory); os.IsNotExist(err) {
			data, err := os.ReadFile(oldHistory)
			if err == nil {
				os.WriteFile(newHistory, data, 0644)
			}
			os.Remove(oldHistory)
		}
	}

	return nil
}
