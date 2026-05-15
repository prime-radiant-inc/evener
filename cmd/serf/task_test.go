package main

import (
	"strings"
	"testing"
)

func TestReadPromptFromArgsOrStdin_PrefersArgs(t *testing.T) {
	got := readPromptFromArgsOrStdin([]string{"hello", "world"}, false, strings.NewReader("ignored"), false)
	if got != "hello world" {
		t.Fatalf("prompt=%q, want %q", got, "hello world")
	}
}

func TestReadPromptFromArgsOrStdin_ResumeReadsStdinWhenPiped(t *testing.T) {
	got := readPromptFromArgsOrStdin(nil, false, strings.NewReader("repair prompt\n"), false)
	if got != "repair prompt" {
		t.Fatalf("prompt=%q, want %q", got, "repair prompt")
	}
}

func TestReadPromptFromArgsOrStdin_ResumeDoesNotReadWhenCharDevice(t *testing.T) {
	got := readPromptFromArgsOrStdin(nil, false, strings.NewReader("would block in real life"), true)
	if got != "" {
		t.Fatalf("prompt=%q, want empty", got)
	}
}

func TestReadPromptFromArgsOrStdin_ListSessionsDoesNotReadStdin(t *testing.T) {
	got := readPromptFromArgsOrStdin(nil, true, strings.NewReader("ignore"), false)
	if got != "" {
		t.Fatalf("prompt=%q, want empty", got)
	}
}
