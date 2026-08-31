package llm

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/llm/registry"
)

func chatRes(maxTokensField string) registry.Resolved {
	res := registry.Resolved{Instance: "work", Protocol: registry.ProtocolOpenAIChat, ModelID: "glm-5.2-nvfp4"}
	if maxTokensField != "" {
		res.Caps.MaxTokensField = new(maxTokensField)
	}
	return res
}

var responsesRes = registry.Resolved{Instance: "groq-responses", Protocol: registry.ProtocolOpenAIResponses, ModelID: "openai/gpt-oss-120b"}
var anthropicRes = registry.Resolved{Instance: "anthropic", Protocol: registry.ProtocolAnthropic, ModelID: "claude-opus-5"}

func TestClassifyHTTPErrorTable(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		headers   http.Header
		body      string
		res       registry.Resolved
		kind      ErrorKind
		retryable bool
		code      string
		hint      string // "" = no hint; "generic" = the generic inspect hint
	}{
		{
			name: "groq 413 TPM ceiling beats the rate-limit code", status: 413,
			body: `{"error":{"message":"Request too large for model ` + "`openai/gpt-oss-120b`" + ` in organization ` + "`org_01`" + ` service tier ` + "`on_demand`" + ` on tokens per minute (TPM): Limit 8000, Requested 12000, please reduce your message size and try again.","type":"tokens","code":"rate_limit_exceeded"}}`,
			res:  responsesRes, kind: KindContextLength, code: "rate_limit_exceeded",
		},
		{
			name: "openai context_length_exceeded", status: 400,
			body: `{"error":{"message":"This model's maximum context length is 128000 tokens. However, your messages resulted in 130000 tokens.","type":"invalid_request_error","param":"messages","code":"context_length_exceeded"}}`,
			res:  chatRes(""), kind: KindContextLength, code: "context_length_exceeded",
		},
		{
			name: "request_too_large code names context length", status: 400,
			body: `{"error":{"message":"Request too large","type":"invalid_request_error","code":"request_too_large"}}`,
			res:  chatRes(""), kind: KindContextLength, code: "request_too_large",
		},
		{
			name: "openai chat unrecognized argument with param null names the other spelling", status: 400,
			body: `{"error":{"message":"Unrecognized request argument supplied: max_completion_tokens","type":"invalid_request_error","param":null,"code":null}}`,
			res:  chatRes("max_completion_tokens"), kind: KindInvalidRequest, code: "invalid_request_error",
			hint: `set max_tokens_field = "max_tokens" on work/glm-5.2-nvfp4`,
		},
		{
			name: "openai unknown field regex names the max-tokens spelling", status: 400,
			body: `{"error":{"message":"unknown field max_completion_tokens","type":"invalid_request_error"}}`,
			res:  chatRes("max_completion_tokens"), kind: KindInvalidRequest, code: "invalid_request_error",
			hint: `set max_tokens_field = "max_tokens" on work/glm-5.2-nvfp4`,
		},
		{
			name: "openai unsupported max_tokens names max_completion_tokens", status: 400,
			body: `{"error":{"message":"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.","type":"invalid_request_error","param":"max_tokens","code":"unsupported_parameter"}}`,
			res:  chatRes(""), kind: KindInvalidRequest, code: "unsupported_parameter",
			hint: `set max_tokens_field = "max_completion_tokens" on work/glm-5.2-nvfp4`,
		},
		{
			name: "openai unsupported parameter regex without a structured param", status: 400,
			body: `{"error":{"message":"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.","type":"invalid_request_error","param":null,"code":null}}`,
			res:  chatRes(""), kind: KindInvalidRequest, code: "invalid_request_error",
			hint: `set max_tokens_field = "max_completion_tokens" on work/glm-5.2-nvfp4`,
		},
		{
			name: "responses unknown_parameter in the prunable set", status: 400,
			body: `{"error":{"message":"Unknown parameter: 'store'.","type":"invalid_request_error","param":"store","code":"unknown_parameter"}}`,
			res:  responsesRes, kind: KindInvalidRequest, code: "unknown_parameter",
			hint: "run `evener models inspect groq-responses/openai/gpt-oss-120b` and set fields.store = false",
		},
		{
			name: "responses unknown nested parameter gets the generic hint", status: 400,
			body: `{"error":{"message":"Unknown parameter: 'reasoning.summary'.","type":"invalid_request_error","param":"reasoning.summary","code":"unknown_parameter"}}`,
			res:  responsesRes, kind: KindInvalidRequest, code: "unknown_parameter", hint: "generic",
		},
		{
			name: "responses unknown parameter regex without a structured param", status: 400,
			body: `{"error":{"message":"Unknown parameter: 'metadata'.","type":"invalid_request_error","param":null,"code":null}}`,
			res:  responsesRes, kind: KindInvalidRequest, code: "invalid_request_error",
			hint: "run `evener models inspect groq-responses/openai/gpt-oss-120b` and set fields.metadata = false",
		},
		{
			name: "groq invalid JSON body", status: 400,
			body: `{"error":{"message":"invalid JSON body","type":"invalid_request_error"}}`,
			res:  responsesRes, kind: KindInvalidRequest, code: "invalid_request_error", hint: "generic",
		},
		{
			name: "anthropic prompt is too long", status: 400,
			body: `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 213462 tokens > 200000 maximum"}}`,
			res:  anthropicRes, kind: KindContextLength, code: "invalid_request_error",
		},
		{
			name: "reduce the length message names context length", status: 400,
			body: `{"error":{"message":"Please reduce the length of the messages.","type":"invalid_request_error"}}`,
			res:  chatRes(""), kind: KindContextLength, code: "invalid_request_error",
		},
		{
			name: "maximum context length message without a structured code", status: 400,
			body: `{"error":{"message":"This model's maximum context length is 8192 tokens.","type":"invalid_request_error"}}`,
			res:  chatRes(""), kind: KindContextLength, code: "invalid_request_error",
		},
		{
			name: "anthropic not supported with thinking names no parameter", status: 400,
			body: "{\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"`top_k` is not supported with thinking.\"}}",
			res:  anthropicRes, kind: KindInvalidRequest, code: "invalid_request_error", hint: "generic",
		},
		{
			name: "openai insufficient_quota", status: 429,
			body: `{"error":{"message":"You exceeded your current quota, please check your plan and billing details.","type":"insufficient_quota","param":null,"code":"insufficient_quota"}}`,
			res:  chatRes(""), kind: KindQuotaExceeded, code: "insufficient_quota",
		},
		{
			name: "chatgpt usage_limit_reached by code", status: 429,
			body: `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","resets_in_seconds":3600}}`,
			res:  registry.Resolved{Instance: "openai-codex", Protocol: registry.ProtocolOpenAIResponses, ModelID: "gpt-5.6"}, kind: KindQuotaExceeded, code: "usage_limit_reached",
		},
		{
			name: "usage limit by phrase on 429", status: 429,
			body: `{"error":{"type":"rate_limit_error","message":"You have hit your usage limit. Try again later."}}`,
			res:  anthropicRes, kind: KindQuotaExceeded, code: "rate_limit_error",
		},
		{
			name: "rate limit with Retry-After", status: 429, headers: http.Header{"Retry-After": []string{"20"}},
			body: `{"error":{"message":"Rate limit reached","type":"requests","code":"rate_limit_exceeded"}}`,
			res:  chatRes(""), kind: KindRateLimit, retryable: true, code: "rate_limit_exceeded",
		},
		{
			name: "rate limit with x-ratelimit-reset", status: 429, headers: http.Header{"X-Ratelimit-Reset-Requests": []string{time.Now().Add(30 * time.Second).UTC().Format(time.RFC3339)}},
			body: `{"error":{"message":"slow down","type":"rate_limit_error"}}`,
			res:  chatRes(""), kind: KindRateLimit, retryable: true, code: "rate_limit_error",
		},
		{name: "401", status: 401, body: `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`, res: chatRes(""), kind: KindAuthentication, code: "invalid_api_key"},
		{name: "404 model", status: 404, body: `{"error":{"message":"The model 'nope' does not exist","type":"invalid_request_error","code":"model_not_found"}}`, res: chatRes(""), kind: KindNotFound, code: "model_not_found"},
		{name: "500", status: 500, body: `{"error":{"message":"boom","type":"server_error"}}`, res: chatRes(""), kind: KindServer, retryable: true, code: "server_error"},
		{name: "non-JSON 502", status: 502, body: `<html>Bad Gateway</html>`, res: chatRes(""), kind: KindServer, retryable: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ClassifyHTTPError("op", tc.status, tc.headers, []byte(tc.body), tc.res)
			var le Error
			if !errors.As(err, &le) {
				t.Fatalf("not an llm.Error: %v", err)
			}
			if Kind(err) != tc.kind {
				t.Fatalf("kind = %v, want %v (%v)", Kind(err), tc.kind, err)
			}
			if le.Retryable() != tc.retryable {
				t.Fatalf("retryable = %v, want %v", le.Retryable(), tc.retryable)
			}
			if le.ErrorCode() != tc.code {
				t.Fatalf("code = %q, want %q", le.ErrorCode(), tc.code)
			}
			if le.Provider() != tc.res.Instance || ErrorProtocol(err) != tc.res.Protocol {
				t.Fatalf("provider/protocol = %q/%q", le.Provider(), ErrorProtocol(err))
			}
			if le.StatusCode() != tc.status {
				t.Fatalf("status = %d", le.StatusCode())
			}
			switch tc.hint {
			case "":
				if h := ErrorHint(err); h != "" {
					t.Fatalf("unexpected hint %q", h)
				}
			case "generic":
				want := "run `evener models inspect " + tc.res.Instance + "/" + tc.res.ModelID + "`; this endpoint rejected a field the registry sends; compare the pruned-field list against the provider's documentation"
				if ErrorHint(err) != want {
					t.Fatalf("hint = %q, want %q", ErrorHint(err), want)
				}
			default:
				if ErrorHint(err) != tc.hint {
					t.Fatalf("hint = %q, want %q", ErrorHint(err), tc.hint)
				}
				if !strings.Contains(err.Error(), "hint: "+tc.hint) {
					t.Fatalf("Error() must render the hint: %q", err.Error())
				}
			}
			if tc.status == 429 && tc.headers != nil {
				if le.RetryAfter() == nil || *le.RetryAfter() < 19*time.Second || *le.RetryAfter() > 31*time.Second {
					t.Fatalf("retry after = %v", le.RetryAfter())
				}
			}
			if tc.name == "chatgpt usage_limit_reached by code" {
				if _, ok := UsageLimitResetAt(err); !ok {
					t.Fatal("usage limit reset time lost")
				}
			}
		})
	}
}

func TestClassifyHTTPErrorKeepsProviderMessageVerbatim(t *testing.T) {
	err := ClassifyHTTPError("messages.create", 400, nil, []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 1 > 0 maximum"}}`), anthropicRes)
	if !strings.Contains(err.Error(), "prompt is too long: 1 > 0 maximum") || !strings.HasPrefix(err.Error(), "anthropic error (status=400): messages.create") {
		t.Fatalf("message = %q", err.Error())
	}
	if ErrorProtocol(errors.New("plain")) != "" || ErrorHint(errors.New("plain")) != "" {
		t.Fatal("plain errors carry no protocol or hint")
	}
}
