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
// (chat completions). The chat adapter has no user-message document part —
// llm.DeliversUserMessageDocuments is false for the protocol — so a document
// describe request sent there carries the text prompt only. The provider
// never sees the PDF, answers "no document provided", and the round reports
// the vision provider failed after a full round trip, which agents reading
// the result took for an empty or scanned document.
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
		switch content := message["content"].(type) {
		case []any:
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

// A PDF through the failing path never becomes a wire request: the chat
// protocol cannot carry the document, so the side-channel reports the
// undeliverable outcome without spending the round trip.
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
