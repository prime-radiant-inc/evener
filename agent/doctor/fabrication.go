package doctor

import (
	"strings"

	"primeradiant.com/evener/agent/internal/mdfence"
	"primeradiant.com/evener/llm"
)

const (
	// fabricationKindHarnessEnvelope marks a fabricated runtime or user input
	// frame such as <delegate-notification>, <job-notification>, <SYSTEM-REMINDER>,
	// or [answers].
	fabricationKindHarnessEnvelope = "harness_envelope"
	// fabricationKindToolMarkup marks unparsed raw tool-call syntax such as
	// <｜DSML｜>, <invoke>, <parameter>, or <tool_call> leaked into assistant text.
	fabricationKindToolMarkup = "tool_markup"
)

type lineSpan struct {
	start  int
	end    int // excludes trailing \r\n or \n
	indent int // leading ASCII spaces when prose is true
	fenced bool
	prose  bool
}

var harnessTagPrefixes = []struct {
	prefix        string
	caseFoldASCII bool
}{
	{prefix: "<delegate-notification"},
	{prefix: "<job-notification"},
	{prefix: "<delegate-user-note"},
	{prefix: "<system-notification"},
	{prefix: "<system-reminder", caseFoldASCII: true},
	{prefix: "<environment_context"},
	{prefix: "<environment-context"},
	{prefix: "<shared-notes"},
	{prefix: "<skill-context"},
}

var lineStartToolPrefixes = []struct {
	prefix      string
	allowEquals bool
}{
	{prefix: "<function_calls"},
	{prefix: "</function_calls"},
	{prefix: "<invoke"},
	{prefix: "</invoke"},
	{prefix: "<parameter", allowEquals: true},
	{prefix: "</parameter"},
	{prefix: "<tool_call"},
	{prefix: "</tool_call"},
}

var dsmlTokens = []string{
	"<｜DSML｜",
	"</｜DSML｜",
	"<|DSML|",
	"</|DSML|",
}

// inspectAssistantFabrication scans non-machinery ContentText parts on msg and
// reports whether the turn carries a fabricated harness/user envelope or
// unparsed raw tool-call markup outside code spans, blockquotes, and list
// items. Within a single assistant turn, harness_envelope (high severity) takes
// precedence over tool_markup (medium severity) so an earlier markup leak never
// masks a fabricated input frame later in the turn.
func inspectAssistantFabrication(msg llm.Message) (kind string, ok bool) {
	sawToolMarkup := false
	for _, part := range msg.Content {
		if part.Kind != llm.ContentText || part.Machinery || part.Text == "" {
			continue
		}
		k, matched := inspectAssistantText(part.Text)
		if !matched {
			continue
		}
		if k == fabricationKindHarnessEnvelope {
			return fabricationKindHarnessEnvelope, true
		}
		sawToolMarkup = true
	}
	if sawToolMarkup {
		return fabricationKindToolMarkup, true
	}
	return "", false
}

// inspectAssistantText scans text for fabricated harness envelopes or raw
// tool-call markup outside code blocks, inline code spans, blockquotes, and
// list items. Clean strings without '<' or '[' return immediately with zero
// allocations.
func inspectAssistantText(text string) (kind string, ok bool) {
	if !strings.ContainsAny(text, "<[") {
		return "", false
	}

	lines, masked := maskCodeSpans(text)
	sawToolMarkup := false

	for i, ln := range lines {
		if !ln.prose {
			continue
		}
		rawLine := text[ln.start:ln.end]
		maskedLine := masked[ln.start:ln.end]
		rest := maskedLine[ln.indent:]
		lineMatchedMarkup := false

		switch rest[0] {
		case '<':
			// 1. Line-start harness/user input envelopes.
			for _, h := range harnessTagPrefixes {
				if after, ok := cutPrefixMode(rest, h.prefix, h.caseFoldASCII); ok && hasTagBoundary(after, false) {
					return fabricationKindHarnessEnvelope, true
				}
			}
			// 2. Line-start raw XML tool-call markup.
			if !sawToolMarkup {
				for _, tm := range lineStartToolPrefixes {
					if after, ok := strings.CutPrefix(rest, tm.prefix); ok && hasTagBoundary(after, tm.allowEquals) {
						sawToolMarkup = true
						lineMatchedMarkup = true
						break
					}
				}
			}
		case '[':
			rawTrimmed := strings.TrimRight(rawLine[ln.indent:], " \t")
			maskedTrimmed := strings.TrimRight(rest, " \t")
			if rawTrimmed == maskedTrimmed {
				if maskedTrimmed == "[CONTEXT SUMMARY]" {
					return fabricationKindHarnessEnvelope, true
				}
				if maskedTrimmed == "[answers]" && hasFollowingNumberedAnswerLine(text, lines, i+1) {
					return fabricationKindHarnessEnvelope, true
				}
			}
		}

		// 3. DSML tokens anywhere on a prose-eligible line.
		if !sawToolMarkup && !lineMatchedMarkup && strings.IndexByte(maskedLine, '<') >= 0 {
			for _, tok := range dsmlTokens {
				if strings.Contains(maskedLine, tok) {
					sawToolMarkup = true
					break
				}
			}
		}
	}

	if sawToolMarkup {
		return fabricationKindToolMarkup, true
	}
	return "", false
}

func maskCodeSpans(text string) ([]lineSpan, string) {
	lines := make([]lineSpan, 0, strings.Count(text, "\n")+1)
	var fence mdfence.Tracker
	listContentCol := 0

	pos := 0
	for pos < len(text) {
		nl := strings.IndexByte(text[pos:], '\n')
		var rawEnd int
		if nl < 0 {
			rawEnd = len(text)
		} else {
			rawEnd = pos + nl + 1
		}
		end := rawEnd
		if end > pos && text[end-1] == '\n' {
			end--
		}
		if end > pos && text[end-1] == '\r' {
			end--
		}

		rawLine := text[pos:end]
		wasInFence := fence.Inside()
		inFence := fence.StepInBlock(rawLine)
		indent := 0
		prose := false
		if inFence {
			if !wasInFence {
				cols := mdfence.LeadingColumns(rawLine)
				if listContentCol > 0 && cols < listContentCol {
					listContentCol = 0
				}
				if listContentCol == 0 && cols < 4 {
					if col, ok := listItemContentColumn(rawLine, cols); ok {
						listContentCol = col
					}
				}
			}
		} else if strings.TrimSpace(rawLine) != "" {
			cols := mdfence.LeadingColumns(rawLine)
			if listContentCol > 0 && cols < listContentCol {
				listContentCol = 0
			}
			if listContentCol == 0 && cols < 4 {
				// Any leading '\t' reaches at least column 4, so cols < 4
				// guarantees leading whitespace is pure ASCII spaces.
				rest := rawLine[cols:]
				if rest[0] != '>' {
					if col, ok := listItemContentColumn(rawLine, cols); ok {
						listContentCol = col
					} else {
						indent = cols
						prose = true
					}
				}
			}
		}

		lines = append(lines, lineSpan{
			start:  pos,
			end:    end,
			indent: indent,
			fenced: inFence,
			prose:  prose,
		})
		pos = rawEnd
	}

	if strings.IndexByte(text, '`') < 0 {
		return lines, text
	}
	buf := []byte(text)

	// Mask CommonMark inline backtick code spans strictly within each contiguous
	// prose paragraph so literal backticks in fenced/indented code, blockquotes,
	// list items, or across blank lines cannot bleed into prose paragraphs.
	pStart := -1
	for idx, ln := range lines {
		if ln.prose && pStart < 0 {
			pStart = ln.start
		}
		if !ln.prose && pStart >= 0 {
			maskParagraphInlineCode(buf, pStart, lines[idx-1].end)
			pStart = -1
		}
	}
	if pStart >= 0 {
		maskParagraphInlineCode(buf, pStart, lines[len(lines)-1].end)
	}

	return lines, string(buf)
}

func listItemContentColumn(rawLine string, indent int) (int, bool) {
	rest := rawLine[indent:]
	mLen, ok := mdfence.ListMarker(rest)
	if !ok {
		return 0, false
	}
	after := rest[mLen:]
	if after == "" || (after[0] != ' ' && after[0] != '\t') {
		return 0, false
	}
	baseCols := indent + mLen
	cols := baseCols
	i := 0
	for i < len(after) && (after[i] == ' ' || after[i] == '\t') {
		if after[i] == ' ' {
			cols++
		} else {
			cols += 4 - cols%4
		}
		i++
	}
	if i == len(after) || cols-baseCols > 4 {
		return baseCols + 1, true
	}
	return cols, true
}

func maskParagraphInlineCode(buf []byte, start, end int) {
	i := start
	for i < end {
		if buf[i] != '`' {
			i++
			continue
		}
		openStart := i
		for i < end && buf[i] == '`' {
			i++
		}
		runLen := i - openStart

		// Search for the matching closing run of exact length runLen within [i, end).
		// If unclosed before paragraph end, leave openStart's backticks literal and continue at i.
		j := i
		for j < end {
			if buf[j] != '`' {
				j++
				continue
			}
			closeStart := j
			for j < end && buf[j] == '`' {
				j++
			}
			if j-closeStart == runLen {
				for k := openStart; k < j; k++ {
					if buf[k] != '\n' && buf[k] != '\r' {
						buf[k] = ' '
					}
				}
				i = j
				break
			}
		}
	}
}

func hasFollowingNumberedAnswerLine(text string, lines []lineSpan, fromLine int) bool {
	for k := fromLine; k < len(lines); k++ {
		raw := text[lines[k].start:lines[k].end]
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if lines[k].fenced || mdfence.LeadingColumns(raw) >= 4 {
			return false
		}
		rawIndent := mdfence.LeadingSpaces(raw)
		return strings.HasPrefix(raw[rawIndent:], "1. ")
	}
	return false
}

func cutPrefixMode(s, prefix string, caseFoldASCII bool) (string, bool) {
	if !caseFoldASCII {
		return strings.CutPrefix(s, prefix)
	}
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return "", false
	}
	return s[len(prefix):], true
}

func hasTagBoundary(rest string, allowEquals bool) bool {
	if rest == "" {
		return true
	}
	switch rest[0] {
	case ' ', '\t', '>', '/', '\r':
		return true
	case '=':
		return allowEquals
	default:
		return false
	}
}
