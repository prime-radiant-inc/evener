package provider

import (
	"testing"

	"primeradiant.com/evener/llm"
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
		// Image input alone does not deliver a document on the Responses
		// protocol: its tool-result shape carries no document (Anthropic does,
		// #4465).
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
		ref   string
		bytes bool
		url   bool
	}{
		// Real rows: a Responses row that declares pdf input takes a
		// user-message document; the image-only row does not.
		{"openai/gpt-5.4", true, true},
		{"openai/gpt-5.2", false, false},
		// A pdf-declaring row takes a user-message document when its protocol
		// represents one: the Responses builder writes input_file and the
		// Anthropic builder writes a document content block (#4209), while
		// Google writes inlineData. The Chat builder writes a file part for
		// inline bytes only — chat has no URL file mechanism.
		{"anthropic/claude-mythos-preview", true, true},
		{"respdoc/doc-1", true, true},
		{"respdoc/vis-1", false, false},
		{"chatdoc/doc-1", true, false},
		{"anthdoc/doc-1", true, true},
		{"googdoc/doc-1", true, true},
		{"googdoc/vis-1", false, false},
		{"google/gemini-2.5-flash", true, true},
	}
	for _, tc := range cases {
		p := mustResolve(t, r, tc.ref)
		if got := p.AcceptsDocumentInput(&llm.DocumentData{Data: []byte("pdf-bytes"), MediaType: "application/pdf"}); got != tc.bytes {
			t.Errorf("%s: AcceptsDocumentInput(bytes) = %v, want %v", tc.ref, got, tc.bytes)
		}
		if got := p.AcceptsDocumentInput(&llm.DocumentData{URL: "https://example.com/doc.pdf"}); got != tc.url {
			t.Errorf("%s: AcceptsDocumentInput(url) = %v, want %v", tc.ref, got, tc.url)
		}
	}
}

func TestProfileDocumentInputRequiresTheModelsOwnRow(t *testing.T) {
	t.Parallel()
	p := mustResolve(t, fixtureRegistryWith(t, documentCapabilityInstances()), "googdoc/doc-1")
	if !p.AcceptsDocumentInput(&llm.DocumentData{Data: []byte("pdf-bytes"), MediaType: "application/pdf"}) {
		t.Fatal("the Google row's own PDF declaration must accept documents")
	}
	for _, ref := range []string{"unknown-model-4463", "unconfigured-provider/model-4463"} {
		if p.WithModel(ref).AcceptsDocumentInput(&llm.DocumentData{Data: []byte("pdf-bytes"), MediaType: "application/pdf"}) {
			t.Errorf("%s inherited another model's PDF support", ref)
		}
	}
}

// The vision predicates require the model's own row: a model id applied over
// another row (withModelID, the WithModel fallback for refs that name another
// provider with no resolver behind them) inherits the source row's facts, and
// inherited image input is not evidence the named model sees media. An unknown
// same-provider model synthesizes a row with no modalities of its own, which
// is conservative without the marker.
func TestSeesToolResultMediaRequiresTheModelsOwnRow(t *testing.T) {
	t.Parallel()
	p := NewOpenAIProfile("gpt-5.2")
	if !p.SeesToolResultMedia("image/png") {
		t.Fatal("the row's own image input must still see tool-result media")
	}
	if p.WithModel("anthropic/claude-x").SeesToolResultMedia("image/png") {
		t.Fatal("row facts inherited across a cross-provider ref vouched for the named model")
	}
	if p.WithModel("gpt-5.2-not-in-catalog").SeesToolResultMedia("image/png") {
		t.Fatal("a synthesized no-modality row vouched for tool-result media")
	}
}
