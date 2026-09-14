// Package planner — ask_clarification tool schema + clarification pause.
//
// The ask_clarification tool is the LLM clarification loop: instead of
// guessing on ambiguous intent the model emits a structured question with a
// bounded option list and the engine pauses the session wall-timer until the
// human resolves it via MsgClarificationResponse.
package planner

import "time"

// AskOption is one selectable clarification option.
type AskOption struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Recommended bool   `json:"recommended,omitempty"`
}

// AskRequest is the ask_clarification tool input schema:
//
//	question (string, required): the clarification question.
//	multi_select (bool): allow selecting multiple options.
//	options (array of {id, title, description, recommended}).
type AskRequest struct {
	Question    string      `json:"question"`
	MultiSelect bool        `json:"multi_select"`
	Options     []AskOption `json:"options"`
}

// ToolName is the canonical tool name the LLM emits.
const ToolNameAskClarification = "ask_clarification"

// Validate reports whether the request is a well-formed tool call: non-empty
// question, at least one option, unique non-empty option ids.
func (r AskRequest) Validate() bool {
	if r.Question == "" || len(r.Options) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(r.Options))
	for _, o := range r.Options {
		if o.ID == "" || o.Title == "" {
			return false
		}
		if _, dup := seen[o.ID]; dup {
			return false
		}
		seen[o.ID] = struct{}{}
	}
	return true
}

// RecommendedIDs returns the ids flagged as recommended (badge hint).
func (r AskRequest) RecommendedIDs() []string {
	var out []string
	for _, o := range r.Options {
		if o.Recommended {
			out = append(out, o.ID)
		}
	}
	return out
}

// ── Engine state pause ───────────────────────────────────────────────────
// When EventClarificationRequired is emitted the active session wall-timer
// pauses; it resumes on MsgClarificationResponse. ClarificationPause tracks
// the frozen accumulation so wall time never bills the human think time.

// ClarificationPause is the pause controller for one clarification round.
type ClarificationPause struct {
	startedAt   time.Time
	pausedAt    time.Time
	accumulated time.Duration
	paused      bool
}

// NewClarificationPause starts a wall-timer session.
func NewClarificationPause(start time.Time) *ClarificationPause {
	if start.IsZero() {
		start = time.Now()
	}
	return &ClarificationPause{startedAt: start}
}

// Pause freezes the wall-timer (idempotent). It is invoked when
// EventClarificationRequired is emitted.
func (p *ClarificationPause) Pause() {
	if p == nil || p.paused {
		return
	}
	now := time.Now()
	p.accumulated += now.Sub(p.startedAt)
	p.pausedAt = now
	p.paused = true
}

// Resume restarts the wall-timer (idempotent). It is invoked upon receiving
// MsgClarificationResponse.
func (p *ClarificationPause) Resume() {
	if p == nil || !p.paused {
		return
	}
	p.startedAt = time.Now()
	p.paused = false
}

// Paused reports whether the timer is currently frozen.
func (p *ClarificationPause) Paused() bool {
	if p == nil {
		return false
	}
	return p.paused
}

// Elapsed returns the billable wall time excluding paused intervals.
func (p *ClarificationPause) Elapsed() time.Duration {
	if p == nil {
		return 0
	}
	if p.paused {
		return p.accumulated
	}
	d := p.accumulated + time.Since(p.startedAt)
	if d < 0 {
		return 0
	}
	return d
}
