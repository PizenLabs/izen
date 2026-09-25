package contextspec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/PizenLabs/izen/internal/session"
)

// CompileInput is the bounded, read-only input to one compilation. The compiler
// receives conversation messages and the previous committed spec; it never
// receives a session handle, a workspace handle, or any execution authority.
type CompileInput struct {
	// ConversationRevision is the base revision this compilation reads. The
	// compiler copies it into the candidate so the Control Plane can CAS it.
	ConversationRevision uint64
	// Objective is the current human objective.
	Objective string
	// Messages is the bounded conversation window.
	Messages []session.Message
	// PreviousSpec is the last committed spec, offered as prior active state.
	PreviousSpec *ContextSpec
}

// CompiledCandidate is the compiler's untrusted output. It is a PROPOSAL: the
// compiler never mutates the store, the session or any authority. Only the
// Control Plane commits a candidate via Store.Commit with a revision CAS.
type CompiledCandidate struct {
	// BaseConversationRevision is the revision the candidate was compiled from.
	BaseConversationRevision uint64
	// Spec is the proposed semantic state (untrusted).
	Spec ContextSpec
}

// ContextCompiler reduces a conversation window into active semantic state. It
// is a semantic state REDUCER, not a summarizer: contradictory historical
// statements collapse to the currently active state.
type ContextCompiler interface {
	Compile(ctx context.Context, input CompileInput) (CompiledCandidate, error)
}

// RuleCompiler is the deterministic, offline default compiler. It performs a
// conservative last-write-wins reduction over explicit and natural-language
// decision/constraint statements. It never calls a model, never reads the
// filesystem and never touches authority. A model-backed compiler may be
// injected later behind the same interface without changing the Control Plane.
type RuleCompiler struct {
	Now func() time.Time
}

// NewRuleCompiler constructs the deterministic default compiler.
func NewRuleCompiler() *RuleCompiler { return &RuleCompiler{} }

func (c *RuleCompiler) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Compile implements ContextCompiler.
func (c *RuleCompiler) Compile(ctx context.Context, in CompileInput) (CompiledCandidate, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return CompiledCandidate{}, err
		}
	}
	spec := ContextSpec{
		Version:              ContextSpecVersion,
		ConversationRevision: in.ConversationRevision,
		CompiledAt:           c.now(),
	}

	goal := strings.TrimSpace(in.Objective)
	var constraints []Constraint
	var decisions []Decision
	var questions []Question
	var targets []TargetRef
	seenTargets := map[string]struct{}{}
	activeDecision := map[string]int{} // topic → index of active decision
	seenConstraint := map[string]struct{}{}
	seenQuestion := map[string]struct{}{}

	for turn, msg := range in.Messages {
		prov := Provenance{
			ConversationRevision: in.ConversationRevision,
			SourceTurn:           turn,
			Role:                 msg.Role,
		}
		// Targets are extracted from every turn (user or assistant) because a
		// reference is a fact about the conversation, not an instruction.
		for _, path := range extractTargetRefs(msg.Content) {
			if _, ok := seenTargets[path]; ok {
				continue
			}
			seenTargets[path] = struct{}{}
			targets = append(targets, TargetRef{Path: path, Provenance: prov})
		}
		if msg.Role != "user" {
			continue
		}
		for _, clause := range splitClauses(msg.Content) {
			kind, topic, text, negated := classifyClause(clause)
			switch kind {
			case clauseGoal:
				if goal == "" {
					goal = text
				}
			case clauseQuestion:
				if _, ok := seenQuestion[text]; ok {
					continue
				}
				seenQuestion[text] = struct{}{}
				questions = append(questions, Question{
					ID:         itemID("q", text, prov),
					Text:       text,
					Provenance: prov,
				})
			case clauseReset:
				for i := range decisions {
					decisions[i].Active = false
				}
				activeDecision = map[string]int{}
			case clauseConstraint:
				id := itemID("c", text, prov)
				if _, ok := seenConstraint[id]; ok {
					continue
				}
				seenConstraint[id] = struct{}{}
				constraints = append(constraints, Constraint{
					ID:         id,
					Text:       text,
					Active:     true,
					Provenance: prov,
				})
			case clauseDecision:
				decisions, activeDecision = applyDecision(decisions, activeDecision, topic, text, negated, prov)
			}
		}
	}

	spec.Goal = goal
	spec.Targets = targets
	spec.Constraints = constraints
	spec.Decisions = decisions
	spec.OpenQuestions = questions

	return CompiledCandidate{BaseConversationRevision: in.ConversationRevision, Spec: spec}, nil
}

// applyDecision folds one decision statement into the active-state reduction.
// A new decision supersedes the previous active decision on the same topic; a
// negated decision deactivates any active decision it contradicts. Superseded
// decisions are retained with Active=false as lineage.
func applyDecision(decisions []Decision, active map[string]int, topic, text string, negated bool, prov Provenance) ([]Decision, map[string]int) {
	value := strings.ToLower(strings.TrimSpace(text))
	if negated {
		for i := range decisions {
			if !decisions[i].Active || decisions[i].Topic != topic {
				continue
			}
			if value == "" || strings.Contains(strings.ToLower(decisions[i].Text), value) {
				decisions[i].Active = false
				delete(active, topic)
			}
		}
		return decisions, active
	}
	if i, ok := active[topic]; ok && i < len(decisions) {
		decisions[i].Active = false
	}
	decisions = append(decisions, Decision{
		ID:         itemID("d", topic+"\x00"+text, prov),
		Topic:      topic,
		Text:       text,
		Active:     true,
		Provenance: prov,
	})
	active[topic] = len(decisions) - 1
	return decisions, active
}

// clauseKind is the closed classification of one conversation clause.
type clauseKind int

const (
	clauseNone clauseKind = iota
	clauseGoal
	clauseDecision
	clauseConstraint
	clauseQuestion
	clauseReset
)

// Decision topic for the vertical slice: the conversation currently has a
// single "approach" decision dimension. Additional dimensions can be added by
// returning a different topic from classifyClause without changing the reducer.
const decisionTopicApproach = "approach"

var (
	goalPrefixes = []string{"goal:", "objective:", "aim:"}

	resetPhrases = []string{"never mind", "nevermind", "forget it", "forget that", "scratch that", "ignore that", "discard that", "actually, no"}

	decisionUsePrefixes = []string{
		"actually use ", "actually keep ", "actually, use ", "actually, keep ",
		"we should use ", "we should keep ", "we should actually use ",
		"let's use ", "lets use ", "let us use ", "i want to use ",
		"switch to ", "go with ", "going with ", "choose ", "choose to use ",
		"prefer ", "only use ", "keep using ", "keep ", "use ",
		"decision:",
	}
	decisionNegPrefixes = []string{
		"don't use ", "dont use ", "do not use ", "never use ", "no longer use ",
		"stop using ", "avoid ", "remove ", "drop ",
	}
	constraintPrefixes = []string{
		"constraint:", "requirement:", "must not ", "must ", "never ", "always ",
		"ensure ", "make sure ",
	}
)

// classifyClause returns the semantic kind, decision topic, normalized text,
// and negation flag of one clause.
func classifyClause(clause string) (clauseKind, string, string, bool) {
	raw := strings.TrimSpace(clause)
	if raw == "" {
		return clauseNone, "", "", false
	}
	lower := strings.ToLower(raw)
	for _, p := range resetPhrases {
		if lower == p || strings.HasPrefix(lower, p+",") {
			return clauseReset, "", "", false
		}
	}
	for _, p := range goalPrefixes {
		if strings.HasPrefix(lower, p) {
			return clauseGoal, "", strings.TrimSpace(raw[len(p):]), false
		}
	}
	for _, p := range decisionNegPrefixes {
		if strings.HasPrefix(lower, p) {
			return clauseDecision, decisionTopicApproach, strings.TrimSpace(raw[len(p):]), true
		}
	}
	for _, p := range decisionUsePrefixes {
		if strings.HasPrefix(lower, p) {
			return clauseDecision, decisionTopicApproach, strings.TrimSpace(raw[len(p):]), false
		}
	}
	for _, p := range constraintPrefixes {
		if strings.HasPrefix(lower, p) {
			return clauseConstraint, "", strings.TrimSpace(raw[len(p):]), false
		}
	}
	if strings.HasSuffix(raw, "?") {
		return clauseQuestion, "", raw, false
	}
	return clauseNone, "", "", false
}

// splitClauses splits a message into sentence-like clauses on newlines and
// terminal punctuation, keeping the terminator so question detection survives.
func splitClauses(message string) []string {
	var out []string
	var b strings.Builder
	flush := func(terminator string) {
		clause := strings.TrimSpace(b.String())
		b.Reset()
		if clause == "" {
			return
		}
		out = append(out, clause+terminator)
	}
	for _, r := range message {
		switch r {
		case '\n', '\r':
			flush("")
		case '.', ';', '!', ',':
			// Commas separate independent clauses (e.g. "Never mind, use
			// existing CSS") so an explicit reset cannot swallow the decision
			// that follows it.
			flush("")
		case '?':
			flush("?")
		default:
			b.WriteRune(r)
		}
	}
	flush("")
	return out
}

// extractTargetRefs returns the @path references in one message, first-seen
// order, cleaned of trailing punctuation.
func extractTargetRefs(content string) []string {
	var out []string
	for _, field := range strings.Fields(content) {
		if !strings.HasPrefix(field, "@") || len(field) < 2 {
			continue
		}
		path := strings.Trim(field[1:], "`'\".,;:!?)(")
		if path == "" || path == "." {
			continue
		}
		out = append(out, path)
	}
	return out
}

// itemID derives a deterministic identity for one semantic item so lineage is
// stable across compilations.
func itemID(kind, text string, prov Provenance) string {
	var b strings.Builder
	b.WriteString("izen-context-item-v1")
	writeField(&b, kind)
	writeField(&b, text)
	writeField(&b, strings.ToLower(prov.Role))
	sum := sha256.Sum256([]byte(b.String()))
	return kind + "-" + hex.EncodeToString(sum[:])[:12]
}

func writeField(b *strings.Builder, s string) {
	b.WriteString(strconv.Itoa(len(s)))
	b.WriteByte(':')
	b.WriteString(s)
	b.WriteByte(0)
}
