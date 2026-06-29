package contextmgr

import (
	"reflect"
	"testing"
)

// FuzzCheckpointExtract drives the checkpoint parse seam
// (extractCheckpointConversation + extractCheckpointWorkingNotes), which spans
// markdown-section parsing and the JSON-tag fallback (json.Unmarshal of the
// <conversation>/<user_messages>/… payloads). Input is arbitrary checkpoint
// text. Beyond no-panic it asserts a render→extract→render fixed point: the
// cleaned conversation, rendered with this package's own renderer and re-parsed,
// recovers the same entries — proving the writer and reader agree.
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

		// Render the cleaned entries back to markdown and re-extract: the
		// renderer + markdown parser must form a fixed point.
		rendered := renderCheckpointConversation(entries)
		roundTripped := extractCheckpointConversation(rendered)

		// renderCheckpointConversation re-cleans, so compare against the same
		// cleaning applied to the original extraction.
		want := cleanCheckpointConversation(entries)
		if len(want) == 0 && len(roundTripped) == 0 {
			return
		}
		if !reflect.DeepEqual(want, roundTripped) {
			t.Fatalf("checkpoint conversation render/extract not a fixed point:\n want=%#v\n got =%#v\n rendered=%q",
				want, roundTripped, rendered)
		}
	})
}
