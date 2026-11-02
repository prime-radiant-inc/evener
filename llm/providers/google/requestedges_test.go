package google

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// geminiBuild runs BuildBody and returns the wire contents, failing the test on
// any build error.
func geminiBuild(t *testing.T, req llm.Request, res registry.Resolved) map[string]any {
	t.Helper()
	body, err := (&Protocol{}).BuildBody(req, res)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestBuildBodyToolChoiceNoneAndUnnamed(t *testing.T) {
	req := protoReq("")
	req.Tools = []llm.ToolDefinition{{Name: "f"}}
	req.ToolChoice = &llm.ToolChoice{Mode: "none"}
	body := geminiBuild(t, req, protoRes(nil))
	cfg := body["toolConfig"].(map[string]any)["functionCallingConfig"].(map[string]any)
	if cfg["mode"] != "NONE" {
		t.Fatalf("tool_choice none must spell functionCallingConfig mode NONE, got %v", cfg)
	}

	req.ToolChoice = &llm.ToolChoice{Mode: "named"}
	_, err := (&Protocol{}).BuildBody(req, protoRes(nil))
	cfgErr, ok := errors.AsType[*llm.ConfigurationError](err)
	if !ok {
		t.Fatalf("named tool_choice without a name must be a configuration error, got %T %v", err, err)
	}
	_ = cfgErr
}

// An effort whose ReasoningBudget is zero (anything outside the six recognized
// levels) must send no thinkingConfig on a budget-shaped row, and an effort a
// level-shaped row's ladder cannot vouch for must send none either.
func TestBuildBodyThinkingEffortWithoutBudgetOrVouchedLevel(t *testing.T) {
	weird := "bizarre"
	req := protoReq("")
	req.ReasoningEffort = &weird

	budget := protoRes(func(c *registry.Caps) { c.ThinkingShape = new("budget") })
	if _, has := geminiBuild(t, req, budget)["generationConfig"].(map[string]any)["thinkingConfig"]; has {
		t.Fatal("an unrecognized effort must not size a thinking budget")
	}

	ladder := protoRes(func(c *registry.Caps) {
		c.ThinkingShape = new("level")
		c.EffortValues = []string{"low"}
	})
	if _, has := geminiBuild(t, req, ladder)["generationConfig"].(map[string]any)["thinkingConfig"]; has {
		t.Fatal("a level-shaped row whose ladder cannot vouch the effort must send no thinking object")
	}
}

func TestSanitizeGeminiSchemaAdversarialTypeShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   map[string]any
		want map[string]any
	}{
		{"nullable pair is collapsed", map[string]any{"type": []any{"string", "null"}}, map[string]any{"type": "string", "nullable": true}},
		{"properties value that is not an object passes through", map[string]any{"properties": "not-a-map"}, map[string]any{"properties": "not-a-map"}},
		{"enum that is not a list passes through", map[string]any{"type": []any{"string", "null"}, "enum": "nope"}, map[string]any{"type": "string", "nullable": true, "enum": "nope"}},
		{"type list with a non-string member passes through", map[string]any{"type": []any{"string", 5}}, map[string]any{"type": []any{"string", 5}}},
		{"one-element type list passes through", map[string]any{"type": []any{"string"}}, map[string]any{"type": []any{"string"}}},
		{"three-element type list passes through", map[string]any{"type": []any{"string", "null", "x"}}, map[string]any{"type": []any{"string", "null", "x"}}},
		{"empty-string type member passes through", map[string]any{"type": []any{"", "null"}}, map[string]any{"type": []any{"", "null"}}},
		{"two non-null type members passes through", map[string]any{"type": []any{"string", "integer"}}, map[string]any{"type": []any{"string", "integer"}}},
		{"all-null type list passes through", map[string]any{"type": []any{"null", "null"}}, map[string]any{"type": []any{"null", "null"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeGeminiSchema(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("sanitizeGeminiSchema(%#v) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

// TestBuildBodyAssistantAndToolResultContents drives toGeminiContents through
// BuildBody with one conversation whose assistant and tool messages select
// every translation arm: assistant text, a nil image part (dropped), an image
// URL part (fileData), a nil tool call (dropped), a tool call whose arguments
// are literal JSON null (must become {}), a thought signature replay, tool
// results carrying a map / a non-string non-map payload / no call id, a tool
// message with a non-result part (dropped), and an unknown role (ignored).
func TestBuildBodyAssistantAndToolResultContents(t *testing.T) {
	req := llm.Request{Model: "gemini-x", Messages: []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hi"}}},
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentImage, Image: nil}}},
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentImage, Image: &llm.ImageData{URL: "https://h/p.png"}}}},
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: nil}}},
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
			ID: "c9", Name: "shot", Arguments: json.RawMessage(`null`), Type: "function", ThoughtSignature: "sig-1",
		}}}},
		{Role: llm.RoleTool, Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "not a tool result"},
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c9", Content: map[string]any{"ok": true}}},
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "  ", Content: 42}},
		}},
		{Role: llm.Role("bogus"), Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "ignored"}}},
	}}
	contents := geminiBuild(t, req, protoRes(nil))["contents"].([]map[string]any)
	if len(contents) != 4 {
		t.Fatalf("contents = %#v, want 4 entries (text, image, tool call, merged tool results)", contents)
	}
	if parts := contents[0]["parts"].([]map[string]any); len(parts) != 1 || parts[0]["text"] != "hi" {
		t.Fatalf("assistant text part = %#v", contents[0])
	}
	imgParts := contents[1]["parts"].([]map[string]any)
	fileData := imgParts[0]["fileData"].(map[string]any)
	if fileData["fileUri"] != "https://h/p.png" || fileData["mimeType"] != "image/png" {
		t.Fatalf("assistant image fileData = %#v", fileData)
	}
	call := contents[2]["parts"].([]map[string]any)[0]["functionCall"].(map[string]any)
	if call["name"] != "shot" || !reflect.DeepEqual(call["args"], map[string]any{}) {
		t.Fatalf("null tool-call arguments must become an empty object, got %#v", call)
	}
	if contents[2]["parts"].([]map[string]any)[0]["thoughtSignature"] != "sig-1" {
		t.Fatalf("assistant tool call must replay its thought signature: %#v", contents[2])
	}
	toolParts := contents[3]["parts"].([]map[string]any)
	if contents[3]["role"] != "user" || len(toolParts) != 2 {
		t.Fatalf("tool results must merge into one user turn: %#v", contents[3])
	}
	first := toolParts[0]["functionResponse"].(map[string]any)
	if first["name"] != "shot" || !reflect.DeepEqual(first["response"], map[string]any{"ok": true}) {
		t.Fatalf("map tool-result content must become the response object verbatim: %#v", first)
	}
	second := toolParts[1]["functionResponse"].(map[string]any)
	if second["name"] != "  " || !reflect.DeepEqual(second["response"], map[string]any{"result": "42"}) {
		t.Fatalf("non-string non-map content must marshal into result, and a blank call id and name must fall back to the raw id: %#v", second)
	}
}

func TestBuildBodyAssistantDocumentAndAudioRejected(t *testing.T) {
	for name, part := range map[string]llm.ContentPart{
		"document": {Kind: llm.ContentDocument, Document: &llm.DocumentData{Data: []byte("%PDF-1.4")}},
		"audio":    {Kind: llm.ContentAudio, Audio: &llm.AudioData{Data: []byte{1}}},
	} {
		t.Run(name, func(t *testing.T) {
			req := llm.Request{Model: "gemini-x", Messages: []llm.Message{
				{Role: llm.RoleAssistant, Content: []llm.ContentPart{part}},
			}}
			_, err := (&Protocol{}).BuildBody(req, protoRes(nil))
			if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok {
				t.Fatalf("assistant %s content must be a configuration error, got %T %v", name, err, err)
			}
		})
	}
}

// TestBuildBodyImageParts drives geminiImagePart through BuildBody: inline data
// with no media type (defaults to image/png), a remote URL, an empty image
// part (dropped), local-file reads under the test's own TempDir (inferred
// media type by extension, the image/png default for an unknown extension),
// and the read-failure arm against a missing TempDir sibling.
func TestBuildBodyImageParts(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	dir := t.TempDir()
	localPNG := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(localPNG, png, 0o600); err != nil {
		t.Fatal(err)
	}
	localNoExt := filepath.Join(dir, "shot")
	if err := os.WriteFile(localNoExt, png, 0o600); err != nil {
		t.Fatal(err)
	}

	build := func(t *testing.T, img *llm.ImageData) []map[string]any {
		t.Helper()
		req := llm.Request{Model: "gemini-x", Messages: []llm.Message{
			{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentImage, Image: img}}},
		}}
		contents, _ := geminiBuild(t, req, protoRes(nil))["contents"].([]map[string]any)
		return contents
	}

	inline := build(t, &llm.ImageData{Data: []byte{1, 2, 3}})[0]["parts"].([]map[string]any)[0]["inlineData"].(map[string]any)
	if inline["mimeType"] != "image/png" || inline["data"] != base64.StdEncoding.EncodeToString([]byte{1, 2, 3}) {
		t.Fatalf("inline data with no media type must default to image/png: %#v", inline)
	}

	remote := build(t, &llm.ImageData{URL: "https://h/p.png"})[0]["parts"].([]map[string]any)[0]["fileData"].(map[string]any)
	if remote["fileUri"] != "https://h/p.png" {
		t.Fatalf("remote image must become fileData: %#v", remote)
	}

	if got := build(t, &llm.ImageData{}); len(got) != 0 {
		t.Fatalf("an image part with neither bytes nor a URL must be dropped, got %#v", got)
	}

	local := build(t, &llm.ImageData{URL: localPNG})[0]["parts"].([]map[string]any)[0]["inlineData"].(map[string]any)
	if local["mimeType"] != "image/png" || local["data"] != base64.StdEncoding.EncodeToString(png) {
		t.Fatalf("local read must inline the file bytes with the inferred type: %#v", local)
	}

	noExt := build(t, &llm.ImageData{URL: localNoExt})[0]["parts"].([]map[string]any)[0]["inlineData"].(map[string]any)
	if noExt["mimeType"] != "image/png" || noExt["data"] != base64.StdEncoding.EncodeToString(png) {
		t.Fatalf("an extension that infers no media type must fall back to image/png: %#v", noExt)
	}

	req := llm.Request{Model: "gemini-x", Messages: []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentImage, Image: &llm.ImageData{URL: filepath.Join(dir, "gone.png")}}}},
	}}
	_, err := (&Protocol{}).BuildBody(req, protoRes(nil))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a missing local image must fail with a not-found error, got %T %v", err, err)
	}

	// The same failure inside an assistant message must surface through the
	// assistant image arm, not only the user one.
	assistantReq := llm.Request{Model: "gemini-x", Messages: []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentImage, Image: &llm.ImageData{URL: filepath.Join(dir, "gone.png")}}}},
	}}
	_, err = (&Protocol{}).BuildBody(assistantReq, protoRes(nil))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a missing local image in an assistant message must fail with a not-found error, got %T %v", err, err)
	}
}

func TestToolNameFromCallIDBlankAndUnknown(t *testing.T) {
	msgs := []llm.Message{{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
			ID: "c1", Name: "shot", Type: "function",
		}}},
	}}
	if got := toolNameFromCallID(msgs, "  "); got != "" {
		t.Fatalf("a blank call id must match nothing, got %q", got)
	}
	if got := toolNameFromCallID(msgs, "c1"); got != "shot" {
		t.Fatalf("matching call id must yield the wire name, got %q", got)
	}
	if got := toolNameFromCallID(msgs, "c2"); got != "" {
		t.Fatalf("an unknown call id must yield no name, got %q", got)
	}
}
