package target

import (
	"reflect"
	"testing"
)

func TestExtractReferences_StandardRefs(t *testing.T) {
	cases := []struct {
		name   string
		prompt string
		want   []string
	}{
		{
			name:   "single ref",
			prompt: "check this file @index.html and rewrite it",
			want:   []string{"index.html"},
		},
		{
			name:   "nested path",
			prompt: "migrate @src/internal/auth.go to the new structure",
			want:   []string{"src/internal/auth.go"},
		},
		{
			name:   "multiple refs",
			prompt: "update @README.md and @docs/intro.md please",
			want:   []string{"README.md", "docs/intro.md"},
		},
		{
			name:   "dedup preserves first appearance",
			prompt: "fix @auth.go then @auth.go again",
			want:   []string{"auth.go"},
		},
		{
			name:   "hyphen and underscore",
			prompt: "edit @my-pkg/my_file.tsx now",
			want:   []string{"my-pkg/my_file.tsx"},
		},
		{
			name:   "no refs",
			prompt: "just a plain prompt with no targets",
			want:   nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractReferencePaths(tc.prompt)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ExtractReferencePaths(%q) = %v, want %v", tc.prompt, got, tc.want)
			}
		})
	}
}

func TestExtractReferences_QuotedRefs(t *testing.T) {
	cases := []struct {
		name   string
		prompt string
		want   []string
	}{
		{
			name:   "quoted path with spaces",
			prompt: `rewrite @"src/my component.tsx" with new props`,
			want:   []string{"src/my component.tsx"},
		},
		{
			name:   "quoted and standard mixed",
			prompt: `update @index.html and @"src/my component.tsx" together`,
			want:   []string{"index.html", "src/my component.tsx"},
		},
		{
			name:   "quoted path dedup",
			prompt: `fix @"src/my component.tsx" and @"src/my component.tsx" again`,
			want:   []string{"src/my component.tsx"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractReferencePaths(tc.prompt)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ExtractReferencePaths(%q) = %v, want %v", tc.prompt, got, tc.want)
			}
		})
	}
}

func TestExtractReferences_QuotedVsStandardIdentical(t *testing.T) {
	// Core requirement: quoted and standard references to the same path
	// must resolve identically. This test pins that invariant.
	standard := "src/internal/auth.go"
	promptStandard := "fix @" + standard
	promptQuoted := `fix @"` + standard + `"`

	gotStandard := ExtractReferencePaths(promptStandard)
	gotQuoted := ExtractReferencePaths(promptQuoted)

	if !reflect.DeepEqual(gotStandard, gotQuoted) {
		t.Fatalf("quoted and standard refs diverge: standard=%v quoted=%v", gotStandard, gotQuoted)
	}
	if len(gotStandard) != 1 || gotStandard[0] != standard {
		t.Fatalf("expected [%q], got %v", standard, gotStandard)
	}
}

func TestExtractReferences_ReferenceMetadata(t *testing.T) {
	prompt := `update @index.html and @"src/my component.tsx" please`
	refs := ExtractReferences(prompt)
	if len(refs) != 2 {
		t.Fatalf("expected 2 references, got %d", len(refs))
	}
	if refs[0].Raw != "index.html" || refs[0].Quoted {
		t.Errorf("ref[0] = {Raw:%s Quoted:%v}, want index.html false", refs[0].Raw, refs[0].Quoted)
	}
	if refs[1].Raw != "src/my component.tsx" || !refs[1].Quoted {
		t.Errorf("ref[1] = {Raw:%s Quoted:%v}, want 'src/my component.tsx' true", refs[1].Raw, refs[1].Quoted)
	}
}

func TestExtractReferences_SkipsEmptyAndSlash(t *testing.T) {
	got := ExtractReferencePaths("check @ and @/ and @file.go")
	want := []string{"file.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
