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
	fenced bool
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
	"<|DSML|>",
	"</|DSML|>",
}

// inspectAssistantFabrication scans non-machinery ContentText parts on msg in
// encounter order and reports the kind of the earliest fabricated harness/user
// envelope or unparsed raw tool-call markup outside code spans, blockquotes,
// and list items.
func inspectAssistantFabrication(msg llm.Message) (kind string, ok bool) {
	for _, part := range msg.Content {
		if part.Kind != llm.ContentText || part.Machinery || part.Text == "" {
			continue
		}
		if k, matched := inspectAssistantText(part.Text); matched {
			return k, true
		}
	}
	return "", false
}

// inspectAssistantText scans text for the earliest fabricated harness envelope
// or raw tool-call markup outside code blocks, inline code spans, blockquotes,
// and list items. Clean strings without '<' or '[' return immediately with zero
// allocations.
func inspectAssistantText(text string) (kind string, ok bool) {
	if !strings.ContainsAny(text, "<[") {
		return "", false
	}

	lines, masked := maskCodeSpans(text)

	for i, ln := range lines {
		if ln.fenced {
			continue
		}
		rawLine := text[ln.start:ln.end]
		rawIndent, ok := proseLineIndent(rawLine)
		if !ok {
			continue
		}
		maskedLine := masked[ln.start:ln.end]
		rest := maskedLine[rawIndent:]

		switch rest[0] {
		case '<':
			// 1. Line-start harness/user input envelopes.
			for _, h := range harnessTagPrefixes {
				if hasPrefixMode(rest, h.prefix, h.caseFoldASCII) && hasTagBoundary(rest[len(h.prefix):], false) {
					return fabricationKindHarnessEnvelope, true
				}
			}
			// 2. Line-start raw XML tool-call markup.
			for _, tm := range lineStartToolPrefixes {
				if strings.HasPrefix(rest, tm.prefix) && hasTagBoundary(rest[len(tm.prefix):], tm.allowEquals) {
					return fabricationKindToolMarkup, true
				}
			}
		case '[':
			rawTrimmed := strings.TrimRight(rawLine[rawIndent:], " \t")
			maskedTrimmed := strings.TrimRight(rest, " \t")
			if rawTrimmed == maskedTrimmed {
				if maskedTrimmed == "[CONTEXT SUMMARY]" {
					return fabricationKindHarnessEnvelope, true
				}
				if maskedTrimmed == "[answers]" && hasFollowingNumberedAnswerLine(text, masked, lines, i+1) {
					return fabricationKindHarnessEnvelope, true
				}
			}
		}

		// 3. DSML tokens anywhere on a prose-eligible line.
		if strings.IndexByte(maskedLine, '<') >= 0 {
			for _, tok := range dsmlTokens {
				if strings.Contains(maskedLine, tok) {
					return fabricationKindToolMarkup, true
				}
			}
		}
	}

	return "", false
}

func maskCodeSpans(text string) ([]lineSpan, string) {
	lines := make([]lineSpan, 0, strings.Count(text, "\n")+1)
	var fence mdfence.Tracker

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

		lines = append(lines, lineSpan{
			start:  pos,
			end:    end,
			fenced: fence.StepInBlock(text[pos:end]),
		})
		pos = rawEnd
	}

	if strings.IndexByte(text, '`') < 0 {
		return lines, text
	}
	buf := []byte(text)

	// Mask CommonMark inline backtick code spans within each non-fenced paragraph
	// (blank lines end a paragraph so an unclosed backtick cannot bleed across paragraphs).
	pStart := -1
	for idx, ln := range lines {
		isBlank := ln.fenced || strings.TrimSpace(text[ln.start:ln.end]) == ""
		if !isBlank && pStart < 0 {
			pStart = ln.start
		}
		if isBlank && pStart >= 0 {
			maskParagraphInlineCode(buf, pStart, lines[idx-1].end)
			pStart = -1
		}
	}
	if pStart >= 0 {
		maskParagraphInlineCode(buf, pStart, lines[len(lines)-1].end)
	}

	return lines, string(buf)
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

func proseLineIndent(rawLine string) (int, bool) {
	if mdfence.LeadingColumns(rawLine) >= 4 {
		return 0, false
	}
	indent := mdfence.LeadingSpaces(rawLine)
	rest := rawLine[indent:]
	if rest == "" || rest[0] == '>' {
		return 0, false
	}
	if mLen, ok := mdfence.ListMarker(rest); ok {
		after := rest[mLen:]
		if after != "" && (after[0] == ' ' || after[0] == '\t') {
			return 0, false
		}
	}
	return indent, true
}

func hasFollowingNumberedAnswerLine(text, masked string, lines []lineSpan, fromLine int) bool {
	for k := fromLine; k < len(lines); k++ {
		raw := text[lines[k].start:lines[k].end]
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if lines[k].fenced || mdfence.LeadingColumns(raw) >= 4 {
			return false
		}
		rawIndent := mdfence.LeadingSpaces(raw)
		mLine := masked[lines[k].start:lines[k].end]
		return strings.HasPrefix(mLine[rawIndent:], "1. ")
	}
	return false
}

func hasPrefixMode(s, prefix string, caseFoldASCII bool) bool {
	if len(s) < len(prefix) {
		return false
	}
	if !caseFoldASCII {
		return strings.HasPrefix(s, prefix)
	}
	return strings.EqualFold(s[:len(prefix)], prefix)
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
