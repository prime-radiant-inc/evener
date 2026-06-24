package openai

import (
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
