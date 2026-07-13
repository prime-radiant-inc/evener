# OpenAI Tool-Result Image Encoding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Serialize OpenAI Responses tool-result images inside their corresponding `function_call_output.output` content array so GPT-5.6 Responses-Lite and the public Responses API accept replayed image results.

**Architecture:** Keep `llm.ToolResultData` and every non-Responses provider unchanged. In `toResponsesInput`, preserve string output for text-only tool results, but replace the string with an `input_text` plus `input_image` array when image bytes are present; never emit the tool image as a separate top-level item.

**Tech Stack:** Go, OpenAI Responses JSON wire maps, deterministic `testing` package tests.

## Global Constraints

- Modify only OpenAI Responses request serialization and its deterministic unit tests.
- Do not change OpenAI Chat Completions, OpenAI-compatible Chat Completions, Gemini, Anthropic, transcript storage, or `llm.ToolResultData`.
- Preserve JSON-wrapped error-result text exactly as the first `input_text` item when an error result also contains an image.
- Preserve the `image/png` fallback for missing tool-result media types.
- GPT-5.6 models must omit image `detail`; other Responses models retain their existing default detail.
- Tests must inspect structured inputs and outputs; do not match rendered JSON or make live provider calls.
- Follow `docs/testing.md`; provider credentials alone must never trigger a live request.

---

### Task 1: Nest tool-result images in `function_call_output.output`

**Files:**
- Modify: `llm/providers/openai/adapter_test.go:3758-3915`
- Modify: `llm/providers/openai/gpt56_test.go:31-122`
- Modify: `llm/providers/openai/responses.go:981-1032`

**Interfaces:**
- Consumes: `toResponsesInput(msgs []llm.Message, model string) (instructions string, items []any, err error)` and `llm.ToolResultData`.
- Produces: a `function_call_output` map whose `output` remains a string without image bytes and becomes `[]any{input_text, input_image}` with image bytes.

- [ ] **Step 1: Rewrite the main image test to require the nested structured output**

Replace `TestToResponsesInput_ToolResultWithImage`'s top-level image search with assertions that locate the matching `function_call_output`, reject every top-level `input_image`, and inspect its two output content items:

```go
var outputItem map[string]any
for _, itemAny := range items {
	item, ok := itemAny.(map[string]any)
	if !ok {
		continue
	}
	if item["type"] == "input_image" {
		t.Fatalf("tool-result image must not be a top-level input item: %v", items)
	}
	if item["type"] == "function_call_output" && item["call_id"] == "call_img" {
		outputItem = item
	}
}
if outputItem == nil {
	t.Fatalf("expected function_call_output item with call_id=call_img; items=%v", items)
}

content, ok := outputItem["output"].([]any)
if !ok || len(content) != 2 {
	t.Fatalf("function_call_output.output=%#v, want text and image content", outputItem["output"])
}
text, _ := content[0].(map[string]any)
if text["type"] != "input_text" || text["text"] != "image content below" {
	t.Fatalf("output text=%#v, want input_text with tool output", text)
}
image, _ := content[1].(map[string]any)
if image["type"] != "input_image" {
	t.Fatalf("output image=%#v, want input_image", image)
}
if got := image["image_url"]; got != llm.DataURI("image/png", imgBytes) {
	t.Fatalf("image_url=%#v, want tool-result data URI", got)
}
if got := image["detail"]; got != "original" {
	t.Fatalf("detail=%#v, want original for gpt-5.4", got)
}
```

- [ ] **Step 2: Run the focused test and verify the RED state**

Run:

```bash
go test ./llm/providers/openai -run '^TestToResponsesInput_ToolResultWithImage$' -count=1 -v
```

Expected: FAIL because `function_call_output.output` is still a string and the image is still a top-level input item.

- [ ] **Step 3: Add structured coverage for fallback media type and error output**

Update `TestToResponsesInput_ToolResultWithImage_DefaultMediaType` to read the image from `function_call_output.output[1]` and assert the URL starts with `data:image/png;base64,`.

Add this error-result regression beside the other tool-image tests:

```go
func TestToResponsesInput_ErrorToolResultWithImagePreservesWrappedText(t *testing.T) {
	msgs := []llm.Message{{Role: llm.RoleTool, Content: []llm.ContentPart{{
		Kind: llm.ContentToolResult,
		ToolResult: &llm.ToolResultData{
			ToolCallID:     "call_error_image",
			Content:        "connection refused",
			IsError:        true,
			ImageData:      []byte("png"),
			ImageMediaType: "image/png",
		},
	}}}}

	_, items, err := toResponsesInput(msgs, "gpt-5.4")
	if err != nil {
		t.Fatalf("toResponsesInput: %v", err)
	}
	item := items[0].(map[string]any)
	content := item["output"].([]any)
	text := content[0].(map[string]any)
	var wrapped map[string]any
	if err := json.Unmarshal([]byte(text["text"].(string)), &wrapped); err != nil {
		t.Fatalf("wrapped error text is not JSON: %v", err)
	}
	if wrapped["is_error"] != true || wrapped["content"] != "connection refused" {
		t.Fatalf("wrapped error=%#v", wrapped)
	}
}
```

Keep `TestToResponsesInput_ToolResultWithoutImage_NoInputImage`; it guards the unchanged string-output path.

- [ ] **Step 4: Update GPT-5.6 image collection to follow the valid nested location**

Change `collectInputImages` so it collects images inside messages and `function_call_output.output`, and fails if a top-level `input_image` appears:

```go
case "input_image":
	t.Fatalf("unexpected top-level input_image: %#v", item)
case "message", "function_call_output":
	content, _ := item[map[bool]string{true: "output", false: "content"}[item["type"] == "function_call_output"]].([]any)
	for _, cAny := range content {
		c, _ := cAny.(map[string]any)
		if c["type"] == "input_image" {
			images = append(images, c)
		}
	}
```

Use a simple local field selection instead of retaining the inline boolean map if that is clearer in the surrounding Go style:

```go
field := "content"
if item["type"] == "function_call_output" {
	field = "output"
}
content, _ := item[field].([]any)
```

Update the helper comment to say images are collected from message content and function-call output content. Update `TestGPT55_ImageDetailUnchanged`'s comment to describe unchanged detail behavior rather than unchanged encoding.

- [ ] **Step 5: Implement the minimal serializer change**

In the `llm.RoleTool` branch of `toResponsesInput`, build the `function_call_output` item as today. When `ImageData` is present, replace its string `output` with the documented content array before appending the item:

```go
item := map[string]any{
	"type":    "function_call_output",
	"call_id": p.ToolResult.ToolCallID,
	"output":  outStr,
}

if len(p.ToolResult.ImageData) > 0 {
	mt := p.ToolResult.ImageMediaType
	if mt == "" {
		mt = "image/png"
	}
	img := map[string]any{
		"type":      "input_image",
		"image_url": llm.DataURI(mt, p.ToolResult.ImageData),
	}
	if !responsesLiteModel(model) {
		img["detail"] = defaultImageDetail(model)
	}
	item["output"] = []any{
		map[string]any{"type": "input_text", "text": outStr},
		img,
	}
}
items = append(items, item)
```

Delete the old separate `items = append(items, img)` path. Do not extract a new helper unless duplication remains after the minimal change.

- [ ] **Step 6: Run focused GREEN verification**

Run:

```bash
gofmt -w llm/providers/openai/adapter_test.go llm/providers/openai/gpt56_test.go llm/providers/openai/responses.go
go test ./llm/providers/openai -run 'TestToResponsesInput_(ToolResultWithImage|ErrorToolResultWithImagePreservesWrappedText)|TestGPT5(5|6)_ImageDetail' -count=1 -v
```

Expected: all selected tests PASS. Then run:

```bash
go test ./llm/providers/openai -count=1
```

Expected: package PASS with no warnings or live requests.

- [ ] **Step 7: Run repository verification**

Run the project gate with its built-in module and memory controls:

```bash
make test
```

Expected: PASS. If the environment kills the gate with exit 137 again, record that as infrastructure evidence and retry the deterministic module runner with reduced Go package parallelism:

```bash
GOFLAGS='-p=2' make test
```

Also run:

```bash
git diff --check
git status --short
```

Expected: no whitespace errors; only the planned test and serializer files are modified before committing.

- [ ] **Step 8: Commit the implementation**

```bash
git add llm/providers/openai/adapter_test.go llm/providers/openai/gpt56_test.go llm/providers/openai/responses.go
git commit -m "Fix OpenAI tool-result image encoding" -m "Nest tool-result text and image content inside function_call_output.output for every OpenAI Responses model. This removes the invalid top-level input_image item rejected by GPT-5.6 Responses-Lite while preserving text-only output, error wrapping, media-type fallback, and model-specific image detail behavior.\n\nAdd structured deterministic regressions for nested image output, error-result text, and GPT-5.5/GPT-5.6 detail handling."
```

Expected: commit succeeds with all pre-commit hooks enabled.

---

## Completion Evidence

- The focused test is observed failing before production changes and passing afterward.
- `go test ./llm/providers/openai -count=1` passes.
- `make test` passes, or an exit-137 infrastructure kill is reported alongside the successful reduced-parallelism retry.
- The final branch contains the design commit, plan commit, and implementation commit.
- GitHub issues #17 and #18 remain separate follow-up work; this branch does not modify Gemini or OpenAI-compatible Chat Completions.
