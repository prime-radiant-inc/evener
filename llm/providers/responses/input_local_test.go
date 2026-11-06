package responses

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

// imagePartsFromItems returns every image URL a built input array carries.
func imagePartsFromItems(t *testing.T, items []any) []string {
	t.Helper()
	var urls []string
	for _, i := range items {
		m, _ := i.(map[string]any)
		if m["type"] != "message" {
			continue
		}
		content, _ := m["content"].([]any)
		for _, c := range content {
			cm, _ := c.(map[string]any)
			if cm["type"] == "input_image" {
				urls = append(urls, cm["image_url"].(string))
			}
		}
	}
	return urls
}

// TestToResponsesInput_LocalPathImageBecomesDataURI pins the local-file image
// arm: a relative-or-absolute path the caller supplied is read from disk,
// normalized, and inlined as a data URI, with the media type inferred from the
// path extension when the part claims none.
func TestToResponsesInput_LocalPathImageBecomesDataURI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "photo.png")
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		claimed string
	}{
		{"media type inferred from the path", ""},
		{"claimed media type kept", "image/png"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, items, err := toResponsesInput([]llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{
				{Kind: llm.ContentImage, Image: &llm.ImageData{URL: path, MediaType: tc.claimed}},
			}}}, "")
			if err != nil {
				t.Fatalf("toResponsesInput: %v", err)
			}
			urls := imagePartsFromItems(t, items)
			if len(urls) != 1 {
				t.Fatalf("image URLs = %v, want the local file inlined once", urls)
			}
			const prefix = "data:image/png;base64,"
			if !strings.HasPrefix(urls[0], prefix) {
				t.Fatalf("image_url = %q, want a png data URI", urls[0])
			}
		})
	}
}

// TestToResponsesInput_LocalPathImageReadFailure surfaces the missing-file
// error rather than sending a path the provider cannot resolve.
func TestToResponsesInput_LocalPathImageReadFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.png")
	_, _, err := toResponsesInput([]llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentImage, Image: &llm.ImageData{URL: missing}},
	}}}, "")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want the raw read failure (os.ErrNotExist)", err)
	}
}

// TestToResponsesInput_LocalPathImageDecodeFailure rejects a local file whose
// bytes are not a raster, whatever its extension claims.
func TestToResponsesInput_LocalPathImageDecodeFailure(t *testing.T) {
	junkPath := filepath.Join(t.TempDir(), "fake.png")
	if err := os.WriteFile(junkPath, []byte("definitely not a raster"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := toResponsesInput([]llm.Message{{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentImage, Image: &llm.ImageData{URL: junkPath}},
	}}}, "")
	if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok {
		t.Fatalf("err = %v, want a configuration error wrapping the decode failure", err)
	}
}

// TestToResponsesInput_ToolResultErrorContentFallbackSerialize pins the
// is_error JSON fallback: when the error content cannot marshal (here a
// NaN, which encoding/json refuses), the wrapper falls back to the quoted
// fmt form rather than dropping the error semantics.
func TestToResponsesInput_ToolResultErrorContentFallbackSerialize(t *testing.T) {
	_, items, err := toResponsesInput([]llm.Message{{Role: llm.RoleTool, Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
			ToolCallID: "call_1",
			IsError:    true,
			Content:    math.NaN(),
		}},
	}}}, "")
	if err != nil {
		t.Fatalf("toResponsesInput: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %v, want the single function_call_output", items)
	}
	item := items[0].(map[string]any)
	if item["type"] != "function_call_output" || item["call_id"] != "call_1" {
		t.Fatalf("item = %v, want a function_call_output for call_1", item)
	}
	if got := item["output"]; got != `{"is_error":true,"content":"NaN"}` {
		t.Fatalf("output = %v, want the is_error fallback wrapper", got)
	}
}

// TestToResponsesInput_ToolResultNonStringContentMarshals pins the non-string
// arm of the tool-result content switch: any non-string value reaches the wire
// as its JSON encoding.
func TestToResponsesInput_ToolResultNonStringContentMarshals(t *testing.T) {
	_, items, err := toResponsesInput([]llm.Message{{Role: llm.RoleTool, Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
			ToolCallID: "call_2",
			Content:    map[string]any{"rows": 2},
		}},
	}}}, "")
	if err != nil {
		t.Fatalf("toResponsesInput: %v", err)
	}
	if got := items[0].(map[string]any)["output"]; got != `{"rows":2}` {
		t.Fatalf("output = %v, want the marshaled JSON object", got)
	}
}
