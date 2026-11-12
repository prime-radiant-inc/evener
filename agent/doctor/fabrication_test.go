package doctor

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/llm"
)

func TestInspectAssistantText_DetectsHarnessEnvelopes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
	}{
		{
			name:  "delegate notification after prose",
			input: "All PRs checked.\n\n<delegate-notification delegate_id=\"dlg_1\">merged</delegate-notification>",
		},
		{
			name:  "job notification with 3 leading spaces",
			input: "Status.\n   <job-notification job_id=\"job_1\">done</job-notification>",
		},
		{
			name:  "delegate user note at byte zero",
			input: "<delegate-user-note>\nstop\n</delegate-user-note>",
		},
		{
			name:  "system notification CRLF",
			input: "Done.\r\n<system-notification>saved</system-notification>",
		},
		{
			name:  "uppercase SYSTEM-REMINDER",
			input: "Reply.\n<SYSTEM-REMINDER>\ninterrupted\n</SYSTEM-REMINDER>",
		},
		{
			name:  "lowercase system-reminder",
			input: "Reply.\n  <system-reminder>background</system-reminder>",
		},
		{
			name:  "environment_context underscore",
			input: "Text.\n<environment_context>\ndate: 2026-11-12\n</environment_context>",
		},
		{
			name:  "environment-context hyphen",
			input: "Text.\n<environment-context>date: 2026-11-12</environment-context>",
		},
		{
			name:  "shared-notes",
			input: "Text.\n<shared-notes>\nAgent: ok\n</shared-notes>",
		},
		{
			name:  "skill-context",
			input: "Text.\n<skill-context>\n{}\n</skill-context>",
		},
		{
			name:  "CONTEXT SUMMARY banner",
			input: "Prefix.\n [CONTEXT SUMMARY] \nEarlier history",
		},
		{
			name:  "answers block LF",
			input: "Waiting.\n\n[answers]\n1. [#342 follow-up] -> \"Implement quota coordinator\"",
		},
		{
			name:  "answers block CRLF with blank line before item 1",
			input: "Waiting.\r\n  [answers]  \r\n\r\n   1. [q1] -> \"yes\"",
		},
		{
			name:  "unclosed backtick before blank line does not mask next paragraph",
			input: "Paragraph one has a stray ` backtick.\n\n<delegate-notification delegate_id=\"dlg_1\">fake</delegate-notification>",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, ok := inspectAssistantText(tc.input)
			if !ok || kind != fabricationKindHarnessEnvelope {
				t.Fatalf("inspectAssistantText(%q) = (%q, %v), want (%q, true)", tc.input, kind, ok, fabricationKindHarnessEnvelope)
			}
		})
	}
}

func TestInspectAssistantText_DetectsToolMarkup(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
	}{
		{
			name:  "fullwidth DSML at line start with leading spaces",
			input: "Watching.\n\n  <｜DSML｜ parameter name=\"intent\">check",
		},
		{
			name:  "mid-line DSML",
			input: "Starting runs.<｜DSML｜function_calls>",
		},
		{
			name:  "closing DSML with extra angle bracket",
			input: "Starting both CI gates.\n\n<</｜DSML｜ parameter>\n</｜DSML｜ invoke>",
		},
		{
			name:  "ASCII pipe DSML",
			input: "Text\n<|DSML|>invoke",
		},
		{
			name:  "omitted opener tag followed by DSML parameter",
			input: "Sending to shepherd.\n\n  <delegate_send>\n<｜DSML｜ parameter name=\"message\" string=\"true\">hi",
		},
		{
			name:  "inline code at line start followed by DSML token",
			input: "`my_func` <｜DSML｜function_calls>",
		},
		{
			name:  "multi-line inline code span closing before DSML",
			input: "See `\n<keep_me>\n` <｜DSML｜function_calls>",
		},
		{
			name:  "Claude XML function_calls",
			input: "Done.\n<function_calls>\n<invoke name=\"shell\">",
		},
		{
			name:  "Claude XML invoke with attributes",
			input: "Done.\n<invoke name=\"read_file\">",
		},
		{
			name:  "Claude XML closing parameter and invoke",
			input: "Text.\n</parameter>\n</invoke>",
		},
		{
			name:  "Claude XML parameter equals form",
			input: "Text.\n<parameter=description>check</parameter>",
		},
		{
			name:  "Qwen tool_call tag",
			input: "Running.\n<tool_call>\n{\"name\":\"shell\"}\n</tool_call>",
		},
		{
			name:  "tool_call with attribute",
			input: "Running.\n <tool_call name=\"shell\">",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, ok := inspectAssistantText(tc.input)
			if !ok || kind != fabricationKindToolMarkup {
				t.Fatalf("inspectAssistantText(%q) = (%q, %v), want (%q, true)", tc.input, kind, ok, fabricationKindToolMarkup)
			}
		})
	}
}

func TestInspectAssistantText_ExemptsCodeSpansBlockquotesAndLists(t *testing.T) {
	t.Parallel()

	cleanInputs := []struct {
		name  string
		input string
	}{
		{
			name:  "backtick fenced block LF",
			input: "Example:\n```xml\n<delegate-notification delegate_id=\"dlg_1\">ok</delegate-notification>\n[answers]\n1. [q] -> \"a\"\n<｜DSML｜ parameter name=\"x\">\n</invoke>\n```\nDone.",
		},
		{
			name:  "tilde fenced block CRLF",
			input: "Example:\r\n~~~\r\n<job-notification job_id=\"j1\">\r\n<|DSML|>invoke\r\n~~~\r\nClean after.",
		},
		{
			name:  "list-nested fence",
			input: "- ~~~text\n  <delegate-notification delegate_id=\"dlg_1\">\n  <｜DSML｜ parameter>\n  ~~~\n- Next item",
		},
		{
			name:  "single and multi-backtick inline spans",
			input: "The runtime parses `<delegate-notification>` and `` `<｜DSML｜ parameter>` `` and `[CONTEXT SUMMARY]` inline.",
		},
		{
			name:  "inline code at line start before parameter",
			input: "`foo` <parameter> is an XML element.",
		},
		{
			name:  "inline code at line start before delegate-notification",
			input: "`prefix` <delegate-notification> in prose.",
		},
		{
			name:  "inline code at line start before CONTEXT SUMMARY",
			input: "`header:` [CONTEXT SUMMARY]",
		},
		{
			name:  "inline code at line start before answers and numbered item",
			input: "In `ask_user`, [answers]\n`example:` 1. choice",
		},
		{
			name:  "4-space indented code block with harness tag and DSML",
			input: "Indented code:\n\n    <delegate-notification delegate_id=\"dlg_1\">\n    <｜DSML｜ parameter name=\"x\">\n    [answers]\n    1. [q] -> \"a\"",
		},
		{
			name:  "tab-indented code block with harness tag and DSML",
			input: "Tab code:\n\t<system-notification>x</system-notification>\n\t<｜DSML｜ invoke>",
		},
		{
			name:  "blockquotes",
			input: "> <delegate-notification delegate_id=\"dlg_1\">\n> <｜DSML｜ parameter name=\"x\">\n> [answers]\n> 1. [q] -> \"a\"",
		},
		{
			name:  "unordered and ordered list items",
			input: "- <delegate-notification> tag\n* <｜DSML｜> token\n+ <invoke> element\n1. <system-notification> wrapper\n2) <tool_call> block",
		},
		{
			name:  "standalone answers word without numbered item",
			input: "Check the\n[answers]\nsection in the docs.",
		},
		{
			name:  "answers followed by fenced 1. item",
			input: "[answers]\n```\n1. [q] -> \"a\"\n```",
		},
		{
			name:  "similar tag prefix without tag boundary",
			input: "<invoke_helper> and <parameterized> and <system-notifications-doc> are custom identifiers.",
		},
	}

	for _, tc := range cleanInputs {
		t.Run(tc.name, func(t *testing.T) {
			if kind, ok := inspectAssistantText(tc.input); ok {
				t.Fatalf("inspectAssistantText(%q) matched %q, want clean", tc.input, kind)
			}
		})
	}
}

func TestInspectAssistantFabrication_SkipsMachineryAndThinkingAndZeroAllocsOnCleanFastPath(t *testing.T) {
	cleanMsg := llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "<delegate-notification>in thinking</delegate-notification>"}},
			{Kind: llm.ContentText, Text: "<system-notification>machinery</system-notification>", Machinery: true},
			{Kind: llm.ContentText, Text: "Normal prose with no angle brackets or square brackets anywhere in sight."},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "shell", Arguments: json.RawMessage(`{}`)}},
		},
	}
	allocs := testing.AllocsPerRun(100, func() {
		if kind, ok := inspectAssistantFabrication(cleanMsg); ok {
			t.Fatalf("unexpected match %q on clean message", kind)
		}
	})
	if allocs != 0 {
		t.Fatalf("clean fast path allocated %.2f objects/op, want 0", allocs)
	}

	// Earliest matching non-machinery ContentText part wins in encounter order.
	multiMsg := llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "<delegate-notification>ignored</delegate-notification>"}},
			{Kind: llm.ContentText, Text: "<delegate-notification>machinery ignored</delegate-notification>", Machinery: true},
			{Kind: llm.ContentText, Text: "Clean first part."},
			{Kind: llm.ContentText, Text: "Second part.\n<｜DSML｜ parameter name=\"x\">"},
			{Kind: llm.ContentText, Text: "<delegate-notification>later part</delegate-notification>"},
		},
	}
	if kind, ok := inspectAssistantFabrication(multiMsg); !ok || kind != fabricationKindToolMarkup {
		t.Fatalf("inspectAssistantFabrication(multiMsg) = (%q, %v), want (%q, true)", kind, ok, fabricationKindToolMarkup)
	}
}
