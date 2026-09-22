package agent

import (
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// ImageAttachment carries a single image attached to user input.
// Data is the raw image bytes; JSON un/marshals it as base64.
type ImageAttachment struct {
	MediaType string `json:"media_type"`     // MIME type, e.g. "image/png"
	Data      []byte `json:"data"`           // raw image bytes (base64 in JSON)
	Name      string `json:"name,omitempty"` // original filename, when known
	// Path is the on-disk location the session persisted these bytes to
	// (agent/image_persist.go), set only when the session has a state
	// directory. In-session-only metadata: clients never send it, the
	// user-input event projection does not carry it, and json:"-" keeps it
	// out of the durable queue snapshots, whose restored entries must stay
	// wire-shaped so a later build re-derives the path for the machine it
	// runs on.
	Path string `json:"-"`
}

// inputHasContent reports whether a durable input's typed parts carry
// content. Prose, image attachments, and selected skill identities all count:
// a selection-only input is content even with no text of its own.
func inputHasContent(text string, images []ImageAttachment, skillNames []string) bool {
	return strings.TrimSpace(text) != "" || len(images) > 0 || len(skillNames) > 0
}

// userInputImagesFromAttachments converts the attachments into the
// UserInputImage slice the USER_INPUT event payload expects,
// returning nil when there are no images.
func userInputImagesFromAttachments(images []ImageAttachment) []events.UserInputImage {
	if len(images) == 0 {
		return nil
	}
	out := make([]events.UserInputImage, 0, len(images))
	for _, img := range images {
		out = append(out, events.UserInputImage{
			MediaType: img.MediaType,
			Data:      img.Data,
			Name:      img.Name,
		})
	}
	return out
}

// buildUserInputMessage constructs the multi-part user message that begins a
// turn. Text becomes a ContentText part (omitted only if empty and at least
// one image is supplied); each image becomes a ContentImage part. Images the
// session persisted to disk add one trailing ContentText part — a
// <system-notification> block naming their stored paths — so the model can
// re-read them later (read_file routes image bytes back into context) even
// after context folding drops the inline parts.
func buildUserInputMessage(input string, images []ImageAttachment) llm.Message {
	if len(images) == 0 {
		return llm.User(input)
	}
	parts := make([]llm.ContentPart, 0, 1+len(images))
	if input != "" {
		parts = append(parts, llm.ContentPart{Kind: llm.ContentText, Text: input})
	}
	for _, img := range images {
		parts = append(parts, llm.ContentPart{
			Kind: llm.ContentImage,
			Image: &llm.ImageData{
				Data:      img.Data,
				MediaType: img.MediaType,
			},
		})
	}
	if note := persistedAttachmentNote(images); note != "" {
		parts = append(parts, llm.MachineryText(note))
	}
	return llm.Message{Role: llm.RoleUser, Content: parts}
}

// persistedAttachmentNote renders the system-notification block naming the
// on-disk paths of the input's persisted attachments. Empty when nothing
// was persisted, so stateless sessions keep today's message shape exactly.
func persistedAttachmentNote(images []ImageAttachment) string {
	var paths []string
	for _, img := range images {
		if img.Path != "" {
			paths = append(paths, img.Path)
		}
	}
	if len(paths) == 0 {
		return ""
	}
	return systemNotificationf("The images attached to this message were saved to disk and can be read again later with the read_file tool:\n%s", strings.Join(paths, "\n"))
}

// skillSelectionMarker is the text part a selection-only input carries on its
// user turn: an empty user message is not representable on every provider,
// and the typed selection itself rides the turn's SkillState, which is what
// replay and UIs read.
func skillSelectionMarker(skillNames []string) string {
	return "[skill selection: " + strings.Join(skillNames, ", ") + "]"
}

// buildSelectedUserInputMessage renders the user turn for an input that may
// carry a durable skill selection. Prose and attachments render as
// themselves; a selection-only input renders the bracketed selection marker so
// the recorded turn is never an empty user message.
func buildSelectedUserInputMessage(input string, images []ImageAttachment, skillNames []string) llm.Message {
	if strings.TrimSpace(input) != "" || len(images) > 0 || len(skillNames) == 0 {
		return buildUserInputMessage(input, images)
	}
	return llm.User(skillSelectionMarker(skillNames))
}

// skillInputNames returns the selection's canonical names, nil-safe for
// inputs without one.
func skillInputNames(input *schema.SkillInputRecord) []string {
	if input == nil {
		return nil
	}
	return input.Names
}
