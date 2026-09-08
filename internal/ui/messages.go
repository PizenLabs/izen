package ui

import (
	"github.com/PizenLabs/izen/internal/policy"
)

// PermissionPromptMsg triggers the interactive security-permission modal and
// blocks the requesting agent goroutine until the human resolves it.
//
// The sender creates a buffered RespCh (capacity 1), dispatches this message
// onto the Bubble Tea event loop (e.g. program.Send), then blocks on
// `<-RespCh`. The UI resolves exactly once via PermissionResolvedMsg and
// always delivers a PermissionResponse — denial included — so the agent
// goroutine can never hang on an abandoned channel.
type PermissionPromptMsg struct {
	Req    policy.PermissionRequest
	RespCh chan policy.PermissionResponse
}

// PermissionResolvedMsg delivers the human's explicit decision back to the
// Agent Executor loop. The Update handler forwards Resp into the request's
// RespCh (non-blocking) and clears the modal state.
type PermissionResolvedMsg struct {
	Resp policy.PermissionResponse
}
