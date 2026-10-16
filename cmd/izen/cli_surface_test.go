package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("read: %v", err)
	}
	return buf.String()
}

// TestModelsCommandRoundTrip proves `izen models` lists providers and
// `izen models use` persists the active binding that `izen config get` reads
// back — a real, non-decorative contract.
func TestModelsCommandRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if code := runModelsCommand([]string{"use", "opencode", "z-ai/glm-4"}); code != exitOK {
		t.Fatalf("models use exit = %d, want 0", code)
	}
	listed := captureStdout(t, func() { runConfigCommand([]string{"get", "provider"}) })
	if strings.TrimSpace(listed) != "opencode" {
		t.Fatalf("config get provider = %q, want opencode", strings.TrimSpace(listed))
	}
	listed = captureStdout(t, func() { runConfigCommand([]string{"get", "model"}) })
	if strings.TrimSpace(listed) != "z-ai/glm-4" {
		t.Fatalf("config get model = %q, want z-ai/glm-4", strings.TrimSpace(listed))
	}
	// The models listing must not fail and must name the active provider.
	out := captureStdout(t, func() {
		if c := runModelsCommand(nil); c != exitOK {
			t.Fatalf("models list exit = %d", c)
		}
	})
	if !strings.Contains(out, "opencode") {
		t.Fatalf("models list omitted opencode:\n%s", out)
	}
}

// TestModelsRejectsUnknownProvider pins the fail-closed selection.
func TestModelsRejectsUnknownProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if code := runModelsCommand([]string{"use", "not-a-provider", "x"}); code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
}

// TestAuthStatusNeverLeaksSecret proves auth status reports credential SOURCE
// without ever printing the secret.
func TestAuthStatusNeverLeaksSecret(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENCODE_API_KEY", "sk-supersecret-value")
	out := captureStdout(t, func() {
		if c := runAuthCommand([]string{"status"}); c != exitOK {
			t.Fatalf("auth status exit = %d", c)
		}
	})
	if strings.Contains(out, "sk-supersecret-value") {
		t.Fatalf("auth status leaked the credential:\n%s", out)
	}
	if !strings.Contains(out, "opencode") || !strings.Contains(out, "env") {
		t.Fatalf("auth status did not report the env credential source:\n%s", out)
	}
}

// TestStatsOnEmptyWorkspace proves `izen stats` reports an empty ledger without
// failing.
func TestStatsOnEmptyWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws := t.TempDir()
	out := captureStdout(t, func() {
		if c := runStatsCommand([]string{"-dir", ws}); c != exitOK {
			t.Fatalf("stats exit = %d", c)
		}
	})
	if !strings.Contains(out, "no durable tasks") {
		t.Fatalf("stats output = %q", out)
	}
}

// TestRunsOnEmptyWorkspace proves `izen runs` reports no tasks without failing.
func TestRunsOnEmptyWorkspace(t *testing.T) {
	ws := t.TempDir()
	out := captureStdout(t, func() {
		if c := runRunsCommand([]string{"-dir", ws}); c != exitOK {
			t.Fatalf("runs exit = %d", c)
		}
	})
	if !strings.Contains(out, "no durable tasks") {
		t.Fatalf("runs output = %q", out)
	}
}

// TestResumeWithoutRecordedTaskFailsClosed proves resume does not invent work.
func TestResumeWithoutRecordedTaskFailsClosed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws := t.TempDir()
	if _, ok := mostRecentRecoverable(ws); ok {
		t.Fatal("mostRecentRecoverable found a task in an empty workspace")
	}
	// ResumeUsage is reachable and the -h path exits 0.
	out := captureStdout(t, func() {
		if c := runResumeCommand([]string{"-h"}); c != exitOK {
			t.Fatalf("resume -h exit = %d", c)
		}
	})
	if !strings.Contains(out, "durable") {
		t.Fatalf("resume help missing durable mention:\n%s", out)
	}
	// A resume against an empty workspace reports a truthful error to stderr.
	if code := runExecRequestAs("$resume", "resume", []string{"-resume", "-dir", ws, "-quiet"}, ""); code == exitOK {
		t.Fatal("resume against an empty workspace reported success")
	}
}
