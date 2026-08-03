// Package presentation is the Presentation layer of the Izen architecture
// (RFC v1.0 section 2). It is a pure view layer: it holds NO workflow state
// and never invokes domain or infrastructure services directly. Every user
// interaction is expressed as a runtime.RuntimeCommand executed through the
// Application-layer Runtime facade, and every state change is received as a
// runtime.PresentationEvent projected onto the view.
//
// Import boundary: this package imports only the Application layer
// (internal/runtime), the UI framework (bubbletea), and the standard library.
package presentation

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PizenLabs/izen/internal/runtime"
)

// Bridge is the single command/event gateway between the presentation layer
// and the Application layer. It wraps the Runtime facade and exposes the five
// canonical user interactions as strongly-typed methods.
//
// Bridge is safe for concurrent use: every method delegates to
// runtime.Runtime.Execute, which is safe for concurrent invocation.
type Bridge struct {
	rt *runtime.Runtime
}

// New returns a Bridge bound to the given Runtime. A nil runtime yields a
// no-op Bridge (all methods return nil) so the presentation layer can degrade
// gracefully in harnesses that never wire a runtime.
func New(rt *runtime.Runtime) *Bridge {
	return &Bridge{rt: rt}
}

// Runtime returns the underlying Runtime facade.
func (b *Bridge) Runtime() *runtime.Runtime {
	if b == nil {
		return nil
	}
	return b.rt
}

// Submit submits a prompt for the workflow to process in the given mode.
func (b *Bridge) Submit(ctx context.Context, prompt, mode string) error {
	return b.Execute(ctx, runtime.SubmitPromptCmd{Prompt: prompt, Mode: mode})
}

// SwitchMode requests a workflow phase transition.
func (b *Bridge) SwitchMode(ctx context.Context, mode string) error {
	return b.Execute(ctx, runtime.SwitchModeCmd{Mode: mode})
}

// ApprovePatch approves a pending patch.
func (b *Bridge) ApprovePatch(ctx context.Context, patchID string) error {
	return b.Execute(ctx, runtime.ApprovePatchCmd{PatchID: patchID})
}

// RejectPatch rejects a pending patch, optionally with a reason.
func (b *Bridge) RejectPatch(ctx context.Context, patchID, reason string) error {
	return b.Execute(ctx, runtime.RejectPatchCmd{PatchID: patchID, Reason: reason})
}

// Cancel cancels the currently in-flight operation.
func (b *Bridge) Cancel(ctx context.Context, reason string) error {
	return b.Execute(ctx, runtime.CancelCmd{Reason: reason})
}

// Execute routes an arbitrary RuntimeCommand through the Runtime facade. It is
// the generic entry point used by views that already hold a command instance
// (e.g. from CommandFromKey/CommandFromInput).
func (b *Bridge) Execute(ctx context.Context, cmd runtime.RuntimeCommand) error {
	if b == nil || b.rt == nil {
		return nil
	}
	return b.rt.Execute(ctx, cmd)
}

// CommandFromInput translates a submitted input line into the corresponding
// RuntimeCommand. Mode-switch lines (/ask, /plan, /mode build, ...) become a
// SwitchModeCmd; any other non-empty line becomes a SubmitPromptCmd. It
// returns ok=false for blank lines.
func CommandFromInput(line string, mode string) (runtime.RuntimeCommand, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, false
	}
	if target, ok := modeFromLine(line); ok {
		return runtime.SwitchModeCmd{Mode: target}, true
	}
	return runtime.SubmitPromptCmd{Prompt: line, Mode: mode}, true
}

// KeyContext describes the interactive state needed to translate a key press.
type KeyContext struct {
	// Mode is the active workflow mode ("ask", "plan", ...).
	Mode string
	// Busy is true when a stream/agent/approval is in flight.
	Busy bool
	// PatchID is the pending patch awaiting approval, if any.
	PatchID string
}

// CommandFromKey translates a key press into a canonical RuntimeCommand when
// it represents one of the five user interactions. Keys that do not map to a
// command return ok=false so the view can keep handling them locally.
//
// Mapping:
//   - With a pending approval, Enter/Alt+A approve the patch and Alt+R/Esc
//     reject it.
//   - While busy, Esc and Ctrl+C cancel the in-flight operation.
func CommandFromKey(msg tea.KeyMsg, ctx KeyContext) (runtime.RuntimeCommand, bool) {
	if ctx.PatchID != "" {
		switch {
		case msg.Type == tea.KeyEnter || msg.String() == "alt+a":
			return runtime.ApprovePatchCmd{PatchID: ctx.PatchID}, true
		case msg.Type == tea.KeyEscape || msg.String() == "alt+r":
			return runtime.RejectPatchCmd{PatchID: ctx.PatchID}, true
		}
		return nil, false
	}
	if ctx.Busy && (msg.Type == tea.KeyEscape || msg.Type == tea.KeyCtrlC) {
		return runtime.CancelCmd{Reason: "user interrupt"}, true
	}
	return nil, false
}

// modeFromLine extracts the target workflow mode from a mode-switch input line
// (e.g. "/plan", "/mode build"). It reports ok=false when the line is not a
// mode switch.
func modeFromLine(line string) (string, bool) {
	lower := strings.ToLower(line)
	for _, m := range []string{"ask", "investigate", "plan", "build", "review"} {
		if lower == "/"+m {
			return m, true
		}
	}
	if strings.HasPrefix(lower, "/mode ") {
		rest := strings.TrimSpace(lower[len("/mode "):])
		for _, m := range []string{"ask", "investigate", "plan", "build", "review"} {
			if rest == m {
				return m, true
			}
		}
	}
	return "", false
}

// Sender accepts a tea.Msg from any goroutine. *tea.Program satisfies it via
// Send, which is safe for concurrent use.
type Sender interface {
	Send(tea.Msg)
}

// EventSink bridges the Runtime's PresentationEvent stream into a Bubble Tea
// program. It subscribes to the Runtime and forwards every translated event as
// a tea.Msg on the UI goroutine, so the view updates strictly from
// PresentationEvent payloads with zero races.
type EventSink struct {
	sender Sender
	toMsg  func(runtime.PresentationEvent) tea.Msg
	cancel func()
}

// NewEventSink subscribes sender to every PresentationEvent emitted by rt and
// forwards each as toMsg(ev). Close stops delivery.
func NewEventSink(sender Sender, rt *runtime.Runtime, toMsg func(runtime.PresentationEvent) tea.Msg) *EventSink {
	s := &EventSink{sender: sender, toMsg: toMsg}
	if sender == nil || rt == nil {
		s.cancel = func() {}
		return s
	}
	s.cancel = rt.SubscribePresentation(func(ev runtime.PresentationEvent) {
		if s.toMsg == nil {
			return
		}
		s.sender.Send(s.toMsg(ev))
	})
	return s
}

// Close stops presentation event delivery. Idempotent.
func (s *EventSink) Close() {
	if s != nil && s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
}
