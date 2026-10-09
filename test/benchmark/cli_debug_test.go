package benchmark

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// binaryOnce builds the real izen binary once for the whole package's CLI tests.
var (
	binaryOnce sync.Once
	binaryPath string
	binaryErr  error
)

func cliBinary(t *testing.T) string {
	t.Helper()
	binaryOnce.Do(func() {
		root := RepoRoot(t)
		dir, err := os.MkdirTemp("", "izen-cli-bin-*")
		if err != nil {
			binaryErr = err
			return
		}
		out := filepath.Join(dir, "izen")
		if err := buildBinary(root, out); err != nil {
			binaryErr = err
			return
		}
		binaryPath = out
	})
	if binaryErr != nil {
		t.Fatalf("build izen: %v", binaryErr)
	}
	return binaryPath
}

// TestCLIDebug is an investigative harness: it runs one create objective against
// the scripted provider and dumps the subprocess output, the JSON report and
// every provider request. Kept as a skipped-by-default diagnostic.
func TestCLIDebug(t *testing.T) {
	if os.Getenv("IZEN_CLI_DEBUG") == "" {
		t.Skip("set IZEN_CLI_DEBUG=1 to run the investigative dump")
	}
	bin := cliBinary(t)
	provider := NewScriptedProvider(func(system, user string, call int) string {
		return "hello from the scripted provider\n"
	})
	defer provider.Close()

	home := t.TempDir()
	ws := t.TempDir()
	initGitRepo(t, ws)
	runner := &CLIRunner{Binary: bin, Timeout: 90 * time.Second}
	res := runner.Run(t, home, ws, envForProvider(),
		"run", "-json", "-approve", "always",
		"-provider", "opencode", "-model", "scripted/model-large",
		"-base-url", provider.BaseURL(),
		"Create a new file named `note.txt` with the content \"hello\".",
	)
	fmt.Printf("EXIT=%d\nSTDOUT:\n%s\nSTDERR:\n%s\n", res.Exit, res.Stdout, res.Stderr)
	if data, err := json.MarshalIndent(res.Report, "", "  "); err == nil {
		fmt.Printf("REPORT:\n%s\n", data)
	}
	for i, c := range provider.Calls() {
		fmt.Printf("CALL %d model=%s system[0:80]=%.80q user=%.120q\n", i, c.model, c.system, c.userText)
	}
	for i, c := range provider.Contents() {
		fmt.Printf("RESP %d content=%.200q\n", i, c)
	}
	if _, err := os.Stat(filepath.Join(ws, "note.txt")); err == nil {
		fmt.Printf("note.txt=%q\n", readWorkspaceFile(t, ws, "note.txt"))
	} else {
		fmt.Printf("note.txt MISSING: %v\n", err)
	}
	for _, rel := range []string{".izen/runtime/ledger.ndjson", ".izen/runtime/snapshot.json", ".izen/audit/events.ndjson"} {
		if data, err := os.ReadFile(filepath.Join(ws, rel)); err == nil {
			fmt.Printf("--- %s (%d bytes) ---\n%s\n", rel, len(data), truncateForDebug(string(data), 1500))
		} else {
			fmt.Printf("--- %s MISSING: %v\n", rel, err)
		}
	}
}

func truncateForDebug(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n...(truncated)"
}
