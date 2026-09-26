package agent

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// Every producer that manufactures machinery notification content for a
// persisted message must set the part's Machinery flag at construction:
// display-side filtering (apptranscript.UserFacingText) matches the flag
// alone, so an unflagged machinery note would leak into reloaded bubbles —
// and an unflagged exact-block part is indistinguishable from a user pasting
// the same block verbatim. These tests pin each producer site.

// TestBuildUserInputMessageFlagsPersistedAttachmentNote: the stored-path
// note appended to an image-bearing input is session machinery, while the
// user's prose and the image itself are not.
func TestBuildUserInputMessageFlagsPersistedAttachmentNote(t *testing.T) {
	t.Parallel()
	images := []ImageAttachment{{
		MediaType: "image/png",
		Data:      []byte{0x89, 0x50, 0x4e, 0x47},
		Name:      "shot.png",
		Path:      "/state/sessions/s1/attachments/abcdef0123456789-shot.png",
	}}
	msg := buildUserInputMessage("what is in this picture?", images)
	if len(msg.Content) != 3 {
		t.Fatalf("parts=%+v, want prose, image, note", msg.Content)
	}
	if msg.Content[0].Machinery {
		t.Fatal("user prose must not carry the machinery flag")
	}
	if msg.Content[2].Kind != llm.ContentText || !strings.Contains(msg.Content[2].Text, "shot.png") {
		t.Fatalf("note part=%+v, want the stored-path notification", msg.Content[2])
	}
	if !msg.Content[2].Machinery {
		t.Fatalf("stored-path note part must be flagged as machinery: %+v", msg.Content[2])
	}
}

// TestSteeringMessageToLLMFlagsNotificationText: notification-kind steering
// (e.g. the cancelled-callback-watches restart notice) is session machinery;
// human steering text is the user's own words and must stay unflagged.
func TestSteeringMessageToLLMFlagsNotificationText(t *testing.T) {
	t.Parallel()
	notice := steeringMessage{
		Text: callbackWatchesCancelledAtRestartMessage,
		Kind: events.SteeringKindNotification,
	}
	msg := steeringMessageToLLM(notice)
	if len(msg.Content) != 1 || msg.Content[0].Kind != llm.ContentText {
		t.Fatalf("notification steering parts=%+v, want one text part", msg.Content)
	}
	if !msg.Content[0].Machinery {
		t.Fatalf("notification steering text must be flagged as machinery: %+v", msg.Content[0])
	}

	human := steeringMessage{Text: "look at this", Source: "user"}
	humanMsg := steeringMessageToLLM(human)
	if len(humanMsg.Content) != 1 {
		t.Fatalf("human steering parts=%+v", humanMsg.Content)
	}
	if humanMsg.Content[0].Machinery {
		t.Fatal("human steering text must not be flagged as machinery")
	}
}

// TestSkillDeliveryMessageKeepsNoteAndBodyInOneFlaggedPart: the
// changed-on-disk carrier is one machinery-flagged part carrying the note
// and the body concatenated exactly as before, so the wire text stays
// byte-identical on every adapter — including chatcompletions'
// textFromParts, which joins separate text parts with an extra newline.
func TestSkillDeliveryMessageKeepsNoteAndBodyInOneFlaggedPart(t *testing.T) {
	t.Parallel()
	note := systemNotificationf("Skill %q changed on disk since its earlier activation; the complete current instructions follow.", "brioche")
	body := "# Brioche skill\nButter the pan."
	msg := skillDeliveryMessage(note, body)
	if len(msg.Content) != 1 {
		t.Fatalf("parts=%+v, want the note and body in one part", msg.Content)
	}
	if msg.Content[0].Text != note+"\n\n"+body || !msg.Content[0].Machinery {
		t.Fatalf("carrier part=%+v, want the flagged concatenation note+\\n\\n+body", msg.Content[0])
	}
	if got, want := msg.Text(), note+"\n\n"+body; got != want {
		t.Fatalf("model-facing text changed: got %q, want %q", got, want)
	}

	plain := skillDeliveryMessage("", body)
	if len(plain.Content) != 1 || plain.Content[0].Text != body || plain.Content[0].Machinery {
		t.Fatalf("empty-note parts=%+v, want one ordinary unflagged body part", plain.Content)
	}
	if plain.Text() != body {
		t.Fatalf("empty-note text=%q, want the body verbatim", plain.Text())
	}
}

// TestRecordSkillReloadNotificationFlagsNotice: the reload explanations
// (failure, reuse, context-budget) are block-wrapped machinery; the
// recorded turn must flag its part.
func TestRecordSkillReloadNotificationFlagsNotice(t *testing.T) {
	t.Parallel()
	s := newTestSessionForEnvctx(t)
	notice := skillReloadReuseNotification("brioche")
	outcome := schema.SkillActivationOutcome{Status: "failed"}
	if err := s.recordSkillReloadNotification(notice, outcome, nil); err != nil {
		t.Fatalf("recordSkillReloadNotification: %v", err)
	}
	s.mu.Lock()
	last := s.history[len(s.history)-1]
	s.mu.Unlock()
	if len(last.Message.Content) != 1 || !last.Message.Content[0].Machinery {
		t.Fatalf("recorded reload notice part=%+v, want one flagged machinery part", last.Message.Content)
	}
	if last.Message.Text() != notice {
		t.Fatalf("recorded text=%q, want the notice verbatim", last.Message.Text())
	}
}
