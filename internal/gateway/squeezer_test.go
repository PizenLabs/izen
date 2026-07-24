package gateway

import (
	"errors"
	"testing"

	"github.com/PizenLabs/izen/internal/ai"
)

func TestClassifyComplexity(t *testing.T) {
	tests := []struct {
		name  string
		input string
		files []string
		want  ComplexityTier
	}{
		{
			name:  "empty input",
			input: "",
			want:  TierUnknown,
		},
		{
			name:  "trivial create LICENSE",
			input: "create MIT LICENSE",
			files: []string{"LICENSE"},
			want:  TierTrivialCreate,
		},
		{
			name:  "trivial create .gitignore",
			input: "create .gitignore for Go project",
			files: []string{".gitignore"},
			want:  TierTrivialCreate,
		},
		{
			name:  "trivial create .env",
			input: "generate .env file",
			files: []string{".env"},
			want:  TierTrivialCreate,
		},
		{
			name:  "simple mutation rename author in LICENSE",
			input: "$prompt rename author in @LICENSE to 'Tomato'",
			want:  TierSimpleMutation,
		},
		{
			name:  "simple mutation fix typo in README",
			input: "fix typo in @README.md at line 10",
			want:  TierSimpleMutation,
		},
		{
			name:  "simple mutation capitalize heading",
			input: "capitalize heading in @README.md",
			want:  TierSimpleMutation,
		},
		{
			name:  "simple mutation bump version",
			input: "bump version to 1.2.3 in @version.txt",
			want:  TierSimpleMutation,
		},
		{
			name:  "complex build multi-file implementation",
			input: "implement user authentication with JWT",
			want:  TierComplexBuild,
		},
		{
			name:  "complex build code file change",
			input: "fix the bug in @main.go",
			files: []string{"main.go"},
			want:  TierComplexBuild,
		},
		{
			name:  "complex build diagnostic intent",
			input: "why is the build failing",
			want:  TierComplexBuild,
		},
		{
			name:  "complex build investigate crash",
			input: "investigate the crash in handler.go",
			want:  TierComplexBuild,
		},
		{
			name:  "complex build no verb",
			input: "what does @LICENSE say",
			want:  TierComplexBuild,
		},
		{
			name:  "complex build mixed code and doc refs",
			input: "update @main.go and @README.md",
			want:  TierComplexBuild,
		},
		{
			name:  "trivial via $prompt prefix",
			input: "$prompt create MIT LICENSE with author Tomato",
			files: []string{"license"},
			want:  TierTrivialCreate,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyComplexity(tc.input, tc.files)
			if got != tc.want {
				t.Errorf("ClassifyComplexity(%q, %v) = %v, want %v", tc.input, tc.files, got, tc.want)
			}
		})
	}
}

func TestClassifyComplexity_WithFilesExtraction(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  ComplexityTier
	}{
		{
			name:  "inline @ref LICENSE",
			input: "$prompt rename author in @LICENSE into 'TOMATO'",
			want:  TierSimpleMutation,
		},
		{
			name:  "inline bare license update",
			input: "update LICENSE with new year",
			want:  TierSimpleMutation,
		},
		{
			name:  "inline ref .gitignore",
			input: "add *.log to @.gitignore",
			want:  TierSimpleMutation,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyComplexity(tc.input, nil)
			if got != tc.want {
				t.Errorf("ClassifyComplexity(%q, nil) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestSqueeze_TrivialCreate(t *testing.T) {
	req := &ai.Request{}
	err := Squeeze(req, TierTrivialCreate)
	if err == nil {
		t.Fatal("Squeeze(TrivialCreate) expected error, got nil")
	}
	if !errors.Is(err, ErrTrivialCreate) {
		t.Errorf("Squeeze(TrivialCreate) error = %v, want ErrTrivialCreate", err)
	}
	var tcErr *TrivialCreateError
	if !errors.As(err, &tcErr) {
		t.Errorf("Squeeze(TrivialCreate) error type = %T, want *TrivialCreateError", err)
	}
}

func TestSqueeze_SimpleMutation(t *testing.T) {
	req := &ai.Request{}
	err := Squeeze(req, TierSimpleMutation)
	if err != nil {
		t.Fatalf("Squeeze(SimpleMutation) unexpected error: %v", err)
	}
	if req.MaxTokens != 150 {
		t.Errorf("MaxTokens = %d, want 150", req.MaxTokens)
	}
	if req.Temperature != 0.0 {
		t.Errorf("Temperature = %f, want 0.0", req.Temperature)
	}
	wantStop := []string{">>>>>>>", "```\n\n", "###"}
	if len(req.Stop) != len(wantStop) {
		t.Errorf("Stop = %v, want %v", req.Stop, wantStop)
	} else {
		for i := range wantStop {
			if req.Stop[i] != wantStop[i] {
				t.Errorf("Stop[%d] = %q, want %q", i, req.Stop[i], wantStop[i])
			}
		}
	}
}

func TestSqueeze_ComplexBuild(t *testing.T) {
	req := &ai.Request{}
	err := Squeeze(req, TierComplexBuild)
	if err != nil {
		t.Fatalf("Squeeze(ComplexBuild) unexpected error: %v", err)
	}
	if req.MaxTokens != 1500 {
		t.Errorf("MaxTokens = %d, want 1500", req.MaxTokens)
	}
	wantStop := []string{"```\n\n"}
	if len(req.Stop) != len(wantStop) || req.Stop[0] != wantStop[0] {
		t.Errorf("Stop = %v, want %v", req.Stop, wantStop)
	}
}

func TestSqueeze_Unknown(t *testing.T) {
	req := &ai.Request{
		MaxTokens:   4096,
		Stop:        []string{"original"},
		Temperature: 0.7,
	}
	err := Squeeze(req, TierUnknown)
	if err != nil {
		t.Fatalf("Squeeze(Unknown) unexpected error: %v", err)
	}
	if req.MaxTokens != 4096 {
		t.Errorf("MaxTokens changed from 4096 to %d", req.MaxTokens)
	}
	if len(req.Stop) != 1 || req.Stop[0] != "original" {
		t.Errorf("Stop changed to %v", req.Stop)
	}
	if req.Temperature != 0.7 {
		t.Errorf("Temperature changed to %f", req.Temperature)
	}
}

func TestClassifyCloudProvider(t *testing.T) {
	tests := []struct {
		name      string
		provider  string
		wantLocal bool
		wantCloud string
	}{
		{
			name:      "ollama is local",
			provider:  "ollama",
			wantLocal: true,
			wantCloud: "",
		},
		{
			name:      "openai is cloud",
			provider:  "openai",
			wantLocal: false,
			wantCloud: "openai",
		},
		{
			name:      "anthropic is cloud",
			provider:  "anthropic",
			wantLocal: false,
			wantCloud: "anthropic",
		},
		{
			name:      "openrouter is cloud",
			provider:  "openrouter",
			wantLocal: false,
			wantCloud: "openrouter",
		},
		{
			name:      "generative language googleapis is cloud",
			provider:  "gemini",
			wantLocal: false,
			wantCloud: "gemini",
		},
		{
			name:      "groq is cloud",
			provider:  "groq",
			wantLocal: false,
			wantCloud: "groq",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := ClassifyCloudProvider(tc.provider)
			if cfg.IsLocal != tc.wantLocal {
				t.Errorf("IsLocal = %v, want %v", cfg.IsLocal, tc.wantLocal)
			}
			if cfg.CloudProvider != tc.wantCloud {
				t.Errorf("CloudProvider = %q, want %q", cfg.CloudProvider, tc.wantCloud)
			}
		})
	}
}

func TestIntentClassifyRequest(t *testing.T) {
	req := IntentClassifyRequest("fix typo in README.md", "gpt-4o-mini")
	if req.Model != "gpt-4o-mini" {
		t.Errorf("Model = %q, want gpt-4o-mini", req.Model)
	}
	if req.MaxTokens != 30 {
		t.Errorf("MaxTokens = %d, want 30", req.MaxTokens)
	}
	if req.Temperature != 0.0 {
		t.Errorf("Temperature = %f, want 0.0", req.Temperature)
	}
	if len(req.Messages) != 1 {
		t.Errorf("Messages = %d, want 1", len(req.Messages))
	}
	if req.Messages[0].Content != "fix typo in README.md" {
		t.Errorf("Message content = %q", req.Messages[0].Content)
	}
	if req.System != ai.IntentClassifyPrompt() {
		t.Errorf("System prompt mismatch")
	}
}

func TestSimpleMutationPrompt(t *testing.T) {
	p := ai.SimpleMutationPrompt()
	if p == "" {
		t.Fatal("SimpleMutationPrompt() returned empty string")
	}
	if !contains(p, "SEARCH") {
		t.Error("SimpleMutationPrompt() missing SEARCH")
	}
	if !contains(p, "REPLACE") {
		t.Error("SimpleMutationPrompt() missing REPLACE")
	}
	if !contains(p, "<<<<<<<") {
		t.Error("SimpleMutationPrompt() missing <<<<<<<")
	}
}

func TestIntentClassifyPrompt(t *testing.T) {
	p := ai.IntentClassifyPrompt()
	if p == "" {
		t.Fatal("IntentClassifyPrompt() returned empty string")
	}
	if !contains(p, "MUTATE") {
		t.Error("IntentClassifyPrompt() missing MUTATE")
	}
	if !contains(p, "DIAGNOSE") {
		t.Error("IntentClassifyPrompt() missing DIAGNOSE")
	}
}

func TestComplexityTierString(t *testing.T) {
	tests := []struct {
		tier ComplexityTier
		want string
	}{
		{TierUnknown, "UNKNOWN"},
		{TierTrivialCreate, "TRIVIAL_CREATE"},
		{TierSimpleMutation, "SIMPLE_MUTATION"},
		{TierComplexBuild, "COMPLEX_BUILD"},
		{ComplexityTier(99), "UNKNOWN"},
	}
	for _, tc := range tests {
		if got := tc.tier.String(); got != tc.want {
			t.Errorf("ComplexityTier(%d).String() = %q, want %q", tc.tier, got, tc.want)
		}
	}
}

func TestIsTrivialCreateTarget(t *testing.T) {
	tests := []struct {
		name string
		file string
		want bool
	}{
		{"empty", "", false},
		{"LICENSE upper", "LICENSE", true},
		{"license lower", "license", true},
		{"LICENCE variant", "LICENCE", true},
		{".gitignore", ".gitignore", true},
		{"gitignore bare", "gitignore", true},
		{".env", ".env", true},
		{"env bare", "env", true},
		{".env.example", ".env.example", true},
		{"main.go code file", "main.go", false},
		{"README.md", "README.md", false},
		{"Dockerfile", "Dockerfile", false},
		{"Makefile", "Makefile", false},
		{"path/to/LICENSE", "path/to/LICENSE", true},
		{"path/.gitignore", "path/.gitignore", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsTrivialCreateTarget(tc.file)
			if got != tc.want {
				t.Errorf("IsTrivialCreateTarget(%q) = %v, want %v", tc.file, got, tc.want)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsStr(s, substr)
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
