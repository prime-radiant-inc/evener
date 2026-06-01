package hubcore

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
	r.Header.Del("Origin")
	SetDaemonAuthorization(r.Header, entry.HubToken)
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
