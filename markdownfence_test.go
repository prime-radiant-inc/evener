package evener_test

import (
	"fmt"
	"strings"
	"testing"
)

// markdownFence is one fenced block in a Markdown document: the info string
// on the opening fence, the content lines between the fences, and the 1-based
// line number the opening fence sits on.
type markdownFence struct {
	Lang   string
	Lines  []string
	LineNo int
}

// markdownFences splits a Markdown document into its fenced blocks. A fence
// opens on any line whose trimmed text starts with three backticks, and the
// rest of that line is its info string ("sh", "bash console", ""). It closes
// only on a bare backtick run: CommonMark gives a closing fence no info string,
// so a ```` ```bash ```` line inside a block is content, not a close. Content
// lines are returned verbatim, so a caller that needs the exact bytes a reader
// would copy still has them.
//
// An unterminated fence is an error, never a silently dropped block: every
// caller is an audit, and a block that never closes is exactly the doc break
// the audit exists to catch.
func markdownFences(doc string) ([]markdownFence, error) {
	var fences []markdownFence
	var current []string
	open, inFence, openedAt := markdownFence{}, false, 0
	for i, line := range strings.Split(doc, "\n") {
		lineNo := i + 1
		trimmed := strings.TrimSpace(line)
		if info, isFence := strings.CutPrefix(trimmed, "```"); isFence {
			if inFence {
				if strings.TrimSpace(info) != "" {
					current = append(current, line)
					continue
				}
				open.Lines = current
				fences = append(fences, open)
				current = nil
			} else {
				openedAt = lineNo
				open = markdownFence{Lang: strings.TrimSpace(info), LineNo: lineNo}
			}
			inFence = !inFence
			continue
		}
		if inFence {
			current = append(current, line)
		}
	}
	if inFence {
		return fences, fmt.Errorf("a fence opened at line %d is never closed; a broken block must fail the audit rather than be skipped", openedAt)
	}
	return fences, nil
}

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

	fences, err := markdownFences(doc)
	if err != nil {
		t.Fatalf("markdownFences: %v", err)
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
	if _, err := markdownFences("intro\n\n```sh\nmake test\n"); err == nil {
		t.Fatal("an unclosed fence was accepted, want an error naming its opening line")
	}
}

func TestMarkdownFencesKeepsBlocksSeparate(t *testing.T) {
	doc := "```\none\n```\n\ntext\n\n```sh\ntwo\n```\n"
	fences, err := markdownFences(doc)
	if err != nil {
		t.Fatalf("markdownFences: %v", err)
	}
	if len(fences) != 2 {
		t.Fatalf("got %d fences, want 2", len(fences))
	}
	if strings.Join(fences[1].Lines, "|") != "two" || fences[1].Lang != "sh" {
		t.Errorf("second fence = %+v, want the sh block containing two", fences[1])
	}
}
