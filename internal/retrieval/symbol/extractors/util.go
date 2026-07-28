package extractors

import (
	"os"
	"path/filepath"
	"strings"
)

func fileExists(root, filename string) bool {
	_, err := os.Stat(filepath.Join(root, filename))
	return err == nil
}

func hasExtension(path string, ext string) bool {
	return strings.HasSuffix(strings.ToLower(path), ext)
}
