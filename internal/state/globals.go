package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	GlobalDirName        = ".izen"
	GlobalConfigFile     = "izen.conf.yml"
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
