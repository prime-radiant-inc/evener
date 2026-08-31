package chatcompletions

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// The tests below are ported from llm/providers/openaicompat's
// compat_request_test.go and reasoning_fields_test.go: the same scenarios
// against the old ModelCompat/ProviderQuirks fixtures, replayed here against
// resolved() caps to prove the port kept the wire behavior those quirks
// used to guarantee. Tests whose subject moved to ShapeRequest (sampling
// locks, MaxStopSequences, DefaultMaxTokens, ThinkingLevels,
// TranslateMaxToXHigh) or the adaptive-fallback ThinkingAlwaysOn-default
// tests are not ported; see the task report for the full skip list.

func toolExchangeMessages() []llm.Message {
	return []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "weather?"}}},
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_1", Name: "get_weather", Arguments: []byte(`{}`)}},
		}},
		{Role: llm.RoleTool, Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_1", Content: "sunny"}},
		}},
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "thanks"}}},
	}
}

// assistantReasoningDetails returns the reasoning_details array emitted on
// the (single) assistant message of a built request body.
func assistantReasoningDetails(t *testing.T, req llm.Request) []map[string]any {
	t.Helper()
	body := build(t, req, resolved(nil))
	msgs := body["messages"].([]map[string]any)
	for _, m := range msgs {
		if m["role"] != "assistant" {
			continue
		}
		rd, ok := m["reasoning_details"]
		if !ok {
			return nil
		}
		details, ok := rd.([]map[string]any)
		if !ok {
			t.Fatalf("reasoning_details not []map[string]any: %T", rd)
		}
		return details
	}
	t.Fatal("no assistant message in body")
	return nil
}

// ported: openaicompat's TestDeveloperRole (developer role).
func TestDeveloperRole(t *testing.T) {
	req := llm.Request{
		Model: "m",
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "sys"}}},
			{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "hi"}}},
		},
	}
	body := build(t, req, resolved(func(c *registry.Caps) { c.Fields[registry.FieldDeveloperRole] = true }))
	msgs := body["messages"].([]map[string]any)
	if msgs[0]["role"] != "developer" {
		t.Errorf("system message role = %v, want developer", msgs[0]["role"])
	}

	body = build(t, req, resolved(nil))
	msgs = body["messages"].([]map[string]any)
	if msgs[0]["role"] != "system" {
		t.Errorf("system message role = %v, want system", msgs[0]["role"])
	}
}

// ported: openaicompat's TestRequireToolResultName (tool-result name).
func TestRequireToolResultName(t *testing.T) {
	req := llm.Request{Model: "m", Messages: toolExchangeMessages()}
	body := build(t, req, resolved(func(c *registry.Caps) { c.ToolResultName = new(true) }))
	msgs := body["messages"].([]map[string]any)
	var toolMsg map[string]any
	for _, m := range msgs {
		if m["role"] == "tool" {
			toolMsg = m
		}
	}
	if toolMsg == nil {
		t.Fatal("no tool message emitted")
	}
	if toolMsg["name"] != "get_weather" {
		t.Errorf("tool message name = %v, want get_weather (recovered from the tool_call)", toolMsg["name"])
	}

	body = build(t, req, resolved(nil))
	msgs = body["messages"].([]map[string]any)
	for _, m := range msgs {
		if m["role"] == "tool" {
			if _, ok := m["name"]; ok {
				t.Error("tool message has name without the cap")
			}
		}
	}
}

// ported: openaicompat's TestRequireAssistantAfterToolResult (assistant-after-tool).
func TestRequireAssistantAfterToolResult(t *testing.T) {
	req := llm.Request{Model: "m", Messages: toolExchangeMessages()}
	body := build(t, req, resolved(func(c *registry.Caps) { c.AssistantAfterToolResult = new(true) }))
	msgs := body["messages"].([]map[string]any)
	idxTool, idxUser := -1, -1
	for i, m := range msgs {
		if m["role"] == "tool" {
			idxTool = i
		}
		if m["role"] == "user" && m["content"] == "thanks" {
			idxUser = i
		}
	}
	if idxTool == -1 || idxUser == -1 {
		t.Fatalf("missing tool or trailing user message: %v", msgs)
	}
	if idxUser != idxTool+2 {
		t.Fatalf("want synthetic assistant between tool and user; got messages %v", msgs)
	}
	between := msgs[idxTool+1]
	if between["role"] != "assistant" || between["content"] != "" {
		t.Errorf("between message = %v, want empty assistant", between)
	}
}

// ported: openaicompat's TestThinkingAsText (thinking-as-text).
func TestThinkingAsText(t *testing.T) {
	req := llm.Request{Model: "m", Messages: []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "q"}}},
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "pondering"}},
			{Kind: llm.ContentText, Text: "answer"},
		}},
		{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "q2"}}},
	}}
	body := build(t, req, resolved(func(c *registry.Caps) { c.ThinkingAsText = new(true) }))
	msgs := body["messages"].([]map[string]any)
	assistant := msgs[1]
	if _, ok := assistant["reasoning_content"]; ok {
		t.Error("reasoning_content present with ThinkingAsText")
	}
	content, _ := assistant["content"].(string)
	if content != "pondering\n\nanswer" {
		t.Errorf("content = %q, want thinking flattened before text", content)
	}
}

// ported: openaicompat's TestAnthropicCacheControl (cache control). The
// Step-1 TestBuildBody_AnthropicCacheControl already covers the marker
// shape and CacheTTL; this one adds the multi-message placement rule the
// old test also checked: only the LAST user/assistant turn gets marked, and
// every earlier turn is untouched.
func TestAnthropicCacheControl(t *testing.T) {
	req := llm.Request{
		Model: "m",
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "sys"}}},
			{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "one"}}},
			{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "two"}}},
		},
		Tools: []llm.ToolDefinition{{Name: "a"}, {Name: "b"}},
	}
	body := build(t, req, resolved(func(c *registry.Caps) { c.CacheControl = new("anthropic") }))
	msgs := body["messages"].([]map[string]any)

	wantCC := map[string]any{"type": "ephemeral"}

	sysParts, ok := msgs[0]["content"].([]map[string]any)
	if !ok || len(sysParts) != 1 {
		t.Fatalf("system content = %#v, want single-part array", msgs[0]["content"])
	}
	if !reflect.DeepEqual(sysParts[0]["cache_control"], wantCC) || sysParts[0]["text"] != "sys" {
		t.Errorf("system part = %v, want text sys with cache_control", sysParts[0])
	}

	lastParts, ok := msgs[2]["content"].([]map[string]any)
	if !ok || len(lastParts) != 1 || !reflect.DeepEqual(lastParts[0]["cache_control"], wantCC) {
		t.Errorf("last message content = %#v, want cache_control on text part", msgs[2]["content"])
	}
	if _, isString := msgs[1]["content"].(string); !isString {
		t.Errorf("middle message content = %#v, want plain string (untouched)", msgs[1]["content"])
	}

	tools := body["tools"].([]map[string]any)
	if _, ok := tools[0]["cache_control"]; ok {
		t.Error("first tool has cache_control, want only last")
	}
	if !reflect.DeepEqual(tools[len(tools)-1]["cache_control"], wantCC) {
		t.Errorf("last tool = %v, want cache_control", tools[len(tools)-1])
	}
}

// ported: openaicompat's TestStoreToolStreamAndStreamOptions (store,
// tool_stream). The OmitStreamUsage sub-case is not ported: stream_options
// is now unconditional at the builder (the row's Fields["stream_options"]
// gates it later, in the prune).
func TestStoreToolStreamAndStreamOptions(t *testing.T) {
	req := userReq("hi")
	req.Tools = []llm.ToolDefinition{{Name: "get_weather"}}

	res := resolved(func(c *registry.Caps) { c.Fields["store"] = true; c.ToolStream = new(true) })
	body, err := buildBody(llm.ShapeRequest(req, res), res, true)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := body["store"].(bool); !ok || got {
		t.Errorf("store = %v, want false", body["store"])
	}
	if got, ok := body["tool_stream"].(bool); !ok || !got {
		t.Errorf("tool_stream = %v, want true", body["tool_stream"])
	}
	if _, ok := body["stream_options"]; !ok {
		t.Error("stream_options missing")
	}

	// tool_stream only when tools are present.
	noTools := userReq("hi")
	res2 := resolved(func(c *registry.Caps) { c.ToolStream = new(true) })
	body, err = buildBody(llm.ShapeRequest(noTools, res2), res2, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := body["tool_stream"]; ok {
		t.Error("tool_stream sent without tools")
	}
}

// ported: openaicompat's TestMaxTokensFieldAndDefault (max-tokens
// spelling), minus its DefaultMaxTokens sub-cases (that subject moved to
// ShapeRequest's MaxOutputTokens fill).
func TestMaxTokensFieldAndDefault(t *testing.T) {
	req := userReq("hi")
	mt := 4096
	req.MaxTokens = &mt
	body := build(t, req, resolved(func(c *registry.Caps) { c.MaxTokensField = new("max_completion_tokens") }))
	if got := body["max_completion_tokens"]; got != 4096 {
		t.Errorf("max_completion_tokens = %v, want 4096", got)
	}
	if _, ok := body["max_tokens"]; ok {
		t.Error("max_tokens present alongside max_completion_tokens")
	}
}

// ported: openaicompat's TestReplay_MixedTextAndEncryptedReasoningDetails
// (encrypted-details replay): text composes with encrypted details, text
// item first.
func TestReplay_MixedTextAndEncryptedReasoningDetails(t *testing.T) {
	encrypted := `[{"type":"reasoning.encrypted","id":"rc_1","data":"OPAQUE"}]`
	req := llm.Request{
		Model: "m",
		ProviderOptions: map[string]any{
			registry.ProtocolOpenAIChat: map[string]any{"reasoning": map[string]any{"enabled": true}},
		},
		Messages: []llm.Message{
			llm.User("q"),
			{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "deep thought", EncryptedContent: encrypted}},
				{Kind: llm.ContentText, Text: "a"},
			}},
			llm.User("q2"),
		},
	}
	details := assistantReasoningDetails(t, req)
	if len(details) != 2 {
		t.Fatalf("reasoning_details = %v, want [text, encrypted]", details)
	}
	if details[0]["type"] != "reasoning.text" || details[0]["text"] != "deep thought" {
		t.Errorf("details[0] = %v, want reasoning.text", details[0])
	}
	if details[1]["type"] != "reasoning.encrypted" || details[1]["data"] != "OPAQUE" {
		t.Errorf("details[1] = %v, want reasoning.encrypted", details[1])
	}
}

// ported: openaicompat's TestReplay_EncryptedOnlyWithoutReasoningFlag
// (encrypted-details replay): an encrypted-only turn replays into
// reasoning_details even when nothing set useReasoningDetails.
func TestReplay_EncryptedOnlyWithoutReasoningFlag(t *testing.T) {
	encrypted := `[{"type":"reasoning.encrypted","id":"rc_1","data":"OPAQUE"}]`
	req := llm.Request{Model: "m", Messages: []llm.Message{
		llm.User("q"),
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{EncryptedContent: encrypted}},
			{Kind: llm.ContentText, Text: "a"},
		}},
		llm.User("q2"),
	}}
	details := assistantReasoningDetails(t, req)
	if len(details) != 1 || details[0]["type"] != "reasoning.encrypted" {
		t.Fatalf("reasoning_details = %v, want single encrypted item", details)
	}
}

// ported: openaicompat's TestReplay_ForeignEncryptedContentSkipped
// (encrypted-details replay): a foreign opaque blob (not our reasoning.*
// item array) must not replay into reasoning_details, but plain thinking
// text still replays normally.
func TestReplay_ForeignEncryptedContentSkipped(t *testing.T) {
	req := llm.Request{Model: "m", Messages: []llm.Message{
		llm.User("q"),
		{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{
				Text:             "pondered",
				EncryptedContent: "gAAAAABopaqueOpenAIResponsesBlob",
			}},
			{Kind: llm.ContentText, Text: "a"},
		}},
		llm.User("q2"),
	}}
	body := build(t, req, resolved(nil))
	msgs := body["messages"].([]map[string]any)
	assistant := msgs[1]
	if _, ok := assistant["reasoning_details"]; ok {
		t.Errorf("foreign EncryptedContent replayed into reasoning_details: %v", assistant)
	}
	if got := assistant["reasoning_content"]; got != "pondered" {
		t.Errorf("thinking text should still replay normally, got %v", got)
	}
}

// ported: openaicompat's TestToChatMessages_MergesTextIntoSignatureItem
// (signature survival, text-merge-into-signature-item, duplicate-field
// non-doubling): OpenRouter's Anthropic route delivers a signature as a
// text-less reasoning.text item; the accumulated text must merge INTO that
// item (one item carrying text+signature+format), never as a second,
// separate item beside it.
func TestToChatMessages_MergesTextIntoSignatureItem(t *testing.T) {
	msgs := []llm.Message{{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{{
			Kind: llm.ContentThinking,
			Thinking: &llm.ThinkingData{
				Text:             "think",
				Signature:        "reasoning",
				EncryptedContent: `[{"type":"reasoning.text","signature":"SIGBLOB","format":"anthropic-claude-v1","index":0}]`,
			},
		}, {Kind: llm.ContentText, Text: "hi"}},
	}}
	caps := registry.Caps{Fields: registry.Baseline(registry.ProtocolOpenAIChat)}
	out, err := toChatMessages(msgs, caps, false)
	if err != nil {
		t.Fatalf("toChatMessages: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("messages = %d, want 1", len(out))
	}
	details, _ := out[0]["reasoning_details"].([]map[string]any)
	if len(details) != 1 {
		t.Fatalf("reasoning_details = %#v, want exactly one merged item (not doubled)", out[0]["reasoning_details"])
	}
	d := details[0]
	if d["text"] != "think" || d["signature"] != "SIGBLOB" || d["format"] != "anthropic-claude-v1" {
		t.Fatalf("merged item = %#v, want text+signature+format together", d)
	}
}
