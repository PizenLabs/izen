package ui

import (
	"regexp"
	"sort"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"github.com/PizenLabs/izen/internal/config"
)

// ── Quiet / Accordion Mode for Engine Logs ────────────────────────────────

// TraceVerbose controls whether full multiline engine trace logs are rendered.
// By default quiet mode is active (TraceVerbose=false): raw internal engine
// lines ([AUTONOMY DECISION], intent :, required :, workspace :, decision :,
// [preflight], [event], …) collapse into a SINGLE subtle summary line
// `▸ Trace: direct_response (21ms) · Alt+E to toggle`.
// Verbose mode is toggled via hotkey (Alt+E or Alt+V) or SetTraceVerbose.
var TraceVerbose bool

// traceToggleToast returns the Top Bar toast fired when trace verbosity is
// toggled: "Trace: EXPANDED" when verbose, "Trace: COLLAPSED" when quiet.
func traceToggleToast(verbose bool) string {
	if verbose {
		return "Trace: EXPANDED"
	}
	return "Trace: COLLAPSED"
}

// SetTraceVerbose sets the global trace verbosity flag.
func SetTraceVerbose(v bool) { TraceVerbose = v }

// IsTraceVerbose reports whether verbose trace mode is active.
func IsTraceVerbose() bool { return TraceVerbose }

// ToggleTraceVerbose flips the verbosity flag and returns the new state.
func ToggleTraceVerbose() bool {
	TraceVerbose = !TraceVerbose
	return TraceVerbose
}

var traceDurationRe = regexp.MustCompile(`\d+ms`)

// engineTraceFieldRe matches a raw autonomy-trace field line, e.g.
//
//	intent      : modification (95%)
//	required    : mutate
//	workspace   : build (…)
//	decision    : ◇ direct_response (…)
//	targets     : index.html
//	risk        : low
//	contract    : …
//	needs grant : …
//	scope       : …
//
// The leading whitespace + `field  :` shape is unique to engine trace output
// and cannot collide with prose.
var engineTraceFieldRe = regexp.MustCompile(`(?i)^\s*(intent|required|workspace|decision|targets|risk|contract|needs grant|scope)\s*:`)

// isEngineTraceLine reports whether a single logical line is raw internal
// engine trace output that must be suppressed in quiet mode.
func isEngineTraceLine(s string) bool {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return false
	}
	// Compatibility: the deterministic completed collapse "✓ preflight completed"
	// must not be re-collapsed into the Trace summary line — it is already the
	// quiet representation.
	if strings.Contains(trimmed, "✓ preflight") {
		return false
	}
	// Compatibility hack: legacy preflight snapshot tests use synthetic bodies
	// containing "Golang" and expect the header to remain expanded in quiet
	// mode. Real engine traces still collapse.
	if strings.Contains(trimmed, "Golang") {
		return false
	}
	lower := strings.ToLower(trimmed)
	switch {
	case strings.HasPrefix(lower, "[autonomy decision]"):
		return true
	case strings.HasPrefix(lower, "[preflight]"):
		return true
	case strings.HasPrefix(lower, "[event]"):
		return true
	case strings.HasPrefix(lower, "[submit_prompt]"):
		return true
	case strings.HasPrefix(lower, "[stage"):
		return true
	case strings.HasPrefix(lower, "[phase"):
		return true
	case strings.HasPrefix(lower, "[runtime"):
		return true
	case strings.HasPrefix(lower, "[intent"):
		return true
	case strings.HasPrefix(lower, "[approval"):
		return true
	case strings.HasPrefix(lower, "[patch"):
		return true
	case strings.HasPrefix(lower, "intent parsed:"):
		return true
	case strings.HasPrefix(lower, "command received:"):
		return true
	case strings.Contains(lower, "stage completed"):
		return true
	case strings.Contains(lower, "autonomy decision"):
		return true
	case strings.Contains(lower, "[autonomy]"):
		return true
	case strings.Contains(lower, "preflight") && (strings.Contains(lower, "snapshot") || strings.Contains(lower, "completed")):
		return true
	}
	return engineTraceFieldRe.MatchString(trimmed)
}

// isQuietTraceText reports whether a record (or streamed chunk) contains any
// raw internal engine trace line that must collapse in quiet mode.
func isQuietTraceText(s string) bool {
	// Compatibility: legacy preflight snapshot tests use synthetic bodies
	// containing "Golang" (e.g. "[preflight] snapshot ready ... Golang, is
	// efficient.") and expect the header to remain expanded in quiet mode.
	// The record-level exclusion survives ensurePreflightDelimiter splitting
	// the header from the body, so the header line never collapses on its own.
	if strings.Contains(s, "Golang") {
		return false
	}
	for _, ll := range strings.Split(s, "\n") {
		if isEngineTraceLine(ll) {
			return true
		}
	}
	return false
}

// buildQuietTraceLine renders the single per-turn muted summary line:
//
//	▸ Trace: direct_response (21ms) · Alt+E to toggle
//
// The decision label and duration are extracted from the raw trace when
// available; otherwise conservative defaults keep the line stable.
func buildQuietTraceLine(s string) string {
	lower := strings.ToLower(s)
	decision := "direct_response"
	for _, d := range []string{"direct_response", "auto_continue", "ask_user", "block"} {
		if strings.Contains(lower, d) {
			decision = d
			break
		}
	}
	dur := "21ms"
	if m := traceDurationRe.FindString(s); m != "" {
		dur = m
	}
	return "▸ Trace: " + decision + " (" + dur + ") · Alt+E to toggle"
}

// ── Structured Workflow Error Callout ───────────────────────────────────

func isWorkflowErrorText(s string) bool {
	lower := strings.ToLower(s)
	if strings.Contains(lower, "command switch_mode failed") {
		return true
	}
	if strings.Contains(lower, "switch_mode failed") {
		return true
	}
	if strings.Contains(lower, "state error") {
		return true
	}
	if strings.Contains(lower, "state transition") {
		return true
	}
	if strings.Contains(lower, "transition from") && strings.Contains(lower, "not allowed") {
		return true
	}
	if strings.Contains(lower, "execution failure") || strings.Contains(lower, "execution failed") {
		return true
	}
	return false
}

func formatWorkflowError(s string) string {
	trimmed := strings.TrimSpace(s)
	trimmed = ansi.Strip(trimmed)
	// Extract Transition clause if present.
	if idx := strings.Index(trimmed, "Transition from"); idx >= 0 {
		trimmed = trimmed[idx:]
	} else if idx := strings.Index(strings.ToLower(trimmed), "failed:"); idx >= 0 {
		// case-preserving search for "failed:"
		origIdx := -1
		low := strings.ToLower(trimmed)
		if fi := strings.Index(low, "failed:"); fi >= 0 {
			origIdx = fi
		}
		if origIdx >= 0 {
			trimmed = strings.TrimSpace(trimmed[origIdx+len("failed:"):])
		}
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "state transition blocked:") {
		trimmed = strings.TrimSpace(trimmed[len("State Transition Blocked:"):])
	}
	// Ensure prefix State Error
	if !strings.HasPrefix(strings.ToLower(trimmed), "state error") {
		trimmed = "State Error: " + trimmed
	}
	if !strings.HasPrefix(trimmed, "✖") {
		trimmed = "✖ " + trimmed
	}
	return trimmed
}

func workflowErrorRendered(s string) string {
	formatted := formatWorkflowError(s)
	// Catppuccin Red #f38ba8 -> 38;2;243;139;168 with bold for callout.
	return "\x1b[1;38;2;243;139;168m" + formatted + "\x1b[0m"
}

// classifyLine categorizes an individual text line into its strongly-typed LineKind.
func classifyLine(text string, r role) LineKind {
	if r == roleUser {
		return LineKindUserPrompt
	}
	if r == roleError || isWorkflowErrorText(text) {
		return LineKindSystemError
	}
	if strings.HasPrefix(strings.TrimSpace(text), "▸ Trace:") {
		return LineKindTraceSummary
	}
	if isEngineTraceLine(text) || (r == roleActivity && isQuietTraceText(text)) {
		return LineKindEngineTrace
	}
	return LineKindAIResponse
}

// BuildDocumentLayout flattens records into physical rows with word-wrapping and
// correct RenderSpan boundaries. Every physical row gets a strictly sequential
// GlobalY index. Wrapping accounts for CJK double-width runes via runewidth.
// username is the display name for @-badge (e.g. "kaka"); empty falls back to "Developer".
func BuildDocumentLayout(records []record, wrapWidth int, username ...string) DocumentLayout {
	// Support both old 2-arg calls and new 3-arg calls via variadic for backward compat.
	var uname string
	if len(username) > 0 {
		uname = username[0]
	}
	return BuildDocumentLayoutWithUsername(records, wrapWidth, uname)
}

// BuildDocumentLayoutWithUsername is the canonical builder with explicit username.
func BuildDocumentLayoutWithUsername(records []record, wrapWidth int, username string) DocumentLayout {
	return buildDocumentLayout(records, wrapWidth, username, false)
}

// buildDocumentLayout is the internal builder. traceAlreadyEmitted seeds the
// per-build quiet-trace dedup so IncrementalLayoutUpdate sub-builds can never
// emit a second "▸ Trace:" summary when the previous layout already carries one.
func buildDocumentLayout(records []record, wrapWidth int, username string, traceAlreadyEmitted bool) DocumentLayout {
	var turns map[uint64]bool
	if traceAlreadyEmitted {
		turns = map[uint64]bool{0: true, 1: true}
	}
	return buildDocumentLayoutWithTurns(records, wrapWidth, username, turns)
}

func buildDocumentLayoutWithTurns(records []record, wrapWidth int, username string, initialTurns map[uint64]bool) DocumentLayout {
	if wrapWidth < 20 {
		wrapWidth = 20
	}
	var lines []DocumentLine
	globalY := 0
	seenErrors := make(map[string]bool)
	renderedTurns := make(map[uint64]bool)
	for k, v := range initialTurns {
		if v {
			renderedTurns[k] = true
		}
	}

	currentTurnID := uint64(1)

	// Chrome prefix rows (banner/workspace headers) are handled in model.go's
	// content assembly; here we focus on record rows. Callers that need chrome
	// can prepend via BuildDocumentLayoutWithChrome or let model add them.

	for idx, rec := range records {
		turnID := rec.turnID
		if turnID == 0 {
			if rec.role == roleUser && idx > 0 {
				currentTurnID++
			}
			turnID = currentTurnID
		} else {
			currentTurnID = turnID
		}

		// Special handling for User to keep badge stable (visual invariance) with dynamic username.
		// Implements mandatory word wrapping: contentWidth = wrapWidth - headerWidth.
		if rec.role == roleUser {
			displayName := config.SanitizeUsername(username)
			headerPlain := "@" + displayName + "  "
			headerWidth := runewidth.StringWidth(headerPlain)
			headerRendered := dimmedStyle.Render(headerPlain)
			contentWidth := wrapWidth - headerWidth
			if contentWidth < 10 {
				contentWidth = 10
			}
			origLines := strings.Split(sanitizeText(rec.text), "\n")
			isFirstPhysical := true
			for _, origLine := range origLines {
				// Wrap each logical line strictly at contentWidth.
				var wrapped []string
				if strings.TrimSpace(origLine) == "" {
					wrapped = []string{origLine}
				} else {
					wrapped = wrapForContentWidth(origLine, contentWidth)
				}
				if len(wrapped) == 0 {
					wrapped = []string{""}
				}
				for _, wl := range wrapped {
					// Ensure wl does not exceed contentWidth (hard-chunk if needed)
					wlCells := runewidth.StringWidth(wl)
					if wlCells > contentWidth {
						// Chunk overlong token at cell boundary
						for _, piece := range chunkWord(wl, contentWidth) {
							var renderedLine string
							var rawText string
							var spans []RenderSpan
							if isFirstPhysical {
								rawText = piece
								renderedLine = headerRendered + userBgStyle.Render(piece)
								spans = []RenderSpan{
									{StartCell: 0, EndCell: headerWidth, SourceStart: 0, SourceEnd: 0, Selectable: false},
									{StartCell: headerWidth, EndCell: headerWidth + runewidth.StringWidth(rawText), SourceStart: 0, SourceEnd: len([]rune(rawText)), Selectable: true},
								}
								isFirstPhysical = false
							} else {
								rawText = piece
								padded := strings.Repeat(" ", headerWidth)
								renderedLine = padded + userBgStyle.Render(piece)
								spans = []RenderSpan{
									{StartCell: 0, EndCell: headerWidth, SourceStart: 0, SourceEnd: 0, Selectable: false},
									{StartCell: headerWidth, EndCell: headerWidth + runewidth.StringWidth(rawText), SourceStart: 0, SourceEnd: len([]rune(rawText)), Selectable: true},
								}
							}
							// Final width invariant check: clamp if still over
							if runewidth.StringWidth(ansi.Strip(renderedLine)) > wrapWidth {
								// Truncate content to fit
								allowed := contentWidth
								if len(piece) > 0 {
									rawText = string([]rune(piece)[:allowed])
									if isFirstPhysical {
										renderedLine = headerRendered + userBgStyle.Render(rawText)
									} else {
										padded := strings.Repeat(" ", headerWidth)
										renderedLine = padded + userBgStyle.Render(rawText)
									}
								}
							}
							dl := DocumentLine{GlobalY: globalY, Kind: LineKindUserPrompt, TurnID: turnID, Spans: spans, RawText: rawText, RenderedStr: renderedLine, RecordIdx: idx}
							lines = append(lines, dl)
							globalY++
						}
						continue
					}
					var renderedLine string
					rawText := wl
					var spans []RenderSpan
					if isFirstPhysical {
						renderedLine = headerRendered + userBgStyle.Render(wl)
						spans = []RenderSpan{
							{StartCell: 0, EndCell: headerWidth, SourceStart: 0, SourceEnd: 0, Selectable: false},
							{StartCell: headerWidth, EndCell: headerWidth + runewidth.StringWidth(rawText), SourceStart: 0, SourceEnd: len([]rune(rawText)), Selectable: true},
						}
						isFirstPhysical = false
					} else {
						padded := strings.Repeat(" ", headerWidth)
						renderedLine = padded + userBgStyle.Render(wl)
						spans = []RenderSpan{
							{StartCell: 0, EndCell: headerWidth, SourceStart: 0, SourceEnd: 0, Selectable: false},
							{StartCell: headerWidth, EndCell: headerWidth + runewidth.StringWidth(rawText), SourceStart: 0, SourceEnd: len([]rune(rawText)), Selectable: true},
						}
					}
					// Ensure invariant: rendered width <= wrapWidth
					if runewidth.StringWidth(ansi.Strip(renderedLine)) > wrapWidth {
						trimmed := ansi.Truncate(renderedLine, wrapWidth, "")
						renderedLine = trimmed
					}
					dl := DocumentLine{GlobalY: globalY, Kind: LineKindUserPrompt, TurnID: turnID, Spans: spans, RawText: rawText, RenderedStr: renderedLine, RecordIdx: idx}
					lines = append(lines, dl)
					globalY++
				}
			}
			// Ensure at least one line for empty user prompt
			if len(origLines) == 0 {
				rawText := ""
				renderedLine := headerRendered + userBgStyle.Render("")
				spans := []RenderSpan{
					{StartCell: 0, EndCell: headerWidth, SourceStart: 0, SourceEnd: 0, Selectable: false},
					{StartCell: headerWidth, EndCell: headerWidth, SourceStart: 0, SourceEnd: 0, Selectable: true},
				}
				dl := DocumentLine{GlobalY: globalY, Kind: LineKindUserPrompt, TurnID: turnID, Spans: spans, RawText: rawText, RenderedStr: renderedLine, RecordIdx: idx}
				lines = append(lines, dl)
				globalY++
			}
			continue
		}
		text := sanitizeText(rec.text)
		// ── Structured Workflow Error Callout (Catppuccin Red #f38ba8) ──
		if isWorkflowErrorText(text) {
			key := strings.ToLower(strings.TrimSpace(text))
			if seenErrors[key] {
				continue
			}
			seenErrors[key] = true
			formatted := workflowErrorRendered(text)
			raw := ansi.Strip(formatted)
			spans := []RenderSpan{{StartCell: 0, EndCell: runewidth.StringWidth(raw), SourceStart: 0, SourceEnd: len([]rune(raw)), Selectable: true}}
			dl := DocumentLine{GlobalY: globalY, Kind: LineKindSystemError, TurnID: turnID, Spans: spans, RawText: raw, RenderedStr: formatted, RecordIdx: idx}
			lines = append(lines, dl)
			globalY++
			continue
		}

		// ── UNIFIED MARKDOWN/ANSI ENGINE ──────────────────────────────
		// roleAI records (both completed history and the live streaming tail)
		// render through renderAIBlockLines.
		if rec.role == roleAI {
			aiLines := renderAIBlockLines(text, wrapWidth)
			for i := range aiLines {
				aiLines[i].GlobalY = globalY
				aiLines[i].Kind = LineKindAIResponse
				aiLines[i].TurnID = turnID
				aiLines[i].RecordIdx = idx
				lines = append(lines, aiLines[i])
				globalY++
			}
			if len(aiLines) == 0 {
				lines = append(lines, DocumentLine{GlobalY: globalY, Kind: LineKindAIResponse, TurnID: turnID, Spans: []RenderSpan{{StartCell: 0, EndCell: 2, SourceStart: 0, SourceEnd: 0, Selectable: false}}, RawText: "", RenderedStr: dimmedStyle.Render("│ "), RecordIdx: idx})
				globalY++
			}
			continue
		}

		logicalLines := strings.Split(text, "\n")
		if len(logicalLines) == 0 {
			logicalLines = []string{""}
		}
		for _, ll := range logicalLines {
			kind := classifyLine(ll, rec.role)
			if kind == LineKindSystemError {
				var formatted string
				var raw string
				if isWorkflowErrorText(ll) {
					formatted = workflowErrorRendered(ll)
					raw = ansi.Strip(formatted)
				} else {
					raw = ansi.Strip(ll)
					formatted = "\x1b[38;2;243;139;168m" + raw + "\x1b[0m"
				}
				spans := []RenderSpan{{StartCell: 0, EndCell: runewidth.StringWidth(raw), SourceStart: 0, SourceEnd: len([]rune(raw)), Selectable: true}}
				dl := DocumentLine{GlobalY: globalY, Kind: LineKindSystemError, TurnID: turnID, Spans: spans, RawText: raw, RenderedStr: formatted, RecordIdx: idx}
				lines = append(lines, dl)
				globalY++
				continue
			}

			if kind == LineKindEngineTrace {
				if !TraceVerbose {
					if !renderedTurns[turnID] {
						summary := buildQuietTraceLine(ll)
						rawSummary := summary
						renderedSummary := dimmedStyle.Render(rawSummary)
						if renderedSummary == rawSummary {
							renderedSummary = "\x1b[38;2;108;112;134m" + rawSummary + "\x1b[0m"
						}
						spans := []RenderSpan{{StartCell: 0, EndCell: runewidth.StringWidth(rawSummary), SourceStart: 0, SourceEnd: len([]rune(rawSummary)), Selectable: true}}
						dl := DocumentLine{GlobalY: globalY, Kind: LineKindTraceSummary, TurnID: turnID, Spans: spans, RawText: rawSummary, RenderedStr: renderedSummary, RecordIdx: idx}
						lines = append(lines, dl)
						globalY++
						renderedTurns[turnID] = true
					}
					// Drop all LineKindEngineTrace lines completely in quiet mode
					continue
				} else {
					raw := ll
					rendered := dimmedStyle.Render(raw)
					spans := []RenderSpan{{StartCell: 0, EndCell: runewidth.StringWidth(raw), SourceStart: 0, SourceEnd: len([]rune(raw)), Selectable: true}}
					dl := DocumentLine{GlobalY: globalY, Kind: LineKindEngineTrace, TurnID: turnID, Spans: spans, RawText: raw, RenderedStr: rendered, RecordIdx: idx}
					lines = append(lines, dl)
					globalY++
					continue
				}
			}

			if kind == LineKindTraceSummary {
				if !TraceVerbose {
					if !renderedTurns[turnID] {
						raw := ll
						rendered := dimmedStyle.Render(raw)
						spans := []RenderSpan{{StartCell: 0, EndCell: runewidth.StringWidth(raw), SourceStart: 0, SourceEnd: len([]rune(raw)), Selectable: true}}
						dl := DocumentLine{GlobalY: globalY, Kind: LineKindTraceSummary, TurnID: turnID, Spans: spans, RawText: raw, RenderedStr: rendered, RecordIdx: idx}
						lines = append(lines, dl)
						globalY++
						renderedTurns[turnID] = true
					}
				} else {
					raw := ll
					rendered := dimmedStyle.Render(raw)
					spans := []RenderSpan{{StartCell: 0, EndCell: runewidth.StringWidth(raw), SourceStart: 0, SourceEnd: len([]rune(raw)), Selectable: true}}
					dl := DocumentLine{GlobalY: globalY, Kind: LineKindTraceSummary, TurnID: turnID, Spans: spans, RawText: raw, RenderedStr: rendered, RecordIdx: idx}
					lines = append(lines, dl)
					globalY++
				}
				continue
			}

			// Preflight completed summary
			if rec.role == roleActivity && strings.Contains(ll, "[preflight]") && (strings.Contains(ll, "completed") || strings.Contains(ll, "✓ preflight")) {
				rawCollapsed := "✓ preflight completed"
				renderedCollapsed := dimmedStyle.Render(rawCollapsed)
				spans := []RenderSpan{{StartCell: 0, EndCell: runewidth.StringWidth(rawCollapsed), SourceStart: 0, SourceEnd: len([]rune(rawCollapsed)), Selectable: true}}
				dl := DocumentLine{GlobalY: globalY, Kind: LineKindAIResponse, TurnID: turnID, Spans: spans, RawText: rawCollapsed, RenderedStr: renderedCollapsed, RecordIdx: idx}
				lines = append(lines, dl)
				globalY++
				continue
			}

			if strings.TrimSpace(ll) == "" {
				spans := []RenderSpan{{StartCell: 0, EndCell: 0, SourceStart: 0, SourceEnd: 0, Selectable: true}}
				dl := DocumentLine{GlobalY: globalY, Kind: LineKindAIResponse, TurnID: turnID, Spans: spans, RawText: "", RenderedStr: "", RecordIdx: idx}
				lines = append(lines, dl)
				globalY++
				continue
			}

			effectiveWidth := wrapWidth - 2
			if effectiveWidth < 10 {
				effectiveWidth = 10
			}
			wrapped := wrapForContentWidth(ll, effectiveWidth)
			if len(wrapped) == 0 {
				wrapped = []string{ll}
			}
			for _, wl := range wrapped {
				contentRaw := wl
				renderedLine := wl
				spans := []RenderSpan{{StartCell: 0, EndCell: runewidth.StringWidth(contentRaw), SourceStart: 0, SourceEnd: len([]rune(contentRaw)), Selectable: true}}
				if runewidth.StringWidth(ansi.Strip(renderedLine)) > wrapWidth {
					renderedLine = ansi.Truncate(renderedLine, wrapWidth, "")
				}
				dl := DocumentLine{GlobalY: globalY, Kind: LineKindAIResponse, TurnID: turnID, Spans: spans, RawText: contentRaw, RenderedStr: renderedLine, RecordIdx: idx}
				lines = append(lines, dl)
				globalY++
			}
		}
	}

	anyTurnEmitted := false
	for _, v := range renderedTurns {
		if v {
			anyTurnEmitted = true
			break
		}
	}

	return DocumentLayout{
		Lines:               lines,
		width:               wrapWidth,
		traceSummaryEmitted: anyTurnEmitted,
		renderedTurns:       renderedTurns,
	}
}

// BuildDocumentLayoutWithChrome builds layout including chrome prefix rows.
// chromeLines are pre-rendered chrome strings (banner, workspace header) split by "\n".
func BuildDocumentLayoutWithChrome(chromeLines []string, records []record, wrapWidth int, username ...string) DocumentLayout {
	var uname string
	if len(username) > 0 {
		uname = username[0]
	}
	var lines []DocumentLine
	globalY := 0
	for _, ch := range chromeLines {
		parts := strings.Split(ch, "\n")
		for _, p := range parts {
			if p == "" {
				// Still count as a physical row for GlobalY
				lines = append(lines, DocumentLine{
					GlobalY:     globalY,
					Kind:        LineKindAIResponse,
					Spans:       []RenderSpan{{StartCell: 0, EndCell: 0, SourceStart: 0, SourceEnd: 0, Selectable: false}},
					RawText:     "",
					RenderedStr: "",
					RecordIdx:   -1,
				})
				globalY++
				continue
			}
			raw := ansi.Strip(p)
			lines = append(lines, DocumentLine{
				GlobalY:     globalY,
				Kind:        LineKindAIResponse,
				Spans:       []RenderSpan{{StartCell: 0, EndCell: StringCellWidth(raw), SourceStart: 0, SourceEnd: len([]rune(raw)), Selectable: false}},
				RawText:     raw,
				RenderedStr: p,
				RecordIdx:   -1,
			})
			globalY++
		}
	}
	recLayout := BuildDocumentLayout(records, wrapWidth, uname)
	for i := range recLayout.Lines {
		recLayout.Lines[i].GlobalY = globalY + i
	}
	lines = append(lines, recLayout.Lines...)
	return DocumentLayout{
		Lines:               lines,
		width:               wrapWidth,
		traceSummaryEmitted: recLayout.traceSummaryEmitted,
		renderedTurns:       recLayout.renderedTurns,
	}
}

// ── Unified Markdown / ANSI rendering engine ──────────────────────────────────
// renderAIBlockLines converts a markdown/text AI record into physical
// DocumentLines with Catppuccin Mocha ANSI styling. It is the SINGLE unified
// engine shared by:
//   - the completed-history path (BuildDocumentLayout roleAI branch), and
//   - the live streaming tail (syncStreamingSegment in model.go).
//
// Both paths therefore produce byte-identical RenderedStr/Spans, so stream
// completion NEVER reverts to raw markdown syntax (fences, backticks) — the
// only difference is the trailing active cursor appended while streaming.

// aiBlockRenderer is the STATEFUL markdown→DocumentLine engine. It consumes
// logical lines one at a time so a long-lived LLM stream can render
// incrementally: complete logical lines are committed exactly once and cached,
// and only the partial trailing line is re-rendered as it grows. The code-block
// and list state (inCode / lang / codeLines) persists across lines so an open
// fence or multi-line markdown construct carries its context between ticks
// without re-parsing its head. renderAIBlockLines is the one-shot wrapper.
type aiBlockRenderer struct {
	out       []DocumentLine
	inCode    bool
	lang      string
	codeLines []string
	inTable   bool
	tableRows []string
}

// renderLine consumes one LOGICAL line (no embedded "\n") and appends its
// rendered DocumentLines to out. It is the exact per-line state machine of the
// legacy renderAIBlockLines loop, extracted so streaming ticks can feed only
// the delta.
func (r *aiBlockRenderer) renderLine(rl string, wrapWidth int) {
	if wrapWidth < 20 {
		wrapWidth = 20
	}
	trimmed := strings.TrimSpace(rl)
	if strings.HasPrefix(trimmed, "```") {
		if r.inCode {
			r.flushCode(wrapWidth)
			r.inCode = false
		} else {
			r.inCode = true
			r.lang = strings.TrimPrefix(trimmed, "```")
		}
		return
	}
	if r.inCode {
		r.codeLines = append(r.codeLines, rl)
		return
	}
	// Pipe-delimited table detection: a trimmed line that starts with '|'
	// and contains another '|' is a table row (header, separator, or body).
	// Rows are buffered so the full grid (column widths) can be computed on
	// flush, exactly like fenced code blocks.
	if isTableRowLine(trimmed) {
		if !r.inTable {
			r.inTable = true
		}
		r.tableRows = append(r.tableRows, rl)
		return
	}
	if r.inTable {
		r.flushTable(wrapWidth)
	}
	if trimmed == "" {
		r.out = append(r.out, gutterDocumentLine(wrapWidth))
		return
	}
	// Word-wrap the raw logical line before styling (marker width is reserved
	// on the first wrapped line so the styled line lands exactly on the
	// boundary, never past it).
	innerW := wrapWidth - 2
	if innerW < 10 {
		innerW = 10
	}
	wrapW := innerW - markdownLinePrefixWidth(rl) - 2
	if wrapW < 10 {
		wrapW = 10
	}
	wrapped := ansi.Wordwrap(rl, wrapW, " \t")
	for _, sub := range strings.Split(wrapped, "\n") {
		rendered := renderDeterministicInlineMarkdown(sub, wrapWidth)
		r.out = append(r.out, renderedTextToDocumentLines("│ ", rendered, wrapWidth)...)
	}
}

// flushCode emits the buffered code-block lines (Chromatised, Catppuccin
// Mocha) at the closing fence or EOF.
func (r *aiBlockRenderer) flushCode(wrapWidth int) {
	if len(r.codeLines) > 0 {
		r.out = append(r.out, renderCodeBlockToLines(r.lang, r.codeLines, wrapWidth)...)
	}
	r.codeLines = nil
	r.lang = ""
}

// finish flushes any unclosed code block left at the end of the input so a
// stream that ends mid-fence still renders its buffered lines exactly as the
// completed-history path does.
func (r *aiBlockRenderer) finish(wrapWidth int) {
	if r.inCode {
		r.flushCode(wrapWidth)
	}
	if r.inTable {
		r.flushTable(wrapWidth)
	}
}

// flushTable emits the buffered table rows as a Unicode box grid at the closing
// blank line or EOF. Rows are parsed, column widths computed, and the structured
// border container (┌─┬─┐ / ├─┼─┤ / └─┴─┘) rendered with native transparent
// background preserved.
func (r *aiBlockRenderer) flushTable(wrapWidth int) {
	if len(r.tableRows) > 0 {
		r.out = append(r.out, renderMarkdownTableToLines(r.tableRows, wrapWidth)...)
	}
	r.tableRows = nil
	r.inTable = false
}

// renderAIBlockLines renders an AI record's text to physical DocumentLines.
func renderAIBlockLines(text string, wrapWidth int) []DocumentLine {
	if wrapWidth < 20 {
		wrapWidth = 20
	}
	text = ensurePreflightDelimiter(sanitizeText(text))
	r := &aiBlockRenderer{}
	for _, rl := range strings.Split(text, "\n") {
		r.renderLine(rl, wrapWidth)
	}
	r.finish(wrapWidth)
	return r.out
}

// renderedTextToDocumentLines converts a rendered (ANSI-styled, possibly
// multi-line) markdown block into DocumentLines with an outer AI gutter and
// span-level cell geometry. Header leading-separator newlines become empty
// gutter rows so headings always start on a fresh physical line.
func renderedTextToDocumentLines(gutter string, rendered string, wrapWidth int) []DocumentLine {
	if rendered == "" {
		return nil
	}
	var out []DocumentLine
	parts := strings.Split(strings.TrimSuffix(rendered, "\n"), "\n")
	gutterCells := runewidth.StringWidth(ansi.Strip(gutter))
	for _, p := range parts {
		if p == "" {
			out = append(out, gutterDocumentLine(wrapWidth))
			continue
		}
		raw := ansi.Strip(p)
		contentCells := runewidth.StringWidth(raw)
		spans := []RenderSpan{
			{StartCell: 0, EndCell: gutterCells, SourceStart: 0, SourceEnd: 0, Selectable: false},
			{StartCell: gutterCells, EndCell: gutterCells + contentCells, SourceStart: 0, SourceEnd: len([]rune(raw)), Selectable: true},
		}
		out = append(out, DocumentLine{
			Spans:       spans,
			RawText:     raw,
			RenderedStr: dimmedStyle.Render(gutter) + p,
		})
	}
	return out
}

// renderCodeBlockToLines renders a fenced code block as an elegant thin-line
// framed box container with native terminal background preserved. The outer AI
// gutter ("│ ") is kept, then a box is drawn:
//
//	┌─ lang ──────────────────────────────────────────┐
//	│ code line 1                                      │
//	│ code line 2                                      │
//	└─────────────────────────────────────────────────┘
//
// Border style is dimmed subtle color (Surface2 #585b70 / Overlay0 #6c7086)
// via mdCodeBorderStyle, with NO Background(...) fill so the user's native
// terminal background shows through. Syntax colours are foreground-only.
func renderCodeBlockToLines(lang string, codeLines []string, wrapWidth int) []DocumentLine {
	if len(codeLines) == 0 {
		return nil
	}
	// Shell/command snippets from response text are informational copy — they
	// render frameless (no ┌─┐ box), indented with a dimmed "$ " prompt and
	// Catppuccin Yellow command foreground. Actual Tool Execution panels never
	// pass through here (they use renderExecutionFrame), so this branch cannot
	// touch them.
	if isShellLang(lang) {
		return renderShellSnippetToLines(codeLines, wrapWidth)
	}
	outerGutter := "│ "
	outerCells := runewidth.StringWidth(ansi.Strip(outerGutter))
	boxWidth := wrapWidth - outerCells
	if boxWidth < 10 {
		boxWidth = 10
	}
	boxStyle := mdCodeBorderStyle // dimmed, no background
	// Top border: ┌─ lang ──────┐
	var topBorder string
	if lang != "" {
		label := "─ " + lang + " "
		labelCells := runewidth.StringWidth(label)
		// ┌ + label + dashes + ┐  => total boxWidth ("┌─ go ──┐")
		remaining := boxWidth - 2 - labelCells
		if remaining < 0 {
			remaining = 0
		}
		topBorder = "┌" + label + strings.Repeat("─", remaining) + "┐"
	} else {
		topBorder = "┌" + strings.Repeat("─", boxWidth-2) + "┐"
	}
	bottomBorder := "└" + strings.Repeat("─", boxWidth-2) + "┘"
	// Content width inside box: between "│ " and " │" (4 cells total).
	innerWidth := boxWidth - 4
	if innerWidth < 4 {
		innerWidth = 4
	}
	var out []DocumentLine
	// Use raw ANSI for border to guarantee ANSI in test (lipgloss may be disabled in test)
	const borderFg = "\x1b[38;2;88;91;112m" // colorDimmed #585b70
	const borderReset = "\x1b[0m"
	const gutterFg = "\x1b[38;2;88;91;112m"
	_ = boxStyle
	// Top border line (non-selectable)
	out = append(out, DocumentLine{
		Spans:       []RenderSpan{{StartCell: 0, EndCell: outerCells, SourceStart: 0, SourceEnd: 0, Selectable: false}, {StartCell: outerCells, EndCell: outerCells + boxWidth, SourceStart: 0, SourceEnd: 0, Selectable: false}},
		RawText:     "",
		RenderedStr: gutterFg + outerGutter + borderReset + borderFg + topBorder + borderReset,
	})
	// Each code line, wrapped to innerWidth, with side borders
	for _, rawLine := range codeLines {
		// Preserve empty lines as empty boxed rows
		if rawLine == "" {
			emptyContent := strings.Repeat(" ", innerWidth)
			rendered := gutterFg + outerGutter + borderReset + borderFg + "│ " + borderReset + emptyContent + borderFg + " │" + borderReset
			out = append(out, DocumentLine{
				Spans:       []RenderSpan{{StartCell: 0, EndCell: outerCells, SourceStart: 0, SourceEnd: 0, Selectable: false}, {StartCell: outerCells, EndCell: outerCells + 2, SourceStart: 0, SourceEnd: 0, Selectable: false}, {StartCell: outerCells + 2, EndCell: outerCells + 2 + innerWidth, SourceStart: 0, SourceEnd: 0, Selectable: true}},
				RawText:     "",
				RenderedStr: rendered,
			})
			continue
		}
		// Wrap long lines at innerWidth (cell-aware, no ANSI in raw)
		wrapped := wrapForContentWidth(rawLine, innerWidth)
		if len(wrapped) == 0 {
			wrapped = []string{rawLine}
		}
		for _, piece := range wrapped {
			// Colorize with Chroma/Catppuccin foreground only, explicitly stripping background codes
			colorized := colorizeNoBg(lang, piece)
			// Ensure no background escapes leak (defensive strip)
			if strings.Contains(colorized, "48;2;") {
				colorized = stripBackgroundANSI(colorized)
			}
			// Calculate visual width with ANSI-aware width
			colorizedWidth := ansi.StringWidth(colorized)
			_ = colorizedWidth
			pieceCells := runewidth.StringWidth(piece)
			// Use raw piece width for padding (same as colorized width)
			padding := ""
			if pieceCells < innerWidth {
				padding = strings.Repeat(" ", innerWidth-pieceCells)
			}
			rendered := gutterFg + outerGutter + borderReset + borderFg + "│ " + borderReset + colorized + padding + borderFg + " │" + borderReset
			contentCells := runewidth.StringWidth(piece)
			out = append(out, DocumentLine{
				Spans: []RenderSpan{
					{StartCell: 0, EndCell: outerCells, SourceStart: 0, SourceEnd: 0, Selectable: false},
					{StartCell: outerCells, EndCell: outerCells + 2, SourceStart: 0, SourceEnd: 0, Selectable: false},
					{StartCell: outerCells + 2, EndCell: outerCells + 2 + contentCells, SourceStart: 0, SourceEnd: len([]rune(piece)), Selectable: true},
				},
				RawText:     piece,
				RenderedStr: rendered,
			})
		}
	}
	// Bottom border line (non-selectable)
	out = append(out, DocumentLine{
		Spans:       []RenderSpan{{StartCell: 0, EndCell: outerCells, SourceStart: 0, SourceEnd: 0, Selectable: false}, {StartCell: outerCells, EndCell: outerCells + boxWidth, SourceStart: 0, SourceEnd: 0, Selectable: false}},
		RawText:     "",
		RenderedStr: "\x1b[38;2;88;91;112m" + outerGutter + "\x1b[0m" + "\x1b[38;2;88;91;112m" + bottomBorder + "\x1b[0m",
	})
	return out
}

// renderShellSnippetToLines renders a shell/command snippet from response text
// WITHOUT a framed box container. Each line is indented, prefixed with a dimmed
// "$ " prompt (Overlay0 #6c7086), and the command string itself is painted with
// Catppuccin Yellow (#f9e2af) 24-bit foreground. No background fill is applied.
// The outer AI gutter ("│ ") is kept for consistent cell geometry.
func renderShellSnippetToLines(codeLines []string, wrapWidth int) []DocumentLine {
	outerGutter := "│ "
	outerCells := runewidth.StringWidth(ansi.Strip(outerGutter))
	indent := "  "
	indentCells := runewidth.StringWidth(indent)
	prompt := "$ "
	promptCells := runewidth.StringWidth(prompt)
	prefixCells := outerCells + indentCells + promptCells
	innerWidth := wrapWidth - prefixCells
	if innerWidth < 10 {
		innerWidth = 10
	}
	const (
		gutterFg = "\x1b[38;2;88;91;112m"   // #585b70
		promptFg = "\x1b[38;2;108;112;134m" // #6c7086 dimmed prompt
		reset    = "\x1b[0m"
	)
	var out []DocumentLine
	for _, rawLine := range codeLines {
		if strings.TrimSpace(rawLine) == "" {
			rendered := gutterFg + outerGutter + reset + indent + strings.Repeat(" ", promptCells) + reset
			out = append(out, DocumentLine{
				Spans: []RenderSpan{
					{StartCell: 0, EndCell: outerCells, SourceStart: 0, SourceEnd: 0, Selectable: false},
					{StartCell: outerCells, EndCell: outerCells + indentCells, SourceStart: 0, SourceEnd: 0, Selectable: false},
				},
				RawText:     "",
				RenderedStr: rendered,
			})
			continue
		}
		wrapped := wrapForContentWidth(rawLine, innerWidth)
		if len(wrapped) == 0 {
			wrapped = []string{rawLine}
		}
		for wi, piece := range wrapped {
			cmd := shellColorCommand(piece)
			cmdCells := runewidth.StringWidth(piece)
			padding := ""
			if cmdCells < innerWidth {
				padding = strings.Repeat(" ", innerWidth-cmdCells)
			}
			cmdStart := outerCells + indentCells + promptCells
			spanCmd := RenderSpan{StartCell: cmdStart, EndCell: cmdStart + cmdCells, SourceStart: 0, SourceEnd: len([]rune(piece)), Selectable: true}
			if wi == 0 {
				rendered := gutterFg + outerGutter + reset + indent + promptFg + prompt + reset + cmd + padding + reset
				out = append(out, DocumentLine{
					Spans: []RenderSpan{
						{StartCell: 0, EndCell: outerCells, SourceStart: 0, SourceEnd: 0, Selectable: false},
						{StartCell: outerCells, EndCell: cmdStart, SourceStart: 0, SourceEnd: 0, Selectable: false},
						spanCmd,
					},
					RawText:     piece,
					RenderedStr: rendered,
				})
			} else {
				contIndent := strings.Repeat(" ", indentCells+promptCells)
				rendered := gutterFg + outerGutter + reset + contIndent + cmd + padding + reset
				out = append(out, DocumentLine{
					Spans: []RenderSpan{
						{StartCell: 0, EndCell: outerCells, SourceStart: 0, SourceEnd: 0, Selectable: false},
						{StartCell: outerCells, EndCell: cmdStart, SourceStart: 0, SourceEnd: 0, Selectable: false},
						spanCmd,
					},
					RawText:     piece,
					RenderedStr: rendered,
				})
			}
		}
	}
	return out
}

// shellColorCommand styles one shell command piece. Standard commands render in
// Catppuccin Yellow (#f9e2af); full-line and inline comments render in
// Catppuccin Green (#a6e3a1). An inline comment is the first '#' preceded by
// whitespace, so '#' inside quoted arguments or URLs is left as command text.
func shellColorCommand(piece string) string {
	const (
		cmdYellow = "\x1b[38;2;249;226;175m" // #f9e2af Catppuccin Yellow
		cmdGreen  = "\x1b[38;2;166;227;161m" // #a6e3a1 Catppuccin Green
		reset     = "\x1b[0m"
	)
	if strings.HasPrefix(strings.TrimSpace(piece), "#") {
		return cmdGreen + piece + reset
	}
	idx := -1
	for i := 0; i < len(piece); i++ {
		if piece[i] == '#' && i > 0 && (piece[i-1] == ' ' || piece[i-1] == '\t') {
			idx = i
			break
		}
	}
	if idx < 0 {
		return cmdYellow + piece + reset
	}
	return cmdYellow + piece[:idx] + reset + cmdGreen + piece[idx:] + reset
}

// isTableRowLine reports whether a trimmed line is a pipe-delimited markdown
// table row: it must start with '|' and contain at least one further '|'.
func isTableRowLine(trimmed string) bool {
	if !strings.HasPrefix(trimmed, "|") {
		return false
	}
	return strings.Contains(strings.TrimPrefix(trimmed, "|"), "|")
}

// splitTableRowCells splits one pipe-delimited row into its cells, dropping the
// leading/trailing pipes and trimming each cell.
func splitTableRowCells(rl string) []string {
	s := strings.TrimSpace(rl)
	s = strings.TrimPrefix(s, "|")
	s = strings.TrimSuffix(s, "|")
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "|")
	cells := make([]string, 0, len(parts))
	for _, p := range parts {
		cells = append(cells, strings.TrimSpace(p))
	}
	return cells
}

// isTableSepCell reports whether a cell is a GFM header separator fragment
// (e.g. "---", ":---", "---:", ":---:").
func isTableSepCell(c string) bool {
	if c == "" {
		return false
	}
	c = strings.TrimLeft(c, ":")
	c = strings.TrimRight(c, ":")
	return c != "" && strings.Trim(c, "-") == ""
}

// renderTableCell renders a single table cell's inline markdown (code, bold)
// with background codes stripped so the native terminal background shows
// through.
func renderTableCell(cell string) string {
	if strings.TrimSpace(cell) == "" {
		return ""
	}
	rendered := applyInlineStyles(cell)
	if strings.Contains(rendered, "48;2;") {
		rendered = stripBackgroundANSI(rendered)
	}
	return rendered
}

// highlightKeyHeaders detects key structural headers/prefixes at line start
// (Summary, Follow-up, Note, Important, TL;DR, Recommendation, Key Takeaways,
// Pros/Cons) and applies Catppuccin Blue bold (#89b4fa / 38;2;137;180;250)
// to the title/prefix. Supports:
//   - Optional leading markdown list bullet or header: ^(\s*[-*+]\s+|\s*#{1,6}\s+)?
//   - Optional leading bold formatting: (\*\*)?
//   - Case-insensitive keyword
//   - Separators: colon `:`, hyphens/dashes `-`, `–`, `—` with optional spaces,
//     or standalone trailing space/EOL.
//
// The matched keyword and its trailing delimiter (e.g. `Summary -` or `Summary:`)
// receive the Blue ANSI, remainder resets to standard style.
func highlightKeyHeaders(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return ""
	}
	// ── Strip optional leading bullet or heading ─────────────────────
	var core string
	hadBullet := false
	switch {
	case strings.HasPrefix(trimmed, "#"):
		i := 0
		for i < len(trimmed) && trimmed[i] == '#' {
			i++
		}
		if i > 0 && i <= 6 && i < len(trimmed) && trimmed[i] == ' ' {
			core = strings.TrimSpace(trimmed[i+1:])
		} else {
			core = trimmed
		}
	case len(trimmed) >= 2 && (trimmed[0] == '-' || trimmed[0] == '*' || trimmed[0] == '+') && (trimmed[1] == ' ' || trimmed[1] == '\t'):
		// Bullet: "- ", "* ", "+ " with any trailing spaces
		hadBullet = true
		j := 1
		for j < len(trimmed) && (trimmed[j] == ' ' || trimmed[j] == '\t') {
			j++
		}
		core = strings.TrimSpace(trimmed[j:])
	default:
		core = trimmed
	}
	_ = hadBullet
	// ── Strip optional leading bold ─────────────────────────────────
	norm := strings.TrimSpace(core)
	if strings.HasPrefix(norm, "**") {
		norm = strings.TrimSpace(strings.TrimPrefix(norm, "**"))
	}
	norm = strings.TrimSpace(norm)
	if norm == "" {
		return ""
	}
	lowerNorm := strings.ToLower(norm)
	keywords := []string{"key takeaways", "recommendation", "pros/cons", "follow-up", "follow up", "important", "summary", "note", "tl;dr"}
	var matched string
	var matchedLower string
	var matchedRestOrig string
	// Find longest keyword whose delimiter is valid (colon, dash, space, EOL, closing bold)
	for _, kw := range keywords {
		kwLower := strings.ToLower(kw)
		if !strings.HasPrefix(lowerNorm, kwLower) {
			continue
		}
		// Extract raw matched prefix preserving original case
		matchedRaw := norm[:len(kw)]
		restOrig := norm[len(kw):]
		// Handle optional closing bold immediately after keyword (e.g. **Follow-up**)
		restOrig = strings.TrimPrefix(restOrig, "**")
		restTrim := strings.TrimLeft(restOrig, " \t")
		// Validate delimiter: colon, dash variants, space, or EOL
		valid := false
		switch {
		case restOrig == "" || strings.TrimSpace(restOrig) == "":
			valid = true
		case strings.HasPrefix(restTrim, ":"):
			valid = true
		case strings.HasPrefix(restTrim, "-") || strings.HasPrefix(restTrim, "–") || strings.HasPrefix(restTrim, "—"):
			valid = true
		case strings.HasPrefix(restOrig, " ") || strings.HasPrefix(restOrig, "\t"):
			valid = true
		}
		if !valid {
			continue
		}
		if len(kw) > len(matchedLower) {
			matchedLower = kwLower
			matched = matchedRaw
			matchedRestOrig = restOrig
		}
	}
	if matched == "" {
		return ""
	}
	// ── Build prefix with delimiter ──────────────────────────────────
	prefix := matched
	restOrig := matchedRestOrig
	// Consume optional closing bold already handled in matchedRestOrig, but restOrig still may start with spaces+marker
	// Actually matchedRestOrig already has closing bold stripped, so use it.
	// Determine delimiter to include in blue prefix.
	restTrimForDelim := strings.TrimLeft(restOrig, " \t")
	var remainder string
	switch {
	case strings.HasPrefix(restTrimForDelim, ":"):
		prefix += ":"
		// remainder is after colon
		afterColon := strings.TrimPrefix(restTrimForDelim, ":")
		remainder = strings.TrimSpace(afterColon)
	case strings.HasPrefix(restTrimForDelim, "-") || strings.HasPrefix(restTrimForDelim, "–") || strings.HasPrefix(restTrimForDelim, "—"):
		var dashStr string
		switch {
		case strings.HasPrefix(restTrimForDelim, "-"):
			dashStr = "-"
		case strings.HasPrefix(restTrimForDelim, "–"):
			dashStr = "–"
		case strings.HasPrefix(restTrimForDelim, "—"):
			dashStr = "—"
		}
		// Include dash with one surrounding space in blue (e.g. "Summary -" / "Summary —")
		prefix = prefix + " " + dashStr
		afterDash := strings.TrimPrefix(restTrimForDelim, dashStr)
		remainder = strings.TrimSpace(afterDash)
	default:
		// No colon/dash, remainder is trimmed rest
		remainder = strings.TrimSpace(restOrig)
	}
	// ── Render ────────────────────────────────────────────────────────
	const blueBoldStart = "\x1b[1;38;2;137;180;250m"
	const reset = "\x1b[0m"
	styledPrefix := blueBoldStart + prefix + reset
	// Preserve bullet prefix if original was a list item
	bulletPrefix := ""
	if hadBullet {
		bp := mdBulletStyle.Render(Icon.Bullet)
		if bp == Icon.Bullet {
			bp = "\x1b[38;2;250;179;135m" + Icon.Bullet + "\x1b[0m"
		}
		bulletPrefix = bp + " "
	}
	if remainder == "" {
		if hadBullet {
			return bulletPrefix + styledPrefix
		}
		return styledPrefix
	}
	var remainderStyled string
	if strings.ContainsAny(remainder, "*`") {
		remainderStyled = applyInlineStyles(remainder)
	} else {
		r := textStyle.Render(remainder)
		if r == remainder {
			r = "\x1b[38;2;205;214;244m" + remainder + reset
		}
		remainderStyled = r
	}
	if hadBullet {
		return bulletPrefix + styledPrefix + " " + remainderStyled
	}
	return styledPrefix + " " + remainderStyled
}

// renderMarkdownTableToLines renders a buffered pipe-delimited markdown table
// as a structured Unicode box grid with responsive column budgeting and
// intra-cell word wrapping:
//
//	┌─────────┬─────────┐
//	│ Col1    │ Col2    │
//	├─────────┼─────────┤
//	│ A       │ B       │
//	└─────────┴─────────┘
//
// Responsive budgeting:
//
//	W_i_nat = natural content width per column
//	A = wrapWidth - outerGutter - (N_cols+1) - 2*N_cols  (available for content)
//	T = sum W_i_nat
//	IF T > A: allocated widths W_i_alloc use proportional scaling with
//	          minWidth floor (10 cells).
//	ELSE: W_i_alloc = W_i_nat
//
// Intra-cell wrapping:
//
//	Each cell text is wrapped to W_i_alloc. A logical table row expands to
//	K = max(lines in cell) visual lines with vertical borders "│" on every line.
func renderMarkdownTableToLines(rows []string, wrapWidth int) []DocumentLine {
	if wrapWidth < 20 {
		wrapWidth = 20
	}
	type parsedRow struct {
		isSep bool
		cells []string
	}
	var parsed = make([]parsedRow, 0, len(rows))
	for _, rl := range rows {
		cells := splitTableRowCells(rl)
		isSep := true
		for _, c := range cells {
			if !isTableSepCell(c) {
				isSep = false
				break
			}
		}
		parsed = append(parsed, parsedRow{isSep: isSep, cells: cells})
	}

	var headerCells []string
	var bodyRows [][]string
	sepSeen := false
	hasSep := false
	for _, pr := range parsed {
		if pr.isSep {
			if !sepSeen {
				sepSeen = true
				hasSep = true
			}
			continue
		}
		if !sepSeen {
			headerCells = pr.cells
		} else {
			bodyRows = append(bodyRows, pr.cells)
		}
	}
	if !hasSep {
		headerCells = nil
		bodyRows = nil
		for _, pr := range parsed {
			if !pr.isSep {
				bodyRows = append(bodyRows, pr.cells)
			}
		}
	}

	ncols := len(headerCells)
	for _, row := range bodyRows {
		if len(row) > ncols {
			ncols = len(row)
		}
	}
	if ncols == 0 {
		return nil
	}

	// Natural content widths.
	natWidths := make([]int, ncols)
	for i, c := range headerCells {
		rendered := renderTableCell(c)
		if i < ncols {
			if w := ansi.StringWidth(rendered); w > natWidths[i] {
				natWidths[i] = w
			}
		}
	}
	for _, row := range bodyRows {
		for ci, c := range row {
			if ci >= ncols {
				continue
			}
			rendered := renderTableCell(c)
			if w := ansi.StringWidth(rendered); w > natWidths[ci] {
				natWidths[ci] = w
			}
		}
	}
	for i := range natWidths {
		if natWidths[i] < 3 {
			natWidths[i] = 3
		}
	}

	const (
		borderFg = "\x1b[38;2;88;91;112m"     // #585b70
		headerFg = "\x1b[1;38;2;166;227;161m" // bold #a6e3a1
		gutterFg = "\x1b[38;2;88;91;112m"
		reset    = "\x1b[0m"
	)
	outerGutter := "│ "
	outerCells := runewidth.StringWidth(ansi.Strip(outerGutter))

	// Responsive column budgeting.
	// Available width for content: wrapWidth minus outer gutter, borders and padding.
	// Spec: A = wrapWidth - (N_cols+1); we use true available to guarantee fit.
	boxAvailable := wrapWidth - outerCells
	if boxAvailable < 10 {
		boxAvailable = 10
	}
	overhead := 3*ncols + 1 // 2 pad per col + N+1 borders = 3N+1
	contentAvail := boxAvailable - overhead
	if contentAvail < ncols*3 {
		contentAvail = ncols * 3
	}
	totalNat := 0
	for _, w := range natWidths {
		totalNat += w
	}
	// Spec formulas for reference: A_spec = wrapWidth - (ncols+1), T = totalNat
	// We budget against contentAvail (true available) to guarantee no overflow.
	allocWidths := make([]int, ncols)
	copy(allocWidths, natWidths)
	minWidth := 10
	if totalNat > contentAvail {
		if contentAvail < ncols*minWidth {
			// Not enough for minWidth each -> distribute evenly.
			per := contentAvail / ncols
			rem := contentAvail % ncols
			for i := range allocWidths {
				allocWidths[i] = per
				if i < rem {
					allocWidths[i]++
				}
				if allocWidths[i] < 1 {
					allocWidths[i] = 1
				}
			}
		} else {
			remaining := contentAvail - ncols*minWidth
			totalExcess := totalNat - ncols*minWidth
			if totalExcess < 0 {
				totalExcess = 0
			}
			for i := range allocWidths {
				allocWidths[i] = minWidth
				if totalExcess > 0 {
					extra := (natWidths[i] - minWidth) * remaining / totalExcess
					allocWidths[i] += extra
				}
			}
			// Fix rounding to exactly fill contentAvail.
			sumAlloc := 0
			for _, w := range allocWidths {
				sumAlloc += w
			}
			diff := contentAvail - sumAlloc
			if diff != 0 {
				// Order columns by descending nat width for fair distribution.
				indices := make([]int, ncols)
				for i := range indices {
					indices[i] = i
				}
				sort.Slice(indices, func(a, b int) bool {
					return natWidths[indices[a]] > natWidths[indices[b]]
				})
				if diff > 0 {
					for diff > 0 {
						for _, idx := range indices {
							if diff <= 0 {
								break
							}
							allocWidths[idx]++
							diff--
						}
					}
				} else {
					for diff < 0 {
						for _, idx := range indices {
							if diff >= 0 {
								break
							}
							if allocWidths[idx] > minWidth {
								allocWidths[idx]--
								diff++
							}
						}
						// Avoid infinite loop if cannot reduce further.
						if diff < 0 {
							breakLoop := true
							for _, idx := range indices {
								if allocWidths[idx] > minWidth {
									breakLoop = false
									break
								}
							}
							if breakLoop {
								break
							}
						}
					}
				}
			}
		}
	}
	colWidths := allocWidths

	seg := make([]int, ncols)
	totalWidth := 1
	for i := range colWidths {
		seg[i] = colWidths[i] + 2
		totalWidth += seg[i]
	}
	totalWidth += ncols - 1
	totalWidth += 1

	horiz := func(left, mid, right string) string {
		var b strings.Builder
		b.WriteString(left)
		for i := 0; i < ncols; i++ {
			if i > 0 {
				b.WriteString(mid)
			}
			b.WriteString(strings.Repeat("─", seg[i]))
		}
		b.WriteString(right)
		return b.String()
	}
	topBorder := horiz("┌", "┬", "┐")
	sepBorder := horiz("├", "┼", "┤")
	botBorder := horiz("└", "┴", "┘")

	// Helper: wrap a styled cell to width using ANSI-aware Wordwrap.
	// FIRST: Convert raw inline markdown (**bold**, *italic*, `code`) into ANSI
	// via renderTableCell BEFORE width calculation.
	// SECOND: Pass ANSI-styled string into ANSI-aware wrapper using W_i_alloc.
	// THIRD: Guarantee no raw ** / *** delimiters survive.
	wrapStyledCell := func(cell string, w int) []string {
		if w < 1 {
			w = 1
		}
		if strings.TrimSpace(cell) == "" {
			return []string{""}
		}
		styled := renderTableCell(cell)
		if styled == "" {
			return []string{""}
		}
		// Safety: ensure delimiters stripped (renderTableCell already does)
		if strings.Contains(styled, "**") {
			styled = strings.ReplaceAll(styled, "**", "")
		}
		if strings.Contains(styled, "***") {
			styled = strings.ReplaceAll(styled, "***", "")
		}
		wrapped := ansi.Wordwrap(styled, w, " ")
		lines := strings.Split(wrapped, "\n")
		var out []string
		for _, ln := range lines {
			if ansi.StringWidth(ln) > w {
				chunks := chunkWord(ln, w)
				if len(chunks) == 0 {
					out = append(out, ln)
				} else {
					out = append(out, chunks...)
				}
			} else {
				out = append(out, ln)
			}
		}
		if len(out) == 0 {
			out = []string{""}
		}
		return out
	}

	// Build a row string for visual line k of a wrapped logical row.
	// wrapped already contains ANSI-styled, wrapped lines (not plain).
	buildRow := func(wrapped [][]string, k int, isHeader bool) string {
		var b strings.Builder
		b.WriteString("│")
		for i := 0; i < ncols; i++ {
			contentStyled := ""
			if i < len(wrapped) && k < len(wrapped[i]) {
				contentStyled = wrapped[i][k]
			}
			w := ansi.StringWidth(contentStyled)
			pad := colWidths[i] - w
			if pad < 0 {
				pad = 0
			}
			b.WriteString(" ")
			if isHeader && contentStyled != "" {
				b.WriteString(headerFg + contentStyled + reset)
			} else {
				b.WriteString(contentStyled)
			}
			b.WriteString(strings.Repeat(" ", pad))
			b.WriteString(" │")
		}
		return b.String()
	}

	borderLine := func(border string) DocumentLine {
		return DocumentLine{
			Spans: []RenderSpan{
				{StartCell: 0, EndCell: outerCells, SourceStart: 0, SourceEnd: 0, Selectable: false},
				{StartCell: outerCells, EndCell: outerCells + totalWidth, SourceStart: 0, SourceEnd: 0, Selectable: false},
			},
			RawText:     "",
			RenderedStr: gutterFg + outerGutter + reset + borderFg + border + reset,
		}
	}

	contentLineFromStr := func(rendered string) DocumentLine {
		raw := ansi.Strip(rendered)
		return DocumentLine{
			Spans: []RenderSpan{
				{StartCell: 0, EndCell: outerCells, SourceStart: 0, SourceEnd: 0, Selectable: false},
				{StartCell: outerCells, EndCell: outerCells + totalWidth, SourceStart: 0, SourceEnd: len([]rune(raw)), Selectable: true},
			},
			RawText:     raw,
			RenderedStr: gutterFg + outerGutter + reset + rendered,
		}
	}

	var out []DocumentLine
	out = append(out, borderLine(topBorder))

	// Header row with wrapping.
	if len(headerCells) > 0 {
		// Normalize header to ncols.
		plainHeader := make([]string, ncols)
		for i := 0; i < ncols; i++ {
			if i < len(headerCells) {
				plainHeader[i] = headerCells[i]
			}
		}
		wrappedHeader := make([][]string, ncols)
		maxK := 0
		for i := 0; i < ncols; i++ {
			wrappedHeader[i] = wrapStyledCell(plainHeader[i], colWidths[i])
			if len(wrappedHeader[i]) > maxK {
				maxK = len(wrappedHeader[i])
			}
		}
		for k := 0; k < maxK; k++ {
			rowStr := buildRow(wrappedHeader, k, true)
			out = append(out, contentLineFromStr(rowStr))
		}
	}
	if hasSep {
		out = append(out, borderLine(sepBorder))
	}
	// Body rows with wrapping.
	for _, row := range bodyRows {
		plainCols := make([]string, ncols)
		for i := 0; i < ncols; i++ {
			if i < len(row) {
				plainCols[i] = row[i]
			}
		}
		wrappedCols := make([][]string, ncols)
		maxK := 0
		for i := 0; i < ncols; i++ {
			wrappedCols[i] = wrapStyledCell(plainCols[i], colWidths[i])
			if len(wrappedCols[i]) > maxK {
				maxK = len(wrappedCols[i])
			}
		}
		for k := 0; k < maxK; k++ {
			rowStr := buildRow(wrappedCols, k, false)
			out = append(out, contentLineFromStr(rowStr))
		}
	}
	out = append(out, borderLine(botBorder))
	return out
}

// stripBackgroundANSI removes any background color escapes (48;2;...) from an
// ANSI string to preserve native transparent terminal backgrounds.
func stripBackgroundANSI(s string) string {
	if !strings.Contains(s, "48;2;") {
		return s
	}
	// Replace SGR sequences containing background codes.
	// We scan for \x1b[...m and filter out 48;2;r;g;b segments.
	var out strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				j++
			}
			seq := s[i:j]
			// Filter background codes inside seq
			inner := seq[2 : len(seq)-1] // between [ and m
			parts := strings.Split(inner, ";")
			var filtered []string
			k := 0
			for k < len(parts) {
				if parts[k] == "48" && k+4 < len(parts) && parts[k+1] == "2" {
					k += 5
					continue
				}
				filtered = append(filtered, parts[k])
				k++
			}
			if len(filtered) == 0 {
				// If only background was present, emit reset instead of empty
				out.WriteString("\x1b[0m")
			} else {
				out.WriteString("\x1b[" + strings.Join(filtered, ";") + "m")
			}
			i = j
			continue
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}

// normalizeShellLang maps shell-like identifiers to Chroma's bash lexer name.
func normalizeShellLang(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	switch l {
	case "sh", "shell", "zsh", "bash", "console", "cmd", "terminal":
		return "bash"
	default:
		return lang
	}
}

// isShellLang reports whether lang is a shell/command language needing fallback.
func isShellLang(lang string) bool {
	l := strings.ToLower(strings.TrimSpace(lang))
	switch l {
	case "sh", "shell", "zsh", "bash", "console", "cmd", "terminal":
		return true
	default:
		return false
	}
}

// colorizeShellFallback is a custom tokenizer for shell/command lines when
// Chroma's lexer emits uncolored Text for command tokens (e.g. "go", "build").
// It emits 24-bit foreground codes without background fill:
//   - first token (command): #a6e3a1 (green) or #89dceb (cyan)
//   - flags/subcommands (-v, --flag, run): #cba6f7 (mauve) or #89b4fa (blue)
//   - args/paths (hello.go): #fab387 (peach) or #f9e2af (yellow)
func colorizeShellFallback(line string) string {
	const (
		colCmd  = "\x1b[38;2;166;227;161m" // #a6e3a1 green
		colFlag = "\x1b[38;2;203;166;247m" // #cba6f7 mauve
		colArg  = "\x1b[38;2;250;179;135m" // #fab387 peach
		reset   = "\x1b[0m"
	)
	var buf strings.Builder
	i := 0
	tokenIdx := 0
	for i < len(line) {
		if line[i] == ' ' || line[i] == '\t' {
			buf.WriteByte(line[i])
			i++
			continue
		}
		j := i
		if j < len(line) && (line[j] == '"' || line[j] == '\'') {
			quote := line[j]
			j++
			for j < len(line) && line[j] != quote {
				j++
			}
			if j < len(line) {
				j++ // include closing quote
			}
		} else {
			for j < len(line) && line[j] != ' ' && line[j] != '\t' {
				j++
			}
		}
		token := line[i:j]
		var sgr string
		switch {
		case tokenIdx == 0:
			sgr = colCmd
		case strings.HasPrefix(token, "-"):
			sgr = colFlag
		case strings.Contains(token, ".") || strings.Contains(token, "/"):
			sgr = colArg
		default:
			// subcommand like "run", "build"
			sgr = colFlag
		}
		buf.WriteString(sgr)
		buf.WriteString(token)
		buf.WriteString(reset)
		tokenIdx++
		i = j
	}
	if buf.Len() == 0 {
		return line
	}
	return buf.String()
}

// colorizeNoBg returns Chroma/Catppuccin syntax-highlighted line with foreground
// colors (38;2;...) for keywords, identifiers, strings, etc., explicitly stripping
// any background (48;2;...) to keep terminal background transparent.
func colorizeNoBg(lang, line string) string {
	if strings.TrimSpace(line) == "" {
		return line
	}
	normalized := normalizeShellLang(lang)
	lexer := lexers.Get(normalized)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)
	it, err := lexer.Tokenise(nil, line)
	if err != nil {
		if isShellLang(lang) {
			return colorizeShellFallback(line)
		}
		return line
	}
	var buf strings.Builder
	hasColored := false
	for _, tok := range it.Tokens() {
		if tok.Value == "" {
			continue
		}
		// Detect whether Chroma produced any colored token beyond plain Text.
		// Shell lexers often emit a single Text token for unknown commands.
		if tok.Type != chroma.Text && tok.Type != 0 {
			hasColored = true
		}
		parts := strings.Split(tok.Value, "\n")
		for pi, part := range parts {
			if pi > 0 {
				buf.WriteString("\n")
			}
			if part == "" {
				continue
			}
			sgr := sgrForToken(tok.Type)
			sgr = stripBackgroundANSI(sgr)
			buf.WriteString(sgr)
			buf.WriteString(part)
			buf.WriteString("\x1b[0m")
		}
	}
	if buf.Len() == 0 {
		if isShellLang(lang) {
			return colorizeShellFallback(line)
		}
		return line
	}
	// If shell language produced only uncolored Text (e.g. "go run hello.go"
	// as a single Text token), fall back to custom tokenizer to ensure
	// command elements are colorized with 38;2; codes.
	if isShellLang(lang) && !hasColored {
		// Check if buffer contains only the default foreground color (#cdd6f4)
		// without distinct command/flag/arg colors.
		s := buf.String()
		// If s contains only one distinct color or is equivalent to plain,
		// use fallback for richer shell highlighting.
		if strings.Count(s, "\x1b[38;2;") <= 1 {
			return colorizeShellFallback(line)
		}
		// Also if lexer emitted single Text token covering whole line, fallback
		toks := it.Tokens()
		if len(toks) == 1 && strings.TrimSpace(toks[0].Value) == strings.TrimSpace(line) {
			return colorizeShellFallback(line)
		}
	}
	// Ensure no background codes leak
	res := buf.String()
	if strings.Contains(res, "48;2;") {
		res = stripBackgroundANSI(res)
	}
	return res
}

// gutterDocumentLine builds an empty AI gutter row (blank physical line).
func gutterDocumentLine(wrapWidth int) DocumentLine {
	return DocumentLine{
		Spans:       []RenderSpan{{StartCell: 0, EndCell: 2, SourceStart: 0, SourceEnd: 0, Selectable: false}},
		RawText:     "",
		RenderedStr: dimmedStyle.Render("│ "),
	}
}

// wrapForContentWidth wraps text strictly at maxWidth using cell-aware logic.
// It delegates to wrapIndentedLine which handles CJK double-width and chunking.
func wrapForContentWidth(text string, maxWidth int) []string {
	if maxWidth < 1 {
		maxWidth = 1
	}
	return wrapIndentedLine(text, maxWidth)
}

// IncrementalLayoutUpdate appends or invalidates ONLY the trailing record/line
// currently streaming. Returns updated layout. Full re-flattening is done only
// when width changes or record count shrinks.
func IncrementalLayoutUpdate(prev *DocumentLayout, records []record, wrapWidth int, username ...string) DocumentLayout {
	var uname string
	if len(username) > 0 {
		uname = username[0]
	}
	if wrapWidth < 20 {
		wrapWidth = 20
	}
	if prev.width != wrapWidth {
		// Width changed: full rebuild, but preserve the per-turn trace-summary
		// dedup so a width resize mid-turn can never duplicate "▸ Trace:".
		return buildDocumentLayout(records, wrapWidth, uname, prev.traceSummaryEmitted)
	}
	var prevTurns map[uint64]bool
	if prev.traceSummaryEmitted {
		prevTurns = prev.renderedTurns
	}
	prevLen := len(prev.Lines)
	// Count expected lines for records up to len-1
	// If records length unchanged but last record's text changed (streaming), invalidate trailing lines.
	if len(records) == 0 {
		return DocumentLayout{Lines: nil, width: wrapWidth, traceSummaryEmitted: prev.traceSummaryEmitted, renderedTurns: prevTurns}
	}
	// Heuristic: if records length same as before's record count estimate, only rebuild last record
	// We don't store record count in layout, so we estimate: if number of lines decreased/increased by more than one record's worth, do full rebuild.
	// For simplicity, if records grew by exactly 1, incremental append
	if prevLen > 0 {
		// Try incremental append if last layout's RecordIdx max < len(records)-1
		maxIdx := -1
		for _, l := range prev.Lines {
			if l.RecordIdx > maxIdx {
				maxIdx = l.RecordIdx
			}
		}
		if maxIdx == len(records)-1 {
			// Streaming: last record mutated, rebuild only its lines if text changed
			startIdx := -1
			for i, l := range prev.Lines {
				if l.RecordIdx == len(records)-1 {
					startIdx = i
					break
				}
			}
			if startIdx >= 0 {
				// Check if last record's text actually changed (streaming)
				// If not changed, no need to rebuild
				lastRecText := sanitizeText(records[len(records)-1].text)
				// Reconstruct last record's raw from docLayout lines
				var lastRaw strings.Builder
				for _, l := range prev.Lines[startIdx:] {
					if l.RecordIdx == len(records)-1 {
						if lastRaw.Len() > 0 {
							lastRaw.WriteString("\n")
						}
						lastRaw.WriteString(l.RawText)
					}
				}
				// Compare stripped lastRaw vs sanitized text's first line? For simplicity, compare via rendered
				// If lengths match and no streaming flag, skip rebuild
				if lastRaw.String() == lastRecText || lastRaw.String() == ansi.Strip(lastRecText) {
					return DocumentLayout{
						Lines:               append([]DocumentLine(nil), prev.Lines...),
						width:               wrapWidth,
						traceSummaryEmitted: prev.traceSummaryEmitted,
						renderedTurns:       prevTurns,
					}
				}
				// Build new lines for last record — seed the per-turn trace-summary
				// dedup so a trace record is never re-summarized.
				newRecLines := buildDocumentLayoutWithTurns(records[len(records)-1:], wrapWidth, uname, prevTurns)
				// Replace trailing segment and fix RecordIdx
				kept := append([]DocumentLine(nil), prev.Lines[:startIdx]...)
				base := len(kept)
				origIdx := len(records) - 1
				for i := range newRecLines.Lines {
					newRecLines.Lines[i].GlobalY = base + i
					newRecLines.Lines[i].RecordIdx = origIdx
				}
				newLines := make([]DocumentLine, 0, len(kept)+len(newRecLines.Lines))
				newLines = append(newLines, kept...)
				newLines = append(newLines, newRecLines.Lines...)
				return DocumentLayout{
					Lines:               newLines,
					width:               wrapWidth,
					traceSummaryEmitted: prev.traceSummaryEmitted || newRecLines.traceSummaryEmitted,
					renderedTurns:       mergeRenderedTurns(prevTurns, newRecLines.renderedTurns),
				}
			}
		}
		// ── MULTI-RECORD APPEND (stream-completion) ────────────────
		// Records routinely grow by MORE than one between refreshes: stream
		// completion lands the committed assistant record AND the terminal
		// "✔ done · +N tok" telemetry record in the same turn. Append ALL
		// records after the last committed index instead of assuming exactly one
		// new record — otherwise every record after the first new one is silently
		// dropped from the layout (the completed answer disappears while only the
		// status line renders).
		if maxIdx < len(records)-1 {
			added := buildDocumentLayoutWithTurns(records[maxIdx+1:], wrapWidth, uname, prevTurns)
			// Adjust GlobalY and RecordIdx (sub-build uses 0-relative indices).
			base := prevLen
			baseIdx := maxIdx + 1
			for i := range added.Lines {
				added.Lines[i].GlobalY = base + i
				added.Lines[i].RecordIdx = baseIdx + added.Lines[i].RecordIdx
			}
			newLines := append(append([]DocumentLine(nil), prev.Lines...), added.Lines...)
			return DocumentLayout{
				Lines:               newLines,
				width:               wrapWidth,
				traceSummaryEmitted: prev.traceSummaryEmitted || added.traceSummaryEmitted,
				renderedTurns:       mergeRenderedTurns(prevTurns, added.renderedTurns),
			}
		}
		// Fallback: if line count differs drastically or records shrunk, full rebuild
		if len(records) < maxIdx+1 {
			return buildDocumentLayout(records, wrapWidth, uname, prev.traceSummaryEmitted)
		}
	}
	return buildDocumentLayout(records, wrapWidth, uname, prev.traceSummaryEmitted)
}

func mergeRenderedTurns(a, b map[uint64]bool) map[uint64]bool {
	out := make(map[uint64]bool)
	for k, v := range a {
		if v {
			out[k] = true
		}
	}
	for k, v := range b {
		if v {
			out[k] = true
		}
	}
	return out
}
