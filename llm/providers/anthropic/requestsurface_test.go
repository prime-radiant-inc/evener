package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// tool_choice mode "none" must drop the tools and emit no tool_choice at all.
func TestBuildBodyToolChoiceNoneDropsTools(t *testing.T) {
	req := protoReq("")
	req.Tools = []llm.ToolDefinition{{Name: "f"}}
	req.ToolChoice = &llm.ToolChoice{Mode: "none"}
	body, err := (&Protocol{}).BuildBody(req, protoRes(nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, has := body["tools"]; has {
		t.Fatalf("tool_choice none must drop the tools: %#v", body["tools"])
	}
	if _, has := body["tool_choice"]; has {
		t.Fatalf("tool_choice none must not emit a tool_choice: %#v", body["tool_choice"])
	}
}

func TestBuildBodyNamedToolChoiceRequiresName(t *testing.T) {
	req := protoReq("")
	req.Tools = []llm.ToolDefinition{{Name: "f"}}
	req.ToolChoice = &llm.ToolChoice{Mode: "named"}
	_, err := (&Protocol{}).BuildBody(req, protoRes(nil))
	if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok {
		t.Fatalf("named tool_choice without a name must be a configuration error, got %T %v", err, err)
	}
}

func TestApplyAnthropicResponseFormat(t *testing.T) {
	const schemaJSON = `{"properties":{"x":{"type":"string"}},"type":"object"}`
	for _, tc := range []struct {
		name string
		rf   *llm.ResponseFormat
		want string
	}{
		{"nil is a pass-through", nil, "sys prompt"},
		{"empty type is a pass-through", &llm.ResponseFormat{Type: ""}, "sys prompt"},
		{"text type is a pass-through", &llm.ResponseFormat{Type: "text"}, "sys prompt"},
		{"unknown type is a pass-through", &llm.ResponseFormat{Type: "yaml"}, "sys prompt"},
		{"json_schema without a schema is a pass-through", &llm.ResponseFormat{Type: "json_schema"}, "sys prompt"},
		{"json appends the instruction", &llm.ResponseFormat{Type: "json"}, strings.TrimSpace("sys prompt" + "\n\nOutput only valid JSON. Do not include any extra text.")},
		{"json_schema appends the marshaled schema", &llm.ResponseFormat{Type: "json_schema", JSONSchema: map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "string"}}}},
			strings.TrimSpace("sys prompt" + "\n\nOutput only valid JSON that matches this JSON Schema. Do not include any extra text.\n\nJSON Schema:\n" + schemaJSON)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := applyAnthropicResponseFormat("sys prompt", tc.rf)
			if err != nil {
				t.Fatalf("applyAnthropicResponseFormat: %v", err)
			}
			if got != tc.want {
				t.Fatalf("system = %q, want %q", got, tc.want)
			}
		})
	}

	// A schema JSON cannot marshal (a channel value) must fail the build, not
	// silently drop the format.
	t.Run("unmarshalable schema fails the build", func(t *testing.T) {
		req := protoReq("")
		req.ResponseFormat = &llm.ResponseFormat{Type: "json_schema", JSONSchema: map[string]any{"ch": make(chan int)}}
		_, err := applyAnthropicResponseFormat("sys", req.ResponseFormat)
		if err == nil {
			t.Fatal("an unmarshalable schema must error")
		}
		if _, err := (&Protocol{}).BuildBody(req, protoRes(nil)); err == nil {
			t.Fatal("BuildBody must propagate the response-format error")
		}
	})
}

// beta_headers ride the HTTP header, never the request body: the ProviderOptions
// overlay must skip the key while still merging its siblings.
func TestBuildBodySkipsBetaHeadersKeyInBody(t *testing.T) {
	req := protoReq("")
	req.ProviderOptions = map[string]any{registry.ProtocolAnthropic: map[string]any{
		"beta_headers": "context-1m-2025-08-07",
		"custom_key":   7,
	}}
	body, err := (&Protocol{}).BuildBody(req, protoRes(nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, has := body["beta_headers"]; has {
		t.Fatalf("beta_headers must never land in the body: %#v", body["beta_headers"])
	}
	if body["custom_key"] != 7 {
		t.Fatalf("sibling provider options must still merge: %#v", body)
	}
}

// A provider_options.anthropic value that is not an object contributes no beta
// header and must not break the call.
func TestCompleteBetaHeaderFromNonObjectProviderOptions(t *testing.T) {
	srv, got := protoServer(t, func(*http.Request) (int, string) { return 200, messagesJSON })
	res := protoLive(srv)
	res.Headers = nil // no ambient anthropic-beta from the resolved headers
	req := protoReq("")
	req.ProviderOptions = map[string]any{registry.ProtocolAnthropic: "not-an-object"}
	if _, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), req, res); err != nil {
		t.Fatal(err)
	}
	if got.header.Get("anthropic-beta") != "" {
		t.Fatalf("a non-object provider_options.anthropic must contribute no beta header: %v", got.header)
	}
}

// The thinking budget the completion contract reconciles can arrive as any
// JSON-decoded numeric shape; int64 and json.Number must both size the
// ContextBudgetError the same way.
func TestBuildBodyThinkingBudgetNumericTypes(t *testing.T) {
	for name, budget := range map[string]any{
		"int64":       int64(2000),
		"json.Number": json.Number("2000"),
	} {
		t.Run(name, func(t *testing.T) {
			req := protoReq("")
			req.MaxTokens = new(100)
			req.ProviderOptions = map[string]any{registry.ProtocolAnthropic: map[string]any{
				"thinking": map[string]any{"type": "enabled", "budget_tokens": budget},
			}}
			_, err := (&Protocol{}).BuildBody(req, protoRes(nil))
			cbe, ok := errors.AsType[*llm.ContextBudgetError](err)
			if !ok {
				t.Fatalf("max_tokens below the thinking budget must fail as a ContextBudgetError, got %T %v", err, err)
			}
			if cbe.OutputTokens != 2001 || cbe.Maximum != 100 {
				t.Fatalf("ContextBudgetError = %+v, want required 2001 above the ceiling 100", cbe)
			}
		})
	}
}

func TestAnthropicImageBlockLocalAndRefusalArms(t *testing.T) {
	dir := t.TempDir()
	noExt := filepath.Join(dir, "shot")
	if err := os.WriteFile(noExt, []byte("\x89PNG\r\n\x1a\nbytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("extension-less local read defaults to image/png", func(t *testing.T) {
		block, err := anthropicImageBlock(llm.ContentPart{Kind: llm.ContentImage, Image: &llm.ImageData{URL: noExt}})
		if err != nil {
			t.Fatal(err)
		}
		src := block["source"].(map[string]any)
		if src["media_type"] != "image/png" || src["type"] != "base64" {
			t.Fatalf("an extension that infers no media type must fall back to image/png: %#v", src)
		}
	})

	var onePx bytes.Buffer
	if err := png.Encode(&onePx, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	t.Run("inline bytes with no media type keep the image/png default", func(t *testing.T) {
		block, err := anthropicImageBlock(llm.ContentPart{Kind: llm.ContentImage, Image: &llm.ImageData{Data: onePx.Bytes()}})
		if err != nil {
			t.Fatal(err)
		}
		src := block["source"].(map[string]any)
		if src["media_type"] != "image/png" {
			t.Fatalf("media_type = %v, want image/png", src["media_type"])
		}
	})

	t.Run("undecodable bytes with an unaccepted media type refuse loudly", func(t *testing.T) {
		_, err := anthropicImageBlock(llm.ContentPart{Kind: llm.ContentImage, Image: &llm.ImageData{
			Data: []byte("not-an-image"), MediaType: "image/bmp",
		}})
		cfgErr, ok := errors.AsType[*llm.ConfigurationError](err)
		if !ok {
			t.Fatalf("err = %T %v, want *llm.ConfigurationError", err, err)
		}
		if cfgErr.Cause == nil {
			t.Fatalf("the decoder's own error must ride the cause: %v", err)
		}
	})

	t.Run("image part with neither bytes nor a URL is dropped", func(t *testing.T) {
		block, err := anthropicImageBlock(llm.ContentPart{Kind: llm.ContentImage, Image: &llm.ImageData{}})
		if err != nil || block != nil {
			t.Fatalf("empty image = (%#v, %v), want (nil, nil)", block, err)
		}
	})
}

func TestToAnthropicMessagesDroppedAndRefusedParts(t *testing.T) {
	t.Run("document parts with no document or no payload are dropped", func(t *testing.T) {
		_, messages, err := toAnthropicMessages([]llm.Message{{
			Role: llm.RoleUser,
			Content: []llm.ContentPart{
				{Kind: llm.ContentDocument, Document: nil},
				{Kind: llm.ContentDocument, Document: &llm.DocumentData{}},
			},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if len(messages) != 0 {
			t.Fatalf("messages = %#v, want none: every document part is undecodable and dropped", messages)
		}
	})

	t.Run("tool-result image outside the accepted set refuses loudly", func(t *testing.T) {
		_, _, err := toAnthropicMessages([]llm.Message{{
			Role: llm.RoleTool,
			Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
				ToolCallID: "c1", Content: "ok", ImageData: []byte("junk"), ImageMediaType: "image/bmp",
			}}},
		}})
		if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok {
			t.Fatalf("a tool-result image that cannot be normalized must be a configuration error, got %T %v", err, err)
		}
	})
}
