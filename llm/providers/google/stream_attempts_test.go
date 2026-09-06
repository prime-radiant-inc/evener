package google

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/apilog"
)

// twoGeminiChunks is the opening of a streamGenerateContent body: two text
// chunks, neither carrying the finishReason that ends the stream.
const twoGeminiChunks = "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hel\"}]}}]}\n\n" +
	"data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"lo\"}]}}]}\n\n"

// TestStreamAppendsTheAttemptBeforeTheTerminalEvent pins the ordering the
// adapter's wire captures pinned before the protocols replaced them: the
// canonical attempt record is appended before the consumer sees the
// stream's terminal event, so a caller that reacts to FINISH always finds
// the attempt already in the log.
func TestStreamAppendsTheAttemptBeforeTheTerminalEvent(t *testing.T) {
	srv, _ := protoServer(t, 200, generateSSE)
	sink := &captureSink{}
	ctx := llm.WithAPIAttemptSink(
		llm.WithAPIAttemptGroup(t.Context(), llm.NewAPIAttemptGroup("ag_google_stream_order")),
		sink,
	)
	s, err := (&Protocol{Client: srv.Client()}).Stream(ctx, protoReq(""), protoLive(srv))
	if err != nil {
		t.Fatal(err)
	}
	sawFinish := false
	for ev := range s.Events() {
		if ev.Type == llm.StreamEventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
		if ev.Type == llm.StreamEventFinish {
			sawFinish = true
			if got := len(sink.records()); got != 1 {
				t.Fatalf("attempts visible at finish = %d, want 1", got)
			}
		}
	}
	if !sawFinish {
		t.Fatal("stream ended without a finish event")
	}
	if got := sink.records()[0].Outcome; got != apilog.AttemptSuccess {
		t.Fatalf("attempt outcome = %q, want success", got)
	}
}

// TestStreamClassifiesAnSSEReadTimeoutAsAProviderTimeout pins that a stream
// that stalls mid-body is recorded as a provider timeout — the request
// reached the provider and the response headers arrived, so neither a
// connect nor a request-deadline classification would be honest.
func TestStreamClassifiesAnSSEReadTimeoutAsAProviderTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, w := io.Pipe()
		stall := make(chan struct{})
		defer close(stall)
		defer r.Close()
		client := &http.Client{Transport: idleAttemptRoundTripper(func(req *http.Request) (*http.Response, error) {
			if req.Body != nil {
				io.Copy(io.Discard, req.Body)
				req.Body.Close()
			}
			go func() { defer w.Close(); io.WriteString(w, twoGeminiChunks); <-stall }()
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: r, Request: req}, nil
		})}
		srv := &httptest.Server{URL: "https://example.invalid"}
		sink := &captureSink{}
		ctx := llm.WithAPIAttemptSink(
			llm.WithAPIAttemptGroup(t.Context(), llm.NewAPIAttemptGroup("ag_google_sse_timeout")),
			sink,
		)
		req := protoReq("")
		req.AdapterTimeout = &llm.AdapterTimeout{StreamRead: time.Millisecond}
		s, err := (&Protocol{Client: client}).Stream(ctx, req, protoLive(srv))
		if err != nil {
			t.Fatal(err)
		}
		for range s.Events() { //nolint:revive // Drain to the terminal timeout evidence.
		}
		llm.WaitForPriorAPIAttempts(ctx)
		attempts := sink.records()
		if len(attempts) != 1 {
			t.Fatalf("attempts = %d, want 1", len(attempts))
		}
		if got := attempts[0].Outcome; got != apilog.AttemptProviderTimeout {
			t.Fatalf("SSE-read timeout outcome = %q, want %q", got, apilog.AttemptProviderTimeout)
		}
	})
}

type idleAttemptRoundTripper func(*http.Request) (*http.Response, error)

func (f idleAttemptRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestStreamCompressedWireProgressDoesNotRequireDecodedSSEBytes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var compressed bytes.Buffer
		zw := gzip.NewWriter(&compressed)
		zw.Name = "slow-compressed-header"
		zw.Write([]byte(generateSSE))
		zw.Close()
		left, right := net.Pipe()
		defer right.Close()
		tr := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return left, nil }}
		defer tr.CloseIdleConnections()
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer right.Close()
			req, err := http.ReadRequest(bufio.NewReader(right))
			if err != nil {
				return
			}
			io.Copy(io.Discard, req.Body)
			req.Body.Close()
			fmt.Fprintf(right, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Encoding: gzip\r\nContent-Length: %d\r\n\r\n", compressed.Len())
			for i, b := range compressed.Bytes() {
				if i < 40 {
					time.Sleep(500 * time.Millisecond)
				}
				if _, err := right.Write([]byte{b}); err != nil {
					return
				}
			}
		}()
		req := protoReq("")
		req.AdapterTimeout = &llm.AdapterTimeout{StreamRead: time.Second}
		stream, err := (&Protocol{Client: &http.Client{Transport: tr}}).Stream(context.Background(), req, protoLive(&httptest.Server{URL: "http://provider.invalid"}))
		if err != nil {
			t.Fatal(err)
		}
		finished := false
		for ev := range stream.Events() {
			if ev.Type == llm.StreamEventError {
				t.Errorf("active gzip stream failed: %v", ev.Err)
			}
			if ev.Type == llm.StreamEventFinish {
				finished = true
			}
		}
		if !finished {
			t.Error("active gzip stream did not finish")
		}
		<-done
	})
}
