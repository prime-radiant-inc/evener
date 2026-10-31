package responses

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// responsesFuzzImageDetails and responsesFuzzReasoningSummaries are the
// string-valued caps FuzzResponsesBuildBody flips, indexed by the fuzzed
// capSel byte (request.go: caps.ImageDetail, caps.ReasoningSummary).
var (
	responsesFuzzImageDetails       = []string{"high", "original", "omit"}
	responsesFuzzReasoningSummaries = []string{"", "auto", "detailed", "none"}
)

// buildFuzzResponsesRequest assembles a moderately rich llm.Request from
// fuzz primitives, routing the fuzzer's bytes into tool-call argument JSON,
// tool parameter schemas, and JSON-schema response formats, plus an image
// content part — the inputs buildBody (and toResponsesInput/toResponsesTools)
// have to survive. Ported from the openai adapter's buildFuzzRequest
// (requestbuild_fuzz_test.go).
//
// sel bits: %5 selects the tool-choice mode, (sel>>3)%4 the response format,
// &1 a tool-result error, &2 an effort, &4 web search, &8 the stream flag,
// &16 a strict schema flag, &32 an inline image, and &64 a name on whatever
// tool choice the mode built (so the backward-compatible default mode and the
// named mode can each run with and without a name).
//
// capSel bits select the request-level control fields that have no other
// fuzzed home: &32 req.MaxToolCalls, &64 req.Background, and &128 a
// ProviderOptions map carrying reasoning-control keys alongside one custom
// key (which must survive the reasoning-off passthrough skip in buildBody).
//
// shapeSel routes the fuzzer into the rarer toResponsesInput content shapes
// one mode at a time: assistant text-phase groups, rejected content kinds,
// document parts, nil and URL-less images, thinking summaries, web-search
// raw payloads, tool-result content variants, unknown roles, and an
// include list that already names the encrypted-reasoning sentinel. Mode 0
// adds none of them, so the earlier seeds' shapes are unchanged.
func buildFuzzResponsesRequest(model, system, user string, toolArgs, toolParams []byte, sel, capSel, shapeSel byte) llm.Request {
	var params map[string]any
	_ = json.Unmarshal(toolParams, &params)

	req := llm.Request{
		Model: model,
		Messages: []llm.Message{
			llm.System(system),
			llm.User(user),
			{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
				Kind: llm.ContentToolCall,
				ToolCall: &llm.ToolCallData{
					ID:        "call_1",
					Name:      "t",
					Arguments: json.RawMessage(toolArgs),
					Type:      "function",
				},
			}}},
			llm.ToolResult("call_1", user, sel&1 == 1),
		},
		Tools: []llm.ToolDefinition{{Name: "t", Description: system, Parameters: params}},
	}

	switch sel % 5 {
	case 0:
		req.ToolChoice = &llm.ToolChoice{Mode: "auto"}
	case 1:
		req.ToolChoice = &llm.ToolChoice{Mode: "required"}
	case 2:
		req.ToolChoice = &llm.ToolChoice{Mode: "none"}
	case 3:
		req.ToolChoice = &llm.ToolChoice{Mode: "named", Name: "t"}
	case 4:
		req.ToolChoice = &llm.ToolChoice{Mode: model}
	}
	if sel&64 == 64 && req.ToolChoice != nil {
		req.ToolChoice.Name = "t"
	}

	switch (sel >> 3) % 4 {
	case 1:
		req.ResponseFormat = &llm.ResponseFormat{Type: "json"}
	case 2:
		req.ResponseFormat = &llm.ResponseFormat{Type: "json_schema", JSONSchema: params, Strict: sel&16 == 16}
	case 3:
		req.ResponseFormat = &llm.ResponseFormat{Type: model}
	}

	temp := 0.5
	req.Temperature = &temp
	tp := 0.9
	req.TopP = &tp
	mt := int(sel) * 8
	req.MaxTokens = &mt
	if sel&2 == 2 {
		eff := "high"
		req.ReasoningEffort = &eff
	}
	req.WebSearch = sel&4 == 4
	if sel&32 == 32 {
		// Image input, with the fuzzed string doubling as a detail hint —
		// exercises the per-model detail encoding (omitted on responses-lite).
		req.Messages[1].Content = append(req.Messages[1].Content, llm.ContentPart{
			Kind:  llm.ContentImage,
			Image: &llm.ImageData{MediaType: "image/png", Data: []byte{0x01}, Detail: system},
		})
	}
	req.StopSequences = []string{system, user}
	if sel&128 == 128 {
		// A per-tool strict override the strict row must honor.
		req.Tools[0].Strict = new(sel&1 == 1)
	}

	if capSel&32 == 32 {
		calls := int(sel) + 1
		req.MaxToolCalls = &calls
	}
	if capSel&64 == 64 {
		req.Background = new(true)
	}
	if capSel&128 == 128 {
		req.ProviderOptions = map[string]any{registry.ProtocolOpenAIResponses: map[string]any{
			"reasoning": map[string]any{"effort": "high"},
			"include":   []any{"reasoning.encrypted_content"},
			"fuzz_keep": "kept",
		}}
	}

	switch int(shapeSel) % 12 {
	case 1:
		// Assistant text parts, grouped by phase; the whitespace-only part is
		// skipped and the two empty-phase parts share one message item.
		req.Messages[2].Content = append(req.Messages[2].Content,
			llm.ContentPart{Kind: llm.ContentText, Text: "group one", Phase: ""},
			llm.ContentPart{Kind: llm.ContentText, Text: "  ", Phase: ""},
			llm.ContentPart{Kind: llm.ContentText, Text: "group two", Phase: ""},
			llm.ContentPart{Kind: llm.ContentText, Text: "group three", Phase: "draft"},
		)
	case 2:
		req.Messages[2].Content = append(req.Messages[2].Content, llm.ContentPart{
			Kind:  llm.ContentAudio,
			Audio: &llm.AudioData{URL: "https://example.invalid/a.wav"},
		})
	case 3:
		req.Messages[1].Content = append(req.Messages[1].Content,
			llm.ContentPart{Kind: llm.ContentDocument, Document: nil},
			llm.ContentPart{Kind: llm.ContentDocument, Document: &llm.DocumentData{Data: []byte("%fuzz-pdf"), MediaType: ""}},
			llm.ContentPart{Kind: llm.ContentDocument, Document: &llm.DocumentData{URL: "https://example.invalid/d.pdf", FileName: "d.pdf"}},
		)
	case 4:
		req.Messages[1].Content = append(req.Messages[1].Content,
			llm.ContentPart{Kind: llm.ContentImage, Image: nil},
			llm.ContentPart{Kind: llm.ContentImage, Image: &llm.ImageData{URL: "https://example.invalid/i.png"}},
		)
	case 5:
		req.Messages[1].Content = append(req.Messages[1].Content, llm.ContentPart{
			Kind:  llm.ContentAudio,
			Audio: &llm.AudioData{URL: "https://example.invalid/b.wav"},
		})
	case 6:
		req.Messages[2].Content = append(req.Messages[2].Content, llm.ContentPart{
			Kind: llm.ContentThinking,
			Thinking: &llm.ThinkingData{
				ID:               "  rs_fuzz  ",
				EncryptedContent: "opaque-fuzz-blob",
				Summary:          []string{"fuzz summary", "   ", ""},
			},
		})
	case 7:
		req.Messages[2].Content = append(req.Messages[2].Content, llm.ContentPart{
			Kind:      llm.ContentWebSearch,
			WebSearch: &llm.WebSearchData{Query: "q", Raw: toolParams},
		})
	case 8:
		req.Messages[3].Content = append([]llm.ContentPart{{Kind: llm.ContentText, Text: "note"}}, req.Messages[3].Content...)
		req.Messages[3].Content[1].ToolResult.Content = map[string]any{"sh": true}
	case 9:
		req.Messages[3].Content[0].ToolResult.ImageData = []byte("fuzz-not-a-raster")
		req.Messages[3].Content[0].ToolResult.ImageMediaType = "image/png"
	case 10:
		req.Messages = append(req.Messages, llm.Message{Role: llm.Role("martian"), Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "x"}}})
	case 11:
		req.Include = []string{encryptedReasoning}
	}
	return req
}

// FuzzResponsesBuildBody drives the Resolved-driven Responses request
// builder (buildBody), the path Complete/Stream use to turn an llm.Request
// into wire JSON. capSel additionally flips the row-level structural caps
// buildBody branches on: ResponsesLite, StrictTools, ImageDetail,
// ReasoningSummary, ThinkingAlwaysOn, StructuredOutput, and Reasoning.
//
// Oracles:
//   - buildBody never panics (floor);
//   - a build error is always a typed llm.Error, never a bare error;
//   - when the build succeeds, the result always re-marshals to valid JSON
//     and preserves "input";
//   - registry.Prune never panics on the built body;
//   - when ResponsesLite is set, input[0].type is "additional_tools" and
//     "tools" is absent from the top level (request.go's lite shape);
//   - each shapeSel mode asserts the wire shape it routes to (text-phase
//     grouping, document parts, reasoning summaries, tool-result content,
//     provider options kept or dropped per the row's reasoning cap, …).
//     Every option-derived expectation is read off the request and the caps
//     the builder branches on, so independently fuzzed bits cannot contradict
//     the oracle the way a hardcoded combo can.
func FuzzResponsesBuildBody(f *testing.F) {
	f.Add("gpt-test", "be terse", "hello", []byte(`{"city":"paris"}`), []byte(`{"type":"object","properties":{"x":{"type":"string"}}}`), byte(0), byte(0), byte(0))
	f.Add("gpt-5", "", "", []byte(`{}`), []byte(`{"oneOf":[{"type":"string"}]}`), byte(3), byte(0), byte(0))
	// gpt-5.6 takes the responses-lite request shape (no image detail and
	// reasoning always sent).
	f.Add("gpt-5.6-sol", "high", "look at this", []byte(`{"x":1}`), []byte(`{"type":"object"}`), byte(34), byte(1), byte(0))
	f.Add("m", "sys", "u", []byte(`not json`), []byte(`null`), byte(11), byte(0), byte(0))
	f.Add("", "s", "u", []byte(``), []byte(`{"anyOf":[1,2]}`), byte(22), byte(0), byte(0))

	// Response formats the (sel>>3)%4==3 arm routes by type name: an empty
	// type and an unknown one.
	f.Add("", "s", "u", []byte(`{}`), []byte(`{}`), byte(24), byte(0), byte(0))
	f.Add("fuzz-format", "s", "u", []byte(`{}`), []byte(`{}`), byte(27), byte(0), byte(0))
	// tool_choice mode=named without a name is a structured configuration
	// error; an unknown mode with a name rides the backward-compatible map.
	f.Add("named", "s", "u", []byte(`{}`), []byte(`{}`), byte(4), byte(0), byte(0))
	f.Add("fuzz-mode", "s", "u", []byte(`{}`), []byte(`{}`), byte(64), byte(0), byte(0))
	// The mode=model arm with a populated name: the recognized mode names
	// emit their normalized wire strings, not the backward-compatible map.
	f.Add("", "s", "u", []byte(`{}`), []byte(`{}`), byte(64), byte(0), byte(0))
	f.Add("auto", "s", "u", []byte(`{}`), []byte(`{}`), byte(64), byte(0), byte(0))
	f.Add("none", "s", "u", []byte(`{}`), []byte(`{}`), byte(64), byte(0), byte(0))
	f.Add("required", "s", "u", []byte(`{}`), []byte(`{}`), byte(64), byte(0), byte(0))
	f.Add("Named", "s", "u", []byte(`{}`), []byte(`{}`), byte(64), byte(0), byte(0))
	// The unknown-role drop must not trip over user text that legitimately
	// contains the sentinel word.
	f.Add("m", "s", "martian", []byte(`{}`), []byte(`{}`), byte(0), byte(0), byte(10))
	// A reasoning summary cap of "none" and the request-level control fields
	// buildBody emits only when the request carries them.
	f.Add("m", "s", "u", []byte(`{}`), []byte(`{}`), byte(2), byte(19), byte(0))
	f.Add("m", "s", "u", []byte(`{}`), []byte(`{}`), byte(0), byte(32), byte(0))
	f.Add("m", "s", "u", []byte(`{}`), []byte(`{}`), byte(0), byte(64), byte(0))
	// A reasoning-off row must drop the reasoning/include provider options and
	// keep the unrelated ones.
	f.Add("m", "s", "u", []byte(`{}`), []byte(`{}`), byte(0), byte(128), byte(0))
	// A strict row reads each tool's own strict override instead of the row
	// default.
	f.Add("m", "s", "u", []byte(`{}`), []byte(`{"type":"object","properties":{"x":{"type":"string"}}}`), byte(131), byte(2), byte(0))
	// The rarer toResponsesInput shapes, one per mode.
	f.Add("m", "sys", "u", []byte(`{"a":1}`), []byte(`{"type":"object"}`), byte(0), byte(0), byte(1))
	f.Add("m", "sys", "u", []byte(`{"a":1}`), []byte(`{}`), byte(0), byte(0), byte(2))
	f.Add("m", "sys", "u", []byte(`{}`), []byte(`{}`), byte(0), byte(0), byte(3))
	f.Add("m", "sys", "u", []byte(`{}`), []byte(`{}`), byte(0), byte(0), byte(4))
	f.Add("m", "sys", "u", []byte(`{}`), []byte(`{}`), byte(0), byte(0), byte(5))
	f.Add("m", "sys", "u", []byte(`{}`), []byte(`{}`), byte(0), byte(0), byte(6))
	f.Add("m", "sys", "u", []byte(`{}`), []byte(`{"type":"web_search_call","id":"ws_1"}`), byte(0), byte(0), byte(7))
	f.Add("m", "sys", "u", []byte(`{}`), []byte(`{}`), byte(0), byte(0), byte(8))
	f.Add("m", "sys", "u", []byte(`{}`), []byte(`{}`), byte(0), byte(0), byte(9))
	f.Add("m", "sys", "u", []byte(`{}`), []byte(`{}`), byte(0), byte(0), byte(10))
	// An include list that already names the encrypted-reasoning sentinel must
	// not duplicate it when the reasoning include is attached.
	f.Add("m", "s", "u", []byte(`{}`), []byte(`{}`), byte(2), byte(16), byte(11))
	// The interacting option pairs the derived oracles must survive: a
	// reasoning-capable row keeping its provider options, recognized and
	// case-variant by-name response formats, a structured-output-off
	// json_schema row, a strict override riding the lite tools prefix, an
	// error tool result reaching the wrapped output shape, and a request
	// include list under both the drop and the pass-through rows.
	f.Add("m", "s", "u", []byte(`{}`), []byte(`{}`), byte(0), byte(144), byte(0))
	f.Add("json", "s", "u", []byte(`{}`), []byte(`{}`), byte(25), byte(0), byte(0))
	f.Add("JSON", "s", "u", []byte(`{}`), []byte(`{}`), byte(25), byte(0), byte(0))
	f.Add("json_schema", "s", "u", []byte(`{}`), []byte(`{"type":"object"}`), byte(25), byte(8), byte(0))
	f.Add("m", "s", "u", []byte(`{}`), []byte(`{"type":"object","properties":{"x":{"type":"string"}}}`), byte(131), byte(3), byte(0))
	f.Add("m", "sys", "u", []byte(`{}`), []byte(`{}`), byte(1), byte(0), byte(8))
	f.Add("m", "s", "u", []byte(`{}`), []byte(`{}`), byte(0), byte(128), byte(11))
	f.Add("m", "s", "u", []byte(`{}`), []byte(`{}`), byte(2), byte(144), byte(11))

	f.Fuzz(func(t *testing.T, model, system, user string, toolArgs, toolParams []byte, sel, capSel, shapeSel byte) {
		req := buildFuzzResponsesRequest(model, system, user, toolArgs, toolParams, sel, capSel, shapeSel)
		lite := capSel&1 == 1
		res := resolved(func(c *registry.Caps) {
			c.ResponsesLite = new(lite)
			c.StrictTools = new(capSel&2 == 2)
			c.ImageDetail = new(responsesFuzzImageDetails[int(capSel)%len(responsesFuzzImageDetails)])
			c.ReasoningSummary = new(responsesFuzzReasoningSummaries[int(capSel)%len(responsesFuzzReasoningSummaries)])
			c.ThinkingAlwaysOn = new(capSel&4 == 4)
			c.StructuredOutput = new(capSel&8 == 8)
			c.Reasoning = new(capSel&16 == 16)
		})

		body, err := buildBody(req, res, sel&8 == 8)
		if err != nil {
			// A structured build error is acceptable, but only as a typed
			// llm.Error — never a bare error or a panic recovery.
			if _, ok := errors.AsType[llm.Error](err); !ok {
				t.Fatalf("buildBody error is not a typed llm.Error: %v", err)
			}
			return
		}

		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("buildBody produced an unmarshalable body: %v\nbody=%#v", err, body)
		}
		var round map[string]any
		if err := json.Unmarshal(b, &round); err != nil {
			t.Fatalf("request body is not valid JSON: %v\njson=%s", err, b)
		}
		if _, ok := round["input"]; !ok {
			t.Fatalf("request body missing required field \"input\"\njson=%s", b)
		}

		items, _ := body["input"].([]any)
		itemMaps := make([]map[string]any, 0, len(items))
		for _, i := range items {
			if m, ok := i.(map[string]any); ok {
				itemMaps = append(itemMaps, m)
			}
		}
		inputJSON, _ := json.Marshal(items)

		if capSel&32 == 32 {
			if body["max_tool_calls"] != int(sel)+1 {
				t.Fatalf("max_tool_calls = %v, want %d", body["max_tool_calls"], sel+1)
			}
		}
		if capSel&64 == 64 {
			if body["background"] != true {
				t.Fatalf("background = %v, want true", body["background"])
			}
		}
		if capSel&128 == 128 {
			if body["fuzz_keep"] != "kept" {
				t.Fatalf("unrelated provider options must survive: %#v", body)
			}
			// The reasoning-off passthrough skip is the only drop: a row whose
			// reasoning cap is on passes the options through, so the presence
			// expectations read off the cap bit, not off the option block.
			if capSel&16 == 16 {
				reasoning, _ := body["reasoning"].(map[string]any)
				if reasoning["effort"] != "high" {
					t.Fatalf("a reasoning-capable row must keep the reasoning provider option: %#v", body["reasoning"])
				}
				if got := jsonOf(t, body["include"]); got != `["`+encryptedReasoning+`"]` {
					t.Fatalf("a reasoning-capable row must keep the include provider option: %s", got)
				}
			} else {
				if _, has := body["reasoning"]; has {
					t.Fatalf("a reasoning-off row must drop the reasoning provider option: %#v", body["reasoning"])
				}
				// A request include list (shapeSel mode 11) survives the drop.
				if _, has := body["include"]; has && int(shapeSel)%12 != 11 {
					t.Fatalf("a reasoning-off row must drop the include provider option: %#v", body["include"])
				}
			}
		}

		if (sel>>3)%4 == 3 {
			// The by-name response format arm routes by type name: "", "text",
			// and unknown names map to no format at all, while the recognized
			// names build one — case and surrounding whitespace included.
			name := strings.ToLower(strings.TrimSpace(model))
			recognized := name == "json" || name == "json_schema"
			if _, has := body["text"]; has != recognized {
				t.Fatalf("response format %q must map to %s text.format on the wire: %#v", model, map[bool]string{true: "a", false: "no"}[recognized], body["text"])
			}
		}
		if sel%5 == 4 && sel&64 == 64 {
			// The mode is the fuzzed model string, so the expectation must be
			// derived from the mode toResponsesToolChoice normalizes: the
			// recognized names emit their wire strings (empty maps to "auto"),
			// while "named" and every unrecognized mode ride the name-keyed map.
			mode := strings.ToLower(strings.TrimSpace(model))
			if mode == "" {
				mode = "auto"
			}
			switch mode {
			case "auto", "none", "required":
				if body["tool_choice"] != mode {
					t.Fatalf("tool_choice = %#v, want the normalized mode %q", body["tool_choice"], mode)
				}
			default:
				tc, _ := body["tool_choice"].(map[string]any)
				if tc == nil || tc["type"] != "function" || tc["name"] != "t" {
					t.Fatalf("tool_choice = %#v, want the name-keyed function map", body["tool_choice"])
				}
			}
		}
		if sel&128 == 128 && capSel&2 == 2 {
			var toolRows []map[string]any
			if lite {
				input, _ := body["input"].([]any)
				first, _ := input[0].(map[string]any)
				carried, _ := first["tools"].([]any)
				for _, t := range carried {
					if m, ok := t.(map[string]any); ok {
						toolRows = append(toolRows, m)
					}
				}
			} else {
				toolRows, _ = body["tools"].([]map[string]any)
			}
			if len(toolRows) == 0 || toolRows[0]["strict"] != (sel&1 == 1) {
				t.Fatalf("tools[0].strict = %#v, want the per-tool override %v", toolRows, sel&1 == 1)
			}
			if sel&1 == 1 {
				var schema map[string]any
				_ = json.Unmarshal(toolParams, &schema)
				if schema == nil {
					schema = map[string]any{"type": "object"}
				}
				if typ, _ := schema["type"].(string); typ == "object" {
					params, _ := toolRows[0]["parameters"].(map[string]any)
					if params == nil || params["additionalProperties"] != false {
						t.Fatalf("a strict tool must carry the strictified schema: %#v", toolRows[0]["parameters"])
					}
				}
			}
		}

		switch int(shapeSel) % 12 {
		case 1:
			var assistantMessages []map[string]any
			for _, m := range itemMaps {
				if m["type"] == "message" && m["role"] == "assistant" {
					assistantMessages = append(assistantMessages, m)
				}
			}
			if len(assistantMessages) != 2 {
				t.Fatalf("assistant text parts must group into 2 message items (empty-phase parts share one): %s", inputJSON)
			}
			first, _ := assistantMessages[0]["content"].([]any)
			if len(first) != 2 {
				t.Fatalf("the two empty-phase text parts must share one item: %s", inputJSON)
			}
			if assistantMessages[1]["phase"] != "draft" {
				t.Fatalf("a phased group must carry its phase: %s", inputJSON)
			}
		case 3:
			var files []map[string]any
			for _, m := range itemMaps {
				for _, c := range flattenContent(m) {
					if c["type"] == "input_file" {
						files = append(files, c)
					}
				}
			}
			if len(files) != 2 {
				t.Fatalf("document parts must build 2 input_file entries (a nil document is skipped): %s", inputJSON)
			}
			data, _ := files[0]["file_data"].(string)
			if data != "data:application/pdf;base64,JWZ1enotcGRm" {
				t.Fatalf("a data document with no claimed media type defaults to pdf: %s", inputJSON)
			}
			if files[1]["file_data"] != "https://example.invalid/d.pdf" || files[1]["filename"] != "d.pdf" {
				t.Fatalf("a URL document carries its URL and filename: %s", inputJSON)
			}
		case 4:
			var images []map[string]any
			for _, m := range itemMaps {
				for _, c := range flattenContent(m) {
					if c["type"] == "input_image" {
						images = append(images, c)
					}
				}
			}
			if len(images) != 1 || images[0]["image_url"] != "https://example.invalid/i.png" {
				t.Fatalf("a nil image part is skipped, the URL image kept: %s", inputJSON)
			}
		case 6:
			var reasoning map[string]any
			for _, m := range itemMaps {
				if m["type"] == "reasoning" {
					reasoning = m
				}
			}
			if reasoning == nil {
				t.Fatalf("a thinking part with non-OpenAI encrypted content must replay as a reasoning item: %s", inputJSON)
			}
			if reasoning["encrypted_content"] != "opaque-fuzz-blob" || reasoning["id"] != "rs_fuzz" {
				t.Fatalf("reasoning item must carry the blob and the trimmed id: %s", inputJSON)
			}
			summary, _ := reasoning["summary"].([]any)
			if len(summary) != 1 || summary[0].(map[string]any)["text"] != "fuzz summary" {
				t.Fatalf("whitespace-only summary entries must be dropped: %s", inputJSON)
			}
		case 7:
			var want map[string]any
			if err := json.Unmarshal(toolParams, &want); err == nil && want != nil {
				found := false
				for _, m := range itemMaps {
					if jsonOf(t, m) == jsonOf(t, want) {
						found = true
					}
				}
				if !found {
					t.Fatalf("a web-search part's raw JSON object must become an input item verbatim: %s", inputJSON)
				}
			}
		case 8:
			for _, m := range itemMaps {
				if m["type"] != "function_call_output" {
					continue
				}
				out, _ := m["output"].(string)
				if sel&1 == 1 {
					var wrapped map[string]any
					if err := json.Unmarshal([]byte(out), &wrapped); err != nil {
						t.Fatalf("an error tool result must wrap its content in JSON: %s", inputJSON)
					}
					if isErr, _ := wrapped["is_error"].(bool); !isErr {
						t.Fatalf("an error tool result must flag is_error: %s", inputJSON)
					}
					content, _ := wrapped["content"].(map[string]any)
					if content["sh"] != true {
						t.Fatalf("the wrapped content must carry the tool result: %s", inputJSON)
					}
					continue
				}
				if out != `{"sh":true}` {
					t.Fatalf("a non-string tool-result content must marshal to JSON: %s", inputJSON)
				}
			}
		case 10:
			// An unknown role must leave the input byte-identical to the same
			// request built without it: the fuzzed strings can legitimately
			// contain the sentinel word, so a substring scan is not an oracle.
			plain := buildFuzzResponsesRequest(model, system, user, toolArgs, toolParams, sel, capSel, shapeSel-shapeSel%12)
			plainBody, plainErr := buildBody(plain, res, sel&8 == 8)
			if plainErr != nil {
				t.Fatalf("the no-unknown-role variant must build wherever the unknown-role one does: %v", plainErr)
			}
			if got, want := jsonOf(t, body["input"]), jsonOf(t, plainBody["input"]); got != want {
				t.Fatalf("an unknown role must leave no wire trace:\nwith: %s\nwithout: %s", got, want)
			}
		case 11:
			if got := jsonOf(t, body["include"]); got != `["`+encryptedReasoning+`"]` {
				t.Fatalf("include = %s, want the sentinel exactly once (no duplicate)", got)
			}
		}

		registry.Prune(body, res.Caps, res.Protocol) // must never panic

		if lite {
			input, _ := body["input"].([]any)
			if len(input) == 0 {
				t.Fatalf("lite body has no input: %#v", body)
			}
			first, _ := input[0].(map[string]any)
			if first["type"] != "additional_tools" {
				t.Fatalf("lite body's first input item must be additional_tools: %#v", first)
			}
			if _, has := body["tools"]; has {
				t.Fatalf("lite body must not carry top-level tools: %#v", body)
			}
		}
	})
}

// flattenContent returns the content array of one input message item, or nil.
func flattenContent(item map[string]any) []map[string]any {
	content, _ := item["content"].([]any)
	out := make([]map[string]any, 0, len(content))
	for _, c := range content {
		if m, ok := c.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}
