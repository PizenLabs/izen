package symbol

import (
	"path/filepath"
	"strings"
)

var defaultIgnoreDirs = map[string]bool{
	".git":         true,
	".idea":        true,
	".vscode":      true,
	"node_modules": true,
	"venv":         true,
	".venv":        true,
	"env":          true,
	"__pycache__":  true,
	"target":       true,
	"vendor":       true,
	"build":        true,
	"dist":         true,
	".next":        true,
	"bin":          true,
	"obj":          true,
}

func ShouldIgnoreDir(dirName string) bool {
	return defaultIgnoreDirs[dirName]
}

func ShouldIgnorePath(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}

	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts {
		if defaultIgnoreDirs[part] {
			return true
		}
	}
	return false
}
