package contextmgr

import (
	"testing"
)

// FuzzCheckpointExtract drives the checkpoint parse seam
// (extractCheckpointConversation + extractCheckpointWorkingNotes), which spans
// markdown-section parsing and the JSON-tag fallback (json.Unmarshal of the
// <conversation>/<user_messages>/… payloads). Input is arbitrary checkpoint
// text. Beyond no-panic it asserts that a rendered conversation re-renders to
// the same bytes after one extract/render round trip: the renderer defuses the
// model-authored frames it writes, so the entries it reads back hold those
// escapes and rendering them again must reproduce them rather than defuse a
// second time. That is what proves the writer and reader agree.
func FuzzCheckpointExtract(f *testing.F) {
	seeds := []string{
		"## Conversation\n\n### User\n\n```text\nhello\n```\n\n### Agent\n\n```text\nhi there\n```\n",
		"<conversation>[{\"role\":\"user\",\"text\":\"a\"},{\"role\":\"agent\",\"text\":\"b\"}]</conversation>",
		"<user_messages>[\"task one\"]</user_messages>\n<agent_responses>[\"did it\"]</agent_responses>",
		"## Working Notes\n\n### Note\n\n```text\nremember this\n```\n",
		"Original task: build the thing\n",
		"plain text with no structure",
		"",
		"## Conversation\n\n### Agent\n\n```````text\n``` nested fence\n```````\n",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, text string) {
		entries := extractCheckpointConversation(text)
		_ = extractCheckpointWorkingNotes(text)

		// Render the cleaned entries back to markdown, re-extract, and render
		// again: the second render must reproduce the first byte for byte.
		rendered := renderCheckpointConversation(entries)
		again := renderCheckpointConversation(extractCheckpointConversation(rendered))
		if again != rendered {
			t.Fatalf("checkpoint conversation render/extract/render not a fixed point:\n first  =%q\n second =%q",
				rendered, again)
		}
	})
}
