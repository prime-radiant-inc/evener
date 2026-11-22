package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// Keep routing, capability checks, and Google serialization real. Only the
// external provider is scripted: inspect the describing request and the later
// serving request to catch a PDF delivered both as a document and tool media.
func TestDescribeImage_GooglePDFRoutingAndSingleDelivery(t *testing.T) {
	t.Parallel()
	for _, route := range []string{"session", "refusal", "failure"} {
		t.Run(route, func(t *testing.T) {
			t.Parallel()
			type wireContent struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				MIMEType string `json:"mime_type"`
				Data     string `json:"data"`
			}
			type wireStep struct {
				Type    string        `json:"type"`
				Content []wireContent `json:"content"`
				Name    string        `json:"name"`
				CallID  string        `json:"call_id"`
				Result  any           `json:"result"`
			}
			type wireRequest struct {
				Input []wireStep `json:"input"`
			}
			requests := make(chan wireRequest, 2)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					_, _ = w.Write([]byte(`{"models":[{"name":"models/pdf-model"}]}`))
					return
				}
				var body wireRequest
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				requests <- body
				_, _ = w.Write([]byte(`{"id":"int_pdf","model":"pdf-model","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"pdf-result-4463"}]}]}`))
			}))
			t.Cleanup(srv.Close)
			configured := newFakeErrAdapter("pdfroute", []func(llm.Request) (llm.Response, error){
				func(llm.Request) (llm.Response, error) {
					if route == "refusal" {
						return llm.Response{}, visionRouteRefusal("pdfroute")
					}
					return llm.Response{}, visionRouteError("pdfroute")
				},
			})
			dir := t.TempDir()
			client := registryClientAt(t, dir, map[string]registry.Provider{
				// The configured vision route is deliberately a document-delivering
				// protocol: these subroutes pin the failure→session-model retry
				// choreography, and a chat route never gets that far — the
				// side-channel declines to send a document its adapter strips
				// (#5472), which session_vision_chat_document_test.go holds.
				"pdfroute": {
					Base: "anthropic", APIKey: "test",
					Transport: registry.Transport{BaseURL: "http://pdfroute.test.invalid"},
				},
				"pdfgoogle": {
					Base: "google", APIKey: "test", DefaultModel: "pdf-model", InheritModels: new(false),
					Transport: registry.Transport{BaseURL: srv.URL + "/v1beta"},
					Models: map[string]registry.Model{"pdf-model": {Caps: registry.Caps{
						InputModalities: []string{"text", "image", "pdf"}, MultimodalToolResults: new(true),
					}}},
				},
			}, []string{"pdfgoogle"}, configured)
			cfg := SessionConfig{StateDir: dir}
			if route != "session" {
				cfg.VisionModel = "pdfroute/model-4463"
			}
			sess := newSession(t, withClient(client), withProfile(resolveClientProfile(t, client, "pdfgoogle/pdf-model")), withConfig(cfg))
			pdf := []byte("%PDF-1.4 pdf-data-4463")
			result := sess.describeImageCall(context.Background(), tool.ExecResult{ImageData: pdf, ImageMediaType: "application/pdf"})
			if result.outcome != visionSideChannelSuccess || result.description != "pdf-result-4463" {
				t.Fatalf("route=%s: result=%+v, want Google PDF description", route, result)
			}
			wantConfiguredCalls := 0
			if route != "session" {
				wantConfiguredCalls = 1
			}
			if got := len(configured.Requests()); got != wantConfiguredCalls {
				t.Fatalf("configured-route calls=%d, want %d", got, wantConfiguredCalls)
			}
			if len(requests) != 1 {
				t.Fatalf("Google describe requests=%d, want 1", len(requests))
			}
			description := <-requests
			if len(description.Input) != 1 || description.Input[0].Type != "user_input" || len(description.Input[0].Content) != 2 {
				t.Fatalf("describe request=%+v", description)
			}
			media := description.Input[0].Content[1]
			if media.Type != "document" || media.MIMEType != "application/pdf" {
				t.Fatalf("PDF document item=%+v", media)
			}
			decoded, err := base64.StdEncoding.DecodeString(media.Data)
			if err != nil || !bytes.Equal(decoded, pdf) {
				t.Fatalf("PDF bytes changed: decoded=%q err=%v", decoded, err)
			}

			retained := &llm.ToolResultData{ToolCallID: "call-4463", Name: "read_file", Content: "tool-result-4463", ImageData: pdf, ImageMediaType: "application/pdf"}
			_, err = client.Complete(context.Background(), llm.Request{Provider: "pdfgoogle", Model: "pdf-model", Messages: []llm.Message{
				{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call-4463", Name: "read_file", Arguments: json.RawMessage(`{}`)}}}},
				{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: retained}}},
				llm.User(result.description),
			}})
			if err != nil {
				t.Fatal(err)
			}
			if len(requests) != 1 {
				t.Fatalf("Google serving requests=%d, want 1", len(requests))
			}
			serving := <-requests
			responses, descriptions := 0, 0
			for _, step := range serving.Input {
				switch step.Type {
				case "function_result":
					responses++
					if step.Name != "read_file" || step.Result != "tool-result-4463" {
						t.Fatalf("tool response=%+v, want string result without binary parts", step)
					}
				case "user_input":
					for _, item := range step.Content {
						if item.Type == "document" || item.Type == "image" {
							t.Fatal("PDF was delivered again in the serving request")
						}
						if item.Text == result.description {
							descriptions++
						}
					}
				}
			}
			if responses != 1 || descriptions != 1 || !bytes.Equal(retained.ImageData, pdf) {
				t.Fatalf("responses=%d descriptions=%d retainedPDF=%q", responses, descriptions, retained.ImageData)
			}
		})
	}
}
