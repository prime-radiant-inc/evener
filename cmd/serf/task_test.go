package main

import (
	"strings"
	"testing"
)

func TestReadTaskFromArgsOrStdin_PrefersArgs(t *testing.T) {
	got := readTaskFromArgsOrStdin([]string{"hello", "world"}, false, strings.NewReader("ignored"), false)
	if got != "hello world" {
		t.Fatalf("task=%q, want %q", got, "hello world")
	}
}

func TestReadTaskFromArgsOrStdin_ResumeReadsStdinWhenPiped(t *testing.T) {
	got := readTaskFromArgsOrStdin(nil, false, strings.NewReader("repair prompt\n"), false)
	if got != "repair prompt" {
		t.Fatalf("task=%q, want %q", got, "repair prompt")
	}
}

func TestReadTaskFromArgsOrStdin_ResumeDoesNotReadWhenCharDevice(t *testing.T) {
	got := readTaskFromArgsOrStdin(nil, false, strings.NewReader("would block in real life"), true)
	if got != "" {
		t.Fatalf("task=%q, want empty", got)
	}
}

func TestReadTaskFromArgsOrStdin_ListSessionsDoesNotReadStdin(t *testing.T) {
	got := readTaskFromArgsOrStdin(nil, true, strings.NewReader("ignore"), false)
	if got != "" {
		t.Fatalf("task=%q, want empty", got)
	}
}
