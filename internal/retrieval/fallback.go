package retrieval

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// FileReadEvent carries real I/O metrics for a file read operation.
type FileReadEvent struct {
	File    string
	Bytes   int64
	Elapsed time.Duration
}

// SearchEvent carries real metrics for a search operation.
type SearchEvent struct {
	Query   string
	Hits    int
	Elapsed time.Duration
}

// ResolveEvent carries real metrics for a symbol resolution operation.
type ResolveEvent struct {
	Symbol  string
	Hits    int
	Elapsed time.Duration
}

type FallbackChain struct {
	root string
}

func NewFallbackChain(root string) *FallbackChain {
	return &FallbackChain{root: root}
}

func (fc *FallbackChain) Glob(pattern string) *ResultSet {
	rs := &ResultSet{Strategy: "glob.file"}

	if globalActivityLog != nil {
		globalActivityLog("[system] glob: %s", pattern)
	}

	globStart := time.Now()
	matches, err := filepath.Glob(filepath.Join(fc.root, pattern))
	elapsed := time.Since(globStart)
	if err != nil {
		rs.Error = err.Error()
		if globalActivityLog != nil {
			globalActivityLog("[FAIL] glob: %s: %v", pattern, err)
		}
		return rs
	}

	if globalActivityLog != nil {
		globalActivityLog("[ OK ] glob: %s (%d matches)", pattern, len(matches))
	}
	if globalEventLog != nil {
		globalEventLog(SearchEvent{Query: "glob:" + pattern, Hits: len(matches), Elapsed: elapsed})
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

	if globalActivityLog != nil {
		globalActivityLog("[system] rg: %s", pattern)
	}

	rgStart := time.Now()
	args := []string{"--no-heading", "-n", pattern}
	if filePattern != "" {
		args = append(args, "-g", filePattern)
	}

	cmd := exec.CommandContext(context.Background(), "rg", args...)
	cmd.Dir = fc.root

	out, err := cmd.Output()
	elapsed := time.Since(rgStart)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if exitErr.ExitCode() == 1 {
				if globalEventLog != nil {
					globalEventLog(SearchEvent{Query: pattern, Hits: 0, Elapsed: elapsed})
				}
				return rs
			}
		}
		rs.Error = err.Error()
		if globalActivityLog != nil {
			globalActivityLog("[FAIL] rg: %s: %v", pattern, err)
		}
		return rs
	}

	lines := strings.Split(string(bytes.TrimSpace(out)), "\n")
	if globalEventLog != nil {
		globalEventLog(SearchEvent{Query: pattern, Hits: len(lines), Elapsed: elapsed})
	}
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

	if globalActivityLog != nil {
		globalActivityLog("[system] grep: %s", pattern)
	}

	grepStart := time.Now()
	cmd := exec.CommandContext(context.Background(), "grep", "-rn", pattern, fc.root)
	cmd.Dir = fc.root

	out, err := cmd.Output()
	elapsed := time.Since(grepStart)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if exitErr.ExitCode() == 1 {
				if globalEventLog != nil {
					globalEventLog(SearchEvent{Query: pattern, Hits: 0, Elapsed: elapsed})
				}
				return rs
			}
		}
		rs.Error = err.Error()
		if globalActivityLog != nil {
			globalActivityLog("[FAIL] grep: %s: %v", pattern, err)
		}
		return rs
	}

	lines := strings.Split(string(bytes.TrimSpace(out)), "\n")
	if globalEventLog != nil {
		globalEventLog(SearchEvent{Query: pattern, Hits: len(lines), Elapsed: elapsed})
	}
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

	// Strip any :line:col suffix from compiler error targets before opening.
	if clean, _ := SplitTargetPath(path); clean != "" {
		path = clean
	}

	if globalActivityLog != nil {
		globalActivityLog("[system] read file: %s", path)
	}

	readStart := time.Now()
	fullPath := filepath.Join(fc.root, path)
	data, err := os.ReadFile(fullPath)
	elapsed := time.Since(readStart)
	if err != nil {
		rs.Error = err.Error()
		if globalActivityLog != nil {
			globalActivityLog("[FAIL] read file: %s: %v", path, err)
		}
		return rs
	}

	if globalActivityLog != nil {
		globalActivityLog("[ OK ] read file: %s (%d bytes)", path, len(data))
	}
	if globalEventLog != nil {
		globalEventLog(FileReadEvent{File: path, Bytes: int64(len(data)), Elapsed: elapsed})
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

	// Strip any :line:col suffix from compiler error targets before opening.
	if clean, _ := SplitTargetPath(path); clean != "" {
		path = clean
	}

	if globalActivityLog != nil {
		globalActivityLog("[system] read file: %s (lines %d-%d)", path, startLine, endLine)
	}

	readStart := time.Now()
	fullPath := filepath.Join(fc.root, path)
	file, err := os.Open(fullPath)
	if err != nil {
		rs.Error = err.Error()
		return rs
	}
	defer func() { _ = file.Close() }()

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
	elapsed := time.Since(readStart)
	if globalEventLog != nil {
		globalEventLog(FileReadEvent{File: path, Bytes: int64(content.Len()), Elapsed: elapsed})
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
	_, _ = fmt.Sscanf(parts[1], "%d", &lineNum)
	return file, lineNum, parts[2]
}

func parseGrepLine(line string) (file string, lineNum int, content string) {
	parts := strings.SplitN(line, ":", 3)
	if len(parts) < 3 {
		return "", 0, line
	}
	file = parts[0]
	lineNum = 0
	_, _ = fmt.Sscanf(parts[1], "%d", &lineNum)
	return file, lineNum, parts[2]
}
