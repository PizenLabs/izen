package layer3

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/PizenLabs/izen/pkg/engine/layer2"
)

type fakeClient struct {
	mu      sync.Mutex
	prompt  string
	text    string
	tokens  TokenUsage
	err     error
	invoked int
}

func (c *fakeClient) Complete(_ context.Context, req *CompletionRequest) (*CompletionResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.invoked++
	c.prompt = req.Prompt
	if c.err != nil {
		return nil, c.err
	}
	return &CompletionResponse{Text: c.text, Tokens: c.tokens}, nil
}

func minimalExec() *layer2.ExecutionContext {
	return &layer2.ExecutionContext{
		Files: []layer2.FileContext{
			{Path: "svc/service.go", Language: "go", Source: "package svc\nfunc F() int { return 1 }\n"},
		},
	}
}

func renameReq() Request {
	return Request{
		Intent:       IntentRename,
		TargetSymbol: "F",
		NewName:      "G",
		Description:  "rename F to G",
	}
}

func TestStatelessWorkerExecute(t *testing.T) {
	client := &fakeClient{
		text:   "=== FILE: svc/service.go\npackage svc\n\nfunc G() int { return 1 }\n=== END\n",
		tokens: TokenUsage{Input: 120, Output: 40},
	}
	w := NewStatelessWorker(ProviderOpenAI, "gpt-test", client)
	res, err := w.Execute(context.Background(), minimalExec(), renameReq())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Patches) != 1 {
		t.Fatalf("patches = %d, want 1", len(res.Patches))
	}
	p := res.Patches[0]
	if p.Path != "svc/service.go" || !strings.Contains(p.New, "func G() int") {
		t.Errorf("patch = %+v", p)
	}
	if res.Tokens.Total() != 160 {
		t.Errorf("tokens = %+v", res.Tokens)
	}
	if !strings.Contains(client.prompt, "rename F to G") {
		t.Errorf("prompt missing description: %s", client.prompt)
	}
	if !strings.Contains(client.prompt, "package svc") {
		t.Errorf("prompt missing context source: %s", client.prompt)
	}
	if w.Name() != "openai" {
		t.Errorf("name = %q", w.Name())
	}
}

func TestStatelessWorkerMissingClient(t *testing.T) {
	w := NewStatelessWorker(ProviderLocal, "model", nil)
	_, err := w.Execute(context.Background(), minimalExec(), renameReq())
	if !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("err = %v, want ErrInvalidRequest", err)
	}
}

func TestStatelessWorkerClientError(t *testing.T) {
	client := &fakeClient{err: errors.New("boom")}
	w := NewStatelessWorker(ProviderClaude, "claude-test", client)
	_, err := w.Execute(context.Background(), minimalExec(), renameReq())
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v", err)
	}
}

func TestStatelessWorkerCancelledContext(t *testing.T) {
	client := &fakeClient{text: "=== FILE: a.go\npackage a\n=== END\n"}
	w := NewStatelessWorker(ProviderLocal, "local", client)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := w.Execute(ctx, minimalExec(), renameReq()); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestStatelessWorkerWithCustomParser(t *testing.T) {
	custom := PatchParserFunc(func(text string) ([]FilePatch, error) {
		return []FilePatch{{Path: "custom.go", New: text, Changed: true}}, nil
	})
	client := &fakeClient{text: "anything"}
	w := NewStatelessWorker(ProviderOpenRouter, "router", client, WithPatchParser(custom))
	res, err := w.Execute(context.Background(), nil, renameReq())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Patches) != 1 || res.Patches[0].Path != "custom.go" {
		t.Errorf("custom parser ignored: %+v", res.Patches)
	}
}

func TestStatelessWorkerConcurrent(t *testing.T) {
	client := &fakeClient{text: "=== FILE: a.go\npackage a\n=== END\n"}
	w := NewStatelessWorker(ProviderLocal, "local", client)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := w.Execute(context.Background(), minimalExec(), renameReq()); err != nil {
				t.Errorf("Execute: %v", err)
			}
		}()
	}
	wg.Wait()
	if client.invoked != 16 {
		t.Errorf("invoked = %d, want 16", client.invoked)
	}
}

func TestLinePatchParserMultipleFiles(t *testing.T) {
	text := "=== FILE: a.go\npackage a\nfunc A() {}\n=== END\n\n=== FILE: b.go\npackage b\n=== END\n"
	patches, err := (LinePatchParser{}).Parse(text)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(patches) != 2 {
		t.Fatalf("patches = %d, want 2", len(patches))
	}
	if patches[0].Path != "a.go" || !strings.Contains(patches[0].New, "func A() {}") {
		t.Errorf("patch[0] = %+v", patches[0])
	}
	if patches[1].Path != "b.go" {
		t.Errorf("patch[1] = %+v", patches[1])
	}
}

func TestLinePatchParserPreservesWhitespace(t *testing.T) {
	text := "=== FILE: a.go\n  indented\n\t tabbed\n=== END\n"
	patches, err := (LinePatchParser{}).Parse(text)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !strings.Contains(patches[0].New, "  indented") || !strings.Contains(patches[0].New, "\t tabbed") {
		t.Errorf("whitespace not preserved: %q", patches[0].New)
	}
}

func TestLinePatchParserMalformed(t *testing.T) {
	_, err := (LinePatchParser{}).Parse("=== FILE: \nsome content\n=== END\n")
	if !errors.Is(err, ErrInvalidPatch) {
		t.Errorf("err = %v, want ErrInvalidPatch", err)
	}
}

// TestLinePatchParserFencedBlocks covers the per-task raw-code acceptance: a
// model that emits path-tagged markdown fences (```lang:path, ```lang path,
// ```file=path) instead of === FILE: blocks must have every block accepted as
// the complete replacement content — never rejected for lacking diff markers.
func TestLinePatchParserFencedBlocks(t *testing.T) {
	text := "```html:index.html\n<!DOCTYPE html>\n<html></html>\n```\n" +
		"```css styles.css\nbody { margin: 0; }\n```\n" +
		"```file=script.js\nconsole.log('hi');\n```\n"
	patches, err := (LinePatchParser{}).Parse(text)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(patches) != 3 {
		t.Fatalf("patches = %d, want 3:\n%+v", len(patches), patches)
	}
	if patches[0].Path != "index.html" || patches[0].New != "<!DOCTYPE html>\n<html></html>" {
		t.Errorf("patches[0] = %+v", patches[0])
	}
	if patches[1].Path != "styles.css" || !strings.Contains(patches[1].New, "margin: 0") {
		t.Errorf("patches[1] = %+v", patches[1])
	}
	if patches[2].Path != "script.js" || !strings.Contains(patches[2].New, "console.log") {
		t.Errorf("patches[2] = %+v", patches[2])
	}
	for i, p := range patches {
		if !p.Changed {
			t.Errorf("patches[%d] = %+v, want Changed", i, p)
		}
	}
}

// TestLinePatchParserMixedProtocols ensures the === FILE: pass and the fence
// pass coexist without duplicating blocks.
func TestLinePatchParserMixedProtocols(t *testing.T) {
	text := "=== FILE: a.go\npackage a\n=== END\n" +
		"```go: b.go\npackage b\n```\n"
	patches, err := (LinePatchParser{}).Parse(text)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(patches) != 2 {
		t.Fatalf("patches = %d, want 2 (no duplicates):\n%+v", len(patches), patches)
	}
}

func TestFuncWorker(t *testing.T) {
	worker := FuncWorker(func(_ context.Context, _ *layer2.ExecutionContext, _ Request) (*WorkerResult, error) {
		return &WorkerResult{Patches: []FilePatch{{Path: "x.go", New: "package x", Changed: true}}}, nil
	})
	res, err := worker.Execute(context.Background(), nil, renameReq())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Patches) != 1 {
		t.Errorf("patches = %d", len(res.Patches))
	}
	if worker.Name() != "func-worker" {
		t.Errorf("name = %q", worker.Name())
	}

	var nilWorker FuncWorker
	if _, err := nilWorker.Execute(context.Background(), nil, renameReq()); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("nil worker err = %v", err)
	}
}

func TestBuildPrompt(t *testing.T) {
	exec := minimalExec()
	prompt := BuildPrompt(exec, renameReq(), 0)
	if !strings.Contains(prompt, "Intent: rename") {
		t.Errorf("prompt missing intent:\n%s", prompt)
	}
	if !strings.Contains(prompt, "### svc/service.go") {
		t.Errorf("prompt missing file header:\n%s", prompt)
	}

	short := BuildPrompt(exec, renameReq(), 32)
	if len(short) > 32 {
		t.Errorf("prompt not bounded: %d > 32", len(short))
	}

	if prompt := BuildPrompt(nil, Request{Intent: IntentRefactor, Description: "d"}, 0); !strings.Contains(prompt, "Files: 0") {
		t.Errorf("nil exec prompt missing Files header:\n%s", prompt)
	}
}

// TestBuildPromptNewFileSelectsFullCreation pins the per-task new-file fallback:
// when the target file does NOT exist on disk, the worker prompt must instruct
// complete full-file creation and explicitly forbid diff formats (SEARCH/REPLACE
// or unified diff) — a diff against a non-existent file has no "old content" and
// makes weak models emit (+0 / -0 lines) no-op patches.
func TestBuildPromptNewFileSelectsFullCreation(t *testing.T) {
	target := filepath.Join(t.TempDir(), "index.html")
	req := Request{
		Intent:      IntentNewFeature,
		TargetFile:  target,
		Description: "create the landing page",
	}
	prompt := BuildPrompt(minimalExec(), req, 0)
	if !strings.Contains(prompt, "does NOT exist on disk") {
		t.Errorf("prompt should mark the target as a new file:\n%s", prompt)
	}
	if !strings.Contains(prompt, "CREATE it with the COMPLETE new file content") {
		t.Errorf("prompt should instruct complete full-file creation:\n%s", prompt)
	}
	if !strings.Contains(prompt, "=== FILE:") {
		t.Errorf("prompt should instruct the FILE block protocol:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Do NOT output a unified diff") {
		t.Errorf("prompt should explicitly forbid diff formats for a new file:\n%s", prompt)
	}
	if strings.Contains(prompt, "Output a unified diff") {
		t.Errorf("prompt must not ask the model to output a diff for a new file:\n%s", prompt)
	}
}

// TestBuildPromptStubSelectsWholeFileOverwrite pins the "Explicit Over
// Implicit" law for the Layer3 worker: a target that EXISTS on disk but is a
// stub (under 100 lines) must be forced through the whole-file overwrite
// protocol — output the COMPLETE, FULLY IMPLEMENTED content, never a diff
// against incomplete "old content". The skeleton is passed in context with the
// "fully implement and expand" directive so a weak model expands it instead of
// echoing the stub back unchanged.
func TestBuildPromptStubSelectsWholeFileOverwrite(t *testing.T) {
	target := filepath.Join(t.TempDir(), "index.html")
	stub := "<!DOCTYPE html>\n<html>\n<body>\n<h1>hi</h1>\n</body>\n</html>\n"
	if err := os.WriteFile(target, []byte(stub), 0644); err != nil {
		t.Fatal(err)
	}
	req := Request{
		Intent:      IntentRefactor,
		TargetFile:  target,
		Description: "fully implement the landing page",
	}
	prompt := BuildPrompt(minimalExec(), req, 0)
	if !strings.Contains(prompt, "is a stub") {
		t.Errorf("prompt should classify the target as a stub:\n%s", prompt)
	}
	if !strings.Contains(prompt, "COMPLETE, FULLY IMPLEMENTED content") {
		t.Errorf("prompt should demand complete full implementation:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Do NOT output a unified diff") {
		t.Errorf("prompt should forbid diff formats for a stub:\n%s", prompt)
	}
	if !strings.Contains(prompt, "incomplete skeleton") {
		t.Errorf("prompt should pass the stub context with the expand directive:\n%s", prompt)
	}
	if !strings.Contains(prompt, "<!DOCTYPE html>") {
		t.Errorf("prompt should carry the on-disk skeleton content:\n%s", prompt)
	}
}

// TestBuildPromptExistingFileSelectsReplacement pins the large-file path: when
// the target file EXISTS on disk with substantial content (100+ lines), the
// worker prompt must switch to the complete-replacement contract and carry the
// on-disk content so the model can see what it is replacing.
func TestBuildPromptExistingFileSelectsReplacement(t *testing.T) {
	target := filepath.Join(t.TempDir(), "main.go")
	var sb strings.Builder
	sb.WriteString("package main\n")
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&sb, "// line %d\n", i)
	}
	if err := os.WriteFile(target, []byte(sb.String()), 0644); err != nil {
		t.Fatal(err)
	}
	req := Request{
		Intent:      IntentRefactor,
		TargetFile:  target,
		Description: "edit the main module",
	}
	prompt := BuildPrompt(minimalExec(), req, 0)
	if !strings.Contains(prompt, "EXISTS on disk") {
		t.Errorf("prompt should mark the target as existing:\n%s", prompt)
	}
	if !strings.Contains(prompt, "COMPLETE replacement content") {
		t.Errorf("prompt should instruct complete replacement:\n%s", prompt)
	}
	if !strings.Contains(prompt, "// line 0") {
		t.Errorf("prompt should carry the on-disk content:\n%s", prompt)
	}
}

type PatchParserFunc func(text string) ([]FilePatch, error)

func (f PatchParserFunc) Parse(text string) ([]FilePatch, error) { return f(text) }
