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
		// ── $prompt prefix tests ──────────────────────────────────────
		{
			name:          "$prompt create MIT LICENSE",
			input:         "$prompt i want to create the MIT LICENSE with author named 'Maha JR' and the years 2026",
			wantFastTrack: true,
			wantFile:      "license",
			wantTaskType:  "FILE_MUTATE",
		},
		{
			name:          "$prompt generate @LICENSE",
			input:         "$prompt generate @LICENSE with Apache 2.0",
			wantFastTrack: true,
			wantFile:      "LICENSE",
			wantTaskType:  "FILE_MUTATE",
		},
		{
			name:          "$prompt write README.md",
			input:         "$prompt write README.md with project description",
			wantFastTrack: true,
			wantFile:      "readme.md",
			wantTaskType:  "FILE_MUTATE",
		},
		{
			name:          "rename author in @LICENSE",
			input:         "$prompt rename author in @LICENSE file into 'Jay JR'",
			wantFastTrack: true,
			wantFile:      "LICENSE",
			wantTaskType:  "FILE_MUTATE",
		},
		{
			name:          "$prompt fix typo in @README.md",
			input:         "$prompt fix typo in @README.md",
			wantFastTrack: true,
			wantFile:      "README.md",
			wantTaskType:  "FILE_MUTATE",
		},
		{
			name:          "$prompt update @.env with new key",
			input:         "$prompt update @.env with new API key",
			wantFastTrack: true,
			wantFile:      ".env",
			wantTaskType:  "FILE_MUTATE",
		},

		// ── /plan prefix tests ────────────────────────────────────────
		{
			name:          "/plan update README.md",
			input:         "/plan update README.md with install instructions",
			wantFastTrack: true,
			wantFile:      "readme.md",
			wantTaskType:  "FILE_MUTATE",
		},
		{
			name:          "/plan fix typo in @CONTRIBUTING.md",
			input:         "/plan fix typo in @CONTRIBUTING.md",
			wantFastTrack: true,
			wantFile:      "CONTRIBUTING.md",
			wantTaskType:  "FILE_MUTATE",
		},

		// ── Direct mutation verbs with @refs ─────────────────────────
		{
			name:          "rename @LICENSE",
			input:         "rename @LICENSE to LICENSE.md",
			wantFastTrack: true,
			wantFile:      "LICENSE",
			wantTaskType:  "FILE_MUTATE",
		},
		{
			name:          "update @config.yml",
			input:         "update @config.yml debug to true",
			wantFastTrack: true,
			wantFile:      "config.yml",
			wantTaskType:  "FILE_MUTATE",
		},
		{
			name:          "fix typo in @CHANGELOG.md",
			input:         "fix typo in @CHANGELOG.md at line 42",
			wantFastTrack: true,
			wantFile:      "CHANGELOG.md",
			wantTaskType:  "FILE_MUTATE",
		},

		// ── Bare filenames (no @ prefix) ─────────────────────────────
		{
			name:          "update LICENSE file",
			input:         "update LICENSE file with new year",
			wantFastTrack: true,
			wantFile:      "license",
			wantTaskType:  "FILE_MUTATE",
		},
		{
			name:          "fix spelling in README.md",
			input:         "fix spelling in README.md",
			wantFastTrack: true,
			wantFile:      "readme.md",
			wantTaskType:  "FILE_MUTATE",
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

		// ── Add / remove / delete on doc files ──────────────────────
		{
			name:          "add to @.gitignore",
			input:         "add *.log to @.gitignore",
			wantFastTrack: true,
			wantFile:      ".gitignore",
			wantTaskType:  "FILE_MUTATE",
		},
		{
			name:          "remove from @.editorconfig",
			input:         "remove indent_size from @.editorconfig",
			wantFastTrack: true,
			wantFile:      ".editorconfig",
			wantTaskType:  "FILE_MUTATE",
		},

		// ── Dockerfile support ──────────────────────────────────────
		{
			name:          "update @Dockerfile",
			input:         "update @Dockerfile to use golang 1.22",
			wantFastTrack: true,
			wantFile:      "Dockerfile",
			wantTaskType:  "FILE_MUTATE",
		},

		// ── Multiple @refs — only first file used ───────────────────
		{
			name:          "multiple doc files",
			input:         "update @README.md and @CHANGELOG.md",
			wantFastTrack: true,
			wantFile:      "README.md",
			wantTaskType:  "FILE_MUTATE",
		},

		// ── Mixed code + doc refs — NOT fast-track ─────────────────
		{
			name:          "mixed code and doc refs",
			input:         "update @main.go and @README.md",
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

func TestClassifyDirectMutation_FileDetection(t *testing.T) {
	tests := []struct {
		name  string
		input string
		file  string
	}{
		{"proto file", "update @api.proto", "api.proto"},
		{"graphql file", "fix typo in @schema.graphql", "schema.graphql"},
		{"sql file", "update @migration.sql", "migration.sql"},
		{"toml file", "change @config.toml", "config.toml"},
		{"ini file", "edit @settings.ini", "settings.ini"},
		{"svg file", "update @logo.svg", "logo.svg"},
		{"sh file", "fix @deploy.sh", "deploy.sh"},
		{"html file", "update @index.html", "index.html"},
		{"css file", "fix @style.css", "style.css"},
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

func TestClassifyDirectMutation_VerbDetection(t *testing.T) {
	verbs := []string{
		"rename",
		"update",
		"change",
		"modify",
		"replace",
		"set",
		"add",
		"remove",
		"delete",
		"bump",
		"format",
		"correct",
		"capitalize",
		"create",
		"generate",
		"make",
		"write",
		"touch",
		"init",
	}

	for _, v := range verbs {
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
