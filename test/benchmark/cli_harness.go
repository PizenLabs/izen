package benchmark

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── SUBPROCESS CLI BENCHMARK HARNESS ────────────────────────────────────────
//
// Unlike the model-comparison DAG harness in harness.go, this harness drives
// the REAL `izen` binary as a subprocess against a scripted OpenAI-compatible
// provider. It therefore exercises the production command surface end to end:
// argument parsing, compose.Wire, the autonomy Driver, the RuntimeExecutor,
// approval, mutation, verification, durable ledger and audit.
//
// The harness never trusts the exit code or the answer text alone. It asserts
// the exact artifact bytes on disk, the machine-readable JSON report, and the
// durable evidence, so a run that reports success without producing the
// requested mutation is detected as a failure.

// ChatScript decides the assistant content for one provider request.
type ChatScript func(system, user string, call int) string

// ScriptedProvider is a deterministic OpenAI-compatible HTTP server. It never
// touches a network or a credential: the binary talks to 127.0.0.1.
type ScriptedProvider struct {
	server *httptest.Server

	mu       sync.Mutex
	calls    []chatRequest
	contents []string
	script   ChatScript
	status   int
	failOnce bool
	failed   bool
}

type chatRequest struct {
	model    string
	system   string
	userText string
}

// NewScriptedProvider starts the server. When script is nil, the provider
// answers requirement/manifest passes with empty JSON and returns "" for the
// proposal (an intentionally empty artifact).
func NewScriptedProvider(script ChatScript) *ScriptedProvider {
	p := &ScriptedProvider{script: script}
	p.server = httptest.NewServer(http.HandlerFunc(p.handle))
	return p
}

// BaseURL returns the endpoint the binary must target.
func (p *ScriptedProvider) BaseURL() string { return p.server.URL }

// Close stops the server.
func (p *ScriptedProvider) Close() { p.server.Close() }

// Calls returns a copy of every request the binary made, in order.
func (p *ScriptedProvider) Calls() []chatRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]chatRequest(nil), p.calls...)
}

// CallsMatching returns the number of requests whose system prompt contains all
// of the given markers.
func (p *ScriptedProvider) CallsMatching(markers ...string) int {
	n := 0
	for _, c := range p.Calls() {
		all := true
		for _, m := range markers {
			if !strings.Contains(c.system, m) {
				all = false
				break
			}
		}
		if all {
			n++
		}
	}
	return n
}

// SetStatus forces every response to an HTTP status (e.g. 500) to simulate a
// provider failure.
func (p *ScriptedProvider) SetStatus(status int) {
	p.mu.Lock()
	p.status = status
	p.mu.Unlock()
}

// Contents returns the assistant content returned for every request, in order.
func (p *ScriptedProvider) Contents() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.contents...)
}

// FailNext makes exactly the next request return HTTP 503.
func (p *ScriptedProvider) FailNext() {
	p.mu.Lock()
	p.failOnce = true
	p.failed = false
	p.mu.Unlock()
}

func (p *ScriptedProvider) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		http.NotFound(w, r)
		return
	}
	body, _ := io.ReadAll(r.Body)

	var req struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(body, &req)
	system, userText := "", ""
	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			system += m.Content + "\n"
		case "user":
			userText = m.Content
		}
	}

	p.mu.Lock()
	call := len(p.calls)
	p.calls = append(p.calls, chatRequest{model: req.Model, system: system, userText: userText})
	status := p.status
	fail := p.failOnce
	p.failOnce = false
	p.failed = fail
	script := p.script
	p.mu.Unlock()

	if fail || status != 0 {
		code := status
		if code == 0 {
			code = http.StatusServiceUnavailable
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"error":{"message":"scripted provider failure","type":"provider_error"}}`))
		return
	}

	content := scriptContent(script, system, userText, call)
	p.mu.Lock()
	p.contents = append(p.contents, content)
	p.mu.Unlock()

	if req.Stream {
		writeSSE(w, req.Model, content, call)
		return
	}
	resp := map[string]any{
		"id":      fmt.Sprintf("chatcmpl-%d", call),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   req.Model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]string{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
		"usage": map[string]int{"prompt_tokens": 12, "completion_tokens": 7, "total_tokens": 19},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// writeSSE emits the assistant content as an OpenAI-compatible SSE stream: one
// content delta, one terminal finish_reason chunk, then [DONE]. This is the
// wire shape the streaming executor path consumes.
func writeSSE(w http.ResponseWriter, model, content string, call int) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	emit := func(v any) {
		data, _ := json.Marshal(v)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		if flusher != nil {
			flusher.Flush()
		}
	}
	emit(map[string]any{
		"id":      fmt.Sprintf("chatcmpl-%d", call),
		"object":  "chat.completion.chunk",
		"model":   model,
		"choices": []map[string]any{{"index": 0, "delta": map[string]string{"role": "assistant"}}},
	})
	if content != "" {
		emit(map[string]any{
			"id":      fmt.Sprintf("chatcmpl-%d", call),
			"object":  "chat.completion.chunk",
			"model":   model,
			"choices": []map[string]any{{"index": 0, "delta": map[string]string{"content": content}}},
		})
	}
	emit(map[string]any{
		"id":      fmt.Sprintf("chatcmpl-%d", call),
		"object":  "chat.completion.chunk",
		"model":   model,
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
		"usage":   map[string]int{"prompt_tokens": 12, "completion_tokens": 7, "total_tokens": 19},
	})
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// scriptContent answers non-proposal passes with contract-shaped empty payloads
// so the runtime's own structural obligations remain the completion authority.
func scriptContent(script ChatScript, system, userText string, call int) string {
	switch {
	case strings.Contains(system, "requirement derivation stage"):
		return "[]"
	case strings.Contains(system, "manifest generator"), strings.Contains(system, "manifest stage"):
		return `{"files":[]}`
	}
	if script == nil {
		return ""
	}
	return script(system, userText, call)
}

// ── process runner ──────────────────────────────────────────────────────────

// CLIRunner runs the built izen binary with an isolated HOME and workspace.
type CLIRunner struct {
	Binary  string
	Timeout time.Duration
	// Stdin, when non-empty, is fed to the subprocess (interactive approval).
	Stdin string
}

// RepoRoot walks up from the test working directory to the module root.
func RepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

// BuildIZEN compiles the real binary once and returns its path.
func BuildIZEN(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "izen")
	if err := buildBinary(RepoRoot(t), out); err != nil {
		t.Fatalf("build izen: %v", err)
	}
	return out
}

// buildBinary compiles ./cmd/izen from root into out.
func buildBinary(root, out string) error {
	cmd := exec.CommandContext(context.Background(), "go", "build", "-o", out, "./cmd/izen")
	cmd.Dir = root
	if data, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w\n%s", err, data)
	}
	return nil
}

// CLIResult is the captured result of one subprocess run.
type CLIResult struct {
	Exit   int
	Stdout string
	Stderr string
	Report map[string]any
}

// Run executes the binary with an isolated HOME and the given workspace.
func (r *CLIRunner) Run(t *testing.T, home, dir string, extraEnv []string, args ...string) CLIResult {
	t.Helper()
	timeout := r.Timeout
	if timeout == 0 {
		timeout = 3 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, r.Binary, args...)
	cmd.Dir = dir
	cmd.Env = append([]string{
		"HOME=" + home,
		"PATH=" + os.Getenv("PATH"),
	}, extraEnv...)
	if r.Stdin != "" {
		cmd.Stdin = strings.NewReader(r.Stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := CLIResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.Exit = exitErr.ExitCode()
		} else {
			res.Exit = -1
			res.Stderr += "\nharness: " + err.Error()
		}
	}
	// Parse the JSON report when -json was requested.
	if trimmed := strings.TrimSpace(res.Stdout); strings.HasPrefix(trimmed, "{") {
		_ = json.Unmarshal([]byte(trimmed), &res.Report)
	}
	return res
}

// envForProvider returns the environment entry that makes the scripted
// provider accept the request.
func envForProvider() []string {
	return []string{"OPENCODE_API_KEY=bench-scripted"}
}

// writeWorkspaceFile writes a fixture file under dir.
func writeWorkspaceFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// readWorkspaceFile reads a workspace file, failing the test on error.
func readWorkspaceFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

// initGitRepo initializes a git repository with one commit so the runtime's
// session-start checkpoint (a `git stash create`) has a HEAD to work against.
// The benchmark workspace must be a real repository: an isolated temp dir is
// not one, and the checkpoint is a production precondition.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=bench", "GIT_AUTHOR_EMAIL=bench@example.com",
			"GIT_COMMITTER_NAME=bench", "GIT_COMMITTER_EMAIL=bench@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("add", "-A")
	run("commit", "-q", "--allow-empty", "-m", "benchmark baseline")
}
