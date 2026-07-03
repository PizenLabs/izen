package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/PizenLabs/izen/internal/lynx"
)

const (
	GlobalDirName        = ".izen"
	GlobalConfigFile     = "config.yml"
	GlobalCredentialsDir = "credentials"
	GlobalProvidersFile  = "providers.json"
	GlobalRuntimeDir     = "runtime"
	GlobalLogsDir        = "logs"
)

func GlobalHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, GlobalDirName), nil
}

func GlobalPath(elems ...string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	all := append([]string{home, GlobalDirName}, elems...)
	return filepath.Join(all...)
}

func EnsureRuntimeBinaries() error {
	rtDir := GlobalPath(GlobalRuntimeDir)
	if err := os.MkdirAll(rtDir, 0755); err != nil {
		return fmt.Errorf("runtime dir: %w", err)
	}

	target := filepath.Join(rtDir, "lx")
	if _, err := os.Stat(target); err == nil {
		return nil
	}

	data, err := lynx.BinaryBytes()
	if err != nil {
		return fmt.Errorf("read embedded lx binary: %w", err)
	}

	if err := os.WriteFile(target, data, 0755); err != nil {
		return fmt.Errorf("write lx binary: %w", err)
	}

	fmt.Fprintf(os.Stderr, "izen: extracted lx binary (%d bytes) to %s\n", len(data), target)
	return nil
}

func InitGlobalState() error {
	dirs := []string{
		GlobalPath(),
		GlobalPath(GlobalCredentialsDir),
		GlobalPath(GlobalRuntimeDir),
		GlobalPath(GlobalLogsDir),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}

	providersPath := GlobalPath(GlobalCredentialsDir, GlobalProvidersFile)
	if _, err := os.Stat(providersPath); os.IsNotExist(err) {
		placeholder := map[string]interface{}{
			"encrypted_providers": []string{},
			"note":                "Use 'izen auth login' to configure cloud providers. This file is permission-locked (chmod 600).",
		}
		data, _ := json.MarshalIndent(placeholder, "", "  ")
		if err := os.WriteFile(providersPath, data, 0600); err != nil {
			return fmt.Errorf("write providers.json: %w", err)
		}
	}

	info, err := os.Stat(providersPath)
	if err == nil {
		if info.Mode().Perm() != 0600 {
			os.Chmod(providersPath, 0600)
		}
	}

	return nil
}
