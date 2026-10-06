// Package live_r6 is the R6-A CANCELLATION experiment against a REAL provider.
//
// The deterministic suite (internal/runtime/autonomy/r6_cancellation_test.go,
// internal/execution/r6_cancellation_test.go) proves the cancellation contract
// with test doubles. This opt-in experiment proves the same contract over the
// real production composition (compose.Wire → autonomy.Driver → RuntimeExecutor)
// and a real local model, with a real in-flight provider call interrupted by a
// real context cancellation.
//
// The property under test is NOT timing. It is:
//
//	a real provider invocation is in flight (observed, not slept on)
//	  → the human cancels
//	  → the run reaches a typed terminal ABORTED state
//	  → the workspace has a zero delta (no post-cancel mutation)
//	  → no objective is declared PROVEN
//
// It is opt-in via IZEN_LIVE_FORENSICS=1, exactly like test/live_r1/r2/r4/r5,
// and is deliberately NOT part of `go test ./...`.
package live_r6

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/autonomy"
	"github.com/PizenLabs/izen/internal/config"
	"github.com/PizenLabs/izen/internal/events"
	"github.com/PizenLabs/izen/internal/forensics"
	"github.com/PizenLabs/izen/internal/providers"
	appruntime "github.com/PizenLabs/izen/internal/runtime"
	"github.com/PizenLabs/izen/internal/runtime/compose"
)

const (
	liveModel   = "qwen2.5-coder:7b"
	liveBaseURL = "http://127.0.0.1:11434/v1"
	liveOptIn   = "IZEN_LIVE_FORENSICS"

	// r6Objective is a legitimate mutation objective whose provider call is
	// long enough to cancel deterministically. The cancellation is triggered by
	// an OBSERVED provider lifecycle event (model.invoked / provider.waiting),
	// never by a sleep.
	r6Objective = `rewrite the complete @index.html file so the h1 greeting reads exactly "hello from the cancelled run"; output the entire updated file`

	r6ArtifactDirEnv = "IZEN_R6_ARTIFACTS"

	r6IndexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Greeting</title>
</head>
<body>
<h1 id="greeting">goodbye</h1>
<p id="intro">Welcome to the demo page served by the workspace runtime.</p>
</body>
</html>
`
)

func requireLiveModel(t *testing.T) {
	t.Helper()
	if os.Getenv(liveOptIn) != "1" {
		t.Skipf("set %s=1 to run the live R6 experiment (needs a local model server)", liveOptIn)
	}
	endpoint := strings.TrimSuffix(liveBaseURL, "/v1") + "/api/tags"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := exec.CommandContext(ctx, "curl", "-sS", "--max-time", "5", endpoint)
	client.Env = append(os.Environ(), "NO_PROXY=*")
	out, err := client.CombinedOutput()
	if err != nil {
		t.Skipf("no local model server at %s: %v", endpoint, err)
	}
	if !strings.Contains(string(out), liveModel) {
		t.Skipf("model %q is not installed locally: %s", liveModel, out)
	}
}

func gitInit(t *testing.T, root string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=izen", "GIT_AUTHOR_EMAIL=izen@local",
			"GIT_COMMITTER_NAME=izen", "GIT_COMMITTER_EMAIL=izen@local")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("add", "-A")
	run("commit", "-q", "-m", "baseline")
}

func liveApp(t *testing.T) (*compose.Application, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(r6IndexHTML), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInit(t, root)

	cfg := config.Default()
	cfg.Bindings.Active.Provider = "ollama"
	cfg.Bindings.Active.Model = liveModel
	app, err := compose.Wire(
		compose.WithRoot(root),
		compose.WithConfig(cfg),
		compose.WithProvider(providers.NewOllamaProvider(liveBaseURL, "ollama", liveModel)),
	)
	if err != nil {
		t.Fatalf("wire: %v", err)
	}
	t.Cleanup(app.Close)

	if _, err := os.Stat(filepath.Join(root, ".izen")); err != nil {
		if err := os.MkdirAll(filepath.Join(root, ".izen"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.Execution.ShadowCP.CreateSessionStartSnapshot(); err != nil {
		t.Fatalf("session-start checkpoint (production precondition): %v", err)
	}
	app.Runtime.Start()
	if err := app.Runtime.Execute(context.Background(),
		appruntime.SubmitPromptCmd{Prompt: r6Objective, Mode: "build"}); err != nil {
		t.Fatalf("SubmitPromptCmd (production preflight dispatch): %v", err)
	}
	return app, root
}

// TestLiveR6_RealProviderCancellationIsBounded proves the real cancellation
// contract: a real in-flight Ollama call, cancelled by context, reaches a typed
// ABORTED terminal with a zero-delta workspace and no PROVEN objective.
func TestLiveR6_RealProviderCancellationIsBounded(t *testing.T) {
	requireLiveModel(t)
	app, root := liveApp(t)

	baseline, err := os.ReadFile(filepath.Join(root, "index.html"))
	if err != nil {
		t.Fatal(err)
	}

	rec := forensics.NewRecorder()
	sub := app.Bus.SubscribeAll(rec.Handle)
	defer sub.Cancel()

	d := app.Autonomous
	d.SetScope("$prompt")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct {
		term *autonomy.LoopTermination
		err  error
	}, 1)
	go func() {
		term, err := d.Run(ctx, r6Objective)
		done <- struct {
			term *autonomy.LoopTermination
			err  error
		}{term, err}
	}()

	// DETERMINISTIC COORDINATION: cancel only once the REAL provider invocation
	// is observed in flight. No sleep.
	if rec.WaitFor(events.EventModelInvoked, 1, 3*time.Minute) < 1 {
		t.Fatalf("real provider invocation never started within the timeout")
	}
	t.Logf("R6: provider invocation observed in flight — cancelling now")
	cancel()

	var r struct {
		term *autonomy.LoopTermination
		err  error
	}
	select {
	case r = <-done:
	case <-time.After(2 * time.Minute):
		t.Fatal("driver.Run did not return after cancellation")
	}
	rec.WaitQuiet(250*time.Millisecond, 20*time.Second)

	// A cancellation may surface as a nil error (clean abort) — never as an
	// unrelated failure.
	if r.err != nil && !errors.Is(r.err, context.Canceled) {
		t.Fatalf("driver.Run returned a non-cancellation error: %v", r.err)
	}
	if r.term == nil || r.term.State != autonomy.RuntimeAborted {
		t.Fatalf("termination = %+v, want aborted on real cancellation", r.term)
	}
	if r.term.Class != autonomy.FailurePermanent {
		t.Fatalf("termination class = %s, want permanent", r.term.Class)
	}
	if d.State() != autonomy.RuntimeAborted {
		t.Fatalf("driver state = %s, want aborted", d.State())
	}

	// ZERO DELTA: the cancellation stopped the run before any mutation.
	after, err := os.ReadFile(filepath.Join(root, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(baseline) {
		t.Fatalf("post-cancel mutation: workspace changed\nbefore:\n%s\nafter:\n%s", baseline, after)
	}

	// NO PROVEN: a cancelled run must never have been declared PROVEN.
	stream := rec.Stream()
	persistArtifacts(t, stream)
	tr, terr := forensics.NewTrace(stream)
	if terr == nil {
		for _, o := range tr.ObjectiveStates {
			if o.Granted && strings.EqualFold(strings.TrimSpace(o.State), "proven") {
				t.Fatalf("cancelled run declared the objective PROVEN\n%s", tr.Render())
			}
		}
		t.Logf("\n%s", tr.Render())
	}

	t.Logf("R6: real cancellation aborted cleanly; workspace byte-identical (%d bytes); no PROVEN objective", len(baseline))
}

func persistArtifacts(t *testing.T, stream []events.DomainEvent) {
	t.Helper()
	dir := os.Getenv(r6ArtifactDirEnv)
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("r6 artifacts dir: %v", err)
	}
	var lines []string
	for _, ev := range stream {
		payload := map[string]any{"type": ev.Type(), "at": ev.Timestamp()}
		b, err := json.Marshal(ev.Payload())
		if err == nil {
			payload["payload"] = json.RawMessage(b)
		}
		line, err := json.Marshal(payload)
		if err != nil {
			continue
		}
		lines = append(lines, string(line))
	}
	if err := os.WriteFile(filepath.Join(dir, "r6-events.ndjson"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write raw events: %v", err)
	}
	t.Logf("R6 artifacts written to %s", dir)
}
