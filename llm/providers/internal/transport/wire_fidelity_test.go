package transport

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"testing"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/apilog"
)

func gzipBytes(t *testing.T, body []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write(body); err != nil {
		t.Fatalf("gzip body: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	return compressed.Bytes()
}

func TestDoWithAPIAttemptsRecordsAdapterVisibleGzipBody(t *testing.T) {
	providerBody := []byte(`{"provider":"decoded by net/http"}`)
	compressed := gzipBytes(t, providerBody)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(compressed)
	}))
	t.Cleanup(server.Close)

	sink := &responseAssociationSink{}
	request, err := http.NewRequestWithContext(attemptContext("ag_gzip_visible", sink), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, attempt, err := DoWithAPIAttempts(context.Background(), server.Client(), request, attemptMeta)
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}
	adapterBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("adapter read: %v", err)
	}
	if !bytes.Equal(adapterBody, providerBody) {
		t.Fatalf("adapter body = %q, want decompressed %q", adapterBody, providerBody)
	}
	reporter := response.Body.(bodyObservationReporter)
	if !reporter.observedExactly() {
		t.Fatal("decompressed body is inexact after EOF")
	}
	attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)
	recorded, err := apilog.DecodeBody(onlyAttempt(t, sink).Response.Body)
	if err != nil {
		t.Fatalf("decode recorded body: %v", err)
	}
	if !bytes.Equal(recorded, providerBody) {
		t.Fatalf("recorded body = %q, want adapter-visible %q", recorded, providerBody)
	}
}

type declaredCompressionTransport struct {
	body []byte
}

func (t declaredCompressionTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Encoding": []string{"gzip"}},
		Body:       io.NopCloser(bytes.NewReader(t.body)),
	}, nil
}

func (declaredCompressionTransport) APILogTransportUsesStandardCompression() bool {
	return true
}

func TestDoWithAPIAttemptsDoesNotEmulateOrUnwrapGzipTransport(t *testing.T) {
	compressed := gzipBytes(t, []byte("provider body"))
	client := &http.Client{Transport: declaredCompressionTransport{body: compressed}}
	sink := &responseAssociationSink{}
	request, err := http.NewRequestWithContext(attemptContext("ag_no_gzip_emulation", sink), http.MethodGet, "https://provider.test/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, attemptMeta)
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}
	adapterBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("adapter read: %v", err)
	}
	if !bytes.Equal(adapterBody, compressed) {
		t.Fatalf("adapter body changed by instrumentation: got %x want %x", adapterBody, compressed)
	}
	attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)
	recorded, err := apilog.DecodeBody(onlyAttempt(t, sink).Response.Body)
	if err != nil {
		t.Fatalf("decode recorded body: %v", err)
	}
	if !bytes.Equal(recorded, compressed) {
		t.Fatalf("recorded body changed by instrumentation: got %x want %x", recorded, compressed)
	}
}

func TestDoWithAPIAttemptsHTTPTraceEventsDoNotSplitRoundTrip(t *testing.T) {
	traceEvents := 0
	trace := &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) {
		traceEvents++
	}}
	ctx := httptrace.WithClientTrace(context.Background(), trace)
	sink := &responseAssociationSink{}
	ctx = llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(ctx, llm.NewAPIAttemptGroup("ag_httptrace_single_attempt")),
		sink,
	)
	client := &http.Client{Transport: responseAssociationRoundTripper(func(request *http.Request) (*http.Response, error) {
		observedTrace := httptrace.ContextClientTrace(request.Context())
		if observedTrace == nil {
			t.Fatal("caller trace was removed")
		}
		if observedTrace.WroteRequest != nil {
			observedTrace.WroteRequest(httptrace.WroteRequestInfo{})
			observedTrace.WroteRequest(httptrace.WroteRequestInfo{})
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("response")),
		}, nil
	})}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://provider.test/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, attemptMeta)
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatalf("read response: %v", err)
	}
	attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)
	if got := sink.count(); got != 1 {
		t.Fatalf("attempts after one RoundTrip with trace events = %d, want 1", got)
	}
	if traceEvents != 2 {
		t.Fatalf("caller trace events = %d, want 2", traceEvents)
	}
}
