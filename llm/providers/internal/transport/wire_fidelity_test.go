package transport

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
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

func TestDoWithAPIAttemptsRecordsAllRawBytesAfterGzipDecodeFailure(t *testing.T) {
	rawBody := append(
		[]byte{0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xff},
		bytes.Repeat([]byte{0xff, 0x00, 0x7f, 0x80}, 8*1024)...,
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(rawBody)
	}))
	t.Cleanup(server.Close)

	sink := &responseAssociationSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_corrupt_gzip_wire_fidelity")),
		sink,
	)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, attempt, err := DoWithAPIAttempts(context.Background(), server.Client(), request, func(*http.Request, []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{ProviderInstance: "test"}
	})
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}
	_, decodeErr := io.ReadAll(response.Body)
	if decodeErr == nil {
		t.Fatal("corrupt gzip response unexpectedly decoded")
	}
	attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode, Err: decodeErr}, llm.APITimeoutNone, decodeErr, nil)
	recordedBody, err := apilog.DecodeBody(onlyAttempt(t, sink).Response.Body)
	if err != nil {
		t.Fatalf("decode canonical response: %v", err)
	}
	if !bytes.Equal(recordedBody, rawBody) {
		t.Fatalf("canonical corrupt response byte count = %d, want all %d raw provider bytes", len(recordedBody), len(rawBody))
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
		const credentialTrailer = "X-Configured-Credential-Trailer"
		var received struct {
			transferEncoding []string
			trailer          []string
			credential       []string
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			received.transferEncoding = append([]string(nil), request.TransferEncoding...)
			_, _ = io.Copy(io.Discard, request.Body)
			received.trailer = append([]string(nil), request.Trailer.Values("X-Wire-Trailer")...)
			received.credential = append([]string(nil), request.Trailer.Values(credentialTrailer)...)
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
		request.Trailer = http.Header{
			"X-Wire-Trailer":  []string{"final-value"},
			credentialTrailer: []string{"credential-value"},
		}
		response, attempt, err := DoWithAPIAttempts(context.Background(), server.Client(), request, func(*http.Request, []byte) llm.APIAttemptMeta {
			return llm.APIAttemptMeta{
				ProviderInstance:   "test",
				CredentialMaterial: llm.NewAPILogCredentialMaterial([]string{credentialTrailer}, nil, "credential-value"),
			}
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
		if len(received.credential) == 0 {
			t.Fatal("server did not receive configured credential trailer")
		}
		if strings.Contains(strings.Join(record.Request.Headers["Trailer"], ","), credentialTrailer) {
			t.Fatalf("canonical Trailer declaration retained credential header name: %#v", record.Request.Headers)
		}
		if _, present := record.Request.Headers[credentialTrailer]; present {
			t.Fatalf("canonical request retained credential trailer: %#v", record.Request.Headers)
		}
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

func TestDoWithAPIAttemptsPreservesGzipReadAfterCloseSemantics(t *testing.T) {
	providerBody := []byte(strings.Repeat("provider-response-", 256))
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write(providerBody); err != nil {
		t.Fatalf("gzip provider body: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(compressed.Bytes())
	}))
	t.Cleanup(server.Close)

	sink := &responseAssociationSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_gzip_read_after_close")),
		sink,
	)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, attempt, err := DoWithAPIAttempts(context.Background(), server.Client(), request, func(*http.Request, []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{ProviderInstance: "test"}
	})
	if err != nil {
		t.Fatalf("DoWithAPIAttempts: %v", err)
	}
	first := make([]byte, 1)
	if n, err := response.Body.Read(first); n != 1 || err != nil {
		t.Fatalf("first decoded read = %d, %v; want one byte", n, err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close decoded response: %v", err)
	}
	if n, err := response.Body.Read(make([]byte, 1)); n != 0 || err == nil {
		t.Fatalf("read after decoded response close = %d, %v; want zero bytes and an error", n, err)
	}
	attempt.Complete(llm.APIAttemptResult{StatusCode: response.StatusCode}, llm.APITimeoutNone, nil, nil)
}

func TestDoWithAPIAttemptsPreservesHTTP1GzipCloseBeforeFirstReadSemantics(t *testing.T) {
	providerGzip := gzipBytes(t, []byte("provider response"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(providerGzip)
	}))
	t.Cleanup(server.Close)

	inactiveRequest, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	inactive, err := server.Client().Do(inactiveRequest)
	if err != nil {
		t.Fatalf("inactive request: %v", err)
	}
	if inactive.ProtoMajor != 1 {
		t.Fatalf("inactive response protocol = %q, want HTTP/1", inactive.Proto)
	}
	if err := inactive.Body.Close(); err != nil {
		t.Fatalf("close inactive response: %v", err)
	}
	_, inactiveErr := inactive.Body.Read(make([]byte, 1))
	if inactiveErr == nil {
		t.Fatal("inactive read after close unexpectedly succeeded")
	}

	sink := &responseAssociationSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_http1_close_before_read")),
		sink,
	)
	activeRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	active, attempt, err := DoWithAPIAttempts(context.Background(), server.Client(), activeRequest, func(*http.Request, []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{ProviderInstance: "test"}
	})
	if err != nil {
		t.Fatalf("active request: %v", err)
	}
	if active.ProtoMajor != 1 {
		t.Fatalf("active response protocol = %q, want HTTP/1", active.Proto)
	}
	if err := active.Body.Close(); err != nil {
		t.Fatalf("close active response: %v", err)
	}
	_, activeErr := active.Body.Read(make([]byte, 1))
	if activeErr == nil {
		t.Fatal("active read after close unexpectedly succeeded")
	}
	if activeErr.Error() != inactiveErr.Error() {
		t.Fatalf("HTTP/1 close-before-read error drift = inactive:%q active:%q", inactiveErr, activeErr)
	}
	attempt.Complete(llm.APIAttemptResult{StatusCode: active.StatusCode}, llm.APITimeoutNone, nil, nil)
}

func TestDoWithAPIAttemptsPreWriteFailureDoesNotInventRequestHeaders(t *testing.T) {
	transportErr := errors.New("pre-write transport failure")
	client := &http.Client{Transport: responseAssociationRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, transportErr
	})}
	sink := &responseAssociationSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_prewrite_request_metadata")),
		sink,
	)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://provider.test/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Preliminary-Only", "not-written")
	request.Trailer = http.Header{"X-Unwritten-Trailer": []string{"not-written"}}
	_, attempt, err := DoWithAPIAttempts(context.Background(), client, request, func(*http.Request, []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{ProviderInstance: "test"}
	})
	if !errors.Is(err, transportErr) {
		t.Fatalf("DoWithAPIAttempts error = %v, want %v", err, transportErr)
	}
	attempt.Complete(llm.APIAttemptResult{Err: err}, llm.APITimeoutNone, nil, err)
	if headers := onlyAttempt(t, sink).Request.Headers; len(headers) != 0 {
		t.Fatalf("pre-write failure recorded invented preliminary headers: %#v", headers)
	}
}

func TestDoWithAPIAttemptsPartialTraceWithoutWroteRequestDoesNotHang(t *testing.T) {
	transportErr := errors.New("partial traced transport failure")
	client := &http.Client{Transport: responseAssociationRoundTripper(func(request *http.Request) (*http.Response, error) {
		trace := httptrace.ContextClientTrace(request.Context())
		trace.WroteHeaderField("X-Actually-Written", []string{"visible"})
		trace.WroteHeaders()
		return nil, transportErr
	})}
	sink := &responseAssociationSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_partial_request_trace")),
		sink,
	)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://provider.test/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Preliminary-Only", "not-written")
	request.Trailer = http.Header{"X-Unwritten-Trailer": []string{"not-written"}}
	_, attempt, err := DoWithAPIAttempts(context.Background(), client, request, func(*http.Request, []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{ProviderInstance: "test"}
	})
	if !errors.Is(err, transportErr) {
		t.Fatalf("DoWithAPIAttempts error = %v, want %v", err, transportErr)
	}
	completed := make(chan struct{})
	go func() {
		attempt.Complete(llm.APIAttemptResult{Err: err}, llm.APITimeoutNone, nil, err)
		close(completed)
	}()
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("attempt completion hung waiting for a WroteRequest callback the transport never promised")
	}
	record := onlyAttempt(t, sink)
	assertHeaderValues(t, record.Request.Headers, "X-Actually-Written", []string{"visible"})
	if _, present := record.Request.Headers["X-Preliminary-Only"]; present {
		t.Fatalf("partial trace retained unwritten preliminary header: %#v", record.Request.Headers)
	}
	if _, present := record.Request.Headers["X-Unwritten-Trailer"]; present {
		t.Fatalf("partial trace invented a trailer without WroteRequest evidence: %#v", record.Request.Headers)
	}
}

func TestDoWithAPIAttemptsBodyWriteFailureDoesNotInventTrailers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(server.Close)

	bodyErr := errors.New("request body read failure")
	sink := &responseAssociationSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(context.Background(), llm.NewAPIAttemptGroup("ag_partial_request_body")),
		sink,
	)
	body := io.NopCloser(io.MultiReader(
		strings.NewReader("partially-written-body"),
		iotest.ErrReader(bodyErr),
	))
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, body)
	if err != nil {
		t.Fatal(err)
	}
	request.ContentLength = -1
	request.Trailer = http.Header{"X-Unwritten-Trailer": []string{"not-written"}}
	_, attempt, err := DoWithAPIAttempts(context.Background(), server.Client(), request, func(*http.Request, []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{ProviderInstance: "test"}
	})
	if !errors.Is(err, bodyErr) {
		t.Fatalf("DoWithAPIAttempts error = %v, want request body failure %v", err, bodyErr)
	}
	attempt.Complete(llm.APIAttemptResult{Err: err}, llm.APITimeoutNone, nil, err)

	headers := onlyAttempt(t, sink).Request.Headers
	assertHeaderValues(t, headers, "Trailer", []string{"X-Unwritten-Trailer"})
	if _, present := headers["X-Unwritten-Trailer"]; present {
		t.Fatalf("partial body write invented an unwritten trailer value: %#v", headers)
	}
}

func TestWireRequestMetadataHandlesEveryTraceCallbackSubset(t *testing.T) {
	transportErr := errors.New("transport failure after trace callbacks")
	wroteRequestErr := errors.New("request write failure")
	for mask := 0; mask < 8; mask++ {
		wroteRequestErrors := []error{nil}
		if mask&4 != 0 {
			wroteRequestErrors = append(wroteRequestErrors, wroteRequestErr)
		}
		for _, writeErr := range wroteRequestErrors {
			name := "callbacks_" + strconv.Itoa(mask)
			if writeErr != nil {
				name += "_write_error"
			}
			t.Run(name, func(t *testing.T) {
				request, err := http.NewRequest(http.MethodPost, "https://provider.test/v1", nil)
				if err != nil {
					t.Fatal(err)
				}
				request.Trailer = http.Header{"X-Trace-Trailer": []string{"final-value"}}
				metadata := newWireRequestMetadata(request)
				traced := metadata.trace(request)
				trace := httptrace.ContextClientTrace(traced.Context())
				if mask&1 != 0 {
					trace.WroteHeaderField("X-Actually-Written", []string{"visible"})
				}
				if mask&2 != 0 {
					trace.WroteHeaders()
				}
				if mask&4 != 0 {
					trace.WroteRequest(httptrace.WroteRequestInfo{Err: writeErr})
				}
				metadata.finishRoundTrip(transportErr)
				headers, ok := metadata.snapshot()
				if !ok {
					t.Fatal("finished metadata did not produce a snapshot")
				}
				_, hasWrittenHeader := headers["X-Actually-Written"]
				if hasWrittenHeader != (mask&1 != 0) {
					t.Fatalf("written-header presence = %v, want %v: %#v", hasWrittenHeader, mask&1 != 0, headers)
				}
				_, hasTrailer := headers["X-Trace-Trailer"]
				wantTrailer := mask&4 != 0 && writeErr == nil
				if hasTrailer != wantTrailer {
					t.Fatalf("trailer presence = %v, want %v: %#v", hasTrailer, wantTrailer, headers)
				}
			})
		}
	}
}

func TestDoWithAPIAttemptsPreservesHTTP2StandardCompressionSemantics(t *testing.T) {
	validBody := []byte(strings.Repeat("http2-provider-response-", 256))
	validGzip := gzipBytes(t, validBody)
	corruptGzip := append(
		[]byte{0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xff},
		bytes.Repeat([]byte{0xff, 0x00, 0x7f, 0x80}, 8*1024)...,
	)
	type streamGate struct {
		release chan struct{}
	}
	gates := make(chan streamGate, 2)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("X-Response-Marker", "wire-semantics")
		switch request.URL.Path {
		case "/valid":
			w.Header().Set("Content-Length", strconv.Itoa(len(validGzip)))
			_, _ = w.Write(validGzip)
		case "/corrupt":
			w.Header().Set("Content-Length", strconv.Itoa(len(corruptGzip)))
			_, _ = w.Write(corruptGzip)
		case "/slow-close":
			gate := streamGate{release: make(chan struct{})}
			_, _ = w.Write(validGzip[:len(validGzip)-8])
			w.(http.Flusher).Flush()
			gates <- gate
			<-gate.release
			_, _ = w.Write(validGzip[len(validGzip)-8:])
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	client := server.Client()

	t.Run("headers and read after close", func(t *testing.T) {
		inactive, _, _ := doHTTP2CompressionRequest(t, client, server.URL+"/valid", false)
		active, attempt, _ := doHTTP2CompressionRequest(t, client, server.URL+"/valid", true)
		for name, response := range map[string]*http.Response{"inactive": inactive, "active": active} {
			if response.ProtoMajor != 2 {
				t.Fatalf("%s response protocol = %q, want HTTP/2", name, response.Proto)
			}
			if !response.Uncompressed || response.ContentLength != -1 {
				t.Fatalf("%s compression metadata = uncompressed:%v length:%d", name, response.Uncompressed, response.ContentLength)
			}
			if response.Header.Get("Content-Encoding") != "" || response.Header.Get("Content-Length") != "" {
				t.Fatalf("%s decoded headers retain wire encoding/length: %#v", name, response.Header)
			}
			if response.Header.Get("X-Response-Marker") != "wire-semantics" {
				t.Fatalf("%s response lost ordinary header: %#v", name, response.Header)
			}
		}
		inactiveErr := readAfterClose(t, inactive.Body)
		activeErr := readAfterClose(t, active.Body)
		if !errors.Is(inactiveErr, fs.ErrClosed) {
			t.Fatalf("inactive HTTP/2 read-after-close error = %v, want fs.ErrClosed", inactiveErr)
		}
		if !errors.Is(activeErr, fs.ErrClosed) {
			t.Fatalf("active HTTP/2 read-after-close error = %v, want fs.ErrClosed", activeErr)
		}
		attempt.Complete(llm.APIAttemptResult{StatusCode: active.StatusCode}, llm.APITimeoutNone, nil, nil)
	})

	t.Run("corrupt gzip", func(t *testing.T) {
		inactive, _, _ := doHTTP2CompressionRequest(t, client, server.URL+"/corrupt", false)
		active, attempt, sink := doHTTP2CompressionRequest(t, client, server.URL+"/corrupt", true)
		_, inactiveErr := io.ReadAll(inactive.Body)
		_, activeErr := io.ReadAll(active.Body)
		if inactiveErr == nil || activeErr == nil {
			t.Fatalf("corrupt gzip errors = inactive:%v active:%v", inactiveErr, activeErr)
		}
		if inactiveErr.Error() != activeErr.Error() {
			t.Fatalf("corrupt gzip error drift = inactive:%q active:%q", inactiveErr, activeErr)
		}
		_ = inactive.Body.Close()
		attempt.Complete(llm.APIAttemptResult{StatusCode: active.StatusCode, Err: activeErr}, llm.APITimeoutNone, activeErr, nil)
		recordedBody, err := apilog.DecodeBody(onlyAttempt(t, sink).Response.Body)
		if err != nil {
			t.Fatalf("decode canonical corrupt response: %v", err)
		}
		if !bytes.Equal(recordedBody, corruptGzip) {
			t.Fatalf("canonical HTTP/2 corrupt response byte count = %d, want %d", len(recordedBody), len(corruptGzip))
		}
	})

	t.Run("early close does not drain an open stream", func(t *testing.T) {
		inactive, _, _ := doHTTP2CompressionRequest(t, client, server.URL+"/slow-close", false)
		inactiveGate := <-gates
		readOneDecodedByte(t, inactive.Body)
		closePromptly(t, inactive.Body, inactiveGate.release)

		active, attempt, _ := doHTTP2CompressionRequest(t, client, server.URL+"/slow-close", true)
		activeGate := <-gates
		readOneDecodedByte(t, active.Body)
		closePromptly(t, active.Body, activeGate.release)
		attempt.Complete(llm.APIAttemptResult{StatusCode: active.StatusCode}, llm.APITimeoutNone, nil, nil)
	})
}

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
	return append([]byte(nil), compressed.Bytes()...)
}

func doHTTP2CompressionRequest(t *testing.T, client *http.Client, endpoint string, active bool) (*http.Response, *APIAttemptCapture, *responseAssociationSink) {
	t.Helper()
	requestCtx := context.Background()
	sink := &responseAssociationSink{}
	if active {
		requestCtx = llm.WithAPIAttemptSink(
			llm.WithAPIAttemptGroup(requestCtx, llm.NewAPIAttemptGroup("ag_http2_semantics_"+strings.TrimPrefix(endpoint, "https://"))),
			sink,
		)
	}
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !active {
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("inactive HTTP/2 request: %v", err)
		}
		return response, nil, sink
	}
	response, attempt, err := DoWithAPIAttempts(context.Background(), client, request, func(*http.Request, []byte) llm.APIAttemptMeta {
		return llm.APIAttemptMeta{ProviderInstance: "test"}
	})
	if err != nil {
		t.Fatalf("active HTTP/2 request: %v", err)
	}
	return response, attempt, sink
}

func readAfterClose(t *testing.T, body io.ReadCloser) error {
	t.Helper()
	if n, err := body.Read(make([]byte, 1)); n != 1 || err != nil {
		t.Fatalf("first response read = %d, %v; want one byte", n, err)
	}
	if err := body.Close(); err != nil {
		t.Fatalf("close response: %v", err)
	}
	if n, err := body.Read(make([]byte, 1)); n != 0 || err == nil {
		t.Fatalf("read after close = %d, %v; want zero bytes and an error", n, err)
	} else {
		return err
	}
	return nil
}

func readOneDecodedByte(t *testing.T, body io.Reader) {
	t.Helper()
	if n, err := body.Read(make([]byte, 1)); n != 1 || err != nil {
		t.Fatalf("decoded response read = %d, %v; want one byte", n, err)
	}
}

func closePromptly(t *testing.T, body io.Closer, release chan struct{}) {
	t.Helper()
	closeDone := make(chan error, 1)
	go func() { closeDone <- body.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("close partial HTTP/2 response: %v", err)
		}
		close(release)
	case <-time.After(time.Second):
		close(release)
		<-closeDone
		t.Fatal("HTTP/2 response Close waited for the provider stream to end")
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
