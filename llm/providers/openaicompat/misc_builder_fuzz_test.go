package openaicompat

import (
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/serf/llm"
)

// miscQuirksFromByte derives a ProviderQuirks from a fuzz byte so every
// quirk-conditional branch in buildRequestBody (LockTemperature/LockTopP/
// LockFrequencyPenalty/LockPresencePenalty/ToolChoiceAutoOnly/MaxStopSequences/
// StripEmptyContent/NoJSONSchema/TranslateMaxToXHigh) is reachable. The bits are
// spread across the byte so most combinations get exercised.
func miscQuirksFromByte(q byte) ProviderQuirks {
	return ProviderQuirks{
		LockTemperature:      q&1 == 1,
		LockTopP:             q&2 == 2,
		LockFrequencyPenalty: q&4 == 4,
		LockPresencePenalty:  q&8 == 8,
		ToolChoiceAutoOnly:   q&16 == 16,
		StripEmptyContent:    q&32 == 32,
		NoJSONSchema:         q&64 == 64,
		TranslateMaxToXHigh:  q&128 == 128,
		MaxStopSequences:     int(q % 3), // 0,1,2 — exercises the truncate branch
	}
}

// miscBuilderRequest assembles a rich llm.Request that reaches the multimodal,
// thinking, tool-call, tool-result, developer-role, reasoning-details, and
// provider-options passthrough branches of buildRequestBody/toChatMessages —
// the branches the existing request-build fuzzer (empty quirks, no images/
// thinking/provider-options) leaves cold.
func miscBuilderRequest(model, sys, user string, imgData []byte, thinking, effort string, sel byte, useReasoning bool, passKey, passVal string) llm.Request {
	req := llm.Request{
		Model: model,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: sys}}},
			{Role: llm.RoleDeveloper, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: sys}, {Kind: llm.ContentText, Text: ""}}},
			{Role: llm.RoleUser, Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: user},
				{Kind: llm.ContentImage, Image: &llm.ImageData{Data: imgData, MediaType: "image/png", Detail: "high"}},
				{Kind: llm.ContentImage, Image: &llm.ImageData{URL: user}},
			}},
			{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: thinking}},
				{Kind: llm.ContentText, Text: user},
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_1", Name: "t", Arguments: json.RawMessage(imgData), Type: "function"}},
			}},
			{Role: llm.RoleTool, Content: []llm.ContentPart{
				{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_1", Content: user}},
				{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_2", Content: map[string]any{"k": user}}},
			}},
		},
		Tools: []llm.ToolDefinition{{Name: "t", Description: sys, Parameters: map[string]any{"type": "object"}}},
	}
	if effort != "" {
		req.ReasoningEffort = &effort
	}
	temp := 0.5
	req.Temperature = &temp
	tp := 0.9
	req.TopP = &tp
	mt := 128
	req.MaxTokens = &mt
	req.StopSequences = []string{sys, user, model}
	req.Metadata = map[string]string{"k": user}
	switch sel % 4 {
	case 1:
		req.ToolChoice = &llm.ToolChoice{Mode: "required"}
	case 2:
		req.ToolChoice = &llm.ToolChoice{Mode: "named", Name: "t"}
	case 3:
		req.ResponseFormat = &llm.ResponseFormat{Type: "json_schema", JSONSchema: map[string]any{"type": "object"}}
	}

	ov := map[string]any{}
	if passKey != "" {
		ov[passKey] = passVal
	}
	if useReasoning {
		ov["reasoning"] = map[string]any{"enabled": true}
	}
	if len(ov) > 0 {
		req.ProviderOptions = map[string]any{"openai-compatible": ov}
	}
	return req
}

// miscUseReasoningDetails mirrors the private predicate buildRequestBody computes
// from ProviderOptions so the differential oracle can call toChatMessages with
// the identical flag.
func miscUseReasoningDetails(req llm.Request) bool {
	if req.ProviderOptions == nil {
		return false
	}
	ov, ok := req.ProviderOptions["openai-compatible"].(map[string]any)
	if !ok {
		return false
	}
	_, has := ov["reasoning"]
	return has
}

// FuzzMiscOpenAICompatBuilder drives the openai-compatible request builder
// (buildRequestBody) and its message translator (toChatMessages) directly over
// fuzzed rich requests + fuzzed ProviderQuirks, reaching the multimodal,
// thinking, reasoning-details, provider-options passthrough, and every
// quirk-override branch.
//
// Oracles:
//   - determinism: buildRequestBody is a pure function — two calls on the same
//     inputs must produce deeply-equal bodies.
//   - request-shape differential: body["messages"] must equal an independent
//     toChatMessages call with the same derived useReasoningDetails flag, proving
//     buildRequestBody delegates message translation faithfully.
//   - quirk invariants: locked fields are absent; a positive MaxStopSequences
//     caps the "stop" slice; stream adds stream + stream_options.
//   - round-trip: a successful body always re-marshals to valid JSON carrying the
//     required model + messages fields.
func FuzzMiscOpenAICompatBuilder(f *testing.F) {
	f.Add("compat", "be terse", "hello", []byte(`{"a":1}`), "reason", "high", byte(0), false, "", "")
	f.Add("glm", "", "hi", []byte(``), "", "max", byte(2), true, "top_k", "40")
	f.Add("m", "sys", "", []byte(`not json`), "t", "low", byte(3), false, "temperature", "0.1")
	f.Add("", "s", "u", []byte(`{}`), "th", "", byte(1), true, "", "")

	f.Fuzz(func(t *testing.T, model, sys, user string, imgData []byte, thinking, effort string, sel byte, useReasoning bool, passKey, passVal string) {
		quirks := miscQuirksFromByte(sel)
		stream := sel&1 == 1
		req := miscBuilderRequest(model, sys, user, imgData, thinking, effort, sel, useReasoning, passKey, passVal)

		body, err := buildRequestBody(req, stream, ModelCompat{Quirks: quirks})
		if err != nil {
			return // structured build error (e.g. unsupported tool_choice) is acceptable.
		}

		// Determinism: pure function, equal inputs -> deeply equal output.
		body2, err2 := buildRequestBody(req, stream, ModelCompat{Quirks: quirks})
		if err2 != nil {
			t.Fatalf("buildRequestBody nondeterministic error: first nil, second %v", err2)
		}
		if !reflect.DeepEqual(body, body2) {
			t.Fatalf("buildRequestBody nondeterministic:\n a=%#v\n b=%#v", body, body2)
		}

		// Request-shape differential against the independent message translator.
		// A passthrough key named "messages" legitimately overwrites the field, so
		// only assert the differential when the passthrough did not touch it.
		if passKey != "messages" {
			wantMsgs, msgErr := toChatMessages(req.Messages, quirks, miscUseReasoningDetails(req))
			if msgErr == nil {
				if !reflect.DeepEqual(body["messages"], wantMsgs) {
					t.Fatalf("messages differential: buildRequestBody body != toChatMessages\n got: %#v\nwant: %#v", body["messages"], wantMsgs)
				}
			}
		}

		// Quirk invariants.
		if quirks.LockTemperature {
			if _, ok := body["temperature"]; ok {
				t.Fatalf("LockTemperature quirk left a temperature field: %#v", body["temperature"])
			}
		}
		if quirks.LockTopP {
			if _, ok := body["top_p"]; ok {
				t.Fatalf("LockTopP quirk left a top_p field: %#v", body["top_p"])
			}
		}
		if quirks.MaxStopSequences > 0 {
			if stops, ok := body["stop"].([]string); ok && len(stops) > quirks.MaxStopSequences {
				t.Fatalf("MaxStopSequences=%d not enforced: stop=%#v", quirks.MaxStopSequences, stops)
			}
		}
		if stream {
			if _, ok := body["stream"]; !ok {
				t.Fatalf("stream=true but body has no stream field: %#v", body)
			}
			if _, ok := body["stream_options"]; !ok {
				t.Fatalf("stream=true but body has no stream_options field: %#v", body)
			}
		}

		// Round-trip: valid JSON with required fields.
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("buildRequestBody produced an unmarshalable body: %v\nbody=%#v", err, body)
		}
		var round map[string]any
		if err := json.Unmarshal(b, &round); err != nil {
			t.Fatalf("request body is not valid JSON: %v\njson=%s", err, b)
		}
		for _, k := range []string{"model", "messages"} {
			if _, ok := round[k]; !ok {
				t.Fatalf("request body missing required field %q\njson=%s", k, b)
			}
		}
	})
}

// FuzzMiscOpenAICompatMessages drives the message translator toChatMessages
// directly over fuzzed rich message slices + fuzzed quirks, independent of the
// surrounding request builder.
//
// Oracles:
//   - never panics and never errors (the current translator has no error path,
//     but the signature reserves one; a non-nil error on contract-honoring input
//     is a real change to flag);
//   - determinism: two calls on equal inputs are deeply equal;
//   - role invariant: every emitted message carries a role in the
//     {system,user,assistant,tool} vocabulary the Chat Completions API accepts;
//   - round-trip: the emitted slice always marshals to valid JSON.
func FuzzMiscOpenAICompatMessages(f *testing.F) {
	f.Add("sys", "hi", "reason", []byte(`{"a":1}`), byte(0))
	f.Add("", "", "", []byte(``), byte(0xFF))
	f.Add("s", "u", "t", []byte(`not json`), byte(0x20))

	f.Fuzz(func(t *testing.T, sys, user, thinking string, imgData []byte, sel byte) {
		quirks := miscQuirksFromByte(sel)
		useReasoning := sel&1 == 1
		req := miscBuilderRequest("m", sys, user, imgData, thinking, "high", sel, useReasoning, "", "")

		out, err := toChatMessages(req.Messages, quirks, useReasoning)
		if err != nil {
			t.Fatalf("toChatMessages returned an error for a contract-honoring request: %v", err)
		}

		out2, _ := toChatMessages(req.Messages, quirks, useReasoning)
		if !reflect.DeepEqual(out, out2) {
			t.Fatalf("toChatMessages nondeterministic:\n a=%#v\n b=%#v", out, out2)
		}

		validRoles := map[string]bool{"system": true, "user": true, "assistant": true, "tool": true}
		for _, m := range out {
			role, _ := m["role"].(string)
			if !validRoles[role] {
				t.Fatalf("toChatMessages emitted a message with an invalid role %q: %#v", role, m)
			}
		}

		if _, err := json.Marshal(out); err != nil {
			t.Fatalf("toChatMessages produced an unmarshalable message slice: %v\nout=%#v", err, out)
		}
	})
}
