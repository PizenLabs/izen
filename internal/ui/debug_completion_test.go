package ui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDebugEnabled(t *testing.T) {
	cases := map[string]bool{
		"1":     true,
		"true":  true,
		"TRUE":  false,
		"0":     false,
		"false": false,
		"":      false,
		"yes":   false,
	}
	for val, want := range cases {
		t.Setenv("IZEN_DEBUG", val)
		if got := debugEnabled(); got != want {
			t.Errorf("debugEnabled() with IZEN_DEBUG=%q = %v, want %v", val, got, want)
		}
	}
}

func TestDebugLogCompletionDisabled(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(wd) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	t.Setenv("IZEN_DEBUG", "")
	debugLogCompletion("hello", 1, 2, "stop", "test")

	if _, err := os.Stat(filepath.Join(".izen", "debug", "completions.log")); !os.IsNotExist(err) {
		t.Errorf("expected completions.log to be absent when IZEN_DEBUG is unset, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(".izen", "debug")); !os.IsNotExist(err) {
		t.Errorf("expected .izen/debug dir to be absent when IZEN_DEBUG is unset, stat err = %v", err)
	}
}

func TestDebugLogCompletionEnabled(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(wd) }()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	t.Setenv("IZEN_DEBUG", "1")
	debugLogCompletion("hello", 1, 2, "stop", "test")
	debugLogCompletion("world", 3, 4, "length", "test")

	data, err := os.ReadFile(filepath.Join(".izen", "debug", "completions.log"))
	if err != nil {
		t.Fatalf("expected completions.log to exist when IZEN_DEBUG=1: %v", err)
	}
	lines := 0
	for _, b := range data {
		if b == '\n' {
			lines++
		}
	}
	if lines != 2 {
		t.Errorf("expected 2 log lines, got %d", lines)
	}
}
