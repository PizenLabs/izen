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

const binaryName = "lx"

var lxBinPath string

func globalBinaryDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".izen", "runtime")
}

func localBinaryDir(root string) string {
	return filepath.Join(root, ".izen", "bin")
}

func BinaryBytes() ([]byte, error) {
	return lxBinary.ReadFile("bin/lx")
}

func BinaryPath() string {
	return lxBinPath
}

func targetPath(dir string) string {
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	return filepath.Join(dir, binaryName+suffix)
}

func UnpackBinary(root string) (string, error) {
	globalDir := globalBinaryDir()
	if err := os.MkdirAll(globalDir, 0755); err != nil {
		return "", fmt.Errorf("mkdir global runtime: %w", err)
	}

	target := targetPath(globalDir)

	existed := false
	if _, err := os.Stat(target); err == nil {
		existed = true
		lxBinPath = target
		return target, nil
	}

	legacyTarget := targetPath(localBinaryDir(root))
	if _, err := os.Stat(legacyTarget); err == nil {
		lxBinPath = legacyTarget
		return legacyTarget, nil
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
