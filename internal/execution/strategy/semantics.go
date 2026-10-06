package strategy

import (
	"strings"

	"github.com/PizenLabs/izen/internal/gateway"
)

// ── THE CANONICAL SEMANTIC BOUNDARY ─────────────────────────────────────────
//
// This file answers exactly one question, and it is the FIRST question the
// runtime asks about a request:
//
//	DID THE USER ASK THE WORKSPACE TO CHANGE?
//
// Everything after it — strategy family, artifact contract, objective
// operation, required scope, authorization — is downstream of that answer, and
// none of it may re-derive it. In particular, nothing here confers authority:
// a MUTATION verdict is a SEMANTIC MUTATION INTENT, and it must still pass the
// admission gate, the scope derivation, the grant and the human approval before
// a byte is written. The separation is the whole point:
//
//	SemanticMutation     !=  authorized mutation
//	SemanticUndetermined !=  SemanticMutation
//
// ── WHY A VERDICT AND NOT A KEYWORD SCAN ────────────────────────────────────
//
// The classifier this file replaces ran strings.Contains over the whole
// objective against five signal lists and returned a mutation-shaped operation
// when nothing matched. Three defects followed, all of them authority defects:
//
//  1. IT COULD NOT SAY "I DO NOT KNOW". OperationContent was simultaneously the
//     zero value and the catch-all, so "a localized content change was
//     determined" and "no signal matched at all" were the same fact. That is why
//     "Review this project and suggest improvements.", "Review this project and
//     implement the improvements." and "Make this project better." all produced
//     one indistinguishable verdict.
//
//  2. IT MATCHED INSIDE WORDS. `move` is a substring of `remove`, so
//     "check @index.html and remove extra contents" classified as a REFACTOR.
//     `design` is a substring of `redesign`, and the fix for that was to add
//     "redesign" to the mutation table — treating a matching bug with a wider
//     dictionary. Every such entry widens authority for text nobody reviewed.
//
//  3. IT HAD NO ADVISORY ROLE. explanation and debugging were represented;
//     "review / suggest / advise / critique" were not, so a request whose
//     deliverable is a JUDGEMENT about the workspace had no read-only verdict to
//     land on and fell through to the mutation-shaped default.
//
// The fix is structural, not lexical. A request is decomposed into CLAUSES, each
// clause is read for its SPEECH ACT, and the acts are combined by a rule that can
// only ever narrow authority:
//
//	read-only constraint stated  -> READ-ONLY   (an explicit negation can only
//	                                           REMOVE mutation intent)
//	any clause directs a change -> MUTATION
//	any clause asks to look or  -> READ-ONLY
//	  to advise
//	nothing could be read       -> UNDETERMINED
//
// UNDETERMINED is a real answer, and it is the one the old classifier could not
// express. It is not an error and it is not a failure: it is the runtime saying
// that the request does not say what it wants done to the workspace, which is a
// question for the human and never a licence to guess.

// SemanticIntent is the closed canonical answer to "did the user ask the
// workspace to change?". It is a SEMANTIC verdict about the request text and
// carries no authority of any kind.
type SemanticIntent string

const (
	// SemanticUndetermined: no clause of the request could be read as a
	// workspace act. The request may name a goal ("make this project better")
	// without saying what change would satisfy it. This verdict is terminal for
	// authority: it can never become MUTATION without a new request.
	SemanticUndetermined SemanticIntent = "UNDETERMINED"

	// SemanticReadOnly: the request asks to be shown, told, checked or advised
	// something. It names no workspace change and carries no mutation intent.
	SemanticReadOnly SemanticIntent = "READ_ONLY"

	// SemanticMutation: at least one clause DIRECTS a change to the workspace.
	// This is semantic intent only — it is not authorization, and every
	// downstream gate still applies unchanged.
	SemanticMutation SemanticIntent = "MUTATION"
)

// AllSemanticIntents returns the closed vocabulary, so a test can assert these
// three values are the whole set: callers branch on this type, so widening it is
// an architectural decision rather than something a call site may do locally.
func AllSemanticIntents() []SemanticIntent {
	return []SemanticIntent{SemanticUndetermined, SemanticReadOnly, SemanticMutation}
}

// String returns the canonical semantic-intent label.
func (s SemanticIntent) String() string { return string(s) }

// RequiresMutation reports whether the verdict expresses an intent to write the
// workspace. Only SemanticMutation does; UNDETERMINED is deliberately NOT a
// mutation, because the runtime may not resolve an unread request into a write.
func (s SemanticIntent) RequiresMutation() bool { return s == SemanticMutation }

// SemanticVerdict is the complete, evidence-carrying outcome of one semantic
// classification pass. The Intent is the verdict; the Operation is the existing
// engine vocabulary projected from it, and Clauses is why.
type SemanticVerdict struct {
	// Intent is the canonical verdict. It is never the zero value: an
	// unclassified request is UNDETERMINED.
	Intent SemanticIntent
	// Operation is the engine's existing OperationKind projected from Intent.
	// An UNDETERMINED verdict projects to OperationUndetermined, which is not a
	// mutation family.
	Operation OperationKind
	// Clauses records the per-clause reading. It is evidence for a human and
	// for $inspect; it never participates in the verdict a second time.
	Clauses []ClauseReading
	// Reason is the deterministic justification, verbatim, in runtime
	// vocabulary.
	Reason string
}

// HasAdvisoryClause reports that the request asks the runtime to JUDGE or
// PROPOSE something about the workspace — "review", "suggest", "advise",
// "critique".
//
// It is narrower than IsReadOnly on purpose. An investigative request ("inspect
// every handler", "trace where the bug is") is also READ-ONLY, but it asks for a
// report FROM the workspace, and reporting on a named target is a legitimate
// targeted path. An advisory request asks for ADVICE ABOUT the workspace, and
// its deliverable is words — so it must not be handed a mutation strategy
// merely because it named a file.
func (v SemanticVerdict) HasAdvisoryClause() bool {
	for _, c := range v.Clauses {
		if c.Act == ActAdvise {
			return true
		}
	}
	return false
}

// IsUndetermined reports that the request did not say what it wants done.
func (v SemanticVerdict) IsUndetermined() bool { return v.Intent == SemanticUndetermined }

// IsReadOnly reports that the request asks to be shown or advised something.
func (v SemanticVerdict) IsReadOnly() bool { return v.Intent == SemanticReadOnly }

// RequiresMutation reports semantic MUTATION intent. It is NOT authority.
func (v SemanticVerdict) RequiresMutation() bool { return v.Intent.RequiresMutation() }

// ── Speech acts ─────────────────────────────────────────────────────────────

// SpeechAct is what one clause ASKS the workspace to be. The roles partition the
// taxonomy: EXECUTIVE is the only role that can produce MUTATION.
type SpeechAct string

const (
	// ActExecutive: the clause directs a durable change to the workspace.
	ActExecutive SpeechAct = "EXECUTIVE"
	// ActInspect: the clause asks the runtime to look at, or report on, the
	// workspace.
	ActInspect SpeechAct = "INSPECT"
	// ActAdvise: the clause asks for a judgement, suggestion or proposal. Its
	// deliverable is ADVICE ABOUT the workspace, not a change TO it.
	ActAdvise SpeechAct = "ADVISE"
	// ActUnread: no signal in the clause could be read as an act.
	ActUnread SpeechAct = "UNREAD"
)

// RequiresMutation reports whether the act directs a workspace change.
func (a SpeechAct) RequiresMutation() bool { return a == ActExecutive }

// ClauseReading is the per-clause record of the pass.
type ClauseReading struct {
	// Text is the clause, verbatim from the objective.
	Text string
	// Act is what the clause asks the workspace to be.
	Act SpeechAct
	// Signal is the phrase that decided the reading, or "" for ActUnread.
	Signal string
}

// ── The role-tagged vocabulary ──────────────────────────────────────────────
//
// These are the SAME five tables the operation classifier already used, moved
// here so that ONE answer exists, plus the advisory table the taxonomy was
// missing. Nothing is added to a mutation list: the mutation lists are
// byte-for-byte what they were, and the only new entries are advisory or
// explicitly narrowing.

// advisorySignals are the requests whose DELIVERABLE is a judgement about the
// workspace rather than a change to it. This is the role the taxonomy was
// missing: without it "review this project and suggest improvements" had no
// read-only verdict to land on and fell through to the mutation-shaped default.
//
// These are not a second mutation list under a friendlier name. A clause that
// carries NO executive act lands here and the verdict is READ-ONLY; a clause
// that DOES carry one is MUTATION regardless of any advisory word in the same
// request.
var advisorySignals = []string{
	"review", "suggest", "suggestion", "suggestions", "advise", "advice",
	"recommend", "recommendation", "propose", "proposal", "critique",
	"criticize", "feedback", "opinion", "assessment", "evaluate",
}

// ── The complete role vocabulary ───────────────────────────────────────────
//
// The lists above pick an OPERATION FAMILY; these pick a SPEECH ACT. The two
// were separate because they answer separate questions, and keeping them apart
// is what let a request the runtime recognises perfectly well
// ("change bar to qux in @index.html", "inspect the repository and trace where
// the bug is") fall out of both and be reported as unreadable.
//
// So the act tables are the union of every read-only and change signal the
// engine already had: the operation families above plus the classification
// tables the intent layer has always used. No verb is invented here — each
// entry is one the engine already acted on.
//
// internal/autonomy still keeps its own copy of the same intent tables, and
// this file is where those tables should end up. The cutover is deliberately
// NOT done yet: internal/autonomy matches with a substring scan whose trailing
// spaces are load-bearing ("add " must not match inside "address"), so adopting
// these tables there means switching it to the token matcher as well, which
// changes intent classification across every caller. That is a real migration,
// not a rename, and it is recorded rather than half-applied. The two tables
// agree today, which is what keeps the contract layer and this layer from
// disagreeing about whether an objective mutates.
var inspectionVerbs = []string{
	"inspect", "search for", "trace", "where is", "where are", "find",
	"locate", "list", "show me the", "what files", "which file",
	"analyze the codebase", "gather evidence", "collect evidence",
	"verify", "check", "validate", "confirm", "is it correct", "does it work",
	"run the tests", "are the tests", "ensure", "make sure",
	"debug",
}

// modificationVerbs signal a concrete change to the workspace: the user asks
// for the workspace to be different afterwards.
var modificationVerbs = []string{
	"remove", "delete", "add", "create", "generate", "implement",
	"write", "update", "modify", "change", "fix", "correct",
	"edit", "insert", "replace", "rewrite", "build",
	"redesign", "restyle", "recreate", "re-create", "rework",
	"overhaul", "revamp",
}

// refactoringVerbs signal a structural change without new behaviour.
var refactoringVerbs = []string{
	"refactor", "restructure", "reorganize", "rename", "extract",
	"simplify", "clean up", "modernize", "reduce duplication",
}

// planningVerbs are design/architecture ADVICE. Like every other read-only
// table they are consulted only when the request carries no change verb: a
// design word DESCRIBES a change far more often than it asks for one, so
// "design a dashboard and write the files" is a mutation and "how should we
// design the migration" is not. That precedence is not a list ordering — it is
// the EXECUTIVE-wins rule of ClassifySemantic.
var planningVerbs = []string{
	"plan", "design", "architecture", "how should", "what is the best way",
	"blueprint", "roadmap", "strategy",
}

// ModificationVerbs returns the canonical change-verb vocabulary. The intent
// layer reads it rather than keeping a third copy.
func ModificationVerbs() []string { return append([]string(nil), modificationVerbs...) }

// RefactoringVerbs returns the canonical structural-change vocabulary.
func RefactoringVerbs() []string { return append([]string(nil), refactoringVerbs...) }

// InvestigationVerbs returns the canonical read-only evidence vocabulary.
func InvestigationVerbs() []string { return append([]string(nil), inspectionVerbs...) }

// PlanningVerbs returns the canonical design-advice vocabulary.
func PlanningVerbs() []string { return append([]string(nil), planningVerbs...) }

// readOnlyConstraintPhrases are EXPLICIT NEGATIONS of workspace change.
//
// The direction of this table is the point: a phrase here can only ever REMOVE
// mutation intent from a request, never create it. There is no entry anywhere in
// this file that can turn a read-only request into a mutation, which is why the
// table can stay small — it does not have to enumerate every way a human might
// decline to authorise a write, only the ones they state outright.
//
// It is consulted EXPLICITLY through StatesReadOnlyConstraint, never inside
// ClassifySemantic. A request that says "read-only", or "do not change
// anything", is read-only even when it also contains change vocabulary: the
// human's negative constraint outranks the runtime's reading of a verb, because
// the human is the authority and the classifier is not.
//
// The separation is not stylistic. ClassifySemantic also runs over
// runtime-composed provider prompts, and those carry the runtime's own scoping
// instructions — among them "do not modify any other region". Folding this
// table into the classifier made the runtime read its own prompt back as the
// user revoking mutation authority, silently downgrading every authorized
// mutation in a decomposed run. So the table answers a question only a caller
// holding a HUMAN request may ask, and it is exported precisely so the gateway
// can ask it and the router cannot. See TestSemanticLock.
var readOnlyConstraintPhrases = []string{
	"read only", "no changes", "do not change", "do not modify", "do not edit",
	"do not write", "do not create", "do not delete", "do not refactor",
	"do not touch", "without changing", "without modifying", "without editing",
	"without creating", "without deleting", "just suggest", "only suggest",
	"suggest only", "report only", "explain only",
}

// StatesReadOnlyConstraint reports whether a HUMAN request explicitly negates
// workspace change.
//
// It is deliberately NOT part of ClassifySemantic, and only the intent gateway
// may call it. ClassifySemantic runs over runtime-composed provider prompts as
// well as human ones, and those carry the runtime's own instructions — "do not
// modify any other region" — which are not a human declining a write. A
// caller holding human text asks the question; a caller holding composed text
// must not.
func StatesReadOnlyConstraint(raw string) bool {
	return findPhrase(tokenize(raw), readOnlyConstraintPhrases) != ""
}

// ── Creation and deletion verbs ─────────────────────────────────────────────
//
// creationVerbs are the objective verbs that ask for a NEW artifact. They are
// only consulted when the target has no durable pre-existing content, so
// "rewrite the docs" against an existing file is a PATCH, never a CREATE.
var creationVerbs = []string{
	"create", "add", "write", "generate", "implement", "scaffold", "new file",
}

// deletionVerbs are the objective verbs that can ask for a removal. A verb alone
// never selects the DELETE contract — the caller binds it positionally to a
// declared target — because "remove every deprecated comment from @big.go"
// removes CONTENT from a file that must survive.
var deletionVerbs = map[string]bool{
	"delete": true, "remove": true, "erase": true, "drop": true, "unlink": true,
}

// CreationVerbs returns the canonical creation-verb vocabulary. The objective
// contract layer reads it rather than keeping a second copy: two copies of the
// same fact drift, and a drift here silently changes which objective is judged
// a CREATE.
func CreationVerbs() []string { return append([]string(nil), creationVerbs...) }

// DeletionVerbs returns the canonical deletion-verb vocabulary, for the same
// reason as CreationVerbs.
func DeletionVerbs() map[string]bool {
	out := make(map[string]bool, len(deletionVerbs))
	for k, v := range deletionVerbs {
		out[k] = v
	}
	return out
}

// ── Token-boundary phrase matching ──────────────────────────────────────────
//
// tokenize splits on every non-alphanumeric character, so "read-only" and
// "read only" are the same two tokens and "@index.html" is ["index","html"].
// A signal is a PHRASE of tokens and matches only as a contiguous run of whole
// tokens.
//
// This is the fix for the substring class, and it needs no new dictionary to
// work: the existing tables become correct as written. `move` stops matching
// inside `remove`; `design` stops matching inside `redesign`; `write` stops
// matching inside `rewrite` — which is precisely the boundary the objective
// contract layer had to hand-roll for itself.
func tokenize(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !isTokenRune(r)
	})
}

// isTokenRune reports whether r belongs to a token. Every other rune is a
// boundary, which is what makes "read-only" and "read only" identical and turns
// "@index.html" into ["index", "html"].
func isTokenRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
}

// containsPhrase reports whether tokens contains the signal as a contiguous run
// of WHOLE tokens. A signal carrying no alphanumerics can never match.
func containsPhrase(tokens []string, signal string) bool {
	want := tokenize(signal)
	if len(want) == 0 || len(want) > len(tokens) {
		return false
	}
	for i := 0; i+len(want) <= len(tokens); i++ {
		match := true
		for j := range want {
			if tokens[i+j] != want[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// findPhrase returns the first signal of phrases present in tokens, or "".
func findPhrase(tokens []string, phrases []string) string {
	for _, p := range phrases {
		if containsPhrase(tokens, p) {
			return p
		}
	}
	return ""
}

// ContainsPhrase reports whether text carries any of the given phrases as whole
// tokens. It is the canonical word-boundary matcher of the semantic layer; the
// objective contract layer uses it instead of its own padded-substring scan.
func ContainsPhrase(text string, phrases []string) bool {
	tokens := tokenize(text)
	for _, p := range phrases {
		if containsPhrase(tokens, p) {
			return true
		}
	}
	return false
}

// ── Clause decomposition ────────────────────────────────────────────────────
//
// clauseSeparators are the coordinating tokens that separate one act from
// another in a request. Splitting on them is what lets "review this project AND
// implement the improvements" be read as two acts rather than one
// undifferentiated string scanned by a priority-ordered list — which is how a
// read-only first clause could silently outvote a mutation second clause.
var clauseSeparators = map[string]bool{
	"and": true, "then": true, "also": true, "plus": true, "but": true,
	"after": true, "first": true, "finally": true,
}

// splitClauses decomposes a request into acts. It is deterministic and total:
// a request with no separator is one clause, and an empty request is no clause.
func splitClauses(text string) []string {
	tokens := tokenize(text)
	var clauses []string
	var current []string
	flush := func() {
		if len(current) > 0 {
			clauses = append(clauses, strings.Join(current, " "))
			current = current[:0]
		}
	}
	for _, t := range tokens {
		if clauseSeparators[t] {
			flush()
			continue
		}
		current = append(current, t)
	}
	flush()
	return clauses
}

// readClause reads one clause for its speech act.
//
// Precedence inside a clause is EXECUTIVE > INSPECT > ADVISE, because a clause
// that both looks at the workspace and directs a change to it is a mutation —
// "check @index.html and remove extra contents" is a DELETE, not an inspection.
// The advisory table is consulted LAST so an advisory noun inside a change clause
// ("apply the refactor suggestions") cannot demote an executive act.
func readClause(text string) ClauseReading {
	tokens := tokenize(text)
	reading := ClauseReading{Text: text}

	// EXECUTIVE is read from the COMPLETE change vocabulary, not only the
	// operation-family tables. The families pick a strategy; this answers
	// whether the workspace is being asked to change at all, and a verb the
	// families do not carry ("change", "modify", "delete", "fix") is still a
	// mutation verb. Treating those as unreadable would let a plainly mutating
	// request reach the clarification path instead of the gated one.
	for _, table := range [][]string{modificationVerbs, refactoringVerbs, architecturalSignals, creationVerbs} {
		if s := findPhrase(tokens, table); s != "" {
			reading.Act, reading.Signal = ActExecutive, s
			return reading
		}
	}
	// Every read-only signal the engine has: evidence collection, root cause,
	// verification and design advice. A request that only asks to be shown
	// something is READ-ONLY work, never a clarification — routing it to the
	// clarification path would answer "inspect the repository" by asking the
	// human what to change.
	for _, table := range [][]string{inspectionVerbs, diagnosticSignals, explainSignals, planningVerbs} {
		if s := findPhrase(tokens, table); s != "" {
			reading.Act, reading.Signal = ActInspect, s
			return reading
		}
	}
	if s := findPhrase(tokens, advisorySignals); s != "" {
		reading.Act, reading.Signal = ActAdvise, s
		return reading
	}
	reading.Act = ActUnread
	return reading
}

// ── The classification pass ─────────────────────────────────────────────────

// ClassifySemantic is the canonical semantic classification of a request. It is
// PURE and TOTAL: the same request always yields the same verdict, and every
// request — including the empty one — yields one of the three SemanticIntents.
//
// The rule, in full:
//
//  1. Any clause with an EXECUTIVE act makes the request MUTATION. A mutation
//     act anywhere in the request is the user asking for a change.
//  2. Otherwise, any INSPECT or ADVISE clause makes the request READ-ONLY.
//  3. Otherwise the request is UNDETERMINED. It is not an error and not a
//     mutation: the runtime will not guess what change a goal implies.
//
// It deliberately does NOT read readOnlyConstraintPhrases. That table answers a
// different question — "did a human decline a write?" — and answering it here
// would make the classifier unsafe to call on runtime-composed text. The
// decomposition prompts carry the runtime's own scoping instructions, among
// them "do not modify any other region"; a classifier that treated those as a
// human's negation would downgrade an authorized mutation to read-only. So the
// negation is a SEPARATE, explicitly-called predicate (StatesReadOnlyConstraint)
// consulted only where the input is known to be human text.
func ClassifySemantic(raw string) SemanticVerdict {
	verdict := SemanticVerdict{Intent: SemanticUndetermined}

	var executive, nonExecutive *ClauseReading
	for _, c := range splitClauses(raw) {
		r := readClause(c)
		verdict.Clauses = append(verdict.Clauses, r)
		switch r.Act {
		case ActExecutive:
			if executive == nil {
				executive = &verdict.Clauses[len(verdict.Clauses)-1]
			}
		case ActInspect, ActAdvise:
			if nonExecutive == nil {
				nonExecutive = &verdict.Clauses[len(verdict.Clauses)-1]
			}
		}
	}

	if executive != nil {
		verdict.Intent = SemanticMutation
		verdict.Operation = OperationContent
		verdict.Reason = "clause \"" + executive.Text + "\" directs a workspace change (signal \"" +
			executive.Signal + "\"); this is semantic mutation INTENT only and confers no authority"
		return verdict
	}
	if nonExecutive != nil {
		verdict.Intent = SemanticReadOnly
		verdict.Operation = OperationExplain
		verdict.Reason = "no clause directs a workspace change; \"" + nonExecutive.Text +
			"\" asks the runtime to " + string(nonExecutive.Act) + " (signal \"" +
			nonExecutive.Signal + "\")"
		return verdict
	}
	if len(verdict.Clauses) == 0 {
		verdict.Reason = "the request is empty; there is nothing to classify"
		return verdict
	}
	verdict.Reason = "no clause of the request states a workspace act the runtime may act on; " +
		"the goal is stated but the change it implies is not, so the runtime will not guess one"
	return verdict
}

// IsCasualPrompt reports whether the request is casual chat rather than a coding
// request. Such a request never claimed to be about the workspace, so it is
// exempt from the undetermined-intent clarification: "hi" says nothing about
// the workspace BECAUSE it is not about the workspace, and asking the human
// what to change would be answering the wrong question.
func IsCasualPrompt(raw string) bool { return gateway.IsCasualChat(raw) }
