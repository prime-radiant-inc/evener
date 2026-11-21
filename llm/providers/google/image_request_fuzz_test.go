package google

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/llm"
)

func FuzzGeminiImageRequestBuild(f *testing.F) {
	f.Add("gemini-test", "look", []byte{1, 2, 3}, "image/jpeg", "", uint8(0))
	f.Add("gemini-3.8-flash", "", []byte{}, "", "pics/cat.png", uint8(1))
	f.Add("m", "u", []byte("\xff\xfe"), "image/png", "", uint8(2))
	f.Add("gemini-test", "u", []byte{4, 5, 6}, "", "", uint8(3))
	f.Add("gemini-test", "u", []byte{}, "", "", uint8(4))

	f.Fuzz(func(t *testing.T, model, user string, imgData []byte, imgMedia, imgURL string, sel uint8) {
		img := &llm.ImageData{Data: imgData, MediaType: imgMedia}
		if len(imgData) == 0 && imgURL != "" {
			img.URL = "https://h/" + imgURL
		}

		req := llm.Request{
			Model: model,
			Messages: []llm.Message{
				llm.System(user),
				{Role: llm.RoleUser, Content: []llm.ContentPart{
					{Kind: llm.ContentText, Text: user},
					{Kind: llm.ContentImage, Image: img},
				}},
			},
		}

		body, err := (&Protocol{}).BuildBody(req, protoRes(nil))
		if err != nil {
			return
		}

		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("BuildBody produced an unmarshalable body: %v\nbody=%#v", err, body)
		}
		var round map[string]any
		if err := json.Unmarshal(b, &round); err != nil {
			t.Fatalf("request body is not valid JSON: %v\njson=%s", err, b)
		}
		if _, ok := round["input"]; !ok {
			t.Fatalf("request body missing required field \"input\"\njson=%s", b)
		}

		assertInteractionImagePartsWellFormed(t, round, b)
	})
}

func assertInteractionImagePartsWellFormed(t *testing.T, round map[string]any, raw []byte) {
	t.Helper()
	steps, _ := round["input"].([]any)
	for _, sAny := range steps {
		s, ok := sAny.(map[string]any)
		if !ok {
			continue
		}
		items, ok := s["content"].([]any)
		if !ok {
			continue
		}
		for _, itemAny := range items {
			item, ok := itemAny.(map[string]any)
			if !ok || item["type"] != "image" {
				continue
			}
			if mt, _ := item["mime_type"].(string); mt == "" {
				t.Fatalf("image item missing mime_type\njson=%s", raw)
			}
			_, hasData := item["data"].(string)
			uri, _ := item["uri"].(string)
			if !hasData && uri == "" {
				t.Fatalf("image item missing both data and uri\njson=%s", raw)
			}
		}
	}
}
