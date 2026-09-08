// Package policy defines the interactive security-permission contract.
//
// The permission interceptor is a HUMAN AUTHORIZATION gate only. It never
// derives the operation (CREATE/MODIFY/DELETE/SHELL_EXEC): the Engine owns
// operation derivation per the IZEN AUTHORITY INVARIANTS. A PermissionRequest
// carries the already-derived facts (tool, command, targets, risk) so the
// operator can explicitly Allow/Deny. Relevance never grants mutation
// authority — only an explicit PermissionResponse{Allowed:true} does, and
// only for the single request (or its session whitelist pattern).
package policy

import (
	"fmt"
	"strings"
	"sync"
)

// PermissionRiskLevel classifies the blast radius of a tool call.
type PermissionRiskLevel string

const (
	// RiskLow covers read-only operations (file reads, search, symbol resolve).
	RiskLow PermissionRiskLevel = "LOW"
	// RiskMedium covers file writes strictly within the workspace.
	RiskMedium PermissionRiskLevel = "MEDIUM"
	// RiskHigh covers shell execution, system file access, and network egress.
	RiskHigh PermissionRiskLevel = "HIGH"
)

// String returns the canonical risk label.
func (r PermissionRiskLevel) String() string { return string(r) }

// PermissionRequest is the single authorization question posed to the human.
// It is a pure fact record: the Engine derived the operation, the policy
// layer classified the risk, and the UI projects it. It carries no control
// authority — the LLM can never mint one.
type PermissionRequest struct {
	ID          string
	ToolName    string
	Command     string
	TargetPaths []string
	RiskLevel   PermissionRiskLevel
	Reason      string
}

// Describe returns the single-line auditable summary of the request.
func (r PermissionRequest) Describe() string {
	target := r.Command
	if target == "" && len(r.TargetPaths) > 0 {
		target = strings.Join(r.TargetPaths, ", ")
	}
	return fmt.Sprintf("%s %s [%s]", r.ToolName, target, r.RiskLevel)
}

// PermissionResponse is the human's explicit decision for one request.
type PermissionResponse struct {
	RequestID string
	Allowed   bool
	// Remember maps to the "[a] Always Allow for Session" key: the resolving
	// gate must add the request's whitelist pattern to the SessionWhitelist.
	Remember bool
	// EditedCmd carries the operator-rewritten command when the modal was
	// resolved via "[e] Edit Command". Empty otherwise.
	EditedCmd string
	// Edited reports whether EditedCmd should supersede Request.Command.
	Edited bool
}

// Denied reports whether the human refused the request.
func (r PermissionResponse) Denied() bool { return !r.Allowed }

// PermissionDeniedError is the structured denial the executor returns when
// the human rejects a request. It is a terminal authorization outcome —
// never retried silently, never reinterpreted as a downgrade.
type PermissionDeniedError struct {
	RequestID string
	ToolName  string
}

func (e *PermissionDeniedError) Error() string {
	return fmt.Sprintf("permission denied for %s (request %s)", e.ToolName, e.RequestID)
}

// ClassifyRisk derives the risk level from tool facts. It is deterministic:
// shell execution is always HIGH, workspace writes are MEDIUM, everything
// else defaults to LOW. Callers may override with an explicit level.
func ClassifyRisk(toolName, command string, targetPaths []string) PermissionRiskLevel {
	lower := strings.ToLower(strings.TrimSpace(toolName + " " + command))
	shellMarkers := []string{"shell", "exec", "bash", "sh -", "rm ", "rm -", "sudo", "chmod", "chown", "curl", "wget", "ssh", "nc ", "netcat", "iptables"}
	for _, m := range shellMarkers {
		if strings.Contains(lower, m) {
			return RiskHigh
		}
	}
	for _, p := range targetPaths {
		pl := strings.ToLower(p)
		if strings.HasPrefix(pl, "/etc/") || strings.HasPrefix(pl, "/usr/") ||
			strings.HasPrefix(pl, "/bin/") || strings.HasPrefix(pl, "/sbin/") ||
			strings.HasPrefix(pl, "http://") || strings.HasPrefix(pl, "https://") {
			return RiskHigh
		}
	}
	writeMarkers := []string{"write", "mutate", "patch", "modify", "create", "delete", "apply"}
	for _, m := range writeMarkers {
		if strings.Contains(lower, m) {
			return RiskMedium
		}
	}
	if len(targetPaths) > 0 {
		return RiskMedium
	}
	return RiskLow
}

// WhitelistKey returns the session-whitelist pattern for the request: the
// tool name plus the command head (first token), so "shell:rm" style entries
// cover repeats without ever granting a blanket shell bypass.
func (r PermissionRequest) WhitelistKey() string {
	head := ""
	if fields := strings.Fields(r.Command); len(fields) > 0 {
		head = fields[0]
	}
	if head == "" && len(r.TargetPaths) > 0 {
		head = r.TargetPaths[0]
	}
	tool := strings.ToLower(strings.TrimSpace(r.ToolName))
	if tool == "" {
		tool = "unknown"
	}
	return tool + ":" + strings.ToLower(head)
}

// SessionWhitelist records "[a] Always Allow for Session" grants. It is the
// ONLY memory of past approvals: one entry authorizes every later request
// with the same WhitelistKey, nothing broader. Thread-safe: the UI grants on
// the Bubble Tea goroutine while executor gates read from worker goroutines.
type SessionWhitelist struct {
	mu      sync.RWMutex
	entries map[string]struct{}
}

// NewSessionWhitelist returns an empty session whitelist.
func NewSessionWhitelist() *SessionWhitelist {
	return &SessionWhitelist{entries: make(map[string]struct{})}
}

// Add records an always-allow grant for the key.
func (w *SessionWhitelist) Add(key string) {
	if w == nil || key == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.entries[strings.ToLower(strings.TrimSpace(key))] = struct{}{}
}

// AddRequest records the request's whitelist pattern.
func (w *SessionWhitelist) AddRequest(req PermissionRequest) {
	if w == nil {
		return
	}
	w.Add(req.WhitelistKey())
}

// Has reports whether the key was granted this session.
func (w *SessionWhitelist) Has(key string) bool {
	if w == nil || key == "" {
		return false
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	_, ok := w.entries[strings.ToLower(strings.TrimSpace(key))]
	return ok
}

// HasRequest reports whether the request is covered by a session grant.
func (w *SessionWhitelist) HasRequest(req PermissionRequest) bool {
	if w == nil {
		return false
	}
	return w.Has(req.WhitelistKey())
}

// Len returns the number of session grants.
func (w *SessionWhitelist) Len() int {
	if w == nil {
		return 0
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return len(w.entries)
}

// Clear drops every session grant (mode transition / /clear parity with the
// legacy pendingBuildAllowAlways reset).
func (w *SessionWhitelist) Clear() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.entries = make(map[string]struct{})
}
