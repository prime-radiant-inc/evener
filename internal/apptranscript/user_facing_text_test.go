package apptranscript

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// The agent attaches machinery notes for the model to user-input and steering
// messages as extra text parts — today the image-attachment persistence note,
// wrapped as "<system-notification>…</system-notification>" by the agent's
// systemNotification helper. Those parts must reach the model, but the live
// stream keeps them out of the user bubble, so the reload projection must not
// paste them back into a reloaded bubble either.

// notificationNote mirrors the shape the agent's systemNotification helper
// produces for the image persistence note: one text part that is exactly the
// faux-XML block.
const notificationNote = "<system-notification>attachment screenshot.png saved to /state/sessions/s1/attachments/abcdef0123456789-screenshot.png; the model can read it back later with read_file</system-notification>"

func imageOnlyPlusNoteContent() []llm.ContentPart {
	return []llm.ContentPart{
		{Kind: llm.ContentImage, Image: &llm.ImageData{MediaType: "image/png", Data: []byte{0x89, 0x50, 0x4e, 0x47}}},
		{Kind: llm.ContentText, Text: notificationNote},
	}
}

// TestProjectTurnUserInputTextOmitsSystemNotificationParts: a user turn with
// prose, an image, and a machinery note part projects Text == the prose.
// Fails while the projection concatenates every text part.
func TestProjectTurnUserInputTextOmitsSystemNotificationParts(t *testing.T) {
	turn := schema.Turn{
		Kind: schema.TurnUserInput,
		Message: llm.Message{
			Role: llm.RoleUser,
			Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "What is in this picture?"},
				{Kind: llm.ContentImage, Image: &llm.ImageData{MediaType: "image/png", Data: []byte{0x89, 0x50, 0x4e, 0x47}}},
				{Kind: llm.ContentText, Text: notificationNote},
			},
		},
	}
	items := ProjectTurn("turn_1", 0, turn, map[string]string{}, nil, nil)
	if len(items) != 1 {
		t.Fatalf("items=%+v, want 1 userMessage item", items)
	}
	if items[0].Type != "userMessage" {
		t.Fatalf("item type=%q, want userMessage", items[0].Type)
	}
	if got, want := items[0].Text, "What is in this picture?"; got != want {
		t.Fatalf("user message text=%q, want %q (system-notification part must not surface)", got, want)
	}
}

// TestProjectTurnSteeringTextOmitsSystemNotificationParts: a steering turn
// with prose and a machinery note part projects Text == the prose.
func TestProjectTurnSteeringTextOmitsSystemNotificationParts(t *testing.T) {
	turn := schema.Turn{
		Kind: schema.TurnSteering,
		Message: llm.Message{
			Role: llm.RoleUser,
			Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "Please continue with the screenshot."},
				{Kind: llm.ContentText, Text: notificationNote},
			},
		},
	}
	items := ProjectTurn("turn_1", 0, turn, map[string]string{}, nil, nil)
	if len(items) != 1 {
		t.Fatalf("items=%+v, want 1 steering item", items)
	}
	if items[0].Type != "steering" {
		t.Fatalf("item type=%q, want steering", items[0].Type)
	}
	if got, want := items[0].Text, "Please continue with the screenshot."; got != want {
		t.Fatalf("steering text=%q, want %q (system-notification part must not surface)", got, want)
	}
}

// TestProjectTurnUserInputKeepsTextMentioningSystemNotificationTags: skipping
// is exact-block only. A user message that merely mentions the tags inline is
// the user's own words and must survive the projection untouched.
func TestProjectTurnUserInputKeepsTextMentioningSystemNotificationTags(t *testing.T) {
	prose := "the transcript may contain <system-notification>blocks</system-notification> mid-sentence"
	turn := schema.Turn{
		Kind:    schema.TurnUserInput,
		Message: llm.User(prose),
	}
	items := ProjectTurn("turn_1", 0, turn, map[string]string{}, nil, nil)
	if len(items) != 1 {
		t.Fatalf("items=%+v, want 1 userMessage item", items)
	}
	if got, want := items[0].Text, prose; got != want {
		t.Fatalf("user message text=%q, want the inline mention preserved verbatim %q", got, want)
	}
}

// TestProjectTurnUserInputImageOnlyWithNoteProjectsEmptyText: an image-only
// paste carries no user prose, so once the machinery note is skipped the
// bubble is just the image — not a stray notification block.
func TestProjectTurnUserInputImageOnlyWithNoteProjectsEmptyText(t *testing.T) {
	turn := schema.Turn{
		Kind: schema.TurnUserInput,
		Message: llm.Message{
			Role:    llm.RoleUser,
			Content: imageOnlyPlusNoteContent(),
		},
	}
	items := ProjectTurn("turn_1", 0, turn, map[string]string{}, nil, nil)
	if len(items) != 1 {
		t.Fatalf("items=%+v, want 1 userMessage item", items)
	}
	if items[0].Text != "" {
		t.Fatalf("user message text=%q, want empty (image-only turn; note must not surface)", items[0].Text)
	}
}

// TestProjectTurnSteeringMachineryOnlyKeepsFullText: a steering turn whose
// only content is one machinery block is a real production shape — the
// cancelled-callback-watches notice routes exactly that text to a session —
// and the live stream projects its text as-is, so reload must keep the item,
// not strip it to nothing.
func TestProjectTurnSteeringMachineryOnlyKeepsFullText(t *testing.T) {
	turn := schema.Turn{
		Kind: schema.TurnSteering,
		Message: llm.Message{
			Role:    llm.RoleUser,
			Content: []llm.ContentPart{{Kind: llm.ContentText, Text: notificationNote}},
		},
	}
	items := ProjectTurn("turn_1", 0, turn, map[string]string{}, nil, nil)
	if len(items) != 1 {
		t.Fatalf("items=%+v, want 1 steering item", items)
	}
	if got, want := items[0].Text, notificationNote; got != want {
		t.Fatalf("steering text=%q, want the full machinery block %q (machinery-only steering must not vanish on reload)", got, want)
	}
}
