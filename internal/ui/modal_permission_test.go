package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/PizenLabs/izen/internal/policy"
)

// stagePermission opens a HIGH-risk shell request on the test model.
func stagePermission(t *testing.T, m *model) chan policy.PermissionResponse {
	t.Helper()
	respCh := make(chan policy.PermissionResponse, 1)
	req := policy.PermissionRequest{
		ID:        "perm-test-1",
		ToolName:  "shell_exec",
		Command:   "rm -rf tmp/",
		RiskLevel: policy.RiskHigh,
		Reason:    "Shell execution requires explicit authorization.",
	}
	_, _ = m.Update(PermissionPromptMsg{Req: req, RespCh: respCh})
	if m.pendingPermission == nil {
		t.Fatal("PermissionPromptMsg must stage the modal")
	}
	return respCh
}

func TestPermissionModal_InterceptRendersHighRisk(t *testing.T) {
	m := newTestModel()
	respCh := stagePermission(t, m)
	defer func() {
		select {
		case <-respCh:
		default:
		}
	}()

	// Modal halts: key input must not reach the text input.
	m.ti.SetValue("")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if m.pendingPermission != nil {
		t.Fatal("pressing y must resolve the modal")
	}
	select {
	case resp := <-respCh:
		if !resp.Allowed {
			t.Fatal("y must allow the request")
		}
		if resp.Remember {
			t.Fatal("y must not set Remember")
		}
	default:
		t.Fatal("y must deliver a response on RespCh (agent unblocked)")
	}

	// Render path: re-stage and assert the HIGH-risk modal content.
	m2 := newTestModel()
	respCh2 := make(chan policy.PermissionResponse, 1)
	req := policy.PermissionRequest{
		ID: "perm-test-2", ToolName: "shell_exec", Command: "rm -rf tmp/",
		RiskLevel: policy.RiskHigh, Reason: "Shell execution requires explicit authorization.",
	}
	_, _ = m2.Update(PermissionPromptMsg{Req: req, RespCh: respCh2})
	out := renderPermissionModal(req, 100, false, "")
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "SECURITY INTERCEPT") {
		t.Fatalf("high-risk modal must carry [SECURITY INTERCEPT], got:\n%s", plain)
	}
	if !strings.Contains(plain, "rm -rf tmp/") {
		t.Fatal("modal must show the exact command")
	}
	for _, key := range []string{"[y] Allow Once", "[a] Always Allow", "[n] Deny", "[e] Edit Command"} {
		if !strings.Contains(plain, key) {
			t.Fatalf("modal footer must contain %q", key)
		}
	}
	select {
	case <-respCh2:
		t.Fatal("unstaged render must not resolve")
	default:
	}
	// Cleanup: deny so no channel leaks.
	_, _ = m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
}

func TestPermissionModal_Keybindings(t *testing.T) {
	// a → Always Allow + session whitelist.
	m := newTestModel()
	respCh := stagePermission(t, m)
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	select {
	case resp := <-respCh:
		if !resp.Allowed || !resp.Remember {
			t.Fatalf("a must allow+remember, got %+v", resp)
		}
	default:
		t.Fatal("a must deliver a response")
	}
	if m.pendingPermission != nil {
		t.Fatal("a must close the modal")
	}
	if !m.isPermissionAllowedBySession(policy.PermissionRequest{ToolName: "shell_exec", Command: "rm other"}) {
		t.Fatal("a must whitelist the tool pattern for the session")
	}

	// Subsequent identical tool calls pass without prompting.
	respCh2 := make(chan policy.PermissionResponse, 1)
	_, cmd := m.Update(PermissionPromptMsg{
		Req:    policy.PermissionRequest{ID: "perm-test-3", ToolName: "shell_exec", Command: "rm other", RiskLevel: policy.RiskHigh},
		RespCh: respCh2,
	})
	if m.pendingPermission != nil {
		t.Fatal("whitelisted tool must not re-prompt")
	}
	if cmd == nil {
		t.Fatal("whitelist bypass must emit PermissionResolvedMsg")
	}
	if msg := cmd(); msg == nil {
		t.Fatal("whitelist bypass cmd must produce a message")
	} else if resolved, ok := msg.(PermissionResolvedMsg); !ok || !resolved.Resp.Allowed {
		t.Fatalf("bypass must resolve allowed, got %#v", msg)
	}

	// n → Deny.
	m2 := newTestModel()
	respCh3 := stagePermission(t, m2)
	_, _ = m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	select {
	case resp := <-respCh3:
		if resp.Allowed {
			t.Fatal("n must deny")
		}
	default:
		t.Fatal("n must deliver a denial")
	}

	// Enter → Allow Once (no remember).
	m3 := newTestModel()
	respCh4 := stagePermission(t, m3)
	_, _ = m3.Update(tea.KeyMsg{Type: tea.KeyEnter})
	select {
	case resp := <-respCh4:
		if !resp.Allowed || resp.Remember {
			t.Fatalf("Enter must allow-once, got %+v", resp)
		}
	default:
		t.Fatal("Enter must deliver a response")
	}

	// Esc → Deny.
	m4 := newTestModel()
	respCh5 := stagePermission(t, m4)
	_, _ = m4.Update(tea.KeyMsg{Type: tea.KeyEscape})
	select {
	case resp := <-respCh5:
		if resp.Allowed {
			t.Fatal("Esc must deny")
		}
	default:
		t.Fatal("Esc must deliver a denial")
	}
}

func TestPermissionModal_EditCommand(t *testing.T) {
	m := newTestModel()
	respCh := stagePermission(t, m)

	// e enters inline edit mode with the command prefilled.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if !m.permissionEditing {
		t.Fatal("e must enter edit mode")
	}
	if m.permissionEditValue != "rm -rf tmp/" {
		t.Fatalf("edit buffer = %q, want original command", m.permissionEditValue)
	}
	if m.pendingPermission == nil {
		t.Fatal("entering edit mode must keep the request pending")
	}

	// Type an suffix, then submit with Enter.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' ', 's', 'a', 'f', 'e'}})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	select {
	case resp := <-respCh:
		if !resp.Allowed || !resp.Edited {
			t.Fatalf("edit submit must allow+edited, got %+v", resp)
		}
		if resp.EditedCmd != "rm -rf tmp/ safe" {
			t.Fatalf("EditedCmd = %q", resp.EditedCmd)
		}
	default:
		t.Fatal("edit submit must deliver a response")
	}
}

func TestPermissionModal_RiskBorderMapping(t *testing.T) {
	low := renderPermissionModal(policy.PermissionRequest{ToolName: "t", RiskLevel: policy.RiskLow}, 80, false, "")
	med := renderPermissionModal(policy.PermissionRequest{ToolName: "t", RiskLevel: policy.RiskMedium}, 80, false, "")
	high := renderPermissionModal(policy.PermissionRequest{ToolName: "t", RiskLevel: policy.RiskHigh}, 80, false, "")
	if low == med || med == high || low == high {
		t.Fatal("each risk level must render a distinct border color")
	}
	if !strings.Contains(high, "SECURITY INTERCEPT") {
		t.Fatal("HIGH must render the warning header")
	}
	if strings.Contains(low, "SECURITY INTERCEPT") {
		t.Fatal("LOW must not render the warning header")
	}
}
