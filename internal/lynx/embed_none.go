//go:build !lynx_embed

package lynx

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

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

	if _, err := os.Stat(target); err == nil {
		lxBinPath = target
		return target, nil
	}

	lynxSourceDir := filepath.Join(root, "lynx")
	if info, err := os.Stat(lynxSourceDir); err == nil && info.IsDir() {
		cargoPath, err := exec.LookPath("cargo")
		if err == nil {
			fmt.Fprintf(os.Stderr, "izen: building lynx from source...\n")
			build := exec.Command(cargoPath, "build", "--release")
			build.Dir = lynxSourceDir
			build.Stdout = os.Stderr
			build.Stderr = os.Stderr
			if err := build.Run(); err == nil {
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