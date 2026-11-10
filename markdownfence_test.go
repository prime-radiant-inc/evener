package evener_test

import (
	"fmt"
	"strings"
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
// rest of that line is its info string ("sh", "bash console", ""). Content
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
