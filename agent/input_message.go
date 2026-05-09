package agent

import "primeradiant.com/serf/llm"

// ImageAttachment carries a single image attached to user input.
// Data is the raw image bytes; JSON un/marshals it as base64.
type ImageAttachment struct {
	MediaType string `json:"media_type"`
	Data      []byte `json:"data"`
	Name      string `json:"name,omitempty"`
}

// userInputImagesFromAttachments shapes the slice the way the
// USER_INPUT event payload expects (smaller field set than the
// internal type and conventionally nil-when-empty).
func userInputImagesFromAttachments(images []ImageAttachment) []UserInputImage {
	if len(images) == 0 {
		return nil
	}
	out := make([]UserInputImage, 0, len(images))
	for _, img := range images {
		out = append(out, UserInputImage{
			MediaType: img.MediaType,
			Data:      img.Data,
			Name:      img.Name,
		})
	}
	return out
}

// buildUserInputMessage constructs the multi-part user message that begins a
// turn. Text becomes a ContentText part (omitted only if empty and at least
// one image is supplied); each image becomes a ContentImage part.
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
	return llm.Message{Role: llm.RoleUser, Content: parts}
}
