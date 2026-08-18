package openai

import "testing"

// TestResponsesURL_NormalizesTrailingV1 covers issue #65: a BaseURL that
// already ends in /v1 must not double the /v1 segment on the Responses API
// URL, matching the normalization chatCompletionsURL already performs for
// the Chat Completions fallback.
func TestResponsesURL_NormalizesTrailingV1(t *testing.T) {
	for _, tc := range []struct {
		name    string
		baseURL string
		path    string // ResponsesPath override; "" uses the default
		want    string
	}{
		{
			name:    "base without trailing /v1 gets /v1/responses appended",
			baseURL: "https://host.example",
			want:    "https://host.example/v1/responses",
		},
		{
			name:    "base with trailing /v1 is not doubled",
			baseURL: "https://host.example/v1",
			want:    "https://host.example/v1/responses",
		},
		{
			name:    "base with trailing /v1/ (slash) is not doubled",
			baseURL: "https://host.example/v1/",
			want:    "https://host.example/v1/responses",
		},
		{
			name:    "default OpenAI base URL is unaffected",
			baseURL: defaultAPIBaseURL,
			want:    defaultAPIBaseURL + defaultResponsesPath,
		},
		{
			name:    "custom path prefix is kept explicit, not stripped",
			baseURL: "https://host.example/v1",
			path:    "custom",
			want:    "https://host.example/v1/custom",
		},
		{
			name:    "codex backend path (no /v1 prefix) is untouched",
			baseURL: "https://chatgpt.com/backend-api/codex",
			path:    defaultCodexResponses,
			want:    "https://chatgpt.com/backend-api/codex" + defaultCodexResponses,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Adapter{BaseURL: tc.baseURL, ResponsesPath: tc.path}
			if got := a.responsesURL(); got != tc.want {
				t.Fatalf("responsesURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestChatCompletionsURL_NormalizesTrailingV1 pins the existing behavior of
// chatCompletionsURL that responsesURL now mirrors.
func TestChatCompletionsURL_NormalizesTrailingV1(t *testing.T) {
	for _, tc := range []struct {
		name    string
		baseURL string
		want    string
	}{
		{
			name:    "base without trailing /v1 gets /v1/chat/completions appended",
			baseURL: "https://host.example",
			want:    "https://host.example/v1/chat/completions",
		},
		{
			name:    "base with trailing /v1 is not doubled",
			baseURL: "https://host.example/v1",
			want:    "https://host.example/v1/chat/completions",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Adapter{BaseURL: tc.baseURL}
			if got := a.chatCompletionsURL(); got != tc.want {
				t.Fatalf("chatCompletionsURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestModelsURL_NormalizesTrailingV1 covers issue #13's validation finding: a
// BaseURL that already ends in /v1 (the form gateway docs hand out, e.g.
// OPENAI_BASE_URL=https://gateway/v1) must not double the /v1 segment on the
// models list URL, matching the normalization responsesURL and
// chatCompletionsURL already perform.
func TestModelsURL_NormalizesTrailingV1(t *testing.T) {
	for _, tc := range []struct {
		name    string
		baseURL string
		want    string
	}{
		{
			name:    "base without trailing /v1 gets /v1/models appended",
			baseURL: "https://host.example",
			want:    "https://host.example/v1/models",
		},
		{
			name:    "base with trailing /v1 is not doubled",
			baseURL: "https://host.example/v1",
			want:    "https://host.example/v1/models",
		},
		{
			name:    "base with trailing /v1/ (slash) is not doubled",
			baseURL: "https://host.example/v1/",
			want:    "https://host.example/v1/models",
		},
		{
			name:    "default OpenAI base URL is unaffected",
			baseURL: defaultAPIBaseURL,
			want:    defaultAPIBaseURL + "/v1/models",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Adapter{BaseURL: tc.baseURL}
			if got := a.modelsURL(); got != tc.want {
				t.Fatalf("modelsURL() = %q, want %q", got, tc.want)
			}
		})
	}
}
