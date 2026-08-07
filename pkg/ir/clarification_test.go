package ir

import (
	"encoding/json"
	"reflect"
	"testing"
)

// sampleQuestions builds a two-question ambiguous intent fixture shared by the
// clarification tests.
func sampleQuestions() []ClarificationQuestion {
	return []ClarificationQuestion{
		{
			ID:           "workspace-conflict",
			Header:       "Workspace Conflict Detected",
			QuestionText: "How should I proceed over the existing todo_app workspace?",
			Options: []QuestionOption{
				{ID: OptionReplaceWorkspace, Label: "Completely replace workspace with portfolio", Description: "Discards the existing todo_app files"},
				{ID: OptionBuildAlongside, Label: "Build portfolio alongside", Description: "Keeps the current files"},
				{ID: OptionMergeSelective, Label: "Merge selectively", Description: "Keeps the relevant parts", IsDefault: true},
				NewCustomAnswerOption(),
			},
			Reason: "requested portfolio over an existing todo_app workspace",
		},
		{
			ID:           "stack-choice",
			Header:       "Stack Choice",
			QuestionText: "Which technology stack should the portfolio use?",
			Options: []QuestionOption{
				{ID: "vanilla", Label: "Vanilla HTML/CSS/JS", Description: "No framework, smallest footprint"},
				{ID: "react", Label: "React", Description: "Component-based, needs a build step"},
			},
		},
	}
}

func TestQuestionOptionJSONRoundTrip(t *testing.T) {
	opt := QuestionOption{
		ID:          OptionReplaceWorkspace,
		Label:       "Completely replace workspace with portfolio",
		Description: "Discards the existing todo_app files",
		IsDefault:   true,
	}
	data, err := json.Marshal(opt)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back QuestionOption
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(back, opt) {
		t.Errorf("round trip = %+v, want %+v", back, opt)
	}
}

func TestQuestionJSONRoundTrip(t *testing.T) {
	q := sampleQuestions()[0]
	data, err := json.Marshal(q)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back ClarificationQuestion
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(back, q) {
		t.Errorf("round trip = %+v, want %+v", back, q)
	}
}

func TestQuestionDefaultOptionID(t *testing.T) {
	qs := sampleQuestions()
	if got := qs[0].DefaultOptionID(); got != OptionMergeSelective {
		t.Errorf("DefaultOptionID = %q, want merge_selective", got)
	}
	if got := qs[1].DefaultOptionID(); got != "vanilla" {
		t.Errorf("DefaultOptionID = %q, want first option vanilla", got)
	}
	if got := (ClarificationQuestion{}).DefaultOptionID(); got != "" {
		t.Errorf("DefaultOptionID on empty question = %q, want empty", got)
	}
}

func TestQuestionIsAnswered(t *testing.T) {
	q := sampleQuestions()[0]
	if q.IsAnswered() {
		t.Error("fresh question must not be answered")
	}
	q.SelectedOption = OptionReplaceWorkspace
	if !q.IsAnswered() {
		t.Error("question with a selection must be answered")
	}
}

func TestCustomAnswerOptionHelpers(t *testing.T) {
	opt := NewCustomAnswerOption()
	if opt.ID != OptionTypeYourOwn {
		t.Errorf("custom option ID = %q, want type_your_own", opt.ID)
	}
	if !IsCustomAnswerOption(opt) {
		t.Error("custom option not recognized as custom")
	}
	if IsCustomAnswerOption(QuestionOption{ID: "react"}) {
		t.Error("preset option misclassified as custom")
	}
}

func TestDefaultAnswers(t *testing.T) {
	qs := sampleQuestions()
	answers := DefaultAnswers(qs)
	if len(answers) != 2 {
		t.Fatalf("answers = %d, want 2", len(answers))
	}
	if answers[0].QuestionID != "workspace-conflict" || answers[0].OptionID != OptionMergeSelective {
		t.Errorf("answers[0] = %+v, want workspace-conflict/merge_selective", answers[0])
	}
	if answers[1].QuestionID != "stack-choice" || answers[1].OptionID != "vanilla" {
		t.Errorf("answers[1] = %+v, want stack-choice/vanilla", answers[1])
	}
}

func TestApplyAnswers(t *testing.T) {
	qs := sampleQuestions()
	answers := []ClarificationAnswer{
		{QuestionID: "workspace-conflict", OptionID: OptionReplaceWorkspace},
		{QuestionID: "stack-choice", OptionID: OptionTypeYourOwn, CustomAnswer: "SvelteKit"},
	}
	got := ApplyAnswers(qs, answers)

	// The source slice must remain untouched.
	if qs[0].SelectedOption != "" {
		t.Error("ApplyAnswers mutated the input questions")
	}
	if got[0].SelectedOption != OptionReplaceWorkspace {
		t.Errorf("got[0].SelectedOption = %q, want replace_workspace", got[0].SelectedOption)
	}
	if got[1].SelectedOption != OptionTypeYourOwn || got[1].CustomAnswer != "SvelteKit" {
		t.Errorf("got[1] = %+v, want custom answer SvelteKit", got[1])
	}

	// Unknown question IDs are ignored, known ones preserved.
	got = ApplyAnswers(qs, []ClarificationAnswer{{QuestionID: "nope", OptionID: "x"}})
	if got[0].SelectedOption != "" {
		t.Error("unmatched answer leaked onto a question")
	}
	if len(got) != len(qs) {
		t.Errorf("ApplyAnswers changed question count: %d -> %d", len(qs), len(got))
	}
}

func TestClarificationResponseJSON(t *testing.T) {
	resp := ClarificationResponse{Answers: []ClarificationAnswer{
		{QuestionID: "workspace-conflict", OptionID: OptionBuildAlongside},
	}}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back ClarificationResponse
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(back, resp) {
		t.Errorf("round trip = %+v, want %+v", back, resp)
	}
}

func TestWellKnownOptionIDsAreStable(t *testing.T) {
	got := map[string]string{
		OptionReplaceWorkspace: "replace_workspace",
		OptionBuildAlongside:   "build_alongside",
		OptionMergeSelective:   "merge_selective",
		OptionTypeYourOwn:      "type_your_own",
	}
	for id, want := range got {
		if id != want {
			t.Errorf("constant %q drifted from its label %q", id, want)
		}
	}
}
