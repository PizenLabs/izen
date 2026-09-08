package executor

import (
	"context"
	"testing"
	"time"

	"github.com/PizenLabs/izen/internal/policy"
)

func TestPermissionGate_LowRiskPassesWithoutPrompt(t *testing.T) {
	var dispatched bool
	g := NewPermissionGate(nil, func(req policy.PermissionRequest, ch chan policy.PermissionResponse) {
		dispatched = true
	})
	req := policy.PermissionRequest{ID: "r1", ToolName: "file_read", Command: "read main.go", RiskLevel: policy.RiskLow}
	cmd, err := g.Request(context.Background(), req)
	if err != nil {
		t.Fatalf("low-risk must pass, got %v", err)
	}
	if cmd != req.Command {
		t.Fatalf("cmd = %q, want %q", cmd, req.Command)
	}
	if dispatched {
		t.Fatal("low-risk must not dispatch a prompt")
	}
}

func TestPermissionGate_HighRiskBlocksUntilAllow(t *testing.T) {
	g := NewPermissionGate(nil, func(req policy.PermissionRequest, ch chan policy.PermissionResponse) {
		ch <- policy.PermissionResponse{RequestID: req.ID, Allowed: true}
	})
	req := policy.PermissionRequest{ID: "r2", ToolName: "shell_exec", Command: "rm -rf tmp/", RiskLevel: policy.RiskHigh}
	cmd, err := g.Request(context.Background(), req)
	if err != nil {
		t.Fatalf("allowed request must pass, got %v", err)
	}
	if cmd != req.Command {
		t.Fatalf("cmd = %q, want %q", cmd, req.Command)
	}
}

func TestPermissionGate_DenyYieldsStructuredError(t *testing.T) {
	g := NewPermissionGate(nil, func(req policy.PermissionRequest, ch chan policy.PermissionResponse) {
		ch <- policy.PermissionResponse{RequestID: req.ID, Allowed: false}
	})
	req := policy.PermissionRequest{ID: "r3", ToolName: "shell_exec", Command: "rm -rf tmp/", RiskLevel: policy.RiskHigh}
	if _, err := g.Request(context.Background(), req); err == nil {
		t.Fatal("denied request must error")
	} else if _, ok := err.(*policy.PermissionDeniedError); !ok {
		t.Fatalf("err type = %T, want *policy.PermissionDeniedError", err)
	}
}

func TestPermissionGate_RememberSkipsSecondPrompt(t *testing.T) {
	prompts := 0
	g := NewPermissionGate(nil, func(req policy.PermissionRequest, ch chan policy.PermissionResponse) {
		prompts++
		ch <- policy.PermissionResponse{RequestID: req.ID, Allowed: true, Remember: true}
	})
	req := policy.PermissionRequest{ID: "r4", ToolName: "shell_exec", Command: "rm -rf tmp/", RiskLevel: policy.RiskHigh}
	if _, err := g.Request(context.Background(), req); err != nil {
		t.Fatalf("first request: %v", err)
	}
	req2 := policy.PermissionRequest{ID: "r5", ToolName: "shell_exec", Command: "rm other", RiskLevel: policy.RiskHigh}
	if _, err := g.Request(context.Background(), req2); err != nil {
		t.Fatalf("whitelisted request: %v", err)
	}
	if prompts != 1 {
		t.Fatalf("prompts = %d, want 1 (second call whitelisted)", prompts)
	}
}

func TestPermissionGate_EditedCommandReturned(t *testing.T) {
	g := NewPermissionGate(nil, func(req policy.PermissionRequest, ch chan policy.PermissionResponse) {
		ch <- policy.PermissionResponse{RequestID: req.ID, Allowed: true, EditedCmd: "rm -rf tmp/safe", Edited: true}
	})
	req := policy.PermissionRequest{ID: "r6", ToolName: "shell_exec", Command: "rm -rf tmp/", RiskLevel: policy.RiskHigh}
	cmd, err := g.Request(context.Background(), req)
	if err != nil {
		t.Fatalf("edited allow: %v", err)
	}
	if cmd != "rm -rf tmp/safe" {
		t.Fatalf("cmd = %q, want edited command", cmd)
	}
}

func TestPermissionGate_CancelDenies(t *testing.T) {
	g := NewPermissionGate(nil, func(req policy.PermissionRequest, ch chan policy.PermissionResponse) {
		// Never respond: ctx cancellation must release the waiter.
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	req := policy.PermissionRequest{ID: "r7", ToolName: "shell_exec", Command: "rm -rf tmp/", RiskLevel: policy.RiskHigh}
	if _, err := g.Request(ctx, req); err == nil {
		t.Fatal("cancelled request must deny")
	}
}

func TestPermissionGate_NilDispatchDenies(t *testing.T) {
	g := NewPermissionGate(nil, nil)
	req := policy.PermissionRequest{ID: "r8", ToolName: "shell_exec", Command: "rm -rf tmp/", RiskLevel: policy.RiskHigh}
	if _, err := g.Request(context.Background(), req); err == nil {
		t.Fatal("headless gate without dispatch must deny")
	}
}
