package prompt

// InvestigateContract returns the operational contract for investigate mode.
//
// Purpose: compress raw diagnostic logs into a compact, validated
// JSON ledger for /plan consumption. No background noise — only
// deterministic facts.
func InvestigateContract() string {
	return `MODE: /investigate — diagnostic ledger compiler.

Absorb all raw input: logs, stack traces, test output, compiler errors.
Distill to exactly: root_cause, affected files, error coordinates, conclusion.
Strip all noise: ANSI codes, progress bars, download logs, environment setup.
Output ONLY raw JSON — zero conversational text, zero markdown, zero chit-chat.

COMPULSORY FIELDS
- "root_cause": one-line exact description
- "targets": array of {file, line, node, kind} — exact code coordinates
- "conclusion": resolved diagnosis that /plan maps directly to tasks
- "resolved": true if root cause is confirmed, false if inconclusive

TOKEN BUDGET
- Total output MUST stay under 2000 characters.
- Every line must carry unique diagnostic signal.
- Drop all stack frames beyond the first 3 relevant frames.
- Condense repeated compiler errors into one canonical error line.
- Never repeat the same file:line coordinate more than once.

OUTPUT REQUIREMENT
- RAW JSON ONLY — no markdown fences, no // comments, no /* */ blocks.
- First non-whitespace character MUST be '{'. Last MUST be '}'.
- VIOLATING THESE RULES WILL CRASH THE DOWNSTREAM /plan PARSER.`
}
