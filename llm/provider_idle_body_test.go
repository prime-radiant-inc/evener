package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type idleTestRoundTripper func(*http.Request) (*http.Response, error)

func (f idleTestRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProviderIdleNonStreamingBody(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, w := io.Pipe()
		client := ClientWithAdapterTimeout(&http.Client{Transport: idleTestRoundTripper(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: r, Header: make(http.Header), Request: req}, nil
		})}, &AdapterTimeout{StreamRead: time.Minute})
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.invalid", nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		// A finite fallback makes the missing timeout fail an assertion rather than hang.
		go func() { time.Sleep(2 * time.Minute); w.Close() }()
		start := time.Now()
		_, err = io.ReadAll(resp.Body)
		if err == nil {
			t.Error("stalled nonstreaming body returned no idle error")
		}
		if elapsed := time.Since(start); elapsed != time.Minute {
			t.Errorf("idle fired after %v, want 1m", elapsed)
		}
		time.Sleep(time.Minute)
	})
}

func TestProviderIdleContinuedBytesExceedOldTotalCap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, w := io.Pipe()
		at := DefaultAdapterTimeout()
		body := newIdleResponseBody(context.Background(), r, at.StreamRead, nil)
		defer body.Close()
		go func() {
			defer w.Close()
			for range 4 {
				time.Sleep(4 * time.Minute)
				if _, err := w.Write([]byte("x")); err != nil {
					return
				}
			}
		}()
		start := time.Now()
		got, err := io.ReadAll(body)
		if err != nil || string(got) != "xxxx" {
			t.Fatalf("active body=%q, err=%v", got, err)
		}
		if time.Since(start) <= 10*time.Minute {
			t.Fatal("fixture did not exceed previous agent cap")
		}
	})
}

func TestProviderIdleCancellationUnblocksRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		r, w := io.Pipe()
		defer w.Close()
		body := newIdleResponseBody(ctx, r, time.Minute, nil)
		defer body.Close()
		go func() { time.Sleep(time.Second); cancel() }()
		_, err := io.ReadAll(body)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("read error=%v, want caller cancellation", err)
		}
	})
}

type idleCloseCounter struct {
	io.Reader
	closes int
}

func (b *idleCloseCounter) Close() error { b.closes++; return nil }
func TestProviderIdleStopsOnEOFAndClose(t *testing.T) {
	for _, closeEarly := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			inner := &idleCloseCounter{Reader: strings.NewReader("")}
			b := newIdleResponseBody(context.Background(), inner, time.Minute, nil)
			if closeEarly {
				b.Close()
			} else {
				if _, err := io.ReadAll(b); err != nil {
					t.Fatal(err)
				}
			}
			time.Sleep(2 * time.Minute)
			synctest.Wait()
			want := 0
			if closeEarly {
				want = 1
			}
			if inner.closes != want {
				t.Fatalf("body closed %d times, want %d", inner.closes, want)
			}
			if b.expired {
				t.Fatal("idle timer survived EOF/Close")
			}
			b.Close()
		})
	}
}

// Reproduce an already-started AfterFunc waiting for the same mutex a
// successful byte read holds while it extends the deadline. Reset alone does
// not revoke that callback; it must re-check the latest deadline under the lock.
func TestProviderIdleStaleCallbackCannotExpireProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inner := &idleCloseCounter{Reader: strings.NewReader("x")}
		b := newIdleResponseBody(context.Background(), inner, time.Minute, nil)
		defer b.Close()
		// Deliver a callback from an earlier timer generation after newer progress.
		// Calling the owned callback directly avoids mutex scheduling assumptions:
		// synctest does not treat sync.Mutex waits as durably blocked.
		b.expire()
		if inner.closes != 0 {
			t.Fatal("stale callback closed an active body")
		}
		var buf [1]byte
		if _, err := b.Read(buf[:]); err != nil {
			t.Fatalf("active read failed: %v", err)
		}
	})
}
