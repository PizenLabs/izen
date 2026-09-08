package policy

import "testing"

func TestClassifyRisk_ShellIsHigh(t *testing.T) {
	req := PermissionRequest{ToolName: "shell_exec", Command: "rm -rf tmp/", Reason: "test"}
	if got := ClassifyRisk(req.ToolName, req.Command, nil); got != RiskHigh {
		t.Fatalf("ClassifyRisk = %s, want HIGH", got)
	}
}

func TestClassifyRisk_WorkspaceWriteIsMedium(t *testing.T) {
	if got := ClassifyRisk("file_write", "write main.go", []string{"main.go"}); got != RiskMedium {
		t.Fatalf("ClassifyRisk = %s, want MEDIUM", got)
	}
}

func TestClassifyRisk_ReadIsLow(t *testing.T) {
	if got := ClassifyRisk("file_read", "read main.go", nil); got != RiskLow {
		t.Fatalf("ClassifyRisk = %s, want LOW", got)
	}
}

func TestClassifyRisk_SystemPathIsHigh(t *testing.T) {
	if got := ClassifyRisk("file_write", "write", []string{"/etc/passwd"}); got != RiskHigh {
		t.Fatalf("ClassifyRisk = %s, want HIGH", got)
	}
}

func TestSessionWhitelist_RequestRoundTrip(t *testing.T) {
	w := NewSessionWhitelist()
	req := PermissionRequest{ID: "p1", ToolName: "shell_exec", Command: "rm -rf tmp/"}
	if w.HasRequest(req) {
		t.Fatal("fresh whitelist must not cover request")
	}
	w.AddRequest(req)
	if !w.HasRequest(req) {
		t.Fatal("whitelist must cover request after AddRequest")
	}
	if w.Len() != 1 {
		t.Fatalf("Len = %d, want 1", w.Len())
	}
	w.Clear()
	if w.HasRequest(req) {
		t.Fatal("whitelist must be empty after Clear")
	}
}

func TestPermissionDeniedError(t *testing.T) {
	err := &PermissionDeniedError{RequestID: "r1", ToolName: "shell_exec"}
	if err.Error() == "" {
		t.Fatal("denial error must carry a message")
	}
}

func TestWhitelistKey_Stable(t *testing.T) {
	a := PermissionRequest{ToolName: "shell_exec", Command: "rm -rf tmp/"}
	b := PermissionRequest{ToolName: "SHELL_EXEC", Command: "rm other"}
	if a.WhitelistKey() != b.WhitelistKey() {
		t.Fatalf("same tool+head must share key: %q vs %q", a.WhitelistKey(), b.WhitelistKey())
	}
}
