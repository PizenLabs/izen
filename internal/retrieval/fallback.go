package retrieval

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type FallbackChain struct {
	root string
}

func NewFallbackChain(root string) *FallbackChain {
	return &FallbackChain{root: root}
}

func (fc *FallbackChain) Glob(pattern string) *ResultSet {
	rs := &ResultSet{Strategy: "glob.file"}

	matches, err := filepath.Glob(filepath.Join(fc.root, pattern))
	if err != nil {
		rs.Error = err.Error()
		return rs
	}

	for _, m := range matches {
		rel, err := filepath.Rel(fc.root, m)
		if err != nil {
			continue
		}
		rs.Add(Score(ConfPartial, Result{
			File:     rel,
			Strategy: "glob.file",
			Content:  m,
		}))
	}

	if !rs.Empty() {
		rs.Confidence = ConfPartial.Float64()
	}

	return rs
}

func (fc *FallbackChain) Ripgrep(pattern string, filePattern string) *ResultSet {
	rs := &ResultSet{Strategy: "rg.pattern"}

	args := []string{"--no-heading", "-n", pattern}
	if filePattern != "" {
		args = append(args, "-g", filePattern)
	}

	cmd := exec.Command("rg", args...)
	cmd.Dir = fc.root

	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() == 1 {
				return rs
			}
		}
		rs.Error = err.Error()
		return rs
	}

	lines := strings.Split(string(bytes.TrimSpace(out)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		file, lineNum, content := parseRgLine(line)
		rs.Add(Score(ConfPattern, Result{
			File:     file,
			Line:     lineNum,
			Strategy: "rg.pattern",
			Content:  content,
		}))
	}

	if !rs.Empty() {
		rs.Confidence = ConfPattern.Float64()
	}

	return rs
}

func (fc *FallbackChain) Grep(pattern string) *ResultSet {
	rs := &ResultSet{Strategy: "grep.text"}

	cmd := exec.Command("grep", "-rn", pattern, fc.root)
	cmd.Dir = fc.root

	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() == 1 {
				return rs
			}
		}
		rs.Error = err.Error()
		return rs
	}

	lines := strings.Split(string(bytes.TrimSpace(out)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		file, lineNum, content := parseGrepLine(line)
		rs.Add(Score(ConfText, Result{
			File:     file,
			Line:     lineNum,
			Strategy: "grep.text",
			Content:  content,
		}))
	}

	if !rs.Empty() {
		rs.Confidence = ConfText.Float64()
	}

	return rs
}

func (fc *FallbackChain) ReadFile(path string) *ResultSet {
	rs := &ResultSet{Strategy: "read.file"}

	fullPath := filepath.Join(fc.root, path)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		rs.Error = err.Error()
		return rs
	}

	rs.Add(Score(ConfText, Result{
		File:     path,
		Strategy: "read.file",
		Content:  string(data),
	}))

	if !rs.Empty() {
		rs.Confidence = ConfText.Float64()
	}

	return rs
}

func (fc *FallbackChain) ReadLines(path string, startLine, endLine int) *ResultSet {
	rs := &ResultSet{Strategy: "read.file"}

	fullPath := filepath.Join(fc.root, path)
	file, err := os.Open(fullPath)
	if err != nil {
		rs.Error = err.Error()
		return rs
	}
	defer file.Close()

	var content strings.Builder
	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if lineNum < startLine {
			continue
		}
		if endLine > 0 && lineNum > endLine {
			break
		}
		content.WriteString(scanner.Text())
		content.WriteString("\n")
	}

	rs.Add(Score(ConfText, Result{
		File:     path,
		Line:     startLine,
		Strategy: "read.file",
		Content:  content.String(),
	}))

	if !rs.Empty() {
		rs.Confidence = ConfText.Float64()
	}

	return rs
}

func parseRgLine(line string) (file string, lineNum int, content string) {
	parts := strings.SplitN(line, ":", 3)
	if len(parts) < 3 {
		return "", 0, line
	}
	file = parts[0]
	lineNum = 0
	fmt.Sscanf(parts[1], "%d", &lineNum)
	return file, lineNum, parts[2]
}

func parseGrepLine(line string) (file string, lineNum int, content string) {
	parts := strings.SplitN(line, ":", 3)
	if len(parts) < 3 {
		return "", 0, line
	}
	file = parts[0]
	lineNum = 0
	fmt.Sscanf(parts[1], "%d", &lineNum)
	return file, lineNum, parts[2]
}
