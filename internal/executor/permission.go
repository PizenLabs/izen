// Package executor — permission gate.
//
// PermissionGate is the execution-side half of the interactive security
// permission interceptor. It suspends tool execution via the Go channel
// response pattern:
//
//	respCh := make(chan policy.PermissionResponse, 1)
//	dispatch(PermissionPromptMsg{Req: req, RespCh: respCh})
//	res := <-respCh // blocks the agent goroutine until the human resolves
//
// The gate never touches Bubble Tea: the caller injects a DispatchFunc that
// forwards the prompt message onto the UI event loop (e.g. program.Send).
// Session "[a] Always Allow" grants short-circuit without prompting. Denial
// yields a structured *policy.PermissionDeniedError — never a silent
// downgrade — per the IZEN AUTHORITY INVARIANTS (conflict preservation,
// authorization independent of resolution).
package executor

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/PizenLabs/izen/internal/policy"
)

// DispatchFunc forwards a permission prompt to the UI event loop. It must
// guarantee the UI will eventually deliver exactly one PermissionResponse on
// RespCh (allow or deny). The gate blocks until that delivery or ctx done.
type DispatchFunc func(req policy.PermissionRequest, respCh chan policy.PermissionResponse)

// PermissionGate suspends unsafe tool calls on human authorization.
type PermissionGate struct {
	whitelist *policy.SessionWhitelist
	dispatch  DispatchFunc
	seq       atomic.Uint64
}

// NewPermissionGate builds a gate. A nil whitelist allocates a fresh session
// whitelist; a nil dispatch means "deny by default" (headless safety: no UI
// to authorize means no execution).
func NewPermissionGate(whitelist *policy.SessionWhitelist, dispatch DispatchFunc) *PermissionGate {
	if whitelist == nil {
		whitelist = policy.NewSessionWhitelist()
	}
	return &PermissionGate{whitelist: whitelist, dispatch: dispatch}
}

// Whitelist exposes the session grant ledger (for UI wiring parity).
func (g *PermissionGate) Whitelist() *policy.SessionWhitelist { return g.whitelist }

// NeedsApproval reports whether the request must prompt. Session "[a]" grants
// and LOW-risk reads pass without prompting; everything else intercepts.
func (g *PermissionGate) NeedsApproval(req policy.PermissionRequest) bool {
	if g == nil {
		return true
	}
	if g.whitelist.HasRequest(req) {
		return false
	}
	if req.RiskLevel == "" || req.RiskLevel == policy.RiskLow {
		// Low-risk read-only operations never intercept.
		return false
	}
	return true
}

// Request blocks the calling agent goroutine until the human resolves the
// prompt. It returns the effective command (edited when the human rewrote it)
// and nil on approval, or a *policy.PermissionDeniedError on denial. A nil
// dispatch, a nil response channel contract violation, or ctx cancellation
// denies safely rather than executing without authorization.
func (g *PermissionGate) Request(ctx context.Context, req policy.PermissionRequest) (string, error) {
	if g == nil {
		return "", &policy.PermissionDeniedError{RequestID: req.ID, ToolName: req.ToolName}
	}
	if req.ID == "" {
		req.ID = fmt.Sprintf("perm-%d", g.seq.Add(1))
	}
	if req.RiskLevel == "" {
		req.RiskLevel = policy.ClassifyRisk(req.ToolName, req.Command, req.TargetPaths)
	}
	if !g.NeedsApproval(req) {
		return req.Command, nil
	}
	if g.dispatch == nil {
		return "", &policy.PermissionDeniedError{RequestID: req.ID, ToolName: req.ToolName}
	}
	respCh := make(chan policy.PermissionResponse, 1)
	g.dispatch(req, respCh)
	select {
	case <-ctx.Done():
		return "", &policy.PermissionDeniedError{RequestID: req.ID, ToolName: req.ToolName}
	case resp := <-respCh:
		if !resp.Allowed {
			return "", &policy.PermissionDeniedError{RequestID: resp.RequestID, ToolName: req.ToolName}
		}
		if resp.Remember {
			g.whitelist.AddRequest(req)
		}
		if resp.Edited {
			return resp.EditedCmd, nil
		}
		return req.Command, nil
	}
}

// Remember records a session grant without prompting (test seam + programmatic
// pre-authorization for trusted harnesses).
func (g *PermissionGate) Remember(req policy.PermissionRequest) {
	if g == nil {
		return
	}
	g.whitelist.AddRequest(req)
}
