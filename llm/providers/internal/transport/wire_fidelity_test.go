package transport

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/llm"
	"primeradiant.com/serf/llm/apilog"
)

func TestDoWithAPIAttemptsRecordsRawGzipResponseBeforeStandardTransportDecoding(t *testing.T) {
	providerBody := []byte(`{"id":"response-1","text":"provider payload"}`)
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write(providerBody); err != nil {
		t.Fatalf("gzip provider body: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	wantWireBody := append([]byte(nil), compressed.Bytes()...)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Accept-Encoding"); got != "gzip" {
			t.Errorf("Accept-Encoding = %q, want standard transport gzip", got)
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(wantWireBody)))
		_, _ = w.Write(wantWireBody)
	}))
	t.Cleanup(server.Close)

	sink := &responseAssociationSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_gzip_wire_fidelity")),
		sink,
	)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(`{"prompt":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	response, attempt, err := DoWithAPIAttempts(context.Background(), server.Client(), request, func(*http.Request, []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{ProviderInstance: "test"}
	})
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}
	adapterBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read adapter response: %v", err)
	}
	if !bytes.Equal(adapterBody, providerBody) {
		t.Fatalf("adapter response = %q, want decoded provider body %q", adapterBody, providerBody)
	}
	if !response.Uncompressed || response.ContentLength != -1 || response.Header.Get("Content-Encoding") != "" {
		t.Fatalf("adapter response compression semantics = uncompressed:%v length:%d encoding:%q", response.Uncompressed, response.ContentLength, response.Header.Get("Content-Encoding"))
	}
	attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)

	sink.mu.Lock()
	if len(sink.attempts) != 1 {
		sink.mu.Unlock()
		t.Fatalf("canonical attempts = %d, want 1", len(sink.attempts))
	}
	record := sink.attempts[0]
	sink.mu.Unlock()
	recordedBody, err := apilog.DecodeBody(record.Response.Body)
	if err != nil {
		t.Fatalf("decode canonical response: %v", err)
	}
	if !bytes.Equal(recordedBody, wantWireBody) {
		t.Fatalf("canonical response = %x, want raw provider gzip bytes %x", recordedBody, wantWireBody)
	}
}

func TestDoWithAPIAttemptsRecordsRawGzipResponseWithResponseHeaderTimeout(t *testing.T) {
	providerBody := []byte(`{"id":"response-with-timeout"}`)
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write(providerBody); err != nil {
		t.Fatalf("gzip provider body: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	wantWireBody := append([]byte(nil), compressed.Bytes()...)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(wantWireBody)
	}))
	t.Cleanup(server.Close)

	sink := &responseAssociationSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_gzip_timeout_wire_fidelity")),
		sink,
	)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := llm.ClientWithAdapterTimeout(server.Client(), &llm.AdapterTimeout{Request: time.Second})
	response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, func(*http.Request, []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{ProviderInstance: "test"}
	})
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}
	adapterBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read adapter response: %v", err)
	}
	if !bytes.Equal(adapterBody, providerBody) {
		t.Fatalf("adapter response = %q, want decoded provider body %q", adapterBody, providerBody)
	}
	attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)
	recordedBody, err := apilog.DecodeBody(onlyAttempt(t, sink).Response.Body)
	if err != nil {
		t.Fatalf("decode canonical response: %v", err)
	}
	if !bytes.Equal(recordedBody, wantWireBody) {
		t.Fatalf("canonical response = %x, want raw provider gzip bytes %x", recordedBody, wantWireBody)
	}
}

func TestDoWithAPIAttemptsRecordsStandardTransportWrittenRequestMetadata(t *testing.T) {
	t.Run("calculated content length and host override", func(t *testing.T) {
		var received struct {
			host            string
			userAgent       string
			acceptEncoding  string
			contentLength   int64
			visible         []string
			credentialValue string
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			received.host = request.Host
			received.userAgent = request.UserAgent()
			received.acceptEncoding = request.Header.Get("Accept-Encoding")
			received.contentLength = request.ContentLength
			received.visible = append([]string(nil), request.Header.Values("X-Visible")...)
			received.credentialValue = request.Header.Get("Authorization")
			_, _ = io.Copy(io.Discard, request.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{}`)
		}))
		t.Cleanup(server.Close)

		const credential = "wire-credential-sentinel"
		sink := &responseAssociationSink{}
		ctx := llm.WithAPIAttemptSink(
			llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_request_metadata_length")),
			sink,
		)
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader("request-body"))
		if err != nil {
			t.Fatal(err)
		}
		request.Host = "provider-override.test"
		request.Header["X-Visible"] = []string{"first", "second"}
		request.Header.Set("Authorization", "Bearer "+credential)
		response, attempt, err := DoWithAPIAttempts(context.Background(), server.Client(), request, func(*http.Request, []byte) llm.APIAttemptMeta {
			return llm.APIAttemptMeta{
				ProviderInstance:   "test",
				CredentialMaterial: llm.NewAPILogCredentialMaterial([]string{"Authorization"}, nil, credential),
			}
		})
		if err != nil {
			t.Fatalf("DoWithAPIAttempts: %v", err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)

		record := onlyAttempt(t, sink)
		assertHeaderValues(t, record.Request.Headers, "Host", []string{received.host})
		assertHeaderValues(t, record.Request.Headers, "User-Agent", []string{received.userAgent})
		assertHeaderValues(t, record.Request.Headers, "Accept-Encoding", []string{received.acceptEncoding})
		assertHeaderValues(t, record.Request.Headers, "Content-Length", []string{strconv.FormatInt(received.contentLength, 10)})
		assertHeaderValues(t, record.Request.Headers, "X-Visible", received.visible)
		if received.credentialValue == "" {
			t.Fatal("server did not receive configured credential header")
		}
		if _, present := record.Request.Headers["Authorization"]; present {
			t.Fatalf("canonical request retained credential header: %#v", record.Request.Headers)
		}
	})

	t.Run("chunked transfer and trailers", func(t *testing.T) {
		var received struct {
			transferEncoding []string
			trailer          []string
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			received.transferEncoding = append([]string(nil), request.TransferEncoding...)
			_, _ = io.Copy(io.Discard, request.Body)
			received.trailer = append([]string(nil), request.Trailer.Values("X-Wire-Trailer")...)
			_, _ = io.WriteString(w, `{}`)
		}))
		t.Cleanup(server.Close)

		sink := &responseAssociationSink{}
		ctx := llm.WithAPIAttemptSink(
			llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_request_metadata_trailer")),
			sink,
		)
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, io.NopCloser(strings.NewReader("streamed-body")))
		if err != nil {
			t.Fatal(err)
		}
		request.ContentLength = -1
		request.Trailer = http.Header{"X-Wire-Trailer": []string{"final-value"}}
		response, attempt, err := DoWithAPIAttempts(context.Background(), server.Client(), request, func(*http.Request, []byte) llm.APIAttemptMeta {
			return llm.APIAttemptMeta{ProviderInstance: "test"}
		})
		if err != nil {
			t.Fatalf("DoWithAPIAttempts: %v", err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)

		record := onlyAttempt(t, sink)
		assertHeaderValues(t, record.Request.Headers, "Transfer-Encoding", received.transferEncoding)
		assertHeaderValues(t, record.Request.Headers, "Trailer", []string{"X-Wire-Trailer"})
		assertHeaderValues(t, record.Request.Headers, "X-Wire-Trailer", received.trailer)
	})
}

func TestDoWithAPIAttemptsPreservesStandardTransportConnectionReuse(t *testing.T) {
	var (
		mu          sync.Mutex
		remoteAddrs []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		mu.Lock()
		remoteAddrs = append(remoteAddrs, request.RemoteAddr)
		mu.Unlock()
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(server.Close)
	client := server.Client()
	sink := &responseAssociationSink{}

	for i := 0; i < 2; i++ {
		ctx := llm.WithAPIAttemptSink(
			llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_connection_reuse_"+strconv.Itoa(i))),
			sink,
		)
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, func(*http.Request, []byte) llm.APIAttemptMeta {
			return llm.APIAttemptMeta{ProviderInstance: "test"}
		})
		if err != nil {
			t.Fatalf("DoWithAPIAttempts call %d: %v", i+1, err)
		}
		if _, err := io.Copy(io.Discard, response.Body); err != nil {
			t.Fatalf("drain response %d: %v", i+1, err)
		}
		attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(remoteAddrs) != 2 {
		t.Fatalf("server requests = %d, want 2", len(remoteAddrs))
	}
	if remoteAddrs[0] != remoteAddrs[1] {
		t.Fatalf("standard transport connection changed across canonical attempts: %q then %q", remoteAddrs[0], remoteAddrs[1])
	}
}

func onlyAttempt(t *testing.T, sink *responseAssociationSink) apilog.APIAttemptRecord {
	t.Helper()
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.attempts) != 1 {
		t.Fatalf("canonical attempts = %d, want 1", len(sink.attempts))
	}
	return sink.attempts[0]
}

func assertHeaderValues(t *testing.T, headers map[string][]string, name string, want []string) {
	t.Helper()
	got, present := headers[name]
	if !present {
		t.Fatalf("canonical request headers omit %s: %#v", name, headers)
	}
	if !slicesEqual(got, want) {
		t.Fatalf("canonical request header %s = %#v, want %#v", name, got, want)
	}
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
