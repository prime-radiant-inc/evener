package main

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
)

// SessionResolver resolves a session_id to a live daemon entry.
// Roster implements this interface via its Find method.
type SessionResolver interface {
	Find(sessionID string) (LiveEntry, bool)
}

// RESTProxy serves the /live/<session_id>/<endpoint> family of routes by
// reverse-proxying to the daemon's matching endpoint.
type RESTProxy struct {
	resolver SessionResolver

	mu      sync.RWMutex
	proxies map[string]*httputil.ReverseProxy // address -> proxy
}

func NewRESTProxy(resolver SessionResolver) *RESTProxy {
	return &RESTProxy{
		resolver: resolver,
		proxies:  make(map[string]*httputil.ReverseProxy),
	}
}

// proxyFor returns a cached reverse proxy for the given daemon address,
// creating one on first use. Caching allows HTTP keepalive reuse.
func (p *RESTProxy) proxyFor(address string) *httputil.ReverseProxy {
	p.mu.RLock()
	rp, ok := p.proxies[address]
	p.mu.RUnlock()
	if ok {
		return rp
	}
	target := &url.URL{Scheme: "http", Host: address}
	rp = httputil.NewSingleHostReverseProxy(target)
	p.mu.Lock()
	p.proxies[address] = rp
	p.mu.Unlock()
	return rp
}

func (p *RESTProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sessID, rest, ok := splitLivePath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	entry, found := p.resolver.Find(sessID)
	if !found {
		http.NotFound(w, r)
		return
	}
	rp := p.proxyFor(entry.Address)
	r.URL.Path = "/" + rest
	r.Host = entry.Address
	rp.ServeHTTP(w, r)
}

// splitLivePath parses "/live/<session_id>/<rest...>" into (session_id, rest, true).
// When there is no trailing slash (e.g. "/live/<id>"), rest is "" and ok is true.
// Returns (_, _, false) for paths that don't match the prefix.
func splitLivePath(path string) (sessionID, rest string, ok bool) {
	const prefix = "/live/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}
	tail := path[len(prefix):]
	slash := strings.Index(tail, "/")
	if slash < 0 {
		return tail, "", true
	}
	return tail[:slash], tail[slash+1:], true
}

// SSEProxy serves /live/<session_id>/events by terminating the browser
// SSE connection and re-emitting the daemon's event stream byte-for-byte.
//
// Forwards Last-Event-ID so the daemon's broadcaster can replay missed
// events from its ring buffer when a browser reconnects.
type SSEProxy struct {
	resolver SessionResolver
}

func NewSSEProxy(resolver SessionResolver) *SSEProxy {
	return &SSEProxy{resolver: resolver}
}

func (p *SSEProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sessID, rest, ok := splitLivePath(r.URL.Path)
	if !ok || rest != "events" {
		http.NotFound(w, r)
		return
	}
	entry, found := p.resolver.Find(sessID)
	if !found {
		http.NotFound(w, r)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Open upstream with browser's Last-Event-ID and the request context.
	upstreamURL := "http://" + entry.Address + "/events"
	upstreamReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstreamURL, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if id := r.Header.Get("Last-Event-ID"); id != "" {
		upstreamReq.Header.Set("Last-Event-ID", id)
	}
	upstreamReq.Header.Set("Accept", "text/event-stream")

	// Disable client-side timeouts; SSE is a long-lived connection.
	client := &http.Client{Timeout: 0}
	resp, err := client.Do(upstreamReq)
	if err != nil {
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		http.Error(w, "upstream returned non-200", http.StatusBadGateway)
		return
	}

	// Mirror upstream content-type (or set if absent).
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Stream bytes through with periodic flushes.
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			flusher.Flush()
		}
		if err != nil {
			return
		}
	}
}
