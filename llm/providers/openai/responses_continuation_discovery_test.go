package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
)

func TestResponsesContinuationDiscovery_RequestShapeMatrix(t *testing.T) {
	storeTrue := true

	baseReq := llm.Request{
		Model:    "gpt-5.2",
		Messages: []llm.Message{llm.User("hello")},
	}

	cases := []struct {
		name       string
		adapter    *Adapter
		req        llm.Request
		want       map[string]any
		wantAbsent []string
	}{
		{
			name:    "public default has store false and no provider state handles",
			adapter: &Adapter{},
			req:     baseReq,
			want:    map[string]any{"store": false},
			wantAbsent: []string{
				"previous_response_id",
				"conversation",
			},
		},
		{
			name:    "public previous response only",
			adapter: &Adapter{},
			req: requestShapeMatrixRequest(baseReq, func(req *llm.Request) {
				req.PreviousResponseID = " resp_public "
			}),
			want: map[string]any{
				"store":                false,
				"previous_response_id": "resp_public",
			},
			wantAbsent: []string{"conversation"},
		},
		{
			name:    "public conversation only",
			adapter: &Adapter{},
			req: requestShapeMatrixRequest(baseReq, func(req *llm.Request) {
				req.ConversationID = " conv_public "
			}),
			want: map[string]any{
				"store":        false,
				"conversation": "conv_public",
			},
			wantAbsent: []string{"previous_response_id"},
		},
		{
			name:    "public previous response plus conversation plus explicit store true",
			adapter: &Adapter{},
			req: requestShapeMatrixRequest(baseReq, func(req *llm.Request) {
				req.PreviousResponseID = " resp_public "
				req.ConversationID = " conv_public "
				req.Store = &storeTrue
			}),
			want: map[string]any{
				"store":                true,
				"previous_response_id": "resp_public",
				"conversation":         "conv_public",
			},
		},
		{
			name:    "codex previous response only",
			adapter: &Adapter{ResponsesPath: "/backend-api/codex/responses"},
			req: requestShapeMatrixRequest(baseReq, func(req *llm.Request) {
				req.PreviousResponseID = " resp_codex "
			}),
			want: map[string]any{
				"store":                false,
				"previous_response_id": "resp_codex",
			},
			wantAbsent: []string{"conversation"},
		},
		{
			name:    "codex conversation only",
			adapter: &Adapter{ResponsesPath: "/backend-api/codex/responses"},
			req: requestShapeMatrixRequest(baseReq, func(req *llm.Request) {
				req.ConversationID = " conv_codex "
			}),
			want: map[string]any{
				"store":        false,
				"conversation": "conv_codex",
			},
			wantAbsent: []string{"previous_response_id"},
		},
		{
			name:    "codex previous response plus conversation plus explicit store true",
			adapter: &Adapter{ResponsesPath: "/backend-api/codex/responses"},
			req: requestShapeMatrixRequest(baseReq, func(req *llm.Request) {
				req.PreviousResponseID = " resp_codex "
				req.ConversationID = " conv_codex "
				req.Store = &storeTrue
			}),
			want: map[string]any{
				"store":                true,
				"previous_response_id": "resp_codex",
				"conversation":         "conv_codex",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := tc.adapter.buildRequestBody(tc.req)
			if err != nil {
				t.Fatalf("buildRequestBody: %v", err)
			}
			for key, want := range tc.want {
				if got := body[key]; got != want {
					t.Fatalf("%s = %#v, want %#v in body %#v", key, got, want, body)
				}
			}
			for _, key := range tc.wantAbsent {
				if _, ok := body[key]; ok {
					t.Fatalf("%s present in body %#v", key, body)
				}
			}
		})
	}
}

func requestShapeMatrixRequest(base llm.Request, apply func(*llm.Request)) llm.Request {
	req := base
	apply(&req)
	return req
}

func TestResponsesContinuationDiscovery_MalformedToolCallPayloadSizeProbe(t *testing.T) {
	storeTrue := true
	adapter := &Adapter{}
	malformedCall := llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{{
			Kind: llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{
				ID:        "call_bad",
				Name:      "my_strict_tool",
				Arguments: json.RawMessage(`{"value": broken`),
				Type:      "function",
			},
		}},
	}
	errorResult := llm.ToolResultNamed(
		"call_bad",
		"my_strict_tool",
		map[string]any{
			"is_error": true,
			"message":  "invalid tool arguments JSON",
		},
		true,
	)

	fullBody, err := adapter.buildRequestBody(llm.Request{
		Model: "gpt-5.2",
		Messages: []llm.Message{
			llm.System("stable system prompt"),
			llm.User(strings.Repeat("prior context marker ", 40)),
			malformedCall,
			errorResult,
			llm.User("recover now"),
		},
	})
	if err != nil {
		t.Fatalf("full buildRequestBody: %v", err)
	}
	deltaBody, err := adapter.buildRequestBody(llm.Request{
		Model:              "gpt-5.2",
		Messages:           []llm.Message{errorResult},
		PreviousResponseID: "resp_bad",
		Store:              &storeTrue,
	})
	if err != nil {
		t.Fatalf("delta buildRequestBody: %v", err)
	}

	fullInput := discoveryInputItems(t, fullBody)
	deltaInput := discoveryInputItems(t, deltaBody)
	if discoveryFindItem(fullInput, "function_call", "call_id", "call_bad") == nil {
		t.Fatalf("full-history probe missing historical function_call: %#v", fullInput)
	}
	if discoveryFindItem(deltaInput, "function_call", "call_id", "call_bad") != nil {
		t.Fatalf("delta probe must omit historical function_call: %#v", deltaInput)
	}
	if discoveryFindItem(deltaInput, "function_call_output", "call_id", "call_bad") == nil {
		t.Fatalf("delta probe missing tool output for call_bad: %#v", deltaInput)
	}
	if deltaBody["previous_response_id"] != "resp_bad" {
		t.Fatalf("delta previous_response_id = %#v", deltaBody["previous_response_id"])
	}

	result := discoveryPayloadSizeResult(t, fullBody, deltaBody)
	if result.GrossOmittedHistoricalItemBytes <= 0 {
		t.Fatalf("gross omitted historical item bytes = %d, want positive", result.GrossOmittedHistoricalItemBytes)
	}
	if result.AddedContinuationOverheadBytes <= 0 {
		t.Fatalf("added continuation overhead bytes = %d, want positive", result.AddedContinuationOverheadBytes)
	}
	if result.NetBodySizeDeltaBytes <= 0 {
		t.Fatalf("net body-size delta = %d, want positive; result=%+v", result.NetBodySizeDeltaBytes, result)
	}
	t.Logf("phase0b payload_size_result=%+v", result)
}

type discoveryPayloadSize struct {
	FullHistoryBytes                int
	ResponsesDeltaBytes             int
	GrossOmittedHistoricalItemBytes int
	AddedContinuationOverheadBytes  int
	NetBodySizeDeltaBytes           int
}

func discoveryPayloadSizeResult(t *testing.T, fullBody, deltaBody map[string]any) discoveryPayloadSize {
	t.Helper()
	fullBytes := len(discoveryJSON(t, fullBody))
	deltaBytes := len(discoveryJSON(t, deltaBody))
	omitted := 0
	for _, item := range discoveryInputItems(t, fullBody) {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if itemMap["type"] == "function_call" || discoveryItemContainsText(itemMap, "prior context marker") {
			omitted += len(discoveryJSON(t, itemMap))
		}
	}
	added := len(discoveryJSON(t, map[string]any{
		"previous_response_id": deltaBody["previous_response_id"],
		"store":                deltaBody["store"],
	}))
	return discoveryPayloadSize{
		FullHistoryBytes:                fullBytes,
		ResponsesDeltaBytes:             deltaBytes,
		GrossOmittedHistoricalItemBytes: omitted,
		AddedContinuationOverheadBytes:  added,
		NetBodySizeDeltaBytes:           fullBytes - deltaBytes,
	}
}

func discoveryInputItems(t *testing.T, body map[string]any) []any {
	t.Helper()
	input, ok := body["input"].([]map[string]any)
	if ok {
		items := make([]any, len(input))
		for i := range input {
			items[i] = input[i]
		}
		return items
	}
	anyInput, ok := body["input"].([]any)
	if !ok {
		t.Fatalf("input = %#v, want slice", body["input"])
	}
	return anyInput
}

func discoveryFindItem(items []any, itemType, key, value string) map[string]any {
	for _, itemAny := range items {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		if item["type"] == itemType && item[key] == value {
			return item
		}
	}
	return nil
}

func discoveryItemContainsText(item map[string]any, needle string) bool {
	content, ok := item["content"].([]map[string]any)
	if ok {
		for _, part := range content {
			if text, _ := part["text"].(string); strings.Contains(text, needle) {
				return true
			}
		}
	}
	contentAny, ok := item["content"].([]any)
	if !ok {
		return false
	}
	for _, partAny := range contentAny {
		part, ok := partAny.(map[string]any)
		if !ok {
			continue
		}
		if text, _ := part["text"].(string); strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func discoveryJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal discovery JSON: %v", err)
	}
	return data
}
