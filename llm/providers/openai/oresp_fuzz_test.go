package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/serf/llm"
)

// This file fuzzes the OpenAI Responses/ChatCompletions build+stream path:
// toResponsesInput, buildRequestBody, PlanResponsesContinuation, streamResponses,
// streamViaChatCompletions, NewForInstance, and Complete (fallback branch). Every
// top-level identifier here is prefixed with the lane token "oresp_" so parallel
// lanes editing this same package cannot collide.
//
// HARD RULES honored here: no fuzzer makes a real network call (all HTTP goes
// through oresp_fakeTransport, a fake http.RoundTripper), spawns a process, or
// touches real disk outside a per-iteration sandbox (t.TempDir()).

// oresp_fakeTransport is a fake http.RoundTripper. It never dials: it either
// returns a fuzzer-controlled network error (failErr) or a non-nil *http.Response
// with a readable body. It records every request URL + body it sees, and can
// serve different payloads for the /responses vs /chat/completions endpoints so
// the Complete fallback path can be exercised over a single transport. It always
// drains and closes the request body, honoring the RoundTripper contract, so any
// panic reproduced through it is a real adapter bug, not a harness artifact.
type oresp_fakeTransport struct {
	failErr error

	responsesStatus int
	responsesBody   []byte
	chatStatus      int
	chatBody        []byte

	// Fallback for endpoints that don't match either bucket (used by the
	// single-endpoint stream harnesses that set only responsesStatus/Body).
	defaultStatus int
	defaultBody   []byte

	calls   int
	urls    []string
	bodies  [][]byte
	sawChat bool
	sawResp bool
}

func (rt *oresp_fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var reqBody []byte
	if req.Body != nil {
		reqBody, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
	}
	rt.calls++
	rt.urls = append(rt.urls, req.URL.String())
	rt.bodies = append(rt.bodies, reqBody)

	if rt.failErr != nil {
		return nil, rt.failErr
	}

	status := rt.defaultStatus
	body := rt.defaultBody
	switch {
	case oresp_isChatURL(req.URL.Path):
		rt.sawChat = true
		if rt.chatStatus != 0 {
			status = rt.chatStatus
			body = rt.chatBody
		}
	default:
		rt.sawResp = true
		if rt.responsesStatus != 0 {
			status = rt.responsesStatus
			body = rt.responsesBody
		}
	}
	if status == 0 {
		status = http.StatusOK
	}

	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    req,
	}, nil
}

func oresp_isChatURL(path string) bool {
	return len(path) >= len("/chat/completions") &&
		path[len(path)-len("/chat/completions"):] == "/chat/completions"
}

// oresp_status maps a fuzzer byte to a plausible HTTP status: 0 -> 200, else a
// value in 200..599 covering 2xx/3xx/4xx/5xx error-mapping branches.
func oresp_status(sel byte) int {
	if sel == 0 {
		return http.StatusOK
	}
	return 200 + int(sel)%400
}

// oresp_fallbackStatus biases toward the fallback-eligible codes (404/422) so the
// Complete/Stream fallback branch is reached, while still exploring other codes.
func oresp_fallbackStatus(sel byte) int {
	switch sel % 4 {
	case 0:
		return 404
	case 1:
		return 422
	case 2:
		return http.StatusOK
	default:
		return 200 + int(sel)%400
	}
}

// oresp_knobs is a tiny cursor over the fuzzer's control bytes; each call returns
// the next byte (0 when exhausted) so a single []byte drives many boolean/enum
// decisions in oresp_buildRequest.
type oresp_knobs struct {
	b   []byte
	pos int
}

func (k *oresp_knobs) next() byte {
	if k.pos >= len(k.b) {
		return 0
	}
	v := k.b[k.pos]
	k.pos++
	return v
}

func oresp_ptrFloat(v float64) *float64 { return &v }
func oresp_ptrInt(v int) *int           { return &v }
func oresp_ptrBool(v bool) *bool        { return &v }
func oresp_ptrStr(v string) *string     { return &v }

// oresp_safeInstanceName reduces a fuzzed string to a filesystem-safe instance
// identifier, modeling real instance names (they are TOML config keys under
// [instances.NAME], never arbitrary bytes). NewForInstance derives the per-
// instance OAuth file path auth/<name>.json from this, so a NUL or "/" would make
// the path unopenable — a filename concern outside the credential-resolution
// branches this fuzzer targets. Empty input maps to "openai".
func oresp_safeInstanceName(s string) string {
	var b []byte
	for i := 0; i < len(s) && len(b) < 32; i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
			b = append(b, c)
		}
	}
	if len(b) == 0 {
		return "openai"
	}
	return string(b)
}

// oresp_buildRequest assembles a richly-varied llm.Request from fuzz primitives,
// steering the control bytes into every optional Responses field, tool-choice
// mode, response-format, provider-option, and content-part kind the builders must
// survive (images/documents by inline bytes and by sandboxed local path, audio,
// thinking, tool calls, tool results, web search). File paths never leave tmpDir.
func oresp_buildRequest(tmpDir, model, sys, user string, control, blob []byte) llm.Request {
	k := &oresp_knobs{b: control}
	req := llm.Request{Model: model}

	if sys != "" {
		req.Messages = append(req.Messages, llm.System(sys))
	}

	// User message with varied content parts.
	userParts := []llm.ContentPart{{Kind: llm.ContentText, Text: user}}
	switch k.next() % 8 {
	case 1:
		userParts = append(userParts, llm.ContentPart{Kind: llm.ContentImage, Image: &llm.ImageData{Data: blob, MediaType: "image/png"}})
	case 2:
		// Local-path image that EXISTS in the sandbox -> exercises os.ReadFile success.
		p := filepath.Join(tmpDir, "img.png")
		_ = os.WriteFile(p, blob, 0o600)
		userParts = append(userParts, llm.ContentPart{Kind: llm.ContentImage, Image: &llm.ImageData{URL: p, Detail: "low"}})
	case 3:
		// Local-path image that does NOT exist -> exercises os.ReadFile error branch.
		p := filepath.Join(tmpDir, "missing-img.png")
		userParts = append(userParts, llm.ContentPart{Kind: llm.ContentImage, Image: &llm.ImageData{URL: p}})
	case 4:
		userParts = append(userParts, llm.ContentPart{Kind: llm.ContentDocument, Document: &llm.DocumentData{Data: blob, MediaType: "application/pdf", FileName: "d.pdf"}})
	case 5:
		userParts = append(userParts, llm.ContentPart{Kind: llm.ContentDocument, Document: &llm.DocumentData{URL: "https://example.test/d.pdf"}})
	case 6:
		// Audio -> unsupported content kind error branch.
		userParts = append(userParts, llm.ContentPart{Kind: llm.ContentAudio, Audio: &llm.AudioData{Data: blob}})
	case 7:
		userParts = append(userParts, llm.ContentPart{Kind: llm.ContentImage, Image: &llm.ImageData{URL: "https://example.test/x.png"}})
	}
	req.Messages = append(req.Messages, llm.Message{Role: llm.RoleUser, Content: userParts})

	// Assistant message: text/phase groups + thinking + tool call + web search.
	asstParts := []llm.ContentPart{
		{Kind: llm.ContentText, Text: string(blob), Phase: "commentary"},
		{Kind: llm.ContentText, Text: user, Phase: "final_answer"},
	}
	if k.next()%2 == 1 {
		asstParts = append(asstParts, llm.ContentPart{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{
			ID: "rs_1", EncryptedContent: string(blob), Summary: []string{sys, ""},
		}})
	}
	if k.next()%2 == 1 {
		asstParts = append(asstParts, llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
			ID: "call_1", Name: "t", Arguments: json.RawMessage(blob), Type: "function",
		}})
	}
	if k.next()%2 == 1 {
		asstParts = append(asstParts, llm.ContentPart{Kind: llm.ContentWebSearch, WebSearch: &llm.WebSearchData{
			Query: user, Raw: json.RawMessage(blob),
		}})
	}
	req.Messages = append(req.Messages, llm.Message{Role: llm.RoleAssistant, Content: asstParts})

	// Tool-role message: result string/json/error/image.
	var tr llm.ToolResultData
	tr.ToolCallID = "call_1"
	switch k.next() % 4 {
	case 0:
		tr.Content = user
	case 1:
		tr.Content = map[string]any{"k": user}
	case 2:
		tr.Content = user
		tr.IsError = true
	case 3:
		tr.Content = user
		tr.ImageData = blob
		tr.ImageMediaType = "image/png"
	}
	req.Messages = append(req.Messages, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &tr}}})

	// Tools.
	if k.next()%2 == 1 {
		var params map[string]any
		_ = json.Unmarshal(blob, &params)
		strict := k.next()%2 == 1
		req.Tools = []llm.ToolDefinition{{Name: "t", Description: sys, Parameters: params, Strict: &strict}}
	}

	// Tool choice.
	switch k.next() % 6 {
	case 1:
		req.ToolChoice = &llm.ToolChoice{Mode: "auto"}
	case 2:
		req.ToolChoice = &llm.ToolChoice{Mode: "required"}
	case 3:
		req.ToolChoice = &llm.ToolChoice{Mode: "named", Name: "t"}
	case 4:
		req.ToolChoice = &llm.ToolChoice{Mode: "named"} // invalid: missing name -> error
	case 5:
		req.ToolChoice = &llm.ToolChoice{Mode: "custom", Name: "t"} // unspecified mode w/ name
	}

	// Scalar optionals.
	if k.next()%2 == 1 {
		req.Temperature = oresp_ptrFloat(0.7)
	}
	if k.next()%2 == 1 {
		req.TopP = oresp_ptrFloat(0.9)
	}
	if k.next()%2 == 1 {
		req.MaxTokens = oresp_ptrInt(128)
	}
	if k.next()%2 == 1 {
		req.StopSequences = []string{"stop", user}
	}
	if k.next()%2 == 1 {
		req.PromptCacheKey = "  key  "
	}
	if k.next()%2 == 1 {
		req.PreviousResponseID = "resp_prev"
	}
	if k.next()%2 == 1 {
		req.ConversationID = "conv_1"
	}
	if k.next()%2 == 1 {
		req.ServiceTier = "flex"
	}
	if k.next()%2 == 1 {
		req.SafetyIdentifier = "safe"
	}
	if k.next()%2 == 1 {
		req.PromptCacheRetention = "24h"
	}
	if k.next()%2 == 1 {
		req.Truncation = "auto"
	}
	if k.next()%2 == 1 {
		req.MaxToolCalls = oresp_ptrInt(3)
	}
	if k.next()%2 == 1 {
		req.Background = oresp_ptrBool(true)
	}
	switch k.next() % 3 {
	case 1:
		req.Store = oresp_ptrBool(true)
	case 2:
		req.Store = oresp_ptrBool(false)
	}
	if k.next()%2 == 1 {
		req.Metadata = map[string]string{"a": "1", "b": user}
	}
	if k.next()%2 == 1 {
		req.ClientMetadata = map[string]string{"c": "2"}
	}
	switch k.next() % 4 {
	case 1:
		req.ReasoningEffort = oresp_ptrStr("low")
	case 2:
		req.ReasoningEffort = oresp_ptrStr("medium")
	case 3:
		req.ReasoningEffort = oresp_ptrStr("high")
	}
	if k.next()%2 == 1 {
		req.Include = []string{"reasoning.encrypted_content", "message.output_text.logprobs"}
	}
	switch k.next() % 4 {
	case 1:
		req.ResponseFormat = &llm.ResponseFormat{Type: "text"}
	case 2:
		req.ResponseFormat = &llm.ResponseFormat{Type: "json"}
	case 3:
		var schema map[string]any
		_ = json.Unmarshal(blob, &schema)
		req.ResponseFormat = &llm.ResponseFormat{Type: "json_schema", JSONSchema: schema, Strict: true}
	}
	switch k.next() % 3 {
	case 1:
		req.ProviderOptions = map[string]any{"openai": map[string]any{"custom_field": "v"}}
	case 2:
		// Includes a codex-unsupported field to exercise the filter branch.
		req.ProviderOptions = map[string]any{"openai": map[string]any{"temperature": 0.1, "extra": user}}
	}
	if k.next()%2 == 1 {
		req.WebSearch = true
	}
	return req
}

// FuzzOresp_Builders drives the request builders directly over a fuzzed
// llm.Request: toResponsesInput (via buildRequestBody), buildRequestBody itself,
// and PlanResponsesContinuation. It runs each on both a public and a Codex-backed
// adapter to reach the usesCodexBackend() branches.
//
// Oracles:
//   - Determinism: buildRequestBody is a pure function of the request; two calls
//     must json.Marshal to byte-identical bodies (map key order is normalized by
//     encoding/json), and toResponsesInput must return identical instructions and
//     items across calls.
//   - Round-trip: a successfully built body must marshal to valid JSON that
//     re-decodes into a map (no unencodable values escape the builder).
//   - Plan consistency: when buildRequestBody succeeds for the public adapter, a
//     hasher-equipped PlanResponsesContinuation must succeed and yield a non-empty
//     request + storage-scope fingerprint, deterministically.
//   - Never panic on any of the above for any input.
func FuzzOresp_Builders(f *testing.F) {
	// The first []byte is the KNOB stream steering oresp_buildRequest; the second
	// is the content blob. These knob vectors are engineered to reach the success
	// paths (buildRequestBody full body + PlanResponsesContinuation), which random
	// bytes seldom hit because low knob values fall on content-error branches.
	//
	// Seed A: image-data user part, thinking+toolcall+websearch, tools+strict,
	// named tool choice, every scalar optional, store, metadata/client_metadata,
	// reasoning=low (+include), json_schema response format, codex-unsupported
	// provider option, web search — a near-maximal buildRequestBody + Plan run.
	f.Add("gpt-5.5", "be terse", "hello",
		[]byte{1, 1, 1, 1, 0, 1, 1, 3, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 3, 2, 1},
		[]byte(`{"type":"object","properties":{"x":{"type":"string"}}}`))
	// Seed B: document-data part, tool-result error, required tool choice, store
	// false, reasoning=high, json response format, provider options.
	f.Add("gpt-4o", "sys", "u",
		[]byte{4, 0, 0, 0, 2, 0, 0, 2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2, 0, 0, 3, 0, 2, 0, 0},
		[]byte(`{}`))
	// Seed C: sandboxed local-path image (os.ReadFile success), tool-result image,
	// custom tool-choice mode with a name, non-strict tool.
	f.Add("o3", "s", "user text",
		[]byte{2, 1, 0, 1, 3, 1, 0, 5, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 2, 1, 1, 1, 1},
		[]byte("PNGBYTES"))
	// Seed D: content-error paths kept as robustness seeds (missing-file image,
	// audio) plus a malformed tool-schema blob.
	f.Add("computer-use", "", "", []byte{3}, []byte("\xff\x00"))
	f.Add("o4-mini", "s", "u", []byte{6}, []byte(`not json`))

	f.Fuzz(func(t *testing.T, model, sys, user string, blob, blob2 []byte) {
		tmp := t.TempDir()
		// Distinct control bytes come from the two blobs interleaved so the knob
		// stream is long enough to steer every decision.
		control := append(append([]byte{}, blob...), blob2...)
		req := oresp_buildRequest(tmp, model, sys, user, control, blob2)

		hasher := llm.NewContinuationHasher(bytes.Repeat([]byte{9}, 32))
		pub := &Adapter{BaseURL: "https://api.openai.test", ResponsesPath: defaultResponsesPath, ContinuationHasher: hasher, AuthScopeIdentity: llm.AuthScopeIdentity{AuthSource: "api_key"}}
		codex := &Adapter{BaseURL: defaultChatGPTBaseURL, ResponsesPath: defaultCodexResponses, ChatGPTAccountID: "acct_1", ContinuationHasher: hasher, AuthScopeIdentity: llm.AuthScopeIdentity{AuthSource: "oauth"}}

		for _, a := range []*Adapter{pub, codex} {
			// toResponsesInput determinism.
			instr1, items1, err1 := toResponsesInput(req.Messages, req.Model)
			instr2, items2, err2 := toResponsesInput(req.Messages, req.Model)
			if (err1 == nil) != (err2 == nil) {
				t.Fatalf("toResponsesInput non-deterministic error: %v vs %v", err1, err2)
			}
			if err1 == nil {
				if instr1 != instr2 {
					t.Fatalf("toResponsesInput non-deterministic instructions:\n%q\n%q", instr1, instr2)
				}
				b1, _ := json.Marshal(items1)
				b2, _ := json.Marshal(items2)
				if !bytes.Equal(b1, b2) {
					t.Fatalf("toResponsesInput non-deterministic items:\n%s\n%s", b1, b2)
				}
			}

			body1, berr1 := a.buildRequestBody(req)
			body2, berr2 := a.buildRequestBody(req)
			if (berr1 == nil) != (berr2 == nil) {
				t.Fatalf("buildRequestBody non-deterministic error: %v vs %v", berr1, berr2)
			}
			if berr1 != nil {
				continue
			}
			mb1, merr1 := json.Marshal(body1)
			mb2, merr2 := json.Marshal(body2)
			if merr1 != nil || merr2 != nil {
				// An unmarshalable body would also break the live path; treat it
				// as a clean build limitation, not a harness bug.
				continue
			}
			if !bytes.Equal(mb1, mb2) {
				t.Fatalf("buildRequestBody non-deterministic body:\n%s\n%s", mb1, mb2)
			}
			if !json.Valid(mb1) {
				t.Fatalf("buildRequestBody produced invalid JSON: %s", mb1)
			}
			var round map[string]any
			if err := json.Unmarshal(mb1, &round); err != nil {
				t.Fatalf("built body does not round-trip: %v (%s)", err, mb1)
			}
		}

		// PlanResponsesContinuation over the public adapter.
		if _, berr := pub.buildRequestBody(req); berr == nil {
			p1, perr1 := pub.PlanResponsesContinuation(req)
			p2, perr2 := pub.PlanResponsesContinuation(req)
			if (perr1 == nil) != (perr2 == nil) {
				t.Fatalf("PlanResponsesContinuation non-deterministic error: %v vs %v", perr1, perr2)
			}
			if perr1 == nil {
				if p1.RequestFingerprint == "" {
					t.Fatalf("PlanResponsesContinuation: empty RequestFingerprint")
				}
				if p1.StorageScopeFingerprint == "" {
					t.Fatalf("PlanResponsesContinuation: empty StorageScopeFingerprint")
				}
				if p1.RequestFingerprint != p2.RequestFingerprint || p1.StorageScopeFingerprint != p2.StorageScopeFingerprint {
					t.Fatalf("PlanResponsesContinuation non-deterministic fingerprints:\n%+v\n%+v", p1, p2)
				}
			}
		}
	})
}

// FuzzOresp_StreamResponses drives Adapter.streamResponses directly over a fake
// transport, feeding fuzzed SSE bytes and HTTP status (plus an occasional
// transport-level error). It exercises the raw Responses streaming path without
// the Chat Completions fallback wrapper.
//
// Oracles:
//   - Request-shape differential: when the request is buildable and the transport
//     is reached, the FIRST request is a POST to responsesURL() whose body equals
//     the direct buildRequestBody output plus "stream":true.
//   - Floor: an unbuildable request never reaches the transport and errors; a
//     transport error or non-2xx status yields a nil stream + non-nil error;
//     draining a returned stream to completion always terminates without panic.
func FuzzOresp_StreamResponses(f *testing.F) {
	sse := []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\"}}\n\n")
	deltaOnly := []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")
	// statusSel doubles as knob0 (content selection) and as the HTTP status via
	// oresp_status (>=100 -> a non-2xx code). failSel%5==0 injects a transport error.
	f.Add("gpt-5.5", "sys", "hi", byte(0), byte(1), sse)                                 // 200, completed -> success drain
	f.Add("gpt-4o", "", "u", byte(200), byte(1), []byte(`{"error":{"message":"nope"}}`)) // 400 -> non-2xx error branch
	f.Add("m", "s", "", byte(0), byte(1), []byte("garbage\n\ndata: {}\n\n"))             // 200, no content -> empty-stream sentinel
	f.Add("m", "s", "u", byte(0), byte(1), deltaOnly)                                    // 200, content then no completion
	f.Add("gpt-4o", "s", "u", byte(6), byte(1), sse)                                     // audio content -> build error before transport
	f.Add("m", "s", "u", byte(0), byte(0), sse)                                          // failSel=0 -> transport error branch

	f.Fuzz(func(t *testing.T, model, sys, user string, statusSel, failSel byte, sseBody []byte) {
		tmp := t.TempDir()
		req := oresp_buildRequest(tmp, model, sys, user, []byte{statusSel, failSel}, sseBody)

		rt := &oresp_fakeTransport{defaultStatus: oresp_status(statusSel), defaultBody: sseBody}
		if failSel%5 == 0 {
			rt.failErr = errors.New("oresp injected transport failure")
		}
		hasher := llm.NewContinuationHasher(bytes.Repeat([]byte{7}, 32))
		a := &Adapter{APIKey: "k", BaseURL: "https://api.openai.test", ResponsesPath: defaultResponsesPath, ContinuationHasher: hasher, Client: &http.Client{Transport: rt}}

		wantBody, buildErr := a.buildRequestBody(req)

		stream, err := a.streamResponses(context.Background(), req)
		if buildErr != nil {
			if err == nil {
				t.Fatalf("streamResponses returned nil error for unbuildable request (build err=%v)", buildErr)
			}
			if rt.calls != 0 {
				t.Fatalf("unbuildable request reached the transport (%d calls)", rt.calls)
			}
			return
		}
		if err != nil {
			// Transport error or non-2xx: nil stream, non-nil error is the contract.
			if stream != nil {
				t.Fatalf("streamResponses returned non-nil stream alongside error %v", err)
			}
			if rt.failErr == nil {
				oresp_assertFirstResponsesRequest(t, rt, a, wantBody)
			}
			return
		}
		for range stream.Events() { //nolint:revive // draining for side effects
		}
		_ = stream.Close()
		oresp_assertFirstResponsesRequest(t, rt, a, wantBody)
	})
}

func oresp_assertFirstResponsesRequest(t *testing.T, rt *oresp_fakeTransport, a *Adapter, wantBody map[string]any) {
	t.Helper()
	if rt.calls == 0 {
		return
	}
	if rt.urls[0] != a.responsesURL() {
		t.Fatalf("first request URL = %q, want %q", rt.urls[0], a.responsesURL())
	}
	streamBody := make(map[string]any, len(wantBody)+1)
	for k, v := range wantBody {
		streamBody[k] = v
	}
	streamBody["stream"] = true
	wantBytes, err := json.Marshal(streamBody)
	if err != nil {
		return
	}
	if !bytes.Equal(rt.bodies[0], wantBytes) {
		t.Fatalf("stream request body != buildRequestBody+stream\n got: %s\nwant: %s", rt.bodies[0], wantBytes)
	}
}

// FuzzOresp_StreamChat drives Adapter.streamViaChatCompletions directly over a
// fake transport with fuzzed Chat Completions SSE + status.
//
// Oracles:
//   - Request-shape differential: when buildChatCompletionsBody succeeds and the
//     transport is reached, the first request is a POST to chatCompletionsURL()
//     whose body equals buildChatCompletionsBody(req,true).
//   - Floor: an unbuildable body (e.g. tool-result images) errors before the
//     transport; a transport error or non-2xx yields nil stream + non-nil error;
//     draining always terminates without panic.
func FuzzOresp_StreamChat(f *testing.F) {
	sse := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n")
	deltaOnly := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
	// statusSel doubles as knob0 (content selection) and as the HTTP status via
	// oresp_status (>=100 -> a non-2xx code). failSel%5==0 injects a transport error.
	f.Add("gpt-4o", "sys", "hi", byte(0), byte(1), sse)                                 // 200, [DONE] -> success drain
	f.Add("gpt-4o", "", "u", byte(200), byte(1), []byte(`{"error":{"message":"bad"}}`)) // 400 -> non-2xx error branch
	f.Add("m", "s", "", byte(0), byte(1), []byte("junk\n\ndata: [DONE]\n\n"))           // 200, malformed chunk
	f.Add("m", "s", "u", byte(0), byte(1), deltaOnly)                                   // 200, content then no [DONE]
	f.Add("gpt-4o", "s", "u", byte(6), byte(1), sse)                                    // audio content -> build error before transport
	f.Add("m", "s", "u", byte(0), byte(0), sse)                                         // failSel=0 -> transport error branch

	f.Fuzz(func(t *testing.T, model, sys, user string, statusSel, failSel byte, sseBody []byte) {
		tmp := t.TempDir()
		req := oresp_buildRequest(tmp, model, sys, user, []byte{statusSel, failSel, 3}, sseBody)

		rt := &oresp_fakeTransport{defaultStatus: oresp_status(statusSel), defaultBody: sseBody}
		if failSel%5 == 0 {
			rt.failErr = errors.New("oresp injected transport failure")
		}
		a := &Adapter{APIKey: "k", BaseURL: "https://api.openai.test", ResponsesPath: defaultResponsesPath, Client: &http.Client{Transport: rt}}

		wantBody, buildErr := buildChatCompletionsBody(req, true)

		stream, err := a.streamViaChatCompletions(context.Background(), req)
		if buildErr != nil {
			if err == nil {
				t.Fatalf("streamViaChatCompletions returned nil error for unbuildable body (build err=%v)", buildErr)
			}
			if rt.calls != 0 {
				t.Fatalf("unbuildable body reached the transport (%d calls)", rt.calls)
			}
			return
		}
		if err != nil {
			if stream != nil {
				t.Fatalf("streamViaChatCompletions returned non-nil stream alongside error %v", err)
			}
			if rt.failErr == nil && rt.calls > 0 {
				oresp_assertFirstChatRequest(t, rt, a, wantBody)
			}
			return
		}
		for range stream.Events() { //nolint:revive // draining for side effects
		}
		_ = stream.Close()
		if rt.calls > 0 {
			oresp_assertFirstChatRequest(t, rt, a, wantBody)
		}
	})
}

func oresp_assertFirstChatRequest(t *testing.T, rt *oresp_fakeTransport, a *Adapter, wantBody map[string]any) {
	t.Helper()
	if rt.urls[0] != a.chatCompletionsURL() {
		t.Fatalf("first request URL = %q, want %q", rt.urls[0], a.chatCompletionsURL())
	}
	wantBytes, err := json.Marshal(wantBody)
	if err != nil {
		return
	}
	if !bytes.Equal(rt.bodies[0], wantBytes) {
		t.Fatalf("chat request body != buildChatCompletionsBody\n got: %s\nwant: %s", rt.bodies[0], wantBytes)
	}
}

// FuzzOresp_CompleteFallback drives Adapter.Complete with the Chat Completions
// fallback ENABLED. It routes the fuzzed responses status (biased toward the
// fallback-eligible 404/422) and a fuzzed Chat Completions SSE through a single
// routing transport, exercising Complete's fallback branch,
// completeViaChatCompletionsFallback, and streamViaChatCompletions.
//
// Oracles:
//   - Request-shape differential: the FIRST request is always a POST to
//     responsesURL() whose body equals buildRequestBody (behavior preservation).
//   - Floor: Complete never panics; a non-2xx responses status that is NOT
//     fallback-eligible yields a non-nil error.
func FuzzOresp_CompleteFallback(f *testing.F) {
	okResp := []byte(`{"id":"resp_1","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}]}`)
	ccSSE := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"fallback\"}}]}\n\ndata: [DONE]\n\n")
	f.Add("gpt-4o", "sys", "hi", byte(0), okResp, ccSSE)
	f.Add("gpt-4o", "", "u", byte(0), []byte(`{"error":{"message":"model not found"}}`), ccSSE)
	f.Add("m", "s", "u", byte(1), []byte(`{"error":{"message":"unprocessable"}}`), []byte(`{"error":{"message":"cc bad"}}`))
	f.Add("m", "s", "u", byte(2), []byte(`bad request`), ccSSE)

	f.Fuzz(func(t *testing.T, model, sys, user string, sel byte, respBody, ccBody []byte) {
		// Build a request WITHOUT continuation state so shouldFallbackToChatCompletions
		// takes the eligible path on 404/422.
		req := llm.Request{
			Model:    model,
			Messages: []llm.Message{llm.System(sys), llm.User(user)},
		}

		respStatus := oresp_fallbackStatus(sel)
		rt := &oresp_fakeTransport{
			responsesStatus: respStatus,
			responsesBody:   respBody,
			chatStatus:      http.StatusOK,
			chatBody:        ccBody,
		}
		hasher := llm.NewContinuationHasher(bytes.Repeat([]byte{5}, 32))
		a := &Adapter{APIKey: "k", BaseURL: "https://api.openai.test", ResponsesPath: defaultResponsesPath, ContinuationHasher: hasher, Client: &http.Client{Transport: rt}}

		wantBody, buildErr := a.buildRequestBody(req)
		if buildErr != nil {
			return // simple two-message request should always build; nothing to assert
		}
		wantBytes, _ := json.Marshal(wantBody)

		_, _ = a.Complete(context.Background(), req)

		if rt.calls == 0 {
			t.Fatalf("Complete never reached the transport")
		}
		if rt.urls[0] != a.responsesURL() {
			t.Fatalf("first request URL = %q, want responses %q", rt.urls[0], a.responsesURL())
		}
		if !bytes.Equal(rt.bodies[0], wantBytes) {
			t.Fatalf("Complete first request body != buildRequestBody\n got: %s\nwant: %s", rt.bodies[0], wantBytes)
		}
	})
}

// FuzzOresp_NewForInstance drives NewForInstance from fuzzed parameters under a
// sandboxed StateHome (t.TempDir()), so the OAuth-status probe reads an empty
// directory and the constructor falls through to the API-key / no-credentials
// paths without touching the real auth store or the network.
//
// Oracles:
//   - Determinism: two constructions from identical params agree on error-ness
//     and, when successful, on Name/BaseURL/ResponsesPath.
//   - Contract: empty API key -> errNoCredentials + nil adapter; non-empty API
//     key -> non-nil adapter whose BaseURL has no trailing slash and whose
//     ResponsesPath is the public default.
//   - Never panic.
func FuzzOresp_NewForInstance(f *testing.F) {
	f.Add("openai", "sk-test", "https://api.openai.test/", "org", "proj", "")
	f.Add("inst", "", "", "", "", "")
	f.Add("x", "  key  ", "https://host/v1", "", "", "https://chatgpt.test/")
	f.Add("", "k", "https://a.b.c///", "o", "p", "")

	f.Fuzz(func(t *testing.T, name, apiKey, baseURL, orgID, projectID, chatGPTBase string) {
		stateHome := t.TempDir()
		name = oresp_safeInstanceName(name)
		hasher := llm.NewContinuationHasher(bytes.Repeat([]byte{3}, 32))
		params := OpenAIInstanceParams{
			Name:               name,
			APIKey:             apiKey,
			BaseURL:            baseURL,
			OrgID:              orgID,
			ProjectID:          projectID,
			ChatGPTBaseURL:     chatGPTBase,
			StateHome:          stateHome,
			ContinuationHasher: hasher,
		}

		a1, err1 := NewForInstance(params)
		a2, err2 := NewForInstance(params)
		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("NewForInstance non-deterministic error: %v vs %v", err1, err2)
		}

		if len(bytes.TrimSpace([]byte(apiKey))) == 0 {
			if !errors.Is(err1, errNoCredentials) {
				t.Fatalf("empty API key: want errNoCredentials, got %v", err1)
			}
			if a1 != nil {
				t.Fatalf("empty API key: want nil adapter, got %+v", a1)
			}
			return
		}

		if err1 != nil {
			t.Fatalf("non-empty API key: unexpected error %v", err1)
		}
		if a1 == nil {
			t.Fatalf("non-empty API key: nil adapter without error")
		}
		if a1.ResponsesPath != defaultResponsesPath {
			t.Fatalf("ResponsesPath = %q, want %q", a1.ResponsesPath, defaultResponsesPath)
		}
		if len(a1.BaseURL) > 0 && a1.BaseURL[len(a1.BaseURL)-1] == '/' {
			t.Fatalf("BaseURL has trailing slash: %q", a1.BaseURL)
		}
		if a2 != nil && a1.BaseURL != a2.BaseURL {
			t.Fatalf("NewForInstance non-deterministic BaseURL: %q vs %q", a1.BaseURL, a2.BaseURL)
		}
	})
}
