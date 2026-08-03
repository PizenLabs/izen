// Package runtime is the Application Layer of the Izen architecture (RFC v1.0).
// It owns the single entry point into the system: the Runtime facade routes
// every incoming user interaction, expressed as a strongly-typed
// RuntimeCommand, to a registered CommandHandler.
//
// Dependencies flow inward: runtime coordinates commands and dispatches them
// to handlers. It never imports the presentation layer nor any concrete
// infrastructure.
package runtime

// CommandType is the discriminator the CommandDispatcher uses to route a
// RuntimeCommand to its registered CommandHandler.
type CommandType string

// Canonical command types handled by the runtime.
const (
	CommandSubmitPrompt CommandType = "submit_prompt"
	CommandSwitchMode   CommandType = "switch_mode"
	CommandApprovePatch CommandType = "approve_patch"
	CommandRejectPatch  CommandType = "reject_patch"
	CommandCancel       CommandType = "cancel"
)

// String returns the canonical command type discriminator.
func (t CommandType) String() string { return string(t) }

// RuntimeCommand is a strongly-typed user interaction expressed as data.
// Commands are immutable: handlers must treat their payloads as read-only.
type RuntimeCommand interface {
	// Type returns the command type discriminator used for routing.
	Type() CommandType
}

// modeCarrier is implemented by commands that carry an explicit workflow
// mode/phase so the runtime can surface it in lifecycle telemetry.
type modeCarrier interface {
	TargetMode() string
}

// SubmitPromptCmd submits a user prompt for the workflow to process. Mode is
// the workflow phase the prompt should be handled in (ask, investigate, plan,
// build, review); it may be empty when the caller leaves routing to the mode
// resolver.
type SubmitPromptCmd struct {
	Prompt string
	Mode   string
}

// Type returns CommandSubmitPrompt.
func (c SubmitPromptCmd) Type() CommandType { return CommandSubmitPrompt }

// TargetMode returns the target workflow mode, if any.
func (c SubmitPromptCmd) TargetMode() string { return c.Mode }

// SwitchModeCmd requests a workflow phase/mode transition.
type SwitchModeCmd struct {
	Mode string
}

// Type returns CommandSwitchMode.
func (c SwitchModeCmd) Type() CommandType { return CommandSwitchMode }

// TargetMode returns the target workflow mode.
func (c SwitchModeCmd) TargetMode() string { return c.Mode }

// ApprovePatchCmd approves a pending patch (e.g. a Tier 4 human-in-the-loop
// approval request).
type ApprovePatchCmd struct {
	PatchID string
}

// Type returns CommandApprovePatch.
func (c ApprovePatchCmd) Type() CommandType { return CommandApprovePatch }

// RejectPatchCmd rejects a pending patch, optionally with a reason.
type RejectPatchCmd struct {
	PatchID string
	Reason  string
}

// Type returns CommandRejectPatch.
func (c RejectPatchCmd) Type() CommandType { return CommandRejectPatch }

// CancelCmd cancels the currently in-flight operation.
type CancelCmd struct {
	Reason string
}

// Type returns CommandCancel.
func (c CancelCmd) Type() CommandType { return CommandCancel }
