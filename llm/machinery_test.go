package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

// The agent wraps one-way notifications to the model in a faux-XML
// <system-notification> block. The spelling lives here — the single source
// both the producer (agent's systemNotification helper) and the consumers
// (apptranscript's reload filter, transcript's decode-time inference for
// pre-flag entries) build from, so the tags cannot drift apart.

func TestSystemNotificationTagSpellings(t *testing.T) {
	if SystemNotificationOpenTag != "<system-notification>" {
		t.Fatalf("SystemNotificationOpenTag = %q, want %q", SystemNotificationOpenTag, "<system-notification>")
	}
	if SystemNotificationCloseTag != "</system-notification>" {
		t.Fatalf("SystemNotificationCloseTag = %q, want %q", SystemNotificationCloseTag, "</system-notification>")
	}
}

func TestIsMachineryNotificationText(t *testing.T) {
	block := SystemNotificationOpenTag + "attachment shot.png saved to /state/s1/attachments/abcdef-screenshot.png" + SystemNotificationCloseTag
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"exact block", block, true},
		{"block with surrounding whitespace", " \n\t" + block + "\n ", true},
		{"inline mention is the user's own words", "the transcript may contain <system-notification>blocks</system-notification> mid-sentence", false},
		{"open tag only", SystemNotificationOpenTag + "stored at /state/attachments/shot.png", false},
		{"close tag only", "stored at /state/attachments/shot.png" + SystemNotificationCloseTag, false},
		{"empty", "", false},
		{"plain prose", "check this screenshot", false},
	}
	for _, tc := range cases {
		if got := IsMachineryNotificationText(tc.text); got != tc.want {
			t.Fatalf("%s: IsMachineryNotificationText(%q) = %v, want %v", tc.name, tc.text, got, tc.want)
		}
	}
}

// A flagged part must survive encode/decode (it is persisted transcript
// state), and an unflagged part must marshal byte-identically to today —
// user prose never carries the key. That is a part-level fact only:
// entry-level readability for older builds is a separate, deliberate
// matter — every new entry carries machinery_flagged, which older builds
// reject wholesale per the wf7e one-way door; see the comment on
// transcript.Entry.
func TestContentPartMachineryFlagJSONRoundTrip(t *testing.T) {
	flagged := ContentPart{
		Kind:      ContentText,
		Text:      SystemNotificationOpenTag + "stored at /state/attachments/shot.png" + SystemNotificationCloseTag,
		Machinery: true,
	}
	raw, err := json.Marshal(flagged)
	if err != nil {
		t.Fatalf("marshal flagged part: %v", err)
	}
	if !strings.Contains(string(raw), `"machinery":true`) {
		t.Fatalf("flagged part JSON = %s, want it to carry \"machinery\":true", raw)
	}
	var back ContentPart
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal flagged part: %v", err)
	}
	if !back.Machinery {
		t.Fatalf("round trip lost the machinery flag: %+v", back)
	}
	if back.Text != flagged.Text || back.Kind != flagged.Kind {
		t.Fatalf("round trip changed kind/text: %+v", back)
	}

	plain := ContentPart{Kind: ContentText, Text: "user prose"}
	rawPlain, err := json.Marshal(plain)
	if err != nil {
		t.Fatalf("marshal plain part: %v", err)
	}
	if strings.Contains(string(rawPlain), "machinery") {
		t.Fatalf("plain part JSON = %s, want no machinery key (omitempty keeps user parts unchanged)", rawPlain)
	}
}

func TestMachineryTextAndUserMachineryConstructors(t *testing.T) {
	part := MachineryText("stored at /state/attachments/shot.png")
	if part.Kind != ContentText || part.Text != "stored at /state/attachments/shot.png" || !part.Machinery {
		t.Fatalf("MachineryText = %+v, want a flagged text part", part)
	}
	msg := UserMachinery("stored at /state/attachments/shot.png")
	if msg.Role != RoleUser || len(msg.Content) != 1 || msg.Content[0] != part {
		t.Fatalf("UserMachinery = %+v, want a user-role message wrapping the flagged part", msg)
	}
	if got := msg.Text(); got != "stored at /state/attachments/shot.png" {
		t.Fatalf("UserMachinery text = %q, model-facing text must stay the notice verbatim", got)
	}
}
