package main

import (
	"testing"

	"github.com/PizenLabs/izen/internal/cli"
)

// TestParseExecArgs pins the shared execution-command grammar. The public
// syntax (-dir, repeatable -target, exactly one objective, -h) is preserved,
// and the automation flags (provider/model/mode/approve/timeout/json/quiet)
// are parsed strictly: an unknown flag or an invalid value fails with a usage
// error rather than being silently ignored.
func TestParseExecArgs(t *testing.T) {
	cases := []struct {
		name         string
		args         []string
		wantDir      string
		wantTargets  []string
		wantPrompt   string
		wantHelp     bool
		wantErr      bool
		wantMode     string
		wantApproval cli.ApprovalPolicy
		wantJSON     bool
	}{
		{name: "bare prompt", args: []string{"redesign the portfolio"},
			wantDir: ".", wantPrompt: "redesign the portfolio", wantMode: "build", wantApproval: cli.ApprovalAsk},
		{name: "dir and repeatable targets", args: []string{"-dir", "/ws", "add a health check", "-target", "main.go", "-target", "server.go"},
			wantDir: "/ws", wantPrompt: "add a health check", wantTargets: []string{"main.go", "server.go"}, wantMode: "build", wantApproval: cli.ApprovalAsk},
		{name: "automation flags", args: []string{"--json", "-quiet", "-approve", "always", "-mode", "plan", "-timeout", "90s", "-provider", "opencode", "-model", "z-ai/glm-4", "explain it"},
			wantDir: ".", wantPrompt: "explain it", wantMode: "plan", wantApproval: cli.ApprovalAlways, wantJSON: true},
		{name: "inline equals", args: []string{"-dir=/tmp/ws", "-approve=never", "do the thing"},
			wantDir: "/tmp/ws", wantPrompt: "do the thing", wantMode: "build", wantApproval: cli.ApprovalNever},
		{name: "help", args: []string{"-h"}, wantHelp: true},
		{name: "missing prompt", args: nil, wantErr: true},
		{name: "two prompts", args: []string{"one", "two"}, wantErr: true},
		{name: "dir without value", args: []string{"-dir"}, wantErr: true},
		{name: "target without value", args: []string{"-target"}, wantErr: true},
		{name: "unknown flag", args: []string{"-bogus"}, wantErr: true},
		{name: "invalid mode", args: []string{"-mode", "destroy", "x"}, wantErr: true},
		{name: "invalid approve", args: []string{"-approve", "maybe", "x"}, wantErr: true},
		{name: "invalid timeout", args: []string{"-timeout", "soon", "x"}, wantErr: true},
		{name: "unknown provider", args: []string{"-provider", "nope", "x"}, wantErr: true},
		{name: "provider model mismatch", args: []string{"-provider", "ollama", "-model", "cohere/x:free", "x"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, help, err := parseExecArgs("run", tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseExecArgs(%v) = nil error, want failure", tc.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseExecArgs(%v): %v", tc.args, err)
			}
			if help != tc.wantHelp {
				t.Fatalf("help = %t, want %t", help, tc.wantHelp)
			}
			if tc.wantHelp {
				return
			}
			if req.Dir != tc.wantDir || req.Objective != tc.wantPrompt {
				t.Fatalf("req = (dir=%q objective=%q), want (dir=%q objective=%q)", req.Dir, req.Objective, tc.wantDir, tc.wantPrompt)
			}
			if req.Mode != tc.wantMode {
				t.Fatalf("mode = %q, want %q", req.Mode, tc.wantMode)
			}
			if req.Approval != tc.wantApproval {
				t.Fatalf("approval = %q, want %q", req.Approval, tc.wantApproval)
			}
			if req.JSON != tc.wantJSON {
				t.Fatalf("json = %t, want %t", req.JSON, tc.wantJSON)
			}
			if req.Scope != "$run" {
				t.Fatalf("scope = %q, want $run", req.Scope)
			}
			if !equalStrings(req.Targets, tc.wantTargets) {
				t.Fatalf("targets = %v, want %v", req.Targets, tc.wantTargets)
			}
		})
	}
}

// TestParseExecArgsResume pins the resume grammar: -resume carries no objective.
func TestParseExecArgsResume(t *testing.T) {
	req, _, err := parseExecArgs("resume", []string{"-resume", "-dir", "/ws"})
	if err != nil {
		t.Fatalf("resume parse: %v", err)
	}
	if !req.Resume || req.Dir != "/ws" || req.Objective != "" {
		t.Fatalf("resume req = %+v", req)
	}
	if _, _, err := parseExecArgs("resume", []string{"-resume", "extra objective"}); err == nil {
		t.Fatal("resume accepted an objective argument")
	}
}

// TestParseApprovalPolicy pins the approval vocabulary.
func TestParseApprovalPolicy(t *testing.T) {
	cases := map[string]cli.ApprovalPolicy{
		"":       cli.ApprovalAsk,
		"ask":    cli.ApprovalAsk,
		"always": cli.ApprovalAlways,
		"auto":   cli.ApprovalAlways,
		"never":  cli.ApprovalNever,
		"deny":   cli.ApprovalNever,
		"  ASK ": cli.ApprovalAsk, //nolint:gocritic // intentional surrounding whitespace exercises TrimSpace
	}
	for in, want := range cases {
		got, err := cli.ParseApprovalPolicy(in)
		if err != nil {
			t.Fatalf("ParseApprovalPolicy(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("ParseApprovalPolicy(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := cli.ParseApprovalPolicy("nonsense"); err == nil {
		t.Error("ParseApprovalPolicy accepted nonsense")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
