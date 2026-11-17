package chatcompletions

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// rawArgsBody builds a Chat Completions response body whose tool-call
// arguments field carries the given raw content as a JSON string value.
// The body is returned as raw bytes so the decode can capture them before
// the lossy map[string]any coercion.
func rawArgsBody(t *testing.T, argsContent string) []byte {
	t.Helper()
	// Build the body using a struct whose Arguments field is json.RawMessage
	// so raw bytes (including invalid UTF-8) survive json.Marshal. The
	// arguments field is a JSON string whose content is argsContent; we
	// build the string token manually to preserve raw bytes.
	argToken := jsonStringToken(argsContent)
	outer := struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Index   int `json:"index"`
			Message struct {
				Role      string `json:"role"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string          `json:"name"`
						Arguments json.RawMessage `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls,omitempty"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}{
		ID:    "chatcmpl-1",
		Model: "m-wire",
		Choices: []struct {
			Index   int `json:"index"`
			Message struct {
				Role      string `json:"role"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string          `json:"name"`
						Arguments json.RawMessage `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls,omitempty"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		}{
			{
				Index: 0,
				Message: struct {
					Role      string `json:"role"`
					ToolCalls []struct {
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string          `json:"name"`
							Arguments json.RawMessage `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls,omitempty"`
				}{
					Role: "assistant",
					ToolCalls: []struct {
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string          `json:"name"`
							Arguments json.RawMessage `json:"arguments"`
						} `json:"function"`
					}{
						{
							ID:   "call_1",
							Type: "function",
							Function: struct {
								Name      string          `json:"name"`
								Arguments json.RawMessage `json:"arguments"`
							}{
								Name:      "write_file",
								Arguments: json.RawMessage(argToken),
							},
						},
					},
				},
				FinishReason: "tool_calls",
			},
		},
	}
	b, err := json.Marshal(outer)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return b
}

// jsonStringToken builds a JSON string token (with surrounding quotes) from
// raw content, escaping only the characters JSON requires for a valid
// string token while preserving raw bytes >= 0x80. This is the inverse of
// rawStringContent: it produces a token that rawStringContent can decode
// back to the original content byte-identically.
func jsonStringToken(content string) []byte {
	var b bytes.Buffer
	b.WriteByte('"')
	for _, c := range []byte(content) {
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if c < 0x20 {
				b.WriteString(`\u00`)
				const hex = "0123456789abcdef"
				b.WriteByte(hex[c>>4])
				b.WriteByte(hex[c&0xf])
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
	return b.Bytes()
}

// decodeRawArgsComplete runs the chatcompletions Complete decode path with a
// scripted response body and returns the tool-call Arguments from the
// decoded response.
func decodeRawArgsComplete(t *testing.T, body []byte) []byte {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	res := resolved(func(c *registry.Caps) { c.FinishReasonMap = map[string]string{"tool_calls": "tool_calls"} })
	res.Transport = registry.Transport{Auth: registry.AuthBearer, BaseURL: srv.URL + "/v1", Endpoint: "/chat/completions", StreamEndpoint: "/chat/completions", ModelsEndpoint: "/models", CountTokensEndpoint: registry.EndpointUnsupported}
	res.Credential = registry.Credential{Value: "k-1", Source: "api_key"}
	p := &Protocol{Client: srv.Client()}
	req := userReq("hi")
	resp, err := p.Complete(context.Background(), llm.ShapeRequest(req, res), res)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(calls))
	}
	return calls[0].Arguments
}

// TestRawArgs_NonStream_InvalidUTF8 asserts that invalid-UTF-8 bytes in
// tool-call arguments survive the non-streaming chatcompletions decode
// byte-identical — no U+FFFD substitution, no re-marshal canonicalization.
//
// RED until the adapter captures arguments as RawMessage from the body.
func TestRawArgs_NonStream_InvalidUTF8(t *testing.T) {
	t.Parallel()
	// A JSON object string value containing a raw 0xff byte.
	argsContent := `{"path":"` + "\xff" + `file.txt"}`
	body := rawArgsBody(t, argsContent)
	got := decodeRawArgsComplete(t, body)
	want := []byte(argsContent)
	if !bytes.Equal(got, want) {
		t.Fatalf("non-stream Arguments = %q (% x), want %q (% x)", got, got, want, want)
	}
	if bytes.Contains(got, []byte{0xEF, 0xBF, 0xBD}) {
		t.Fatalf("non-stream Arguments contains U+FFFD, want raw 0xff preserved: % x", got)
	}
}

// When raw capture succeeds, the adapter must rescue the chosen argument
// string once per tool call, not rescue the fallback and then rescue the raw
// string again.
func TestRawArgs_NonStream_RescuesOncePerToolCall(t *testing.T) {
	// Not parallel: swaps the package's rescueClaudeXMLArgsHook seam.
	calls := 0
	rescueClaudeXMLArgsHook = func() { calls++ }
	t.Cleanup(func() { rescueClaudeXMLArgsHook = nil })

	body := rawArgsBody(t, `{"path":"x"}`)
	decodeRawArgsComplete(t, body)
	if calls != 1 {
		t.Fatalf("rescueClaudeXMLArgs calls = %d, want 1 per tool call", calls)
	}
}

// TestRawArgs_NonStream_NonCanonicalJSON asserts that non-canonical but
// valid JSON (spacing/key-order) survives the decode byte-identical — not
// re-marshaled to canonical form.
//
// RED until the adapter captures arguments as RawMessage from the body.
func TestRawArgs_NonStream_NonCanonicalJSON(t *testing.T) {
	t.Parallel()
	// Non-canonical JSON: spaces after keys, keys in non-sorted order.
	argsContent := `{ "b" : 2 , "a" : 1 }`
	body := rawArgsBody(t, argsContent)
	got := decodeRawArgsComplete(t, body)
	want := []byte(argsContent)
	if !bytes.Equal(got, want) {
		t.Fatalf("non-stream Arguments = %q, want %q (non-canonical JSON should not be re-marshaled)", got, want)
	}
}

// TestRawArgs_NonStream_FallbackOnBadToken asserts that if the focused
// RawMessage capture fails (malformed token), the decode falls back to
// the existing map-decoded args and still produces a valid response.
//
// This pins Mandate 1: degrade, never drop.
func TestRawArgs_NonStream_FallbackOnBadToken(t *testing.T) {
	t.Parallel()
	// arguments is null: the existing decode maps it to "" (string zero
	// value), and the focused RawMessage captures "null" — rawStringContent
	// fails (not a string token), so the decode falls back to the existing
	// map-decoded args (""). The response must still succeed.
	body := []byte(`{"id":"r1","model":"m","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":null}}]},"finish_reason":"tool_calls"}]}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	res := resolved(func(c *registry.Caps) { c.FinishReasonMap = map[string]string{"tool_calls": "tool_calls"} })
	res.Transport = registry.Transport{Auth: registry.AuthBearer, BaseURL: srv.URL + "/v1", Endpoint: "/chat/completions", StreamEndpoint: "/chat/completions", ModelsEndpoint: "/models", CountTokensEndpoint: registry.EndpointUnsupported}
	res.Credential = registry.Credential{Value: "k-1", Source: "api_key"}
	p := &Protocol{Client: srv.Client()}
	req := userReq("hi")
	resp, err := p.Complete(context.Background(), llm.ShapeRequest(req, res), res)
	if err != nil {
		t.Fatalf("Complete (fallback): %v", err)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(calls))
	}
	// The fallback produces the map-decoded value for null: empty string.
	// The key assertion: the decode did not error (degrade, never drop).
	if string(calls[0].Arguments) != "" {
		t.Fatalf("fallback Arguments = %q, want empty (null maps to empty string)", calls[0].Arguments)
	}
}

// --- Streaming ---

// rawArgsStreamSSE builds a Chat Completions SSE stream where the tool-call
// arguments are delivered in fragments. The arguments content may contain
// invalid UTF-8 bytes.
func rawArgsStreamSSE(argsContent string) string {
	// Escape the argsContent for JSON string transport: we need the raw
	// bytes (including 0xff) to arrive as-is in the SSE data field. The
	// SSE data field is not JSON-escaped — it's raw text terminated by \n.
	// The arguments field inside the JSON chunk IS a JSON string, so we
	// need to properly escape it as a JSON string value.
	var esc strings.Builder
	for _, b := range []byte(argsContent) {
		switch b {
		case '"':
			esc.WriteString(`\"`)
		case '\\':
			esc.WriteString(`\\`)
		case '\n':
			esc.WriteString(`\n`)
		case '\r':
			esc.WriteString(`\r`)
		case '\t':
			esc.WriteString(`\t`)
		default:
			if b < 0x20 {
				esc.WriteString(`\u00`)
				const hex = "0123456789abcdef"
				esc.WriteByte(hex[b>>4])
				esc.WriteByte(hex[b&0xf])
			} else {
				esc.WriteByte(b)
			}
		}
	}
	argEsc := esc.String()
	return "data: {\"id\":\"c1\",\"model\":\"m-wire\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"write_file\",\"arguments\":\"" + argEsc + "\"}}]}}]}\n\n" +
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"
}

// decodeRawArgsStream runs the chatcompletions streaming decode with a
// scripted SSE body and returns the tool-call Arguments from the final
// response.
func decodeRawArgsStream(t *testing.T, sseBody string) []byte {
	t.Helper()
	srv, _ := server(t, 200, sseBody)
	res := liveRes(srv, func(c *registry.Caps) { c.FinishReasonMap = map[string]string{"tool_calls": "tool_calls"} })
	p := &Protocol{Client: srv.Client()}
	req := userReq("hi")
	s, err := p.Stream(context.Background(), llm.ShapeRequest(req, res), res)
	if err != nil {
		t.Fatal(err)
	}
	var final *llm.Response
	for ev := range s.Events() {
		if ev.Type == llm.StreamEventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
		if ev.Type == llm.StreamEventFinish {
			final = ev.Response
		}
	}
	if final == nil {
		t.Fatal("stream ended without a finish event")
	}
	calls := final.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(calls))
	}
	return calls[0].Arguments
}

// TestRawArgs_Stream_InvalidUTF8 asserts that invalid-UTF-8 bytes in
// streamed tool-call arguments survive the decode byte-identical.
//
// RED until the streaming adapter preserves raw argument bytes.
func TestRawArgs_Stream_InvalidUTF8(t *testing.T) {
	t.Parallel()
	argsContent := `{"path":"` + "\xff" + `file.txt"}`
	sseBody := rawArgsStreamSSE(argsContent)
	got := decodeRawArgsStream(t, sseBody)
	want := []byte(argsContent)
	if !bytes.Equal(got, want) {
		t.Fatalf("stream Arguments = %q (% x), want %q (% x)", got, got, want, want)
	}
	if bytes.Contains(got, []byte{0xEF, 0xBF, 0xBD}) {
		t.Fatalf("stream Arguments contains U+FFFD, want raw 0xff preserved: % x", got)
	}
}

func TestRawArgs_Stream_ToolCallDeltaPreservesRawBytes(t *testing.T) {
	t.Parallel()
	args := `{"path":"` + "\xff" + `file.txt"}`
	srv, _ := server(t, http.StatusOK, rawArgsStreamSSE(args))
	res := liveRes(srv, func(c *registry.Caps) { c.FinishReasonMap = map[string]string{"tool_calls": "tool_calls"} })
	s, err := (&Protocol{Client: srv.Client()}).Stream(context.Background(), llm.ShapeRequest(userReq("hi"), res), res)
	if err != nil {
		t.Fatal(err)
	}
	var got []byte
	for ev := range s.Events() {
		if ev.Type == llm.StreamEventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
		if ev.Type == llm.StreamEventToolCallDelta && ev.ToolCall != nil {
			got = append(got, ev.ToolCall.Arguments...)
		}
	}
	if !bytes.Equal(got, []byte(args)) {
		t.Fatalf("ToolCallDelta Arguments = %q (% x), want %q (% x)", got, got, args, []byte(args))
	}
}

// TestRawArgs_Stream_NonCanonicalJSON asserts that non-canonical valid JSON
// in streamed tool-call arguments survives the decode byte-identical.
//
// RED until the streaming adapter preserves raw argument bytes.
func TestRawArgs_Stream_NonCanonicalJSON(t *testing.T) {
	t.Parallel()
	argsContent := `{ "b" : 2 , "a" : 1 }`
	sseBody := rawArgsStreamSSE(argsContent)
	got := decodeRawArgsStream(t, sseBody)
	want := []byte(argsContent)
	if !bytes.Equal(got, want) {
		t.Fatalf("stream Arguments = %q, want %q (non-canonical JSON should not be re-marshaled)", got, want)
	}
}

// TestRawArgs_Stream_FallbackOnBadFragment asserts that if a streaming
// argument fragment is malformed, the decode still completes.
func TestRawArgs_Stream_FallbackOnBadFragment(t *testing.T) {
	t.Parallel()
	// arguments is null: the existing decode maps it to "" (string zero
	// value), and the focused RawMessage captures "null" — rawStringContent
	// fails (not a string token), so the decode falls back to the
	// existing string-form args (""). The stream must still complete.
	sseBody := "data: {\"id\":\"c1\",\"model\":\"m-wire\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"f\",\"arguments\":null}}]}}]}\n\n" +
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"
	srv, _ := server(t, 200, sseBody)
	res := liveRes(srv, func(c *registry.Caps) { c.FinishReasonMap = map[string]string{"tool_calls": "tool_calls"} })
	p := &Protocol{Client: srv.Client()}
	req := userReq("hi")
	s, err := p.Stream(context.Background(), llm.ShapeRequest(req, res), res)
	if err != nil {
		t.Fatal(err)
	}
	var final *llm.Response
	for ev := range s.Events() {
		if ev.Type == llm.StreamEventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
		if ev.Type == llm.StreamEventFinish {
			final = ev.Response
		}
	}
	if final == nil {
		t.Fatal("stream ended without a finish event")
	}
	calls := final.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(calls))
	}
	// The fallback produces the map-decoded value for null: empty string.
	// The key assertion: the stream did not error (degrade, never drop).
	if string(calls[0].Arguments) != "" {
		t.Fatalf("fallback Arguments = %q, want empty (null maps to empty string)", calls[0].Arguments)
	}
}

func TestRawArgs_NonStream_MultipleToolCallsPreserveIndex(t *testing.T) {
	t.Parallel()
	first := `{ "first" : "` + "\xff" + `" }`
	second := `{ "second" : "` + "\xfe" + `" }`
	body := []byte(`{"id":"chatcmpl-1","model":"m-wire","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[` +
		`{"id":"call_1","type":"function","function":{"name":"first","arguments":` + string(jsonStringToken(first)) + `}},` +
		`{"id":"call_2","type":"function","function":{"name":"second","arguments":` + string(jsonStringToken(second)) + `}}` +
		`]},"finish_reason":"tool_calls"}]}`)

	got := decodeRawArgsCompleteMany(t, body)
	want := [][]byte{[]byte(first), []byte(second)}
	assertRawArgsByIndex(t, got, want)
}

func TestRawArgs_Stream_MultipleToolCallsPreserveIndex(t *testing.T) {
	t.Parallel()
	first := `{ "first" : "` + "\xff" + `" }`
	second := `{ "second" : "` + "\xfe" + `" }`
	sseBody := "data: {\"id\":\"c1\",\"model\":\"m-wire\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[" +
		`{"index":0,"id":"call_1","type":"function","function":{"name":"first","arguments":` + string(jsonStringToken(first)) + `}},` +
		`{"index":1,"id":"call_2","type":"function","function":{"name":"second","arguments":` + string(jsonStringToken(second)) + `}}` +
		"]}}]}\n\n" +
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"

	got := decodeRawArgsStreamMany(t, sseBody)
	want := [][]byte{[]byte(first), []byte(second)}
	assertRawArgsByIndex(t, got, want)
}

func decodeRawArgsCompleteMany(t *testing.T, body []byte) [][]byte {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	res := resolved(func(c *registry.Caps) { c.FinishReasonMap = map[string]string{"tool_calls": "tool_calls"} })
	res.Transport = registry.Transport{Auth: registry.AuthBearer, BaseURL: srv.URL + "/v1", Endpoint: "/chat/completions", StreamEndpoint: "/chat/completions", ModelsEndpoint: "/models", CountTokensEndpoint: registry.EndpointUnsupported}
	res.Credential = registry.Credential{Value: "k-1", Source: "api_key"}
	resp, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), llm.ShapeRequest(userReq("hi"), res), res)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	return toolCallArguments(resp)
}

// countRawArgsBodyDecodes swaps the package-level focused raw-args body
// decode seam for a counter. Tests that use it must stay serial (not
// t.Parallel); t.Cleanup restores the seam.
func countRawArgsBodyDecodes(t *testing.T) *int {
	t.Helper()
	decodes := 0
	previous := rawArgsBodyDecode
	rawArgsBodyDecode = func(body []byte, v any) error {
		decodes++
		return json.Unmarshal(body, v)
	}
	t.Cleanup(func() { rawArgsBodyDecode = previous })
	return &decodes
}

// chatManyBody builds a Chat Completions body whose message carries a content
// part before the given tool-call fragments.
func chatManyBody(calls ...string) []byte {
	return []byte(`{"id":"chatcmpl-1","model":"m-wire","choices":[{"index":0,"message":{"role":"assistant","content":"hello","tool_calls":[` +
		strings.Join(calls, ",") +
		`]},"finish_reason":"tool_calls"}]}`)
}

// TestRawArgs_NonStream_DecodesBodyOnce pins that one response with several
// tool calls runs the focused raw-args body decode once, not once per call.
// Not parallel: it swaps the package-level rawArgsBodyDecode seam.
func TestRawArgs_NonStream_DecodesBodyOnce(t *testing.T) {
	decodes := countRawArgsBodyDecodes(t)
	first := `{ "first" : "` + "\xff" + `" }`
	second := `{ "second" : "` + "\xfe" + `" }`
	third := `{ "third" : "` + "\xfd" + `" }`
	body := chatManyBody(
		`{"id":"call_1","type":"function","function":{"name":"first","arguments":`+string(jsonStringToken(first))+`}}`,
		`{"id":"call_2","type":"function","function":{"name":"second","arguments":`+string(jsonStringToken(second))+`}}`,
		`{"id":"call_3","type":"function","function":{"name":"third","arguments":`+string(jsonStringToken(third))+`}}`,
	)

	got := decodeRawArgsCompleteMany(t, body)
	assertRawArgsByIndex(t, got, [][]byte{[]byte(first), []byte(second), []byte(third)})
	if *decodes != 1 {
		t.Fatalf("focused raw-args body decodes = %d, want 1", *decodes)
	}
}

// TestRawArgs_NonStream_NoToolCallsDecodesNothing pins that a tool-free
// response performs no focused raw-args decode.
// Not parallel: it swaps the package-level rawArgsBodyDecode seam.
func TestRawArgs_NonStream_NoToolCallsDecodesNothing(t *testing.T) {
	decodes := countRawArgsBodyDecodes(t)
	body := []byte(`{"id":"chatcmpl-1","model":"m-wire","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`)

	got := decodeRawArgsCompleteMany(t, body)
	if len(got) != 0 {
		t.Fatalf("got %d tool calls, want 0", len(got))
	}
	if *decodes != 0 {
		t.Fatalf("focused raw-args body decodes = %d, want 0", *decodes)
	}
}

// TestRawArgs_RecordedJSON_DecodesBodyOnce pins the recorded-response JSON
// branch to one focused body decode for several tool calls.
// Not parallel: it swaps the package-level rawArgsBodyDecode seam.
func TestRawArgs_RecordedJSON_DecodesBodyOnce(t *testing.T) {
	decodes := countRawArgsBodyDecodes(t)
	first := `{ "first" : "` + "\xff" + `" }`
	second := `{ "second" : "` + "\xfe" + `" }`
	third := `{ "third" : "` + "\xfd" + `" }`
	body := chatManyBody(
		`{"id":"call_1","type":"function","function":{"name":"first","arguments":`+string(jsonStringToken(first))+`}}`,
		`{"id":"call_2","type":"function","function":{"name":"second","arguments":`+string(jsonStringToken(second))+`}}`,
		`{"id":"call_3","type":"function","function":{"name":"third","arguments":`+string(jsonStringToken(third))+`}}`,
	)

	resp, err := ExtractRecordedResponse(body, "m-wire")
	if err != nil {
		t.Fatalf("ExtractRecordedResponse: %v", err)
	}
	assertRawArgsByIndex(t, toolCallArguments(resp), [][]byte{[]byte(first), []byte(second), []byte(third)})
	if *decodes != 1 {
		t.Fatalf("focused raw-args body decodes = %d, want 1", *decodes)
	}
}

// TestRawArgs_Stream_CarriesArgsFromTheSingleChunkDecode supersedes
// TestRawArgs_Stream_DecodesChunkOnce, which pinned one *focused* chunk decode
// on top of the chunk's own decode. Each delta's arguments token now rides out
// of that one decode on chatChunkFunction.Arguments, so the invariant is that
// the focused raw-args seam is never called on the streaming path, and
// the assembled arguments stay byte-exact for a chunk carrying several calls.
// Not parallel: it swaps the package-level rawArgsBodyDecode seam.
func TestRawArgs_Stream_CarriesArgsFromTheSingleChunkDecode(t *testing.T) {
	decodes := countRawArgsBodyDecodes(t)
	first := `{ "first" : "` + "\xff" + `" }`
	second := `{ "second" : "` + "\xfe" + `" }`
	third := `{ "third" : "` + "\xfd" + `" }`
	sseBody := "data: {\"id\":\"c1\",\"model\":\"m-wire\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\",\"tool_calls\":[" +
		`{"index":0,"id":"call_1","type":"function","function":{"name":"first","arguments":` + string(jsonStringToken(first)) + `}},` +
		`{"index":1,"id":"call_2","type":"function","function":{"name":"second","arguments":` + string(jsonStringToken(second)) + `}},` +
		`{"index":2,"id":"call_3","type":"function","function":{"name":"third","arguments":` + string(jsonStringToken(third)) + `}}` +
		"]}}]}\n\n" +
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"

	got := decodeRawArgsStreamMany(t, sseBody)
	assertRawArgsByIndex(t, got, [][]byte{[]byte(first), []byte(second), []byte(third)})
	if *decodes != 0 {
		t.Fatalf("focused raw-args chunk decodes = %d, want 0 (the chunk's own decode must carry the arguments)", *decodes)
	}
}

// rawArgsChunkedSSE builds a single-tool-call stream that delivers frags as
// one chunk each. Every fragment is escaped on its own, so each chunk is a
// complete JSON chunk and a fragment boundary may land anywhere in the
// assembled arguments, including beside a JSON escape.
func rawArgsChunkedSSE(frags []string) string {
	var b strings.Builder
	for _, frag := range frags {
		b.WriteString(`data: {"id":"c1","model":"m-wire","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"write_file","arguments":`)
		b.Write(jsonStringToken(frag))
		b.WriteString("}}]}}]}\n\n")
	}
	b.WriteString("data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

// argContent is the assembled tool arguments for the split tests. It holds a
// JSON \u escape, an escaped backslash pair and a multi-byte rune, so the
// split cases below put a chunk boundary beside each of them.
const argContent = `{"path":"a\\b","u":"\u00e9","q":"say \"hi\"","note":"é"}`

// argContentUnits returns the smallest fragments a provider can delimit: one
// rune each, so every boundary between two units escapes to a valid chunk.
func argContentUnits(content string) []string {
	units := make([]string, 0, len(content))
	for _, r := range content {
		units = append(units, string(r))
	}
	return units
}

// TestRawArgs_Stream_SplitArgContentAssemblesByteExact cuts argContent into
// two chunks at every unit boundary and requires the assembled arguments to
// equal the content byte for byte. The boundaries next to the \u escape, the
// escaped backslashes and the quoted text put a lone "\" or "\\" at the end
// of one chunk and its continuation at the start of the next.
func TestRawArgs_Stream_SplitArgContentAssemblesByteExact(t *testing.T) {
	t.Parallel()
	units := argContentUnits(argContent)
	if len(units) < 8 {
		t.Fatalf("fixture has %d units, want at least 8 to cut between", len(units))
	}
	for cut := 1; cut < len(units); cut++ {
		frags := []string{strings.Join(units[:cut], ""), strings.Join(units[cut:], "")}
		got := decodeRawArgsStream(t, rawArgsChunkedSSE(frags))
		if !bytes.Equal(got, []byte(argContent)) {
			t.Fatalf("cut %d of %d: Arguments = %q, want %q", cut, len(units), got, argContent)
		}
	}
}

// TestRawArgs_Stream_ManyArgumentChunksAssembleByteExact delivers argContent
// one rune per chunk and requires the assembled arguments to equal it byte for
// byte.
func TestRawArgs_Stream_ManyArgumentChunksAssembleByteExact(t *testing.T) {
	t.Parallel()
	got := decodeRawArgsStream(t, rawArgsChunkedSSE(argContentUnits(argContent)))
	if !bytes.Equal(got, []byte(argContent)) {
		t.Fatalf("Arguments = %q, want %q", got, argContent)
	}
}

// TestRawArgs_Stream_SplitMultiByteRuneAcrossChunks puts a chunk boundary
// between the two bytes of "é", so neither chunk's string token holds a whole
// rune. Unescaping per chunk must still reproduce the original two bytes.
func TestRawArgs_Stream_SplitMultiByteRuneAcrossChunks(t *testing.T) {
	t.Parallel()
	frags := []string{`{"note":"` + "\xc3", "\xa9" + `"}`}
	want := []byte(`{"note":"` + "\xc3\xa9" + `"}`)
	got := decodeRawArgsStream(t, rawArgsChunkedSSE(frags))
	if !bytes.Equal(got, want) {
		t.Fatalf("Arguments = %q (% x), want %q (% x)", got, got, want, want)
	}
}

// TestRawArgs_Stream_InterleavedToolCallsAssembleByIndex streams two tool
// calls whose deltas interleave across chunks, with the chunk's array order
// reversed on the second chunk. Each call must assemble its own fragments, and
// each chunk's arguments must stay with the call it belongs to.
func TestRawArgs_Stream_InterleavedToolCallsAssembleByIndex(t *testing.T) {
	t.Parallel()
	first := []string{`{"first":`, `"a`, `\u00e9"}`}
	second := []string{`{"second":`, `"b`, `\\c"}`}
	chunk := func(calls string) string {
		return "data: {\"id\":\"c1\",\"model\":\"m-wire\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[" + calls + "]}}]}\n\n"
	}
	call := func(index int, id, name, frag string) string {
		return `{"index":` + strconv.Itoa(index) + `,"id":"` + id + `","type":"function","function":{"name":"` + name + `","arguments":` + string(jsonStringToken(frag)) + `}}`
	}
	sseBody := chunk(call(0, "call_1", "first", first[0])+","+call(1, "call_2", "second", second[0])) +
		chunk(call(1, "call_2", "second", second[1])+","+call(0, "call_1", "first", first[1])) +
		chunk(call(0, "call_1", "first", first[2])+","+call(1, "call_2", "second", second[2])) +
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"

	got := decodeRawArgsStreamMany(t, sseBody)
	assertRawArgsByIndex(t, got, [][]byte{
		[]byte(strings.Join(first, "")),
		[]byte(strings.Join(second, "")),
	})
}

// TestRawArgs_Stream_ChunkWithoutArgumentsCarriesNoFragment pins that a
// chunk whose tool-call entry omits arguments, or sends null or "", still
// assembles the fragments the rest of the stream carries, and emits no
// tool-call delta for the empty one. A delta with no argument bytes is not a
// tool-call delta: the adapter only reports argument content.
func TestRawArgs_Stream_ChunkWithoutArgumentsCarriesNoFragment(t *testing.T) {
	t.Parallel()
	for name, emptyToken := range map[string]string{
		"absent": ``,
		"null":   `,"arguments":null`,
		"empty":  `,"arguments":""`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rest := []string{`{"a`, `":`, `1}`}
			sseBody := "data: {\"id\":\"c1\",\"model\":\"m-wire\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"write_file\"" + emptyToken + "}}]}}]}\n\n" +
				rawArgsChunkedSSE(rest)

			srv, _ := server(t, http.StatusOK, sseBody)
			res := liveRes(srv, func(c *registry.Caps) { c.FinishReasonMap = map[string]string{"tool_calls": "tool_calls"} })
			s, err := (&Protocol{Client: srv.Client()}).Stream(context.Background(), llm.ShapeRequest(userReq("hi"), res), res)
			if err != nil {
				t.Fatal(err)
			}
			var deltaBytes []byte
			var deltas int
			var final *llm.Response
			for ev := range s.Events() {
				if ev.Type == llm.StreamEventError {
					t.Fatalf("stream error: %v", ev.Err)
				}
				if ev.Type == llm.StreamEventToolCallDelta && ev.ToolCall != nil {
					deltas++
					if len(ev.ToolCall.Arguments) == 0 {
						t.Fatalf("tool-call delta %d carries no argument bytes", deltas)
					}
					deltaBytes = append(deltaBytes, ev.ToolCall.Arguments...)
				}
				if ev.Type == llm.StreamEventFinish {
					final = ev.Response
				}
			}
			if final == nil {
				t.Fatal("stream ended without a finish event")
			}
			want := []byte(strings.Join(rest, ""))
			if deltas != len(rest) {
				t.Fatalf("tool-call deltas = %d, want %d", deltas, len(rest))
			}
			if !bytes.Equal(deltaBytes, want) {
				t.Fatalf("delta Arguments = %q, want %q", deltaBytes, want)
			}
			assertRawArgsByIndex(t, toolCallArguments(*final), [][]byte{want})
		})
	}
}

// TestRawArgs_Stream_NonStringArgumentsSkipTheChunk pins that an arguments
// token that is not a JSON string still fails its chunk's decode, so the
// chunk rides out as a raw passthrough provider event and starts no tool
// call. Degrade, never drop: a rejected token is reported, not silently
// coerced into argument bytes.
func TestRawArgs_Stream_NonStringArgumentsSkipTheChunk(t *testing.T) {
	t.Parallel()
	for name, token := range map[string]string{
		"number": `123`,
		"object": `{"a":1}`,
		"bool":   `true`,
		"array":  `[1]`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sseBody := "data: {\"id\":\"c1\",\"model\":\"m-wire\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"f\",\"arguments\":" + token + "}}]}}]}\n\n" +
				"data: [DONE]\n\n"
			srv, _ := server(t, http.StatusOK, sseBody)
			res := liveRes(srv, func(c *registry.Caps) { c.FinishReasonMap = map[string]string{"tool_calls": "tool_calls"} })
			s, err := (&Protocol{Client: srv.Client()}).Stream(context.Background(), llm.ShapeRequest(userReq("hi"), res), res)
			if err != nil {
				t.Fatal(err)
			}
			var passThrough int
			var final *llm.Response
			for ev := range s.Events() {
				switch ev.Type {
				case llm.StreamEventError:
					t.Fatalf("stream error: %v", ev.Err)
				case llm.StreamEventProviderEvent:
					passThrough++
				case llm.StreamEventToolCallStart, llm.StreamEventToolCallDelta, llm.StreamEventToolCallEnd:
					t.Fatalf("chunk with a non-string %s arguments token produced a %s event", name, ev.Type)
				case llm.StreamEventFinish:
					final = ev.Response
				}
			}
			if passThrough != 1 {
				t.Fatalf("raw passthrough events = %d, want 1", passThrough)
			}
			if final == nil {
				t.Fatal("stream ended without a finish event")
			}
			if calls := final.ToolCalls(); len(calls) != 0 {
				t.Fatalf("tool calls = %d, want 0", len(calls))
			}
		})
	}
}

func decodeRawArgsStreamMany(t *testing.T, sseBody string) [][]byte {
	t.Helper()
	srv, _ := server(t, http.StatusOK, sseBody)
	res := liveRes(srv, func(c *registry.Caps) { c.FinishReasonMap = map[string]string{"tool_calls": "tool_calls"} })
	s, err := (&Protocol{Client: srv.Client()}).Stream(context.Background(), llm.ShapeRequest(userReq("hi"), res), res)
	if err != nil {
		t.Fatal(err)
	}
	var final *llm.Response
	for ev := range s.Events() {
		if ev.Type == llm.StreamEventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
		if ev.Type == llm.StreamEventFinish {
			final = ev.Response
		}
	}
	if final == nil {
		t.Fatal("stream ended without a finish event")
	}
	return toolCallArguments(*final)
}

func toolCallArguments(resp llm.Response) [][]byte {
	calls := resp.ToolCalls()
	args := make([][]byte, len(calls))
	for i := range calls {
		args[i] = calls[i].Arguments
	}
	return args
}

func assertRawArgsByIndex(t *testing.T, got, want [][]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d tool calls, want %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("tool call %d Arguments = %q (% x), want %q (% x)", i, got[i], got[i], want[i], want[i])
		}
	}
}
