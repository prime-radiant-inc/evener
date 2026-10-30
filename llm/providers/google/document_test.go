package google

import (
	"errors"
	"reflect"
	"testing"

	"primeradiant.com/evener/llm"
)

func TestToGeminiContents_UserDocuments(t *testing.T) {
	t.Parallel()
	inline := map[string]any{"inlineData": map[string]any{"mimeType": "application/pdf", "data": "JVBERi0xLjQ="}}
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
		{"declared media", &llm.DocumentData{Data: []byte("%PDF-1.4"), MediaType: "text/plain"}, map[string]any{"inlineData": map[string]any{"mimeType": "text/plain", "data": "JVBERi0xLjQ="}}},
		{"file uri", &llm.DocumentData{URL: " https://generativelanguage.googleapis.com/v1beta/files/pdf-4463 "}, map[string]any{"fileData": map[string]any{"mimeType": "application/pdf", "fileUri": "https://generativelanguage.googleapis.com/v1beta/files/pdf-4463"}}},
		{"nil", nil, nil},
		{"empty", &llm.DocumentData{}, nil},
		{"blank uri", &llm.DocumentData{URL: " \t"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, contents, err := toGeminiContents("gemini", []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "sentinel-before"},
				{Kind: llm.ContentDocument, Document: tc.doc},
				{Kind: llm.ContentImage, Image: &llm.ImageData{Data: []byte{1}, MediaType: "image/png"}},
				{Kind: llm.ContentText, Text: "sentinel-after"},
			}}}, false)
			if err != nil {
				t.Fatal(err)
			}
			parts := []map[string]any{{"text": "sentinel-before"}}
			if tc.want != nil {
				parts = append(parts, tc.want)
			}
			parts = append(parts, map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": "AQ=="}}, map[string]any{"text": "sentinel-after"})
			want := []map[string]any{{"role": "user", "parts": parts}}
			if !reflect.DeepEqual(contents, want) {
				t.Fatalf("contents = %#v, want %#v", contents, want)
			}
		})
	}
}

func TestToGeminiContents_AssistantDocumentsRemainUnsupported(t *testing.T) {
	t.Parallel()
	_, contents, err := toGeminiContents("gemini", []llm.Message{{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentDocument, Document: &llm.DocumentData{Data: []byte("%PDF-1.4")}},
	}}}, false)
	if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok || contents != nil {
		t.Fatalf("assistant document: contents=%v err=%v, want configuration refusal", contents, err)
	}
}

func TestToGeminiContents_AudioRemainsUnsupported(t *testing.T) {
	t.Parallel()
	for _, role := range []llm.Role{llm.RoleUser, llm.RoleAssistant} {
		_, _, err := toGeminiContents("gemini", []llm.Message{{Role: role, Content: []llm.ContentPart{{Kind: llm.ContentAudio, Audio: &llm.AudioData{Data: []byte{1}}}}}}, false)
		if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok {
			t.Fatalf("%s audio: %v, want configuration refusal", role, err)
		}
	}
}

func TestToGeminiContents_ToolDocumentsStayOffTheWire(t *testing.T) {
	t.Parallel()
	for _, cap := range []bool{false, true} {
		for _, mediaType := range []string{"application/pdf", " APPLICATION/PDF ", "text/plain"} {
			result := &llm.ToolResultData{ToolCallID: "call-4463", Name: "read_file", Content: "result-4463", ImageData: []byte("%PDF-1.4"), ImageMediaType: mediaType}
			_, contents, err := toGeminiContents("gemini", []llm.Message{{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: result}}}}, cap)
			if err != nil {
				t.Errorf("cap=%v media=%q: %v", cap, mediaType, err)
				continue
			}
			want := []map[string]any{{"role": "user", "parts": []map[string]any{{"functionResponse": map[string]any{"name": "read_file", "response": map[string]any{"result": "result-4463"}}}}}}
			if !reflect.DeepEqual(contents, want) {
				t.Errorf("cap=%v media=%q: contents=%#v, want text-only function response", cap, mediaType, contents)
			}
			if string(result.ImageData) != "%PDF-1.4" || result.ImageMediaType != mediaType {
				t.Fatal("serializing the result changed its retained document")
			}
		}
	}
}

func TestToGeminiContents_ToolImagesKeepTheirDelivery(t *testing.T) {
	t.Parallel()
	for _, mediaType := range []string{"", " \t", "image/png"} {
		_, contents, err := toGeminiContents("gemini", []llm.Message{{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
			ToolCallID: "call-4463", Name: "read_file", Content: "result-4463", ImageData: []byte{1}, ImageMediaType: mediaType,
		}}}}}, true)
		if err != nil {
			t.Fatal(err)
		}
		want := []map[string]any{{"role": "user", "parts": []map[string]any{{"functionResponse": map[string]any{
			"name": "read_file", "response": map[string]any{"result": "result-4463"},
			"parts": []map[string]any{{"inlineData": map[string]any{"mimeType": "image/png", "data": "AQ=="}}},
		}}}}}
		if !reflect.DeepEqual(contents, want) {
			t.Errorf("media=%q: contents=%#v, want image delivery", mediaType, contents)
		}
	}
}
