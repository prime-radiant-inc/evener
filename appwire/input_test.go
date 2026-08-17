package appwire

import (
	"slices"
	"testing"
)

func TestNormalizeMutationInputRejectsNonCanonicalTypes(t *testing.T) {
	for _, itemType := range []string{"", "input_text", "input_image", "audio"} {
		t.Run(itemType, func(t *testing.T) {
			if _, err := NormalizeMutationInput([]InputItem{{Type: itemType, Text: "legacy"}}); err == nil {
				t.Fatalf("NormalizeMutationInput accepted type %q", itemType)
			}
		})
	}
}

func TestNormalizeMutationInputPreservesCanonicalPayload(t *testing.T) {
	input := []InputItem{
		{Type: "text", Text: " \n "},
		{Type: "text", Text: "canonical text"},
		{Type: "image", MediaType: "image/png", Data: []byte{1, 2, 3}, Name: "proof.png", Metadata: map[string]string{"source": "test"}},
	}
	normalized, err := NormalizeMutationInput(input)
	if err != nil {
		t.Fatalf("NormalizeMutationInput: %v", err)
	}
	if !normalized.HasContent() || len(normalized.Items) != 2 {
		t.Fatalf("normalized input = %#v", normalized.Items)
	}
	text, image := normalized.Items[0], normalized.Items[1]
	if text.Type != "text" || text.Text != "canonical text" ||
		image.Type != "image" || image.MediaType != "image/png" || image.Name != "proof.png" ||
		!slices.Equal(image.Data, []byte{1, 2, 3}) || image.Metadata["source"] != "test" {
		t.Fatalf("normalized input = %#v", normalized.Items)
	}
	input[2].Data[0] = 9
	input[2].Metadata["source"] = "mutated"
	if normalized.Items[1].Data[0] != 1 || normalized.Items[1].Metadata["source"] != "test" {
		t.Fatalf("normalized input aliases caller: %#v", normalized.Items[1])
	}
}
