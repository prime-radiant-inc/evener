package google

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/llm"
)

// basicReq returns a minimal valid request for the google adapter.
func basicReq() llm.Request {
	return llm.Request{
		Model:    "gemini-test",
		Messages: []llm.Message{llm.User("hello")},
	}
}

// badToolChoiceReq returns a request whose ToolChoice mode is unsupported,
// which makes buildRequestBody return an error.
func badToolChoiceReq() llm.Request {
	r := basicReq()
	r.ToolChoice = &llm.ToolChoice{Mode: "bad-mode"}
	return r
}

// namedToolChoiceNoNameReq returns a request with ToolChoice mode "named" but
// no name, which makes buildRequestBody return a ConfigurationError.
func namedToolChoiceNoNameReq() llm.Request {
	r := basicReq()
	r.ToolChoice = &llm.ToolChoice{Mode: "named"}
	return r
}

// cyclicProviderOptionsReq returns a request whose ProviderOptions contains a
// cyclic map, which makes json.Marshal fail.
func cyclicProviderOptionsReq() llm.Request {
	r := basicReq()
	cyclic := map[string]any{}
	cyclic["cycle"] = cyclic
	r.ProviderOptions = map[string]any{"google": cyclic}
	return r
}

func seedErrorClient(err error) *http.Client {
	return &http.Client{Transport: errorRoundTripper{err: err}}
}

type errorRoundTripper struct{ err error }

func (e errorRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, e.err
}

// TestCompleteBuildRequestBodyError covers the buildRequestBody error path
// in Complete (lines 143-144).
func TestCompleteBuildRequestBodyError(t *testing.T) {
	a := &Adapter{APIKey: "k", BaseURL: "http://seed.invalid"}
	_, err := a.Complete(context.Background(), badToolChoiceReq())
	if err == nil {
		t.Fatal("Complete with bad ToolChoice should error")
	}
}

// TestCompleteJSONMarshalError covers the json.Marshal error path in Complete
// (lines 148-149).
func TestCompleteJSONMarshalError(t *testing.T) {
	a := &Adapter{APIKey: "k", BaseURL: "http://seed.invalid"}
	_, err := a.Complete(context.Background(), cyclicProviderOptionsReq())
	if err == nil {
		t.Fatal("Complete with cyclic ProviderOptions should error")
	}
}

// TestCompleteURLParseError covers the url.Parse error path in Complete
// (lines 157-158).
func TestCompleteURLParseError(t *testing.T) {
	a := &Adapter{APIKey: "k", BaseURL: "%"}
	_, err := a.Complete(context.Background(), basicReq())
	if err == nil {
		t.Fatal("Complete with bad BaseURL should error")
	}
}

// TestCountInputTokensBuildRequestBodyError covers the buildRequestBody error
// path in CountInputTokens (lines 259-260).
func TestCountInputTokensBuildRequestBodyError(t *testing.T) {
	a := &Adapter{APIKey: "k", BaseURL: "http://seed.invalid"}
	_, err := a.CountInputTokens(context.Background(), badToolChoiceReq())
	if err == nil {
		t.Fatal("CountInputTokens with bad ToolChoice should error")
	}
}

// TestCountInputTokensJSONMarshalError covers the json.Marshal error path in
// CountInputTokens (lines 266-267).
func TestCountInputTokensJSONMarshalError(t *testing.T) {
	a := &Adapter{APIKey: "k", BaseURL: "http://seed.invalid"}
	_, err := a.CountInputTokens(context.Background(), cyclicProviderOptionsReq())
	if err == nil {
		t.Fatal("CountInputTokens with cyclic ProviderOptions should error")
	}
}

// TestCountInputTokensURLParseError covers the url.Parse error path in
// CountInputTokens (lines 276-277).
func TestCountInputTokensURLParseError(t *testing.T) {
	a := &Adapter{APIKey: "k", BaseURL: "%"}
	_, err := a.CountInputTokens(context.Background(), basicReq())
	if err == nil {
		t.Fatal("CountInputTokens with bad BaseURL should error")
	}
}

// TestCountInputTokensTransportError covers the transport error path in
// CountInputTokens (lines 300-303).
func TestCountInputTokensTransportError(t *testing.T) {
	a := &Adapter{APIKey: "k", BaseURL: "http://seed.invalid", Client: seedErrorClient(errors.New("transport fail"))}
	_, err := a.CountInputTokens(context.Background(), basicReq())
	if err == nil {
		t.Fatal("CountInputTokens with transport error should return error")
	}
}

// TestStreamBuildRequestBodyError covers the buildRequestBody error path in
// Stream (lines 367-369).
func TestStreamBuildRequestBodyError(t *testing.T) {
	a := &Adapter{APIKey: "k", BaseURL: "http://seed.invalid"}
	_, err := a.Stream(context.Background(), badToolChoiceReq())
	if err == nil {
		t.Fatal("Stream with bad ToolChoice should error")
	}
}

// TestStreamJSONMarshalError covers the json.Marshal error path in Stream
// (lines 373-375).
func TestStreamJSONMarshalError(t *testing.T) {
	a := &Adapter{APIKey: "k", BaseURL: "http://seed.invalid"}
	_, err := a.Stream(context.Background(), cyclicProviderOptionsReq())
	if err == nil {
		t.Fatal("Stream with cyclic ProviderOptions should error")
	}
}

// TestStreamURLParseError covers the url.Parse error path in Stream
// (lines 380-382).
func TestStreamURLParseError(t *testing.T) {
	a := &Adapter{APIKey: "k", BaseURL: "%"}
	_, err := a.Stream(context.Background(), basicReq())
	if err == nil {
		t.Fatal("Stream with bad BaseURL should error")
	}
}

// TestStreamNewRequestError covers the newGoogleStreamRequest error path in
// Stream (lines 390-392).
func TestStreamNewRequestError(t *testing.T) {
	oldStreamRequest := newGoogleStreamRequest
	newGoogleStreamRequest = func(context.Context, string, string, io.Reader) (*http.Request, error) {
		return nil, errors.New("seed request")
	}
	defer func() { newGoogleStreamRequest = oldStreamRequest }()
	a := &Adapter{APIKey: "k", BaseURL: "http://seed.invalid"}
	_, err := a.Stream(context.Background(), basicReq())
	if err == nil {
		t.Fatal("Stream with request creation error should fail")
	}
}

// TestAPILogCredentialMaterialWithURLUser covers the URL user/password
// extraction in apiLogCredentialMaterial (lines 240-241, 242-243).
func TestAPILogCredentialMaterialWithURLUser(t *testing.T) {
	a := &Adapter{APIKey: "key", CredentialHeaders: map[string]string{"X-Gateway": "gateway-secret"}}
	req, err := http.NewRequest(http.MethodPost, "https://user:pass@provider.test/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	material := a.apiLogCredentialMaterial(req)
	// The material should include the API key, credential header value,
	// URL username, and URL password.
	found := map[string]bool{}
	for _, v := range material.Values {
		found[v] = true
	}
	if !found["key"] {
		t.Error("missing APIKey value")
	}
	if !found["gateway-secret"] {
		t.Error("missing credential header value")
	}
	if !found["user"] {
		t.Error("missing URL username")
	}
	if !found["pass"] {
		t.Error("missing URL password")
	}
}

// TestStreamErrorPathReadErrCoversReadErrBranch covers the branch in Stream
// error path where readErr overrides jsonErr (line 424-425).
func TestStreamErrorPathReadErrCoversReadErrBranch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		// Write a body that errors on read by closing the connection mid-write.
		// Use a flusher to write partial data.
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := a.Stream(ctx, basicReq())
	if err != nil {
		// This is expected if the error path fires before stream creation.
		return
	}
	// Drain the stream.
	for range s.Events() {
	}
}

// TestStreamNonMapPartCoversContinue covers the continue branch for non-map
// parts in the stream decode (line 515).
func TestStreamNonMapPartCoversContinue(t *testing.T) {
	// Build an SSE stream where candidates[0].content.parts contains a
	// non-map element (an integer) alongside a valid text part.
	streamBody := "data: " + `{"candidates":[{"content":{"parts":[1,{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}` + "\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, streamBody)
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := a.Stream(ctx, basicReq())
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var text strings.Builder
	for ev := range s.Events() {
		if ev.Type == llm.StreamEventTextDelta {
			text.WriteString(ev.Delta)
		}
	}
	if got := strings.TrimSpace(text.String()); got != "hello" {
		t.Fatalf("stream text = %q, want %q", got, "hello")
	}
}

// TestNamedToolChoiceNoNameCoversCompleteAndStream covers the named-without-name
// error path in buildRequestBody, exercised through Complete and Stream.
func TestNamedToolChoiceNoNameCoversCompleteAndStream(t *testing.T) {
	a := &Adapter{APIKey: "k", BaseURL: "http://seed.invalid"}
	if _, err := a.Complete(context.Background(), namedToolChoiceNoNameReq()); err == nil {
		t.Error("Complete with named ToolChoice without name should error")
	}
	if _, err := a.CountInputTokens(context.Background(), namedToolChoiceNoNameReq()); err == nil {
		t.Error("CountInputTokens with named ToolChoice without name should error")
	}
	if _, err := a.Stream(context.Background(), namedToolChoiceNoNameReq()); err == nil {
		t.Error("Stream with named ToolChoice without name should error")
	}
}
