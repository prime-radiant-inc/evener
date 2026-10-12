package llm

import (
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// TestDeliversUserMessageDocuments pins which protocols represent a document
// part in a user message. The Anthropic builder maps a ContentDocument to the
// document content block (the Messages API accepts base64 application/pdf
// user messages, no beta header), and the Responses builder writes input_file;
// the Google builder rejects the kind and chat strips it.
func TestDeliversUserMessageDocuments(t *testing.T) {
	t.Parallel()
	cases := []struct {
		protocol string
		want     bool
	}{
		{registry.ProtocolAnthropic, true},
		{registry.ProtocolOpenAIResponses, true},
		{registry.ProtocolGoogle, false},
		{registry.ProtocolOpenAIChat, false},
		{"some-future-protocol", false},
	}
	for _, tc := range cases {
		res := registry.Resolved{Protocol: tc.protocol}
		if got := DeliversUserMessageDocuments(res); got != tc.want {
			t.Errorf("DeliversUserMessageDocuments(%q) = %v, want %v", tc.protocol, got, tc.want)
		}
	}
}

// TestRowSeesToolResultMediaStaysImageOnlyForAnthropic guards the side-channel:
// an Anthropic row that declares pdf input must still not count a document
// tool result as natively seen (DeliversToolResultMedia is image-only), so the
// PDF is described out of band rather than skipped. Flipping this predicate
// would silently stop document descriptions on Anthropic routes.
func TestRowSeesToolResultMediaStaysImageOnlyForAnthropic(t *testing.T) {
	t.Parallel()
	res := registry.Resolved{
		Protocol: registry.ProtocolAnthropic,
		Caps:     registry.Caps{InputModalities: []string{"text", "image", "pdf"}},
	}
	if RowSeesToolResultMedia(res, "application/pdf") {
		t.Fatal("a document tool result on an Anthropic row counted as natively seen; the side-channel must still describe it")
	}
	if !RowSeesToolResultMedia(res, "image/png") {
		t.Fatal("an image tool result on an Anthropic row must count as natively seen")
	}
}
