package main

import (
	"reflect"
	"testing"
)

// TestParseRunArgs pins the `izen run` argument grammar after the Phase 3
// migration: the public syntax (-dir, repeatable -target, one prompt, -h) is
// preserved even though the lifecycle behind it changed. The parser must not
// silently accept ambiguity (two prompts, a flag without its value).
func TestParseRunArgs(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantDir     string
		wantTargets []string
		wantPrompt  string
		wantHelp    bool
		wantErr     bool
	}{
		{name: "bare prompt", args: []string{"redesign the portfolio"},
			wantDir: ".", wantPrompt: "redesign the portfolio"},
		{name: "dir and repeatable targets", args: []string{"-dir", "/ws", "add a health check", "-target", "main.go", "-target", "server.go"},
			wantDir: "/ws", wantPrompt: "add a health check", wantTargets: []string{"main.go", "server.go"}},
		{name: "help", args: []string{"-h"}, wantHelp: true},
		{name: "missing prompt", args: nil, wantErr: true},
		{name: "two prompts", args: []string{"one", "two"}, wantErr: true},
		{name: "dir without value", args: []string{"-dir"}, wantErr: true},
		{name: "target without value", args: []string{"-target"}, wantErr: true},
		{name: "unknown flag", args: []string{"-bogus"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, targets, prompt, help, err := parseRunArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseRunArgs(%v) = nil error, want failure", tc.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRunArgs(%v): %v", tc.args, err)
			}
			if dir != tc.wantDir || prompt != tc.wantPrompt || help != tc.wantHelp {
				t.Fatalf("parseRunArgs(%v) = (dir=%q prompt=%q help=%t), want (dir=%q prompt=%q help=%t)",
					tc.args, dir, prompt, help, tc.wantDir, tc.wantPrompt, tc.wantHelp)
			}
			if !reflect.DeepEqual(targets, tc.wantTargets) {
				t.Fatalf("parseRunArgs(%v) targets = %v, want %v", tc.args, targets, tc.wantTargets)
			}
		})
	}
}
