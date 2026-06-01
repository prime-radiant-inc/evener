package appsource

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"primeradiant.com/serf/appwire"
)

func codexInput(prompt string, items []appwire.InputItem) ([]map[string]any, error) {
	var input []map[string]any
	if prompt != "" {
		input = append(input, map[string]any{"type": "text", "text": prompt})
	}
	for _, item := range items {
		switch item.Type {
		case "", "input_text", "text":
			if item.Text != "" {
				input = append(input, map[string]any{"type": "text", "text": item.Text})
			}
		case "input_image", "image":
			if url := strings.TrimSpace(item.URL); url != "" {
				input = append(input, map[string]any{"type": "image", "url": url})
				continue
			}
			if len(item.Data) == 0 {
				return nil, appwire.InvalidParams("codex image input requires image data")
			}
			mediaType := firstNonEmpty(item.MediaType, "image/png")
			input = append(input, map[string]any{
				"type": "image",
				"url":  "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(item.Data),
			})
		case "local_image", "localImage":
			path := firstNonEmpty(item.Path, item.Name)
			if path == "" {
				return nil, appwire.InvalidParams("codex localImage input requires path")
			}
			input = append(input, map[string]any{"type": "localImage", "path": path})
		case "skill":
			if item.Name == "" || item.Path == "" {
				return nil, appwire.InvalidParams("codex skill input requires name and path")
			}
			input = append(input, map[string]any{"type": "skill", "name": item.Name, "path": item.Path})
		case "mention":
			if item.Name == "" || item.Path == "" {
				return nil, appwire.InvalidParams("codex mention input requires name and path")
			}
			input = append(input, map[string]any{"type": "mention", "name": item.Name, "path": item.Path})
		default:
			return nil, appwire.InvalidParams("unsupported codex input item type: " + item.Type)
		}
	}
	return input, nil
}

func codexInputText(raw json.RawMessage) string {
	var inputs []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &inputs) != nil {
		return ""
	}
	var parts []string
	for _, input := range inputs {
		if input.Type == "text" && input.Text != "" {
			parts = append(parts, input.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func codexInputImages(raw json.RawMessage) []appwire.InputItem {
	var inputs []map[string]json.RawMessage
	if json.Unmarshal(raw, &inputs) != nil {
		return nil
	}
	var images []appwire.InputItem
	for _, input := range inputs {
		switch rawString(input["type"]) {
		case "image", "input_image":
			url := rawString(input["url"])
			item := codexInputImageFromURL(url, firstNonEmpty(rawString(input["mediaType"]), rawString(input["mimeType"])))
			if item.URL != "" || item.MediaType != "" || len(item.Data) > 0 {
				images = append(images, item)
			}
		case "localImage", "local_image":
			path := firstNonEmpty(rawString(input["path"]), rawString(input["name"]))
			if path != "" {
				images = append(images, appwire.InputItem{
					Type: "local_image",
					Path: path,
					Name: rawString(input["name"]),
				})
			}
		}
	}
	return images
}

func codexInputImageFromURL(rawURL, mediaType string) appwire.InputItem {
	item := appwire.InputItem{
		Type:      "input_image",
		URL:       rawURL,
		MediaType: mediaType,
	}
	if data, dataMediaType, ok := decodeDataImageURL(rawURL); ok {
		item.URL = ""
		item.Data = data
		item.MediaType = firstNonEmpty(mediaType, dataMediaType)
	}
	return item
}

func decodeDataImageURL(rawURL string) ([]byte, string, bool) {
	if !strings.HasPrefix(rawURL, "data:") {
		return nil, "", false
	}
	header, payload, ok := strings.Cut(strings.TrimPrefix(rawURL, "data:"), ",")
	if !ok {
		return nil, "", false
	}
	parts := strings.Split(header, ";")
	if len(parts) == 0 || parts[len(parts)-1] != "base64" {
		return nil, "", false
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, "", false
	}
	return data, parts[0], true
}
