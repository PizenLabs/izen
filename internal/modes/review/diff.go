package review

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	cmd := exec.Command("git", "diff", "--no-color", "--diff-filter=ACDMRTUXB")
	cmd.Dir = da.root
	out, err := cmd.Output()
	if err != nil {
		cmd2 := exec.Command("git", "diff", "--cached", "--no-color")
		cmd2.Dir = da.root
		out2, err2 := cmd2.Output()
		if err2 != nil {
			cmd3 := exec.Command("git", "status", "--porcelain")
			cmd3.Dir = da.root
			out3, _ := cmd3.Output()
			return da.parsePorcelain(string(out3))
		}
		return da.parseUnifiedDiff(string(out2))
	}
	return da.parseUnifiedDiff(string(out))
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
				if strings.HasPrefix(part, "-") && strings.Contains(part, ",") {
					fmt.Sscanf(part, "-%d,%d", &currentHunk.StartOld, &currentHunk.CountOld)
				} else if strings.HasPrefix(part, "+") && strings.Contains(part, ",") {
					fmt.Sscanf(part, "+%d,%d", &currentHunk.StartNew, &currentHunk.CountNew)
				} else if strings.HasPrefix(part, "-") {
					fmt.Sscanf(part, "-%d", &currentHunk.StartOld)
					currentHunk.CountOld = 1
				} else if strings.HasPrefix(part, "+") {
					fmt.Sscanf(part, "+%d", &currentHunk.StartNew)
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
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = da.root
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (da *DiffAnalyzer) getBaseBranch() string {
	branch, err := da.getBranch()
	if err != nil {
		return "main"
	}
	if branch == "main" || branch == "master" {
		return branch + "~1"
	}

	cmd := exec.Command("git", "merge-base", branch, "main")
	cmd.Dir = da.root
	if out, err := cmd.Output(); err == nil && len(out) > 0 {
		return strings.TrimSpace(string(out))
	}

	cmd2 := exec.Command("git", "merge-base", branch, "master")
	cmd2.Dir = da.root
	if out2, err := cmd2.Output(); err == nil && len(out2) > 0 {
		return strings.TrimSpace(string(out2))
	}

	return "main"
}

func (da *DiffAnalyzer) getHash() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	cmd.Dir = da.root
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (da *DiffAnalyzer) isRepo() bool {
	_, err := os.Stat(filepath.Join(da.root, ".git"))
	return err == nil
}

func (da *DiffAnalyzer) hasChanges() bool {
	if !da.isRepo() {
		return false
	}
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = da.root
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(out))) > 0
}