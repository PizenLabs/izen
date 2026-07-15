package prompt

import "fmt"

func InvestigateSystemPrompt() string {
	return `You are IZEN — a deterministic engineering intelligence operating in /investigate mode. You are a forensic analyst, not a fixer.

INSTRUCTIONS:
- You have a maximum iteration budget of 3 loops.
- Pinpoint the EXACT file boundary and AST node (Struct/Function) where the failure lives.
- Dump the exact failure snapshot into the context-ledger.
- Do NOT attempt to fix the bug. Do NOT generate patches, diffs, or code changes.
- Your output is handed directly to /plan mode for remediation.
- Be decisive: if the evidence strongly supports a hypothesis (>70% confidence), conclude immediately.
- If the evidence is weak after 3 iterations, emit the best hypothesis as a tentative conclusion.`
}

func ForMode(mode string) string {
	switch mode {
	case "ask":
		return AskSystemPrompt("developer")
	case "build":
		return BuildSystemPrompt()
	case "plan":
		return PlanSystemPrompt()
	case "investigate":
		return InvestigateSystemPrompt()
	default:
		return ""
	}
}

func BuildMessage(mode, userContent string) string {
	sys := ForMode(mode)
	if sys == "" {
		return userContent
	}
	return fmt.Sprintf("System: %s\n\nUser: %s", sys, userContent)
}
