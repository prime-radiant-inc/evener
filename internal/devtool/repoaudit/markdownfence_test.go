package repoaudit

import (
	"strings"
	"testing"
)

func TestMarkdownFencesTreatsAnInfoBearingLineAsContent(t *testing.T) {
	doc := strings.Join([]string{
		"# Recipe",
		"",
		"```sh",
		"evener-dev hub start",
		"```bash",
		"evener-dev hub stop",
		"```",
		"",
		"after",
	}, "\n")

	fences, err := MarkdownFences(doc)
	if err != nil {
		t.Fatalf("MarkdownFences: %v", err)
	}
	if len(fences) != 1 {
		t.Fatalf("got %d fences, want 1: %+v", len(fences), fences)
	}
	if got, want := strings.Join(fences[0].Lines, "|"), "evener-dev hub start|```bash|evener-dev hub stop"; got != want {
		t.Errorf("block content = %q, want %q", got, want)
	}
	if fences[0].Lang != "sh" || fences[0].LineNo != 3 {
		t.Errorf("fence = %+v, want sh at line 3", fences[0])
	}
}

func TestMarkdownFencesRejectsAnUnclosedBlock(t *testing.T) {
	if _, err := MarkdownFences("intro\n\n```sh\nmake test\n"); err == nil {
		t.Fatal("an unclosed fence was accepted, want an error naming its opening line")
	}
}

func TestMarkdownFencesKeepsBlocksSeparate(t *testing.T) {
	doc := "```\none\n```\n\ntext\n\n```sh\ntwo\n```\n"
	fences, err := MarkdownFences(doc)
	if err != nil {
		t.Fatalf("MarkdownFences: %v", err)
	}
	if len(fences) != 2 {
		t.Fatalf("got %d fences, want 2", len(fences))
	}
	if strings.Join(fences[1].Lines, "|") != "two" || fences[1].Lang != "sh" {
		t.Errorf("second fence = %+v, want the sh block containing two", fences[1])
	}
}
