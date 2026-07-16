package git

import (
	"context"
	"fmt"
	"os/exec"
)

func InitRepo(root string) error {
	cmd := exec.CommandContext(context.Background(), "git", "init", "-b", "main")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git init failed: %w\n%s", err, string(out))
	}
	return nil
}
