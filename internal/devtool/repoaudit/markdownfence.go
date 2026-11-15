package repoaudit

import (
	"fmt"
	"os"
	"strings"
)

// MarkdownFence is one fenced block in a Markdown document: the info string
// on the opening fence, the content lines between the fences, and the 1-based
// line number the opening fence sits on.
type MarkdownFence struct {
	Lang   string
	Lines  []string
	LineNo int
}

// MarkdownFences splits a Markdown document into its fenced blocks. A fence
// opens on any line whose trimmed text starts with three backticks, and the
// rest of that line is its info string ("sh", "bash console", ""). It closes
// only on a bare backtick run: CommonMark gives a closing fence no info string,
// so a ```` ```bash ```` line inside a block is content, not a close. Content
// lines are returned verbatim, so a caller that needs the exact bytes a reader
// would copy still has them.
//
// An unterminated fence is an error, never a silently dropped block: every
// caller audits or runs the blocks, and a block that never closes is exactly
// the doc break they exist to catch.
func MarkdownFences(doc string) ([]MarkdownFence, error) {
	var fences []MarkdownFence
	var current []string
	open, inFence, openedAt := MarkdownFence{}, false, 0
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
				open = MarkdownFence{Lang: strings.TrimSpace(info), LineNo: lineNo}
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

// ReadMarkdownFences reads the Markdown file at path and splits it into its
// fenced blocks, naming path in any error.
func ReadMarkdownFences(path string) ([]MarkdownFence, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	fences, err := MarkdownFences(string(body))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return fences, nil
}

// HasCommand reports whether a non-comment line in a shell block invokes cmd.
// A comment that merely names a helper must not count as calling it.
func HasCommand(text, cmd string) bool {
	for line := range strings.SplitSeq(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.Contains(trimmed, cmd) {
			return true
		}
	}
	return false
}
