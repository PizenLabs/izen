package ui

import (
	"fmt"
	"strings"

	"github.com/PizenLabs/izen/internal/core/artifact"
	"github.com/PizenLabs/izen/internal/core/budget"
	"github.com/PizenLabs/izen/internal/core/capability"
)

// Style aliases — defined in styles.go as exported variables.

// approvalPromptData carries the structured information needed to render
// a high-visibility approval prompt.
//
//nolint:unused // Ready for approval flow wiring; not yet in call path.
type approvalPromptData struct {
	Title         string
	TargetFiles   []string
	Capabilities  []capability.Capability
	BudgetDelta   budget.BudgetDelta
	LifecycleFrom artifact.LifecycleState
	LifecycleTo   artifact.LifecycleState
	Description   string
}

// renderApprovalPrompt renders a double-bordered, high-visibility approval
// prompt showing target files, required capability elevations, and budget
// impact. The prompt blocks until the user explicitly approves or rejects.
//
//nolint:unused // Ready for approval flow wiring; not yet in call path.
func renderApprovalPrompt(data approvalPromptData, width int) string {
	if width < 50 {
		width = 50
	}
	innerW := width - 4

	var b strings.Builder
	b.WriteString(ApprovalTitle.Render("▲ APPROVAL REQUIRED"))
	b.WriteString("\n\n")

	// Description
	if data.Description != "" {
		b.WriteString(ApprovalInfo.Render(data.Description))
		b.WriteString("\n\n")
	}

	// Target files
	if len(data.TargetFiles) > 0 {
		b.WriteString(ApprovalLabel.Render("Target Files:"))
		b.WriteString("\n")
		for _, f := range data.TargetFiles {
			b.WriteString("  " + ApprovalFile.Render("▸ "+f))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Lifecycle transition
	if data.LifecycleFrom != "" || data.LifecycleTo != "" {
		b.WriteString(ApprovalLabel.Render("Lifecycle:"))
		b.WriteString(" ")
		b.WriteString(ApprovalValue.Render(string(data.LifecycleFrom)))
		b.WriteString(ApprovalValue.Render(" → "))
		b.WriteString(ApprovalValue.Render(string(data.LifecycleTo)))
		b.WriteString("\n\n")
	}

	// Required capabilities
	if len(data.Capabilities) > 0 {
		b.WriteString(ApprovalLabel.Render("Required Capabilities:"))
		b.WriteString("\n")
		for _, capa := range data.Capabilities {
			b.WriteString("  " + ApprovalCap.Render("● "+capa.String()))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Budget impact
	hasBudget := data.BudgetDelta.Files > 0 || data.BudgetDelta.DiffLines > 0 ||
		data.BudgetDelta.Tokens > 0 || data.BudgetDelta.Attempts > 0
	if hasBudget {
		b.WriteString(ApprovalLabel.Render("Budget Impact:"))
		b.WriteString("\n")
		if data.BudgetDelta.Files > 0 {
			fmt.Fprintf(&b, "  %s %d file(s)\n", ApprovalBudget.Render("Files:"), data.BudgetDelta.Files)
		}
		if data.BudgetDelta.DiffLines > 0 {
			fmt.Fprintf(&b, "  %s %d line(s)\n", ApprovalBudget.Render("Diffs:"), data.BudgetDelta.DiffLines)
		}
		if data.BudgetDelta.Tokens > 0 {
			fmt.Fprintf(&b, "  %s %d token(s)\n", ApprovalBudget.Render("Tokens:"), data.BudgetDelta.Tokens)
		}
		if data.BudgetDelta.Attempts > 0 {
			fmt.Fprintf(&b, "  %s %d attempt(s)\n", ApprovalBudget.Render("Attempts:"), data.BudgetDelta.Attempts)
		}
		b.WriteString("\n")
	}

	// Key bindings
	sep := strings.Repeat("─", innerW-6)
	b.WriteString(" " + sep + "\n")
	fmt.Fprintf(&b, "%s  %s",
		ApprovalKey.Render("Alt+A / Enter  Accept"),
		ApprovalKey.Render("Alt+R / Esc    Reject"),
	)

	return ApprovalBox.Width(width).Render(b.String())
}

// renderBuildApprovalPrompt renders a SHELL_EXEC approval prompt with task details.
//
//nolint:unused
func renderBuildApprovalPrompt(taskTarget, taskDesc string, width int) string {
	if width < 50 {
		width = 50
	}

	var b strings.Builder
	b.WriteString(ApprovalTitle.Render("▲ PERMISSION REQUIRED"))
	b.WriteString("\n\n")
	b.WriteString(ApprovalLabel.Render("Action:") + " " + ApprovalValue.Render("SHELL_EXEC"))
	b.WriteString("\n\n")
	b.WriteString(ApprovalFile.Render(taskTarget))
	b.WriteString("\n")
	if taskDesc != "" {
		b.WriteString(ApprovalInfo.Render("Reason: " + taskDesc))
		b.WriteString("\n\n")
	}
	innerW := width - 4
	sep := strings.Repeat("─", innerW-6)
	b.WriteString(" " + sep + "\n")
	fmt.Fprintf(&b, "%s  %s  %s",
		ApprovalKey.Render("Alt+A / Enter  Allow Once"),
		ApprovalKey.Render("Alt+L  Allow Always"),
		ApprovalKey.Render("Alt+R / Esc  Reject"),
	)

	return ApprovalBox.Width(width).Render(b.String())
}
