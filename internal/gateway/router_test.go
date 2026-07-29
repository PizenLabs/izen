package gateway

import (
	"testing"
)

func TestClassifyDirectMutation(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		wantFastTrack bool
		wantFile      string
		wantTaskType  string
	}{
		// ── TRIVIAL MUTATIONS: typo fixes ──────────────────────────────
		{
			name:          "$prompt fix typo in @README.md",
			input:         "$prompt fix typo in @README.md",
			wantFastTrack: true,
			wantFile:      "README.md",
			wantTaskType:  "FILE_MUTATE",
		},
		{
			name:          "/plan fix typo in @CONTRIBUTING.md",
			input:         "/plan fix typo in @CONTRIBUTING.md",
			wantFastTrack: true,
			wantFile:      "CONTRIBUTING.md",
			wantTaskType:  "FILE_MUTATE",
		},
		{
			name:          "fix typo in @CHANGELOG.md",
			input:         "fix typo in @CHANGELOG.md at line 42",
			wantFastTrack: true,
			wantFile:      "CHANGELOG.md",
			wantTaskType:  "FILE_MUTATE",
		},
		{
			name:          "fix spelling in README.md",
			input:         "fix spelling in README.md",
			wantFastTrack: true,
			wantFile:      "readme.md",
			wantTaskType:  "FILE_MUTATE",
		},

		// ── TRIVIAL MUTATIONS: rename on doc files ────────────────────
		{
			name:          "rename @LICENSE",
			input:         "rename @LICENSE to LICENSE.md",
			wantFastTrack: true,
			wantFile:      "LICENSE",
			wantTaskType:  "FILE_MUTATE",
		},
		{
			name:          "rename author in @LICENSE (via $prompt)",
			input:         "$prompt rename author in @LICENSE file into 'Jay JR'",
			wantFastTrack: true,
			wantFile:      "LICENSE",
			wantTaskType:  "FILE_MUTATE",
		},

		// ── TRIVIAL MUTATIONS: correct/format on doc files ───────────
		{
			name:          "correct @CHANGELOG.md",
			input:         "correct @CHANGELOG.md",
			wantFastTrack: true,
			wantFile:      "CHANGELOG.md",
			wantTaskType:  "FILE_MUTATE",
		},
		{
			name:          "capitalize @README.md",
			input:         "capitalize @README.md",
			wantFastTrack: true,
			wantFile:      "README.md",
			wantTaskType:  "FILE_MUTATE",
		},
		{
			name:          "format @.env",
			input:         "format @.env",
			wantFastTrack: true,
			wantFile:      ".env",
			wantTaskType:  "FILE_MUTATE",
		},

		// ── NOT fast-track: non-trivial mutations on doc files ───────
		{
			name:          "$prompt create MIT LICENSE",
			input:         "$prompt i want to create the MIT LICENSE with author named 'Maha JR' and the years 2026",
			wantFastTrack: false,
		},
		{
			name:          "$prompt generate @LICENSE",
			input:         "$prompt generate @LICENSE with Apache 2.0",
			wantFastTrack: false,
		},
		{
			name:          "$prompt write README.md",
			input:         "$prompt write README.md with project description",
			wantFastTrack: false,
		},
		{
			name:          "$prompt update @.env with new key",
			input:         "$prompt update @.env with new API key",
			wantFastTrack: false,
		},
		{
			name:          "/plan update README.md",
			input:         "/plan update README.md with install instructions",
			wantFastTrack: false,
		},
		{
			name:          "update @config.yml",
			input:         "update @config.yml debug to true",
			wantFastTrack: false,
		},
		{
			name:          "update LICENSE file",
			input:         "update LICENSE file with new year",
			wantFastTrack: false,
		},
		{
			name:          "add to @.gitignore",
			input:         "add *.log to @.gitignore",
			wantFastTrack: false,
		},
		{
			name:          "remove from @.editorconfig",
			input:         "remove indent_size from @.editorconfig",
			wantFastTrack: false,
		},
		{
			name:          "update @Dockerfile",
			input:         "update @Dockerfile to use golang 1.22",
			wantFastTrack: false,
		},
		{
			name:          "multiple doc files",
			input:         "update @README.md and @CHANGELOG.md",
			wantFastTrack: false,
		},

		// ── NOT fast-track: diagnostic intent ────────────────────────
		{
			name:          "diagnostic why is broken @README",
			input:         "why is @README.md not rendering correctly",
			wantFastTrack: false,
		},
		{
			name:          "diagnostic debug @config",
			input:         "debug @config.yml is not being loaded",
			wantFastTrack: false,
		},
		{
			name:          "diagnostic root cause",
			input:         "what is the root cause of the build failure",
			wantFastTrack: false,
		},

		// ── NOT fast-track: code files ───────────────────────────────
		{
			name:          "code file @main.go",
			input:         "fix the bug in @main.go",
			wantFastTrack: false,
		},
		{
			name:          "code file handler.go",
			input:         "fix undefined error in handler.go",
			wantFastTrack: false,
		},
		{
			name:          "code file @router.go",
			input:         "fix typo in @router.go",
			wantFastTrack: false,
		},

		// ── NOT fast-track: no mutation verb ────────────────────────
		{
			name:          "no verb just @LICENSE",
			input:         "what does @LICENSE say",
			wantFastTrack: false,
		},
		{
			name:          "no verb README.md",
			input:         "tell me about README.md",
			wantFastTrack: false,
		},

		// ── NOT fast-track: empty / edge cases ──────────────────────
		{
			name:          "empty input",
			input:         "",
			wantFastTrack: false,
		},
		{
			name:          "just prefix no content",
			input:         "$prompt",
			wantFastTrack: false,
		},
		{
			name:          "no verb no file",
			input:         "$prompt hello world",
			wantFastTrack: false,
		},
		{
			name:          "fix typo on code file (not doc)",
			input:         "fix typo in @main.go",
			wantFastTrack: false,
		},

		// ── NOT fast-track: multi-file ──────────────────────────────
		{
			name:          "mixed code and doc refs",
			input:         "update @main.go and @README.md",
			wantFastTrack: false,
		},

		// ── NOT fast-track: frontend UI tasks ────────────────────────
		{
			name:          "frontend UI - move nav",
			input:         "move navigation to the top of the webpage",
			wantFastTrack: false,
		},
		{
			name:          "frontend UI - fix css layout",
			input:         "fix the css layout for the header component",
			wantFastTrack: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target, got := ClassifyDirectMutation(tc.input)
			if got != tc.wantFastTrack {
				t.Errorf("ClassifyDirectMutation(%q) fastTrack = %v, want %v", tc.input, got, tc.wantFastTrack)
			}
			if tc.wantFastTrack {
				if target.File == "" {
					t.Errorf("ClassifyDirectMutation(%q) returned empty file for fast-track", tc.input)
				}
				if target.TaskType != tc.wantTaskType {
					t.Errorf("ClassifyDirectMutation(%q) TaskType = %q, want %q", tc.input, target.TaskType, tc.wantTaskType)
				}
				if target.Description != tc.input {
					t.Errorf("ClassifyDirectMutation(%q) Description = %q, want raw input preserved", tc.input, target.Description)
				}
			}
		})
	}
}

func TestClassifyDirectMutation_TrivialFileDetection(t *testing.T) {
	tests := []struct {
		name  string
		input string
		file  string
	}{
		{"typo fix on graphql", "fix typo in @schema.graphql", "schema.graphql"},
		{"rename proto file", "rename @api.proto to v2.proto", "api.proto"},
		{"correct toml file", "correct @config.toml", "config.toml"},
		{"format ini file", "format @settings.ini", "settings.ini"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target, ok := ClassifyDirectMutation(tc.input)
			if !ok {
				t.Errorf("ClassifyDirectMutation(%q) = false, want true", tc.input)
				return
			}
			if target.File != tc.file {
				t.Errorf("ClassifyDirectMutation(%q) file = %q, want %q", tc.input, target.File, tc.file)
			}
		})
	}
}

func TestClassifyDirectMutation_NoFalsePositives(t *testing.T) {
	inputs := []string{
		"why is the build failing",
		"investigate the crash in main.go",
		"debug the panic handler",
		"what caused the nil pointer",
		"the router is broken",
		"fix the bug",
		"compile error in src/main.go",
		"test is failing",
		"undefined symbol Log",
		"what does this code do",
		"move the sidebar to the left",
		"position header above navigation",
	}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			_, ok := ClassifyDirectMutation(input)
			if ok {
				t.Errorf("ClassifyDirectMutation(%q) = true, want false (no false positive for diagnostic)", input)
			}
		})
	}
}

func TestClassifyDirectMutation_TrivialVerbDetection(t *testing.T) {
	// These trivial verbs should still fast-track on doc files.
	trivialVerbs := []string{
		"rename",
		"format",
		"correct",
		"capitalize",
	}

	for _, v := range trivialVerbs {
		t.Run(v, func(t *testing.T) {
			input := v + " @README.md"
			target, ok := ClassifyDirectMutation(input)
			if !ok {
				t.Errorf("ClassifyDirectMutation(%q) = false, want true", input)
				return
			}
			if target.File != "README.md" {
				t.Errorf("ClassifyDirectMutation(%q) file = %q, want README.md", input, target.File)
			}
		})
	}
}

func TestClassifyDirectMutation_NonTrivialVerbDetection(t *testing.T) {
	// These verbs should NOT fast-track under the new strict policy.
	nonTrivialVerbs := []string{
		"update",
		"change",
		"modify",
		"replace",
		"set",
		"add",
		"remove",
		"delete",
		"bump",
		"create",
		"generate",
		"make",
		"write",
		"touch",
		"init",
	}

	for _, v := range nonTrivialVerbs {
		t.Run(v, func(t *testing.T) {
			input := v + " @README.md"
			_, ok := ClassifyDirectMutation(input)
			if ok {
				t.Errorf("ClassifyDirectMutation(%q) = true, want false (non-trivial verb should not fast-track)", input)
			}
		})
	}
}

func TestExtractDirectMutationTargets_SingleFile(t *testing.T) {
	target := ExtractDirectMutationTargets("create index.html for landing page")
	if len(target) != 1 {
		t.Fatalf("expected 1 target, got %d", len(target))
	}
	if target[0] != "index.html" {
		t.Errorf("expected index.html, got %q", target[0])
	}
}

func TestExtractDirectMutationTargets_MultiFile(t *testing.T) {
	target := ExtractDirectMutationTargets("create index.html, styles.css, script.js for static portfolio")
	if len(target) != 3 {
		t.Fatalf("expected 3 targets, got %d", len(target))
	}
	expected := []string{"index.html", "styles.css", "script.js"}
	for i, f := range expected {
		if target[i] != f {
			t.Errorf("target[%d] = %q, want %q", i, target[i], f)
		}
	}
}

func TestExtractDirectMutationTargets_AtRefs(t *testing.T) {
	target := ExtractDirectMutationTargets("update @index.html, @styles.css, @script.js")
	if len(target) != 3 {
		t.Fatalf("expected 3 targets, got %d", len(target))
	}
	expected := []string{"index.html", "styles.css", "script.js"}
	for i, f := range expected {
		if target[i] != f {
			t.Errorf("target[%d] = %q, want %q", i, target[i], f)
		}
	}
}

func TestExtractDirectMutationTargets_Empty(t *testing.T) {
	target := ExtractDirectMutationTargets("")
	if target != nil {
		t.Errorf("expected nil for empty input, got %v", target)
	}
}

func TestExtractDirectMutationTargets_NoMatch(t *testing.T) {
	target := ExtractDirectMutationTargets("investigate why the build fails")
	if len(target) != 0 {
		t.Errorf("expected no targets for diagnostic input, got %v", target)
	}
}

// ── New tests for REFORM features ─────────────────────────────────────────

func TestIsFrontendUI(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"move navigation to the top", true},
		{"fix the layout of the webpage", true},
		{"position the header above the nav", true},
		{"update the css for the sidebar", true},
		{"make the page responsive", true},
		{"fix typo in @README.md", false},
		{"rename @LICENSE", false},
		{"why is the build failing", false},
		{"implement the login handler", false},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			if got := IsFrontendUI(tc.input); got != tc.want {
				t.Errorf("IsFrontendUI(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestIsTrivialMutation(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		// Trivial mutations
		{"fix typo in @README.md", true},
		{"fix spelling in @CHANGELOG.md", true},
		{"rename @LICENSE", true},
		{"correct @config.yml", true},
		{"capitalize @README.md", true},
		{"format @.env", true},

		// Non-trivial: frontend UI tasks
		{"move navigation to top", false},
		{"fix the css layout", false},
		{"position header above nav", false},

		// Non-trivial: non-trivial verbs
		{"update @README.md", false},
		{"create @LICENSE", false},
		{"write @CHANGELOG.md", false},
		{"add *.log to @.gitignore", false},
		{"remove from @.editorconfig", false},
		{"delete @file.txt", false},

		// Non-trivial: code files
		{"fix typo in @main.go", false},

		// Non-trivial: diagnostic
		{"why is the build failing", false},
		{"investigate the crash", false},

		// Edge cases
		{"", false},
		{"hello world", false},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			if got := IsTrivialMutation(tc.input); got != tc.want {
				t.Errorf("IsTrivialMutation(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestClassifyIntentMode_FrontendUIRoutesToPlan(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// UI tasks → plan (never build)
		{"move navigation to the top", "plan"},
		{"fix the css layout for the header", "plan"},
		{"position the sidebar on the left", "plan"},
		{"make the webpage responsive", "plan"},

		// Trivial mutations → build
		{"fix typo in @README.md", "build"},
		{"rename @LICENSE", "build"},
		{"correct @CHANGELOG.md", "build"},

		// Diagnostic → investigate
		{"why is the build failing", "investigate"},
		{"investigate the crash in main.go", "investigate"},
		{"fix the bug in @handler.go", "investigate"},

		// Non-trivial mutation verbs → plan (new safer default)
		{"update @config.yml", "plan"},
		{"create @LICENSE", "plan"},
		{"write unit test for login", "plan"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			if got := ClassifyIntentMode(tc.input); got != tc.want {
				t.Errorf("ClassifyIntentMode(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
