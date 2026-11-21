package google

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func geminiBuild(t *testing.T, req llm.Request, res registry.Resolved) map[string]any {
	t.Helper()
	body, err := (&Protocol{}).BuildBody(req, res)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestBuildBodyToolChoiceNoneAndUnnamed(t *testing.T) {
	t.Parallel()
	req := protoReq("")
	req.Tools = []llm.ToolDefinition{{Name: "f"}}
	req.ToolChoice = &llm.ToolChoice{Mode: "none"}
	body := geminiBuild(t, req, protoRes(nil))
	gen := body["generation_config"].(map[string]any)
	if gen["tool_choice"] != "none" {
		t.Fatalf("tool_choice none must spell generation_config.tool_choice = none, got %v", gen)
	}

	req.ToolChoice = &llm.ToolChoice{Mode: "named"}
	_, err := (&Protocol{}).BuildBody(req, protoRes(nil))
	if _, ok := errors.AsType[*llm.ConfigurationError](err); !ok {
		t.Fatalf("named tool_choice without a name must be a configuration error, got %T %v", err, err)
	}
}

func TestBuildBodyThinkingEffortWithoutVouchedLevel(t *testing.T) {
	t.Parallel()
	weird := "bizarre"
	req := protoReq("")
	req.ReasoningEffort = &weird

	ladder := protoRes(func(c *registry.Caps) {
		c.ThinkingShape = new("level")
		c.EffortValues = []string{"low"}
	})
	if _, has := geminiBuild(t, req, ladder)["generation_config"].(map[string]any)["thinking_level"]; has {
		t.Fatal("a level-shaped row whose ladder cannot vouch the effort must send no thinking_level")
	}
}

func TestBuildBodyAssistantAndToolResultContents(t *testing.T) {
	t.Parallel()
	req := llm.Request{Model: "gemini-x", Messages: []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hi"}}},
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentImage, Image: nil}}},
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentImage, Image: &llm.ImageData{URL: "https://h/p.png"}}}},
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: nil}}},
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
			GoogleInteraction: true, GoogleInstance: "gemini-prod", GoogleModel: "gemini-x",
			ID: "c9", Name: "shot", Arguments: json.RawMessage(`null`), Type: "function", ThoughtSignature: "sig-1",
		}}}},
		{Role: llm.RoleTool, Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "not a tool result"},
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "c9", Content: map[string]any{"ok": true}}},
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "  ", Content: 42}},
		}},
		{Role: llm.Role("bogus"), Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "ignored"}}},
	}}
	steps := geminiBuild(t, req, protoRes(nil))["input"].([]any)
	// First model_output merges adjacent GoogleStep=0 text and image; then function_call, then 2 function_result steps.
	if len(steps) != 4 {
		t.Fatalf("steps = %#v, want 4 entries (model_output, function_call, 2 function_results)", steps)
	}
	mo := steps[0].(map[string]any)
	moContent := mo["content"].([]any)
	if mo["type"] != "model_output" || len(moContent) != 2 {
		t.Fatalf("model_output = %#v", mo)
	}
	if moContent[0].(map[string]any)["text"] != "hi" {
		t.Fatalf("assistant text = %#v", moContent[0])
	}
	img := moContent[1].(map[string]any)
	if img["type"] != "image" || img["uri"] != "https://h/p.png" || img["mime_type"] != "image/png" {
		t.Fatalf("assistant image = %#v", img)
	}
	fc := steps[1].(map[string]any)
	if fc["type"] != "function_call" || fc["name"] != "shot" || string(fc["arguments"].(json.RawMessage)) != "{}" || fc["signature"] != "sig-1" {
		t.Fatalf("function_call = %#v", fc)
	}
	fr0 := steps[2].(map[string]any)
	if fr0["type"] != "function_result" || fr0["name"] != "shot" || fr0["result"] != `{"ok":true}` {
		t.Fatalf("map tool-result content must serialize to JSON string: %#v", fr0)
	}
	fr1 := steps[3].(map[string]any)
	if fr1["type"] != "function_result" || fr1["result"] != "42" {
		t.Fatalf("non-string tool-result content = %#v", fr1)
	}
}

func TestBuildBodyAssistantDocumentAndAudioRejected(t *testing.T) {
	t.Parallel()
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

func TestBuildBodyImageParts(t *testing.T) {
	t.Parallel()
	png := testPNG(t)
	dir := t.TempDir()
	localPNG := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(localPNG, png, 0o600); err != nil {
		t.Fatal(err)
	}
	localNoExt := filepath.Join(dir, "shot")
	if err := os.WriteFile(localNoExt, png, 0o600); err != nil {
		t.Fatal(err)
	}

	buildItem := func(t *testing.T, img *llm.ImageData) map[string]any {
		t.Helper()
		req := llm.Request{Model: "gemini-x", Messages: []llm.Message{
			{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentImage, Image: img}}},
		}}
		steps := geminiBuild(t, req, protoRes(nil))["input"].([]any)
		return steps[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	}

	inline := buildItem(t, &llm.ImageData{Data: png})
	if inline["mime_type"] != "image/png" || inline["data"] != base64.StdEncoding.EncodeToString(png) {
		t.Fatalf("inline data with no media type: %#v", inline)
	}

	remote := buildItem(t, &llm.ImageData{URL: "https://h/p.png"})
	if remote["uri"] != "https://h/p.png" {
		t.Fatalf("remote image must become uri item: %#v", remote)
	}

	// Empty image part yields no input steps -> ConfigurationError.
	emptyReq := llm.Request{Model: "gemini-x", Messages: []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentImage, Image: &llm.ImageData{}}}},
	}}
	if _, err := (&Protocol{}).BuildBody(emptyReq, protoRes(nil)); err == nil {
		t.Fatalf("empty-steps request must fail with ConfigurationError, got %v", err)
	}

	local := buildItem(t, &llm.ImageData{URL: localPNG})
	if local["mime_type"] != "image/png" || local["data"] != base64.StdEncoding.EncodeToString(png) {
		t.Fatalf("local read: %#v", local)
	}

	noExt := buildItem(t, &llm.ImageData{URL: localNoExt})
	if noExt["mime_type"] != "image/png" || noExt["data"] != base64.StdEncoding.EncodeToString(png) {
		t.Fatalf("no-ext default mime_type: %#v", noExt)
	}

	req := llm.Request{Model: "gemini-x", Messages: []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentImage, Image: &llm.ImageData{URL: filepath.Join(dir, "gone.png")}}}},
	}}
	_, err := (&Protocol{}).BuildBody(req, protoRes(nil))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing local image must fail with ErrNotExist, got %T %v", err, err)
	}

	assistantReq := llm.Request{Model: "gemini-x", Messages: []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentImage, Image: &llm.ImageData{URL: filepath.Join(dir, "gone.png")}}}},
	}}
	_, err = (&Protocol{}).BuildBody(assistantReq, protoRes(nil))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing assistant local image must fail with ErrNotExist, got %T %v", err, err)
	}
}
