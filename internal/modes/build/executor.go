package build

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type FileMutation struct {
	File    string
	Content string
	Mode    MutationMode
}

type MutationMode int

const (
	ModeDiff MutationMode = iota
	ModeFullRewrite
)

type Executor struct {
	root   string
	engine *Engine
}

func NewExecutor(root string, engine *Engine) *Executor {
	return &Executor{
		root:   root,
		engine: engine,
	}
}

func (ex *Executor) ApplyMutation(ctx context.Context, mut FileMutation) error {
	absPath := filepath.Join(ex.root, mut.File)
	dir := filepath.Dir(absPath)

	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}

	if mut.Mode == ModeFullRewrite {
		return os.WriteFile(absPath, []byte(mut.Content), 0644)
	}

	return os.WriteFile(absPath, []byte(mut.Content), 0644)
}

func (ex *Executor) VerifyCompilation(ctx context.Context, packages ...string) (bool, string, error) {
	args := []string{"build"}
	if len(packages) > 0 {
		args = append(args, packages...)
	} else {
		args = append(args, "./...")
	}

	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = ex.root
	output, err := cmd.CombinedOutput()

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false, string(output), nil
		}
		return false, string(output), err
	}
	return true, "", nil
}

func (ex *Executor) CheckAndRecover(ctx context.Context, file string, content string, packages ...string) (bool, string, error) {
	ok, output, err := ex.VerifyCompilation(ctx, packages...)
	if err != nil {
		return false, output, err
	}
	if ok {
		ex.engine.RecordCompilationSuccess()
		return true, "", nil
	}

	ex.engine.RecordCompilationFailure(file)

	if ex.engine.MustRewriteEntireFile(file) {
		mut := FileMutation{
			File:    file,
			Content: content,
			Mode:    ModeFullRewrite,
		}
		if err := ex.ApplyMutation(ctx, mut); err != nil {
			return false, output, fmt.Errorf("force rewrite failed: %w", err)
		}
	}

	return false, output, nil
}

func ParseBuildOutput(output string) []FileMutation {
	var mutations []FileMutation
	lines := strings.Split(output, "\n")

	var currentFile string
	var currentContent strings.Builder
	var inBlock bool
	var inDiff bool

	flush := func() {
		if currentFile != "" && currentContent.Len() > 0 {
			mode := ModeDiff
			if inDiff {
				mode = ModeDiff
			}
			mutations = append(mutations, FileMutation{
				File:    currentFile,
				Content: strings.TrimSpace(currentContent.String()),
				Mode:    mode,
			})
			currentFile = ""
			currentContent.Reset()
			inBlock = false
			inDiff = false
		}
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "FILE:") {
			flush()
			currentFile = strings.TrimSpace(strings.TrimPrefix(trimmed, "FILE:"))
			continue
		}

		if strings.HasPrefix(trimmed, "--- a/") {
			flush()
			filePart := strings.TrimPrefix(trimmed, "--- a/")
			filePart = strings.TrimSpace(filePart)
			if idx := strings.IndexAny(filePart, " \t"); idx != -1 {
				filePart = filePart[:idx]
			}
			currentFile = filePart
			inDiff = true
			currentContent.WriteString(line)
			currentContent.WriteString("\n")
			continue
		}

		if strings.HasPrefix(trimmed, "+++ b/") {
			currentContent.WriteString(line)
			currentContent.WriteString("\n")
			continue
		}

		if strings.HasPrefix(trimmed, "@@") && strings.Contains(trimmed, "@@") {
			currentContent.WriteString(line)
			currentContent.WriteString("\n")
			continue
		}

		if strings.HasPrefix(line, "```") {
			if inBlock {
				flush()
			} else {
				flush()
				inBlock = true
			}
			continue
		}

		if currentFile != "" {
			currentContent.WriteString(line)
			currentContent.WriteString("\n")
		}
	}

	flush()
	return mutations
}
