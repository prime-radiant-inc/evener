package google

import (
	"encoding/json"
	"testing"

	"primeradiant.com/serf/llm"
)

// FuzzGeminiImageRequestBuild drives the real request-build path with image
// content, exercising geminiImagePart (reached via toGeminiContents) — the spot
// that turns an llm image part into Gemini's inlineData (base64) or fileData
// (URI) part. The existing request fuzzer carries no image parts, so this branch
// was unfuzzed. The harness only uses inline data or a remote (never local) URL,
// so geminiImagePart's os.ReadFile branch is never taken.
//
// Oracles beyond no-panic:
//   - a successful build re-marshals to valid JSON and keeps the required
//     "contents" field;
//   - every emitted image part is well-formed: inlineData carries mimeType+data,
//     or fileData carries mimeType+fileUri — a malformed part Gemini would reject
//     reddens it.
func FuzzGeminiImageRequestBuild(f *testing.F) {
	f.Add("gemini-test", "look", []byte{1, 2, 3}, "image/jpeg", "", uint8(0))
	f.Add("gemini-2.5-pro", "", []byte{}, "", "pics/cat.png", uint8(1))
	f.Add("m", "u", []byte("\xff\xfe"), "image/png", "", uint8(2))

	a := &Adapter{}

	f.Fuzz(func(t *testing.T, model, user string, imgData []byte, imgMedia, imgURL string, sel uint8) {
		img := &llm.ImageData{Data: imgData, MediaType: imgMedia}
		if len(imgData) == 0 && imgURL != "" {
			img.URL = "https://h/" + imgURL // force remote: never a local path -> no os.ReadFile
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

		sys, contents, err := toGeminiContents(req.Messages)
		if err != nil {
			return
		}
		body, err := a.buildRequestBody(req, sys, contents)
		if err != nil {
			return
		}

		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("buildRequestBody produced an unmarshalable body: %v\nbody=%#v", err, body)
		}
		var round map[string]any
		if err := json.Unmarshal(b, &round); err != nil {
			t.Fatalf("request body is not valid JSON: %v\njson=%s", err, b)
		}
		if _, ok := round["contents"]; !ok {
			t.Fatalf("request body missing required field \"contents\"\njson=%s", b)
		}

		assertGeminiImagePartsWellFormed(t, round, b)
	})
}

func assertGeminiImagePartsWellFormed(t *testing.T, round map[string]any, raw []byte) {
	t.Helper()
	contents, _ := round["contents"].([]any)
	for _, cAny := range contents {
		c, ok := cAny.(map[string]any)
		if !ok {
			continue
		}
		parts, ok := c["parts"].([]any)
		if !ok {
			continue
		}
		for _, pAny := range parts {
			p, ok := pAny.(map[string]any)
			if !ok {
				continue
			}
			if inline, ok := p["inlineData"].(map[string]any); ok {
				if mt, _ := inline["mimeType"].(string); mt == "" {
					t.Fatalf("inlineData missing mimeType\njson=%s", raw)
				}
				if _, ok := inline["data"].(string); !ok {
					t.Fatalf("inlineData missing data\njson=%s", raw)
				}
			}
			if fileData, ok := p["fileData"].(map[string]any); ok {
				if mt, _ := fileData["mimeType"].(string); mt == "" {
					t.Fatalf("fileData missing mimeType\njson=%s", raw)
				}
				if u, _ := fileData["fileUri"].(string); u == "" {
					t.Fatalf("fileData missing fileUri\njson=%s", raw)
				}
			}
		}
	}
}
