package investigate

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type StackFrame struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Column   int    `json:"column,omitempty"`
	Function string `json:"function,omitempty"`
	Package  string `json:"package,omitempty"`
}

type ProximitySlice struct {
	File    string     `json:"file"`
	Line    int        `json:"line"`
	Context []string   `json:"context"`
	Start   int        `json:"start"`
	End     int        `json:"end"`
	Frame   StackFrame `json:"frame"`
}

var (
	goStackTraceRe    = regexp.MustCompile(`^\s*(.+\.go):(\d+)(?::(\d+))?\s+(.*)$`)
	panicStackTraceRe = regexp.MustCompile(`^\s*(.+\.go):(\d+)`)
	javaStackTraceRe  = regexp.MustCompile(`^\s+at\s+(.+?)\((.+\.go):(\d+)\)`)
	pythonTracebackRe = regexp.MustCompile(`^\s*File\s+"(.+?)",\s+line\s+(\d+)`)
)

type ProximitySlicer struct {
	root  string
	lines int
}

func NewProximitySlicer(root string, contextLines int) *ProximitySlicer {
	if contextLines <= 0 {
		contextLines = 10
	}
	return &ProximitySlicer{
		root:  root,
		lines: contextLines,
	}
}

func (ps *ProximitySlicer) ExtractAll(frames []StackFrame) []ProximitySlice {
	var slices []ProximitySlice
	for _, frame := range frames {
		slice := ps.Extract(frame)
		if slice != nil {
			slices = append(slices, *slice)
		}
	}
	return slices
}

func (ps *ProximitySlicer) Extract(frame StackFrame) *ProximitySlice {
	fullPath := filepath.Join(ps.root, frame.File)
	file, err := os.Open(fullPath)
	if err != nil {
		rel, err2 := findFile(ps.root, frame.File)
		if err2 != nil {
			return nil
		}
		fullPath = rel
		file, err = os.Open(fullPath)
		if err != nil {
			return nil
		}
	}
	defer file.Close()

	var allLines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		allLines = append(allLines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil
	}

	start := frame.Line - ps.lines
	if start < 1 {
		start = 1
	}
	end := frame.Line + ps.lines
	if end > len(allLines) {
		end = len(allLines)
	}

	relFile, _ := filepath.Rel(ps.root, fullPath)

	var context []string
	for i := start - 1; i < end; i++ {
		marker := " "
		if i+1 == frame.Line {
			marker = ">"
		}
		context = append(context, fmt.Sprintf("%s %5d: %s", marker, i+1, allLines[i]))
	}

	return &ProximitySlice{
		File:    relFile,
		Line:    frame.Line,
		Context: context,
		Start:   start,
		End:     end,
		Frame:   frame,
	}
}

func ParseStackFrames(input string) []StackFrame {
	var frames []StackFrame
	lines := strings.Split(input, "\n")

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		if m := goStackTraceRe.FindStringSubmatch(trimmed); m != nil {
			frame := StackFrame{File: m[1]}
			frame.Line, _ = strconv.Atoi(m[2])
			if len(m) > 3 && m[3] != "" {
				frame.Column, _ = strconv.Atoi(m[3])
			}
			if len(m) > 4 {
				frame.Function = strings.TrimSpace(m[4])
			}
			frames = append(frames, frame)
			continue
		}

		if m := panicStackTraceRe.FindStringSubmatch(trimmed); m != nil {
			frame := StackFrame{File: m[1]}
			frame.Line, _ = strconv.Atoi(m[2])
			frames = append(frames, frame)
			continue
		}

		if m := pythonTracebackRe.FindStringSubmatch(trimmed); m != nil {
			frame := StackFrame{File: m[1]}
			frame.Line, _ = strconv.Atoi(m[2])
			frames = append(frames, frame)
			continue
		}

		if m := javaStackTraceRe.FindStringSubmatch(trimmed); m != nil {
			frame := StackFrame{
				Function: m[1],
				File:     m[2],
			}
			frame.Line, _ = strconv.Atoi(m[3])
			frames = append(frames, frame)
			continue
		}
	}

	return frames
}

func findFile(root, target string) (string, error) {
	var found string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, target) || strings.Contains(path, target) {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil || found == "" {
		return "", fmt.Errorf("file not found: %s", target)
	}
	return found, nil
}

func ExtractErrorOutput(runResult StdoutStderrer, err error) string {
	if runResult == nil {
		if err != nil {
			return err.Error()
		}
		return ""
	}
	output := runResult.StdErr()
	if output == "" {
		output = runResult.StdOut()
	}
	if err != nil && output == "" {
		output = err.Error()
	}
	return output
}

type StdoutStderrer interface {
	StdOut() string
	StdErr() string
	ExitCode() int
}

type runResultAdapter struct {
	stdout   string
	stderr   string
	exitCode int
}

func (r *runResultAdapter) StdOut() string { return r.stdout }
func (r *runResultAdapter) StdErr() string { return r.stderr }
func (r *runResultAdapter) ExitCode() int  { return r.exitCode }

func NewRunResultAdapter(stdout, stderr string, exitCode int) StdoutStderrer {
	return &runResultAdapter{stdout: stdout, stderr: stderr, exitCode: exitCode}
}
