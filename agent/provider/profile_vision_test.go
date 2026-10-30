package provider

import (
	"slices"
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
		// A pdf-declaring row takes a user-message document when its protocol
		// represents one: the Responses builder writes input_file and the
		// Anthropic builder writes a document content block (#4209), while
		// Google writes inlineData. Chat silently strips documents.
		{"anthropic/claude-mythos-preview", true},
		{"respdoc/doc-1", true},
		{"respdoc/vis-1", false},
		{"chatdoc/doc-1", false},
		{"anthdoc/doc-1", true},
		{"googdoc/doc-1", true},
		{"googdoc/vis-1", false},
		{"google/gemini-2.5-flash", true},
	}
	for _, tc := range cases {
		p := mustResolve(t, r, tc.ref)
		if got := p.AcceptsDocumentInput(); got != tc.acceptsDocumentInput {
			t.Errorf("%s: AcceptsDocumentInput = %v, want %v", tc.ref, got, tc.acceptsDocumentInput)
		}
	}
}

func TestProfileDocumentInputRequiresTheModelsOwnRow(t *testing.T) {
	t.Parallel()
	p := mustResolve(t, fixtureRegistryWith(t, documentCapabilityInstances()), "googdoc/doc-1")
	if !p.AcceptsDocumentInput() {
		t.Fatal("the Google row's own PDF declaration must accept documents")
	}
	for _, ref := range []string{"unknown-model-4463", "unconfigured-provider/model-4463"} {
		if p.WithModel(ref).AcceptsDocumentInput() {
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

// A gateway namespaces the model ids it serves, so a slashed ref may name a
// row on this instance rather than another instance. Facts resolution must
// classify refs exactly like WithModel does: a genuine row on this instance
// wins over the instance the prefix names.
func TestResolveModelFactsForRefClassifiesLikeWithModel(t *testing.T) {
	t.Parallel()
	r := fixtureRegistryWith(t, map[string]registry.Provider{
		"gw": {
			Surface:       registry.SurfaceOpenAI,
			Protocol:      registry.ProtocolOpenAIResponses,
			APIKey:        "k",
			DefaultModel:  "vis-1",
			Transport:     registry.Transport{BaseURL: "http://gw.test.invalid/v1"},
			InheritModels: new(false),
			Models: map[string]registry.Model{
				"vis-1":                     {Caps: registry.Caps{InputModalities: []string{"text", "image"}}},
				"anthropic/claude-opus-4-5": {Caps: registry.Caps{InputModalities: []string{"text"}}},
				// Disabled rows never win the current-instance match: the
				// runner's own classification refuses them, so the ref names
				// the prefix's instance.
				"anthropic/claude-3-haiku": {Caps: registry.Caps{InputModalities: []string{"text", "image"}}, Disabled: new(true)},
			},
		},
	})
	p := mustResolve(t, r, "gw/vis-1")

	// A namespaced id this gateway serves resolves to the gateway's own
	// row, not the first-party row the prefix also names.
	res, err := p.ResolveModelFactsForRef("anthropic/claude-opus-4-5")
	if err != nil || res.Synthesized {
		t.Fatalf("namespaced ref: err=%v synthesized=%v", err, res.Synthesized)
	}
	if res.Instance != "gw" {
		t.Fatalf("namespaced ref instance = %q, want gw", res.Instance)
	}
	if got := res.Caps.InputModalities; !slices.Equal(got, []string{"text"}) {
		t.Fatalf("namespaced ref modalities = %v, want the gateway's own text-only row", got)
	}

	// A disabled namespaced row does not win the match even though its facts
	// resolve: the ref resolves to the prefix's instance.
	res, err = p.ResolveModelFactsForRef("anthropic/claude-3-haiku")
	if err != nil {
		t.Fatalf("disabled namespaced ref: err=%v", err)
	}
	if res.Instance != "anthropic" {
		t.Fatalf("disabled namespaced ref instance = %q, want anthropic", res.Instance)
	}

	// A slashed ref that is not a row on this instance names the instance its
	// prefix names, resolved there (a synthesized row when unknown —
	// conservative either way).
	res, err = p.ResolveModelFactsForRef("anthropic/claude-opus-5-no-such-row")
	if err != nil {
		t.Fatalf("unknown namespaced ref: err=%v", err)
	}
	if res.Instance != "anthropic" {
		t.Fatalf("unknown namespaced ref instance = %q, want anthropic", res.Instance)
	}

	// Cross-instance, own-prefixed, and bare refs keep their WithModel
	// meanings.
	if res, err := p.ResolveModelFactsForRef("openai/gpt-5.2"); err != nil || res.Instance != "openai" {
		t.Fatalf("cross-instance ref: err=%v instance=%q", err, res.Instance)
	}
	if res, err := p.ResolveModelFactsForRef("gw/vis-1"); err != nil || res.Instance != "gw" {
		t.Fatalf("own-prefixed ref: err=%v instance=%q", err, res.Instance)
	}
	if res, err := p.ResolveModelFactsForRef("vis-1"); err != nil || res.Instance != "gw" {
		t.Fatalf("bare ref: err=%v instance=%q", err, res.Instance)
	}
}
