package provider

import (
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// visionCapabilityInstances are the synthetic rows the native-vision predicate
// needs on top of the fixture set: a chat-protocol row that declares image
// input (the protocol strips tool-result images, so image input alone must not
// read as native vision) and google-protocol rows with and without the
// multimodal-tool-results cap.
func visionCapabilityInstances() map[string]registry.Provider {
	return map[string]registry.Provider{
		"chatvis": {Protocol: registry.ProtocolOpenAIChat, APIKey: "k", DefaultModel: "im-1", Transport: registry.Transport{BaseURL: "http://chatvis.test.invalid/v1"}, InheritModels: new(false), Models: map[string]registry.Model{
			"im-1": {Caps: registry.Caps{InputModalities: []string{"text", "image"}}},
		}},
		"goovis": {Protocol: registry.ProtocolGoogle, APIKey: "k", DefaultModel: "cap-1", Transport: registry.Transport{BaseURL: "http://goovis.test.invalid/v1"}, InheritModels: new(false), Models: map[string]registry.Model{
			"cap-1":   {Caps: registry.Caps{InputModalities: []string{"text", "image"}, MultimodalToolResults: new(true)}},
			"nocap-1": {Caps: registry.Caps{InputModalities: []string{"text", "image"}}},
		}},
	}
}

// documentCapabilityInstances are the synthetic rows the document-input
// predicate needs on top of the fixture set: pdf-declaring rows on every
// protocol, so the row modality and the adapter's user-message representation
// are exercised separately.
func documentCapabilityInstances() map[string]registry.Provider {
	pdfRow := func(protocol string) registry.Provider {
		return registry.Provider{Protocol: protocol, APIKey: "k", DefaultModel: "doc-1", Transport: registry.Transport{BaseURL: "http://docvis.test.invalid/v1"}, InheritModels: new(false), Models: map[string]registry.Model{
			"doc-1": {Caps: registry.Caps{InputModalities: []string{"text", "image", "pdf"}}},
			"vis-1": {Caps: registry.Caps{InputModalities: []string{"text", "image"}}},
		}}
	}
	return map[string]registry.Provider{
		"respdoc": pdfRow(registry.ProtocolOpenAIResponses),
		"chatdoc": pdfRow(registry.ProtocolOpenAIChat),
		"anthdoc": pdfRow(registry.ProtocolAnthropic),
		"googdoc": pdfRow(registry.ProtocolGoogle),
	}
}

func TestProfileImageInputPredicates(t *testing.T) {
	t.Parallel()
	r := fixtureRegistryWith(t, visionCapabilityInstances())

	cases := []struct {
		ref               string
		mediaType         string
		acceptsImageInput bool
		seesMedia         bool
	}{
		// A responses-protocol row that declares image input: the adapter
		// embeds tool-result images, so the session model sees image media
		// natively.
		{"openai/gpt-5.2", "image/png", true, true},
		// An empty media type reads as the tool layer's image default.
		{"openai/gpt-5.2", "", true, true},
		// Image input alone does not deliver a document: no adapter puts PDF
		// tool-result bytes in front of the model.
		{"openai/gpt-5.2", "application/pdf", true, false},
		// No declared image input: not vision-capable either way.
		{"openai/gpt-5.2-codex", "image/png", false, false},
		// Chat protocol strips tool-result images, so image input alone does
		// not give the session model native access to tool-result bytes.
		{"chatvis/im-1", "image/png", true, false},
		// Google emits tool-result images only when the row declares the cap.
		{"goovis/cap-1", "image/png", true, true},
		{"goovis/nocap-1", "image/png", true, false},
	}
	for _, tc := range cases {
		p := mustResolve(t, r, tc.ref)
		if got := p.AcceptsImageInput(); got != tc.acceptsImageInput {
			t.Errorf("%s: AcceptsImageInput = %v, want %v", tc.ref, got, tc.acceptsImageInput)
		}
		if got := p.SeesToolResultMedia(tc.mediaType); got != tc.seesMedia {
			t.Errorf("%s: SeesToolResultMedia(%q) = %v, want %v", tc.ref, tc.mediaType, got, tc.seesMedia)
		}
	}
}

func TestProfileDocumentInputPredicate(t *testing.T) {
	t.Parallel()
	r := fixtureRegistryWith(t, documentCapabilityInstances())

	cases := []struct {
		ref                  string
		acceptsDocumentInput bool
	}{
		// Real rows: a Responses row that declares pdf input takes a
		// user-message document; the image-only row does not.
		{"openai/gpt-5.4", true},
		{"openai/gpt-5.2", false},
		// A pdf-declaring row on a protocol whose builder rejects or strips
		// user-message documents still cannot take one: the Anthropic and
		// Google builders reject the kind, chat silently strips it, so only
		// the Responses builder's input_file representation delivers.
		{"anthropic/claude-mythos-preview", false},
		{"respdoc/doc-1", true},
		{"respdoc/vis-1", false},
		{"chatdoc/doc-1", false},
		{"anthdoc/doc-1", false},
		{"googdoc/doc-1", false},
	}
	for _, tc := range cases {
		p := mustResolve(t, r, tc.ref)
		if got := p.AcceptsDocumentInput(); got != tc.acceptsDocumentInput {
			t.Errorf("%s: AcceptsDocumentInput = %v, want %v", tc.ref, got, tc.acceptsDocumentInput)
		}
	}
}
