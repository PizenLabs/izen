package preflight

import (
	"errors"
	"strings"
	"testing"

	"github.com/PizenLabs/izen/pkg/runtime/context"
	"github.com/PizenLabs/izen/pkg/runtime/target"
)

// fakeResolver is a scriptable target.Resolver for isolating the preflight
// engine from real VCS/filesystem resolution.
type fakeResolver struct {
	ref *target.TargetRef
	err error
}

func (f *fakeResolver) Resolve(_ string, _ string) (*target.TargetRef, error) {
	return f.ref, f.err
}

// ref builds a TargetRef with the given identity fields.
func ref(canonical string, source target.ResolutionSource, tracked, exists bool) *target.TargetRef {
	return &target.TargetRef{
		Raw:       canonical,
		Canonical: canonical,
		Exists:    exists,
		Tracked:   tracked,
		Source:    source,
	}
}

// candidateUnits returns one unit per context kind with a bounded token cost.
func candidateUnits() []context.ContextUnit {
	return []context.ContextUnit{
		{ID: "target", Kind: context.KindTargetState, Source: "state", Content: "T", TokenCost: 10, Relevance: 0.9},
		{ID: "manifest", Kind: context.KindManifest, Source: "go.mod", Content: "M", TokenCost: 10, Relevance: 0.9},
		{ID: "topology", Kind: context.KindTopology, Source: "topo", Content: "P", TokenCost: 10, Relevance: 0.9},
		{ID: "source", Kind: context.KindSourceSnippet, Source: "snippet", Content: "S", TokenCost: 10, Relevance: 0.9},
	}
}

func TestRiskLevelString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		level RiskLevel
		want  string
	}{
		{RiskLow, "low"},
		{RiskMedium, "medium"},
		{RiskHigh, "high"},
		{RiskLevel(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.level.String(); got != tt.want {
			t.Errorf("RiskLevel(%d).String() = %q, want %q", tt.level, got, tt.want)
		}
	}
}

func TestPreflightRiskAssessment(t *testing.T) {
	t.Parallel()

	compiler := context.NewCompiler()

	tests := []struct {
		name string
		tr   *target.TargetRef
		want RiskLevel
	}{
		{
			name: "tracked go.mod is high risk",
			tr:   ref("go.mod", target.ResolutionVCS, true, true),
			want: RiskHigh,
		},
		{
			name: "untracked package.json is high risk",
			tr:   ref("package.json", target.ResolutionFilesystem, false, true),
			want: RiskHigh,
		},
		{
			name: "nested Makefile is high risk",
			tr:   ref("build/Makefile", target.ResolutionVCS, true, true),
			want: RiskHigh,
		},
		{
			name: "raw new file is high risk",
			tr:   ref("newfile.txt", target.ResolutionRaw, false, false),
			want: RiskHigh,
		},
		{
			name: "tracked source file is medium risk",
			tr:   ref("internal/app.go", target.ResolutionVCS, true, true),
			want: RiskMedium,
		},
		{
			name: "untracked filesystem source file is medium risk",
			tr:   ref("notes.txt", target.ResolutionFilesystem, false, true),
			want: RiskMedium,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			engine := NewEngine(&fakeResolver{ref: tt.tr}, compiler)
			got, err := engine.Execute(PreflightRequest{
				RawInput:       "update " + tt.tr.Canonical,
				WorkDir:        ".",
				TokenBudget:    1000,
				CandidateUnits: candidateUnits(),
			})
			if err != nil {
				t.Fatalf("Execute returned error: %v", err)
			}
			if got.Risk != tt.want {
				t.Errorf("risk = %v, want %v", got.Risk, tt.want)
			}
		})
	}
}

func TestPreflightContextOrchestration(t *testing.T) {
	t.Parallel()

	compiler := context.NewCompiler()
	units := candidateUnits()

	tests := []struct {
		name        string
		tr          *target.TargetRef
		wantDepth   context.ExpansionDepth
		wantRisk    RiskLevel
		wantKinds   []string
		notWantKind string
	}{
		{
			name:        "tracked match feeds deep confidence",
			tr:          ref("internal/app.go", target.ResolutionVCS, true, true),
			wantDepth:   context.DepthDeep,
			wantRisk:    RiskMedium,
			wantKinds:   []string{"target", "manifest", "topology", "source"},
			notWantKind: "",
		},
		{
			name:        "untracked match feeds conservative confidence",
			tr:          ref("notes.txt", target.ResolutionFilesystem, false, true),
			wantDepth:   context.DepthConservative,
			wantRisk:    RiskMedium,
			wantKinds:   []string{"target", "manifest"},
			notWantKind: "topology",
		},
		{
			name:        "raw target feeds minimal confidence",
			tr:          ref("newfile.txt", target.ResolutionRaw, false, false),
			wantDepth:   context.DepthMinimal,
			wantRisk:    RiskHigh,
			wantKinds:   []string{"target"},
			notWantKind: "manifest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			engine := NewEngine(&fakeResolver{ref: tt.tr}, compiler)
			got, err := engine.Execute(PreflightRequest{
				RawInput:       "modify " + tt.tr.Canonical,
				WorkDir:        ".",
				TokenBudget:    1000,
				CandidateUnits: units,
			})
			if err != nil {
				t.Fatalf("Execute returned error: %v", err)
			}

			if got.TargetRef != tt.tr {
				t.Errorf("TargetRef not propagated from resolver")
			}
			if got.Risk != tt.wantRisk {
				t.Errorf("risk = %v, want %v", got.Risk, tt.wantRisk)
			}
			if got.Context.Depth != tt.wantDepth {
				t.Errorf("depth = %v, want %v", got.Context.Depth, tt.wantDepth)
			}

			gotKinds := map[string]bool{}
			for _, u := range got.Context.Units {
				gotKinds[u.Kind.String()] = true
			}
			for _, want := range tt.wantKinds {
				if !gotKinds[want] {
					t.Errorf("depth %v missing kind %q in units %+v", tt.wantDepth, want, got.Context.Units)
				}
			}
			if tt.notWantKind != "" && gotKinds[tt.notWantKind] {
				t.Errorf("depth %v must not expose kind %q", tt.wantDepth, tt.notWantKind)
			}

			if got.Context.Intent != "modify "+tt.tr.Canonical {
				t.Errorf("compiled intent = %q, want %q", got.Context.Intent, "modify "+tt.tr.Canonical)
			}
			if got.Context.TotalTokens > got.Context.Budget {
				t.Errorf("compiled tokens %d exceed budget %d", got.Context.TotalTokens, got.Context.Budget)
			}
		})
	}
}

func TestPreflightBudgetConstraint(t *testing.T) {
	t.Parallel()

	engine := NewEngine(
		&fakeResolver{ref: ref("go.mod", target.ResolutionVCS, true, true)},
		context.NewCompiler(),
	)

	t.Run("negative budget propagates error", func(t *testing.T) {
		t.Parallel()
		_, err := engine.Execute(PreflightRequest{
			RawInput:    "update go.mod",
			WorkDir:     ".",
			TokenBudget: -1,
		})
		if err == nil {
			t.Fatal("expected error for negative token budget")
		}
	})

	t.Run("zero budget yields empty context", func(t *testing.T) {
		t.Parallel()
		got, err := engine.Execute(PreflightRequest{
			RawInput:       "update go.mod",
			WorkDir:        ".",
			TokenBudget:    0,
			CandidateUnits: candidateUnits(),
		})
		if err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		if len(got.Context.Units) != 0 || got.Context.TotalTokens != 0 {
			t.Errorf("expected empty context, got %d units / %d tokens", len(got.Context.Units), got.Context.TotalTokens)
		}
		if got.Context.Budget != 0 {
			t.Errorf("budget = %d, want 0", got.Context.Budget)
		}
	})
}

func TestPreflightPromptFormatting(t *testing.T) {
	t.Parallel()

	engine := NewEngine(
		&fakeResolver{ref: ref("internal/app.go", target.ResolutionVCS, true, true)},
		context.NewCompiler(),
	)

	got, err := engine.Execute(PreflightRequest{
		RawInput:       "add a & helper to <app>",
		WorkDir:        ".",
		TokenBudget:    1000,
		CandidateUnits: candidateUnits(),
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	prompt := got.FormattedPrompt
	if !strings.HasPrefix(prompt, "<izen_task>\n") || !strings.HasSuffix(prompt, "</izen_task>") {
		t.Errorf("prompt lacks task envelope:\n%s", prompt)
	}
	for _, want := range []string{
		`<instruction>add a &amp; helper to &lt;app&gt;</instruction>`,
		`<target_ref raw="internal/app.go" canonical="internal/app.go" tracked="true" exists="true" source="vcs"/>`,
		`<risk_level>medium</risk_level>`,
		`<compiled_context intent="add a &amp; helper to &lt;app&gt;"`,
		`</compiled_context>`,
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestPreflightExecuteErrors(t *testing.T) {
	t.Parallel()

	t.Run("nil engine", func(t *testing.T) {
		t.Parallel()
		var engine *PreflightEngine
		if _, err := engine.Execute(PreflightRequest{}); err == nil {
			t.Fatal("expected error for nil engine")
		}
	})

	t.Run("nil resolver", func(t *testing.T) {
		t.Parallel()
		engine := NewEngine(nil, context.NewCompiler())
		if _, err := engine.Execute(PreflightRequest{WorkDir: ".", RawInput: "x"}); err == nil {
			t.Fatal("expected error for nil resolver")
		}
	})

	t.Run("nil compiler", func(t *testing.T) {
		t.Parallel()
		engine := NewEngine(&fakeResolver{ref: ref("a.txt", target.ResolutionVCS, true, true)}, nil)
		if _, err := engine.Execute(PreflightRequest{WorkDir: ".", RawInput: "x"}); err == nil {
			t.Fatal("expected error for nil compiler")
		}
	})

	t.Run("empty workdir", func(t *testing.T) {
		t.Parallel()
		engine := NewEngine(&fakeResolver{}, context.NewCompiler())
		if _, err := engine.Execute(PreflightRequest{RawInput: "x"}); err == nil {
			t.Fatal("expected error for empty working directory")
		}
	})

	t.Run("empty raw input", func(t *testing.T) {
		t.Parallel()
		engine := NewEngine(&fakeResolver{}, context.NewCompiler())
		if _, err := engine.Execute(PreflightRequest{WorkDir: "."}); err == nil {
			t.Fatal("expected error for empty raw input")
		}
	})

	t.Run("resolver error propagates", func(t *testing.T) {
		t.Parallel()
		sentinel := errors.New("resolve exploded")
		engine := NewEngine(&fakeResolver{err: sentinel}, context.NewCompiler())
		_, err := engine.Execute(PreflightRequest{WorkDir: ".", RawInput: "x"})
		if err == nil || !strings.Contains(err.Error(), sentinel.Error()) {
			t.Fatalf("expected resolver error propagation, got %v", err)
		}
	})

	t.Run("nil resolver result rejected", func(t *testing.T) {
		t.Parallel()
		engine := NewEngine(&fakeResolver{ref: nil}, context.NewCompiler())
		if _, err := engine.Execute(PreflightRequest{WorkDir: ".", RawInput: "x"}); err == nil {
			t.Fatal("expected error for nil resolver result")
		}
	})
}
