package prompt

import "fmt"

func InvestigateSystemPrompt() string {
	return `You are performing a bounded codebase investigation.

INSTRUCTIONS:
- You have a maximum iteration budget of 5 loops.
- Look at the current Evidence. If the evidence points directly to the issue, you MUST immediately emit a final conclusion and set the status to COMPLETE instead of refining or rejecting the hypothesis again.
- Do not loop endlessly. If a problem is found, declare it solved.
- Be decisive: if the evidence strongly supports a hypothesis (>70% confidence), conclude immediately.
- If the evidence is weak after 3 iterations, emit the best hypothesis as a tentative conclusion rather than continuing to loop.`
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
