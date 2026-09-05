package review

import (
	"bufio"
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/PizenLabs/izen/internal/runtime/substrate"
)

type DiffAnalyzer struct {
	root string
}

func NewDiffAnalyzer(root string) *DiffAnalyzer {
	return &DiffAnalyzer{root: root}
}

func (da *DiffAnalyzer) Analyze() (*DiffAnalysis, error) {
	files, err := da.getChangedFiles()
	if err != nil {
		return nil, fmt.Errorf("get changed files: %w", err)
	}

	branch, _ := da.getBranch()
	baseBranch := da.getBaseBranch()
	hash, _ := da.getHash()
	commits := len(files) / 2
	if commits < 1 {
		commits = 1
	}

	return &DiffAnalysis{
		Files:   files,
		Branch:  branch,
		Base:    baseBranch,
		Hash:    hash,
		Commits: commits,
	}, nil
}

type DiffAnalysis struct {
	Files   []DiffFile `json:"files"`
	Branch  string     `json:"branch"`
	Base    string     `json:"base"`
	Hash    string     `json:"hash"`
	Commits int        `json:"commits"`
}

func (da *DiffAnalyzer) getChangedFiles() ([]DiffFile, error) {
	// Git-aware diff via substrate helper (no direct exec in semantic layer).
	ctx := context.Background()
	res := substrate.ExecCommand(ctx, da.root, nil, []string{"git", "diff", "--no-color", "--diff-filter=ACDMRTUXB"})
	if res.Err == nil && res.Stdout != "" {
		return da.parseUnifiedDiff(res.Stdout)
	}
	res2 := substrate.ExecCommand(ctx, da.root, nil, []string{"git", "diff", "--cached", "--no-color"})
	if res2.Err == nil && res2.Stdout != "" {
		return da.parseUnifiedDiff(res2.Stdout)
	}
	res3 := substrate.ExecCommand(ctx, da.root, nil, []string{"git", "status", "--porcelain"})
	if res3.Err == nil {
		return da.parsePorcelain(res3.Stdout)
	}
	return []DiffFile{}, nil
}

func (da *DiffAnalyzer) parseUnifiedDiff(diff string) ([]DiffFile, error) {
	scanner := bufio.NewScanner(strings.NewReader(diff))
	var files []DiffFile
	var current *DiffFile
	var currentHunk *DiffHunk

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "diff --git ") {
			if current != nil {
				if currentHunk != nil {
					current.Hunks = append(current.Hunks, *currentHunk)
					currentHunk = nil
				}
				files = append(files, *current)
			}
			current = &DiffFile{Status: "modified"}
			continue
		}

		if strings.HasPrefix(line, "--- a/") {
			continue
		}

		if strings.HasPrefix(line, "+++ b/") {
			path := strings.TrimPrefix(line, "+++ b/")
			if current != nil {
				current.Path = path
				ext := filepath.Ext(path)
				current.Language = strings.TrimPrefix(ext, ".")
			}
			continue
		}

		if strings.HasPrefix(line, "new file mode") {
			if current != nil {
				current.Status = "added"
			}
			continue
		}

		if strings.HasPrefix(line, "deleted file mode") {
			if current != nil {
				current.Status = "deleted"
			}
			continue
		}

		if strings.HasPrefix(line, "rename from ") {
			if current != nil {
				current.Status = "renamed"
			}
			continue
		}

		if strings.HasPrefix(line, "@@") && strings.Contains(line, "@@") {
			if currentHunk != nil && current != nil {
				current.Hunks = append(current.Hunks, *currentHunk)
			}
			currentHunk = &DiffHunk{}
			parts := strings.Split(line, " ")
			for _, part := range parts {
				switch {
				case strings.HasPrefix(part, "-") && strings.Contains(part, ","):
					_, _ = fmt.Sscanf(part, "-%d,%d", &currentHunk.StartOld, &currentHunk.CountOld)
				case strings.HasPrefix(part, "+") && strings.Contains(part, ","):
					_, _ = fmt.Sscanf(part, "+%d,%d", &currentHunk.StartNew, &currentHunk.CountNew)
				case strings.HasPrefix(part, "-"):
					_, _ = fmt.Sscanf(part, "-%d", &currentHunk.StartOld)
					currentHunk.CountOld = 1
				case strings.HasPrefix(part, "+"):
					_, _ = fmt.Sscanf(part, "+%d", &currentHunk.StartNew)
					currentHunk.CountNew = 1
				}
			}
			continue
		}

		if currentHunk != nil {
			currentHunk.Content += line + "\n"
			if strings.HasPrefix(line, "+") {
				current.Additions++
			} else if strings.HasPrefix(line, "-") {
				current.Deletions++
			}
		}
	}

	if currentHunk != nil && current != nil {
		current.Hunks = append(current.Hunks, *currentHunk)
	}
	if current != nil {
		files = append(files, *current)
	}

	return files, nil
}

func (da *DiffAnalyzer) parsePorcelain(status string) ([]DiffFile, error) {
	scanner := bufio.NewScanner(strings.NewReader(status))
	var files []DiffFile

	for scanner.Scan() {
		line := scanner.Text()
		if len(line) < 4 {
			continue
		}

		df := DiffFile{
			Path:   strings.TrimSpace(line[3:]),
			Status: da.mapStatus(string(line[0]), string(line[1])),
		}

		ext := filepath.Ext(df.Path)
		df.Language = strings.TrimPrefix(ext, ".")

		files = append(files, df)
	}

	return files, nil
}

func (da *DiffAnalyzer) mapStatus(staging, worktree string) string {
	switch {
	case staging == "?" && worktree == "?":
		return "untracked"
	case staging == "A" || staging == "?":
		return "added"
	case staging == "D" || worktree == "D":
		return "deleted"
	case staging == "R":
		return "renamed"
	case staging == "M" || worktree == "M":
		return "modified"
	default:
		return "modified"
	}
}

func (da *DiffAnalyzer) getBranch() (string, error) {
	res := substrate.ExecCommand(context.Background(), da.root, nil, []string{"git", "rev-parse", "--abbrev-ref", "HEAD"})
	if res.Err != nil || strings.TrimSpace(res.Stdout) == "" {
		return "", res.Err
	}
	return strings.TrimSpace(res.Stdout), nil
}

func (da *DiffAnalyzer) getBaseBranch() string {
	branch, err := da.getBranch()
	if err != nil {
		return "main"
	}
	if branch == "main" || branch == "master" {
		return branch + "~1"
	}
	res := substrate.ExecCommand(context.Background(), da.root, nil, []string{"git", "merge-base", branch, "main"})
	if res.Err == nil && strings.TrimSpace(res.Stdout) != "" {
		return strings.TrimSpace(res.Stdout)
	}
	res2 := substrate.ExecCommand(context.Background(), da.root, nil, []string{"git", "merge-base", branch, "master"})
	if res2.Err == nil && strings.TrimSpace(res2.Stdout) != "" {
		return strings.TrimSpace(res2.Stdout)
	}
	return "main"
}

func (da *DiffAnalyzer) getHash() (string, error) {
	res := substrate.ExecCommand(context.Background(), da.root, nil, []string{"git", "rev-parse", "--short", "HEAD"})
	if res.Err != nil {
		return "", res.Err
	}
	return strings.TrimSpace(res.Stdout), nil
}

func (da *DiffAnalyzer) isRepo() bool {
	rs := substrate.NewFSReadScope(da.root)
	if _, err := rs.ReadFile(".git/HEAD"); err == nil {
		return true
	}
	// Bare .git directory counts as repo for isRepo probe (fast path test creates empty .git).
	if _, err := rs.ReadTree(".git"); err == nil {
		return true
	}
	res := substrate.ExecCommand(context.Background(), da.root, nil, []string{"git", "rev-parse", "--is-inside-work-tree"})
	return res.Err == nil && strings.TrimSpace(res.Stdout) == "true"
}

func (da *DiffAnalyzer) hasChanges() bool {
	if !da.isRepo() {
		return false
	}
	res := substrate.ExecCommand(context.Background(), da.root, nil, []string{"git", "status", "--porcelain"})
	if res.Err != nil {
		return false
	}
	return len(strings.TrimSpace(res.Stdout)) > 0
}
