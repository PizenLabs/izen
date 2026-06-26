//go:build lynx_embed

package lynx

import (
	"embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

//go:embed bin/lx
var lxBinary embed.FS

const binaryDir = ".izen/bin"
const binaryName = "lx"

var lxBinPath string

func BinaryPath() string {
	return lxBinPath
}

func UnpackBinary(root string) (string, error) {
	targetDir := filepath.Join(root, binaryDir)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return "", fmt.Errorf("mkdir bin: %w", err)
	}

	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	target := filepath.Join(targetDir, binaryName+suffix)

	existed := false
	if _, err := os.Stat(target); err == nil {
		existed = true
		return target, nil
	}

	src, err := lxBinary.Open("bin/lx")
	if err != nil {
		return "", fmt.Errorf("open embedded lx: %w", err)
	}
	defer src.Close()

	tmpTarget := target + ".tmp"
	dst, err := os.OpenFile(tmpTarget, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return "", fmt.Errorf("create lx binary: %w", err)
	}

	written, err := io.Copy(dst, src)
	if err != nil {
		dst.Close()
		os.Remove(tmpTarget)
		return "", fmt.Errorf("write lx binary: %w", err)
	}
	dst.Close()

	if err := os.Rename(tmpTarget, target); err != nil {
		os.Remove(tmpTarget)
		return "", fmt.Errorf("rename lx binary: %w", err)
	}

	lxBinPath = target

	if !existed {
		fmt.Fprintf(os.Stderr, "izen: unpacked lynx binary (%d bytes) to %s\n", written, target)
	}

	return target, nil
}

func EnsureBinary(root string) (string, error) {
	if lxBinPath != "" {
		if _, err := os.Stat(lxBinPath); err == nil {
			return lxBinPath, nil
		}
	}
	return UnpackBinary(root)
}

func MustUnpack(root string) string {
	path, err := EnsureBinary(root)
	if err != nil {
		panic(fmt.Sprintf("lynx binary unpack: %v", err))
	}
	return path
}
