package apptranscript

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// The agent attaches machinery notes for the model to user-input and
// steering messages as extra text parts — e.g. the image-attachment
// persistence note, wrapped as "<system-notification>…</system-notification>"
// by the agent's systemNotification helper and flagged on the part
// (llm.ContentPart.Machinery). Those parts must reach the model, but the
// live stream keeps them out of the user bubble, so the reload projection
// must not paste them back into a reloaded bubble either. Filtering matches
// the flag alone — never the text shape — so a user pasting the exact block
// verbatim keeps their own words.

// notificationNote mirrors the shape the agent's systemNotification helper
// produces for the image persistence note: one text part that is exactly the
// faux-XML block.
const notificationNote = "<system-notification>attachment screenshot.png saved to /state/sessions/s1/attachments/abcdef0123456789-screenshot.png; the model can read it back later with read_file</system-notification>"

func flaggedNotificationPart() llm.ContentPart {
	return llm.MachineryText(notificationNote)
}

func imageOnlyPlusNoteContent() []llm.ContentPart {
	return []llm.ContentPart{
		{Kind: llm.ContentImage, Image: &llm.ImageData{MediaType: "image/png", Data: []byte{0x89, 0x50, 0x4e, 0x47}}},
		flaggedNotificationPart(),
	}
}

// TestProjectTurnUserInputTextOmitsSystemNotificationParts: a user turn with
// prose, an image, and a flagged machinery note part projects Text == the
// prose.
func TestProjectTurnUserInputTextOmitsSystemNotificationParts(t *testing.T) {
	turn := schema.Turn{
		Kind: schema.TurnUserInput,
		Message: llm.Message{
			Role: llm.RoleUser,
			Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "What is in this picture?"},
				{Kind: llm.ContentImage, Image: &llm.ImageData{MediaType: "image/png", Data: []byte{0x89, 0x50, 0x4e, 0x47}}},
				flaggedNotificationPart(),
			},
		},
	}
	items := ProjectTurn("turn_1", 0, turn, NewToolCallRegistry(), nil, nil)
	if len(items) != 1 {
		t.Fatalf("items=%+v, want 1 userMessage item", items)
	}
	if items[0].Type != "userMessage" {
		t.Fatalf("item type=%q, want userMessage", items[0].Type)
	}
	if got, want := items[0].Text, "What is in this picture?"; got != want {
		t.Fatalf("user message text=%q, want %q (flagged machinery part must not surface)", got, want)
	}
}

// TestProjectTurnSteeringTextOmitsSystemNotificationParts: a steering turn
// with prose and a flagged machinery note part projects Text == the prose.
func TestProjectTurnSteeringTextOmitsSystemNotificationParts(t *testing.T) {
	turn := schema.Turn{
		Kind: schema.TurnSteering,
		Message: llm.Message{
			Role: llm.RoleUser,
			Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "Please continue with the screenshot."},
				flaggedNotificationPart(),
			},
		},
	}
	items := ProjectTurn("turn_1", 0, turn, NewToolCallRegistry(), nil, nil)
	if len(items) != 1 {
		t.Fatalf("items=%+v, want 1 steering item", items)
	}
	if items[0].Type != "steering" {
		t.Fatalf("item type=%q, want steering", items[0].Type)
	}
	if got, want := items[0].Text, "Please continue with the screenshot."; got != want {
		t.Fatalf("steering text=%q, want %q (flagged machinery part must not surface)", got, want)
	}
}

// TestProjectTurnUserInputKeepsTextMentioningSystemNotificationTags: an
// unflagged user message that merely mentions the tags inline is the user's
// own words and must survive the projection untouched.
func TestProjectTurnUserInputKeepsTextMentioningSystemNotificationTags(t *testing.T) {
	prose := "the transcript may contain <system-notification>blocks</system-notification> mid-sentence"
	turn := schema.Turn{
		Kind:    schema.TurnUserInput,
		Message: llm.User(prose),
	}
	items := ProjectTurn("turn_1", 0, turn, NewToolCallRegistry(), nil, nil)
	if len(items) != 1 {
		t.Fatalf("items=%+v, want 1 userMessage item", items)
	}
	if got, want := items[0].Text, prose; got != want {
		t.Fatalf("user message text=%q, want the inline mention preserved verbatim %q", got, want)
	}
}

// TestProjectTurnUserInputImageOnlyWithNoteProjectsEmptyText: an image-only
// paste carries no user prose, so once the flagged machinery note is skipped
// the bubble is just the image — not a stray notification block.
func TestProjectTurnUserInputImageOnlyWithNoteProjectsEmptyText(t *testing.T) {
	turn := schema.Turn{
		Kind: schema.TurnUserInput,
		Message: llm.Message{
			Role:    llm.RoleUser,
			Content: imageOnlyPlusNoteContent(),
		},
	}
	items := ProjectTurn("turn_1", 0, turn, NewToolCallRegistry(), nil, nil)
	if len(items) != 1 {
		t.Fatalf("items=%+v, want 1 userMessage item", items)
	}
	if items[0].Text != "" {
		t.Fatalf("user message text=%q, want empty (image-only turn; note must not surface)", items[0].Text)
	}
}

// TestProjectTurnSteeringFlaggedMachineryOnlyKeepsFullText: a steering turn
// whose only content is one flagged machinery block is a real production
// shape — the cancelled-callback-watches notice routes exactly that text to
// a session — and the live stream projects its text as-is, so reload must
// keep the item, not strip it to nothing. The stripped-empty fallback keeps
// the full text.
func TestProjectTurnSteeringFlaggedMachineryOnlyKeepsFullText(t *testing.T) {
	turn := schema.Turn{
		Kind: schema.TurnSteering,
		Message: llm.Message{
			Role:    llm.RoleUser,
			Content: []llm.ContentPart{flaggedNotificationPart()},
		},
	}
	items := ProjectTurn("turn_1", 0, turn, NewToolCallRegistry(), nil, nil)
	if len(items) != 1 {
		t.Fatalf("items=%+v, want 1 steering item", items)
	}
	if got, want := items[0].Text, notificationNote; got != want {
		t.Fatalf("steering text=%q, want the full machinery block %q (flagged machinery-only steering must not vanish on reload)", got, want)
	}
}

// TestUserFacingTextKeepsVerbatimMachineryPasteWithImage: THE verbatim-paste
// gap. A user pasting the exact machinery block as their whole message, with
// an image attached, must keep their text — the block shape alone must never
// decide visibility, because the user's paste has the same shape as the
// session's own notes.
func TestUserFacingTextKeepsVerbatimMachineryPasteWithImage(t *testing.T) {
	msg := llm.Message{
		Role: llm.RoleUser,
		Content: []llm.ContentPart{
			{Kind: llm.ContentImage, Image: &llm.ImageData{MediaType: "image/png", Data: []byte{0x89, 0x50, 0x4e, 0x47}}},
			{Kind: llm.ContentText, Text: notificationNote},
		},
	}
	if got, want := UserFacingText(msg), notificationNote; got != want {
		t.Fatalf("UserFacingText on a verbatim paste = %q, want the user's own block %q", got, want)
	}
}

// TestUserFacingTextKeepsVerbatimMachineryPasteWithoutImage: the lone-paste
// variant — one unflagged block part and nothing else — must also survive.
func TestUserFacingTextKeepsVerbatimMachineryPasteWithoutImage(t *testing.T) {
	msg := llm.Message{
		Role:    llm.RoleUser,
		Content: []llm.ContentPart{{Kind: llm.ContentText, Text: notificationNote}},
	}
	if got, want := UserFacingText(msg), notificationNote; got != want {
		t.Fatalf("UserFacingText on a lone paste = %q, want the user's own block %q", got, want)
	}
}

// TestUserFacingTextStripsFlaggedPartRegardlessOfShape: the flag alone
// decides. A flagged part whose text is not block-shaped still strips from
// user-facing text; an unflagged part with the same text would stay.
func TestUserFacingTextStripsFlaggedPartRegardlessOfShape(t *testing.T) {
	msg := llm.Message{
		Role: llm.RoleUser,
		Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "user prose"},
			llm.MachineryText("internal session note"),
		},
	}
	if got, want := UserFacingText(msg), "user prose"; got != want {
		t.Fatalf("UserFacingText = %q, want %q (the flagged part must strip on the flag alone)", got, want)
	}
}
