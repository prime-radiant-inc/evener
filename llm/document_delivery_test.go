package llm

import (
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// TestDeliversUserMessageDocuments pins which protocols represent a document
// part in a user message, per payload source. Inline bytes ride every
// document-capable wire: the Anthropic builder maps a ContentDocument to the
// document content block (the Messages API accepts base64 application/pdf
// user messages, no beta header), the Responses builder writes input_file,
// Google writes inlineData, and the Chat builder writes a file part. A
// URL-only document rides Anthropic's url source, Google's fileData, and the
// Responses pass-through — but chat has no URL file mechanism.
func TestDeliversUserMessageDocuments(t *testing.T) {
	t.Parallel()
	bytesDoc := &DocumentData{Data: []byte("pdf-bytes"), MediaType: "application/pdf"}
	urlDoc := &DocumentData{URL: "https://example.com/doc.pdf"}
	cases := []struct {
		protocol string
		doc      *DocumentData
		want     bool
	}{
		{registry.ProtocolAnthropic, bytesDoc, true},
		{registry.ProtocolOpenAIResponses, bytesDoc, true},
		{registry.ProtocolGoogle, bytesDoc, true},
		{registry.ProtocolOpenAIChat, bytesDoc, true},
		{registry.ProtocolAnthropic, urlDoc, true},
		{registry.ProtocolOpenAIResponses, urlDoc, true},
		{registry.ProtocolGoogle, urlDoc, true},
		{registry.ProtocolOpenAIChat, urlDoc, false},
		{registry.ProtocolOpenAIChat, nil, false},
		{registry.ProtocolOpenAIChat, &DocumentData{MediaType: "application/pdf"}, false},
		{registry.ProtocolOpenAIResponses, &DocumentData{MediaType: "application/pdf"}, false},
		{"some-future-protocol", bytesDoc, false},
	}
	for _, tc := range cases {
		res := registry.Resolved{Protocol: tc.protocol}
		if got := DeliversUserMessageDocuments(res, tc.doc); got != tc.want {
			t.Errorf("DeliversUserMessageDocuments(%q, doc) = %v, want %v", tc.protocol, got, tc.want)
		}
	}
}

func TestRowSeesToolResultMediaStaysImageOnlyForGoogle(t *testing.T) {
	t.Parallel()
	for _, capable := range []bool{false, true} {
		res := registry.Resolved{Protocol: registry.ProtocolGoogle, Caps: registry.Caps{
			InputModalities: []string{"text", "image", "pdf"}, MultimodalToolResults: new(capable),
		}}
		if DeliversToolResultMedia(res, "application/pdf") || RowSeesToolResultMedia(res, "application/pdf") {
			t.Fatalf("cap=%v: PDF tool results must still take the side-channel", capable)
		}
		if got := RowSeesToolResultMedia(res, "image/png"); got != capable {
			t.Fatalf("cap=%v: image delivery=%v", capable, got)
		}
	}
}

// TestRowSeesToolResultDocumentOnAnthropicPDFRows pins the #4465 flip: the
// Anthropic builder writes a document content block inside tool_result, so a
// PDF tool result on a row that declares pdf input is natively seen and the
// side-channel skips it. A row without pdf input, or a protocol whose
// tool-result shape carries no document, still takes the side-channel.
func TestRowSeesToolResultDocumentOnAnthropicPDFRows(t *testing.T) {
	t.Parallel()
	res := registry.Resolved{
		Protocol: registry.ProtocolAnthropic,
		Caps:     registry.Caps{InputModalities: []string{"text", "image", "pdf"}},
	}
	if !RowSeesToolResultMedia(res, "application/pdf") {
		t.Fatal("a PDF tool result on an Anthropic row with pdf input must be natively seen")
	}
	if !RowSeesToolResultMedia(res, "image/png") {
		t.Fatal("an image tool result on an Anthropic row must count as natively seen")
	}
	noPDF := registry.Resolved{
		Protocol: registry.ProtocolAnthropic,
		Caps:     registry.Caps{InputModalities: []string{"text", "image"}},
	}
	if RowSeesToolResultMedia(noPDF, "application/pdf") {
		t.Fatal("an Anthropic row without pdf input must not count a PDF tool result as natively seen")
	}
	synthesized := registry.Resolved{
		Protocol:    registry.ProtocolAnthropic,
		Synthesized: true,
		Caps:        registry.Caps{InputModalities: []string{"text", "image", "pdf"}},
	}
	if RowSeesToolResultMedia(synthesized, "application/pdf") {
		t.Fatal("a synthesized row inherits another row's facts and cannot vouch for a tool-result document")
	}
	for _, protocol := range []string{registry.ProtocolOpenAIResponses, registry.ProtocolOpenAIChat, registry.ProtocolGoogle} {
		res := registry.Resolved{Protocol: protocol, Caps: registry.Caps{InputModalities: []string{"text", "image", "pdf"}}}
		if DeliversToolResultMedia(res, "application/pdf") || RowSeesToolResultMedia(res, "application/pdf") {
			t.Fatalf("%s: a tool-result document must still take the side-channel", protocol)
		}
	}
}

// TestBillsToolResultMedia_AnthropicDocumentBills keeps token estimation honest
// with the wire shape: a PDF tool result on an Anthropic row that declares pdf
// input rides inline and is billed, while a row without pdf input (and every
// protocol that describes documents out of band) bills text only.
func TestBillsToolResultMedia_AnthropicDocumentBills(t *testing.T) {
	t.Parallel()
	tr := &ToolResultData{ImageData: []byte("%PDF-1.4"), ImageMediaType: "application/pdf"}
	cases := []struct {
		name string
		row  registry.Resolved
		want bool
	}{
		{"anthropic with pdf", registry.Resolved{Protocol: registry.ProtocolAnthropic,
			Caps: registry.Caps{InputModalities: []string{"text", "pdf"}}}, true},
		{"anthropic without pdf", registry.Resolved{Protocol: registry.ProtocolAnthropic}, false},
		{"unknown", registry.Resolved{}, true}, // a name-based target cannot be ruled out
	}
	for _, tc := range cases {
		got := targetFromResolved(tc.row, "", "").billsToolResultMedia(tr)
		if got != tc.want {
			t.Errorf("billsToolResultMedia(%s, pdf) = %v, want %v", tc.name, got, tc.want)
		}
	}
}
