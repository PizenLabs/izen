package compose

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/events"
)

// gitRepo seeds a real git repository at root with a committed file, returning
// the workspace-relative tracked file name.
func gitRepo(t *testing.T, root string) string {
	t.Helper()
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=izen test", "GIT_AUTHOR_EMAIL=test@izen",
			"GIT_COMMITTER_NAME=izen test", "GIT_COMMITTER_EMAIL=test@izen",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	runGit("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("clean\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "tracked.txt")
	runGit("commit", "-q", "-m", "seed")
	return "tracked.txt"
}

// TestComposeWiresWorkspaceGuardAndAuditSessionCorrelation is the Phase 3
// end-to-end integration: the composition root wires (a) the git-backed
// WorkspaceGuard so uncommitted changes are injected into the target session on
// switch, and (b) the audit logger's session resolver so every NDJSON audit
// record carries the originating session_id (INV-SESSION-10).
func TestComposeWiresWorkspaceGuardAndAuditSessionCorrelation(t *testing.T) {
	root := t.TempDir()
	dirtyFile := gitRepo(t, root)
	auditDir := filepath.Join(root, ".izen", "audit")

	app, err := Wire(
		WithRoot(root),
		WithAuditDir(auditDir),
	)
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}
	defer app.Close()

	if app.Sessions == nil {
		t.Fatal("session manager not wired")
	}
	if app.Audit == nil {
		t.Fatal("audit logger not wired")
	}

	// ── Dirty workspace detection upon session switch ─────────────────
	// Dirty the tracked file, then create a fresh session. The guard must
	// inject the uncommitted file into the fresh session's view.
	if err := os.WriteFile(filepath.Join(root, dirtyFile), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fresh, err := app.Sessions.NewSession(context.Background())
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if len(fresh.WorkspaceDirtyFiles) == 0 {
		t.Fatal("workspace guard did not inject dirty files into the fresh session on switch")
	}
	found := false
	for _, f := range fresh.WorkspaceDirtyFiles {
		if f == dirtyFile {
			found = true
		}
	}
	if !found {
		t.Fatalf("dirty file %q not injected; got %v", dirtyFile, fresh.WorkspaceDirtyFiles)
	}
	// .izen/ internal state must never surface as workspace dirt.
	for _, f := range fresh.WorkspaceDirtyFiles {
		if strings.HasPrefix(f, ".izen/") {
			t.Fatalf("workspace guard surfaced .izen/ internal state: %q", f)
		}
	}

	// The dirty files must survive into the derived compact context (the
	// Context Compiler's view).
	cc, err := app.Sessions.CompactContext(app.Sessions.Active())
	if err != nil || cc == nil {
		t.Fatalf("CompactContext: %v", err)
	}
	if len(cc.DirtyFiles) == 0 {
		t.Fatal("compact context missing injected dirty files")
	}

	// ── Audit session_id correlation (INV-SESSION-10) ─────────────────
	// Publish a canonical lifecycle event; the audit logger must stamp the
	// ACTIVE session id onto the persisted NDJSON line.
	activeID := app.Sessions.ActiveSessionID()
	if activeID == "" {
		t.Fatal("active session id is empty")
	}
	app.Bus.Publish(events.NewExecutionStarted("req-c1", "build", "fix tracked.txt", activeID))
	waitAuditLines(t, app, auditDir, 2) // one line already? no: fresh file starts empty

	lines := readAuditLines(t, auditDir)
	if len(lines) == 0 {
		t.Fatal("audit log empty after publish")
	}
	last := lines[len(lines)-1]
	var env struct {
		SessionID string `json:"session_id"`
		Source    string `json:"source"`
	}
	if err := json.Unmarshal(last, &env); err != nil {
		t.Fatalf("unmarshal audit line: %v", err)
	}
	if env.SessionID != activeID {
		t.Fatalf("audit session_id = %q, want active %q", env.SessionID, activeID)
	}
	if env.Source != events.EventExecutionStarted {
		t.Fatalf("audit source = %q, want %q", env.Source, events.EventExecutionStarted)
	}
}

func waitAuditLines(t *testing.T, app *Application, dir string, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if app.Audit != nil {
			_ = app.Audit.Flush()
		}
		if len(readAuditLines(t, dir)) >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func readAuditLines(t *testing.T, dir string) [][]byte {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, "events.ndjson"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("open audit: %v", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	var out [][]byte
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			out = append(out, []byte(line))
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan audit: %v", err)
	}
	return out
}

// TestComposeAuditStampsActiveSessionOnSwitch pins that the resolver tracks the
// ACTIVE session: after a session switch the stamped id follows the new active
// session.
func TestComposeAuditStampsActiveSessionOnSwitch(t *testing.T) {
	root := t.TempDir()
	gitRepo(t, root)
	auditDir := filepath.Join(root, ".izen", "audit")

	app, err := Wire(WithRoot(root), WithAuditDir(auditDir))
	if err != nil {
		t.Fatalf("Wire: %v", err)
	}
	defer app.Close()

	firstID := app.Sessions.ActiveSessionID()
	app.Bus.Publish(events.NewExecutionStarted("req-a", "build", "a", ""))
	waitAuditLines(t, app, auditDir, 1)

	if _, err := app.Sessions.NewSession(context.Background()); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	secondID := app.Sessions.ActiveSessionID()
	if secondID == firstID {
		t.Fatal("session switch did not change the active session")
	}
	app.Bus.Publish(events.NewExecutionStarted("req-b", "build", "b", ""))
	waitAuditLines(t, app, auditDir, 2)

	lines := readAuditLines(t, auditDir)
	if len(lines) < 2 {
		t.Fatalf("expected >=2 audit lines, got %d", len(lines))
	}
	got := make([]string, 0, len(lines))
	for _, line := range lines {
		var env struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(line, &env); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		got = append(got, env.SessionID)
	}
	if got[0] != firstID {
		t.Errorf("first record session_id = %q, want %q", got[0], firstID)
	}
	if got[len(got)-1] != secondID {
		t.Errorf("last record session_id = %q, want %q", got[len(got)-1], secondID)
	}
}
