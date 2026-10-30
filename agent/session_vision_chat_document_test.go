package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// The #5472 host: a session model served behind an OpenAI-compatible gateway
// (chat completions) whose row declares image input only. The chat file part
// is a gateway extension, not a wire contract, so a row that does not declare
// pdf input cannot vouch that the endpoint speaks it — a document describe
// sent there carries the text prompt only, the provider never sees the PDF,
// answers "no document provided", and the round reports the vision provider
// failed after a full round trip, which agents reading the result took for an
// empty or scanned document.
//
// The side-channel must not send that request at all: a document whose route
// cannot represent it stays undelivered, with no wire round trip, and the
// round steers vision unavailable for the file instead of blaming a provider
// that was handed nothing.

// chatVisionSession builds the #5472 session: an openai-compatible provider
// with a real chat-completions transport behind an httptest server, a row
// that declares image input the way a gateway vision model does, and a live
// instance so dispatch reaches the server. The server records every request
// body it receives; a describe request that carries an image part is answered
// as the gateway would answer a populated vision request, and any request
// without one is answered with the document-less error the real gateway
// produced for the stripped PDF describe.
func chatVisionSession(t *testing.T) (*Session, *[]map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"vision-model","object":"model"}]}`))
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, body)
		mu.Unlock()
		if !chatRequestCarriesImage(body) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"no document provided","type":"invalid_request_error"}}`))
			return
		}
		if stream, _ := body["stream"].(bool); stream {
			w.Header().Set("Content-Type", "text/event-stream")
			chunk := map[string]any{"id": "describe", "object": "chat.completion.chunk", "model": "vision-model", "choices": []any{
				map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": "image-result-5472"}, "finish_reason": "stop"},
			}}
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", mustJSON(t, chunk))
			return
		}
		_, _ = w.Write([]byte(`{"id":"describe","object":"chat.completion","model":"vision-model","choices":[{"index":0,"message":{"role":"assistant","content":"image-result-5472"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	client := registryClientAt(t, dir, map[string]registry.Provider{
		"chatvision": {
			Base: "openai-compatible", APIKey: "test", DefaultModel: "vision-model", InheritModels: new(false),
			Transport: registry.Transport{BaseURL: srv.URL},
			Models: map[string]registry.Model{"vision-model": {Caps: registry.Caps{
				InputModalities: []string{"text", "image"},
			}}},
		},
	}, []string{"chatvision"})
	sess := newSession(t, withClient(client),
		withProfile(resolveClientProfile(t, client, "chatvision/vision-model")),
		withConfig(SessionConfig{StateDir: dir}))
	return sess, &requests
}

// chatRequestCarriesImage reports whether a chat-completions body's user
// content holds any image_url part — the one shape that proves media bytes
// reached the provider on this protocol.
func chatRequestCarriesImage(body map[string]any) bool {
	messages, _ := body["messages"].([]any)
	for _, m := range messages {
		message, _ := m.(map[string]any)
		if content, ok := message["content"].([]any); ok {
			for _, part := range content {
				entry, _ := part.(map[string]any)
				if entry["type"] == "image_url" {
					return true
				}
			}
		}
	}
	return false
}

// chatFileVisionSession mirrors chatVisionSession with the one difference
// that matters for the composed world: the row declares pdf input, vouching
// that this gateway speaks the chat file part. The server answers requests
// carrying a file part as a populated vision request, and any request
// without one with the document-less error — a gateway that implements the
// file part but was handed nothing.
func chatFileVisionSession(t *testing.T) (*Session, *[]map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"vision-model","object":"model"}]}`))
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, body)
		mu.Unlock()
		if !chatRequestCarriesFile(body) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"no document provided","type":"invalid_request_error"}}`))
			return
		}
		if stream, _ := body["stream"].(bool); stream {
			w.Header().Set("Content-Type", "text/event-stream")
			chunk := map[string]any{"id": "describe", "object": "chat.completion.chunk", "model": "vision-model", "choices": []any{
				map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": "file-result-5472"}, "finish_reason": "stop"},
			}}
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", mustJSON(t, chunk))
			return
		}
		_, _ = w.Write([]byte(`{"id":"describe","object":"chat.completion","model":"vision-model","choices":[{"index":0,"message":{"role":"assistant","content":"file-result-5472"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	client := registryClientAt(t, dir, map[string]registry.Provider{
		"chatdocvision": {
			Base: "openai-compatible", APIKey: "test", DefaultModel: "vision-model", InheritModels: new(false),
			Transport: registry.Transport{BaseURL: srv.URL},
			Models: map[string]registry.Model{"vision-model": {Caps: registry.Caps{
				InputModalities: []string{"text", "image", "pdf"},
			}}},
		},
	}, []string{"chatdocvision"})
	sess := newSession(t, withClient(client),
		withProfile(resolveClientProfile(t, client, "chatdocvision/vision-model")),
		withConfig(SessionConfig{StateDir: dir}))
	return sess, &requests
}

// chatRequestCarriesFile reports whether a chat-completions body's user
// content holds any file part — the shape that proves document bytes reached
// the provider on this protocol.
func chatRequestCarriesFile(body map[string]any) bool {
	messages, _ := body["messages"].([]any)
	for _, m := range messages {
		message, _ := m.(map[string]any)
		if content, ok := message["content"].([]any); ok {
			for _, part := range content {
				entry, _ := part.(map[string]any)
				if entry["type"] == "file" {
					return true
				}
			}
		}
	}
	return false
}

// The composed world the file part enables: a chat route whose row declares
// pdf input vouches that the gateway speaks the chat file part, so a PDF
// describe rides the wire as a file part carrying the bytes and the gateway
// answers the description — the #5472 recovery, not a steering message.
func TestDescribeImage_ChatRouteWithPdfInputCarriesFilePart(t *testing.T) {
	t.Parallel()
	sess, requests := chatFileVisionSession(t)

	result := sess.describeImageCall(context.Background(), tool.ExecResult{
		ImageData:      validPDFFixture(t),
		ImageMediaType: "application/pdf",
		ImagePrompt:    "describe the document",
	})
	if result.outcome != visionSideChannelSuccess || result.description != "file-result-5472" {
		t.Fatalf("PDF describe result = %+v, want the scripted description", result)
	}
	if got := len(*requests); got != 1 {
		t.Fatalf("provider requests = %d, want the one file-part describe: %v", got, *requests)
	}
	body := (*requests)[0]
	messages, _ := body["messages"].([]any)
	message, _ := messages[0].(map[string]any)
	content, _ := message["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("describe content = %#v, want a text part and a file part", message["content"])
	}
	file, _ := content[1].(map[string]any)
	fileObj, _ := file["file"].(map[string]any)
	fileData, _ := fileObj["file_data"].(string)
	if file["type"] != "file" || !strings.HasPrefix(fileData, "data:application/pdf;base64,") {
		t.Fatalf("file part = %#v, want the PDF bytes as a data URI", content[1])
	}
}

// A PDF through the failing path never becomes a wire request: the image-only
// row cannot vouch that this gateway speaks the chat file part, so the
// side-channel reports the undeliverable outcome without spending the round
// trip.
func TestDescribeImage_ChatRoutePDFNeverReachesTheWire(t *testing.T) {
	t.Parallel()
	sess, requests := chatVisionSession(t)

	result := sess.describeImageCall(context.Background(), tool.ExecResult{
		ImageData:      validPDFFixture(t),
		ImageMediaType: "application/pdf",
		ImagePrompt:    "describe the document",
	})
	if got := len(*requests); got != 0 {
		t.Fatalf("provider requests = %d, want 0: the document cannot reach a chat-completions describe request; wire body: %v", got, *requests)
	}
	if result.outcome == visionSideChannelSuccess || result.description != "" {
		t.Fatalf("PDF describe unexpectedly succeeded: %+v", result)
	}
	if result.outcome != visionSideChannelUndeliverableDocument {
		t.Fatalf("PDF describe outcome = %v, want the undeliverable-document outcome", result.outcome)
	}
}

// The undeliverable outcome must reach the agent as steering that names the
// file, not as a provider failure that sends the agent chasing an empty or
// scanned document.
func TestPersistToolResults_UndeliverablePDFSteersUnavailableNotFailed(t *testing.T) {
	t.Parallel()
	sess, requests := chatVisionSession(t)
	pdf := validPDFFixture(t)

	calls := []llm.ToolCallData{{
		ID: "call-pdf", Name: "read_file", Type: "function",
		Arguments: json.RawMessage(`{"file_path":"/tmp/report.pdf"}`),
	}}
	results := []tool.ExecResult{{
		CallID: "call-pdf", ToolName: "read_file",
		Output:         "[document: pdf, 48213 bytes, base64 data follows]",
		ImageData:      pdf,
		ImageMediaType: "application/pdf",
	}}
	if err := sess.persistToolResults(context.Background(), calls, results); err != nil {
		t.Fatalf("persistToolResults: %v", err)
	}
	if got := len(*requests); got != 0 {
		t.Fatalf("provider requests = %d, want 0: the document cannot reach a chat-completions describe request; wire body: %v", got, *requests)
	}
	steered := sess.drainSteering()
	if len(steered) != 1 || steered[0].Kind != events.SteeringKindImageDescription {
		t.Fatalf("undeliverable PDF steering = %#v, want one image-description entry", steered)
	}
	if !strings.Contains(steered[0].Text, "/tmp/report.pdf") {
		t.Fatalf("undeliverable PDF steering did not name the file: %q", steered[0].Text)
	}
}

// The gate also covers a vision model the session explicitly configures:
// a chat-protocol route is refused for documents no matter who chose it,
// while the same configured route keeps describing images.
func TestDescribeImage_ConfiguredChatVisionRouteBlocksDocumentsOnly(t *testing.T) {
	t.Parallel()
	sess, requests := chatVisionSession(t)
	sess.cfg.VisionModel = "chatvision/other-model"

	result := sess.describeImageCall(context.Background(), tool.ExecResult{
		ImageData:      validPDFFixture(t),
		ImageMediaType: "application/pdf",
		ImagePrompt:    "describe the document",
	})
	if result.outcome != visionSideChannelUndeliverableDocument {
		t.Fatalf("configured-route PDF outcome = %v, want the undeliverable-document outcome", result.outcome)
	}
	if got := len(*requests); got != 0 {
		t.Fatalf("configured-route provider requests = %d, want 0", got)
	}

	image := sess.describeImageCall(context.Background(), tool.ExecResult{
		ImageData:      validPNGFixture(t),
		ImageMediaType: "image/png",
		ImagePrompt:    "describe the image",
	})
	if image.outcome != visionSideChannelSuccess || image.description != "image-result-5472" {
		t.Fatalf("configured-route image result = %+v, want the scripted description", image)
	}
}

// The gate is document-only: an image through the same chat session still
// describes, over the wire, with the raster bytes in the request.
func TestDescribeImage_ChatRouteStillDescribesImages(t *testing.T) {
	t.Parallel()
	sess, requests := chatVisionSession(t)

	result := sess.describeImageCall(context.Background(), tool.ExecResult{
		ImageData:      validPNGFixture(t),
		ImageMediaType: "image/png",
		ImagePrompt:    "describe the image",
	})
	if result.outcome != visionSideChannelSuccess || result.description != "image-result-5472" {
		t.Fatalf("image describe result = %+v, want the scripted description", result)
	}
	if got := len(*requests); got != 1 {
		t.Fatalf("provider requests = %d, want the one image describe", got)
	}
	body := (*requests)[0]
	messages, _ := body["messages"].([]any)
	message, _ := messages[0].(map[string]any)
	content, _ := message["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("image describe content = %#v, want a text part and an image part", message["content"])
	}
	image, _ := content[1].(map[string]any)
	imageURL, _ := image["image_url"].(map[string]any)
	url, _ := imageURL["url"].(string)
	if image["type"] != "image_url" || !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Fatalf("image part = %#v, want image_url carrying the raster data URI", content[1])
	}
}

// The gate must not strand a document the session model can still describe:
// a configured chat vision route cannot carry a PDF, but a document-capable
// session model can, so the side-channel routes there — the same fallback
// the retry leg gives a failed attempt — instead of steering vision
// unavailable. Only when neither route can represent the document is it
// undeliverable.
func TestDescribeImage_ChatVisionRouteFallsBackToDocumentCapableSessionModel(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	chatRequests := 0
	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		chatRequests++
		mu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"no document provided","type":"invalid_request_error"}}`))
	}))
	t.Cleanup(chatSrv.Close)

	googleRequests := 0
	googleSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"models":[{"name":"models/pdf-model"}]}`))
			return
		}
		mu.Lock()
		googleRequests++
		mu.Unlock()
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"pdf-result-chat-fallback"}]},"finishReason":"STOP"}]}`))
	}))
	t.Cleanup(googleSrv.Close)

	dir := t.TempDir()
	client := registryClientAt(t, dir, map[string]registry.Provider{
		"chatvision": {
			Base: "openai-compatible", APIKey: "test", DefaultModel: "vision-model", InheritModels: new(false),
			Transport: registry.Transport{BaseURL: chatSrv.URL},
			Models: map[string]registry.Model{"vision-model": {Caps: registry.Caps{
				InputModalities: []string{"text", "image"},
			}}},
		},
		"pdfgoogle": {
			Base: "google", APIKey: "test", DefaultModel: "pdf-model", InheritModels: new(false),
			Transport: registry.Transport{BaseURL: googleSrv.URL},
			Models: map[string]registry.Model{"pdf-model": {Caps: registry.Caps{
				InputModalities: []string{"text", "image", "pdf"}, MultimodalToolResults: new(true),
			}}},
		},
	}, []string{"pdfgoogle"})
	cfg := SessionConfig{StateDir: dir, VisionModel: "chatvision/vision-model"}
	sess := newSession(t, withClient(client), withProfile(resolveClientProfile(t, client, "pdfgoogle/pdf-model")), withConfig(cfg))

	result := sess.describeImageCall(context.Background(), tool.ExecResult{
		ImageData:      validPDFFixture(t),
		ImageMediaType: "application/pdf",
		ImagePrompt:    "describe the document",
	})
	if result.outcome != visionSideChannelSuccess || result.description != "pdf-result-chat-fallback" {
		t.Fatalf("PDF describe via document-capable session model = %+v, want the scripted description", result)
	}
	if chatRequests != 0 {
		t.Fatalf("chat route requests = %d, want 0: the document must never be sent to a route that strips it", chatRequests)
	}
	if googleRequests != 1 {
		t.Fatalf("session model requests = %d, want 1", googleRequests)
	}
}
