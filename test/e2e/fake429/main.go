// Command fake429 stands in for a rate-limited LLM provider in end-to-end
// tests: any request whose path ends "/models" (the launch-check /
// model-catalog call) answers 200, and every other request (a completion
// endpoint) answers 429 with a configurable Retry-After. Pointing a
// providers.toml at it drives serf's model-retry path (kata 4zn8, e79v)
// end to end without waiting on a real provider to actually throttle.
//
// Usage:
//
//	fake429 <listen-addr> [retry-after-seconds]
//
// <listen-addr> is host:port to listen on. Use 127.0.0.1:0 to let the
// kernel assign a free port rather than hardcoding one (kata 68fm is the
// same lesson for serf-hub — see docs/agentic-testing.md) and read the
// real port back from the "fake429 listening on ..." line this prints to
// stderr. [retry-after-seconds] is the value sent in the 429 response's
// Retry-After header; it defaults to 8.
//
// See scripts/e2e-ratelimited-provider.sh for the full HOME-isolated hub
// launch recipe this fixture is built for.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
)

const defaultRetryAfterSeconds = "8"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: fake429 <listen-addr> [retry-after-seconds]")
		os.Exit(2)
	}
	retryAfter := defaultRetryAfterSeconds
	if len(os.Args) >= 3 {
		retryAfter = os.Args[2]
	}
	if _, err := strconv.Atoi(retryAfter); err != nil {
		fmt.Fprintf(os.Stderr, "fake429: retry-after-seconds must be a non-negative integer, got %q: %v\n", retryAfter, err)
		os.Exit(2)
	}

	ln, err := listen(os.Args[1])
	if err != nil {
		log.Fatalf("fake429: %v", err)
	}
	// Bind first, THEN log ln.Addr() — the caller may have passed
	// "127.0.0.1:0" and needs the port the kernel actually handed back, not
	// the literal string it asked for.
	log.Printf("fake429 listening on %s", ln.Addr())
	log.Fatal(serve(ln, retryAfter))
}

// listen binds addr ("127.0.0.1:0" lets the kernel pick a free port) and
// returns the listener. The caller must read the real address back from
// ln.Addr() — never from the string passed in. Uses net.ListenConfig (the
// same pattern cmd/serf-hub/main.go's own "-addr 127.0.0.1:0" fix uses)
// rather than the bare net.Listen so the call carries a context.
func listen(addr string) (net.Listener, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}
	return ln, nil
}

// serve runs the fake429 HTTP handler on ln until it errors or ln is closed.
func serve(ln net.Listener, retryAfterSeconds string) error {
	return http.Serve(ln, handler(retryAfterSeconds))
}

// handler implements the two behaviors a rate-limited-provider check needs:
// a request whose path ends "/models" (model discovery / launch-check)
// succeeds, and everything else (the completion endpoints) is rejected 429
// with the configured Retry-After.
func handler(retryAfterSeconds string) http.Handler {
	var hits atomic.Int64
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/models") {
			log.Printf("hit %d %s %s -> 200 (model catalog)", n, r.Method, r.URL.Path)
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"fake-model","object":"model","owned_by":"fake429"}]}`)
			return
		}
		log.Printf("hit %d %s %s -> 429", n, r.Method, r.URL.Path)
		w.Header().Set("Retry-After", retryAfterSeconds)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprint(w, `{"error":{"message":"rate limit exceeded (fake429)","type":"rate_limit_error"}}`)
	})
}
