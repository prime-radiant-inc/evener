package llm

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type idleCloseObserver struct {
	io.ReadCloser
	beforeClose func()
}

func (b *idleCloseObserver) Close() error { b.beforeClose(); return b.ReadCloser.Close() }
func TestProviderIdleRealHTTPUnblocksStalledBody(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clientConn, serverConn := net.Pipe()
		defer serverConn.Close()
		tr := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return clientConn, nil }}
		defer tr.CloseIdleConnections()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer serverConn.Close()
			req, err := http.ReadRequest(bufio.NewReader(serverConn))
			if err != nil {
				return
			}
			req.Body.Close()
			fmt.Fprint(serverConn, "HTTP/1.1 200 OK\r\nContent-Length: 10\r\n\r\n")
			<-ctx.Done()
		}()
		observed := false
		base := idleTestRoundTripper(func(req *http.Request) (*http.Response, error) {
			resp, err := tr.RoundTrip(req)
			if err != nil {
				return resp, err
			}
			resp.Body = &idleCloseObserver{ReadCloser: resp.Body, beforeClose: func() {
				if !observed {
					observed = true
					if req.Context().Err() == nil {
						t.Error("idle close did not cancel HTTP request before closing a read-locked body")
					}
					cancel()
				}
			}}
			return resp, nil
		})
		client := ClientWithAdapterTimeout(&http.Client{Transport: base}, &AdapterTimeout{StreamRead: time.Minute})
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://provider.invalid/", nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err == nil {
			t.Error("stalled HTTP response did not return an error")
		}
		<-done
	})
}

func TestProviderIdleTracksCompressedWireBytes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var compressed bytes.Buffer
		payload := make([]byte, 300<<10)
		rand.New(rand.NewSource(1)).Read(payload)
		zw := gzip.NewWriter(&compressed)
		zw.Name = strings.Repeat("n", 20)
		zw.Write(payload)
		zw.Close()
		if compressed.Len() <= 256<<10 {
			t.Fatal("fixture must exceed HTTP post-close drain limit")
		}
		clientConn, serverConn := net.Pipe()
		defer serverConn.Close()
		tr := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return clientConn, nil }}
		defer tr.CloseIdleConnections()
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer serverConn.Close()
			req, err := http.ReadRequest(bufio.NewReader(serverConn))
			if err != nil {
				return
			}
			req.Body.Close()
			fmt.Fprintf(serverConn, "HTTP/1.1 200 OK\r\nContent-Encoding: gzip\r\nContent-Length: %d\r\n\r\n", compressed.Len())
			for i, b := range compressed.Bytes() {
				if i < 40 {
					time.Sleep(500 * time.Millisecond)
				}
				if _, err := serverConn.Write([]byte{b}); err != nil {
					return
				}
			}
		}()
		client := ClientWithAdapterTimeout(&http.Client{Transport: tr}, &AdapterTimeout{StreamRead: time.Second})
		resp, err := client.Get("http://provider.invalid/")
		if err != nil {
			t.Fatal(err)
		}
		// Ensure a broken implementation cannot deadlock while closing a Read-locked HTTP body.
		fallback := time.AfterFunc(time.Minute, func() { serverConn.Close() })
		defer fallback.Stop()
		got, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || !bytes.Equal(got, payload) {
			t.Errorf("active compressed response length=%d, err=%v", len(got), err)
		}
		if !resp.Uncompressed || resp.Header.Get("Content-Encoding") != "" {
			t.Error("automatic gzip response semantics changed")
		}
		<-done
	})
}

func TestProviderConnectBoundsTLSHandshake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clientConn, serverConn := net.Pipe()
		defer serverConn.Close()
		tr := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) { return clientConn, nil }}
		defer tr.CloseIdleConnections()
		done := make(chan struct{})
		go func() { defer close(done); io.Copy(io.Discard, serverConn) }()
		fallback := time.AfterFunc(2*time.Second, func() { serverConn.Close() })
		defer fallback.Stop()
		client := ClientWithAdapterTimeout(&http.Client{Transport: tr}, &AdapterTimeout{Connect: time.Second})
		start := time.Now()
		_, err := client.Get("https://provider.invalid/")
		if err == nil {
			t.Error("stalled TLS handshake succeeded")
		}
		if elapsed := time.Since(start); elapsed != time.Second {
			t.Errorf("TLS handshake unblocked after %v, want 1s", elapsed)
		}
		serverConn.Close()
		<-done
	})
}

func TestProviderConnectPreservesShorterTLSHandshake(t *testing.T) {
	tr := configuredAdapterTransport(&http.Transport{TLSHandshakeTimeout: time.Millisecond}, &AdapterTimeout{Connect: time.Second})
	if tr.TLSHandshakeTimeout != time.Millisecond {
		t.Fatalf("TLS timeout=%v", tr.TLSHandshakeTimeout)
	}
}
