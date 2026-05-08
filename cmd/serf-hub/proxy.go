package main

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
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
}

func NewRESTProxy(resolver SessionResolver) *RESTProxy {
	return &RESTProxy{resolver: resolver}
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
	target := &url.URL{
		Scheme: "http",
		Host:   entry.Address,
	}
	rp := httputil.NewSingleHostReverseProxy(target)
	originalDirector := rp.Director
	rp.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Path = "/" + rest
		req.Host = entry.Address
	}
	rp.ServeHTTP(w, r)
}

// splitLivePath parses "/live/<session_id>/<rest...>" into (session_id, rest, true).
// Returns (_, _, false) for paths that don't match the prefix.
func splitLivePath(path string) (sessionID, rest string, ok bool) {
	const prefix = "/live/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}
	tail := path[len(prefix):]
	slash := strings.Index(tail, "/")
	if slash < 0 {
		return "", "", false
	}
	return tail[:slash], tail[slash+1:], true
}
