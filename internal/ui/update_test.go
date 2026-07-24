package ui

import (
	"strings"
	"testing"

	"github.com/PizenLabs/izen/internal/templates"
)

func TestRenderMITLicense(t *testing.T) {
	tests := []struct {
		name        string
		description string
		wantAuthor  string
		wantYear    string
	}{
		{
			name:        "quoted author with year",
			description: `create MIT LICENSE with author 'TOMATO' 2026`,
			wantAuthor:  "TOMATO",
			wantYear:    "2026",
		},
		{
			name:        "unquoted author after author keyword",
			description: "MIT LICENSE author TOMATO 2026",
			wantAuthor:  "TOMATO",
			wantYear:    "2026",
		},
		{
			name:        "no author uses git config default",
			description: "create MIT LICENSE",
			wantAuthor:  "",
			wantYear:    "2026",
		},
		{
			name:        "double quoted author",
			description: `create MIT license with author "Jane Doe" 2025`,
			wantAuthor:  "Jane Doe",
			wantYear:    "2025",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			content, ok := templates.RenderLicense("mit", tc.description)
			if !ok {
				t.Fatal("RenderLicense returned false for mit type")
			}
			if !strings.Contains(content, "MIT License") {
				t.Errorf("content missing MIT License header")
			}
			if !strings.Contains(content, tc.wantYear) {
				t.Errorf("content missing year %q", tc.wantYear)
			}
			if tc.wantAuthor != "" && !strings.Contains(content, tc.wantAuthor) {
				t.Errorf("content missing author %q\ncontent:\n%s", tc.wantAuthor, content)
			}
		})
	}
}

func TestGenerateTrivialContent(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		desc    string
		wantErr bool
	}{
		{"MIT LICENSE", "LICENSE", "MIT LICENSE author TOMATO 2026", false},
		{"lowercase license", "license", "create license", false},
		{".gitignore", ".gitignore", "create gitignore", false},
		{".env", ".env", "create env file", false},
		{"unknown returns empty", "main.go", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := generateTrivialContent(tc.target, tc.desc)
			if tc.wantErr && got != "" {
				t.Errorf("expected empty, got %q", got)
			}
			if !tc.wantErr && got == "" {
				t.Errorf("expected non-empty content")
			}
		})
	}
}

func TestIsYear(t *testing.T) {
	tests := []struct {
		s    string
		want bool
	}{
		{"2026", true},
		{"1999", true},
		{"2099", true},
		{"1800", false},
		{"0000", false},
		{"abc", false},
		{"20", false},
		{"", false},
	}
	for _, tc := range tests {
		t.Run(tc.s, func(t *testing.T) {
			got := isYear(tc.s)
			if got != tc.want {
				t.Errorf("isYear(%q) = %v, want %v", tc.s, got, tc.want)
			}
		})
	}
}

func TestGenerateTrivialContentCanonical(t *testing.T) {
	tests := []struct {
		name   string
		target string
		desc   string
	}{
		{"lowercase license", "license", "create license"},
		{"uppercase LICENSE", "LICENSE", "create LICENSE"},
		{"lowercase gitignore", "gitignore", "create gitignore"},
		{"uppercase .GITIGNORE", ".GITIGNORE", "create .gitignore"},
		{"lowercase env", "env", "create env"},
		{"uppercase .ENV", ".ENV", "create .env"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := generateTrivialContent(tc.target, tc.desc)
			if got == "" {
				t.Errorf("expected non-empty content for target %q", tc.target)
			}
		})
	}
}

func TestRenderLicenseFallback(t *testing.T) {
	_, ok := templates.RenderLicense("unknown-xyz", "some description")
	if ok {
		t.Error("expected false for unknown license type")
	}
}
