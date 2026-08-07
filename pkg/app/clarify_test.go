package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PizenLabs/izen/pkg/event"
	"github.com/PizenLabs/izen/pkg/ir"
)

// ambiguousIntent builds a create-portfolio intent flagged ambiguous over an
// existing todo workspace.
func ambiguousIntent() *ir.IntentIR {
	return &ir.IntentIR{
		Category:          ir.CategoryCreate,
		TargetType:        "portfolio",
		PreserveWorkspace: true,
		DecisionAmbiguity: true,
		ClarificationQuestions: []ir.ClarificationQuestion{{
			ID:           "workspace-conflict",
			Header:       "Workspace Conflict Detected",
			QuestionText: "Your request targets a portfolio, but this workspace is a todo_app workspace. How should I proceed?",
			Options: []ir.QuestionOption{
				{ID: ir.OptionReplaceWorkspace, Label: "Completely replace workspace with portfolio", Description: "Discards the existing todo_app files"},
				{ID: ir.OptionBuildAlongside, Label: "Build alongside", Description: "Keeps the current files"},
				{ID: ir.OptionMergeSelective, Label: "Merge selectively", Description: "Keeps the relevant parts", IsDefault: true},
				ir.NewCustomAnswerOption(),
			},
		}},
	}
}

func answer(questionID, optionID string) ir.ClarificationResponse {
	return ir.ClarificationResponse{Answers: []ir.ClarificationAnswer{{QuestionID: questionID, OptionID: optionID}}}
}

// channelClarifier blocks until the test feeds a reply, then forwards it to
// the pipeline. It closes invoked the moment it is called.
type channelClarifier struct {
	invoked chan struct{}
	reply   chan ir.ClarificationResponse
}

func (c *channelClarifier) Clarify(ctx context.Context, _ []ir.ClarificationQuestion, out chan<- ir.ClarificationResponse) error {
	close(c.invoked)
	select {
	case r := <-c.reply:
		out <- r
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// todoWorkspaceDir materialises a recognisable brownfield To-Do App workspace.
func todoWorkspaceDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	body := `<html><head><title>Todo App</title></head><body>
<div><input id="newTodo" placeholder="Add a task"></div>
<div id="taskList"></div></body></html>`
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestPipelineBlocksUntilClarificationResolves(t *testing.T) {
	root := todoWorkspaceDir(t)
	gen := &scriptedGenerator{resp: []string{fenced("html", "index.html", portfolioPage)}}
	cl := &channelClarifier{invoked: make(chan struct{}), reply: make(chan ir.ClarificationResponse, 1)}
	p := mustPipeline(t, WithRoot(root), WithGenerator(gen), WithClarifier(cl))
	events := collectBusEvents(t, p.Bus())

	type outcome struct {
		res *Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := p.Run(t.Context(), Request{Intent: "create a portfolio website", IntentIR: ambiguousIntent()})
		done <- outcome{res, err}
	}()

	// The clarifier must be invoked, which proves the pipeline reached the
	// gate and is now blocked on the response channel.
	select {
	case <-cl.invoked:
	case <-time.After(3 * time.Second):
		t.Fatal("pipeline never invoked the clarifier")
	}

	// While blocked, the pipeline must not have progressed to generation.
	select {
	case <-done:
		t.Fatal("pipeline completed before the clarification was resolved")
	case <-time.After(50 * time.Millisecond):
	}

	// The TypeClarificationRequired event must have been dispatched.
	waitForEvent(t, events, event.TypeClarificationRequired, "pipeline")

	// Resolve: replace the workspace entirely.
	cl.reply <- answer("workspace-conflict", ir.OptionReplaceWorkspace)

	select {
	case o := <-done:
		if o.err != nil {
			t.Fatalf("Run after clarification: %v", o.err)
		}
		intent := o.res.IntentIR
		if intent.DecisionAmbiguity {
			t.Error("DecisionAmbiguity must be cleared after the gate")
		}
		if intent.PreserveWorkspace {
			t.Error("PreserveWorkspace must be false for a replace answer")
		}
		if intent.ClarificationQuestions[0].SelectedOption != ir.OptionReplaceWorkspace {
			t.Errorf("SelectedOption = %q, want replace_workspace", intent.ClarificationQuestions[0].SelectedOption)
		}
		// Replacing the workspace forces a greenfield one-shot write even
		// over the brownfield todo workspace.
		if o.res.Mode != ModeGreenfield {
			t.Errorf("mode = %s, want greenfield (replace)", o.res.Mode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pipeline did not resume after the clarification was fed")
	}
}

func TestPipelineClarificationPreservesWorkspaceAlongside(t *testing.T) {
	root := todoWorkspaceDir(t)
	gen := &scriptedGenerator{resp: []string{fenced("html", "index.html", portfolioPage)}}
	cl := &channelClarifier{invoked: make(chan struct{}), reply: make(chan ir.ClarificationResponse, 1)}
	p := mustPipeline(t, WithRoot(root), WithGenerator(gen), WithClarifier(cl),
		WithVerifyCommand(func(string) string { return "true" }))

	type outcome struct {
		res *Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := p.Run(t.Context(), Request{Intent: "create a portfolio website", IntentIR: ambiguousIntent()})
		done <- outcome{res, err}
	}()
	<-cl.invoked
	cl.reply <- answer("workspace-conflict", ir.OptionBuildAlongside)

	select {
	case o := <-done:
		if o.err != nil {
			t.Fatalf("Run after alongside answer: %v", o.err)
		}
		// An alongside answer preserves the workspace, so the brownfield
		// repair loop is kept over the todo workspace.
		if !o.res.IntentIR.PreserveWorkspace {
			t.Error("PreserveWorkspace must stay true for an alongside answer")
		}
		if o.res.Mode != ModeBrownfield {
			t.Errorf("mode = %s, want brownfield (preserve)", o.res.Mode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pipeline did not resume")
	}
}

func TestPipelineClarificationPreserveWorkspaceFlag(t *testing.T) {
	cl := &channelClarifier{invoked: make(chan struct{}), reply: make(chan ir.ClarificationResponse, 1)}
	p := mustPipeline(t, WithRoot(t.TempDir()), WithGenerator(&scriptedGenerator{resp: []string{fenced("html", "index.html", portfolioPage)}}), WithClarifier(cl))

	done := make(chan *Result, 1)
	errs := make(chan error, 1)
	go func() {
		res, err := p.Run(t.Context(), Request{Intent: "create a portfolio website", IntentIR: ambiguousIntent()})
		errs <- err
		done <- res
	}()
	<-cl.invoked
	// A custom answer (OptionTypeYourOwn) counts as preserve.
	cl.reply <- ir.ClarificationResponse{Answers: []ir.ClarificationAnswer{{
		QuestionID: "workspace-conflict", OptionID: ir.OptionTypeYourOwn, CustomAnswer: "rebuild but keep the data",
	}}}
	res := <-done
	if err := <-errs; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.IntentIR.PreserveWorkspace {
		t.Error("custom answer must preserve the workspace")
	}
	if res.IntentIR.ClarificationQuestions[0].CustomAnswer != "rebuild but keep the data" {
		t.Errorf("CustomAnswer = %q", res.IntentIR.ClarificationQuestions[0].CustomAnswer)
	}
}

func TestPipelineClarificationDefaultsWithoutClarifier(t *testing.T) {
	// No Clarifier wired: the ambiguous intent auto-resolves to defaults so a
	// headless run never hangs.
	root := t.TempDir()
	gen := &scriptedGenerator{resp: []string{fenced("html", "index.html", portfolioPage)}}
	p := mustPipeline(t, WithRoot(root), WithGenerator(gen))
	events := collectBusEvents(t, p.Bus())

	res, err := p.Run(t.Context(), Request{Intent: "create a portfolio website", IntentIR: ambiguousIntent()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IntentIR.DecisionAmbiguity {
		t.Error("DecisionAmbiguity must be cleared")
	}
	q := res.IntentIR.ClarificationQuestions[0]
	if q.SelectedOption != ir.OptionMergeSelective {
		t.Errorf("SelectedOption = %q, want merge_selective default", q.SelectedOption)
	}
	if !res.IntentIR.PreserveWorkspace {
		t.Error("default merge must preserve the workspace")
	}
	waitForEvent(t, events, event.TypeClarificationRequired, "pipeline")
}

func TestPipelineClarificationContextCancellation(t *testing.T) {
	// A clarifier that never replies must not leak: cancelling the context
	// unblocks the pipeline with the context error.
	cl := &channelClarifier{invoked: make(chan struct{}), reply: make(chan ir.ClarificationResponse)}
	p := mustPipeline(t, WithRoot(t.TempDir()), WithGenerator(&scriptedGenerator{}), WithClarifier(cl))

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := p.Run(ctx, Request{Intent: "create a portfolio website", IntentIR: ambiguousIntent()})
		done <- err
	}()
	<-cl.invoked
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pipeline did not unblock on context cancellation")
	}
}

func TestPipelineClarifierErrorDegradesToDefaults(t *testing.T) {
	cl := &channelClarifier{invoked: make(chan struct{}), reply: make(chan ir.ClarificationResponse, 1)}
	// A clarifier that errors out on every call.
	flaky := ClarifierFunc(func(context.Context, []ir.ClarificationQuestion, chan<- ir.ClarificationResponse) error {
		return errors.New("clarifier crashed")
	})
	p := mustPipeline(t, WithRoot(t.TempDir()), WithGenerator(&scriptedGenerator{resp: []string{fenced("html", "index.html", portfolioPage)}}), WithClarifier(flaky))
	events := collectBusEvents(t, p.Bus())

	res, err := p.Run(t.Context(), Request{Intent: "create a portfolio website", IntentIR: ambiguousIntent()})
	if err != nil {
		t.Fatalf("Run with failing clarifier: %v", err)
	}
	if res.IntentIR.ClarificationQuestions[0].SelectedOption != ir.OptionMergeSelective {
		t.Errorf("SelectedOption = %q, want default after clarifier failure", res.IntentIR.ClarificationQuestions[0].SelectedOption)
	}
	waitForEvent(t, events, event.TypeTaskFailed, "pipeline")
	_ = cl
}

func TestPipelineClarificationSkipsWhenNotAmbiguous(t *testing.T) {
	gen := &scriptedGenerator{resp: []string{fenced("html", "index.html", portfolioPage)}}
	p := mustPipeline(t, WithRoot(t.TempDir()), WithGenerator(gen))
	intent := &ir.IntentIR{Category: ir.CategoryCreate, TargetType: "portfolio", PreserveWorkspace: true}

	// Not ambiguous: no clarification event, plain run.
	events := collectBusEvents(t, p.Bus())
	res, err := p.Run(t.Context(), Request{Intent: "build a portfolio website", IntentIR: intent})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.IntentIR == nil {
		t.Fatal("Result.IntentIR must carry the request intent")
	}
	select {
	case e := <-events:
		if e.Type == event.TypeClarificationRequired {
			t.Fatal("clarification event dispatched for an unambiguous intent")
		}
	default:
	}
}

func TestPipelineClarificationDoesNotMutateCallerIntent(t *testing.T) {
	cl := &channelClarifier{invoked: make(chan struct{}), reply: make(chan ir.ClarificationResponse, 1)}
	p := mustPipeline(t, WithRoot(t.TempDir()), WithGenerator(&scriptedGenerator{resp: []string{fenced("html", "index.html", portfolioPage)}}), WithClarifier(cl))

	caller := ambiguousIntent()
	done := make(chan error, 1)
	go func() {
		_, err := p.Run(t.Context(), Request{Intent: "create a portfolio website", IntentIR: caller})
		done <- err
	}()
	<-cl.invoked
	cl.reply <- answer("workspace-conflict", ir.OptionReplaceWorkspace)
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The caller's intent is a read-only input: the gate reconciles a private
	// copy and must leave the original untouched.
	if !caller.DecisionAmbiguity {
		t.Error("caller's DecisionAmbiguity was cleared by the pipeline")
	}
	if caller.ClarificationQuestions[0].SelectedOption != "" {
		t.Error("caller's questions were mutated by the pipeline")
	}
}
