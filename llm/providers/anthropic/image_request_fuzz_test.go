package anthropic

import (
	"encoding/json"
	"testing"

	"primeradiant.com/serf/llm"
)

// FuzzAnthropicImageRequestBuild drives the real request-build path with image
// content, exercising anthropicImageBlock (reached via toAnthropicMessages inside
// buildRequestBody) — the spot that turns an llm image part into Anthropic's
// {type:"image", source:{base64|url}} block. The existing request fuzzer carries
// no image parts, so this branch was unfuzzed. To stay off-disk, the harness
// only uses inline data or a remote (never local) URL, so anthropicImageBlock's
// os.ReadFile branch is never taken.
//
// Oracles beyond no-panic:
//   - a successful build re-marshals to valid JSON and keeps the required model,
//     max_tokens, and messages fields;
//   - when an image block is emitted it is well-formed: type=="image" with a
//     source object that is either a base64 source (media_type + data) or a url
//     source (url) — a malformed image block that Anthropic would 400 reddens it.
func FuzzAnthropicImageRequestBuild(f *testing.F) {
	f.Add("claude-test", "look", []byte{1, 2, 3}, "image/jpeg", "", uint8(0))
	f.Add("claude-opus-4-6", "", []byte{}, "", "pics/cat.png", uint8(1))
	f.Add("m", "u", []byte("\xff\xfe"), "image/png", "", uint8(2))

	a := &Adapter{}

	f.Fuzz(func(t *testing.T, model, user string, imgData []byte, imgMedia, imgURL string, sel uint8) {
		img := &llm.ImageData{Data: imgData, MediaType: imgMedia, Detail: "high"}
		if len(imgData) == 0 && imgURL != "" {
			img.URL = "https://h/" + imgURL // force remote: never a local path -> no os.ReadFile
		}

		mt := 128
		req := llm.Request{
			Model:     model,
			MaxTokens: &mt,
			Messages: []llm.Message{
				llm.System(user),
				{Role: llm.RoleUser, Content: []llm.ContentPart{
					{Kind: llm.ContentText, Text: user},
					{Kind: llm.ContentImage, Image: img},
				}},
			},
		}

		body, err := a.buildRequestBody(req)
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
		for _, k := range []string{"model", "max_tokens", "messages"} {
			if _, ok := round[k]; !ok {
				t.Fatalf("request body missing required field %q\njson=%s", k, b)
			}
		}

		assertAnthropicImageBlocksWellFormed(t, round, b)
	})
}

// assertAnthropicImageBlocksWellFormed walks the emitted messages and validates
// every image block's source shape.
func assertAnthropicImageBlocksWellFormed(t *testing.T, round map[string]any, raw []byte) {
	t.Helper()
	messages, _ := round["messages"].([]any)
	for _, mAny := range messages {
		m, ok := mAny.(map[string]any)
		if !ok {
			continue
		}
		content, ok := m["content"].([]any)
		if !ok {
			continue
		}
		for _, cAny := range content {
			c, ok := cAny.(map[string]any)
			if !ok || c["type"] != "image" {
				continue
			}
			source, ok := c["source"].(map[string]any)
			if !ok {
				t.Fatalf("image block missing source object\njson=%s", raw)
			}
			switch source["type"] {
			case "base64":
				if _, ok := source["media_type"].(string); !ok {
					t.Fatalf("base64 image source missing media_type\njson=%s", raw)
				}
				if _, ok := source["data"].(string); !ok {
					t.Fatalf("base64 image source missing data\njson=%s", raw)
				}
			case "url":
				if u, _ := source["url"].(string); u == "" {
					t.Fatalf("url image source missing url\njson=%s", raw)
				}
			default:
				t.Fatalf("image source has illegal type %v\njson=%s", source["type"], raw)
			}
		}
	}
}
