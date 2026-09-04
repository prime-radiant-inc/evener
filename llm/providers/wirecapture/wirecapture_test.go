package wirecapture

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
	gauth "golang.org/x/oauth2/google"

	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/apilog"
	"primeradiant.com/evener/llm/providers/anthropic"
	"primeradiant.com/evener/llm/providers/chatcompletions"
	"primeradiant.com/evener/llm/providers/google"
	"primeradiant.com/evener/llm/providers/responses"
	"primeradiant.com/evener/llm/providers/tokenauth"
	"primeradiant.com/evener/llm/registry"
)

var update = flag.Bool("update", false, "rewrite testdata/golden files")

// config is the plan-1 golden configuration (llm/registry/golden_test.go),
// repeated here because that file is test-scoped to package registry.
const config = `
default = "anthropic"
[providers."groq-responses"]
base = "groq"
protocol = "openai-responses"
[providers.work]
base = "openai"
base_url = "https://gw.example.com/v1"
protocol = "openai-chat"
surface = "generic"
headers = { "X-Portkey-Provider" = "openai" }
credential_headers = { "Authorization" = "Bearer $PORTKEY_KEY" }
[providers.work.fields]
stream_options = false
[providers.work.models."glm-5.2-nvfp4"]
context_window = 1048576
max_output_tokens = 131072
effort_values = ["high", "max"]
thinking_format = "zai"
[providers.local]
base = "openai-compatible"
base_url = "http://localhost:8080/v1"
auth = "none"
[providers.bedrock]
base = "amazon-bedrock"
[providers.bedrock.vars]
"AWS_REGION" = "us-east-1"
[providers.vertex]
base = "google-vertex-anthropic"
[providers.vertex.vars]
"GOOGLE_VERTEX_PROJECT" = "my-project"
"GOOGLE_VERTEX_LOCATION" = "global"
[providers.azure]
[providers.azure.vars]
"AZURE_RESOURCE_NAME" = "contoso-prod"
[providers.azure.models."gpt55-prod"]
alias_of = "gpt-5.5"
[providers.azure.models."claude-prod"]
alias_of = "claude-opus-4-5"
[providers.orclaude]
base = "openrouter"
protocol = "anthropic"
[providers.orclaude.models."minimax/*"]
surface = "anthropic"
[providers.ollama.models."qwen3*"]
context_window = 40960
`

// secrets are the fixture credentials; the test asserts none of them, nor
// the minted tokens, ever reaches a golden file.
var secrets = map[string]string{
	"ANTHROPIC_API_KEY": "SECRET-anthropic", "OPENAI_API_KEY": "SECRET-openai", "GROQ_API_KEY": "SECRET-groq",
	"OPENROUTER_API_KEY": "SECRET-openrouter", "GEMINI_API_KEY": "SECRET-gemini", "AZURE_API_KEY": "SECRET-azure",
	"AWS_BEARER_TOKEN_BEDROCK": "SECRET-bedrock", "PORTKEY_KEY": "SECRET-portkey", "GOOGLE_VERTEX_API_KEY": "SECRET-vertex-express",
}

var env = map[string]string{
	"OPENAI_ORG_ID": "org-golden", "GOOGLE_VERTEX_PROJECT": "my-project", "GOOGLE_VERTEX_LOCATION": "global", "OLLAMA_HOST": "localhost",
}

const (
	adcToken   = "SECRET-adc-token"
	codexToken = "SECRET-codex-token"
	// credentialPlaceholder stands in for every secret in a golden file.
	credentialPlaceholder = "<credential>"
)

type capture struct {
	Case         string            `json:"case"`
	Ref          string            `json:"ref"`
	Stream       bool              `json:"stream"`
	Method       string            `json:"method"`
	URL          string            `json:"url"`
	Headers      map[string]string `json:"headers"`
	Body         json.RawMessage   `json:"body"`
	PrunedFields []string          `json:"pruned_fields"`
}

// recorder is the fake network: it records the last request and answers
// with the canned success body of the protocol being driven.
type recorder struct {
	mu       sync.Mutex
	last     *http.Request
	lastBody []byte
	respond  func() (body, contentType string)
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	r.mu.Lock()
	r.last, r.lastBody = req, body
	r.mu.Unlock()
	payload, ctype := r.respond()
	return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{"Content-Type": []string{ctype}}, Body: io.NopCloser(strings.NewReader(payload)), Request: req}, nil
}

var canned = map[string]map[bool]string{
	registry.ProtocolAnthropic: {
		false: `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
		true:  "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	},
	registry.ProtocolGoogle: {
		false: `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`,
		true:  "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1,\"totalTokenCount\":2}}\n\n",
	},
	registry.ProtocolOpenAIChat: {
		false: `{"id":"c1","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
		true:  "data: {\"id\":\"c1\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"id\":\"c1\",\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n",
	},
	registry.ProtocolOpenAIResponses: {
		false: `{"id":"resp_1","status":"completed","model":"m","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`,
		true:  "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\nevent: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"ok\"}\n\nevent: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"m\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	},
}

func text(s string) []llm.ContentPart { return []llm.ContentPart{{Kind: llm.ContentText, Text: s}} }

var weatherTool = llm.ToolDefinition{Name: "weather", Description: "Current weather", Parameters: map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}}}

// toolsRequest is the common fixture: a system prompt, one user turn, one
// tool with forced tool choice, the session identifiers the Codex headers
// and the affinity headers read, and a prompt cache key plus 24h retention
// the rows gate independently (spec §7.5: Codex keeps the key but drops
// retention).
func toolsRequest(effort string) llm.Request {
	req := llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: text("You are terse.")},
			{Role: llm.RoleUser, Content: text("What is the weather in Oslo?")},
		},
		Tools:                []llm.ToolDefinition{weatherTool},
		ToolChoice:           &llm.ToolChoice{Mode: "required"},
		SessionID:            "sess-golden",
		ThreadID:             "thread-golden",
		ClientMetadata:       map[string]string{"installation_id": "inst-golden"},
		PromptCacheKey:       "pck-golden",
		PromptCacheRetention: "24h",
	}
	if effort != "" {
		req.ReasoningEffort = new(effort)
	}
	return req
}

func webSearchRequest() llm.Request {
	req := toolsRequest("")
	req.Tools, req.ToolChoice, req.WebSearch = nil, nil, true
	return req
}

// signedToolTurn replays a prior assistant turn whose thinking carried an
// OpenRouter reasoning_details signature, so the golden shows the signed
// round trip of spec §8.4.
func signedToolTurn() llm.Request {
	req := toolsRequest("high")
	req.Messages = append(req.Messages[:2],
		llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "Need the weather tool.", Signature: "reasoning_details", EncryptedContent: `[{"type":"reasoning.text","text":"","signature":"sig-golden","format":"anthropic-claude-v1","index":0}]`}},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_1", Name: "weather", Arguments: []byte(`{"city":"Oslo"}`)}},
		}},
		llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_1", Name: "weather", Content: "12C, rain"}}}},
		llm.Message{Role: llm.RoleUser, Content: text("And tomorrow?")},
	)
	return req
}

type wireCase struct {
	name    string
	ref     string
	stream  bool
	request llm.Request
}

var cases = []wireCase{
	{"anthropic-sonnet-4-5-budget", "anthropic/claude-sonnet-4-5", false, toolsRequest("high")},
	{"anthropic-sonnet-4-5-1m-stream", "anthropic/claude-sonnet-4-5[1m]", true, toolsRequest("high")},
	{"anthropic-opus-4-6-no-effort", "anthropic/claude-opus-4-6", false, toolsRequest("")},
	{"anthropic-opus-4-7-display", "anthropic/claude-opus-4-7", false, toolsRequest("high")},
	{"anthropic-opus-4-5-hybrid", "anthropic/claude-opus-4-5", false, toolsRequest("high")},
	{"openai-gpt-5-5", "openai/gpt-5.5", false, toolsRequest("high")},
	{"openai-codex-gpt-5-6-lite-stream", "openai-codex/gpt-5.6", true, toolsRequest("")},
	{"groq-chat-stream", "groq/openai/gpt-oss-120b", true, toolsRequest("high")},
	{"groq-responses", "groq-responses/openai/gpt-oss-120b", false, toolsRequest("high")},
	{"openrouter-opus-5-signed-tool-turn", "openrouter/anthropic/claude-opus-5", false, signedToolTurn()},
	{"work-glm-zai-stream", "work/glm-5.2-nvfp4", true, toolsRequest("high")},
	{"azure-gpt55-prod", "azure/gpt55-prod", false, toolsRequest("high")},
	{"azure-claude-prod", "azure/claude-prod", false, toolsRequest("high")},
	{"bedrock-sonnet-5-stream", "bedrock/anthropic.claude-sonnet-5", true, webSearchRequest()},
	{"vertex-opus-5", "vertex/claude-opus-5", false, toolsRequest("high")},
	{"vertex-opus-5-stream", "vertex/claude-opus-5", true, toolsRequest("high")},
	{"vertex-gemini-stream", "google-vertex/gemini-2.5-flash", true, toolsRequest("")},
	{"vertex-express-gemini", "google-vertex-express/gemini-2.5-flash", false, toolsRequest("")},
	{"vertex-express-gemini-stream", "google-vertex-express/gemini-2.5-flash", true, toolsRequest("")},
	{"google-flash-lite-web-search", "google/gemini-2.5-flash-lite", false, webSearchRequest()},
	{"ollama-llama3-optional-bearer", "ollama/llama3:8b", false, toolsRequest("")},
}

type harness struct {
	registry *registry.Registry
	sink     *captureSink
}

type captureSink struct {
	mu   sync.Mutex
	last *apilog.APIAttemptRecord
}

func (s *captureSink) AppendAttempt(_ context.Context, r apilog.APIAttemptRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = &r
	return nil
}
func (s *captureSink) AppendSettlement(context.Context, apilog.APIAttemptGroupSettlement) error {
	return nil
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	home := t.TempDir()
	adc := filepath.Join(home, ".config", "gcloud", "application_default_credentials.json")
	if err := os.MkdirAll(filepath.Dir(adc), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(adc, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	now := time.Now()
	if err := authopenai.SaveAuth(state, "openai-codex", authopenai.AuthRecord{Version: 1, Provider: "openai", Source: authopenai.AuthSourceOAuth, ObtainedAt: now, TokenType: "Bearer", AccessToken: "stale", RefreshToken: "rt", Expiry: now.Add(time.Hour), AccountID: "acct_golden"}); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(t.TempDir(), "providers.toml")
	if err := os.WriteFile(cfg, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	lookup := map[string]string{"HOME": home}
	maps.Copy(lookup, env)
	maps.Copy(lookup, secrets)
	r, err := registry.Load(
		registry.WithConfigPath(cfg),
		registry.WithEnv(func(k string) (string, bool) { v, ok := lookup[k]; return v, ok }),
		registry.WithStateRoot(state), registry.WithOffline(true), registry.WithoutCache(),
	)
	if err != nil {
		t.Fatal(err)
	}
	r.ApplyLive("ollama", []registry.Model{{ID: "llama3:8b"}})

	prevDir, prevCreds, prevFind := tokenauth.DefaultCodex.StateDir, tokenauth.DefaultCodex.Credentials, tokenauth.DefaultGCPADC.FindCredentials
	tokenauth.DefaultCodex.StateDir = state
	tokenauth.DefaultCodex.Credentials = func(context.Context, string, string) (authopenai.RuntimeCredentials, error) {
		return authopenai.RuntimeCredentials{BearerToken: codexToken, Source: authopenai.AuthSourceOAuth}, nil
	}
	tokenauth.DefaultGCPADC.FindCredentials = func(context.Context, ...string) (*gauth.Credentials, error) {
		return &gauth.Credentials{TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: adcToken})}, nil
	}
	t.Cleanup(func() {
		tokenauth.DefaultCodex.StateDir, tokenauth.DefaultCodex.Credentials, tokenauth.DefaultGCPADC.FindCredentials = prevDir, prevCreds, prevFind
	})
	return &harness{registry: r, sink: &captureSink{}}
}

func (h *harness) protocol(id string, client *http.Client) llm.Protocol {
	switch id {
	case registry.ProtocolAnthropic:
		return &anthropic.Protocol{Client: client}
	case registry.ProtocolGoogle:
		return &google.Protocol{Client: client}
	case registry.ProtocolOpenAIChat:
		return &chatcompletions.Protocol{Client: client}
	default:
		return &responses.Protocol{Client: client}
	}
}

func (h *harness) run(t *testing.T, c wireCase) capture {
	t.Helper()
	h.sink.mu.Lock()
	h.sink.last = nil
	h.sink.mu.Unlock()
	res, err := h.registry.Resolve(c.ref)
	if err != nil {
		t.Fatalf("%s: resolve %s: %v", c.name, c.ref, err)
	}
	rec := &recorder{respond: func() (string, string) {
		if c.stream {
			return canned[res.Protocol][true], "text/event-stream"
		}
		return canned[res.Protocol][false], "application/json"
	}}
	p := h.protocol(res.Protocol, &http.Client{Transport: rec})
	req := llm.ShapeRequest(c.request, res)
	req.Model = res.ModelID
	ctx := llm.WithAPIAttemptSink(llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_"+c.name)), h.sink)
	if c.stream {
		s, err := p.Stream(ctx, req, res)
		if err != nil {
			t.Fatalf("%s: stream: %v", c.name, err)
		}
		for ev := range s.Events() {
			if ev.Type == llm.StreamEventError {
				t.Fatalf("%s: stream event error: %v", c.name, ev.Err)
			}
		}
	} else if _, err := p.Complete(ctx, req, res); err != nil {
		t.Fatalf("%s: complete: %v", c.name, err)
	}
	llm.WaitForPriorAPIAttempts(ctx)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.last == nil {
		t.Fatalf("%s: nothing was sent", c.name)
	}
	headers := map[string]string{}
	for name, values := range rec.last.Header {
		headers[name] = normalizePlatform(redact(strings.Join(values, ", ")))
	}
	var body bytes.Buffer
	if len(rec.lastBody) > 0 {
		var decoded any
		if err := json.Unmarshal(rec.lastBody, &decoded); err != nil {
			t.Fatalf("%s: body is not JSON: %v\n%s", c.name, err, rec.lastBody)
		}
		enc := json.NewEncoder(&body)
		enc.SetIndent("", "  ")
		_ = enc.Encode(decoded)
	}
	var pruned []string
	h.sink.mu.Lock()
	if h.sink.last != nil {
		pruned = h.sink.last.Request.PrunedFields
	}
	h.sink.mu.Unlock()
	return capture{Case: c.name, Ref: c.ref, Stream: c.stream, Method: rec.last.Method, URL: rec.last.URL.String(), Headers: headers, Body: bytes.TrimSpace(body.Bytes()), PrunedFields: pruned}
}

// normalizePlatform replaces the host's GOOS/GOARCH with placeholders so a
// User-Agent built from runtime.GOOS/GOARCH (tokenauth's Codex header) does
// not bake this machine's platform into a checked-in golden.
func normalizePlatform(v string) string {
	v = strings.ReplaceAll(v, runtime.GOOS, "<os>")
	return strings.ReplaceAll(v, runtime.GOARCH, "<arch>")
}

// redact replaces each fixture secret where it appears inside a header
// value rather than blanking the whole value, so the golden still shows the
// header's shape ("Bearer <credential>" versus a raw "<credential>") and a
// double-applied bearer prefix cannot hide behind the redaction.
func redact(v string) string {
	for _, s := range secrets {
		v = strings.ReplaceAll(v, s, credentialPlaceholder)
	}
	v = strings.ReplaceAll(v, adcToken, credentialPlaceholder)
	return strings.ReplaceAll(v, codexToken, credentialPlaceholder)
}

func TestWireCaptures(t *testing.T) {
	h := newHarness(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := h.run(t, c)
			raw, err := json.MarshalIndent(got, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			raw = append(raw, '\n')
			if bytes.Contains(raw, []byte("SECRET-")) {
				t.Fatalf("credential leaked into the capture: %s", raw)
			}
			path := filepath.Join("testdata", "golden", c.name+".json")
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, raw, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("missing golden %s (run with -update): %v", path, err)
			}
			if !bytes.Equal(want, raw) {
				t.Fatalf("golden mismatch for %s (run with -update after an intended change)\n--- want\n%s\n--- got\n%s", path, want, raw)
			}
		})
	}
}

// TestWireCaptureAssertions pins the transport facts spec §13 names, so a
// golden regeneration cannot silently lose them.
func TestWireCaptureAssertions(t *testing.T) {
	h := newHarness(t)
	byName := map[string]capture{}
	for _, c := range cases {
		byName[c.name] = h.run(t, c)
	}
	has := func(name, key string) bool { _, ok := bodyOf(t, byName[name])[key]; return ok }
	check := func(cond bool, format string, args ...any) {
		t.Helper()
		if !cond {
			t.Errorf(format, args...)
		}
	}
	codex := byName["openai-codex-gpt-5-6-lite-stream"]
	check(strings.HasPrefix(codex.URL, "https://chatgpt.com/backend-api/codex/responses"), "codex url = %s", codex.URL)
	for _, hdr := range []string{"Authorization", "Chatgpt-Account-Id", "Originator", "User-Agent", "X-Openai-Internal-Codex-Responses-Lite", "Session-Id", "Thread-Id", "X-Client-Request-Id"} {
		check(codex.Headers[hdr] != "", "codex header %s missing: %v", hdr, codex.Headers)
	}
	check(codex.Headers["Openai-Organization"] == "" && codex.Headers["Openai-Project"] == "", "codex must not send org/project headers: %v", codex.Headers)
	codexBody := bodyOf(t, codex)
	check(codexBody["parallel_tool_calls"] == false && codexBody["instructions"] == "" && codexBody["tools"] == nil, "codex lite body: %s", codex.Body)
	codexFirstInput := codexBody["input"].([]any)[0].(map[string]any)
	check(codexFirstInput["type"] == "additional_tools" && len(codexFirstInput["tools"].([]any)) == 1, "codex additional_tools item: %s", codex.Body)
	check(codexBody["client_metadata"] != nil && codexBody["metadata"] == nil, "codex client_metadata rule: %s", codex.Body)
	check(codexBody["text"].(map[string]any)["verbosity"] == "low" && codexBody["reasoning"].(map[string]any)["context"] == "all_turns", "codex body constants: %s", codex.Body)
	check(codexBody["model"] == "gpt-5.6-sol", "codex wire id: %v", codexBody["model"])

	openai := byName["openai-gpt-5-5"]
	check(openai.Headers["Openai-Organization"] == "org-golden" && openai.Headers["Authorization"] == "Bearer <credential>", "openai headers: %v", openai.Headers)
	check(has("openai-gpt-5-5", "store") && has("openai-gpt-5-5", "prompt_cache_key"), "openai control fields: %s", openai.Body)

	groq := bodyOf(t, byName["groq-responses"])
	for _, k := range []string{"store", "include", "truncation", "safety_identifier", "prompt_cache_key", "previous_response_id", "metadata"} {
		check(groq[k] == nil, "groq responses must not send %s: %s", k, byName["groq-responses"].Body)
	}
	check(groq["tools"].([]any)[0].(map[string]any)["strict"] == nil, "groq tools carry no strict: %s", byName["groq-responses"].Body)
	groqChat := bodyOf(t, byName["groq-chat-stream"])
	check(groqChat["max_tokens"] != nil && groqChat["max_completion_tokens"] == nil && groqChat["stream_options"] != nil, "groq chat spelling/stream_options: %s", byName["groq-chat-stream"].Body)

	sonnet1m := byName["anthropic-sonnet-4-5-1m-stream"]
	// Sonnet 4.5's 1M window is GA (verified live 2026-08-31), so the [1m] row
	// puts no anthropic-beta on the wire — only the standing auth/version pair.
	check(sonnet1m.Headers["Anthropic-Beta"] == "" && sonnet1m.Headers["X-Api-Key"] == "<credential>" && sonnet1m.Headers["Anthropic-Version"] != "", "sonnet [1m] headers: %v", sonnet1m.Headers)
	check(bodyOf(t, sonnet1m)["model"] == "claude-sonnet-4-5", "sonnet [1m] wire id: %v", bodyOf(t, sonnet1m)["model"])
	opus46 := bodyOf(t, byName["anthropic-opus-4-6-no-effort"])
	check(opus46["thinking"].(map[string]any)["type"] == "adaptive" && opus46["thinking"].(map[string]any)["display"] == nil && opus46["output_config"] == nil, "opus 4.6 adaptive without display: %s", byName["anthropic-opus-4-6-no-effort"].Body)
	opus47 := bodyOf(t, byName["anthropic-opus-4-7-display"])
	check(opus47["thinking"].(map[string]any)["display"] == "summarized" && opus47["output_config"] != nil, "opus 4.7 display+effort: %s", byName["anthropic-opus-4-7-display"].Body)
	opus45 := bodyOf(t, byName["anthropic-opus-4-5-hybrid"])
	check(opus45["thinking"].(map[string]any)["budget_tokens"] != nil && opus45["output_config"] != nil, "opus 4.5 hybrid: %s", byName["anthropic-opus-4-5-hybrid"].Body)

	azure := byName["azure-gpt55-prod"]
	check(azure.URL == "https://contoso-prod.openai.azure.com/openai/v1/responses" && azure.Headers["Api-Key"] == "<credential>" && bodyOf(t, azure)["model"] == "gpt55-prod", "azure responses: %s %v", azure.URL, azure.Headers)
	azureClaude := byName["azure-claude-prod"]
	check(azureClaude.URL == "https://contoso-prod.services.ai.azure.com/anthropic/v1/messages" && azureClaude.Headers["Api-Key"] == "<credential>", "azure claude: %s %v", azureClaude.URL, azureClaude.Headers)

	bedrock := byName["bedrock-sonnet-5-stream"]
	check(bedrock.URL == "https://bedrock-mantle.us-east-1.api.aws/anthropic/v1/messages" && bedrock.Headers["X-Api-Key"] == "<credential>", "bedrock: %s %v", bedrock.URL, bedrock.Headers)
	check(!has("bedrock-sonnet-5-stream", "tools"), "bedrock WebSearch=false drops the tool: %s", bedrock.Body)

	vertex := byName["vertex-opus-5"]
	check(vertex.URL == "https://aiplatform.googleapis.com/v1/projects/my-project/locations/global/publishers/anthropic/models/claude-opus-5:rawPredict", "vertex url = %s", vertex.URL)
	check(vertex.Headers["Authorization"] == "Bearer <credential>" && bodyOf(t, vertex)["anthropic_version"] == "vertex-2023-10-16" && bodyOf(t, vertex)["model"] == nil, "vertex auth/body: %v %s", vertex.Headers, vertex.Body)
	vertexStream := byName["vertex-opus-5-stream"]
	check(strings.HasSuffix(vertexStream.URL, ":streamRawPredict"), "vertex stream url = %s", vertexStream.URL)
	check(bodyOf(t, vertexStream)["stream"] == true && bodyOf(t, vertexStream)["anthropic_version"] == "vertex-2023-10-16" && bodyOf(t, vertexStream)["model"] == nil, "vertex stream body: %s", vertexStream.Body)
	vertexGemini := byName["vertex-gemini-stream"]
	check(strings.HasPrefix(vertexGemini.URL, "https://aiplatform.googleapis.com/v1/projects/my-project/locations/global/publishers/google/models/gemini-2.5-flash:streamGenerateContent") && vertexGemini.Headers["Authorization"] == "Bearer <credential>", "vertex gemini: %s %v", vertexGemini.URL, vertexGemini.Headers)
	check(vertexGemini.Headers["X-Goog-User-Project"] == "my-project" && vertexStream.Headers["X-Goog-User-Project"] == "my-project", "vertex quota project: %v %v", vertexGemini.Headers, vertexStream.Headers)

	gemini := byName["google-flash-lite-web-search"]
	check(gemini.URL == "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash-lite:generateContent" && gemini.Headers["X-Goog-Api-Key"] == "<credential>", "gemini: %s %v", gemini.URL, gemini.Headers)
	check(bodyOf(t, gemini)["tools"].([]any)[0].(map[string]any)["google_search"] != nil, "gemini google_search: %s", gemini.Body)

	express := byName["vertex-express-gemini"]
	check(express.URL == "https://aiplatform.googleapis.com/v1/publishers/google/models/gemini-2.5-flash:generateContent" && express.Headers["X-Goog-Api-Key"] == "<credential>" && express.Headers["Authorization"] == "" && express.Headers["X-Goog-User-Project"] == "", "vertex express: %s %v", express.URL, express.Headers)
	expressStream := byName["vertex-express-gemini-stream"]
	check(strings.HasSuffix(expressStream.URL, ":streamGenerateContent?alt=sse") && expressStream.Headers["X-Goog-Api-Key"] == "<credential>", "vertex express stream: %s %v", expressStream.URL, expressStream.Headers)

	or := bodyOf(t, byName["openrouter-opus-5-signed-tool-turn"])
	msgs := or["messages"].([]any)
	assistant := msgs[2].(map[string]any)
	details := assistant["reasoning_details"].([]any)
	check(len(details) == 1 && details[0].(map[string]any)["signature"] == "sig-golden" && details[0].(map[string]any)["text"] == "Need the weather tool.", "signed reasoning_details round trip: %v", assistant)
	check(or["reasoning"].(map[string]any)["effort"] == "high" && or["tool_choice"] == "auto", "openrouter reasoning/forcing: %s", byName["openrouter-opus-5-signed-tool-turn"].Body)
	check(byName["openrouter-opus-5-signed-tool-turn"].Headers["X-Session-Affinity"] == "sess-golden", "openrouter affinity headers: %v", byName["openrouter-opus-5-signed-tool-turn"].Headers)

	work := byName["work-glm-zai-stream"]
	// The work gateway authors credential_headers.Authorization = "Bearer
	// $PORTKEY_KEY"; auth = bearer must leave it alone, so the wire header
	// is one Bearer prefix, not two (spec §10).
	check(work.Headers["X-Portkey-Provider"] == "openai" && work.Headers["Authorization"] == "Bearer <credential>" && bodyOf(t, work)["thinking"] != nil && bodyOf(t, work)["stream_options"] == nil, "work: %v %s", work.Headers, work.Body)
	check(slices.Contains(work.PrunedFields, "stream_options"), "work pruned fields must name stream_options: %v", work.PrunedFields)

	ollama := byName["ollama-llama3-optional-bearer"]
	check(ollama.URL == "http://localhost:11434/v1/chat/completions" && ollama.Headers["Authorization"] == "", "ollama: %s %v", ollama.URL, ollama.Headers)
}

func bodyOf(t *testing.T, c capture) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(c.Body, &m); err != nil {
		t.Fatalf("%s: %v", c.Case, err)
	}
	return m
}
