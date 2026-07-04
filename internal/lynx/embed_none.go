//go:build !lynx_embed

package lynx

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

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
	return nil, fmt.Errorf("lynx binary not embedded; build with -tags lynx_embed")
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

	if _, err := os.Stat(target); err == nil {
		lxBinPath = target
		return target, nil
	}

	legacyTarget := targetPath(localBinaryDir(root))
	if _, err := os.Stat(legacyTarget); err == nil {
		lxBinPath = legacyTarget
		return legacyTarget, nil
	}

	lynxSourceDir := filepath.Join(root, "lynx")
	if info, err := os.Stat(lynxSourceDir); err == nil && info.IsDir() {
		cargoPath, err := exec.LookPath("cargo")
		if err == nil {
			fmt.Fprintf(os.Stderr, "izen: building lynx from source...\n")
			build := exec.CommandContext(context.Background(), cargoPath, "build", "--release")
			build.Dir = lynxSourceDir
			build.Stdout = os.Stderr
			build.Stderr = os.Stderr
			if err := build.Run(); err == nil {
				suffix := ""
				if runtime.GOOS == "windows" {
					suffix = ".exe"
				}
				builtBinary := filepath.Join(lynxSourceDir, "target", "release", binaryName+suffix)
				if _, err := os.Stat(builtBinary); err == nil {
					input, err := os.ReadFile(builtBinary)
					if err == nil {
						if err := os.WriteFile(target, input, 0755); err == nil {
							lxBinPath = target
							fmt.Fprintf(os.Stderr, "izen: lynx built and copied to %s\n", target)
							return target, nil
						}
					}
				}
			}
		}
	}

	if path, err := exec.LookPath("lx"); err == nil {
		lxBinPath = path
		return path, nil
	}

	return "", fmt.Errorf("lynx binary not found: try 'cd lynx && cargo build --release && cp target/release/lx internal/lynx/bin/lx'")
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
		panic(fmt.Sprintf("lynx binary: %v", err))
	}
	return path
}
