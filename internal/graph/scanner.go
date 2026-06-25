package graph

import (
	"os"
	"path/filepath"
	"strings"
)

type ScanConfig struct {
	Root         string
	ExcludeDirs  []string
	ExcludeFiles []string
	MaxSize      int64
}

func DefaultScanConfig(root string) ScanConfig {
	return ScanConfig{
		Root:    root,
		MaxSize: 1 << 20,
		ExcludeDirs: []string{
			".git", ".izen", ".opencode", "node_modules",
			"vendor", "dist", "build", "__pycache__",
			".next", ".nuxt", ".cache",
		},
		ExcludeFiles: []string{
			"package-lock.json", "yarn.lock", "pnpm-lock.yaml",
			"go.sum", ".DS_Store",
		},
	}
}

type ScanResult struct {
	Files []FileInfo
}

type FileInfo struct {
	Path  string
	Ext   string
	Lang  Language
	Size  int64
	Lines int
}

func Scan(cfg ScanConfig) (*ScanResult, error) {
	excludeDirs := make(map[string]bool)
	for _, d := range cfg.ExcludeDirs {
		excludeDirs[d] = true
	}
	excludeFiles := make(map[string]bool)
	for _, f := range cfg.ExcludeFiles {
		excludeFiles[f] = true
	}

	var result ScanResult
	root := cfg.Root

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		if info.IsDir() {
			name := info.Name()
			if name != "." && excludeDirs[name] {
				return filepath.SkipDir
			}
			return nil
		}

		if excludeFiles[info.Name()] {
			return nil
		}

		if cfg.MaxSize > 0 && info.Size() > cfg.MaxSize {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		lang, ok := LangFromExt(ext)
		if !ok {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}

		result.Files = append(result.Files, FileInfo{
			Path: rel,
			Ext:  ext,
			Lang: lang,
			Size: info.Size(),
		})

		return nil
	})

	if err != nil {
		return nil, err
	}

	return &result, nil
}