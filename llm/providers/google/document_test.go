package google

import (
	"encoding/base64"
	"errors"
	"reflect"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func TestToInteractionSteps_UserDocuments(t *testing.T) {
	t.Parallel()
	inline := map[string]any{"type": "document", "mime_type": "application/pdf", "data": "JVBERi0xLjQ="}
	for _, tc := range []struct {
		name string
		doc  *llm.DocumentData
		want map[string]any
	}{
		{"pdf", &llm.DocumentData{Data: []byte("%PDF-1.4"), MediaType: "application/pdf", FileName: "a.pdf"}, inline},
		{"default media", &llm.DocumentData{Data: []byte("%PDF-1.4")}, inline},
		{"blank media", &llm.DocumentData{Data: []byte("%PDF-1.4"), MediaType: " \t"}, inline},
		{"trim media", &llm.DocumentData{Data: []byte("%PDF-1.4"), MediaType: " application/pdf "}, inline},
		{"bytes before uri", &llm.DocumentData{Data: []byte("%PDF-1.4"), URL: "https://example.invalid/a.pdf"}, inline},
		{"declared media", &llm.DocumentData{Data: []byte("%PDF-1.4"), MediaType: "text/plain"}, map[string]any{"type": "document", "mime_type": "text/plain", "data": "JVBERi0xLjQ="}},
		{"file uri", &llm.DocumentData{URL: " https://generativelanguage.googleapis.com/v1beta/files/pdf-4463 "}, map[string]any{"type": "document", "mime_type": "application/pdf", "uri": "https://generativelanguage.googleapis.com/v1beta/files/pdf-4463"}},
		{"nil", nil, nil},
		{"empty", &llm.DocumentData{}, nil},
		{"blank uri", &llm.DocumentData{URL: " \t"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := llm.Request{
				Model: "gemini-3.8-flash",
				Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{
					{Kind: llm.ContentText, Text: "sentinel-before"},
					{Kind: llm.ContentDocument, Document: tc.doc},
					{Kind: llm.ContentImage, Image: &llm.ImageData{Data: []byte{1}, MediaType: "image/png"}},
					{Kind: llm.ContentText, Text: "sentinel-after"},
				}}},
			}
			body, err := (&Protocol{}).BuildBody(req, protoRes(nil))
			if err != nil {
				t.Fatal(err)
			}
			items := []any{map[string]any{"type": "text", "text": "sentinel-before"}}
			if tc.want != nil {
				items = append(items, tc.want)
			}
			items = append(items,
				map[string]any{"type": "image", "mime_type": "image/png", "data": "AQ=="},
				map[string]any{"type": "text", "text": "sentinel-after"},
			)
			want := []any{map[string]any{"type": "user_input", "content": items}}
			if !reflect.DeepEqual(body["input"], want) {
				t.Fatalf("input = %#v, want %#v", body["input"], want)
			}
		})
	}
}

func TestToInteractionSteps_AssistantDocumentsRemainUnsupported(t *testing.T) {
	t.Parallel()
	_, err := (&Protocol{}).BuildBody(llm.Request{
		Model: "gemini-3.8-flash",
		Messages: []llm.Message{{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentDocument, Document: &llm.DocumentData{Data: []byte("%PDF-1.4")}},
		}}},
	}, protoRes(nil))
	if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok {
		t.Fatalf("assistant document: err=%v, want configuration refusal", err)
	}
}

func TestToInteractionSteps_AudioRemainsUnsupported(t *testing.T) {
	t.Parallel()
	for _, role := range []llm.Role{llm.RoleUser, llm.RoleAssistant} {
		_, err := (&Protocol{}).BuildBody(llm.Request{
			Model:    "gemini-3.8-flash",
			Messages: []llm.Message{{Role: role, Content: []llm.ContentPart{{Kind: llm.ContentAudio, Audio: &llm.AudioData{Data: []byte{1}}}}}},
		}, protoRes(nil))
		if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok {
			t.Fatalf("%s audio: %v, want configuration refusal", role, err)
		}
	}
}

func TestToInteractionSteps_ToolDocumentsStayOffTheWire(t *testing.T) {
	t.Parallel()
	for _, capable := range []bool{false, true} {
		for _, mediaType := range []string{"application/pdf", " APPLICATION/PDF ", "text/plain"} {
			result := &llm.ToolResultData{ToolCallID: "call-4463", Name: "read_file", Content: "result-4463", ImageData: []byte("%PDF-1.4"), ImageMediaType: mediaType}
			res := protoRes(func(c *registry.Caps) { c.MultimodalToolResults = new(capable) })
			body, err := (&Protocol{}).BuildBody(llm.Request{
				Model:    "gemini-3.8-flash",
				Messages: []llm.Message{{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: result}}}},
			}, res)
			if err != nil {
				t.Errorf("cap=%v media=%q: %v", capable, mediaType, err)
				continue
			}
			want := []any{map[string]any{"type": "function_result", "call_id": "call-4463", "name": "read_file", "result": "result-4463"}}
			if !reflect.DeepEqual(body["input"], want) {
				t.Errorf("cap=%v media=%q: input=%#v, want text-only function_result", capable, mediaType, body["input"])
			}
			if string(result.ImageData) != "%PDF-1.4" || result.ImageMediaType != mediaType {
				t.Fatal("serializing the result changed its retained document")
			}
		}
	}
}

func TestToInteractionSteps_ToolImagesKeepTheirDelivery(t *testing.T) {
	t.Parallel()
	png := testPNG(t)
	encoded := base64.StdEncoding.EncodeToString(png)
	for _, mediaType := range []string{"", " \t", "image/png"} {
		res := protoRes(func(c *registry.Caps) { c.MultimodalToolResults = new(true) })
		body, err := (&Protocol{}).BuildBody(llm.Request{
			Model: "gemini-3.8-flash",
			Messages: []llm.Message{{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
				ToolCallID: "call-4463", Name: "read_file", Content: "result-4463", ImageData: png, ImageMediaType: mediaType,
			}}}}},
		}, res)
		if err != nil {
			t.Fatal(err)
		}
		want := []any{map[string]any{
			"type":    "function_result",
			"call_id": "call-4463",
			"name":    "read_file",
			"result": []map[string]any{
				{"type": "text", "text": "result-4463"},
				{"type": "image", "mime_type": "image/png", "data": encoded},
			},
		}}
		if !reflect.DeepEqual(body["input"], want) {
			t.Errorf("media=%q: input=%#v, want image delivery", mediaType, body["input"])
		}
	}
}
